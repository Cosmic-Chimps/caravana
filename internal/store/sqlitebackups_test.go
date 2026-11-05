package store

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// Migration 0022 lets a backup and a restore be made by the sqlite engine
// (feature 029), and keeps every other column as it was.
func TestSQLiteRunRecords(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	saveTestApp(t, s, "notes")
	t0 := time.Date(2026, 11, 5, 3, 0, 0, 0, time.UTC)
	done := t0.Add(4 * time.Second)

	b := BackupRecord{AppID: "notes", ID: "db-1", Service: "db", JobID: "notes_db_sqlite/1", Engine: "sqlite",
		Status: BackupSucceeded, Object: "bucket/notes/db/1.sqlite.gz", Bytes: 4096, StartedAt: t0, FinishedAt: &done,
		Trigger: BackupTriggerOwner}
	if err := s.SaveBackup(ctx, b); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetBackup(ctx, "notes", "db-1")
	if err != nil || !reflect.DeepEqual(got, b) {
		t.Errorf("backup = %+v, %v; want %+v", got, err, b)
	}

	stopped := 2 * time.Second
	r := RestoreRecord{ID: "notes-db-2", AppID: "notes", Service: "db", BackupID: "db-1", Engine: "sqlite",
		Status: BackupSucceeded, CreatedAt: t0, FinishedAt: &done, Stopped: &stopped}
	if err := s.SaveRestore(ctx, r); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListRestores(ctx, "notes", 0)
	if err != nil || len(list) != 1 || !reflect.DeepEqual(list[0], r) {
		t.Errorf("restores = %+v, %v; want %+v", list, err, r)
	}

	if err := s.SaveBackup(ctx, BackupRecord{AppID: "notes", ID: "db-2", Service: "db", Engine: "mysql",
		Status: BackupRunning, StartedAt: t0}); err == nil {
		t.Error("an engine the table doesn't know was saved")
	}
}
