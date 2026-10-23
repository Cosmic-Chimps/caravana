package service

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/store"
)

func TestRunScheduleNow(t *testing.T) {
	ctx := context.Background()
	l := newSchedLab(t)
	live, err := l.store.LatestHealthyDeployment(ctx, "whoami")
	if err != nil {
		t.Fatal(err)
	}
	sc := l.create("report", "0 3 * * *", "sh", "-c", "echo done")

	r := l.runNow(sc.ID)
	if r.Status != store.RunRunning || r.Trigger != RunTriggerOwner || r.SlotAt != nil ||
		r.DeploymentID == nil || *r.DeploymentID != live.Deployment.ID || r.JobVersion == nil ||
		!slices.Equal(r.Command, []string{"sh", "-c", "echo done"}) {
		t.Fatalf("run = %+v", r)
	}
	jobID := job(r.ID)
	j := l.nomad.Job(jobID)
	if j == nil {
		t.Fatalf("no job %s in Nomad", jobID)
	}
	task := j.TaskGroups[0].Tasks[0]
	if j.Meta["caravana.schedule"] != sc.ID || j.Meta["caravana.run"] != r.ID || task.Name != "web" ||
		task.Config["image"] != live.Deployment.Digests["web"] || *task.Resources.MemoryMB != 256 {
		t.Errorf("job meta %v, task %s, config %v", j.Meta, task.Name, task.Config)
	}
	path := nomadjob.ScheduleRunVariablePath(jobID)
	if got := l.nomad.Variable(path); got["API_TOKEN"] != schedSecret {
		t.Errorf("the run's variable = %v keys, want the service's secret", len(got))
	}
	if row := l.stored(r.ID); row.Status != store.RunRunning {
		t.Errorf("row = %s", row.Status)
	}

	t.Run("a second run while one goes is refused when the schedule skips", func(t *testing.T) {
		if _, err := l.svc.RunScheduleNow(ctx, sc.ID); !errors.Is(err, ErrRunGoing) {
			t.Fatalf("RunScheduleNow = %v, want ErrRunGoing", err)
		}
		if n := len(l.runs(sc.ID)); n != 1 {
			t.Errorf("%d rows, want 1: a refused owner run writes nothing", n)
		}
	})

	t.Run("the run ends, is recorded and its job and variable are gone", func(t *testing.T) {
		l.nomad.FinishRunExit(jobID, 0)
		l.nomad.SetRunLogs(jobID, []byte("done\n"), nil)
		l.follow()
		row := l.stored(r.ID)
		if row.Status != store.RunOK || row.ExitCode == nil || *row.ExitCode != 0 || !row.OutputKept || row.Stdout != "done\n" {
			t.Errorf("row = %+v", row)
		}
		if !l.nomad.Purged(jobID) || l.nomad.Variable(path) != nil {
			t.Errorf("purged %v, variable %v", l.nomad.Purged(jobID), l.nomad.Variable(path) != nil)
		}
	})

	t.Run("overlap allowed: three at once, not four", func(t *testing.T) {
		skip := false
		many, err := l.svc.CreateSchedule(ctx, NewSchedule{App: "whoami", Name: "many", Command: []string{"sleep", "60"},
			Cron: "0 3 * * *", SkipIfRunning: &skip})
		if err != nil {
			t.Fatal(err)
		}
		for range 3 {
			l.runNow(many.ID)
		}
		if _, err := l.svc.RunScheduleNow(ctx, many.ID); !errors.Is(err, ErrRunGoing) {
			t.Fatalf("a fourth run = %v, want ErrRunGoing", err)
		}
	})
}

