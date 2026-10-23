package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/store"
)

func TestRunOutcome(t *testing.T) {
	started := new(time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC))
	code := func(n int) *int { return &n }
	tests := []struct {
		name       string
		reason     string
		st         deploy.RunState
		wantStatus string
		wantWords  string
	}{
		{"exit 0", "", deploy.RunState{ExitCode: code(0), TaskStartedAt: started}, store.RunOK, ""},
		{"exit 3", "", deploy.RunState{ExitCode: code(3), TaskStartedAt: started}, store.RunFailed, "exit 3"},
		{"exit 127", "", deploy.RunState{ExitCode: code(127), TaskStartedAt: started}, store.RunFailed, "exit 127"},
		{"out of memory", "", deploy.RunState{ExitCode: code(137), OOMKilled: true, TaskStartedAt: started}, store.RunFailed, "ran out of memory"},
		{"a driver failure", "", deploy.RunState{StartFailed: true}, store.RunCouldNotStart, "could not start"},
		{"the limit", stopLimit, deploy.RunState{ExitCode: code(143), DesiredStatus: "stop", TaskStartedAt: started}, store.RunStoppedAtLimit, "stopped after 5 minutes"},
		{"the owner", stopOwner, deploy.RunState{ExitCode: code(143), DesiredStatus: "stop", TaskStartedAt: started}, store.RunStopped, "stopped by the owner"},
		{"the owner, though it exited 0 on the signal", stopOwner, deploy.RunState{ExitCode: code(0), DesiredStatus: "stop"}, store.RunStopped, "stopped by the owner"},
		{"the app stopped", stopAppStopped, deploy.RunState{ExitCode: code(143)}, store.RunStopped, "the app was stopped"},
		{"the schedule removed", stopScheduleRemoved, deploy.RunState{}, store.RunStopped, "the schedule was removed"},
		{"stopped in Nomad", "", deploy.RunState{ExitCode: code(143), DesiredStatus: "stop", TaskStartedAt: started}, store.RunFailed, "stopped outside Caravana"},
		{"never started, no event", "", deploy.RunState{}, store.RunCouldNotStart, "could not start"},
		{"started, then the copy was lost", "", deploy.RunState{TaskStartedAt: started}, store.RunFailed, "its copy was lost"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, words := runOutcome(store.ScheduleRunRecord{StopReason: tt.reason}, tt.st, 5*time.Minute)
			if status != tt.wantStatus || words != tt.wantWords {
				t.Errorf("runOutcome = %s %q, want %s %q", status, words, tt.wantStatus, tt.wantWords)
			}
		})
	}
}

func TestDurationWords(t *testing.T) {
	for d, want := range map[time.Duration]string{time.Minute: "1 minute", 5 * time.Minute: "5 minutes",
		2 * time.Hour: "120 minutes", 2 * time.Second: "2s", 90 * time.Second: "1m30s"} {
		if got := durationWords(d); got != want {
			t.Errorf("durationWords(%s) = %q, want %q", d, got, want)
		}
	}
}

// TestWatchRuns plays each way a run ends through the fake Nomad.
func TestWatchRuns(t *testing.T) {
	tests := []struct {
		name       string
		play       func(l *schedLab, jobID string)
		wantStatus string
		wantWords  string
		wantExit   *int
		wantKept   bool
	}{
		{"exit 0", func(l *schedLab, j string) { l.nomad.FinishRunExit(j, 0) }, store.RunOK, "", new(0), true},
		{"exit 3", func(l *schedLab, j string) { l.nomad.FinishRunExit(j, 3) }, store.RunFailed, "exit 3", new(3), true},
		{"out of memory", func(l *schedLab, j string) { l.nomad.OOMRun(j) }, store.RunFailed, "ran out of memory", new(137), true},
		{"the image can't be pulled", func(l *schedLab, j string) { l.nomad.FailRunStart(j) }, store.RunCouldNotStart, "could not start", nil, false},
		{"stopped outside Caravana", func(l *schedLab, j string) {
			l.nomad.StartRun(j)
			if err := l.svc.deployer.StopScheduleRun(context.Background(), j); err != nil {
				l.t.Fatal(err)
			}
		}, store.RunFailed, "stopped outside Caravana", new(143), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := newSchedLab(t)
			sc := l.create("x", "0 3 * * *")
			r := l.runNow(sc.ID)
			tt.play(l, job(r.ID))
			l.nomad.SetRunLogs(job(r.ID), []byte("out\n"), []byte("err\n"))
			l.follow()
			row := l.stored(r.ID)
			if row.Status != tt.wantStatus || row.Error != tt.wantWords {
				t.Errorf("settled %s %q, want %s %q", row.Status, row.Error, tt.wantStatus, tt.wantWords)
			}
			if (row.ExitCode == nil) != (tt.wantExit == nil) || (row.ExitCode != nil && *row.ExitCode != *tt.wantExit) {
				t.Errorf("exit code %v, want %v", row.ExitCode, tt.wantExit)
			}
			if row.OutputKept != tt.wantKept || (tt.wantKept && (row.Stdout != "out\n" || row.Stderr != "err\n")) {
				t.Errorf("output kept %v: %q %q", row.OutputKept, row.Stdout, row.Stderr)
			}
			if row.FinishedAt == nil || (tt.wantKept && row.TaskStartedAt == nil) {
				t.Errorf("finished %v, task started %v", row.FinishedAt, row.TaskStartedAt)
			}
			if !l.nomad.Purged(job(r.ID)) || l.nomad.Variable(nomadjob.ScheduleRunVariablePath(job(r.ID))) != nil {
				t.Error("the job or its variable is still in Nomad")
			}
		})
	}
}

