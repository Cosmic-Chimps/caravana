package ui

import (
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 029: a service backed up with the sqlite engine names its file, says
// it keeps running, and its runs carry what the restore confirmation says.
func TestBackupsTabSQLiteRow(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	started := now.Add(-time.Hour)
	done := started.Add(3 * time.Second)
	a := app.App{ID: "notes", Services: []app.Service{{Name: "db", Image: "alpine/sqlite:3",
		Volumes: []app.Volume{{Name: "data", Path: "/data"}},
		Backup:  &app.Backup{Engine: app.BackupSQLite, File: "/data/app.db"}}}}
	sum := service.AppSummary{AppRecord: store.AppRecord{App: a}}
	backups := []store.BackupRecord{{AppID: "notes", ID: "db-1", Service: "db", Engine: app.BackupSQLite,
		Status: store.BackupSucceeded, Bytes: 2048, StartedAt: started, FinishedAt: &done}}
	dest := &store.BackupDestination{Bucket: "backups"}

	tab := backupsTabOf(now, sum, backups, nil, nil, dest, backupsTab{})
	if len(tab.Rows) != 1 || len(tab.Gaps) != 0 {
		t.Fatalf("rows = %+v, gaps = %+v", tab.Rows, tab.Gaps)
	}
	row := tab.Rows[0]
	if !row.SQLite || row.Volume || row.What != "SQLite · /data/app.db" {
		t.Errorf("row = SQLite %v, Volume %v, What %q", row.SQLite, row.Volume, row.What)
	}
	if len(row.Runs) != 1 || !row.Runs[0].SQLite || row.Runs[0].File != "/data/app.db" || !row.Runs[0].CanRestore {
		t.Errorf("run = %+v", row.Runs)
	}
	if got := builtinCommand(app.BackupSQLite, "backups"); got != "sqlite copy → backups" {
		t.Errorf("builtinCommand = %q", got)
	}
}
