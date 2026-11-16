package service

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// copySentinel is what the fake exec of these tests prints, whatever the
// command is: it is in no command or argument, so it must reach no row, log
// line, error or answer (a copy run's output is dropped, feature 040).
const copySentinel = "s3cret-sentinel-040-output"

// copyLab is a schedLab whose whoami service has two healthy copies: "c1", the
// older, and "c2".
type copyLab struct {
	*schedLab
}

func newCopyLab(t *testing.T) *copyLab {
	t.Helper()
	l := &copyLab{newSchedLab(t)}
	l.setCopies(
		api.AllocationListStub{ID: "c2", TaskGroup: "web", ClientStatus: "running", CreateTime: 2000},
		api.AllocationListStub{ID: "c1", TaskGroup: "web", ClientStatus: "running", CreateTime: 1000})
	return l
}

func (l *copyLab) setCopies(allocs ...api.AllocationListStub) {
	l.nomad.SetCopies("whoami", allocs...)
}

// createCopy makes a schedule that runs in the running copy.
func (l *copyLab) createCopy(name string, all bool, command ...string) Schedule {
	l.t.Helper()
	if len(command) == 0 {
		command = []string{"sh", "-c", "echo hi"}
	}
	sc, err := l.svc.CreateSchedule(context.Background(), NewSchedule{App: "whoami", Name: name, Command: command,
		Cron: "*/5 * * * *", Target: TargetCopy, AllCopies: all})
	if err != nil {
		l.t.Fatalf("CreateSchedule(%s): %v", name, err)
	}
	return sc
}

// waitSettled waits for the run's goroutine to settle it and returns the row.
func (l *copyLab) waitSettled(runID string) store.ScheduleRunRecord {
	l.t.Helper()
	waitFor(l.t, "run "+runID+" to settle", func() bool { return !l.stored(runID).Open() })
	return l.stored(runID)
}

// waitExecs waits for n commands to have reached the fake Nomad.
func (l *copyLab) waitExecs(n int) {
	l.t.Helper()
	waitFor(l.t, "the commands to reach Nomad", func() bool { return len(l.nomad.Execs()) >= n })
}

// goingCopyRuns is how many runs of this process have an exec to cancel.
func (l *copyLab) goingCopyRuns() int {
	l.svc.runMu.Lock()
	defer l.svc.runMu.Unlock()
	return len(l.svc.copyRuns)
}

func (l *copyLab) actions() []store.ActionRecord {
	l.t.Helper()
	acts, err := l.svc.ListActions(context.Background(), "whoami", 50)
	if err != nil {
		l.t.Fatal(err)
	}
	return acts
}