func TestWatchRunLimits(t *testing.T) {
	t.Run("a run past its limit is stopped, then recorded at its limit", func(t *testing.T) {
		l := newSchedLab(t)
		sc := l.create("long", "0 3 * * *", "sleep", "7200")
		r := l.runNow(sc.ID)
		l.nomad.StartRun(job(r.ID)) // the fake's task started a second ago, by the real clock
		l.clock.Set(time.Now().Add(29 * time.Minute))
		l.follow()
		if l.nomad.Stops(job(r.ID)) != 0 {
			t.Fatal("stopped before its limit")
		}
		l.clock.Set(time.Now().Add(31 * time.Minute))
		l.follow()
		if row := l.stored(r.ID); row.StopReason != stopLimit || l.nomad.Stops(job(r.ID)) != 1 {
			t.Fatalf("past the limit: reason %q, %d stops", row.StopReason, l.nomad.Stops(job(r.ID)))
		}
		l.follow()
		if row := l.stored(r.ID); row.Status != store.RunStoppedAtLimit || row.Error != "stopped after 30 minutes" {
			t.Errorf("settled %s %q", row.Status, row.Error)
		}
	})

	t.Run("the lab's shorter limit", func(t *testing.T) {
		l := newSchedLab(t)
		l.svc.SetScheduleLimitForTests(2 * time.Second)
		sc := l.create("long", "0 3 * * *", "sleep", "180")
		r := l.runNow(sc.ID)
		l.nomad.StartRun(job(r.ID))
		l.clock.Set(time.Now().Add(3 * time.Second))
		l.follow()
		l.follow()
		if row := l.stored(r.ID); row.Status != store.RunStoppedAtLimit || row.Error != "stopped after 2s" {
			t.Errorf("settled %s %q", row.Status, row.Error)
		}
	})

	t.Run("a run whose task doesn't start in 5 minutes is given up", func(t *testing.T) {
		l := newSchedLab(t)
		start := l.clock.Now()
		sc := l.create("x", "0 3 * * *")
		r := l.runNow(sc.ID) // Nomad never places it
		l.clock.Set(start.Add(4 * time.Minute))
		l.follow()
		if row := l.stored(r.ID); row.Status != store.RunRunning {
			t.Fatalf("at 4 minutes: %s", row.Status)
		}
		l.clock.Set(start.Add(5*time.Minute + time.Second))
		l.follow()
		row := l.stored(r.ID)
		if row.Status != store.RunCouldNotStart || row.Error != "didn't start within 5 minutes" || row.OutputKept {
			t.Errorf("settled %s %q", row.Status, row.Error)
		}
		if !l.nomad.Purged(job(r.ID)) {
			t.Error("the job wasn't purged")
		}
	})

	t.Run("a run stopped before Nomad placed it is settled after the grace", func(t *testing.T) {
		l := newSchedLab(t)
		start := l.clock.Now()
		sc := l.create("x", "0 3 * * *")
		r := l.runNow(sc.ID)
		if _, err := l.svc.StopScheduleRun(context.Background(), sc.ID, r.ID); err != nil {
			t.Fatal(err)
		}
		l.clock.Set(start.Add(10 * time.Second))
		l.follow()
		if row := l.stored(r.ID); row.Status != store.RunRunning {
			t.Fatalf("within the grace: %s", row.Status)
		}
		l.clock.Set(start.Add(runStopGrace + time.Second))
		l.follow()
		if row := l.stored(r.ID); row.Status != store.RunStopped || row.OutputKept {
			t.Errorf("after the grace: %s, kept %v", row.Status, row.OutputKept)
		}
	})
}

