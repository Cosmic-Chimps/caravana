package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app/apptest"
)

// TestUpgradeTo0033 migrates a store holding actions as feature 042 left it:
// every row survives the new table, the index is still there, and a scale
// action can now be stored.
func TestUpgradeTo0033(t *testing.T) {
	ctx := context.Background()
	db := testDB(t, t.TempDir())
	if _, err := migrate(ctx, db, migrationsUpTo(t, 32)); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO app_actions (id, app_id, action, trigger, status, copy_id, service, program, exit_code, error, started_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	rows := [][]any{
		{"act_a", "whoami", "run", "console", "succeeded", "c1", "web", "ls", 3, "", "2026-10-18T09:00:00.000000000Z", "2026-10-18T09:00:01.000000000Z"},
		{"act_b", "whoami", "stop", "manual", "failed", "", "", "", nil, "boom", "2026-10-18T09:05:00.000000000Z", "2026-10-18T09:05:01.000000000Z"},
	}
	for _, r := range rows {
		if _, err := db.ExecContext(ctx, insert, r...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, insert, "act_x", "whoami", "scale", "autoscale", "succeeded", "", "web", "", nil, "", "2026-10-18T09:07:00.000000000Z", nil); err == nil {
		t.Fatal("0032 accepted a scale action; the test would prove nothing")
	}

	if n, err := migrate(ctx, db, testMigrations(t)); err != nil || n != 1 {
		t.Fatalf("migrate on = %d, %v; want 1", n, err)
	}
	s := &Store{db: db, nowFn: time.Now}

	got, err := s.ListActions(ctx, "whoami", 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("ListActions = %+v, %v; want 2 rows", got, err)
	}
	if got[0].ID != "act_b" || got[0].Error != "boom" || got[0].FromCount != nil || got[0].Reason != "" || got[0].JobVersion != nil {
		t.Errorf("first row = %+v", got[0])
	}
	if got[1].ID != "act_a" || got[1].Program != "ls" || got[1].ExitCode == nil || *got[1].ExitCode != 3 {
		t.Errorf("second row = %+v", got[1])
	}
	t0 := time.Date(2026, 10, 18, 9, 7, 0, 0, time.UTC)
	if err := s.InsertAction(ctx, ActionRecord{ID: "act_s", AppID: "whoami", Action: ActionScale, Trigger: "autoscale",
		Status: ActionSucceeded, Service: "web", StartedAt: t0, FinishedAt: &t0, FromCount: new(1), ToCount: new(2), Reason: "why"}); err != nil {
		t.Fatalf("insert a scale action: %v", err)
	}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'app_actions_by_app'").Scan(&n); err != nil || n != 1 {
		t.Errorf("index app_actions_by_app: %d, %v; want 1", n, err)
	}
}

func scaleAction(id, app, service string, at time.Time, from, to int, job, base *uint64) ActionRecord {
	return ActionRecord{ID: id, AppID: app, Action: ActionScale, Trigger: "autoscale", Status: ActionSucceeded,
		Service: service, StartedAt: at, FinishedAt: &at, FromCount: new(from), ToCount: new(to),
		Reason: "62 requests a second per copy over the last 3 minutes, target 50", JobVersion: job, BaseVersion: base}
}

func TestScaleActionRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	t0 := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	in := scaleAction("act_1", "shop", "web", t0, 1, 2, new(uint64(7)), new(uint64(5)))
	if err := s.InsertAction(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListActions(ctx, "shop", 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("ListActions = %+v, %v", got, err)
	}
	if !reflect.DeepEqual(got[0], in) {
		t.Errorf("round trip\n got: %+v\nwant: %+v", got[0], in)
	}
	// A failed step has no versions.
	failed := scaleAction("act_2", "shop", "web", t0.Add(time.Minute), 2, 3, nil, nil)
	failed.Status, failed.Error = ActionFailed, "Nomad refused"
	if err := s.InsertAction(ctx, failed); err != nil {
		t.Fatal(err)
	}
	last, ok, err := s.LastScale(ctx, "shop", "web")
	if err != nil || !ok || last.ID != "act_2" || last.JobVersion != nil || last.Error != "Nomad refused" {
		t.Errorf("LastScale = %+v, %v, %v; want the failed step", last, ok, err)
	}
	if _, ok, err := s.LastScale(ctx, "shop", "worker"); err != nil || ok {
		t.Errorf("LastScale of a service with none = %v, %v", ok, err)
	}
	if _, ok, err := s.LastScale(ctx, "other", "web"); err != nil || ok {
		t.Errorf("LastScale of another app = %v, %v", ok, err)
	}
}

func TestScaleVersions(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	t0 := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	for _, a := range []ActionRecord{
		scaleAction("act_1", "shop", "web", t0, 1, 2, new(uint64(6)), new(uint64(5))),
		scaleAction("act_2", "shop", "web", t0.Add(time.Minute), 2, 3, new(uint64(7)), new(uint64(5))),
		scaleAction("act_3", "shop", "web", t0.Add(2*time.Minute), 3, 4, nil, nil),
		scaleAction("act_4", "blog", "web", t0, 1, 2, new(uint64(3)), new(uint64(2))),
	} {
		if err := s.InsertAction(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	// A non-scale action never maps a version.
	if err := s.InsertAction(ctx, ActionRecord{ID: "act_5", AppID: "shop", Action: ActionStop, Trigger: "console", Status: ActionSucceeded, StartedAt: t0}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ScaleVersions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]map[uint64]uint64{"shop": {6: 5, 7: 5}, "blog": {3: 2}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ScaleVersions = %v, want %v", got, want)
	}
}

func TestRecentScales(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	t0 := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	for i, app := range []string{"a", "b", "a", "c"} {
		if err := s.InsertAction(ctx, scaleAction("act_"+string(rune('1'+i)), app, "web", t0.Add(time.Duration(i)*time.Minute), 1, 2, nil, nil)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.InsertAction(ctx, ActionRecord{ID: "act_x", AppID: "a", Action: ActionRestart, Trigger: "console", Status: ActionSucceeded, StartedAt: t0.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	got, err := s.RecentScales(ctx, []string{"a", "b"}, 10)
	if err != nil || len(got) != 3 || got[0].ID != "act_3" || got[1].ID != "act_2" || got[2].ID != "act_1" {
		t.Errorf("RecentScales = %+v, %v; want act_3, act_2, act_1", got, err)
	}
	if got, err := s.RecentScales(ctx, []string{"a", "b"}, 1); err != nil || len(got) != 1 {
		t.Errorf("limit 1 = %+v, %v", got, err)
	}
	if got, err := s.RecentScales(ctx, nil, 10); err != nil || got == nil || len(got) != 0 {
		t.Errorf("no apps = %#v, %v; want an empty slice", got, err)
	}
}

func TestServiceCounts(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	a := apptest.Whoami().App
	if _, err := s.SaveApp(ctx, a, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ServiceCounts(ctx); err != nil || len(got) != 0 {
		t.Fatalf("ServiceCounts of none = %v, %v", got, err)
	}
	for _, c := range []struct {
		app, svc string
		n        int
	}{{a.ID, "web", 2}, {a.ID, "api", 1}, {"kept", "web", 4}, {a.ID, "web", 3}} {
		if err := s.SetServiceCount(ctx, c.app, c.svc, c.n); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]map[string]int{a.ID: {"web": 3, "api": 1}, "kept": {"web": 4}}
	if got, err := s.ServiceCounts(ctx); err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("ServiceCounts = %v, %v; want %v", got, err, want)
	}
	if err := s.DeleteApp(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	want = map[string]map[string]int{"kept": {"web": 4}}
	if got, err := s.ServiceCounts(ctx); err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("after delete ServiceCounts = %v, %v; want %v", got, err, want)
	}
}

func TestReplaceServiceCounts(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	for _, c := range []struct {
		app, svc string
		n        int
	}{{"shop", "web", 3}, {"shop", "old", 2}, {"kept", "web", 4}} {
		if err := s.SetServiceCount(ctx, c.app, c.svc, c.n); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ReplaceServiceCounts(ctx, "shop", map[string]int{"web": 1, "api": 2}); err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]int{"shop": {"web": 1, "api": 2}, "kept": {"web": 4}}
	if got, err := s.ServiceCounts(ctx); err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("ServiceCounts = %v, %v; want %v", got, err, want)
	}
	if err := s.ReplaceServiceCounts(ctx, "shop", nil); err != nil {
		t.Fatal(err)
	}
	want = map[string]map[string]int{"kept": {"web": 4}}
	if got, err := s.ServiceCounts(ctx); err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("after replacing with none: %v, %v; want %v", got, err, want)
	}
}
