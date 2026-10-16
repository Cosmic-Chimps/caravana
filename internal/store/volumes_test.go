package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// saveTestApp stores a one-service app, which backups and sizes point at.
func saveTestApp(t *testing.T, s *Store, id string) {
	t.Helper()
	a := app.App{ID: id, Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{{Name: "web", Image: "kuma"}}}
	if _, err := s.SaveApp(context.Background(), a, nil); err != nil {
		t.Fatal(err)
	}
}

func TestVolumeRunRecords(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	saveTestApp(t, s, "kuma")
	t0 := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
	done := t0.Add(40 * time.Second)

	t.Run("a volume backup keeps its engine, stop time and archives", func(t *testing.T) {
		stopped := 12500 * time.Millisecond
		want := BackupRecord{
			AppID: "kuma", ID: "web-1", Service: "web", JobID: "kuma_web_volumes/1", Engine: "volume",
			Status: BackupSucceeded, Object: "kuma/web/1/", Bytes: 3000, StartedAt: t0, FinishedAt: &done,
			Stopped: &stopped,
			Volumes: []VolumeArchive{{Name: "data", Bytes: 2000, SizeKB: 5000}, {Name: "logs", Bytes: 1000, SizeKB: 1200}},
		}
		running := want
		running.Status, running.Object, running.Bytes, running.FinishedAt, running.Stopped, running.Volumes = BackupRunning, "", 0, nil, nil, nil
		if err := s.SaveBackup(ctx, running); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveBackup(ctx, want); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetBackup(ctx, "kuma", "web-1")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("backup = %+v, want %+v", got, want)
		}
	})

	t.Run("a volume restore keeps its engine and stop time", func(t *testing.T) {
		stopped := 3 * time.Second
		r := RestoreRecord{ID: "r1", AppID: "kuma", Service: "web", BackupID: "web-1", Engine: "volume",
			Status: BackupRunning, CreatedAt: t0}
		if err := s.SaveRestore(ctx, r); err != nil {
			t.Fatal(err)
		}
		r.Status, r.FinishedAt, r.Stopped = BackupSucceeded, &done, &stopped
		if err := s.SaveRestore(ctx, r); err != nil {
			t.Fatal(err)
		}
		list, err := s.ListRestores(ctx, "kuma", 0)
		if err != nil || len(list) != 1 || !reflect.DeepEqual(list[0], r) {
			t.Errorf("restores = %+v, %v; want %+v", list, err, r)
		}
	})

	t.Run("records without an engine are postgres runs without a stop time", func(t *testing.T) {
		if err := s.SaveBackup(ctx, BackupRecord{AppID: "kuma", ID: "db-1", Service: "db", Status: BackupSucceeded, StartedAt: t0.Add(-time.Hour)}); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveRestore(ctx, RestoreRecord{ID: "r0", AppID: "kuma", Service: "db", BackupID: "db-1", Status: BackupSucceeded, CreatedAt: t0.Add(-time.Hour)}); err != nil {
			t.Fatal(err)
		}
		b, err := s.GetBackup(ctx, "kuma", "db-1")
		if err != nil || b.Engine != "postgres" || b.Stopped != nil || b.Volumes != nil {
			t.Errorf("backup = %+v, %v; want engine postgres, no stop time, no volumes", b, err)
		}
		restores, err := s.ListRestores(ctx, "kuma", 0)
		if err != nil || len(restores) != 2 || restores[1].Engine != "postgres" || restores[1].Stopped != nil {
			t.Errorf("restores = %+v, %v; want the older one engine postgres, no stop time", restores, err)
		}
	})

	t.Run("rows written before the migration read back as postgres", func(t *testing.T) {
		// The way feature 010 inserted a row: none of the new columns.
		_, err := s.db.ExecContext(ctx, `
			INSERT INTO backups (app_id, id, service, job_id, status, started_at)
			VALUES ('kuma', 'old-1', 'db', 'kuma_db_backup/periodic-1', 'succeeded', ?)`, formatTime(t0.Add(-48*time.Hour)))
		if err != nil {
			t.Fatal(err)
		}
		b, err := s.GetBackup(ctx, "kuma", "old-1")
		if err != nil || b.Engine != "postgres" || b.Stopped != nil || b.Volumes != nil {
			t.Errorf("old backup = %+v, %v", b, err)
		}
	})

	t.Run("the latest runs carry the new fields", func(t *testing.T) {
		backups, restores, err := s.LatestBackupRuns(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range backups {
			if b.Service == "web" && (b.Engine != "volume" || b.Stopped == nil || len(b.Volumes) != 2) {
				t.Errorf("latest web backup = %+v, want the volume fields", b)
			}
		}
		for _, r := range restores {
			if r.Service == "web" && (r.Engine != "volume" || r.Stopped == nil) {
				t.Errorf("latest web restore = %+v, want the volume fields", r)
			}
		}
	})
}

func TestVolumeSizes(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	saveTestApp(t, s, "kuma")
	saveTestApp(t, s, "blog")
	t0 := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)

	if got, err := s.VolumeSizes(ctx, "kuma"); err != nil || len(got) != 0 {
		t.Fatalf("sizes of a new app = %v, %v; want none", got, err)
	}

	save := func(appID, volume string, kb int64, at time.Time) {
		t.Helper()
		if err := s.SaveVolumeSize(ctx, appID, volume, kb, at); err != nil {
			t.Fatal(err)
		}
	}
	save("kuma", "data", 100, t0)
	save("kuma", "logs", 7, t0)
	save("blog", "data", 55, t0)
	save("kuma", "data", 250, t0.Add(time.Hour)) // a newer measurement replaces
	save("kuma", "logs", 1, t0.Add(-time.Hour))  // an older one is ignored

	want := map[string]VolumeSize{
		"data": {KB: 250, MeasuredAt: t0.Add(time.Hour)},
		"logs": {KB: 7, MeasuredAt: t0},
	}
	got, err := s.VolumeSizes(ctx, "kuma")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("sizes of kuma = %v, %v; want %v", got, err, want)
	}

	// The sizes go with the app, and only with that app.
	if err := s.DeleteApp(ctx, "kuma"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.VolumeSizes(ctx, "kuma"); err != nil || len(got) != 0 {
		t.Errorf("sizes after the app's delete = %v, %v; want none", got, err)
	}
	if got, err := s.VolumeSizes(ctx, "blog"); err != nil || len(got) != 1 {
		t.Errorf("sizes of blog = %v, %v; want its 1", got, err)
	}
}