func TestCopyScheduleChecks(t *testing.T) {
	ctx := context.Background()
	l := newCopyLab(t)
	vol := whoami()
	vol.ID, vol.Name = "files", "files"
	vol.Services = append(vol.Services, app.Service{Name: "store", Image: "busybox:latest",
		Volumes: []app.Volume{{Name: "data", Path: "/data"}}})
	if _, err := l.store.SaveApp(ctx, vol, nil); err != nil {
		t.Fatal(err)
	}

	no := false
	ok := func() NewSchedule {
		return NewSchedule{App: "whoami", Name: "purge", Command: []string{"purge"}, Cron: "0 3 * * *", Target: TargetCopy}
	}
	for _, tt := range []struct {
		name   string
		change func(*NewSchedule)
		field  string
	}{
		{"a target that isn't one", func(n *NewSchedule) { n.Target = "both" }, "target"},
		{"every copy of a new copy", func(n *NewSchedule) { n.Target, n.AllCopies = TargetFresh, true }, "allCopies"},
		{"every copy with no target", func(n *NewSchedule) { n.Target, n.AllCopies = "", true }, "allCopies"},
		{"a fresh limit", func(n *NewSchedule) { n.StopAfterSeconds = 1800 }, "stopAfterSeconds"},
		{"a limit of 90 seconds", func(n *NewSchedule) { n.StopAfterSeconds = 90 }, "stopAfterSeconds"},
		{"memory", func(n *NewSchedule) { n.MemoryMB = 256 }, "memoryMB"},
		{"allowing overlap", func(n *NewSchedule) { n.SkipIfRunning = &no }, "skipIfRunning"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			in := ok()
			tt.change(&in)
			_, err := l.svc.CreateSchedule(ctx, in)
			var fe *app.FieldError
			if !errors.As(err, &fe) || fe.Field != tt.field {
				t.Fatalf("CreateSchedule = %v, want a field error on %q", err, tt.field)
			}
		})
	}

	t.Run("60, 120 and 300 seconds are the limits, 60 by default", func(t *testing.T) {
		for _, s := range []int{0, 60, 120, 300} {
			in := ok()
			in.Name, in.StopAfterSeconds = "limit-"+time.Duration(s).String(), s
			sc, err := l.svc.CreateSchedule(ctx, in)
			if err != nil {
				t.Fatalf("limit %d: %v", s, err)
			}
			if want := max(s, 60); sc.StopAfterSeconds != want {
				t.Errorf("limit %d stored as %d, want %d", s, sc.StopAfterSeconds, want)
			}
		}
	})
	t.Run("what is stored and read back", func(t *testing.T) {
		in := ok()
		in.Name, in.AllCopies = "everywhere", true
		sc, err := l.svc.CreateSchedule(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if sc.Target != TargetCopy || !sc.AllCopies || sc.MemoryMB != 0 || !sc.SkipIfRunning || sc.StopAfterSeconds != 60 {
			t.Errorf("schedule = %+v", sc)
		}
		got, err := l.svc.GetSchedule(ctx, sc.ID)
		if err != nil || got.Target != TargetCopy || !got.AllCopies || got.MemoryMB != 0 {
			t.Errorf("read back = %+v, %v", got, err)
		}
	})
	t.Run("a service with a volume may run in the running copy, not in a new one", func(t *testing.T) {
		in := NewSchedule{App: "files", Service: "store", Name: "clean", Command: []string{"ls"}, Cron: "0 3 * * *", Target: TargetCopy}
		if _, err := l.svc.CreateSchedule(ctx, in); err != nil {
			t.Fatalf("a copy schedule on a volume service: %v", err)
		}
		in.Name, in.Target = "clean-fresh", TargetFresh
		var fe *app.FieldError
		if _, err := l.svc.CreateSchedule(ctx, in); !errors.As(err, &fe) || fe.Field != "service" {
			t.Fatalf("a fresh schedule on a volume service = %v, want a field error on service", err)
		}
	})
	t.Run("an edit can change the target both ways", func(t *testing.T) {
		sc := l.create("movable", "0 3 * * *")
		if sc.Target != TargetFresh {
			t.Fatalf("a new schedule's target is %q, want fresh", sc.Target)
		}
		up, err := l.svc.UpdateSchedule(ctx, sc.ID, NewSchedule{Name: "movable", Command: []string{"echo", "hello"},
			Cron: "0 3 * * *", Target: TargetCopy})
		if err != nil || up.Target != TargetCopy || up.MemoryMB != 0 {
			t.Fatalf("to copy = %+v, %v", up, err)
		}
		up, err = l.svc.UpdateSchedule(ctx, sc.ID, NewSchedule{Name: "movable", Command: []string{"echo", "hello"}, Cron: "0 3 * * *"})
		if err != nil || up.Target != TargetFresh || up.MemoryMB != 256 || up.StopAfterSeconds != 1800 {
			t.Fatalf("back to fresh = %+v, %v", up, err)
		}
	})
}

