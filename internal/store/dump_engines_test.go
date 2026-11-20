package store

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// Migration 0036 lets a backup and a restore be made by the mysql and mongodb
// engines (feature 046), and keeps what was there: every column of an old
// backup and restore, a pin and its unique label, and the indexes.
func TestUpgradeTo0036(t *testing.T) {
	ctx := context.Background()
	db := testDB(t, t.TempDir())
	if _, err := migrate(ctx, db, migrationsUpTo(t, 35)); err != nil {
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
	mustExec(`INSERT INTO backups (app_id, id, service, job_id, status, object, bytes, started_at, finished_at, engine, trigger, pin, pinned_at)
		VALUES ('notes', 'db-1', 'db', 'notes_db_backup/periodic-1', 'succeeded', 'b/notes/db/1.dump', 1499,
		        '2026-11-01T10:02:00.000000000Z', '2026-11-01T10:03:00.000000000Z', 'postgres', 'owner', 'golden', '2026-11-01T11:00:00.000000000Z')`)
	mustExec(`INSERT INTO restores (id, app_id, service, backup_id, status, message, created_at, finished_at, engine, trigger, pin)
		VALUES ('r1', 'notes', 'db', 'db-1', 'succeeded', '', '2026-11-01T10:05:00.000000000Z',
		        '2026-11-01T10:06:00.000000000Z', 'postgres', 'schedule', 'golden')`)

	if _, err := migrate(ctx, db, testMigrations(t)); err != nil {
		t.Fatal(err)
	}
	s := &Store{db: db, nowFn: time.Now}

	b, err := s.GetBackup(ctx, "notes", "db-1")
	if err != nil || b.Engine != "postgres" || b.Object != "b/notes/db/1.dump" || b.Bytes != 1499 || b.Trigger != BackupTriggerOwner ||
		b.Pin != "golden" || b.PinnedAt == nil {
		t.Errorf("backup = %+v, %v; want every old column and its pin kept", b, err)
	}
	restores, err := s.ListRestores(ctx, "notes", 0)
	if err != nil || len(restores) != 1 || restores[0].Engine != "postgres" || restores[0].Trigger != RestoreTriggerSchedule || restores[0].Pin != "golden" {
		t.Errorf("restores = %+v, %v; want the old restore kept", restores, err)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name IN ('backups_by_time', 'backups_pin', 'restores_by_time')`).Scan(&n); err != nil || n != 3 {
		t.Errorf("indexes = %d, %v; want all three back", n, err)
	}
	// The pin label is still unique per service.
	if err := s.SaveBackup(ctx, BackupRecord{AppID: "notes", ID: "db-2", Service: "db", Status: BackupSucceeded, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE backups SET pin = 'golden' WHERE id = 'db-2'`); err == nil {
		t.Error("two backups of one service have the same pin label")
	}
}

// A mysql and a mongodb backup and restore save and read back with their
// engine, as a sqlite one does.
func TestDumpEngineRunRecords(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	saveTestApp(t, s, "notes")
	t0 := time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)
	done := t0.Add(4 * time.Second)

	for _, tc := range []struct{ engine, ext string }{{"mysql", ".sql.gz"}, {"mongodb", ".archive.gz"}} {
		t.Run(tc.engine, func(t *testing.T) {
			b := BackupRecord{AppID: "notes", ID: tc.engine + "-1", Service: tc.engine, JobID: "notes_" + tc.engine + "_backup/periodic-1",
				Engine: tc.engine, Status: BackupSucceeded, Object: "bucket/notes/db/1" + tc.ext, Bytes: 4096,
				StartedAt: t0, FinishedAt: &done, Trigger: BackupTriggerOwner}
			if err := s.SaveBackup(ctx, b); err != nil {
				t.Fatal(err)
			}
			got, err := s.GetBackup(ctx, "notes", b.ID)
			if err != nil || !reflect.DeepEqual(got, b) {
				t.Errorf("backup = %+v, %v; want %+v", got, err, b)
			}

			r := RestoreRecord{ID: "r-" + tc.engine, AppID: "notes", Service: tc.engine, BackupID: b.ID, Engine: tc.engine,
				Status: BackupSucceeded, CreatedAt: t0, FinishedAt: &done, Trigger: RestoreTriggerSchedule, Pin: "golden"}
			if err := s.SaveRestore(ctx, r); err != nil {
				t.Fatal(err)
			}
			restores, err := s.ListRestores(ctx, "notes", 0)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, got := range restores {
				found = found || reflect.DeepEqual(got, r)
			}
			if !found {
				t.Errorf("restores = %+v, want %+v among them", restores, r)
			}
			if _, err := s.PinBackup(ctx, "notes", b.ID, "golden", done); err != nil {
				t.Errorf("pin a %s backup: %v", tc.engine, err)
			}
		})
	}
}
