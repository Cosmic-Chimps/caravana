package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app/apptest"
)

// scheduleFixture makes the app the schedules belong to and returns its ID.
func scheduleFixture(t *testing.T, s *Store) string {
	t.Helper()
	a := apptest.Whoami().App
	if _, err := s.SaveApp(context.Background(), a, nil); err != nil {
		t.Fatal(err)
	}
	return a.ID
}

func testSchedule(appID, id, name string) ScheduleRecord {
	return ScheduleRecord{
		ID: id, AppID: appID, Service: "web", Name: name,
		Command:   []string{"sh", "-c", "echo 'a b'"},
		Cron:      "*/5 * * * *",
		StopAfter: 30 * time.Minute, MemoryMB: 512, SkipIfRunning: true,
		ArmedAt: time.Date(2026, 10, 23, 8, 0, 0, 0, time.UTC),
	}
}

func TestScheduleRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	appID := scheduleFixture(t, s)

	in := testSchedule(appID, "aaaaaaaaaaaa", "nightly")
	got, err := s.InsertSchedule(ctx, in)
	if err != nil {
		t.Fatalf("InsertSchedule: %v", err)
	}
	if !got.ArmedAt.Equal(in.ArmedAt) || got.CreatedAt.IsZero() || !got.UpdatedAt.Equal(got.CreatedAt) {
		t.Errorf("times = armed %v created %v updated %v", got.ArmedAt, got.CreatedAt, got.UpdatedAt)
	}
	in.CreatedAt, in.UpdatedAt = got.CreatedAt, got.UpdatedAt
	if !reflect.DeepEqual(got, in) {
		t.Errorf("round trip changed the schedule\n got: %+v\nwant: %+v", got, in)
	}

	t.Run("update changes what the owner edits and keeps the pause and the base", func(t *testing.T) {
		paused := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
		if err := s.SetSchedulePaused(ctx, in.ID, &paused); err != nil {
			t.Fatal(err)
		}
		edit := in
		edit.Name, edit.Service, edit.Command = "weekly", "worker", []string{"/bin/job"}
		edit.Cron, edit.StopAfter, edit.MemoryMB, edit.SkipIfRunning = "0 3 * * 1", time.Hour, 2048, false
		edit.PausedAt, edit.ArmedAt = nil, time.Time{} // an edit can't resume or re-arm
		upd, err := s.UpdateSchedule(ctx, edit)
		if err != nil {
			t.Fatalf("UpdateSchedule: %v", err)
		}
		if upd.Name != "weekly" || upd.Service != "worker" || !reflect.DeepEqual(upd.Command, []string{"/bin/job"}) ||
			upd.Cron != "0 3 * * 1" || upd.StopAfter != time.Hour || upd.MemoryMB != 2048 || upd.SkipIfRunning {
			t.Errorf("update = %+v", upd)
		}
		if upd.PausedAt == nil || !upd.PausedAt.Equal(paused) || !upd.ArmedAt.Equal(in.ArmedAt) {
			t.Errorf("update changed the pause or base: paused %v armed %v", upd.PausedAt, upd.ArmedAt)
		}
		if !upd.UpdatedAt.After(upd.CreatedAt) {
			t.Errorf("UpdatedAt %v not after CreatedAt %v", upd.UpdatedAt, upd.CreatedAt)
		}
	})

	t.Run("pause, resume and arm", func(t *testing.T) {
		if err := s.SetSchedulePaused(ctx, in.ID, nil); err != nil {
			t.Fatal(err)
		}
		at := time.Date(2026, 10, 24, 1, 2, 3, 4, time.UTC)
		if err := s.ArmSchedule(ctx, in.ID, at); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetSchedule(ctx, in.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.PausedAt != nil || !got.ArmedAt.Equal(at) {
			t.Errorf("paused %v armed %v, want not paused and %v", got.PausedAt, got.ArmedAt, at)
		}
	})

	t.Run("a missing schedule is ErrNotFound everywhere", func(t *testing.T) {
		now := time.Now()
		for name, err := range map[string]error{
			"get":    func() error { _, err := s.GetSchedule(ctx, "nope"); return err }(),
			"update": func() error { _, err := s.UpdateSchedule(ctx, testSchedule(appID, "nope", "x")); return err }(),
			"delete": s.DeleteSchedule(ctx, "nope"),
			"pause":  s.SetSchedulePaused(ctx, "nope", &now),
			"arm":    s.ArmSchedule(ctx, "nope", now),
		} {
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("%s = %v, want ErrNotFound", name, err)
			}
		}
	})
}