func TestPickCopies(t *testing.T) {
	at := func(n int64) *time.Time { return new(time.Unix(n, 0).UTC()) }
	healthy := func(id, service string, created *time.Time) Copy {
		return Copy{ID: id, Service: service, Status: deploy.CopyHealthy, CreatedAt: created}
	}
	copies := []Copy{
		healthy("b", "web", at(2)),
		healthy("z", "web", nil), // no create time: after the ones that have it
		healthy("a", "web", at(2)),
		healthy("c", "web", at(1)),
		healthy("w", "worker", at(0)),
		{ID: "u", Service: "web", Status: deploy.CopyUnhealthy, CreatedAt: at(0)},
		{ID: "s", Service: "web", Status: deploy.CopyStarting, CreatedAt: at(0)},
		{ID: "t", Service: "web", Status: deploy.CopyStopped, Terminal: true, CreatedAt: at(0)},
		{ID: "d", Service: "web", Status: deploy.CopyHealthy, Stopping: true, CreatedAt: at(0)},
	}
	ids := func(cs []Copy) string {
		var out []string
		for _, c := range cs {
			out = append(out, c.ID)
		}
		return strings.Join(out, " ")
	}
	one, err := pickCopies(copies, "web", false)
	if err != nil || ids(one) != "c" {
		t.Errorf("one copy = %q, %v; want the oldest, c", ids(one), err)
	}
	all, err := pickCopies(copies, "web", true)
	if err != nil || ids(all) != "c a b z" {
		t.Errorf("every copy = %q, %v; want c a b z (oldest first, then by ID)", ids(all), err)
	}
	if _, err := pickCopies(copies, "none", true); !errors.Is(err, ErrNoRunningCopy) {
		t.Errorf("a service with no copy = %v, want ErrNoRunningCopy", err)
	}
}

