package service

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/registry/registrytest"
	"github.com/cosmic-chimps/caravana/internal/secrets/secretstest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// schedSecret is the value of whoami's secret API_TOKEN in a schedLab. It must
// never reach a log line, the database or a run's kept output.
const schedSecret = "s3cret-sentinel-022-value"

// schedLab is an env with whoami deployed healthy (secret API_TOKEN set), the
// store's directory and the service's log known, and the fake clock at hand.
type schedLab struct {
	*env
	t     *testing.T
	clock *fakeClock
	dir   string
	logs  *syncBuffer
}

func newSchedLab(t *testing.T) *schedLab {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{store: st, registry: registrytest.New(t), nomad: deploytest.New(t), secrets: secretstest.New()}
	e.registry.AddIndex("traefik/whoami", "latest", whoamiDigest)
	e.nomad.Succeed()
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e.svc = startServiceLogging(t, st, newDeployer(t, e.registry, e.nomad), e.secrets, log)
	deployHealthy(t, e, withSecrets(set("API_TOKEN", schedSecret)))
	return &schedLab{env: e, t: t, clock: e.svc.clock.(*fakeClock), dir: dir, logs: logs}
}

// create makes a schedule of whoami's web service, failing the test on error.
func (l *schedLab) create(name, cron string, command ...string) Schedule {
	l.t.Helper()
	if len(command) == 0 {
		command = []string{"echo", "hello"}
	}
	sc, err := l.svc.CreateSchedule(context.Background(), NewSchedule{App: "whoami", Name: name, Command: command, Cron: cron})
	if err != nil {
		l.t.Fatalf("CreateSchedule(%s): %v", name, err)
	}
	return sc
}

// runNow is RunScheduleNow, failing the test on error.
func (l *schedLab) runNow(id string) ScheduleRun {
	l.t.Helper()
	r, err := l.svc.RunScheduleNow(context.Background(), id)
	if err != nil {
		l.t.Fatalf("RunScheduleNow(%s): %v", id, err)
	}
	return r
}

// job is the Nomad job of a run of whoami.
func job(runID string) string { return deploy.ScheduleRunJobID("whoami", runID) }

// stored is the run's row, output included.
func (l *schedLab) stored(runID string) store.ScheduleRunRecord {
	l.t.Helper()
	r, err := l.store.GetRun(context.Background(), runID)
	if err != nil {
		l.t.Fatalf("GetRun(%s): %v", runID, err)
	}
	return r
}

// runs are a schedule's rows, newest first.
func (l *schedLab) runs(scheduleID string) []store.ScheduleRunRecord {
	l.t.Helper()
	list, err := l.store.ListRuns(context.Background(), scheduleID, 0)
	if err != nil {
		l.t.Fatal(err)
	}
	return list
}

// follow is one pass of the run watcher.
func (l *schedLab) follow() { l.svc.followScheduleRuns(context.Background()) }

