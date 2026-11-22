package store

import (
	"context"
	"testing"
	"time"
)

// Migration 0037 lets a channel be of kind email or telegram and adds its
// settings. Rebuilding the channels table must not cost the deliveries that
// reference it (a plain DROP would cascade), and the cascade must still work
// afterwards.
func TestUpgradeTo0037(t *testing.T) {
	ctx := context.Background()
	db := testDB(t, t.TempDir())
	if _, err := migrate(ctx, db, migrationsUpTo(t, 36)); err != nil {
		t.Fatal(err)
	}
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	const at = "2026-11-01T10:00:00.000000000Z"
	for _, id := range []string{"nch_a", "nch_b"} {
		mustExec(`INSERT INTO notification_channels (id, kind, name, address, events, paused, created_at, updated_at)
			VALUES (?, 'ntfy', ?, 'ntfy.sh/••••', '["problems"]', 1, ?, ?)`, id, "channel "+id, at, at)
		for _, d := range []string{"1", "2"} {
			mustExec(`INSERT INTO notification_deliveries (id, channel_id, event_type, title, payload, status, attempts, next_at, error, created_at, finished_at)
				VALUES (?, ?, 'test', 'Test', '{}', 'failed', 3, ?, 'boom', ?, ?)`, "ndl_"+id+d, id, at, at, at)
		}
	}

	if _, err := migrate(ctx, db, testMigrations(t)); err != nil {
		t.Fatal(err)
	}
	s := &Store{db: db, nowFn: time.Now}

	var n int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM notification_deliveries").Scan(&n); err != nil || n != 4 {
		t.Fatalf("deliveries after the migration = %d, %v; want all 4", n, err)
	}
	c, err := s.GetNotificationChannel(ctx, "nch_a")
	if err != nil || c.Kind != "ntfy" || c.Name != "channel nch_a" || !c.Paused || string(c.Settings) != "{}" || len(c.Events) != 1 {
		t.Errorf("channel = %+v, %v; want every old column kept and empty settings", c, err)
	}
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		t.Error("foreign_key_check finds a broken reference")
	}
	rows.Close()
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name IN ('deliveries_due', 'deliveries_by_channel')`).Scan(&n); err != nil || n != 2 {
		t.Errorf("indexes = %d, %v; want both back", n, err)
	}
	// The new kinds are allowed, with settings that read back.
	now := time.Now().UTC()
	for _, kind := range []string{"email", "telegram"} {
		row := NotificationChannel{ID: "nch_" + kind, Kind: kind, Name: kind, Address: "x", Events: []string{"problems"},
			Settings: []byte(`{"k":"v"}`), CreatedAt: now, UpdatedAt: now}
		if err := s.SaveNotificationChannel(ctx, row); err != nil {
			t.Fatalf("save a %s channel: %v", kind, err)
		}
		if got, err := s.GetNotificationChannel(ctx, row.ID); err != nil || string(got.Settings) != `{"k":"v"}` {
			t.Errorf("%s channel = %+v, %v; want its settings back", kind, got, err)
		}
	}
	if err := s.SaveNotificationChannel(ctx, NotificationChannel{ID: "nch_x", Kind: "carrier-pigeon", Name: "x", Address: "x", Events: []string{"problems"}, CreatedAt: now, UpdatedAt: now}); err == nil {
		t.Error("an unknown kind was saved")
	}
	// Deleting a channel still takes its deliveries, and only its own.
	if err := s.DeleteNotificationChannel(ctx, "nch_a"); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM notification_deliveries").Scan(&n); err != nil || n != 2 {
		t.Errorf("deliveries after deleting a channel = %d, %v; want its 2 gone and the other 2 kept", n, err)
	}
}
