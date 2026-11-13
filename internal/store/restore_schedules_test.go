package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Migration 0029 rebuilds restores: a restore recorded before it keeps every
// column and reads as the owner's, with no label; a backup keeps its row and
// has no pin.
func TestUpgradeTo0029(t *testing.T) {
	ctx := context.Background()
	db := testDB(t, t.TempDir())
	if _, err := migrate(ctx, db, migrationsUpTo(t, 28)); err != nil {
		t.Fatal(err)
	}
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	mustExec(`INSERT INTO apps (id, schema_version, document, created_at, updated_at)
		VALUES ('notes', 1, '{"id":"notes"}', '2026-11-01T10:00:00.000000000Z', '2026-11-01T10:00:00.000000000Z')`)
	mustExec(`INSERT INTO backups (app_id, id, service, job_id, status, started_at)
		VALUES ('notes', 'db-1', 'db', 'notes_db_backup/1', 'succeeded', '2026-11-01T10:02:00.000000000Z')`)
	mustExec(`INSERT INTO restores (id, app_id, service, backup_id, status, message, created_at, finished_at, engine, stopped_ms)
		VALUES ('r1', 'notes', 'db', 'db-1', 'failed', 'pg_restore: error', '2026-11-01T10:05:00.000000000Z',
		        '2026-11-01T10:06:00.000000000Z', 'volume', 2500)`)

	if _, err := migrate(ctx, db, testMigrations(t)); err != nil {
		t.Fatal(err)
	}
	s := &Store{db: db, nowFn: time.Now}

	restores, err := s.ListRestores(ctx, "notes", 0)
	if err != nil || len(restores) != 1 {
		t.Fatalf("ListRestores = %+v, %v; want the old restore", restores, err)
	}
	r := restores[0]
	if r.ID != "r1" || r.BackupID != "db-1" || r.Status != BackupFailed || r.Message != "pg_restore: error" ||
		r.Engine != "volume" || r.Stopped == nil || *r.Stopped != 2500*time.Millisecond ||
		r.Trigger != RestoreTriggerOwner || r.Pin != "" || r.FinishedAt == nil {
		t.Errorf("restore = %+v, want every old column kept, trigger owner and no pin", r)
	}
	b, err := s.GetBackup(ctx, "notes", "db-1")
	if err != nil || b.Pin != "" || b.PinnedAt != nil {
		t.Errorf("backup = %+v, %v; want it unpinned", b, err)
	}
	// The rebuilt table takes the new status, and the index is back.
	if err := s.SaveRestore(ctx, RestoreRecord{ID: "r2", AppID: "notes", Service: "db", Status: RestoreSkipped,
		CreatedAt: time.Now(), Trigger: RestoreTriggerSchedule, Pin: "golden"}); err != nil {
		t.Errorf("save a skipped restore: %v", err)
	}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name IN ('restores_by_time', 'backups_pin')").Scan(&n); err != nil || n != 2 {
		t.Errorf("indexes = %d, %v; want restores_by_time and backups_pin", n, err)
	}
}

