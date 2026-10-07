package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
)

func TestBackupDestination(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if _, err := s.GetBackupDestination(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get on a new store = %v, want ErrNotFound", err)
	}
	t0 := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	d := BackupDestination{Endpoint: "https://s3.example.com", Region: "eu-west-1", Bucket: "b", Prefix: "p", AccessKeyID: "AK", UpdatedAt: t0}
	if err := s.SetBackupDestination(ctx, d); err != nil {
		t.Fatal(err)
	}
	d.Bucket = "b2"
	if err := s.SetBackupDestination(ctx, d); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetBackupDestination(ctx)
	if err != nil || got != d {
		t.Errorf("Get = %+v, %v; want %+v", got, err, d)
	}
}

func TestBackupRecords(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if _, err := s.SaveApp(ctx, app.App{ID: "blog", Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{{Name: "db", Image: "postgres"}}}); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC)
	b1 := BackupRecord{AppID: "blog", ID: "db-1", Service: "db", JobID: "blog_db_backup/periodic-1", Status: BackupRunning, StartedAt: t0}
	b2 := BackupRecord{AppID: "blog", ID: "db-2", Service: "db", JobID: "blog_db_backup/periodic-2", Status: BackupRunning, StartedAt: t0.Add(time.Hour)}
	for _, b := range []BackupRecord{b1, b2} {
		if err := s.SaveBackup(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	done := t0.Add(time.Minute)
	b1.Status, b1.Object, b1.Bytes, b1.FinishedAt = BackupSucceeded, "b/blog/db/1.dump", 1499, &done
	if err := s.SaveBackup(ctx, b1); err != nil {
		t.Fatal(err)
	}
	// A late "running" write doesn't undo a final status.
	stale := b1
	stale.Status, stale.FinishedAt = BackupRunning, nil
	if err := s.SaveBackup(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if err := s.ExpireBackups(ctx, "blog", []string{"db-1"}); err != nil {
		t.Fatal(err)
	}

	list, err := s.ListBackups(ctx, "blog", 0)
	if err != nil || len(list) != 2 || list[0].ID != "db-2" {
		t.Fatalf("List = %+v, %v; want db-2 first", list, err)
	}
	got := list[1]
	if got.Status != BackupSucceeded || got.Bytes != 1499 || !got.Expired || got.FinishedAt == nil || !got.FinishedAt.Equal(done) {
		t.Errorf("db-1 = %+v", got)
	}
	if _, err := s.GetBackup(ctx, "blog", "db-9"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetBackup(db-9) = %v, want ErrNotFound", err)
	}

	r := RestoreRecord{ID: "r1", AppID: "blog", Service: "db", BackupID: "db-1", Status: BackupRunning, CreatedAt: t0}
	if err := s.SaveRestore(ctx, r); err != nil {
		t.Fatal(err)
	}
	if running, err := s.RunningRestores(ctx); err != nil || len(running) != 1 {
		t.Fatalf("RunningRestores = %+v, %v", running, err)
	}
	r.Status, r.Message, r.FinishedAt = BackupFailed, "pg_restore: error", &done
	if err := s.SaveRestore(ctx, r); err != nil {
		t.Fatal(err)
	}
	if running, _ := s.RunningRestores(ctx); len(running) != 0 {
		t.Errorf("RunningRestores after it failed = %+v", running)
	}

	// Rows go with the app.
	if err := s.DeleteApp(ctx, "blog"); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListBackups(ctx, "blog", 0); len(list) != 0 {
		t.Errorf("backups after the app's delete = %+v", list)
	}
	if list, _ := s.ListRestores(ctx, "blog", 0); len(list) != 0 {
		t.Errorf("restores after the app's delete = %+v", list)
	}
}