func TestCopyRun(t *testing.T) {
	ctx := context.Background()

	t.Run("runs in the oldest healthy copy, keeps no output and records the command", func(t *testing.T) {
		l := newCopyLab(t)
		l.nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
			return deploytest.ExecReply{Stdout: copySentinel, Stderr: copySentinel}
		})
		sc := l.createCopy("purge", false, "sh", "-c", "echo hi")
		r := l.runNow(sc.ID)
		if r.Target != TargetCopy || r.Trigger != RunTriggerOwner || !slices.Equal(r.Copies, []string{"c1"}) {
			t.Fatalf("run = %+v", r)
		}
		row := l.waitSettled(r.ID)
		if row.Status != store.RunOK || row.ExitCode == nil || *row.ExitCode != 0 || row.Error != "" || row.OutputKept ||
			row.Stdout != "" || row.Stderr != "" || row.Target != store.TargetCopy || !slices.Equal(row.Copies, []string{"c1"}) {
			t.Errorf("row = %+v", row)
		}
		execs := l.nomad.Execs()
		if len(execs) != 1 || execs[0].Copy != "c1" || execs[0].Task != "web" ||
			!slices.Equal(execs[0].Command, []string{"sh", "-c", "echo hi"}) {
			t.Errorf("execs = %+v, want one in task web of copy c1", execs)
		}
		if l.nomad.Job(job(r.ID)) != nil {
			t.Error("a copy run registered a Nomad job")
		}
		if l.goingCopyRuns() != 0 {
			t.Error("a settled run still has a cancel function")
		}
		acts := l.actions()
		if len(acts) != 1 || acts[0].Action != store.ActionRun || acts[0].Trigger != string(app.TriggerSchedule) ||
			acts[0].Program != "sh" || acts[0].CopyID != "c1" || acts[0].Service != "web" ||
			acts[0].Status != store.ActionSucceeded || acts[0].ExitCode == nil || *acts[0].ExitCode != 0 {
			t.Errorf("actions = %+v", acts)
		}
		if _, _, err := l.svc.GetScheduleRunOutput(ctx, sc.ID, r.ID, "stdout"); !errors.Is(err, ErrOutputNotKept) {
			t.Errorf("reading the output = %v, want ErrOutputNotKept", err)
		}
		if got, _ := l.svc.GetScheduleRun(ctx, sc.ID, r.ID); got.OutputKept || got.StdoutBytes != 0 || !slices.Equal(got.Copies, []string{"c1"}) {
			t.Errorf("GetScheduleRun = %+v", got)
		}
	})

	t.Run("a failed exit is the run's, with its code", func(t *testing.T) {
		l := newCopyLab(t)
		l.nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply { return deploytest.ExecReply{ExitCode: 3} })
		r := l.runNow(l.createCopy("purge", false).ID)
		row := l.waitSettled(r.ID)
		if row.Status != store.RunFailed || row.Error != "exit 3" || row.ExitCode == nil || *row.ExitCode != 3 {
			t.Errorf("row = %s %q %v", row.Status, row.Error, row.ExitCode)
		}
		if acts := l.actions(); len(acts) != 1 || acts[0].ExitCode == nil || *acts[0].ExitCode != 3 {
			t.Errorf("actions = %+v", acts)
		}
		if items := attentionKinds(t, l.svc, AttentionScheduleFailed); len(items) != 1 || items[0].ExitCode == nil || *items[0].ExitCode != 3 {
			t.Errorf("needs-you items = %+v, want one schedule_failed with exit code 3", items)
		}
	})

	t.Run("every copy: one after the other, the first failure is the run's, the others still run", func(t *testing.T) {
		l := newCopyLab(t)
		l.nomad.OnExec(func(r deploytest.ExecRequest) deploytest.ExecReply {
			if r.Copy == "c1" {
				return deploytest.ExecReply{ExitCode: 3}
			}
			return deploytest.ExecReply{ExitCode: 4}
		})
		r := l.runNow(l.createCopy("purge", true).ID)
		if !slices.Equal(r.Copies, []string{"c1", "c2"}) {
			t.Fatalf("copies = %v, want c1 c2", r.Copies)
		}
		row := l.waitSettled(r.ID)
		if row.Status != store.RunFailed || row.Error != "exit 3 in copy c1" || row.ExitCode == nil || *row.ExitCode != 3 {
			t.Errorf("row = %s %q %v", row.Status, row.Error, row.ExitCode)
		}
		execs := l.nomad.Execs()
		if len(execs) != 2 || execs[0].Copy != "c1" || execs[1].Copy != "c2" {
			t.Errorf("execs = %+v, want c1 then c2", execs)
		}
		if acts := l.actions(); len(acts) != 2 {
			t.Errorf("%d action rows, want one per copy", len(acts))
		}
		if items := attentionKinds(t, l.svc, AttentionScheduleFailed); len(items) != 1 || items[0].ExitCode == nil || *items[0].ExitCode != 3 {
			t.Errorf("needs-you items = %+v, want exit code 3", items)
		}
	})

	t.Run("a command Nomad can't start", func(t *testing.T) {
		l := newCopyLab(t)
		l.nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
			return deploytest.ExecReply{Fail: `exec: "nope": executable file not found`}
		})
		r := l.runNow(l.createCopy("purge", false, "nope").ID)
		row := l.waitSettled(r.ID)
		if row.Status != store.RunCouldNotStart || row.Error != "the command couldn't start in its copy" || row.ExitCode != nil {
			t.Errorf("row = %s %q %v", row.Status, row.Error, row.ExitCode)
		}
		if acts := l.actions(); len(acts) != 1 || acts[0].Error != "failed to start" {
			t.Errorf("actions = %+v", acts)
		}
	})

	t.Run("Nomad refusing the exec is a failure in our words", func(t *testing.T) {
		l := newCopyLab(t)
		l.nomad.FailExec(403)
		r := l.runNow(l.createCopy("purge", false).ID)
		row := l.waitSettled(r.ID)
		if row.Status != store.RunFailed || (row.Error != "Nomad refused or could not be reached" && row.Error != "Nomad could not run it") {
			t.Errorf("row = %s %q", row.Status, row.Error)
		}
	})

	t.Run("the limit stops it", func(t *testing.T) {
		l := newCopyLab(t)
		l.svc.SetScheduleLimitForTests(150 * time.Millisecond)
		l.nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
			return deploytest.ExecReply{Stdout: copySentinel, Hold: true}
		})
		r := l.runNow(l.createCopy("slow", false, "sleep", "100").ID)
		row := l.waitSettled(r.ID)
		if row.Status != store.RunStoppedAtLimit || row.Error != "stopped after 150ms" || row.ExitCode != nil {
			t.Errorf("row = %s %q %v", row.Status, row.Error, row.ExitCode)
		}
		if acts := l.actions(); len(acts) != 1 || acts[0].Status != store.ActionFailed || !strings.Contains(acts[0].Error, "timed out") {
			t.Errorf("actions = %+v", acts)
		}
	})

	t.Run("no healthy copy: Run now is refused and nothing is written", func(t *testing.T) {
		l := newCopyLab(t)
		l.setCopies(api.AllocationListStub{ID: "c1", TaskGroup: "web", ClientStatus: "pending"})
		sc := l.createCopy("purge", false)
		if _, err := l.svc.RunScheduleNow(ctx, sc.ID); !errors.Is(err, ErrNoRunningCopy) {
			t.Fatalf("RunScheduleNow = %v, want ErrNoRunningCopy", err)
		}
		if n := len(l.runs(sc.ID)); n != 0 {
			t.Errorf("%d rows written", n)
		}
	})
}