// savePinTestBackups stores finished backups of one service, one per minute.
func savePinTestBackups(t *testing.T, s *Store, appID, service string, ids ...string) {
	t.Helper()
	t0 := time.Date(2026, 11, 13, 3, 0, 0, 0, time.UTC)
	for i, id := range ids {
		done := t0.Add(time.Duration(i)*time.Minute + time.Second)
		err := s.SaveBackup(context.Background(), BackupRecord{AppID: appID, ID: id, Service: service, Status: BackupSucceeded,
			Object: "b/" + id, StartedAt: t0.Add(time.Duration(i) * time.Minute), FinishedAt: &done})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestPinBackup(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	saveTestApp(t, s, "notes")
	savePinTestBackups(t, s, "notes", "web", "web-1", "web-2", "web-3")
	savePinTestBackups(t, s, "notes", "other", "other-1")
	at := time.Date(2026, 11, 13, 4, 0, 0, 0, time.UTC)

	prev, err := s.PinBackup(ctx, "notes", "web-1", "golden", at)
	if err != nil || prev != "" {
		t.Fatalf("PinBackup = %q, %v; want no previous holder", prev, err)
	}
	got, err := s.PinnedBackup(ctx, "notes", "web", "golden")
	if err != nil || got.ID != "web-1" || got.Pin != "golden" || got.PinnedAt == nil || !got.PinnedAt.Equal(at) {
		t.Errorf("PinnedBackup = %+v, %v; want web-1 pinned as golden at %v", got, err, at)
	}

	// The label moves: one holder per service.
	prev, err = s.PinBackup(ctx, "notes", "web-2", "golden", at.Add(time.Hour))
	if err != nil || prev != "web-1" {
		t.Fatalf("second PinBackup = %q, %v; want web-1 as the previous holder", prev, err)
	}
	if b, _ := s.GetBackup(ctx, "notes", "web-1"); b.Pin != "" || b.PinnedAt != nil {
		t.Errorf("web-1 = %+v, want the label gone", b)
	}
	if got, err := s.PinnedBackup(ctx, "notes", "web", "golden"); err != nil || got.ID != "web-2" {
		t.Errorf("PinnedBackup = %+v, %v; want web-2", got, err)
	}

	// Pinning the holder again is not a move.
	if prev, err := s.PinBackup(ctx, "notes", "web-2", "golden", at); err != nil || prev != "" {
		t.Errorf("pinning the holder again = %q, %v; want no previous holder", prev, err)
	}

	// A backup has one label: another label replaces the first.
	if _, err := s.PinBackup(ctx, "notes", "web-2", "demo", at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PinnedBackup(ctx, "notes", "web", "golden"); !errors.Is(err, ErrNotFound) {
		t.Errorf("golden after relabelling = %v, want ErrNotFound", err)
	}

	// The same label on another service is another pin.
	if _, err := s.PinBackup(ctx, "notes", "other-1", "demo", at); err != nil {
		t.Fatal(err)
	}
	if got, err := s.PinnedBackup(ctx, "notes", "web", "demo"); err != nil || got.ID != "web-2" {
		t.Errorf("web's demo = %+v, %v; want web-2 still", got, err)
	}

	// SaveBackup (the watcher's record, retention's expiry) leaves the pin alone.
	done := at.Add(time.Minute)
	if err := s.SaveBackup(ctx, BackupRecord{AppID: "notes", ID: "web-2", Service: "web", Status: BackupSucceeded,
		StartedAt: at, FinishedAt: &done, Expired: true}); err != nil {
		t.Fatal(err)
	}
	if b, _ := s.GetBackup(ctx, "notes", "web-2"); b.Pin != "demo" {
		t.Errorf("web-2 pin after SaveBackup = %q, want demo", b.Pin)
	}

	// PinnedBackups lists the pins retention must honour: not the expired one.
	list, err := s.PinnedBackups(ctx, "notes")
	if err != nil || len(list) != 1 || list[0].ID != "other-1" {
		t.Errorf("PinnedBackups = %+v, %v; want only other-1 (web-2 is expired)", list, err)
	}

	if _, err := s.PinBackup(ctx, "notes", "nope", "golden", at); !errors.Is(err, ErrNotFound) {
		t.Errorf("PinBackup of a missing backup = %v, want ErrNotFound", err)
	}

	// Unpin, twice and for a backup that isn't there: no error.
	for _, id := range []string{"other-1", "other-1", "nope"} {
		if err := s.UnpinBackup(ctx, "notes", id); err != nil {
			t.Errorf("UnpinBackup %s: %v", id, err)
		}
	}
	if _, err := s.PinnedBackup(ctx, "notes", "other", "demo"); !errors.Is(err, ErrNotFound) {
		t.Errorf("PinnedBackup after unpin = %v, want ErrNotFound", err)
	}
}

func TestRestoreTriggerAndLatest(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	saveTestApp(t, s, "notes")
	t0 := time.Date(2026, 11, 13, 3, 0, 0, 0, time.UTC)
	save := func(id, status, trigger string, at time.Time) {
		t.Helper()
		done := at.Add(time.Second)
		if err := s.SaveRestore(ctx, RestoreRecord{ID: id, AppID: "notes", Service: "web", BackupID: "web-1", Status: status,
			CreatedAt: at, FinishedAt: &done, Trigger: trigger, Pin: map[string]string{RestoreTriggerSchedule: "golden"}[trigger]}); err != nil {
			t.Fatal(err)
		}
	}

	if _, ok, err := s.LastScheduledRestore(ctx, "notes", "web"); err != nil || ok {
		t.Errorf("LastScheduledRestore with none = %v, %v; want not ok", ok, err)
	}
	save("r-owner", BackupSucceeded, "", t0)
	if _, ok, _ := s.LastScheduledRestore(ctx, "notes", "web"); ok {
		t.Error("an owner's restore counted as scheduled")
	}
	save("r-fail", BackupFailed, RestoreTriggerSchedule, t0.Add(time.Hour))
	save("r-skip", RestoreSkipped, RestoreTriggerSchedule, t0.Add(2*time.Hour))

	at, ok, err := s.LastScheduledRestore(ctx, "notes", "web")
	if err != nil || !ok || !at.Equal(t0.Add(2*time.Hour)) {
		t.Errorf("LastScheduledRestore = %v, %v, %v; want the skipped slot (any outcome) at %v", at, ok, err, t0.Add(2*time.Hour))
	}
	if _, ok, _ := s.LastScheduledRestore(ctx, "notes", "other"); ok {
		t.Error("another service's scheduled restore counted")
	}

	list, err := s.ListRestores(ctx, "notes", 0)
	if err != nil || len(list) != 3 {
		t.Fatalf("ListRestores = %+v, %v", list, err)
	}
	if list[0].ID != "r-skip" || list[0].Trigger != RestoreTriggerSchedule || list[0].Pin != "golden" {
		t.Errorf("newest = %+v, want the skipped scheduled run with its label", list[0])
	}
	if list[2].ID != "r-owner" || list[2].Trigger != RestoreTriggerOwner || list[2].Pin != "" {
		t.Errorf("oldest = %+v, want the owner's restore without a label", list[2])
	}

	// A skipped restore never hides the failure before it.
	_, restores, err := s.LatestBackupRuns(ctx)
	if err != nil || len(restores) != 1 || restores[0].ID != "r-fail" {
		t.Errorf("LatestBackupRuns restores = %+v, %v; want r-fail, not the skipped r-skip", restores, err)
	}
}
