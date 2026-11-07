package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

var urT0 = time.Date(2026, 11, 6, 14, 2, 41, 123456789, time.UTC)

func newRun(id, version string, at time.Time) UpdateRun {
	return UpdateRun{ID: id, Version: version, FromVersion: "0.16.0", Trigger: UpdateByConsole, RequestedAt: at}
}

func TestUpdateRuns(t *testing.T) {
	ctx := context.Background()

	t.Run("a new store has none", func(t *testing.T) {
		s := openTestStore(t)
		if _, ok, err := s.OpenUpdateRun(ctx); err != nil || ok {
			t.Errorf("OpenUpdateRun() = %v, %v; want none", ok, err)
		}
		if _, ok, err := s.LatestFinishedUpdateRun(ctx); err != nil || ok {
			t.Errorf("LatestFinishedUpdateRun() = %v, %v; want none", ok, err)
		}
		if got, err := s.UpdateRuns(ctx, 20); err != nil || len(got) != 0 {
			t.Errorf("UpdateRuns() = %v, %v; want none", got, err)
		}
		if ok, err := s.UpdateRunExists(ctx, "0.17.0"); err != nil || ok {
			t.Errorf("UpdateRunExists() = %v, %v; want false", ok, err)
		}
	})

	t.Run("insert, open, settle", func(t *testing.T) {
		s := openTestStore(t)
		r := newRun("aaaa", "0.17.0", urT0)
		r.Trigger, r.TokenName = UpdateByAPI, "ci"
		got, err := s.InsertUpdateRun(ctx, r)
		if err != nil || got.State != UpdateRequested {
			t.Fatalf("InsertUpdateRun() = %+v, %v; want state requested", got, err)
		}
		open, ok, err := s.OpenUpdateRun(ctx)
		if err != nil || !ok {
			t.Fatalf("OpenUpdateRun() = %v, %v; want the run", ok, err)
		}
		if open.ID != "aaaa" || open.Version != "0.17.0" || open.FromVersion != "0.16.0" || open.Trigger != UpdateByAPI ||
			open.TokenName != "ci" || !open.RequestedAt.Equal(urT0) || !open.StartedAt.IsZero() || !open.FinishedAt.IsZero() || open.Log != "" {
			t.Errorf("open run = %+v, lost a field", open)
		}

		started := urT0.Add(19 * time.Second)
		if err := s.SetUpdateRunState(ctx, "aaaa", UpdateRunning, started, time.Time{}, "", ""); err != nil {
			t.Fatal(err)
		}
		open, _, _ = s.OpenUpdateRun(ctx)
		if open.State != UpdateRunning || !open.StartedAt.Equal(started) || !open.FinishedAt.IsZero() {
			t.Errorf("running run = %+v", open)
		}

		finished := urT0.Add(2 * time.Minute)
		if err := s.SetUpdateRunState(ctx, "aaaa", UpdateRolledBack, time.Time{}, finished, "didn't start", "/var/lib/caravana-update/logs/aaaa.log"); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := s.OpenUpdateRun(ctx); ok {
			t.Error("a final run is still open")
		}
		done, ok, err := s.LatestFinishedUpdateRun(ctx)
		if err != nil || !ok {
			t.Fatalf("LatestFinishedUpdateRun() = %v, %v", ok, err)
		}
		if done.State != UpdateRolledBack || !done.StartedAt.Equal(started) || !done.FinishedAt.Equal(finished) ||
			done.Reason != "didn't start" || !strings.HasSuffix(done.Log, "aaaa.log") {
			t.Errorf("finished run = %+v; started must stay when only the end is set", done)
		}
		if ok, _ := s.UpdateRunExists(ctx, "0.17.0"); !ok {
			t.Error("UpdateRunExists(0.17.0) = false, want true")
		}
		if ok, _ := s.UpdateRunExists(ctx, "0.17.1"); ok {
			t.Error("UpdateRunExists(0.17.1) = true, want false")
		}
	})

	t.Run("times keep nine fraction digits", func(t *testing.T) {
		s := openTestStore(t)
		if _, err := s.InsertUpdateRun(ctx, newRun("aaaa", "0.17.0", urT0)); err != nil {
			t.Fatal(err)
		}
		var at string
		if err := s.db.QueryRowContext(ctx, "SELECT requested_at FROM update_runs").Scan(&at); err != nil || at != "2026-11-06T14:02:41.123456789Z" {
			t.Errorf("stored time = %q, %v; want nine fraction digits", at, err)
		}
	})

	t.Run("newest first, limited", func(t *testing.T) {
		s := openTestStore(t)
		for i, id := range []string{"a", "b", "c"} {
			if _, err := s.InsertUpdateRun(ctx, newRun(id, "0.17.0", urT0.Add(time.Duration(i)*time.Hour))); err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.UpdateRuns(ctx, 2)
		if err != nil || len(got) != 2 || got[0].ID != "c" || got[1].ID != "b" {
			t.Errorf("UpdateRuns(2) = %v, %v; want c, b", got, err)
		}
		if _, err := s.UpdateRuns(ctx, 0); err == nil {
			t.Error("UpdateRuns(0) = nil error, want one")
		}
	})

	t.Run("the latest finished is the one that finished last", func(t *testing.T) {
		s := openTestStore(t)
		for _, id := range []string{"a", "b"} {
			if _, err := s.InsertUpdateRun(ctx, newRun(id, "0.17.0", urT0)); err != nil {
				t.Fatal(err)
			}
		}
		_ = s.SetUpdateRunState(ctx, "a", UpdateFailed, time.Time{}, urT0.Add(time.Hour), "x", "")
		_ = s.SetUpdateRunState(ctx, "b", UpdateUpdated, time.Time{}, urT0.Add(time.Minute), "", "")
		if got, _, _ := s.LatestFinishedUpdateRun(ctx); got.ID != "a" {
			t.Errorf("LatestFinishedUpdateRun() = %q, want a", got.ID)
		}
	})

	t.Run("an ID in use, an unknown ID, a bad state or trigger", func(t *testing.T) {
		s := openTestStore(t)
		if _, err := s.InsertUpdateRun(ctx, newRun("a", "0.17.0", urT0)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.InsertUpdateRun(ctx, newRun("a", "0.17.1", urT0)); !errors.Is(err, ErrAlreadyExists) {
			t.Errorf("same ID = %v, want ErrAlreadyExists", err)
		}
		if err := s.SetUpdateRunState(ctx, "nope", UpdateUpdated, time.Time{}, urT0, "", ""); !errors.Is(err, ErrNotFound) {
			t.Errorf("SetUpdateRunState(unknown) = %v, want ErrNotFound", err)
		}
		if err := s.SetUpdateRunState(ctx, "a", "weird", time.Time{}, time.Time{}, "", ""); err == nil {
			t.Error("a state outside the seven was accepted")
		}
		bad := newRun("b", "0.17.0", urT0)
		bad.Trigger = "cron"
		if _, err := s.InsertUpdateRun(ctx, bad); err == nil {
			t.Error("a trigger outside the three was accepted")
		}
	})

	t.Run("delete", func(t *testing.T) {
		s := openTestStore(t)
		_, _ = s.InsertUpdateRun(ctx, newRun("a", "0.17.0", urT0))
		if err := s.DeleteUpdateRun(ctx, "a"); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteUpdateRun(ctx, "a"); err != nil {
			t.Errorf("deleting twice = %v, want nil", err)
		}
		if got, _ := s.UpdateRuns(ctx, 5); len(got) != 0 {
			t.Errorf("runs after delete = %v", got)
		}
	})
}

func TestAutoUpdateSetting(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	rc, err := s.ReleaseCheck(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rc.AutoUpdate || rc.AutoUpdateHour != 3 {
		t.Errorf("a new store = auto %v at %d; want off at 3", rc.AutoUpdate, rc.AutoUpdateHour)
	}
	if err := s.SetAutoUpdate(ctx, true, 5); err != nil {
		t.Fatal(err)
	}
	rc, _ = s.ReleaseCheck(ctx)
	if !rc.AutoUpdate || rc.AutoUpdateHour != 5 || !rc.Enabled {
		t.Errorf("after set = %+v; want on at 5, the check still on", rc)
	}
	// A check's result doesn't undo the setting.
	rc.LatestTag = "v0.16.3"
	if err := s.SaveReleaseResult(ctx, rc, nil); err != nil {
		t.Fatal(err)
	}
	rc, _ = s.ReleaseCheck(ctx)
	if !rc.AutoUpdate || rc.AutoUpdateHour != 5 {
		t.Errorf("after a check result = auto %v at %d; want on at 5", rc.AutoUpdate, rc.AutoUpdateHour)
	}
	if err := s.SetAutoUpdate(ctx, true, 24); err == nil {
		t.Error("hour 24 was accepted")
	}
	if err := s.SetAutoUpdate(ctx, true, -1); err == nil {
		t.Error("hour -1 was accepted")
	}
}

// An update that went through is one row of the feed, and the version change it explains is not
// listed again; a run that didn't go through, and a version change nothing explains, are.
func TestActivityUpdateRuns(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	base := time.Date(2026, 11, 6, 14, 0, 0, 0, time.UTC)

	for _, r := range []UpdateRun{
		{ID: "ok", Version: "0.17.0", FromVersion: "0.16.0", Trigger: UpdateByConsole, RequestedAt: base},
		{ID: "bad", Version: "0.17.1", FromVersion: "0.17.0", Trigger: UpdateByAPI, TokenName: "ci", RequestedAt: base.Add(2 * time.Hour)},
		{ID: "open", Version: "0.17.2", FromVersion: "0.17.0", Trigger: UpdateByConsole, RequestedAt: base.Add(5 * time.Hour)},
	} {
		if _, err := s.InsertUpdateRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	// The new version starts (and records its version event) before the updater says "updated".
	_ = s.SetUpdateRunState(ctx, "ok", UpdateUpdated, base, base.Add(3*time.Minute), "", "")
	_ = s.SetUpdateRunState(ctx, "bad", UpdateRolledBack, base, base.Add(2*time.Hour+3*time.Minute), "no health", "")
	for _, e := range []HostEvent{
		{Kind: HostEventVersion, At: base.Add(2 * time.Minute), Detail: `{"from":"0.16.0","to":"0.17.0"}`, Key: "version|0.17.0"},
		{Kind: HostEventVersion, At: base.Add(4 * time.Hour), Detail: `{"from":"0.17.0","to":"0.17.3"}`, Key: "version|0.17.3"},
	} {
		if _, err := s.AppendHostEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := s.ListActivity(ctx, 20, time.Time{}, true)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rows {
		switch r.Source {
		case ActivityUpdate:
			got = append(got, fmt.Sprintf("update %s %s %s<-%s %q %q", r.RowID, r.Status, r.Label, r.Program, r.Error, r.Detail))
		case ActivityHost:
			got = append(got, "host "+r.Detail)
		}
	}
	want := []string{
		`host {"from":"0.17.0","to":"0.17.3"}`,
		`update bad rolled_back 0.17.1<-0.17.0 "no health" "ci"`,
		`update ok updated 0.17.0<-0.16.0 "" ""`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("feed =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
