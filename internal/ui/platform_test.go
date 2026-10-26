package ui

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/notify"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 025: the Apps home's Platform panel and the needs-you row for a disk nearly full.

func TestPlatformOf(t *testing.T) {
	now := time.Date(2026, 10, 25, 14, 0, 0, 0, time.UTC)
	ok := hostSample(now)

	p := platformOf(ok)
	if p.Mark != "healthy" || p.Line != "1 server up" || len(p.Meters) != 2 || p.Room == nil {
		t.Fatalf("healthy = %+v", p)
	}
	if p.Meters[0].Label != "mem" || p.Meters[0].Read != "1.9/3.8G" || !p.Meters[0].HasReserved || p.Meters[1].Label != "disk" || p.Meters[1].Read != "18/40G" {
		t.Errorf("meters = %+v", p.Meters)
	}
	if p.Room.Text.String() != "Room for about 4 more apps like whoami (256 MB, 100 MHz each)." {
		t.Errorf("room = %q", p.Room.Text.String())
	}

	// The fuller disk, with no mount in its readout: the panel has room for one.
	two := ok
	two.Disks = []service.HostDisk{
		{Mount: "/", UsedMB: 4096, TotalMB: 40960, FreeMB: 36864, UsedPercent: 10, State: metrics.DiskOK},
		{Mount: "/var/lib/docker", UsedMB: 36864, TotalMB: 40960, FreeMB: 4096, UsedPercent: 90, State: metrics.DiskWarning},
	}
	d := platformOf(two).Meters[1]
	if d.Read != "36/40G" || d.Mark != "warning" || !d.Warn {
		t.Errorf("fuller disk = %+v", d)
	}

	// Can't be read: Nomad's refusal, its silence, a sample that is too old. No meters, no room.
	stale := ok
	old := now.Add(-10 * time.Minute)
	stale.SampledAt, stale.Stale = &old, true
	denied := ok
	denied.Sources.Nomad = service.SamplerDenied
	gone := ok
	gone.SampledAt, gone.Sources.Nomad = nil, service.SamplerUnreachable
	for name, h := range map[string]service.HostView{"stale": stale, "denied": denied, "unreachable and never read": gone} {
		p := platformOf(h)
		if p.Mark != "failed" || p.Line != "Can't read the host's numbers" || len(p.Meters) != 0 || p.Room != nil {
			t.Errorf("%s = %+v", name, p)
		}
	}
	// Never read, and nothing wrong: waiting, not failed.
	p = platformOf(service.HostView{Sources: service.SamplerSources{Nomad: service.SamplerUnknown}})
	if p.Mark != "idle" || p.Line != "Waiting for the first reading of the host" {
		t.Errorf("never read = %+v", p)
	}
}

func TestDiskNeedsYouRow(t *testing.T) {
	e := newConsoleEnv(t)
	pl := places{}
	now := time.Now()
	for _, tc := range []struct {
		name string
		it   service.AttentionItem
		text string
		mark string
	}{
		{"one filesystem", service.AttentionItem{Kind: service.AttentionDiskNearlyFull, Mount: "/var/lib/docker", Holds: []string{"docker"},
			UsedPercent: 86.4, FreeBytes: 11 << 30, Level: metrics.DiskWarning},
			"The disk at /var/lib/docker is 86% full: 11 GB left.", "warning"},
		{"both held, failed", service.AttentionItem{Kind: service.AttentionDiskNearlyFull, Mount: "/", Holds: []string{"nomad", "docker"},
			UsedPercent: 96.9, FreeBytes: 1536 << 20, Level: metrics.DiskFailed},
			"The disk at / is 96% full: 1.5 GB left.", "failed"},
		{"just under failed is still a warning", service.AttentionItem{Kind: service.AttentionDiskNearlyFull, Mount: "/data", Holds: []string{"nomad"},
			UsedPercent: 94.6, FreeBytes: 600 << 20, Level: metrics.DiskWarning},
			"The disk at /data is 94% full: 600 MB left.", "warning"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := e.Server.attentionRow(now, tc.it, pl, nil)
			// The same sentence the API's detail and the notifications carry.
			if want := notify.DiskSentence(tc.it.Mount, tc.it.UsedPercent, tc.it.FreeBytes); row.Text.String() != want {
				t.Errorf("row = %q, the shared sentence is %q", row.Text.String(), want)
			}
			if row.Text.String() != tc.text || row.Mark != tc.mark || row.Action != "Open monitoring" || row.Href != "/monitoring#host" {
				t.Errorf("row = %q (%s) %q -> %s, want %q (%s)", row.Text.String(), row.Mark, row.Action, row.Href, tc.text, tc.mark)
			}
			if !row.Text[1].Mono && !row.Text[len(row.Text)-2].Mono {
				t.Error("the filesystem isn't set in mono")
			}
		})
	}
}