func TestStopCopyRun(t *testing.T) {
	ctx := context.Background()
	hold := func(l *copyLab) {
		l.nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply { return deploytest.ExecReply{Hold: true} })
	}

	t.Run("the owner stops it: the exec is cancelled and the run is stopped", func(t *testing.T) {
		l := newCopyLab(t)
		hold(l)
		sc := l.createCopy("slow", false, "sleep", "100")
		r := l.runNow(sc.ID)
		l.waitExecs(1)
		got, err := l.svc.StopScheduleRun(ctx, sc.ID, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != store.RunRunning && got.Status != store.RunStopped {
			t.Errorf("after Stop: %s", got.Status)
		}
		row := l.waitSettled(r.ID)
		if row.Status != store.RunStopped || row.Error != "stopped by the owner" || row.StopReason != stopOwner {
			t.Errorf("row = %s %q (%s)", row.Status, row.Error, row.StopReason)
		}
		if _, err := l.svc.StopScheduleRun(ctx, sc.ID, r.ID); !errors.Is(err, ErrRunNotRunning) {
			t.Errorf("stopping it again = %v, want ErrRunNotRunning", err)
		}
	})

	t.Run("stopping the app stops it", func(t *testing.T) {
		l := newCopyLab(t)
		hold(l)
		r := l.runNow(l.createCopy("slow", false, "sleep", "100").ID)
		l.waitExecs(1)
		if _, err := l.svc.StopApp(ctx, "whoami", app.TriggerManual); err != nil {
			t.Fatal(err)
		}
		row := l.waitSettled(r.ID)
		if row.Status != store.RunStopped || row.Error != "the app was stopped" {
			t.Errorf("row = %s %q", row.Status, row.Error)
		}
	})

	t.Run("deleting the schedule cancels the exec and leaves no row", func(t *testing.T) {
		l := newCopyLab(t)
		hold(l)
		sc := l.createCopy("slow", false, "sleep", "100")
		l.runNow(sc.ID)
		l.waitExecs(1)
		if err := l.svc.DeleteSchedule(ctx, sc.ID); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the cancelled run to be forgotten", func() bool { return l.goingCopyRuns() == 0 })
		if _, err := l.svc.GetSchedule(ctx, sc.ID); !errors.Is(err, ErrScheduleNotFound) {
			t.Errorf("GetSchedule = %v, want ErrScheduleNotFound", err)
		}
		if strings.Contains(l.logs.String(), "level=ERROR") {
			t.Errorf("an error was logged:\n%s", l.logs.String())
		}
	})
}

