package ui

import (
	"context"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 016, US3: the Volumes section's rows, as a table of the cases the
// section must tell apart.
func TestVolumeRows(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	measured := now.Add(-2 * time.Hour)
	ran := now.Add(-26 * time.Hour)
	tests := []struct {
		name     string
		in       []service.VolumeView
		want     []volumeRow
		wantHint bool
	}{
		{name: "no volumes: no rows, no hint"},
		{
			name: "backed up, measured, last run succeeded",
			in: []service.VolumeView{{Name: "data", Mounted: true, Mounts: []service.VolumeMount{{Service: "kuma", Path: "/app/data"}},
				HostPath: "/h/data", Size: &service.VolumeSize{Bytes: 312 << 20, MeasuredAt: measured}, BackedUp: true, Schedule: "0 3 * * *", Keep: 7,
				LastBackup: &service.VolumeBackup{ID: "kuma-1", Status: store.BackupSucceeded, StartedAt: ran}}},
			want: []volumeRow{{Name: "data", Mounts: []string{"kuma:/app/data"}, HostPath: "/h/data", Size: "312 MB",
				Measured: whenOf(now, &measured, ""), BackedUp: true, Schedule: "every day at 03:00 UTC",
				HasLast: true, Last: whenOf(now, &ran, ""), LastStatus: statusView{"healthy", "succeeded"}}},
		},
		{
			name: "not measured, not backed up: the hint",
			in:   []service.VolumeView{{Name: "data", Mounted: true, Mounts: []service.VolumeMount{{Service: "web", Path: "/d", ReadOnly: true}}}},
			want: []volumeRow{{Name: "data", Mounts: []string{"web:/d (read-only)"}}}, wantHint: true,
		},
		{
			name: "backed up, never run, and a failed run's mark",
			in: []service.VolumeView{
				{Name: "a", Mounted: true, BackedUp: true, Schedule: "15 * * * *"},
				{Name: "b", Mounted: true, BackedUp: true, Schedule: "0 3 * * *", LastBackup: &service.VolumeBackup{Status: store.BackupFailed, StartedAt: ran}},
			},
			want: []volumeRow{
				{Name: "a", BackedUp: true, Schedule: "every hour at :15"},
				{Name: "b", BackedUp: true, Schedule: "every day at 03:00 UTC", HasLast: true, Last: whenOf(now, &ran, ""), LastStatus: statusView{"failed", "failed"}},
			},
		},
		{
			name: "a volume no service mounts is not a reason for the hint",
			in:   []service.VolumeView{{Name: "old", HostPath: "/h/old"}},
			want: []volumeRow{{Name: "old", Unmounted: true, HostPath: "/h/old"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, hint := volumeRows(now, tt.in)
			if !reflect.DeepEqual(got, tt.want) || hint != tt.wantHint {
				t.Errorf("volumeRows =\n %+v, hint %v\nwant\n %+v, hint %v", got, hint, tt.want, tt.wantHint)
			}
		})
	}
}

func TestHumanBytes(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1 KB"},
		{1536, "1.5 KB"},
		{4 << 10, "4 KB"},
		{10 << 10, "10 KB"},
		{312 << 20, "312 MB"},
		{327155712, "312 MB"},
		{(1 << 20) - 1, "1 MB"}, // 1023.999 KB, not "1024 KB"
		{1536 << 20, "1.5 GB"},
		{9<<30 + 990<<20, "10 GB"}, // 9.97 GB would print as "10.0"
		{5 << 40, "5 TB"},
	}
	for _, tt := range tests {
		if got := humanBytes(tt.n); got != tt.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

// Feature 016, US3: the Volumes section is first on the Backups tab, shows
// every column with its copy, and is plain HTML. It answers while Nomad can't
// be read.
func TestBackupsTabVolumes(t *testing.T) {
	ctx := context.Background()
	e := volumeBackupsEnv(t)
	// The page's clock stands still at the time the test began, so times are
	// set from it, a minute past the hour they should read as.
	clockNow := e.Clock.Now()
	ran := clockNow.Add(-2*time.Hour - time.Minute).UTC()
	if err := e.Store.SaveBackup(ctx, store.BackupRecord{AppID: "kuma", ID: "uptime-kuma-1000", Service: "uptime-kuma", Engine: "volume",
		Status: store.BackupSucceeded, Bytes: 2048, StartedAt: ran, Stopped: new(4 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	started := clockNow.Add(-3*time.Hour - time.Minute)
	e.Nomad.SetCopies("kuma", api.AllocationListStub{ID: "c1", TaskGroup: "uptime-kuma", ClientStatus: "running", JobVersion: 1,
		TaskStates: map[string]*api.TaskState{"uptime-kuma": {StartedAt: started}}})
	e.Nomad.WriteAllocFile("c1", "alloc/data/caravana/sizes", "data 319488\n") // 312 MB
	e.Nomad.AddHostVolume("kuma_old")
	if err := e.Store.SaveVolumeSize(ctx, "kuma", "old", 5, started.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	_, body := e.get(t, "/apps/kuma/backups")
	got := text(body)
	for _, want := range []string{
		"Volumes", "Mounted at", "On the host", "Size", "Backup",
		"uptime-kuma:/app/data", "312 MB", "measured 3 hours ago",
		"every day at 03:00 UTC", "succeeded, last 2 hours ago",
		"old", "not mounted", "5 KB",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the Volumes section lacks %q:\n%s", want, got)
		}
	}
	// The lab's console-volumes step (cmd/caravana/console_lab_test.go) reads
	// the page by these patterns, so the markup stays in step with it.
	for _, pattern := range []string{
		`>(\d+(?:\.\d)? (?:B|KB|MB|GB))</span>\s*<span class="cv-usage-of">measured `,
		`<span class="cv-data text-fg-1">data</span>`,
		`succeeded</span>, last <time`,
		`(?s)<details class="cv-confirm">\s*<summary[^>]*>Back up now…</summary>\s*<form method="post" action="(/apps/kuma/backups)"[^>]*>(.*?)</form>`,
		`stopped (?:under 1|\d+) s`,
	} {
		if !regexp.MustCompile(pattern).MatchString(body) {
			t.Errorf("the page doesn't match the lab step's pattern %s", pattern)
		}
	}
	if !strings.Contains(body, "/host_volumes/") {
		t.Errorf("the host path isn't shown:\n%s", body)
	}
	volumes, services := strings.Index(body, `id="volumes-title"`), strings.Index(body, `id="databases-title"`)
	if volumes < 0 || volumes > services {
		t.Error("the Volumes section isn't first on the tab")
	}
	// The backed-up volume needs no hint; the unmounted one never does.
	if strings.Contains(got, "Set a backup on the service in Configure") {
		t.Error("the hint shows although the only mounted volume is backed up")
	}
	if section := body[volumes:services]; strings.Contains(section, "<script") || strings.Contains(section, "hx-") {
		t.Errorf("the Volumes section needs scripting:\n%s", section)
	}

	t.Run("Nomad can't be read: the section still answers", func(t *testing.T) {
		e.Nomad.Down()
		resp, body := e.get(t, "/apps/kuma/backups")
		got := text(body)
		if resp.StatusCode != http.StatusOK || !strings.Contains(got, "uptime-kuma:/app/data") || !strings.Contains(got, "312 MB") ||
			strings.Contains(got, "not mounted") {
			t.Errorf("the tab with Nomad down = %d:\n%s", resp.StatusCode, got)
		}
	})
}

// A volume that isn't backed up shows "not backed up" and the hint with a
// link to Configure; an app without a volume has no section at all.
func TestBackupsTabVolumesNotBackedUp(t *testing.T) {
	e := newConsoleEnv(t)
	a := kumaApp()
	a.ID, a.Services[0].Backup = "plain", nil
	e.seed(t, a)
	_, body := e.get(t, "/apps/plain/backups")
	got := text(body)
	if !strings.Contains(got, "not backed up") || !strings.Contains(got, "not measured yet") ||
		!strings.Contains(got, "Set a backup on the service in Configure to keep a copy off this server.") ||
		!strings.Contains(body, `<a href="/apps/plain/configure">Configure</a>`) {
		t.Errorf("the tab of a volume without a backup:\n%s", got)
	}

	e.seed(t, whoamiApp())
	_, body = e.get(t, "/apps/whoami/backups")
	if strings.Contains(body, `id="volumes-title"`) {
		t.Error("an app with no volume has a Volumes section")
	}
}