func TestCreateScheduleChecks(t *testing.T) {
	ctx := context.Background()
	l := newSchedLab(t)
	// An app with a volume on one service, and one with no definition yet.
	vol := whoami()
	vol.ID, vol.Name = "files", "files"
	vol.Services = append(vol.Services, app.Service{Name: "store", Image: "busybox:latest",
		Volumes: []app.Volume{{Name: "data", Path: "/data"}}})
	if _, err := l.store.SaveApp(ctx, vol, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := l.svc.CreateAppOnly(ctx, app.App{ID: "empty", Name: "empty"}); err != nil {
		t.Fatal(err)
	}

	ok := func() NewSchedule {
		return NewSchedule{App: "whoami", Name: "nightly", Command: []string{"echo", "hi"}, Cron: "0 3 * * *"}
	}
	tests := []struct {
		name   string
		change func(*NewSchedule)
		field  string
	}{
		{"no app", func(n *NewSchedule) { n.App = "" }, "app"},
		{"an app with no definition", func(n *NewSchedule) { n.App = "empty" }, "app"},
		{"no name", func(n *NewSchedule) { n.Name = "" }, "name"},
		{"a name that isn't a label", func(n *NewSchedule) { n.Name = "Night Job" }, "name"},
		{"a name of 32 characters", func(n *NewSchedule) { n.Name = strings.Repeat("a", 32) }, "name"},
		{"no command", func(n *NewSchedule) { n.Command = nil }, "command"},
		{"an empty program", func(n *NewSchedule) { n.Command = []string{" ", "x"} }, "command"},
		{"a NUL in an argument", func(n *NewSchedule) { n.Command = []string{"echo", "a\x00b"} }, "command"},
		{"65 items", func(n *NewSchedule) { n.Command = slices.Repeat([]string{"x"}, 65) }, "command"},
		{"no cron", func(n *NewSchedule) { n.Cron = "  " }, "cron"},
		{"words, not a cron line", func(n *NewSchedule) { n.Cron = "every day at 03:00" }, "cron"},
		{"six fields", func(n *NewSchedule) { n.Cron = "0 0 3 * * *" }, "cron"},
		{"a time that never comes", func(n *NewSchedule) { n.Cron = "0 0 31 2 *" }, "cron"},
		{"an unknown service", func(n *NewSchedule) { n.Service = "worker" }, "service"},
		{"a stop-after that isn't a choice", func(n *NewSchedule) { n.StopAfterSeconds = 600 }, "stopAfterSeconds"},
		{"memory that isn't a choice", func(n *NewSchedule) { n.MemoryMB = 300 }, "memoryMB"},
	}
	for _, tt := range tests {
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

	t.Run("a service with a volume names the service", func(t *testing.T) {
		_, err := l.svc.CreateSchedule(ctx, NewSchedule{App: "files", Service: "store", Name: "x", Command: []string{"ls"}, Cron: "0 3 * * *"})
		var fe *app.FieldError
		if !errors.As(err, &fe) || fe.Field != "service" ||
			fe.Message != "the service mounts a volume; a schedule needs a service without volumes" {
			t.Fatalf("CreateSchedule = %v", err)
		}
	})
	t.Run("an unknown app is not found", func(t *testing.T) {
		in := ok()
		in.App = "nope"
		if _, err := l.svc.CreateSchedule(ctx, in); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("CreateSchedule = %v, want store.ErrNotFound", err)
		}
	})

	t.Run("the defaults", func(t *testing.T) {
		l.clock.Set(time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC))
		sc, err := l.svc.CreateSchedule(ctx, NewSchedule{App: "whoami", Name: "defaults", Command: []string{"date"}, Cron: " 0  3 * * * "})
		if err != nil {
			t.Fatal(err)
		}
		want := Schedule{ID: sc.ID, App: "whoami", Service: "web", Name: "defaults", Command: []string{"date"},
			Cron: "0 3 * * *", StopAfterSeconds: 1800, MemoryMB: 256, SkipIfRunning: true, Words: "Every day at 03:00 UTC",
			NextRunAt: new(time.Date(2026, 10, 16, 3, 0, 0, 0, time.UTC)), Project: "default", Environment: "production",
			AppName: "whoami", ArmedAt: time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)}
		if len(sc.ID) != 12 || !equalSchedules(sc, want) {
			t.Errorf("created\n got %+v\nwant %+v", sc, want)
		}
		if _, err := l.svc.CreateSchedule(ctx, NewSchedule{App: "whoami", Name: "defaults", Command: []string{"date"}, Cron: "0 4 * * *"}); !errors.Is(err, ErrScheduleExists) {
			t.Errorf("the same name again = %v, want ErrScheduleExists", err)
		}
	})
}

// equalSchedules compares two schedules, times by instant.
func equalSchedules(a, b Schedule) bool {
	sameTime := func(x, y *time.Time) bool { return (x == nil) == (y == nil) && (x == nil || x.Equal(*y)) }
	return a.ID == b.ID && a.App == b.App && a.Service == b.Service && a.Name == b.Name &&
		slices.Equal(a.Command, b.Command) && a.Cron == b.Cron && a.StopAfterSeconds == b.StopAfterSeconds &&
		a.MemoryMB == b.MemoryMB && a.SkipIfRunning == b.SkipIfRunning && a.Words == b.Words &&
		a.Paused == b.Paused && sameTime(a.PausedAt, b.PausedAt) && a.Builtin == b.Builtin &&
		sameTime(a.NextRunAt, b.NextRunAt) && (a.LastRun == nil) == (b.LastRun == nil) &&
		a.RunningNow == b.RunningNow && (a.UsualSeconds == nil) == (b.UsualSeconds == nil) &&
		a.Project == b.Project && a.Environment == b.Environment && a.AppName == b.AppName && a.ArmedAt.Equal(b.ArmedAt)
}