func TestRunScheduleNowRefusals(t *testing.T) {
	ctx := context.Background()

	t.Run("a stopped app", func(t *testing.T) {
		l := newSchedLab(t)
		sc := l.create("x", "0 3 * * *")
		if err := l.store.SetAppStopped(ctx, "whoami", new(l.clock.Now())); err != nil {
			t.Fatal(err)
		}
		if _, err := l.svc.RunScheduleNow(ctx, sc.ID); !errors.Is(err, ErrAppStopped) {
			t.Errorf("RunScheduleNow = %v, want ErrAppStopped", err)
		}
	})
	t.Run("a deploy going", func(t *testing.T) {
		l := newSchedLab(t)
		sc := l.create("x", "0 3 * * *")
		if _, err := l.store.CreateDeployment(ctx, "whoami", app.Deployment{ID: "dep-pending", Trigger: app.TriggerManual,
			Digests: map[string]string{"web": "traefik/whoami@" + whoamiDigest}}, nil, nil, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := l.svc.RunScheduleNow(ctx, sc.ID); !errors.Is(err, ErrNoLiveDeployment) {
			t.Errorf("RunScheduleNow = %v, want ErrNoLiveDeployment", err)
		}
		if n := len(l.runs(sc.ID)); n != 0 {
			t.Errorf("%d rows written", n)
		}
	})
	t.Run("Nomad refuses the job", func(t *testing.T) {
		l := newSchedLab(t)
		sc := l.create("x", "0 3 * * *")
		l.nomad.RejectNext(400, "job is invalid")
		r, err := l.svc.RunScheduleNow(ctx, sc.ID)
		if err != nil {
			t.Fatalf("RunScheduleNow = %v, want the run back without an error", err)
		}
		if r.Status != store.RunCouldNotStart || r.Error != "Nomad refused the job" {
			t.Errorf("run = %s %q", r.Status, r.Error)
		}
		if row := l.stored(r.ID); row.Status != store.RunCouldNotStart || row.Error != "Nomad refused the job" {
			t.Errorf("row = %s %q", row.Status, row.Error)
		}
		if v := l.nomad.Variable(nomadjob.ScheduleRunVariablePath(job(r.ID))); v != nil {
			t.Error("the refused run's variable is still in Nomad")
		}
	})
	t.Run("Nomad can't be reached", func(t *testing.T) {
		l := newSchedLab(t)
		sc := l.create("x", "0 3 * * *")
		l.nomad.Down()
		defer l.nomad.Up()
		r, err := l.svc.RunScheduleNow(ctx, sc.ID)
		if err == nil {
			t.Fatal("RunScheduleNow succeeded with Nomad down")
		}
		if row := l.stored(r.ID); row.Status != store.RunCouldNotStart || row.Error != "Nomad couldn't be reached" {
			t.Errorf("row = %s %q", row.Status, row.Error)
		}
	})
}

func TestStopScheduleRun(t *testing.T) {
	ctx := context.Background()
	l := newSchedLab(t)
	sc := l.create("long", "0 3 * * *", "sleep", "600")
	r := l.runNow(sc.ID)
	l.nomad.StartRun(job(r.ID))

	got, err := l.svc.StopScheduleRun(ctx, sc.ID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.RunRunning || l.nomad.Stops(job(r.ID)) != 1 {
		t.Errorf("after Stop: %s, %d stops", got.Status, l.nomad.Stops(job(r.ID)))
	}
	if row := l.stored(r.ID); row.StopReason != stopOwner || row.StopRequestedAt == nil {
		t.Errorf("intent = %q at %v", row.StopReason, row.StopRequestedAt)
	}
	l.follow()
	row := l.stored(r.ID)
	if row.Status != store.RunStopped || row.Error != "stopped by the owner" || row.ExitCode == nil || *row.ExitCode != 143 {
		t.Errorf("settled = %s %q %v", row.Status, row.Error, row.ExitCode)
	}
	if !l.nomad.Purged(job(r.ID)) {
		t.Error("the stopped run's job wasn't purged")
	}
	if _, err := l.svc.StopScheduleRun(ctx, sc.ID, r.ID); !errors.Is(err, ErrRunNotRunning) {
		t.Errorf("stopping it again = %v, want ErrRunNotRunning", err)
	}
	if _, err := l.svc.StopScheduleRun(ctx, sc.ID, "nope"); !errors.Is(err, ErrRunNotFound) {
		t.Errorf("an unknown run = %v, want ErrRunNotFound", err)
	}
}

func TestStopAppStopsRuns(t *testing.T) {
	ctx := context.Background()
	l := newSchedLab(t)
	sc := l.create("long", "0 3 * * *", "sleep", "600")
	r := l.runNow(sc.ID)
	l.nomad.StartRun(job(r.ID))
	if _, err := l.svc.StopApp(ctx, "whoami", app.TriggerManual); err != nil {
		t.Fatal(err)
	}
	if l.nomad.Stops(job(r.ID)) != 1 {
		t.Errorf("the run's job was stopped %d times, want 1", l.nomad.Stops(job(r.ID)))
	}
	l.follow()
	if row := l.stored(r.ID); row.Status != store.RunStopped || row.StopReason != stopAppStopped || row.Error != "the app was stopped" {
		t.Errorf("settled = %s %q (%s)", row.Status, row.Error, row.StopReason)
	}
}

func TestDeleteScheduleEndsItsRuns(t *testing.T) {
	ctx := context.Background()
	l := newSchedLab(t)
	sc := l.create("long", "0 3 * * *", "sleep", "600")
	r := l.runNow(sc.ID)
	l.nomad.StartRun(job(r.ID))
	if err := l.svc.DeleteSchedule(ctx, sc.ID); err != nil {
		t.Fatal(err)
	}
	if !l.nomad.Purged(job(r.ID)) || l.nomad.Variable(nomadjob.ScheduleRunVariablePath(job(r.ID))) != nil {
		t.Error("the run going was left in Nomad")
	}
	if _, err := l.store.GetRun(ctx, r.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the run's row = %v, want gone", err)
	}
}

func TestMaskSecrets(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		secrets map[string]string
		want    string
	}{
		{"nothing to mask", "hello\n", map[string]string{"TOKEN": "abcdef123"}, "hello\n"},
		{"one value, twice", "a=abcdef123 b=abcdef123", map[string]string{"TOKEN": "abcdef123"},
			"a=[secret TOKEN] b=[secret TOKEN]"},
		{"values under 6 bytes stay", "on 12345 yes", map[string]string{"FLAG": "on", "PIN": "12345"}, "on 12345 yes"},
		{"exactly 6 bytes is masked", "pin=123456", map[string]string{"PIN": "123456"}, "pin=[secret PIN]"},
		{"the longer value wins where one holds the other", "postgres://u:hunter22@db",
			map[string]string{"PASSWORD": "hunter22", "URL": "postgres://u:hunter22@db"}, "[secret URL]"},
		{"a value inside a replacement's text isn't masked again", "x=secret!",
			map[string]string{"A": "secret!", "B": "[secret A]"}, "x=[secret A]"},
		{"no secrets", "abc", nil, "abc"},
		{"no output", "", map[string]string{"TOKEN": "abcdef123"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(maskSecrets([]byte(tt.out), tt.secrets)); got != tt.want {
				t.Errorf("maskSecrets = %q, want %q", got, tt.want)
			}
		})
	}
}
