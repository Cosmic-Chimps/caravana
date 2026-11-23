package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func testDestination(name string) BackupDestination {
	return BackupDestination{Name: name, Endpoint: "https://s3.example.com", Region: "eu-west-1", Bucket: name + "-bucket",
		Prefix: "p", AccessKeyID: "AK", SecretPath: "caravana/backup-destinations/" + name,
		UpdatedAt: time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)}
}

func TestBackupDestinations(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if _, err := s.GetBackupDestination(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetBackupDestination on a new store = %v, want ErrNotFound", err)
	}
	if _, err := s.GetBackupDestinationNamed(ctx, "main"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetBackupDestinationNamed on a new store = %v, want ErrNotFound", err)
	}

	// The first destination is the default, the second is not.
	for _, name := range []string{"main", "offsite"} {
		if err := s.SaveBackupDestination(ctx, testDestination(name)); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.ListBackupDestinations(ctx)
	if err != nil || len(list) != 2 || list[0].Name != "main" || list[1].Name != "offsite" {
		t.Fatalf("list = %+v, %v; want main, offsite", list, err)
	}
	if !list[0].IsDefault || list[1].IsDefault {
		t.Errorf("defaults = %v, %v; want main only", list[0].IsDefault, list[1].IsDefault)
	}
	if got, err := s.GetBackupDestination(ctx); err != nil || got.Name != "main" {
		t.Errorf("GetBackupDestination = %+v, %v; want main", got, err)
	}

	// A test is kept, and a save forgets it. An update keeps the default and the secret path.
	tested := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	if err := s.SaveBackupDestinationTest(ctx, "offsite", DestinationTest{OK: false, Message: "could not write", TestedAt: tested}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetBackupDestinationNamed(ctx, "offsite")
	if err != nil || got.Test == nil || got.Test.OK || got.Test.Message != "could not write" || !got.Test.TestedAt.Equal(tested) {
		t.Errorf("offsite = %+v, %v; want the failed test", got, err)
	}
	changed := testDestination("offsite")
	changed.Bucket, changed.SecretPath = "other", "caravana/somewhere-else"
	if err := s.SaveBackupDestination(ctx, changed); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetBackupDestinationNamed(ctx, "offsite"); got.Bucket != "other" || got.Test != nil || got.IsDefault ||
		got.SecretPath != "caravana/backup-destinations/offsite" {
		t.Errorf("offsite after a save = %+v; want the new bucket, no test, not default, the first secret path", got)
	}
	if err := s.SaveBackupDestinationTest(ctx, "ghost", DestinationTest{TestedAt: tested}); !errors.Is(err, ErrNotFound) {
		t.Errorf("test of an unknown destination = %v, want ErrNotFound", err)
	}

	// Moving the default leaves exactly one.
	if err := s.SetDefaultBackupDestination(ctx, "offsite"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetBackupDestination(ctx); got.Name != "offsite" {
		t.Errorf("default = %q, want offsite", got.Name)
	}
	if err := s.SetDefaultBackupDestination(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("default of an unknown destination = %v, want ErrNotFound", err)
	}
	if got, _ := s.GetBackupDestination(ctx); got.Name != "offsite" {
		t.Errorf("a refused change moved the default to %q", got.Name)
	}
	// The unique index holds one default even when written to directly.
	if _, err := s.db.ExecContext(ctx, "UPDATE backup_destinations SET is_default = 1 WHERE name = 'main'"); err == nil {
		t.Error("two destinations were the default")
	}

	// The default can't be deleted; another can.
	if err := s.DeleteBackupDestination(ctx, "offsite"); !errors.Is(err, ErrDefaultDestination) {
		t.Errorf("delete the default = %v, want ErrDefaultDestination", err)
	}
	if err := s.DeleteBackupDestination(ctx, "main"); err != nil {
		t.Errorf("delete main = %v", err)
	}
	if err := s.DeleteBackupDestination(ctx, "main"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete main twice = %v, want ErrNotFound", err)
	}
}

func TestProjectBackupDestination(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if _, err := s.InsertProject(ctx, "acme", "Acme"); err != nil {
		t.Fatal(err)
	}
	if p, err := s.GetProject(ctx, "acme"); err != nil || p.BackupDestination != "" {
		t.Fatalf("new project = %+v, %v; want the installation's default", p, err)
	}
	if err := s.SetProjectBackupDestination(ctx, "acme", "client-a"); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.GetProject(ctx, "acme"); p.BackupDestination != "client-a" {
		t.Errorf("BackupDestination = %q, want client-a", p.BackupDestination)
	}
	if err := s.SetProjectBackupDestination(ctx, "acme", ""); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.GetProject(ctx, "acme"); p.BackupDestination != "" {
		t.Errorf("BackupDestination = %q, want it back to the default", p.BackupDestination)
	}
	if err := s.SetProjectBackupDestination(ctx, "ghost", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown project = %v, want ErrNotFound", err)
	}
}

// Migration 0042 turns the one destination there was into main, the default,
// with its secret key still where it was, and gives every backup and restore
// row that destination. With no destination set nothing is made up.
func TestUpgradeTo0042(t *testing.T) {
	seed := func(t *testing.T, withDestination bool) *Store {
		t.Helper()
		ctx := context.Background()
		db := testDB(t, t.TempDir())
		if _, err := migrate(ctx, db, migrationsUpTo(t, 41)); err != nil {
			t.Fatal(err)
		}
		mustExec := func(query string, args ...any) {
			t.Helper()
			if _, err := db.ExecContext(ctx, query, args...); err != nil {
				t.Fatalf("%s: %v", query, err)
			}
		}
		if withDestination {
			mustExec(`INSERT INTO backup_destination (id, endpoint, region, bucket, prefix, access_key_id, updated_at)
				VALUES (1, 'https://s3.example.com', 'eu-west-1', 'vee', 'caravana', 'AK', '2026-11-01T09:00:00.000000000Z')`)
		}
		mustExec(`INSERT INTO apps (id, schema_version, document, created_at, updated_at)
			VALUES ('notes', 1, '{"id":"notes"}', '2026-11-01T10:00:00.000000000Z', '2026-11-01T10:00:00.000000000Z')`)
		mustExec(`INSERT INTO backups (app_id, id, service, job_id, status, object, bytes, started_at, finished_at, engine, trigger)
			VALUES ('notes', 'db-1', 'db', 'notes_db_backup/periodic-1', 'succeeded', 'vee/caravana/notes/db/1.dump', 1499,
			        '2026-11-01T10:02:00.000000000Z', '2026-11-01T10:03:00.000000000Z', 'postgres', 'owner')`)
		mustExec(`INSERT INTO restores (id, app_id, service, backup_id, status, created_at, engine, trigger)
			VALUES ('r1', 'notes', 'db', 'db-1', 'succeeded', '2026-11-01T10:05:00.000000000Z', 'postgres', 'owner')`)
		if _, err := migrate(ctx, db, testMigrations(t)); err != nil {
			t.Fatal(err)
		}
		return &Store{db: db, nowFn: time.Now}
	}

	t.Run("one destination becomes main", func(t *testing.T) {
		ctx := context.Background()
		s := seed(t, true)
		list, err := s.ListBackupDestinations(ctx)
		if err != nil || len(list) != 1 {
			t.Fatalf("destinations = %+v, %v; want main alone", list, err)
		}
		want := BackupDestination{Name: "main", Endpoint: "https://s3.example.com", Region: "eu-west-1", Bucket: "vee", Prefix: "caravana",
			AccessKeyID: "AK", SecretPath: "caravana/backup-destination", IsDefault: true,
			UpdatedAt: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)}
		if got := list[0]; got.Name != want.Name || got.Endpoint != want.Endpoint || got.Region != want.Region || got.Bucket != want.Bucket ||
			got.Prefix != want.Prefix || got.AccessKeyID != want.AccessKeyID || got.SecretPath != want.SecretPath ||
			!got.IsDefault || !got.UpdatedAt.Equal(want.UpdatedAt) || got.Test != nil {
			t.Errorf("main = %+v, want %+v", got, want)
		}
		b, err := s.GetBackup(ctx, "notes", "db-1")
		if err != nil || b.Destination != "main" || b.Object != "vee/caravana/notes/db/1.dump" || b.Copies != nil {
			t.Errorf("backup = %+v, %v; want destination main and nothing else changed", b, err)
		}
		if rs, err := s.ListRestores(ctx, "notes", 0); err != nil || len(rs) != 1 || rs[0].Destination != "main" {
			t.Errorf("restores = %+v, %v; want destination main", rs, err)
		}
		if _, err := s.db.ExecContext(ctx, "SELECT 1 FROM backup_destination"); err == nil {
			t.Error("the old backup_destination table is still there")
		}
	})

	t.Run("no destination stays none", func(t *testing.T) {
		ctx := context.Background()
		s := seed(t, false)
		if list, err := s.ListBackupDestinations(ctx); err != nil || len(list) != 0 {
			t.Errorf("destinations = %+v, %v; want none", list, err)
		}
		if b, err := s.GetBackup(ctx, "notes", "db-1"); err != nil || b.Destination != "" {
			t.Errorf("backup = %+v, %v; want no destination", b, err)
		}
		if rs, _ := s.ListRestores(ctx, "notes", 0); len(rs) != 1 || rs[0].Destination != "" {
			t.Errorf("restores = %+v; want no destination", rs)
		}
	})
}

func TestBackupCopies(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	saveTestApp(t, s, "notes")
	saveTestApp(t, s, "blog")
	t0 := time.Date(2026, 11, 23, 3, 0, 0, 0, time.UTC)
	done := t0.Add(time.Minute)
	for _, id := range []string{"db-1", "db-2"} {
		rec := BackupRecord{AppID: "notes", ID: id, Service: "db", Status: BackupSucceeded, Object: "main-bucket/notes/db/" + id,
			StartedAt: t0, FinishedAt: &done, Destination: "main"}
		if id == "db-2" {
			rec.StartedAt = t0.Add(time.Hour)
		}
		if err := s.SaveBackup(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.QueueBackupCopies(ctx, "notes", "db-1", []string{"offsite", "cold"}); err != nil {
		t.Fatal(err)
	}
	// Queueing again leaves the rows as they were.
	if err := s.SaveBackupCopy(ctx, BackupCopy{AppID: "notes", BackupID: "db-1", Destination: "cold", Status: CopyRunning, JobID: "notes_db_copy-abc", StartedAt: &t0}); err != nil {
		t.Fatal(err)
	}
	if err := s.QueueBackupCopies(ctx, "notes", "db-1", []string{"offsite", "cold"}); err != nil {
		t.Fatal(err)
	}
	b, err := s.GetBackup(ctx, "notes", "db-1")
	if err != nil || len(b.Copies) != 2 {
		t.Fatalf("copies = %+v, %v; want two", b.Copies, err)
	}
	byDest := map[string]BackupCopy{}
	for _, c := range b.Copies {
		byDest[c.Destination] = c
	}
	if c := byDest["cold"]; c.Status != CopyRunning || c.JobID != "notes_db_copy-abc" || c.Service != "db" || c.StartedAt == nil {
		t.Errorf("cold = %+v, want running with its job", c)
	}
	if c := byDest["offsite"]; c.Status != CopyPending || c.CreatedAt.IsZero() {
		t.Errorf("offsite = %+v, want pending", c)
	}
	if open, err := s.OpenBackupCopies(ctx); err != nil || len(open) != 2 {
		t.Errorf("open = %+v, %v; want both", open, err)
	}

	// A copy finishes, and a late write of running doesn't undo it.
	finished := done.Add(time.Minute)
	if err := s.SaveBackupCopy(ctx, BackupCopy{AppID: "notes", BackupID: "db-1", Destination: "cold", Status: CopySucceeded,
		Object: "cold-bucket/notes/db/db-1", Bytes: 1499, StartedAt: &t0, FinishedAt: &finished}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveBackupCopy(ctx, BackupCopy{AppID: "notes", BackupID: "db-1", Destination: "cold", Status: CopyRunning, StartedAt: &t0}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveBackupCopy(ctx, BackupCopy{AppID: "notes", BackupID: "db-1", Destination: "ghost", Status: CopyFailed}); !errors.Is(err, ErrNotFound) {
		t.Errorf("save of an unqueued copy = %v, want ErrNotFound", err)
	}
	if err := s.ExpireBackupCopies(ctx, "notes", "cold", []string{"db-1"}); err != nil {
		t.Fatal(err)
	}
	copies, err := s.ListBackupCopies(ctx, "notes")
	if err != nil {
		t.Fatal(err)
	}
	var cold BackupCopy
	for _, c := range copies["db-1"] {
		if c.Destination == "cold" {
			cold = c
		}
	}
	if cold.Status != CopySucceeded || cold.Object != "cold-bucket/notes/db/db-1" || cold.Bytes != 1499 || !cold.Expired ||
		cold.FinishedAt == nil || !cold.FinishedAt.Equal(finished) {
		t.Errorf("cold = %+v, want succeeded, expired, with its object", cold)
	}
	if open, _ := s.OpenBackupCopies(ctx); len(open) != 1 || open[0].Destination != "offsite" {
		t.Errorf("open = %+v, want offsite only", open)
	}

	// The newest copy per service and destination: db-2's replaces db-1's.
	if err := s.QueueBackupCopies(ctx, "notes", "db-2", []string{"offsite"}); err != nil {
		t.Fatal(err)
	}
	latest, err := s.LatestBackupCopies(ctx)
	if err != nil || len(latest) != 2 {
		t.Fatalf("latest = %+v, %v; want one per destination", latest, err)
	}
	for _, c := range latest {
		if c.Destination == "offsite" && c.BackupID != "db-2" {
			t.Errorf("latest offsite copy is of %s, want db-2", c.BackupID)
		}
	}

	// Deleting the app deletes its copies and nobody else's.
	saveBackedUp := func(app string) {
		rec := BackupRecord{AppID: app, ID: "web-1", Service: "web", Status: BackupSucceeded, StartedAt: t0, FinishedAt: &done, Destination: "main"}
		if err := s.SaveBackup(ctx, rec); err != nil {
			t.Fatal(err)
		}
		if err := s.QueueBackupCopies(ctx, app, "web-1", []string{"offsite"}); err != nil {
			t.Fatal(err)
		}
	}
	saveBackedUp("blog")
	if err := s.DeleteApp(ctx, "notes"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM backup_copies WHERE app_id = 'notes'").Scan(&n); err != nil || n != 0 {
		t.Errorf("copies of the deleted app = %d, %v; want 0", n, err)
	}
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM backup_copies WHERE app_id = 'blog'").Scan(&n); err != nil || n != 1 {
		t.Errorf("copies of the other app = %d, %v; want 1", n, err)
	}
}

// A backup record keeps the destination it was first recorded with when a later
// write doesn't know one, and takes a new one when it does.
func TestBackupRecordDestination(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	saveTestApp(t, s, "notes")
	t0 := time.Date(2026, 11, 23, 3, 0, 0, 0, time.UTC)
	rec := BackupRecord{AppID: "notes", ID: "db-1", Service: "db", Status: BackupRunning, StartedAt: t0, Destination: "client-a"}
	if err := s.SaveBackup(ctx, rec); err != nil {
		t.Fatal(err)
	}
	done := t0.Add(time.Minute)
	rec.Status, rec.FinishedAt, rec.Destination = BackupSucceeded, &done, ""
	if err := s.SaveBackup(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetBackup(ctx, "notes", "db-1"); got.Destination != "client-a" {
		t.Errorf("destination = %q, want it kept", got.Destination)
	}
	rec.Destination = "offsite"
	if err := s.SaveBackup(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetBackup(ctx, "notes", "db-1"); got.Destination != "offsite" {
		t.Errorf("destination = %q, want the one the watcher read", got.Destination)
	}
	r := RestoreRecord{ID: "r1", AppID: "notes", Service: "db", BackupID: "db-1", Status: BackupRunning, CreatedAt: t0, Destination: "offsite"}
	if err := s.SaveRestore(ctx, r); err != nil {
		t.Fatal(err)
	}
	if rs, _ := s.ListRestores(ctx, "notes", 0); len(rs) != 1 || rs[0].Destination != "offsite" {
		t.Errorf("restores = %+v, want destination offsite", rs)
	}
}