func TestRoundedMemory(t *testing.T) {
	for in, want := range map[int]int{1: 256, 64: 256, 256: 256, 257: 512, 512: 512, 1000: 1024, 2048: 2048, 4096: 2048} {
		if got := roundedMemory(in); got != want {
			t.Errorf("roundedMemory(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestChangeSchedule(t *testing.T) {
	ctx := context.Background()
	l := newSchedLab(t)
	t0 := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
	l.clock.Set(t0)
	notices, stop := l.svc.Subscribe("")
	defer stop()
	got := collect(notices)
	sc := l.create("tidy", "0 * * * *")

	t.Run("an edit keeps the clock unless the cron changes", func(t *testing.T) {
		l.clock.Set(t0.Add(10 * time.Minute))
		up, err := l.svc.UpdateSchedule(ctx, sc.ID, NewSchedule{Name: "tidy-up", Command: []string{"true"}, Cron: "0 * * * *", MemoryMB: 512})
		if err != nil {
			t.Fatal(err)
		}
		if up.Name != "tidy-up" || up.MemoryMB != 512 || !up.ArmedAt.Equal(t0) {
			t.Errorf("after an edit: %+v", up)
		}
		l.clock.Set(t0.Add(20 * time.Minute))
		up, err = l.svc.UpdateSchedule(ctx, sc.ID, NewSchedule{App: "whoami", Name: "tidy-up", Command: []string{"true"}, Cron: "*/10 * * * *"})
		if err != nil {
			t.Fatal(err)
		}
		if !up.ArmedAt.Equal(t0.Add(20*time.Minute)) || !up.NextRunAt.Equal(t0.Add(30*time.Minute)) {
			t.Errorf("after a cron change: armed %v, next %v", up.ArmedAt, up.NextRunAt)
		}
	})
	t.Run("a schedule stays in its app", func(t *testing.T) {
		_, err := l.svc.UpdateSchedule(ctx, sc.ID, NewSchedule{App: "other", Name: "x", Command: []string{"true"}, Cron: "0 * * * *"})
		var fe *app.FieldError
		if !errors.As(err, &fe) || fe.Field != "app" {
			t.Errorf("UpdateSchedule to another app = %v", err)
		}
	})
	t.Run("a name another schedule has", func(t *testing.T) {
		l.create("other", "0 5 * * *")
		_, err := l.svc.UpdateSchedule(ctx, sc.ID, NewSchedule{Name: "other", Command: []string{"true"}, Cron: "0 * * * *"})
		if !errors.Is(err, ErrScheduleExists) {
			t.Errorf("UpdateSchedule to a taken name = %v", err)
		}
	})
	t.Run("pause and resume", func(t *testing.T) {
		l.clock.Set(t0.Add(25 * time.Minute))
		p, err := l.svc.PauseSchedule(ctx, sc.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !p.Paused || p.PausedAt == nil || p.NextRunAt != nil {
			t.Errorf("paused: %+v", p)
		}
		if again, _ := l.svc.PauseSchedule(ctx, sc.ID); !again.PausedAt.Equal(*p.PausedAt) {
			t.Errorf("a second pause moved the pause to %v", again.PausedAt)
		}
		l.clock.Set(t0.Add(95 * time.Minute))
		r, err := l.svc.ResumeSchedule(ctx, sc.ID)
		if err != nil {
			t.Fatal(err)
		}
		if r.Paused || !r.ArmedAt.Equal(t0.Add(95*time.Minute)) || !r.NextRunAt.Equal(t0.Add(100*time.Minute)) {
			t.Errorf("resumed: %+v", r)
		}
	})
	t.Run("not found", func(t *testing.T) {
		for name, err := range map[string]error{
			"get":    func() error { _, err := l.svc.GetSchedule(ctx, "aaaaaaaaaaaa"); return err }(),
			"pause":  func() error { _, err := l.svc.PauseSchedule(ctx, "aaaaaaaaaaaa"); return err }(),
			"delete": l.svc.DeleteSchedule(ctx, "aaaaaaaaaaaa"),
			"runs":   func() error { _, err := l.svc.ListScheduleRuns(ctx, "aaaaaaaaaaaa", 0); return err }(),
		} {
			if !errors.Is(err, ErrScheduleNotFound) {
				t.Errorf("%s = %v, want ErrScheduleNotFound", name, err)
			}
		}
	})
	t.Run("delete", func(t *testing.T) {
		if err := l.svc.DeleteSchedule(ctx, sc.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := l.svc.GetSchedule(ctx, sc.ID); !errors.Is(err, ErrScheduleNotFound) {
			t.Errorf("after delete, GetSchedule = %v", err)
		}
	})
	waitFor(t, "the notices", func() bool { return got.count("whoami", ChangeSchedules) >= 6 })
}

func TestListSchedules(t *testing.T) {
	ctx := context.Background()
	l := newSchedLab(t)
	t0 := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
	l.clock.Set(t0)

	// A second app with a Postgres backup on db and a volume backup on files.
	shop := app.App{ID: "shop", Name: "shop", Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{
		{Name: "db", Image: "postgres:17", Backup: &app.Backup{Engine: app.BackupPostgres, Schedule: "30 2 * * *"}},
		{Name: "files", Image: "busybox:latest", Volumes: []app.Volume{{Name: "data", Path: "/data"}},
			Backup: &app.Backup{Engine: app.BackupVolume}},
		{Name: "web", Image: "busybox:latest"},
	}}
	if _, err := l.store.SaveApp(ctx, shop, nil); err != nil {
		t.Fatal(err)
	}
	for _, b := range []store.BackupRecord{
		{AppID: "shop", ID: "db-1", Service: "db", Status: store.BackupSucceeded, StartedAt: t0.Add(-25 * time.Hour), FinishedAt: new(t0.Add(-25*time.Hour + 40*time.Second))},
		{AppID: "shop", ID: "db-2", Service: "db", Status: store.BackupFailed, Message: "pg_dump failed", StartedAt: t0.Add(-time.Hour), FinishedAt: new(t0.Add(-time.Hour + 5*time.Second))},
		{AppID: "shop", ID: "files-1", Service: "files", Engine: app.BackupVolume, Status: store.BackupRunning, StartedAt: t0.Add(-time.Minute)},
	} {
		if err := l.store.SaveBackup(ctx, b); err != nil {
			t.Fatal(err)
		}
	}

	// Stored schedules in each state; runs are written to the store directly.
	l.create("never", "0 9 * * *")            // next 09:00
	early := l.create("early", "0 4 * * *")   // next 04:00
	paused := l.create("paused", "0 5 * * *") // paused, though its last run failed
	failed := l.create("failed", "0 8 * * *")
	running := l.create("running", "0 7 * * *")
	if _, err := l.svc.PauseSchedule(ctx, paused.ID); err != nil {
		t.Fatal(err)
	}
	addRun := func(sc Schedule, id, status string, took time.Duration, minutesAgo int) {
		t.Helper()
		start := t0.Add(-time.Duration(minutesAgo) * time.Minute)
		r := store.ScheduleRunRecord{ID: id, ScheduleID: sc.ID, AppID: "whoami", Service: "web", Trigger: RunTriggerOwner,
			Command: []string{"x"}, Status: store.RunRunning, StartedAt: start}
		if status == store.RunSkipped {
			r.Status, r.FinishedAt = store.RunSkipped, &start
		}
		if err := l.store.InsertRun(ctx, r); err != nil {
			t.Fatal(err)
		}
		if status == store.RunRunning || status == store.RunSkipped {
			return
		}
		if err := l.store.SettleRun(ctx, id, store.RunSettlement{Status: status, TaskStartedAt: &start,
			FinishedAt: start.Add(took)}); err != nil {
			t.Fatal(err)
		}
	}
	addRun(paused, "p00000000001", store.RunFailed, time.Second, 50)
	addRun(failed, "f00000000001", store.RunOK, 10*time.Second, 90)
	addRun(failed, "f00000000002", store.RunOK, 30*time.Second, 80)
	addRun(failed, "f00000000003", store.RunOK, 20*time.Second, 70)
	addRun(failed, "f00000000004", store.RunStoppedAtLimit, 5*time.Minute, 60)
	addRun(failed, "f00000000005", store.RunSkipped, 0, 10) // a newer skipped slot doesn't hide the failure
	addRun(running, "r00000000001", store.RunRunning, 0, 1)
	addRun(early, "e00000000001", store.RunOK, time.Second, 30)

	list, err := l.svc.ListSchedules(ctx, ScheduleFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, sc := range list {
		names = append(names, sc.App+"/"+sc.Name)
	}
	// Failed first (failed's last run hit its limit, db's last backup failed),
	// then running (files' backup is going), each group by next run: today
	// 08:00 before tomorrow 02:30, today 07:00 before tomorrow 03:00 (the
	// default); then the rest by next run; the paused one last, though its
	// last run failed.
	want := []string{"whoami/failed", "shop/db-backup", "whoami/running", "shop/files-backup",
		"whoami/early", "whoami/never", "whoami/paused"}
	if !slices.Equal(names, want) {
		t.Fatalf("order\n got %v\nwant %v", names, want)
	}

	byName := map[string]Schedule{}
	for _, sc := range list {
		byName[sc.App+"/"+sc.Name] = sc
	}
	t.Run("derived values", func(t *testing.T) {
		f := byName["whoami/failed"]
		if f.LastRun == nil || f.LastRun.Status != store.RunStoppedAtLimit || f.RunningNow != 0 {
			t.Errorf("failed: last %+v running %d", f.LastRun, f.RunningNow)
		}
		// The ok runs took 10, 30 and 20 s: the median is 20.
		if f.UsualSeconds == nil || *f.UsualSeconds != 20 {
			t.Errorf("failed: usual %v, want 20", f.UsualSeconds)
		}
		if r := byName["whoami/running"]; r.RunningNow != 1 || r.LastRun == nil || r.LastRun.Status != store.RunRunning {
			t.Errorf("running: %+v", r)
		}
		if n := byName["whoami/never"]; n.LastRun != nil || n.UsualSeconds != nil || !n.NextRunAt.Equal(t0.Add(6*time.Hour)) {
			t.Errorf("never: %+v", n)
		}
	})
	t.Run("built-in rows", func(t *testing.T) {
		db := byName["shop/db-backup"]
		if db.ID != "backup:shop:db" || !db.Builtin || db.Engine != app.BackupPostgres || db.Words != "Every day at 02:30 UTC" ||
			!db.NextRunAt.Equal(time.Date(2026, 10, 16, 2, 30, 0, 0, time.UTC)) || db.LastRun == nil ||
			db.LastRun.Status != store.RunFailed || db.LastRun.Error != "pg_dump failed" || db.Project != "default" {
			t.Errorf("db: %+v (last %+v)", db, db.LastRun)
		}
		files := byName["shop/files-backup"]
		if files.Engine != app.BackupVolume || files.Cron != app.DefaultBackupSchedule || files.RunningNow != 1 {
			t.Errorf("files: %+v", files)
		}
		runs, err := l.svc.ListScheduleRuns(ctx, "backup:shop:db", 0)
		if err != nil || len(runs) != 2 || runs[0].ID != "db-2" || runs[1].Status != store.RunOK || *runs[1].DurationSeconds != 40 {
			t.Errorf("db runs = %+v, %v", runs, err)
		}
		if one, err := l.svc.GetScheduleRun(ctx, "backup:shop:db", "db-1"); err != nil || one.Status != store.RunOK {
			t.Errorf("GetScheduleRun(db-1) = %+v, %v", one, err)
		}
		if _, _, err := l.svc.GetScheduleRunOutput(ctx, "backup:shop:db", "db-1", "stdout"); !errors.Is(err, ErrOutputNotKept) {
			t.Errorf("a backup's output = %v, want ErrOutputNotKept", err)
		}
		if _, err := l.svc.GetScheduleRun(ctx, "backup:shop:db", "nope"); !errors.Is(err, ErrRunNotFound) {
			t.Errorf("an unknown backup run = %v", err)
		}
		if _, err := l.svc.GetSchedule(ctx, "backup:shop:web"); !errors.Is(err, ErrScheduleNotFound) {
			t.Errorf("a service without a backup = %v", err)
		}
		in := NewSchedule{App: "shop", Name: "x", Command: []string{"x"}, Cron: "0 1 * * *"}
		for name, err := range map[string]error{
			"update": func() error { _, err := l.svc.UpdateSchedule(ctx, "backup:shop:db", in); return err }(),
			"delete": l.svc.DeleteSchedule(ctx, "backup:shop:db"),
			"pause":  func() error { _, err := l.svc.PauseSchedule(ctx, "backup:shop:db"); return err }(),
			"resume": func() error { _, err := l.svc.ResumeSchedule(ctx, "backup:shop:db"); return err }(),
			"run":    func() error { _, err := l.svc.RunScheduleNow(ctx, "backup:shop:db"); return err }(),
			"stop":   func() error { _, err := l.svc.StopScheduleRun(ctx, "backup:shop:db", "db-1"); return err }(),
		} {
			if !errors.Is(err, ErrScheduleBuiltin) {
				t.Errorf("%s of a built-in = %v, want ErrScheduleBuiltin", name, err)
			}
		}
		// A stopped app's backup has no next run.
		if err := l.store.SetAppStopped(ctx, "shop", new(t0)); err != nil {
			t.Fatal(err)
		}
		if sc, _ := l.svc.GetSchedule(ctx, "backup:shop:db"); sc.NextRunAt != nil {
			t.Errorf("a stopped app's backup runs next at %v", sc.NextRunAt)
		}
	})
	t.Run("filters", func(t *testing.T) {
		count := func(f ScheduleFilter) int {
			list, err := l.svc.ListSchedules(ctx, f)
			if err != nil {
				t.Fatal(err)
			}
			return len(list)
		}
		for f, want := range map[ScheduleFilter]int{
			{App: "whoami"}: 5, {App: "shop"}: 2, {App: "nope"}: 0,
			{Project: "default"}: 7, {Project: "nope"}: 0, {App: "shop", Project: "default"}: 2,
		} {
			if got := count(f); got != want {
				t.Errorf("ListSchedules(%+v) = %d, want %d", f, got, want)
			}
		}
	})
}

func TestScheduleRunOutputReads(t *testing.T) {
	ctx := context.Background()
	l := newSchedLab(t)
	sc := l.create("out", "0 3 * * *")
	other := l.create("other", "0 4 * * *")
	r := store.ScheduleRunRecord{ID: "aaaaaaaaaaa1", ScheduleID: sc.ID, AppID: "whoami", Service: "web",
		Trigger: RunTriggerOwner, Command: []string{"x"}, Status: store.RunRunning, StartedAt: l.clock.Now()}
	if err := l.store.InsertRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.svc.GetScheduleRunOutput(ctx, sc.ID, r.ID, "stdout"); !errors.Is(err, ErrOutputNotKept) {
		t.Errorf("output of a run going = %v, want ErrOutputNotKept", err)
	}
	if err := l.store.SettleRun(ctx, r.ID, store.RunSettlement{Status: store.RunOK, FinishedAt: l.clock.Now(),
		Stdout: []byte("out\n"), Stderr: []byte("err\n"), StderrCut: true, OutputKept: true}); err != nil {
		t.Fatal(err)
	}
	if out, cut, err := l.svc.GetScheduleRunOutput(ctx, sc.ID, r.ID, "stderr"); err != nil || string(out) != "err\n" || !cut {
		t.Errorf("stderr = %q, %v, %v", out, cut, err)
	}
	if _, _, err := l.svc.GetScheduleRunOutput(ctx, sc.ID, r.ID, "both"); err == nil {
		t.Error("an unknown stream was read")
	}
	if _, _, err := l.svc.GetScheduleRunOutput(ctx, other.ID, r.ID, "stdout"); !errors.Is(err, ErrRunNotFound) {
		t.Errorf("a run read through another schedule = %v, want ErrRunNotFound", err)
	}
	got, err := l.svc.GetScheduleRun(ctx, sc.ID, r.ID)
	if err != nil || !got.OutputKept || got.StdoutBytes != 4 || got.StderrBytes != 4 || !got.StderrCut {
		t.Errorf("GetScheduleRun = %+v, %v", got, err)
	}
}

func TestMedianAndDuration(t *testing.T) {
	for _, tt := range []struct {
		in   []int
		want int
		ok   bool
	}{{nil, 0, false}, {[]int{5}, 5, true}, {[]int{9, 1, 5}, 5, true}, {[]int{4, 1, 3, 2}, 2, true}} {
		if got, ok := median(tt.in); got != tt.want || ok != tt.ok {
			t.Errorf("median(%v) = %d, %v", tt.in, got, ok)
		}
	}
	start := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
	if d := durationOf(start, nil, nil); d != nil {
		t.Errorf("a run going has a duration %d", *d)
	}
	if d := durationOf(start, new(start.Add(10*time.Second)), new(start.Add(70*time.Second))); *d != 60 {
		t.Errorf("duration from the task's start = %d, want 60", *d)
	}
	if d := durationOf(start, nil, new(start.Add(1500*time.Millisecond))); *d != 2 {
		t.Errorf("duration without the task's start = %d, want 2", *d)
	}
}
