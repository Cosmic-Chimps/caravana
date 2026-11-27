package store

import (
	"context"
	"testing"
)

// TestUpgradeTo0048 migrates a store that holds backups, a copy of one, a restore, and orphans
// (rows whose parent is gone, kept from before foreign keys were enforced): every row must come
// through, the copy must survive the rebuild of the table it references with ON DELETE CASCADE,
// and the engine opensearch must be accepted afterwards.
func TestUpgradeTo0048(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := testDB(t, dir)
	if _, err := migrate(ctx, db, migrationsUpTo(t, 47)); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO apps (id, schema_version, document, created_at, updated_at, app_name) VALUES ('shop', 1, '{}', '2026-11-01T00:00:00Z', '2026-11-01T00:00:00Z', 'shop')`)
	exec(`INSERT INTO backups (app_id, id, service, job_id, status, started_at, engine, destination, actor, pin, pinned_at)
	      VALUES ('shop', 'db-1', 'db', 'j', 'succeeded', '2026-11-01T00:00:00Z', 'volume', 'main', 'ana@example.com', 'golden', 'x')`)
	exec(`INSERT INTO backup_copies (app_id, backup_id, destination, status, created_at) VALUES ('shop', 'db-1', 'offsite', 'succeeded', 'x')`)
	exec(`INSERT INTO restores (id, app_id, service, backup_id, status, created_at, engine, destination, actor)
	      VALUES ('shop-db-2', 'shop', 'db', 'db-1', 'succeeded', 'x', 'sqlite', 'main', 'ana@example.com')`)
	// What the old binary could leave: rows whose parent isn't there.
	exec("PRAGMA foreign_keys = OFF")
	exec(`INSERT INTO backups (app_id, id, service, job_id, status, started_at) VALUES ('gone', 'old-1', 'db', 'j', 'failed', 'x')`)
	exec(`INSERT INTO backup_copies (app_id, backup_id, destination, status, created_at) VALUES ('gone', 'no-such', 'offsite', 'failed', 'x')`)
	exec(`INSERT INTO restores (id, app_id, service, backup_id, status, created_at) VALUES ('gone-1', 'gone', 'db', 'x', 'failed', 'x')`)
	exec("PRAGMA foreign_keys = ON")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open = %v", err)
	}
	defer st.Close()

	count := func(q string) int {
		t.Helper()
		var n int
		if err := st.db.QueryRowContext(ctx, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(`SELECT count(*) FROM backups`); n != 2 {
		t.Errorf("backups = %d, want 2 (the orphan kept)", n)
	}
	if n := count(`SELECT count(*) FROM backup_copies`); n != 2 {
		t.Errorf("backup_copies = %d, want 2: the rebuild must not cascade-delete them", n)
	}
	if n := count(`SELECT count(*) FROM restores`); n != 2 {
		t.Errorf("restores = %d, want 2", n)
	}
	if n := count(`SELECT count(*) FROM backups WHERE id = 'db-1' AND destination = 'main' AND actor = 'ana@example.com' AND pin = 'golden' AND engine = 'volume'`); n != 1 {
		t.Error("the backup's columns changed")
	}
	if n := count(`SELECT count(*) FROM restores WHERE id = 'shop-db-2' AND destination = 'main' AND actor = 'ana@example.com' AND engine = 'sqlite'`); n != 1 {
		t.Error("the restore's columns changed")
	}
	if n := count(`SELECT count(*) FROM pragma_foreign_key_list('backup_copies') WHERE "table" = 'backups' AND on_delete = 'CASCADE'`); n != 2 { // a composite key: one row per column
		t.Error("backup_copies lost its reference to backups")
	}
	// Enforcement is back on, and the new engine is allowed.
	if n := count(`PRAGMA foreign_keys`); n != 1 {
		t.Errorf("foreign_keys = %d after the migration, want 1", n)
	}
	if _, err := st.db.ExecContext(ctx, `INSERT INTO backups (app_id, id, service, job_id, status, started_at, engine) VALUES ('shop', 'os-1', 'os', 'j', 'running', 'x', 'opensearch')`); err != nil {
		t.Errorf("an opensearch backup: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `INSERT INTO restores (id, app_id, service, backup_id, status, created_at, engine) VALUES ('shop-os-2', 'shop', 'os', 'os-1', 'running', 'x', 'opensearch')`); err != nil {
		t.Errorf("an opensearch restore: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `INSERT INTO backups (app_id, id, service, job_id, status, started_at, engine) VALUES ('shop', 'x-1', 'os', 'j', 'running', 'x', 'redis')`); err == nil {
		t.Error("an unknown engine was accepted")
	}
	if _, err := st.db.ExecContext(ctx, `INSERT INTO backups (app_id, id, service, job_id, status, started_at) VALUES ('nobody', 'x-2', 'os', 'j', 'running', 'x')`); err == nil {
		t.Error("a backup of no app was accepted: foreign keys are off")
	}
	// A deleted backup still takes its copies with it.
	if _, err := st.db.ExecContext(ctx, `DELETE FROM backups WHERE app_id = 'shop' AND id = 'db-1'`); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM backup_copies WHERE app_id = 'shop'`); n != 0 {
		t.Errorf("a deleted backup left %d copies", n)
	}
}