func TestCopyScheduleClock(t *testing.T) {
	ctx := context.Background()

	t.Run("a due slot runs in the copy; while it goes the next slot is skipped", func(t *testing.T) {
		l := newCopyLab(t)
		l.nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply { return deploytest.ExecReply{Hold: true} })
		l.clock.Set(clockAt(3, 0, 0))
		sc := l.createCopy("purge", false)
		l.tick(clockAt(3, 5, 10))
		l.waitExecs(1)
		if got := l.slots(sc.ID); got != "03:05 running" {
			t.Fatalf("slots = %s", got)
		}
		if r := l.runs(sc.ID)[0]; r.Trigger != RunTriggerTimer || r.Target != store.TargetCopy || !slices.Equal(r.Copies, []string{"c1"}) {
			t.Errorf("run = %+v", r)
		}
		l.tick(clockAt(3, 10, 10))
		if got := l.slots(sc.ID); got != "03:10 skipped, 03:05 running" {
			t.Fatalf("slots = %s", got)
		}
		if r := l.runs(sc.ID)[0]; r.Error != "the run before was still going" || len(l.nomad.Execs()) != 1 {
			t.Errorf("skipped row = %q, %d execs", r.Error, len(l.nomad.Execs()))
		}
		// Run now by hand is refused too, as for a schedule that skips.
		if _, err := l.svc.RunScheduleNow(ctx, sc.ID); !errors.Is(err, ErrRunGoing) {
			t.Errorf("RunScheduleNow = %v, want ErrRunGoing", err)
		}
		if _, err := l.svc.StopScheduleRun(ctx, sc.ID, l.runs(sc.ID)[1].ID); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a stopped app's slot is a skipped row, not a wait", func(t *testing.T) {
		l := newCopyLab(t)
		l.clock.Set(clockAt(3, 0, 0))
		sc := l.createCopy("purge", false)
		fresh := l.create("fresh", "*/5 * * * *")
		if err := l.store.SetAppStopped(ctx, "whoami", new(clockAt(3, 1, 0))); err != nil {
			t.Fatal(err)
		}
		l.tick(clockAt(3, 5, 10))
		if got := l.slots(sc.ID); got != "03:05 skipped" {
			t.Fatalf("copy schedule's slots = %q, want one skipped", got)
		}
		r := l.runs(sc.ID)[0]
		if r.Error != "the app is stopped" || r.Target != store.TargetCopy || r.FinishedAt == nil || len(r.Copies) != 0 {
			t.Errorf("skipped row = %+v", r)
		}
		if len(l.nomad.Execs()) != 0 {
			t.Error("a command ran in a stopped app")
		}
		if got := l.slots(fresh.ID); got != "" {
			t.Errorf("the fresh schedule's slots = %q: a stopped app's slot waits for it", got)
		}
		// The same slot isn't owed twice.
		l.tick(clockAt(3, 6, 0))
		if got := l.slots(sc.ID); got != "03:05 skipped" {
			t.Errorf("slots = %q", got)
		}
		// Three skipped slots in a row are no needs-you item for a stopped app.
		l.tick(clockAt(3, 10, 10))
		l.tick(clockAt(3, 15, 10))
		if got := l.slots(sc.ID); got != "03:15 skipped, 03:10 skipped, 03:05 skipped" {
			t.Fatalf("slots = %q", got)
		}
		if items := attentionKinds(t, l.svc, AttentionScheduleSkipping); len(items) != 0 {
			t.Errorf("a stopped app raised schedule_skipping: %+v", items)
		}
	})

	t.Run("without a healthy copy the slot waits until there is one", func(t *testing.T) {
		l := newCopyLab(t)
		l.setCopies(api.AllocationListStub{ID: "c1", TaskGroup: "web", ClientStatus: "pending"})
		l.clock.Set(clockAt(3, 0, 0))
		sc := l.createCopy("purge", false)
		l.tick(clockAt(3, 5, 10))
		l.tick(clockAt(3, 6, 10))
		if got := l.slots(sc.ID); got != "" {
			t.Fatalf("slots = %q, want none: the slot waits", got)
		}
		l.setCopies(api.AllocationListStub{ID: "c1", TaskGroup: "web", ClientStatus: "running"})
		l.tick(clockAt(3, 7, 10))
		waitFor(t, "the waiting slot to run", func() bool { return l.slots(sc.ID) != "" })
		row := l.waitSettled(l.runs(sc.ID)[0].ID)
		if row.Status != store.RunOK || row.SlotAt == nil || !row.SlotAt.Equal(clockAt(3, 5, 0)) {
			t.Errorf("row = %s slot %v, want ok for the 03:05 slot", row.Status, row.SlotAt)
		}
	})
}