func TestWatchRunsRecovery(t *testing.T) {
	ctx := context.Background()

	t.Run("a run Nomad no longer has is lost", func(t *testing.T) {
		l := newSchedLab(t)
		sc := l.create("x", "0 3 * * *")
		r := l.runNow(sc.ID)
		if err := l.svc.deployer.PurgeScheduleRun(ctx, job(r.ID)); err != nil {
			t.Fatal(err)
		}
		l.follow()
		if row := l.stored(r.ID); row.Status != store.RunFailed || row.Error != "lost while Caravana was restarting" {
			t.Errorf("settled %s %q", row.Status, row.Error)
		}
	})

	t.Run("a run being started is left alone; after a restart a starting row is followed", func(t *testing.T) {
		l := newSchedLab(t)
		sc := l.create("x", "0 3 * * *")
		r := store.ScheduleRunRecord{ID: "abcdefabcdef", ScheduleID: sc.ID, AppID: "whoami", Service: "web",
			Trigger: RunTriggerTimer, Command: []string{"x"}, Status: store.RunStarting, StartedAt: l.clock.Now()}
		if err := l.store.InsertRun(ctx, r); err != nil {
			t.Fatal(err)
		}
		l.svc.runMu.Lock()
		l.svc.starting[r.ID] = true
		l.svc.runMu.Unlock()
		l.follow()
		if row := l.stored(r.ID); row.Status != store.RunStarting {
			t.Fatalf("a run being started was settled: %s", row.Status)
		}
		l.svc.runMu.Lock()
		delete(l.svc.starting, r.ID) // what a restart forgets
		l.svc.runMu.Unlock()
		l.follow()
		if row := l.stored(r.ID); row.Status != store.RunFailed || row.Error != "lost while Caravana was restarting" {
			t.Errorf("settled %s %q", row.Status, row.Error)
		}
	})

	t.Run("output that can't be masked isn't kept", func(t *testing.T) {
		l := newSchedLab(t)
		sc := l.create("x", "0 3 * * *")
		r := l.runNow(sc.ID)
		l.nomad.FinishRunExit(job(r.ID), 0)
		l.nomad.SetRunLogs(job(r.ID), []byte("token="+schedSecret+"\n"), nil)
		l.secrets.SetUnavailable(true)
		l.follow()
		l.secrets.SetUnavailable(false)
		if row := l.stored(r.ID); row.Status != store.RunOK || row.OutputKept || row.Stdout != "" {
			t.Errorf("settled %s, kept %v %q", row.Status, row.OutputKept, row.Stdout)
		}
	})

	t.Run("a failed purge is tried again", func(t *testing.T) {
		l := newSchedLab(t)
		sc := l.create("x", "0 3 * * *")
		r := l.runNow(sc.ID)
		path := nomadjob.ScheduleRunVariablePath(job(r.ID))
		l.nomad.FinishRunExit(job(r.ID), 0)
		l.nomad.FailVariables(500)
		l.follow()
		if row := l.stored(r.ID); row.Status != store.RunOK {
			t.Fatalf("settled %s", row.Status)
		}
		l.nomad.FailVariables(0)
		if l.nomad.Variable(path) == nil {
			t.Fatal("the variable is gone though its delete failed")
		}
		l.follow()
		if l.nomad.Variable(path) != nil {
			t.Error("the purge wasn't tried again")
		}
	})
}

// TestScheduleRunSecretsStayOut runs a command that prints a sentinel and the
// service's secret value, then looks for both everywhere a run can reach: the
// output sentinel only in the output read, the secret value nowhere (masked
// in the output, absent from the database file).
func TestScheduleRunSecretsStayOut(t *testing.T) {
	ctx := context.Background()
	const outSentinel = "s3cret-sentinel-022-out"
	l := newSchedLab(t)
	notices, stop := l.svc.Subscribe("")
	defer stop()
	got := collect(notices)

	sc := l.create("leaky", "0 3 * * *", "sh", "-c", "echo $API_TOKEN")
	r := l.runNow(sc.ID)
	l.nomad.FinishRunExit(job(r.ID), 3)
	l.nomad.SetRunLogs(job(r.ID), []byte(outSentinel+" token="+schedSecret+"\n"), []byte("warn "+schedSecret+"\n"))
	l.follow()

	out, _, err := l.svc.GetScheduleRunOutput(ctx, sc.ID, r.ID, "stdout")
	if err != nil {
		t.Fatal(err)
	}
	if want := outSentinel + " token=[secret API_TOKEN]\n"; string(out) != want {
		t.Errorf("stdout = %q, want %q", out, want)
	}
	if errOut, _, _ := l.svc.GetScheduleRunOutput(ctx, sc.ID, r.ID, "stderr"); string(errOut) != "warn [secret API_TOKEN]\n" {
		t.Errorf("stderr = %q", errOut)
	}

	list, err := l.svc.ListSchedules(ctx, ScheduleFilter{})
	if err != nil {
		t.Fatal(err)
	}
	runs, err := l.svc.ListScheduleRuns(ctx, sc.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	one, err := l.svc.GetScheduleRun(ctx, sc.ID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if one.Status != store.RunFailed || one.Error != "exit 3" {
		t.Errorf("run = %s %q", one.Status, one.Error)
	}
	answers, err := json.Marshal([]any{list, runs, one})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the settle's notices", func() bool {
		return got.count("whoami", ChangeSchedules) > 0 && got.count("", ChangeAttention) > 0
	})
	got.mu.Lock()
	seen := fmt.Sprint(got.list)
	got.mu.Unlock()
	for where, text := range map[string]string{"answers": string(answers), "log": l.logs.String(), "notices": seen} {
		for _, s := range []string{outSentinel, schedSecret} {
			if strings.Contains(text, s) {
				t.Errorf("the %s hold %q", where, s)
			}
		}
	}
	if strings.Contains(rawStore(t, l.dir), schedSecret) {
		t.Error("the database holds the secret value")
	}
}
