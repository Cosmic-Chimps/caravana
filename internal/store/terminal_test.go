package store

import (
	"context"
	"testing"
	"time"
)

// TestUpgradeTo0014 migrates a store holding actions as feature 020.5 left it:
// every row survives the new table, the index is still there, and a terminal
// action can now be stored.
func TestUpgradeTo0014(t *testing.T) {
	ctx := context.Background()
	db := testDB(t, t.TempDir())
	if n, err := migrate(ctx, db, migrationsUpTo(t, 13)); err != nil || n != 13 {
		t.Fatalf("migrate to 0013 = %d, %v; want 13", n, err)
	}
	insert := `INSERT INTO app_actions (id, app_id, action, trigger, status, copy_id, service, program, exit_code, error, started_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	rows := [][]any{
		{"act_a", "whoami", "run", "console", "succeeded", "c1", "web", "ls", 3, "", "2026-10-18T09:00:00.000000000Z", "2026-10-18T09:00:01.000000000Z"},
		{"act_b", "whoami", "stop", "manual", "failed", "", "", "", nil, "boom", "2026-10-18T09:05:00.000000000Z", "2026-10-18T09:05:01.000000000Z"},
		{"act_c", "blog", "restart", "console", "running", "", "", "", nil, "", "2026-10-18T09:06:00.000000000Z", nil},
	}
	for _, r := range rows {
		if _, err := db.ExecContext(ctx, insert, r...); err != nil {
			t.Fatal(err)
		}
	}
	// Before the migration a terminal action is refused by the CHECK.
	if _, err := db.ExecContext(ctx, insert, "act_x", "whoami", "terminal", "console", "running", "c1", "web", "auto", nil, "", "2026-10-18T09:07:00.000000000Z", nil); err == nil {
		t.Fatal("0013 accepted a terminal action; the test would prove nothing")
	}

	if n, err := migrate(ctx, db, testMigrations(t)); err != nil || n != len(embeddedVersions(t))-13 {
		t.Fatalf("migrate on = %d, %v", n, err)
	}
	s := &Store{db: db, now: time.Now}

	got, err := s.ListActions(ctx, "whoami", 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("ListActions(whoami) = %+v, %v; want 2 rows", got, err)
	}
	// Newest first: the stop, then the run with its exit code.
	if got[0].ID != "act_b" || got[0].Error != "boom" || got[0].ExitCode != nil || got[0].Status != ActionFailed {
		t.Errorf("first row = %+v", got[0])
	}
	if got[1].ID != "act_a" || got[1].Program != "ls" || got[1].CopyID != "c1" || got[1].Service != "web" ||
		got[1].ExitCode == nil || *got[1].ExitCode != 3 || got[1].FinishedAt == nil {
		t.Errorf("second row = %+v", got[1])
	}
	if blog, err := s.ListActions(ctx, "blog", 10); err != nil || len(blog) != 1 || blog[0].Status != ActionRunning || blog[0].FinishedAt != nil {
		t.Errorf("ListActions(blog) = %+v, %v; want the running restart", blog, err)
	}

	t0 := time.Date(2026, 10, 18, 9, 7, 0, 0, time.UTC)
	if err := s.InsertAction(ctx, ActionRecord{ID: "act_t", AppID: "whoami", Action: ActionTerminal, Trigger: "console",
		Status: ActionRunning, CopyID: "c1", Service: "web", Program: "auto", StartedAt: t0}); err != nil {
		t.Fatalf("insert a terminal action: %v", err)
	}
	if err := s.FinishAction(ctx, "act_t", ActionSucceeded, new(0), "", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'app_actions_by_app'").Scan(&n); err != nil || n != 1 {
		t.Errorf("index app_actions_by_app: %d, %v; want 1", n, err)
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name = 'app_actions_new'").Scan(&n); err != nil || n != 0 {
		t.Errorf("leftover app_actions_new: %d, %v; want 0", n, err)
	}
}