func TestScheduleNamesAreUniquePerApp(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	appID := scheduleFixture(t, s)
	other := apptest.Whoami().App
	other.ID = "other"
	if _, err := s.SaveApp(ctx, other, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := s.InsertSchedule(ctx, testSchedule(appID, "aaaaaaaaaaaa", "nightly")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertSchedule(ctx, testSchedule(appID, "bbbbbbbbbbbb", "nightly")); !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("same name in the same app = %v, want ErrAlreadyExists", err)
	}
	if _, err := s.InsertSchedule(ctx, testSchedule(appID, "aaaaaaaaaaaa", "other-name")); !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("same ID = %v, want ErrAlreadyExists", err)
	}
	if _, err := s.InsertSchedule(ctx, testSchedule(other.ID, "cccccccccccc", "nightly")); err != nil {
		t.Errorf("same name in another app = %v, want ok", err)
	}
	if _, err := s.InsertSchedule(ctx, testSchedule("ghost", "dddddddddddd", "x")); err == nil {
		t.Error("a schedule of an unknown app was stored")
	}

	if _, err := s.InsertSchedule(ctx, testSchedule(appID, "eeeeeeeeeeee", "weekly")); err != nil {
		t.Fatal(err)
	}
	renamed := testSchedule(appID, "eeeeeeeeeeee", "nightly")
	if _, err := s.UpdateSchedule(ctx, renamed); !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("rename onto a taken name = %v, want ErrAlreadyExists", err)
	}

	all, err := s.ListSchedules(ctx, "")
	if err != nil || len(all) != 3 {
		t.Fatalf("ListSchedules(all) = %d, %v; want 3", len(all), err)
	}
	mine, err := s.ListSchedules(ctx, appID)
	if err != nil || len(mine) != 2 || mine[0].Name != "nightly" || mine[1].Name != "weekly" {
		t.Errorf("ListSchedules(app) = %+v, %v; want nightly, weekly", mine, err)
	}
}

// run makes a run row of a schedule, started n seconds after a base time.
func run(scheduleID, appID, id, status string, n int) ScheduleRunRecord {
	at := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC).Add(time.Duration(n) * time.Second)
	r := ScheduleRunRecord{
		ID: id, ScheduleID: scheduleID, AppID: appID, Service: "web", Trigger: "timer",
		Command: []string{"echo", "hi"}, Status: status, StartedAt: at,
	}
	if status != RunStarting && status != RunRunning {
		r.FinishedAt = &at
	}
	return r
}

func TestScheduleRunRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	appID := scheduleFixture(t, s)
	sc, err := s.InsertSchedule(ctx, testSchedule(appID, "aaaaaaaaaaaa", "nightly"))
	if err != nil {
		t.Fatal(err)
	}

	slot := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	in := run(sc.ID, appID, "r00000000001", RunStarting, 1)
	in.SlotAt = &slot
	in.DeploymentID, in.JobVersion = "dep_1", new(4)
	if err := s.InsertRun(ctx, in); err != nil {
		t.Fatalf("InsertRun: %v", err)
	}
	if err := s.InsertRun(ctx, in); !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("same run twice = %v, want ErrAlreadyExists", err)
	}
	got, err := s.GetRun(ctx, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, in) {
		t.Errorf("round trip changed the run\n got: %+v\nwant: %+v", got, in)
	}

	if err := s.SetRunRunning(ctx, in.ID); err != nil {
		t.Fatal(err)
	}
	intentAt := in.StartedAt.Add(time.Minute)
	if err := s.SetRunStopIntent(ctx, in.ID, "limit", intentAt); err != nil {
		t.Fatal(err)
	}
	// The first intent stays.
	if err := s.SetRunStopIntent(ctx, in.ID, "owner", intentAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetRun(ctx, in.ID)
	if got.Status != RunRunning || got.StopReason != "limit" || got.StopRequestedAt == nil || !got.StopRequestedAt.Equal(intentAt) {
		t.Errorf("after intent: status %s reason %q at %v", got.Status, got.StopReason, got.StopRequestedAt)
	}

	taskStart := in.StartedAt.Add(5 * time.Second)
	done := in.StartedAt.Add(2 * time.Minute)
	st := RunSettlement{
		Status: RunStoppedAtLimit, ExitCode: new(143), Error: "stopped after 1 minute",
		TaskStartedAt: &taskStart, FinishedAt: done,
		Stdout: []byte("out\n"), Stderr: []byte("err\n"), StdoutCut: true, StderrCut: false, OutputKept: true,
	}
	if err := s.SettleRun(ctx, in.ID, st); err != nil {
		t.Fatalf("SettleRun: %v", err)
	}
	got, err = s.GetRun(ctx, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := in
	want.Status, want.ExitCode, want.Error = RunStoppedAtLimit, new(143), "stopped after 1 minute"
	want.StopReason, want.StopRequestedAt = "limit", &intentAt
	want.TaskStartedAt, want.FinishedAt = &taskStart, &done
	want.Stdout, want.Stderr, want.StdoutCut, want.OutputKept = "out\n", "err\n", true, true
	want.StdoutBytes, want.StderrBytes = 4, 4
	if !reflect.DeepEqual(got, want) {
		t.Errorf("settled run\n got: %+v\nwant: %+v", got, want)
	}

	t.Run("a settled run stays settled", func(t *testing.T) {
		for name, err := range map[string]error{
			"settle":  s.SettleRun(ctx, in.ID, RunSettlement{Status: RunOK, FinishedAt: done}),
			"running": s.SetRunRunning(ctx, in.ID),
			"intent":  s.SetRunStopIntent(ctx, in.ID, "owner", done),
		} {
			if !errors.Is(err, ErrInvalidTransition) {
				t.Errorf("%s = %v, want ErrInvalidTransition", name, err)
			}
		}
		if err := s.SetRunRunning(ctx, "nope"); !errors.Is(err, ErrNotFound) {
			t.Errorf("unknown run = %v, want ErrNotFound", err)
		}
	})

	t.Run("a status a run doesn't end with is refused", func(t *testing.T) {
		open := run(sc.ID, appID, "r00000000002", RunRunning, 2)
		if err := s.InsertRun(ctx, open); err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{RunSkipped, RunRunning, "nonsense"} {
			if err := s.SettleRun(ctx, open.ID, RunSettlement{Status: bad, FinishedAt: done}); err == nil {
				t.Errorf("settled as %q", bad)
			}
		}
	})

	t.Run("output not kept is stored as nothing", func(t *testing.T) {
		r := run(sc.ID, appID, "r00000000003", RunRunning, 3)
		if err := s.InsertRun(ctx, r); err != nil {
			t.Fatal(err)
		}
		err := s.SettleRun(ctx, r.ID, RunSettlement{
			Status: RunFailed, Error: "exit 1", ExitCode: new(1), FinishedAt: done,
			Stdout: []byte("must not be kept"), StdoutCut: true, OutputKept: false,
		})
		if err != nil {
			t.Fatal(err)
		}
		got, _ := s.GetRun(ctx, r.ID)
		if got.OutputKept || got.Stdout != "" || got.StdoutCut {
			t.Errorf("run kept output it was told not to: %+v", got)
		}
		var stdout *string
		if err := s.db.QueryRowContext(ctx, "SELECT stdout FROM schedule_runs WHERE id = ?", r.ID).Scan(&stdout); err != nil || stdout != nil {
			t.Errorf("stdout column = %v, %v; want NULL", stdout, err)
		}
	})

	t.Run("a skipped slot keeps its reason", func(t *testing.T) {
		r := run(sc.ID, appID, "r00000000004", RunSkipped, 4)
		r.Error = "a run was still going"
		if err := s.InsertRun(ctx, r); err != nil {
			t.Fatal(err)
		}
		got, _ := s.GetRun(ctx, r.ID)
		if got.Status != RunSkipped || got.Error != r.Error || got.FinishedAt == nil {
			t.Errorf("skipped run = %+v", got)
		}
	})

	t.Run("the CHECK refuses an unknown status", func(t *testing.T) {
		if err := s.InsertRun(ctx, run(sc.ID, appID, "r00000000005", "weird", 5)); err == nil {
			t.Error("stored a run with status weird")
		}
	})

	t.Run("lists leave the output out and GetRun has it", func(t *testing.T) {
		list, err := s.ListRuns(ctx, sc.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range list {
			if r.Stdout != "" || r.Stderr != "" {
				t.Errorf("run %s listed with output", r.ID)
			}
			// The sizes come with the list; the settled run kept "out\n" and "err\n".
			if r.ID == in.ID && (r.StdoutBytes != 4 || r.StderrBytes != 4) {
				t.Errorf("run %s listed with %d and %d bytes, want 4 and 4", r.ID, r.StdoutBytes, r.StderrBytes)
			}
		}
		if len(list) < 2 || !list[0].StartedAt.After(list[1].StartedAt) {
			t.Errorf("ListRuns = %d runs, not newest first", len(list))
		}
		if two, _ := s.ListRuns(ctx, sc.ID, 2); len(two) != 2 {
			t.Errorf("ListRuns(limit 2) = %d runs", len(two))
		}
	})
}

func TestOpenRuns(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	appID := scheduleFixture(t, s)
	a, err := s.InsertSchedule(ctx, testSchedule(appID, "aaaaaaaaaaaa", "a"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.InsertSchedule(ctx, testSchedule(appID, "bbbbbbbbbbbb", "b"))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []ScheduleRunRecord{
		run(a.ID, appID, "r1", RunStarting, 1),
		run(a.ID, appID, "r2", RunOK, 2),
		run(a.ID, appID, "r3", RunRunning, 3),
		run(b.ID, appID, "r4", RunRunning, 0),
		run(b.ID, appID, "r5", RunFailed, 5),
		run(b.ID, appID, "r6", RunSkipped, 6),
	} {
		if err := s.InsertRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(rs []ScheduleRunRecord) string {
		var out []string
		for _, r := range rs {
			out = append(out, r.ID)
		}
		return strings.Join(out, ",")
	}

	all, err := s.AllOpenRuns(ctx)
	if err != nil || ids(all) != "r4,r1,r3" {
		t.Errorf("AllOpenRuns = %s, %v; want r4,r1,r3 (oldest first)", ids(all), err)
	}
	mine, err := s.OpenRuns(ctx, a.ID)
	if err != nil || ids(mine) != "r3,r1" {
		t.Errorf("OpenRuns(a) = %s, %v; want r3,r1 (newest first)", ids(mine), err)
	}
	if !all[0].Open() {
		t.Error("an open run says it isn't")
	}
}

func TestLatestScheduleRuns(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	appID := scheduleFixture(t, s)
	failing, err := s.InsertSchedule(ctx, testSchedule(appID, "aaaaaaaaaaaa", "failing"))
	if err != nil {
		t.Fatal(err)
	}
	healed, err := s.InsertSchedule(ctx, testSchedule(appID, "bbbbbbbbbbbb", "healed"))
	if err != nil {
		t.Fatal(err)
	}
	empty, err := s.InsertSchedule(ctx, testSchedule(appID, "cccccccccccc", "empty"))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []ScheduleRunRecord{
		// failing: an ok run, then a failed one, then newer skipped, stopped and open rows.
		run(failing.ID, appID, "f1", RunOK, 1),
		run(failing.ID, appID, "f2", RunFailed, 2),
		run(failing.ID, appID, "f3", RunSkipped, 3),
		run(failing.ID, appID, "f4", RunStopped, 4),
		run(failing.ID, appID, "f5", RunRunning, 5),
		// healed: a failure, then a newer success.
		run(healed.ID, appID, "h1", RunFailed, 1),
		run(healed.ID, appID, "h2", RunOK, 2),
		run(healed.ID, appID, "h3", RunSkipped, 3),
	} {
		if err := s.InsertRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	// A run that never started counts, and so does one stopped at its limit.
	if err := s.InsertRun(ctx, run(empty.ID, appID, "e1", RunCouldNotStart, 1)); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertRun(ctx, run(empty.ID, appID, "e2", RunStopped, 2)); err != nil {
		t.Fatal(err)
	}

	got, err := s.LatestScheduleRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]string{}
	for _, r := range got {
		byID[r.ScheduleID] = r.ID
	}
	want := map[string]string{failing.ID: "f2", healed.ID: "h2", empty.ID: "e1"}
	if !reflect.DeepEqual(byID, want) {
		t.Errorf("LatestScheduleRuns = %v, want %v", byID, want)
	}
}

func TestSkipStreak(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	appID := scheduleFixture(t, s)
	for i, c := range []struct {
		name     string
		statuses []string // oldest first
		want     int
	}{
		{"no runs", nil, 0},
		{"newest is not skipped", []string{RunSkipped, RunSkipped, RunOK}, 0},
		{"two skipped after a run", []string{RunOK, RunSkipped, RunSkipped}, 2},
		{"three skipped", []string{RunFailed, RunSkipped, RunSkipped, RunSkipped}, 3},
		{"more than three is capped", []string{RunSkipped, RunSkipped, RunSkipped, RunSkipped, RunSkipped}, 3},
		{"a run between breaks the streak", []string{RunSkipped, RunOK, RunSkipped}, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			sc, err := s.InsertSchedule(ctx, testSchedule(appID, strings.Repeat(string(rune('a'+i)), 12), "s"+string(rune('a'+i))))
			if err != nil {
				t.Fatal(err)
			}
			for j, status := range c.statuses {
				id := sc.ID[:6] + strings.Repeat(string(rune('a'+j)), 6)
				if err := s.InsertRun(ctx, run(sc.ID, appID, id, status, j)); err != nil {
					t.Fatal(err)
				}
			}
			if got, err := s.SkipStreak(ctx, sc.ID); err != nil || got != c.want {
				t.Errorf("SkipStreak = %d, %v; want %d", got, err, c.want)
			}
		})
	}
}

func TestPruneRuns(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	appID := scheduleFixture(t, s)
	sc, err := s.InsertSchedule(ctx, testSchedule(appID, "aaaaaaaaaaaa", "a"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.InsertSchedule(ctx, testSchedule(appID, "bbbbbbbbbbbb", "b"))
	if err != nil {
		t.Fatal(err)
	}
	// r1 is an old run still going: pruning never deletes an open run.
	for i, status := range []string{RunRunning, RunOK, RunOK, RunFailed, RunOK, RunOK} {
		if err := s.InsertRun(ctx, run(sc.ID, appID, "r"+string(rune('1'+i)), status, i+1)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.InsertRun(ctx, run(other.ID, appID, "o1", RunOK, 1)); err != nil {
		t.Fatal(err)
	}

	n, err := s.PruneRuns(ctx, sc.ID, 3)
	if err != nil || n != 2 {
		t.Fatalf("PruneRuns = %d, %v; want 2 deleted (r2, r3)", n, err)
	}
	left, _ := s.ListRuns(ctx, sc.ID, 0)
	var ids []string
	for _, r := range left {
		ids = append(ids, r.ID)
	}
	if want := []string{"r6", "r5", "r4", "r1"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("runs left = %v, want %v", ids, want)
	}
	if rs, _ := s.ListRuns(ctx, other.ID, 0); len(rs) != 1 {
		t.Errorf("another schedule's runs = %d, want 1", len(rs))
	}
	if n, err := s.PruneRuns(ctx, sc.ID, 100); err != nil || n != 0 {
		t.Errorf("PruneRuns(100) = %d, %v; want 0", n, err)
	}
}

func TestDeleteScheduleDeletesItsRuns(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	appID := scheduleFixture(t, s)
	a, _ := s.InsertSchedule(ctx, testSchedule(appID, "aaaaaaaaaaaa", "a"))
	b, _ := s.InsertSchedule(ctx, testSchedule(appID, "bbbbbbbbbbbb", "b"))
	for _, r := range []ScheduleRunRecord{
		run(a.ID, appID, "r1", RunOK, 1), run(a.ID, appID, "r2", RunRunning, 2), run(b.ID, appID, "r3", RunOK, 3),
	} {
		if err := s.InsertRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteSchedule(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetRun(ctx, "r1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetRun(r1) = %v, want ErrNotFound", err)
	}
	if open, _ := s.AllOpenRuns(ctx); len(open) != 0 {
		t.Errorf("open runs left = %d, want 0", len(open))
	}
	if _, err := s.GetRun(ctx, "r3"); err != nil {
		t.Errorf("another schedule's run is gone: %v", err)
	}
}

func TestDeleteAppDeletesSchedules(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	appID := scheduleFixture(t, s)
	sc, _ := s.InsertSchedule(ctx, testSchedule(appID, "aaaaaaaaaaaa", "a"))
	if err := s.InsertRun(ctx, run(sc.ID, appID, "r1", RunOK, 1)); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteApp(ctx, appID); err != nil {
		t.Fatalf("DeleteApp with schedules: %v", err)
	}
	if _, err := s.GetSchedule(ctx, sc.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSchedule = %v, want ErrNotFound", err)
	}
	if _, err := s.GetRun(ctx, "r1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetRun = %v, want ErrNotFound", err)
	}
}
