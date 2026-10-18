package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app/apptest"
)

func TestAppStopped(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	a := apptest.Whoami().App
	before, err := s.SaveApp(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	if before.StoppedAt != nil {
		t.Fatalf("a new app is stopped at %v", before.StoppedAt)
	}

	at := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	if err := s.SetAppStopped(ctx, a.ID, &at); err != nil {
		t.Fatal(err)
	}
	stopped, err := s.GetApp(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.StoppedAt == nil || !stopped.StoppedAt.Equal(at) {
		t.Fatalf("StoppedAt = %v, want %v", stopped.StoppedAt, at)
	}
	// Stopping changes neither the revision nor UpdatedAt: it is not an edit.
	if stopped.Revision != before.Revision || !stopped.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("stop moved revision %s -> %s, updated %v -> %v",
			before.Revision, stopped.Revision, before.UpdatedAt, stopped.UpdatedAt)
	}

	// Saving the app again (a redeploy of the same definition) keeps it stopped:
	// the service refuses that, but the store must not clear it by accident.
	again, err := s.SaveApp(ctx, a, nil)
	if err != nil || again.StoppedAt == nil {
		t.Errorf("SaveApp cleared the stopped mark: %v, %v", again.StoppedAt, err)
	}

	list, err := s.ListApps(ctx)
	if err != nil || len(list) != 1 || list[0].StoppedAt == nil {
		t.Errorf("ListApps = %+v, %v; want the app stopped", list, err)
	}

	if err := s.SetAppStopped(ctx, a.ID, nil); err != nil {
		t.Fatal(err)
	}
	started, err := s.GetApp(ctx, a.ID)
	if err != nil || started.StoppedAt != nil {
		t.Errorf("after start StoppedAt = %v, %v; want nil", started.StoppedAt, err)
	}

	if err := s.SetAppStopped(ctx, "ghost", &at); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetAppStopped of an unknown app = %v, want ErrNotFound", err)
	}
}

func TestActions(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	a := apptest.Whoami().App
	if _, err := s.SaveApp(ctx, a, nil); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	stop := ActionRecord{ID: "act_1", AppID: a.ID, Action: ActionStop, Trigger: "console", Status: ActionRunning, StartedAt: t0}
	run := ActionRecord{ID: "act_2", AppID: a.ID, Action: ActionRun, Trigger: "manual", Status: ActionRunning,
		CopyID: "alloc-1", Service: "web", Program: "echo", StartedAt: t0.Add(time.Minute)}
	for _, r := range []ActionRecord{stop, run} {
		if err := s.InsertAction(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.InsertAction(ctx, run); err == nil {
		t.Error("inserting the same action twice succeeded")
	}
	// A restart of another app doesn't show in this one's list.
	other := ActionRecord{ID: "act_3", AppID: "other", Action: ActionRestart, Trigger: "console", Status: ActionRunning, StartedAt: t0}
	if err := s.InsertAction(ctx, other); err != nil {
		t.Fatal(err)
	}

	done := t0.Add(time.Minute + time.Second)
	zero := 0
	if err := s.FinishAction(ctx, "act_2", ActionSucceeded, &zero, "", done); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishAction(ctx, "act_1", ActionFailed, nil, "nomad unreachable", done); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishAction(ctx, "act_nope", ActionFailed, nil, "", done); !errors.Is(err, ErrNotFound) {
		t.Errorf("FinishAction of an unknown action = %v, want ErrNotFound", err)
	}

	got, err := s.ListActions(ctx, a.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "act_2" || got[1].ID != "act_1" {
		t.Fatalf("ListActions = %+v, want act_2 then act_1", got)
	}
	r := got[0]
	if r.Status != ActionSucceeded || r.ExitCode == nil || *r.ExitCode != 0 || r.Program != "echo" ||
		r.CopyID != "alloc-1" || r.Service != "web" || r.Trigger != "manual" || r.FinishedAt == nil || !r.FinishedAt.Equal(done) {
		t.Errorf("run record = %+v", r)
	}
	if f := got[1]; f.Status != ActionFailed || f.ExitCode != nil || f.Error != "nomad unreachable" {
		t.Errorf("stop record = %+v", f)
	}
	if limited, err := s.ListActions(ctx, a.ID, 1); err != nil || len(limited) != 1 || limited[0].ID != "act_2" {
		t.Errorf("ListActions limit 1 = %+v, %v", limited, err)
	}
	if none, err := s.ListActions(ctx, "ghost", 10); err != nil || none == nil || len(none) != 0 {
		t.Errorf("ListActions of an app with none = %#v, %v; want an empty slice", none, err)
	}
}

func TestFailRunningActions(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	t0 := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	running := ActionRecord{ID: "act_1", AppID: "blog", Action: ActionRestart, Trigger: "console", Status: ActionRunning, StartedAt: t0}
	fin := t0.Add(time.Second)
	finished := ActionRecord{ID: "act_2", AppID: "blog", Action: ActionStart, Trigger: "console", Status: ActionSucceeded,
		StartedAt: t0, FinishedAt: &fin}
	for _, r := range []ActionRecord{running, finished} {
		if err := s.InsertAction(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	at := t0.Add(time.Hour)
	n, err := s.FailRunningActions(ctx, "Caravana restarted during it", at)
	if err != nil || n != 1 {
		t.Fatalf("FailRunningActions = %d, %v; want 1", n, err)
	}
	got, err := s.ListActions(ctx, "blog", 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("ListActions = %+v, %v", got, err)
	}
	for _, r := range got {
		switch r.ID {
		case "act_1":
			if r.Status != ActionFailed || r.Error != "Caravana restarted during it" || r.FinishedAt == nil || !r.FinishedAt.Equal(at) {
				t.Errorf("interrupted action = %+v", r)
			}
		case "act_2":
			if r.Status != ActionSucceeded || r.Error != "" {
				t.Errorf("finished action changed: %+v", r)
			}
		}
	}
	if n, err := s.FailRunningActions(ctx, "again", at); err != nil || n != 0 {
		t.Errorf("second FailRunningActions = %d, %v; want 0", n, err)
	}
}

func TestActionsDeletedWithApp(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	a := apptest.Whoami().App
	if _, err := s.SaveApp(ctx, a, nil); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	for _, id := range []string{"x", "y"} {
		appID := a.ID
		if id == "y" {
			appID = "kept"
		}
		if err := s.InsertAction(ctx, ActionRecord{ID: "act_" + id, AppID: appID, Action: ActionStop, Trigger: "console",
			Status: ActionSucceeded, StartedAt: t0}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteApp(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ListActions(ctx, a.ID, 10); err != nil || len(got) != 0 {
		t.Errorf("actions after delete = %+v, %v; want none", got, err)
	}
	if got, err := s.ListActions(ctx, "kept", 10); err != nil || len(got) != 1 {
		t.Errorf("another app's actions = %+v, %v; want 1", got, err)
	}
}