// A copy run found open by the watcher that this process isn't running was
// left by a process that ended: it is settled without asking Nomad anything.
func TestFollowOrphanCopyRuns(t *testing.T) {
	ctx := context.Background()
	l := newCopyLab(t)
	sc := l.createCopy("purge", false)
	insert := func(id string) store.ScheduleRunRecord {
		r := store.ScheduleRunRecord{ID: id, ScheduleID: sc.ID, AppID: "whoami", Service: "web", Trigger: RunTriggerTimer,
			Command: sc.Command, Target: store.TargetCopy, Copies: []string{"c1"}, Status: store.RunRunning,
			StartedAt: l.clock.Now().UTC()}
		if err := l.store.InsertRun(ctx, r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	lost := insert("lost00000001")
	stopped := insert("stop00000001")
	if err := l.store.SetRunStopIntent(ctx, stopped.ID, stopOwner, l.clock.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	l.follow()
	if row := l.stored(lost.ID); row.Status != store.RunFailed || row.Error != "lost while Caravana was restarting" || row.FinishedAt == nil {
		t.Errorf("lost run = %s %q", row.Status, row.Error)
	}
	if row := l.stored(stopped.ID); row.Status != store.RunStopped || row.Error != "stopped by the owner" {
		t.Errorf("a run stopped before the process ended = %s %q", row.Status, row.Error)
	}
	if l.nomad.Purged(job(lost.ID)) || l.nomad.Stops(job(lost.ID)) != 0 {
		t.Error("the watcher called Nomad about a run that has no job")
	}
}

// A run this process is running isn't touched by the watcher.
func TestFollowLeavesAGoingCopyRunAlone(t *testing.T) {
	l := newCopyLab(t)
	l.nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply { return deploytest.ExecReply{Hold: true} })
	sc := l.createCopy("slow", false, "sleep", "100")
	r := l.runNow(sc.ID)
	l.waitExecs(1)
	l.follow()
	if row := l.stored(r.ID); row.Status != store.RunRunning {
		t.Fatalf("after a watcher pass: %s %q", row.Status, row.Error)
	}
	if _, err := l.svc.StopScheduleRun(context.Background(), sc.ID, r.ID); err != nil {
		t.Fatal(err)
	}
	l.waitSettled(r.ID)
}

// Nothing a command prints, and nothing Nomad says about a command that
// couldn't start, is in a row, a log line, an error or an answer.
func TestCopyRunOutputIsNeverKept(t *testing.T) {
	ctx := context.Background()
	for name, reply := range map[string]deploytest.ExecReply{
		"ok":          {Stdout: copySentinel, Stderr: copySentinel},
		"failed":      {Stdout: copySentinel, Stderr: copySentinel, ExitCode: 2},
		"no start":    {Fail: "exec: " + copySentinel},
		"exit first":  {Stdout: copySentinel, ExitFirst: true},
		"no exit":     {Stdout: copySentinel, NoExit: true},
		"held, limit": {Stdout: copySentinel, Hold: true},
	} {
		t.Run(name, func(t *testing.T) {
			l := newCopyLab(t)
			l.svc.SetScheduleLimitForTests(200 * time.Millisecond)
			l.nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply { return reply })
			sc := l.createCopy("purge", true, "sh", "-c", "echo hi")
			r := l.runNow(sc.ID)
			l.waitSettled(r.ID)

			var seen []string
			for _, run := range l.runs(sc.ID) {
				full := l.stored(run.ID)
				blob, _ := json.Marshal(full)
				seen = append(seen, string(blob), full.Stdout, full.Stderr, full.Error)
			}
			list, _ := l.svc.ListScheduleRuns(ctx, sc.ID, 0)
			one, _ := l.svc.GetScheduleRun(ctx, sc.ID, r.ID)
			shown, _ := l.svc.GetSchedule(ctx, sc.ID)
			for _, v := range []any{list, one, shown, l.actions()} {
				blob, _ := json.Marshal(v)
				seen = append(seen, string(blob))
			}
			feed, _ := l.svc.Activity(ctx, ActivityQuery{})
			att, _ := l.svc.Attention(ctx)
			blob, _ := json.Marshal([]any{feed, att})
			seen = append(seen, string(blob), l.logs.String(), rawStore(t, l.dir))
			for _, text := range seen {
				if strings.Contains(text, copySentinel) {
					t.Fatalf("the sentinel is in %.300q", text)
				}
			}
			if _, _, err := l.svc.GetScheduleRunOutput(ctx, sc.ID, r.ID, "stderr"); !errors.Is(err, ErrOutputNotKept) {
				t.Errorf("reading the output = %v, want ErrOutputNotKept", err)
			}
		})
	}
}

// attentionKinds are the needs-you items of one kind.
func attentionKinds(t *testing.T, svc *Service, kind AttentionKind) []AttentionItem {
	t.Helper()
	att, err := svc.Attention(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []AttentionItem
	for _, it := range att.Items {
		if it.Kind == kind {
			out = append(out, it)
		}
	}
	return out
}
