package service

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// withData is whoami, one copy, with a volume.
func withData() app.App {
	a := whoami()
	a.Services[0].Replicas = nil
	a.Services[0].Volumes = []app.Volume{{Name: "data", Path: "/data"}}
	return a
}

// US4, US5: the first deploy creates the volume, a redeploy keeps it, and
// deleting the app deletes it.
func TestVolumesLiveWithTheApp(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	deployHealthy(t, e, withData())
	if got := e.nomad.HostVolumes(); !slices.Equal(got, []string{"whoami_data"}) {
		t.Fatalf("volumes = %v, want whoami_data", got)
	}
	rec, err := e.svc.Redeploy(ctx, "whoami", app.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if final := waitFinal(t, e.store, rec.Deployment.ID); final.Status != store.StatusHealthy {
		t.Fatalf("redeploy = %s (%s)", final.Status, final.Error)
	}
	if got := e.nomad.HostVolumes(); !slices.Equal(got, []string{"whoami_data"}) {
		t.Errorf("volumes after the redeploy = %v", got)
	}

	// A deploy that stops mounting it warns, and keeps it.
	rec, err = e.svc.Deploy(ctx, func() app.App { a := withData(); a.Services[0].Volumes = nil; return a }(), app.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Warnings) == 0 || rec.Warnings[0].Field != "" || len(e.nomad.HostVolumes()) != 1 {
		t.Errorf("warnings = %+v, volumes = %v; want a warning and the volume kept", rec.Warnings, e.nomad.HostVolumes())
	}
	waitFinal(t, e.store, rec.Deployment.ID)

	if err := e.svc.DeleteApp(ctx, "whoami"); err != nil {
		t.Fatalf("DeleteApp: %v", err)
	}
	if got := e.nomad.HostVolumes(); len(got) != 0 {
		t.Errorf("volumes after delete = %v", got)
	}
}

func TestDeleteAppKeepsTheAppWhenVolumesCantGo(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	deployHealthy(t, e, withData())
	e.nomad.FailVolumes(500)
	if err := e.svc.DeleteApp(ctx, "whoami"); !errors.Is(err, deploy.ErrNomadRejected) {
		t.Fatalf("DeleteApp = %v, want ErrNomadRejected", err)
	}
	if _, err := e.svc.GetApp(ctx, "whoami"); err != nil {
		t.Fatalf("the app is gone although its volume wasn't deleted: %v", err)
	}
	e.nomad.FailVolumes(0)
	if err := e.svc.DeleteApp(ctx, "whoami"); err != nil {
		t.Fatalf("second DeleteApp: %v", err)
	}
}

// US3: the panel is put together from the definition, Nomad's volumes, the
// stored sizes and the backup records. volumeViews is pure, so a table covers
// the cases.
func TestVolumeViews(t *testing.T) {
	t0 := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
	svc := func(name string, backup *app.Backup, vols ...app.Volume) app.Service {
		return app.Service{Name: name, Volumes: vols, Backup: backup}
	}
	data := app.Volume{Name: "data", Path: "/app/data"}
	hostVols := map[string]deploy.HostVolumeInfo{"data": {HostPath: "/h/data"}, "old": {HostPath: "/h/old"}, "pg": {HostPath: "/h/pg"}}
	sizes := map[string]store.VolumeSize{"data": {KB: 3, MeasuredAt: t0}, "old": {KB: 5, MeasuredAt: t0.Add(-time.Hour)}}
	backups := []store.BackupRecord{ // newest first
		{ID: "web-3", Service: "web", Engine: app.BackupVolume, Status: store.BackupFailed, StartedAt: t0.Add(-time.Hour)},
		{ID: "web-2", Service: "web", Engine: app.BackupVolume, Status: store.BackupSucceeded, StartedAt: t0.Add(-25 * time.Hour)},
		{ID: "db-1", Service: "db", Engine: app.BackupPostgres, Status: store.BackupSucceeded, StartedAt: t0.Add(-2 * time.Hour)},
	}
	volBackup := &app.Backup{Engine: app.BackupVolume, Keep: 4}

	tests := []struct {
		name  string
		app   app.App
		host  map[string]deploy.HostVolumeInfo
		sizes map[string]store.VolumeSize
		want  []VolumeView
	}{
		{
			name: "no volumes, none in Nomad",
			app:  app.App{Services: []app.Service{svc("web", nil)}},
			want: nil,
		},
		{
			name:  "a backed-up volume: size, path and its newest backup, failed ones too",
			app:   app.App{Services: []app.Service{svc("web", volBackup, data)}},
			host:  hostVols,
			sizes: sizes,
			want: []VolumeView{
				{Name: "data", Mounted: true, Mounts: []VolumeMount{{Service: "web", Path: "/app/data"}}, HostPath: "/h/data",
					Size: &VolumeSize{Bytes: 3 * 1024, MeasuredAt: t0}, BackedUp: true, Schedule: "0 3 * * *", Keep: 4,
					LastBackup: &VolumeBackup{ID: "web-3", Status: store.BackupFailed, StartedAt: t0.Add(-time.Hour)}},
				{Name: "old", HostPath: "/h/old", Size: &VolumeSize{Bytes: 5 * 1024, MeasuredAt: t0.Add(-time.Hour)}},
				{Name: "pg", HostPath: "/h/pg"},
			},
		},
		{
			name:  "a volume never measured has no size, and one never backed up has no last backup",
			app:   app.App{Services: []app.Service{svc("api", &app.Backup{Engine: app.BackupVolume, Schedule: "15 * * * *"}, data)}},
			host:  map[string]deploy.HostVolumeInfo{"data": {HostPath: "/h/data"}},
			sizes: nil,
			want: []VolumeView{{Name: "data", Mounted: true, Mounts: []VolumeMount{{Service: "api", Path: "/app/data"}}, HostPath: "/h/data",
				BackedUp: true, Schedule: "15 * * * *", Keep: app.DefaultBackupKeep}},
		},
		{
			name: "a Postgres service's volume is not backed up by the volume engine, whatever its records say",
			app:  app.App{Services: []app.Service{svc("db", &app.Backup{Engine: app.BackupPostgres}, app.Volume{Name: "pg", Path: "/var/lib/postgresql/data"})}},
			host: hostVols,
			want: []VolumeView{
				{Name: "pg", Mounted: true, Mounts: []VolumeMount{{Service: "db", Path: "/var/lib/postgresql/data"}}, HostPath: "/h/pg"},
				{Name: "data", HostPath: "/h/data"},
				{Name: "old", HostPath: "/h/old"},
			},
		},
		{
			name: "read-only mounts, a volume mounted twice, and Nomad unreadable (no host paths)",
			app: app.App{Services: []app.Service{
				svc("web", nil, app.Volume{Name: "media", Path: "/media", ReadOnly: true}, data),
				svc("worker", nil, app.Volume{Name: "media", Path: "/in"}),
			}},
			host: nil,
			want: []VolumeView{
				{Name: "media", Mounted: true, Mounts: []VolumeMount{{Service: "web", Path: "/media", ReadOnly: true}, {Service: "worker", Path: "/in"}}},
				{Name: "data", Mounted: true, Mounts: []VolumeMount{{Service: "web", Path: "/app/data"}}},
			},
		},
		{
			name:  "an unmounted volume keeps its last size",
			app:   app.App{Services: []app.Service{svc("web", nil)}},
			host:  map[string]deploy.HostVolumeInfo{"old": {HostPath: "/h/old"}},
			sizes: sizes,
			want:  []VolumeView{{Name: "old", HostPath: "/h/old", Size: &VolumeSize{Bytes: 5 * 1024, MeasuredAt: t0.Add(-time.Hour)}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := volumeViews(tt.app, tt.host, tt.sizes, backups)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("volumeViews =\n %+v\nwant\n %+v", got, tt.want)
			}
		})
	}
}

// A copy's sizes file, its host volume and its backup records reach the panel.
func TestVolumesFromNomadAndTheStore(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	setDestination(t, e)
	deployHealthy(t, e, kuma(false))

	started := time.Date(2026, 10, 15, 1, 0, 0, 0, time.UTC)
	restarted := started.Add(30 * time.Minute)
	e.nomad.SetCopies("whoami", api.AllocationListStub{ID: "c1", TaskGroup: "web", ClientStatus: "running", JobVersion: 1,
		TaskStates: map[string]*api.TaskState{"web": {StartedAt: started, LastRestart: restarted}}})
	e.nomad.WriteAllocFile("c1", "alloc/data/caravana/sizes", "data 312\n")
	e.nomad.AddHostVolume("whoami_old") // a volume no service mounts
	if err := e.store.SaveVolumeSize(ctx, "whoami", "old", 9, started); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SaveBackup(ctx, store.BackupRecord{AppID: "whoami", ID: "web-1", Service: "web", Engine: app.BackupVolume,
		Status: store.BackupSucceeded, StartedAt: started}); err != nil {
		t.Fatal(err)
	}

	got, err := e.svc.Volumes(ctx, "whoami")
	if err != nil {
		t.Fatalf("Volumes: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Volumes = %+v, want data and old", got)
	}
	data, old := got[0], got[1]
	if data.Name != "data" || !data.Mounted || data.HostPath == "" || !data.BackedUp ||
		data.Size == nil || data.Size.Bytes != 312*1024 || !data.Size.MeasuredAt.Equal(restarted) ||
		data.LastBackup == nil || data.LastBackup.ID != "web-1" {
		t.Errorf("data = %+v, want mounted, on the host, 312 KiB measured at the restart, backed up by web-1", data)
	}
	if old.Name != "old" || old.Mounted || len(old.Mounts) != 0 || old.HostPath == "" || old.Size == nil || old.Size.Bytes != 9*1024 {
		t.Errorf("old = %+v, want unmounted with its stored size", old)
	}

	t.Run("a read that fails keeps the stored size", func(t *testing.T) {
		e.nomad.FailFS(403)
		got, err := e.svc.Volumes(ctx, "whoami")
		if err != nil || len(got) != 2 || got[0].Size == nil || got[0].Size.Bytes != 312*1024 {
			t.Errorf("Volumes with unreadable files = %+v, %v; want the stored 312 KiB", got, err)
		}
		e.nomad.FailFS(0)
	})

	t.Run("Nomad down: the definition and the store still answer", func(t *testing.T) {
		e.nomad.Down()
		got, err := e.svc.Volumes(ctx, "whoami")
		if err != nil {
			t.Fatalf("Volumes with Nomad down: %v", err)
		}
		if len(got) != 1 || got[0].Name != "data" || got[0].HostPath != "" || got[0].Size == nil || got[0].Size.Bytes != 312*1024 || !got[0].BackedUp {
			t.Errorf("Volumes with Nomad down = %+v, want data with no host path and its stored size", got)
		}
	})

	t.Run("an unknown app", func(t *testing.T) {
		if _, err := e.svc.Volumes(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Volumes of an unknown app = %v, want ErrNotFound", err)
		}
	})
}

// A size is stamped with the time its copy last measured, not with the time of
// the read; a copy that has no times is stamped now.
func TestMeasuredAt(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	started := now.Add(-5 * time.Hour)
	restarted := now.Add(-2 * time.Hour)
	tests := []struct {
		name string
		copy deploy.Copy
		want time.Time
	}{
		{"started only", deploy.Copy{StartedAt: &started}, started},
		{"restarted since", deploy.Copy{StartedAt: &started, LastRestart: &restarted}, restarted},
		{"no times", deploy.Copy{}, now},
	}
	for _, tt := range tests {
		if got := measuredAt(tt.copy, now); !got.Equal(tt.want) {
			t.Errorf("%s: measuredAt = %v, want %v", tt.name, got, tt.want)
		}
	}
}