func TestAppsHomePlatformPanel(t *testing.T) {
	e := monHostEnv(t, 30*time.Second, store.DiskSample{Node: "node-1", Mount: "/", Holds: "nomad,docker", UsedMB: 18432, TotalMB: 40960, FreeMB: 22528})
	resp, body := e.get(t, "/apps")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /apps = %d", resp.StatusCode)
	}
	for _, want := range []string{`id="platform" hx-get="/parts/platform" hx-trigger="sse:metrics"`, `<a href="/monitoring#host" class="cv-panel cv-mnplat"`,
		`class="cv-mark cv-mark-healthy"`, `class="cv-mnroom" title="Nomad&#39;s count of what fits.`} {
		if !strings.Contains(body, want) {
			t.Errorf("the home lacks %q", want)
		}
	}
	panel := text(body[strings.Index(body, `id="platform"`):strings.Index(body, `id="activity-title"`)])
	for _, want := range []string{"This host", "1 server up", "mem", "1.9/3.8G", "disk", "18/40G", "Room for about 14 more apps like whoami (256 MB, 100 MHz each)."} {
		if !strings.Contains(panel, want) {
			t.Errorf("the panel lacks %q:\n%s", want, panel)
		}
	}
	// Above Activity, and a plain link: no style attribute, no script.
	if strings.Index(body, `id="platform"`) > strings.Index(body, `id="activity-title"`) {
		t.Error("the Platform panel is below Activity")
	}
	if regexp.MustCompile(monNoStyle).MatchString(body) {
		t.Error("the home has a style attribute")
	}
	resp, part := e.get(t, "/parts/platform")
	if resp.StatusCode != http.StatusOK || strings.Contains(part, "<html") || !strings.Contains(text(part), "1 server up") || !strings.Contains(part, `href="/monitoring#host"`) {
		t.Errorf("part = %d:\n%s", resp.StatusCode, text(part))
	}
}

func TestAppsHomePlatformCantRead(t *testing.T) {
	e := monHostEnv(t, 12*time.Minute)
	_, body := e.get(t, "/apps")
	panel := body[strings.Index(body, `id="platform"`):strings.Index(body, `id="activity-title"`)]
	if !strings.Contains(text(panel), "Can't read the host's numbers") || !strings.Contains(panel, `cv-mark cv-mark-failed`) || strings.Contains(panel, `class="cv-mnmeter"`) {
		t.Errorf("stale panel:\n%s", text(panel))
	}
	never := newConsoleEnv(t)
	_, body = never.get(t, "/apps")
	if !strings.Contains(text(body), "Waiting for the first reading of the host") {
		t.Errorf("never read:\n%s", text(body))
	}
}

// A disk at 90 % is a needs-you row on the home, in the list part, with Open monitoring; at 96 % it
// is failed.
func TestAppsHomeDiskNeedsYou(t *testing.T) {
	e := monHostEnv(t, 30*time.Second, store.DiskSample{Node: "node-1", Mount: "/", Holds: "nomad,docker", UsedMB: 36864, TotalMB: 40960, FreeMB: 4096})
	for _, path := range []string{"/apps", "/parts/attention"} {
		_, body := e.get(t, path)
		words := text(body)
		if !strings.Contains(words, "The disk at / is 90% full: 4.0 GB left.") || !strings.Contains(words, "Open monitoring") {
			t.Errorf("GET %s lacks the disk row:\n%s", path, words)
		}
		if !strings.Contains(body, `<a href="/monitoring#host" class="cv-btn cv-btn-secondary cv-btn-sm">Open monitoring</a>`) || !strings.Contains(body, `cv-mark cv-mark-warning`) {
			t.Errorf("GET %s: the row's action or mark is wrong", path)
		}
	}
	failed := monHostEnv(t, 30*time.Second, store.DiskSample{Node: "node-1", Mount: "/", Holds: "nomad,docker", UsedMB: 39400, TotalMB: 40960, FreeMB: 1560})
	_, body := failed.get(t, "/parts/attention")
	if !strings.Contains(text(body), "is 96% full") || !strings.Contains(body, `cv-mark cv-mark-failed`) {
		t.Errorf("a disk at 96%%:\n%s", text(body))
	}
}
