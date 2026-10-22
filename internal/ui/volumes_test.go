package ui

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

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

// Feature 016's facts, re-homed into the rows of the Backups tab (feature 021): a volume
// service's mounts, its host path, its size and its last backup are all on the page, and
// it answers while Nomad can't be read.
func TestBackupsTabVolumes(t *testing.T) {
	ctx := context.Background()
	e := volumeBackupsEnv(t)
	// The page's clock stands still at the time the test began, so times are
	// set from it, a minute past the hour they should read as.
	clockNow := e.Clock.Now()
	ran := clockNow.Add(-2*time.Hour - time.Minute).UTC()
	finished := ran.Add(4 * time.Second)
	if err := e.Store.SaveBackup(ctx, store.BackupRecord{AppID: "kuma", ID: "uptime-kuma-1000", Service: "uptime-kuma", Engine: "volume",
		Status: store.BackupSucceeded, Bytes: 2048, StartedAt: ran, FinishedAt: &finished, Stopped: new(4 * time.Second)}); err != nil {
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
		"Volume · /app/data",                  // what the row backs up, by mount
		"Every day at 03:00 UTC", "3 backups", // how often, and how many are kept
		"The service stops while it runs.",      // what a volume run does
		"2 hours ago", "took 4 s · stopped 4 s", // the last backup, and the stop
		"2 KB", "2 KB all", // its size, and the kept runs' sum
		"/host_volumes/", "on this host · 312 MB, measured 3 hours ago", // where the volume lives, and its size
		"Volumes no service mounts", "old", "5 KB", // the volume Nomad has that nothing mounts
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the tab lacks %q:\n%s", want, got)
		}
	}
	// The lab's console-volumes step (cmd/caravana/console_lab_test.go) reads the page by
	// these patterns, so the markup stays in step with it.
	for _, pattern := range []string{
		`<span class="cv-bname">uptime-kuma</span>`,
		`<span class="cv-bmeta">Volume · /app/data</span>`,
		`<span class="cv-bmeta">took \d+(?: s| min[^<]*)(?: · stopped (?:under 1|\d+) s)?</span>`,
		`(?s)<details class="cv-confirm">\s*<summary[^>]*>.*?Back up now</summary>\s*<form method="post" action="(/apps/kuma/backups)"[^>]*>(.*?)</form>`,
		`class="cv-bpath">[^<]*on this host · (\d+(?:\.\d)? (?:B|KB|MB|GB)), measured [^<]*</p>`,
	} {
		if !regexp.MustCompile(pattern).MatchString(body) {
			t.Errorf("the page doesn't match the lab step's pattern %s", pattern)
		}
	}
	if tab := body[strings.Index(body, `id="backups-title"`):]; strings.Contains(tab[:strings.Index(tab, "</main>")], "<script") || strings.Contains(tab[:strings.Index(tab, "</main>")], "hx-") {
		t.Error("the tab needs scripting")
	}

	t.Run("Nomad can't be read: the tab still answers", func(t *testing.T) {
		e.Nomad.Down()
		resp, body := e.get(t, "/apps/kuma/backups")
		got := text(body)
		if resp.StatusCode != http.StatusOK || !strings.Contains(got, "Volume · /app/data") || !strings.Contains(got, "2 KB") ||
			strings.Contains(got, "Volumes no service mounts") {
			t.Errorf("the tab with Nomad down = %d:\n%s", resp.StatusCode, got)
		}
	})
}

// A service with a volume and no backup gets a gap row, with the volume's size when it
// is known; an app with nothing to back up says so.
func TestBackupsTabVolumesNotBackedUp(t *testing.T) {
	e := newConsoleEnv(t)
	a := kumaApp()
	a.ID, a.Services[0].Backup = "plain", nil
	e.seed(t, a)
	_, body := e.get(t, "/apps/plain/backups")
	got := text(body)
	if !strings.Contains(got, "uptime-kuma · /app/data has no backup.") || !strings.Contains(got, "Volume") || !strings.Contains(got, "0 backed up · 0 failing · 1 not") {
		t.Errorf("the tab of a volume without a backup:\n%s", got)
	}

	e.seed(t, whoamiApp())
	_, body = e.get(t, "/apps/whoami/backups")
	if got := text(body); !strings.Contains(got, "Nothing to back up.") || strings.Contains(got, "has no backup.") {
		t.Errorf("the tab of an app with nothing to back up:\n%s", got)
	}
}

// serviceVolumeBytes sums what the service's volumes measured, and says when none was.
func TestServiceVolumeBytes(t *testing.T) {
	vols := []service.VolumeView{
		{Name: "a", Mounted: true, Mounts: []service.VolumeMount{{Service: "web", Path: "/a"}}, Size: &service.VolumeSize{Bytes: 100}},
		{Name: "b", Mounted: true, Mounts: []service.VolumeMount{{Service: "web", Path: "/b"}}, Size: &service.VolumeSize{Bytes: 50}},
		{Name: "c", Mounted: true, Mounts: []service.VolumeMount{{Service: "db", Path: "/c"}}},
	}
	if got, ok := serviceVolumeBytes("web", vols); !ok || got != 150 {
		t.Errorf("web = %d, %v; want 150, true", got, ok)
	}
	if _, ok := serviceVolumeBytes("db", vols); ok {
		t.Error("db's volume was never measured, and the sum says it was")
	}
}
