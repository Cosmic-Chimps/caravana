package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/hostos"
	"github.com/cosmic-chimps/caravana/internal/notify"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// fakeGitHub is a server that answers the release check, the installer and the
// Traefik job of a release, and keeps every request it got.
type fakeGitHub struct {
	*httptest.Server
	mu       sync.Mutex
	requests []seenRequest
	status   int    // of the release answer; 200 when zero
	etag     string // sent with a 200
	release  string // the body of a 200
	install  string // the installer's text; empty answers 404
	job      string // the Traefik job's text; empty answers 404
}

type seenRequest struct {
	Path   string
	Header http.Header
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.requests = append(g.requests, seenRequest{r.URL.Path, r.Header.Clone()})
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			if g.etag != "" && r.Header.Get("If-None-Match") == g.etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			if g.status != 0 && g.status != http.StatusOK {
				w.WriteHeader(g.status)
				_, _ = w.Write([]byte("SENTINEL-GITHUB-TEXT"))
				return
			}
			if g.etag != "" {
				w.Header().Set("ETag", g.etag)
			}
			_, _ = w.Write([]byte(g.release))
		case strings.HasSuffix(r.URL.Path, "/install.sh") && g.install != "":
			_, _ = w.Write([]byte(g.install))
		case strings.HasSuffix(r.URL.Path, "/traefik.nomad.hcl") && g.job != "":
			_, _ = w.Write([]byte(g.job))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(g.Close)
	return g
}

func (g *fakeGitHub) set(f func(g *fakeGitHub)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	f(g)
}

func (g *fakeGitHub) seen() []seenRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.requests)
}

func (g *fakeGitHub) count(suffix string) int {
	n := 0
	for _, r := range g.seen() {
		if strings.HasSuffix(r.Path, suffix) {
			n++
		}
	}
	return n
}

// releaseJSON is GitHub's answer for a release.
func releaseBody(tag string, extra string) string {
	return fmt.Sprintf(`{"tag_name":%q,"html_url":"https://github.com/Cosmic-Chimps/caravana/releases/tag/%s",`+
		`"published_at":"2026-11-02T10:00:00Z","body":"Highlights\n\n- One thing.\n- Another thing.\n* Third, with a star.\n","draft":false,"prerelease":false%s,`+
		`"assets":[{"name":"install.sh","browser_download_url":"https://github.com/x/install.sh"}]}`, tag, tag, extra)
}

// updatesLab is a notify env whose service runs 0.15.0 and asks fake GitHub.
type updatesLab struct {
	*notifyEnv
	gh *fakeGitHub
}

func newUpdatesLab(t *testing.T) *updatesLab {
	t.Helper()
	l := &updatesLab{notifyEnv: newNotifyEnv(t), gh: newFakeGitHub(t)}
	l.clockAt(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC))
	l.svc.SetVersion("0.15.0")
	l.svc.SetReleaseURL(l.gh.URL + "/repos/x/releases/latest")
	l.svc.SetReleasePinURLs(l.gh.URL+"/download", l.gh.URL+"/raw")
	l.gh.set(func(g *fakeGitHub) { g.etag, g.release = `"e1"`, releaseBody("v0.16.0", "") })
	return l
}

func (l *updatesLab) releaseRow(t *testing.T) store.ReleaseCheck {
	t.Helper()
	rc, err := l.store.ReleaseCheck(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return rc
}

func (l *updatesLab) check(t *testing.T) {
	t.Helper()
	l.svc.releaseCheck(context.Background())
}

func (l *updatesLab) view(t *testing.T) UpdatesView {
	t.Helper()
	v, err := l.svc.Updates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// The story of a release: found, shown, told once to every channel that isn't
// paused, not told again for the same release (a 304, or the same tag served
// again), told again for the next one.
func TestReleaseCheckFindsAndTellsOnce(t *testing.T) {
	l := newUpdatesLab(t)
	l.check(t)

	v := l.view(t).Caravana
	if v.Current != "0.15.0" || v.Latest != "0.16.0" || v.URL != "https://github.com/Cosmic-Chimps/caravana/releases/tag/v0.16.0" ||
		!v.ReleasedAt.Equal(time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)) || !v.CheckEnabled || v.CheckError != "" || v.CheckedAt.IsZero() {
		t.Errorf("caravana = %+v", v)
	}
	if want := []ReleaseNote{{Text: "One thing."}, {Text: "Another thing."}, {Text: "Third, with a star."}}; !slices.Equal(v.Notes, want) {
		t.Errorf("notes = %v, want %v", v.Notes, want)
	}
	if want := "curl -fsSL https://github.com/Cosmic-Chimps/caravana/releases/download/v0.16.0/install.sh | sudo bash"; v.Install != want {
		t.Errorf("install = %q", v.Install)
	}

	// One message each for the channels that aren't paused, whatever they asked for.
	for name, ch := range map[string]store.NotificationChannel{"problems": l.problems, "only-deploy": l.onlyDeploy} {
		ds := l.deliveriesOf(t, ch)
		if len(ds) != 1 || ds[0].EventType != notify.EventCaravanaRelease || ds[0].Status != store.DeliveryPending {
			t.Fatalf("%s: deliveries = %+v, want one pending caravana.release", name, ds)
		}
		ev := payloadOf(t, ds[0])
		if ev.Title != "New release: caravana 0.16.0" || ev.Text != "caravana 0.16.0 is out. You run 0.15.0." ||
			ev.Release == nil || *ev.Release != (notify.Release{Current: "0.15.0", Latest: "0.16.0", URL: v.URL}) || ev.URL != v.URL || ev.Kind != "" {
			t.Errorf("%s: event = %+v", name, ev)
		}
	}
	if n := len(l.deliveriesOf(t, l.paused)); n != 0 {
		t.Errorf("the paused channel got %d messages", n)
	}
	if rc := l.releaseRow(t); rc.NotifiedTag != "v0.16.0" || rc.ETag != `"e1"` {
		t.Errorf("row = %+v", rc)
	}

	// A day later: a 304 answers If-None-Match, and nothing is told again.
	l.svc.clock.(*fakeClock).Advance(24 * time.Hour)
	l.check(t)
	reqs := l.gh.seen()
	var latest []seenRequest
	for _, r := range reqs {
		if strings.HasSuffix(r.Path, "/releases/latest") {
			latest = append(latest, r)
		}
	}
	if len(latest) != 2 || latest[0].Header.Get("If-None-Match") != "" || latest[1].Header.Get("If-None-Match") != `"e1"` {
		t.Fatalf("release requests = %+v; want the second to ask If-None-Match", latest)
	}
	if n := len(l.deliveriesOf(t, l.problems)); n != 1 {
		t.Errorf("after a 304 the channel has %d messages, want 1", n)
	}
	if rc := l.releaseRow(t); !rc.CheckedAt.Equal(l.svc.clock.Now()) {
		t.Errorf("checked at %v after the 304, want the check's time", rc.CheckedAt)
	}

	// The same release served again (a changed ETag, say): still once.
	l.gh.set(func(g *fakeGitHub) { g.etag = `"e2"` })
	l.svc.clock.(*fakeClock).Advance(24 * time.Hour)
	l.check(t)
	if n := len(l.deliveriesOf(t, l.problems)); n != 1 {
		t.Errorf("after the same tag again the channel has %d messages, want 1", n)
	}

	// The next release is told.
	l.gh.set(func(g *fakeGitHub) { g.etag, g.release = `"e3"`, releaseBody("v0.17.0", "") })
	l.svc.clock.(*fakeClock).Advance(24 * time.Hour)
	l.check(t)
	ds := l.deliveriesOf(t, l.problems)
	if len(ds) != 2 || payloadOf(t, ds[1]).Release.Latest != "0.17.0" {
		t.Errorf("deliveries = %+v, want a second, for 0.17.0", ds)
	}
	if got := l.view(t).Caravana.Latest; got != "0.17.0" {
		t.Errorf("latest = %q", got)
	}
}

// The request is small: a User-Agent and the stored ETag, no token, no body.
func TestReleaseCheckRequestIsSmall(t *testing.T) {
	l := newUpdatesLab(t)
	l.check(t)
	l.check(t)
	for _, r := range l.gh.seen() {
		for name := range r.Header {
			switch name {
			case "User-Agent", "If-None-Match", "Accept-Encoding":
			default:
				t.Errorf("%s was sent with header %s", r.Path, name)
			}
		}
		if ua := r.Header.Get("User-Agent"); ua != "caravana/0.15.0" {
			t.Errorf("%s: User-Agent = %q", r.Path, ua)
		}
	}
}

func TestReleaseCheckOnlyNewerReleasesCount(t *testing.T) {
	for name, tc := range map[string]struct{ release, running string }{
		"the same version":       {releaseBody("v0.15.0", ""), "0.15.0"},
		"an older one":           {releaseBody("v0.14.0", ""), "0.15.0"},
		"a pre-release":          {releaseBody("v0.99.0", "")[:0] + `{"tag_name":"v0.99.0","prerelease":true,"body":"- x","published_at":"2026-11-02T10:00:00Z"}`, "0.15.0"},
		"a draft":                {`{"tag_name":"v0.99.0","draft":true,"body":"- x"}`, "0.15.0"},
		"by number, not by text": {releaseBody("v0.9.0", ""), "0.15.0"},
	} {
		t.Run(name, func(t *testing.T) {
			l := newUpdatesLab(t)
			l.svc.SetVersion(tc.running)
			l.gh.set(func(g *fakeGitHub) { g.release = tc.release })
			l.check(t)
			if v := l.view(t).Caravana; v.Latest != "" || v.CheckError != "" {
				t.Errorf("caravana = %+v; want no newer release and no error", v)
			}
			for _, ch := range []store.NotificationChannel{l.problems, l.onlyDeploy} {
				if n := len(l.deliveriesOf(t, ch)); n != 0 {
					t.Errorf("a message was queued: %d", n)
				}
			}
			if l.releaseRow(t).NotifiedTag != "" {
				t.Error("notified_tag set")
			}
		})
	}
	t.Run("0.10.0 is newer than 0.9.0", func(t *testing.T) {
		l := newUpdatesLab(t)
		l.svc.SetVersion("0.9.0")
		l.gh.set(func(g *fakeGitHub) { g.release = releaseBody("v0.10.0", "") })
		l.check(t)
		if got := l.view(t).Caravana.Latest; got != "0.10.0" {
			t.Errorf("latest = %q", got)
		}
	})
}

// A running version that isn't a release never compares, never asks and never
// notifies.
func TestReleaseCheckDevBuild(t *testing.T) {
	for _, version := range []string{"dev", "0.0.0-dev.1730000000", ""} {
		t.Run("version "+version, func(t *testing.T) {
			l := newUpdatesLab(t)
			l.svc.SetVersion(version)
			l.check(t)
			if err := l.svc.CheckRelease(context.Background()); !errors.Is(err, ErrCheckUnavailable) {
				t.Errorf("CheckRelease = %v, want ErrCheckUnavailable", err)
			} else if !strings.Contains(err.Error(), "development build") {
				t.Errorf("the sentence = %q", err)
			}
			if n := len(l.gh.seen()); n != 0 {
				t.Errorf("%d requests were made", n)
			}
			if v := l.view(t).Caravana; v.Latest != "" || !v.CheckedAt.IsZero() {
				t.Errorf("caravana = %+v", v)
			}
			if n := len(l.deliveriesOf(t, l.problems)); n != 0 {
				t.Errorf("a message was queued: %d", n)
			}
		})
	}
}

// With the owner's setting off Caravana makes no request at all: not the daily
// check, not Check now, not for the pins.
func TestReleaseCheckOffMakesNoRequest(t *testing.T) {
	ctx := context.Background()
	l := newUpdatesLab(t)
	if err := l.svc.SetReleaseCheck(ctx, false); err != nil {
		t.Fatal(err)
	}
	l.check(t)
	if err := l.svc.CheckRelease(ctx); !errors.Is(err, ErrCheckDisabled) {
		t.Errorf("CheckRelease = %v, want ErrCheckDisabled", err)
	}
	// The loop, with a gap of milliseconds, over a few rounds.
	loop, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { l.svc.runReleaseCheck(loop, 5*time.Millisecond); close(done) }()
	time.Sleep(60 * time.Millisecond)
	stop()
	<-done
	if n := len(l.gh.seen()); n != 0 {
		t.Fatalf("%d requests were made with the check off", n)
	}
	if v := l.view(t).Caravana; v.CheckEnabled || v.Latest != "" {
		t.Errorf("caravana = %+v", v)
	}

	// Turning it on again: the next check asks, and the setting survived it.
	if err := l.svc.SetReleaseCheck(ctx, true); err != nil {
		t.Fatal(err)
	}
	l.check(t)
	if n := l.gh.count("/releases/latest"); n != 1 {
		t.Errorf("requests with the check on = %d, want 1", n)
	}
	if v := l.view(t).Caravana; !v.CheckEnabled || v.Latest != "0.16.0" {
		t.Errorf("caravana = %+v", v)
	}
}

// Turning the check off while one runs is not undone by its result.
func TestReleaseResultDoesNotUndoTheSetting(t *testing.T) {
	ctx := context.Background()
	l := newUpdatesLab(t)
	l.check(t)
	if err := l.svc.SetReleaseCheck(ctx, false); err != nil {
		t.Fatal(err)
	}
	rc := l.releaseRow(t)
	rc.Enabled = true // what a stale copy of the row would write back
	if err := l.store.SaveReleaseResult(ctx, rc, nil); err != nil {
		t.Fatal(err)
	}
	if l.releaseRow(t).Enabled {
		t.Error("a result turned the check back on")
	}
}

func TestReleaseCheckFailures(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"GitHub is down", 500, "", failGitHubDown},
		{"rate limited", 403, "", failGitHubLimit},
		{"too many requests", 429, "", failGitHubLimit},
		{"no release yet", 404, "", failGitHubNone},
		{"an answer that isn't JSON", 200, "<html>SENTINEL-GITHUB-TEXT</html>", failGitHubBad},
		{"a tag that isn't a version", 200, `{"tag_name":"nightly","body":"SENTINEL-GITHUB-TEXT"}`, failGitHubBad},
		{"an answer past the limit", 200, `{"body":"` + strings.Repeat("x", releaseResponseLimit) + `"}`, failGitHubBad},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := newUpdatesLab(t)
			l.check(t) // a good answer first: what it found stays
			goodAt := l.svc.clock.Now()
			l.gh.set(func(g *fakeGitHub) {
				g.etag, g.status, g.release = "", tc.status, tc.body
			})
			l.svc.clock.(*fakeClock).Advance(24 * time.Hour)
			l.check(t)
			v := l.view(t).Caravana
			// The last good check stays where it was: "last checked" on the page is that one.
			if !v.CheckedOKAt.Equal(goodAt) || !v.CheckedAt.After(v.CheckedOKAt) {
				t.Errorf("checkedOkAt = %v, checkedAt = %v; want the good check at %v and a later attempt", v.CheckedOKAt, v.CheckedAt, goodAt)
			}
			if v.CheckError != tc.want {
				t.Errorf("error = %q, want %q", v.CheckError, tc.want)
			}
			if strings.Contains(v.CheckError, "SENTINEL") {
				t.Error("GitHub's own text is in the error")
			}
			if v.Latest != "0.16.0" || !v.CheckedAt.Equal(l.svc.clock.Now()) {
				t.Errorf("the earlier answer or the attempt time was lost: %+v", v)
			}
			// A good answer clears it.
			l.gh.set(func(g *fakeGitHub) { g.status, g.release = 0, releaseBody("v0.16.0", "") })
			l.svc.clock.(*fakeClock).Advance(24 * time.Hour)
			l.check(t)
			if v := l.view(t).Caravana; v.CheckError != "" || !v.CheckedOKAt.Equal(v.CheckedAt) {
				t.Errorf("after a good answer: error = %q, checkedOkAt = %v, checkedAt = %v", v.CheckError, v.CheckedOKAt, v.CheckedAt)
			}
			if n := len(l.deliveriesOf(t, l.problems)); n != 1 {
				t.Errorf("messages = %d, want 1 (a failure and a retry tell nothing)", n)
			}
		})
	}
	t.Run("a server that isn't there", func(t *testing.T) {
		l := newUpdatesLab(t)
		l.gh.Close()
		l.check(t)
		v := l.view(t).Caravana
		if v.CheckError != failGitHubDown {
			t.Errorf("error = %q", v.CheckError)
		}
		// Never a good check: there is nothing to say was "last checked".
		if !v.CheckedOKAt.IsZero() || v.CheckedAt.IsZero() {
			t.Errorf("checkedOkAt = %v, checkedAt = %v; want none and an attempt", v.CheckedOKAt, v.CheckedAt)
		}
	})
}

// The release notes are kept up to 16 KiB.
func TestReleaseBodyIsCapped(t *testing.T) {
	l := newUpdatesLab(t)
	big := strings.Repeat("- é a long line of notes\\n", 2000) // two bytes in each é
	l.gh.set(func(g *fakeGitHub) {
		g.release = `{"tag_name":"v0.16.0","published_at":"2026-11-02T10:00:00Z","body":"` + big + `"}`
	})
	l.check(t)
	rc := l.releaseRow(t)
	if n := len(rc.Body); n == 0 || n > releaseBodyLimit {
		t.Errorf("body is %d bytes, want 1 to %d", n, releaseBodyLimit)
	}
	if !strings.HasSuffix(rc.Body, "notes") && !strings.HasSuffix(rc.Body, "no") && len([]rune(rc.Body)) == 0 {
		t.Error("the body was cut inside a character")
	}
	if v := l.view(t).Caravana; len(v.Notes) != releaseNotesLines {
		t.Errorf("notes = %d lines, want %d", len(v.Notes), releaseNotesLines)
	}
}

// A page address that isn't http or https is not made a link.
func TestPageURL(t *testing.T) {
	for given, want := range map[string]string{
		"https://github.com/Cosmic-Chimps/caravana/releases/tag/v0.16.0": "https://github.com/Cosmic-Chimps/caravana/releases/tag/v0.16.0",
		"javascript:alert(1)": releasePageURL + "v0.16.0",
		"":                    releasePageURL + "v0.16.0",
		"//evil.example/x":    releasePageURL + "v0.16.0",
		"data:text/html,x":    releasePageURL + "v0.16.0",
	} {
		if got := pageURL(given, "v0.16.0"); got != want {
			t.Errorf("pageURL(%q) = %q, want %q", given, got, want)
		}
	}
}

// The pins of a newer release are read from its installer and its Traefik job,
// and shown only where they change.
func TestReleasePins(t *testing.T) {
	setup := func(t *testing.T, install, job string) *updatesLab {
		l := newUpdatesLab(t)
		l.svc.sampler.node = &HostNode{NodeFacts: deploy.NodeFacts{NomadVersion: "2.0.7", TraefikVersion: "v3.7.13"}}
		l.gh.set(func(g *fakeGitHub) { g.install, g.job = install, job })
		l.check(t)
		return l
	}
	const install = "#!/usr/bin/env bash\nCARAVANA_VERSION=0.16.0\nNOMAD_VERSION=2.1.0\nCNI_VERSION=1.9.1\n"
	const job = "job \"traefik\" {\n  task \"traefik\" {\n    config {\n        image        = \"traefik:v3.8.0\"\n    }\n  }\n}\n"

	t.Run("both change", func(t *testing.T) {
		l := setup(t, install, job)
		v := l.view(t).Caravana
		want := []VersionPin{{"Nomad", "2.0.7", "2.1.0"}, {"Traefik", "3.7.13", "3.8.0"}}
		if !slices.Equal(v.Pins, want) || v.PinsText != "Updates Nomad 2.0.7 → 2.1.0 and Traefik 3.7.13 → 3.8.0." {
			t.Errorf("pins = %+v, %q", v.Pins, v.PinsText)
		}
		// Read once for the release, not on every check.
		l.svc.clock.(*fakeClock).Advance(24 * time.Hour)
		l.check(t)
		if n := l.gh.count("/install.sh"); n != 1 {
			t.Errorf("install.sh was read %d times, want 1", n)
		}
	})
	t.Run("only one changes", func(t *testing.T) {
		l := setup(t, strings.Replace(install, "2.1.0", "2.0.7", 1), job)
		if v := l.view(t).Caravana; v.PinsText != "Updates Traefik 3.7.13 → 3.8.0." || len(v.Pins) != 1 {
			t.Errorf("pins = %+v, %q", v.Pins, v.PinsText)
		}
	})
	t.Run("only the installer can be read", func(t *testing.T) {
		l := setup(t, install, "")
		if v := l.view(t).Caravana; v.PinsText != "Updates Nomad 2.0.7 → 2.1.0." {
			t.Errorf("pins = %+v, %q", v.Pins, v.PinsText)
		}
	})
	t.Run("neither can be read", func(t *testing.T) {
		l := setup(t, "", "")
		if v := l.view(t).Caravana; len(v.Pins) != 0 || v.PinsText != "" || v.Latest != "0.16.0" {
			t.Errorf("pins = %+v, %q", v.Pins, v.PinsText)
		}
	})
	t.Run("not a version line", func(t *testing.T) {
		l := setup(t, "NOMAD_VERSION=$(curl evil)\n", "image = \"traefik:latest\"\n")
		if v := l.view(t).Caravana; len(v.Pins) != 0 {
			t.Errorf("pins = %+v", v.Pins)
		}
	})
	t.Run("a release that is not newer isn't read", func(t *testing.T) {
		l := newUpdatesLab(t)
		l.gh.set(func(g *fakeGitHub) { g.release, g.install, g.job = releaseBody("v0.15.0", ""), install, job })
		l.check(t)
		if n := l.gh.count("/install.sh") + l.gh.count("/traefik.nomad.hcl"); n != 0 {
			t.Errorf("%d pin files were read", n)
		}
	})
	t.Run("the node's versions aren't known yet", func(t *testing.T) {
		l := newUpdatesLab(t)
		l.gh.set(func(g *fakeGitHub) { g.install, g.job = install, job })
		l.check(t)
		if v := l.view(t).Caravana; len(v.Pins) != 0 || v.Latest != "0.16.0" {
			t.Errorf("pins = %+v", v.Pins)
		}
	})
}

// Check now: refused within 10 minutes of the last check (a timed one counts),
// with the time to wait; allowed after.
func TestCheckReleaseNow(t *testing.T) {
	ctx := context.Background()
	l := newUpdatesLab(t)

	if err := l.svc.CheckRelease(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the check to end", func() bool { return !l.view(t).Checking.Release && l.releaseRow(t).LatestTag != "" })
	if n := l.gh.count("/releases/latest"); n != 1 {
		t.Fatalf("requests = %d, want 1", n)
	}

	l.svc.clock.(*fakeClock).Advance(9*time.Minute + 59*time.Second)
	err := l.svc.CheckRelease(ctx)
	var soon *CheckTooSoonError
	if !errors.Is(err, ErrCheckTooSoon) || !errors.As(err, &soon) {
		t.Fatalf("CheckRelease after 9m59s = %v, want ErrCheckTooSoon", err)
	}
	if want := time.Date(2026, 11, 2, 12, 10, 0, 0, time.UTC); !soon.Again.Equal(want) {
		t.Errorf("may run again at %v, want %v", soon.Again, want)
	}
	if !strings.Contains(err.Error(), "12:10 UTC") {
		t.Errorf("the sentence = %q", err)
	}
	if n := l.gh.count("/releases/latest"); n != 1 {
		t.Errorf("a refused check made a request: %d", n)
	}

	l.svc.clock.(*fakeClock).Advance(time.Second)
	if err := l.svc.CheckRelease(ctx); err != nil {
		t.Fatalf("CheckRelease after 10 minutes = %v", err)
	}
	waitFor(t, "the second check", func() bool { return l.gh.count("/releases/latest") == 2 && !l.view(t).Checking.Release })
}

// A check that is running refuses another, and tells open pages when it starts
// and ends.
func TestCheckReleaseRunningAndNotices(t *testing.T) {
	ctx := context.Background()
	l := newUpdatesLab(t)
	notices, stop := l.svc.Subscribe("")
	defer stop()
	gate := make(chan struct{})
	l.svc.releaseClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-gate
		return http.DefaultTransport.RoundTrip(r)
	})}

	if err := l.svc.CheckRelease(ctx); err != nil {
		t.Fatal(err)
	}
	if !l.view(t).Checking.Release {
		t.Error("the view doesn't say a check is running")
	}
	if err := l.svc.CheckRelease(ctx); !errors.Is(err, ErrCheckTooSoon) {
		t.Errorf("a second check while one runs = %v, want ErrCheckTooSoon", err)
	}
	close(gate)
	waitFor(t, "the check to end", func() bool { return !l.view(t).Checking.Release && l.releaseRow(t).LatestTag != "" })
	updates := 0
	for drained := false; !drained; {
		select {
		case c := <-notices:
			if c.What == ChangeUpdates {
				updates++
			}
		default:
			drained = true
		}
	}
	if updates < 2 {
		t.Errorf("updates notices = %d, want one when it starts and one when it ends", updates)
	}
}

func slogTo(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The loop checks about every gap, and stops with its context.
func TestRunReleaseCheckLoop(t *testing.T) {
	l := newUpdatesLab(t)
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.svc.runReleaseCheck(ctx, 10*time.Millisecond); close(done) }()
	waitFor(t, "two checks", func() bool { return l.gh.count("/releases/latest") >= 2 })
	stop()
	<-done
	// Told once, however many checks.
	if n := len(l.deliveriesOf(t, l.problems)); n != 1 {
		t.Errorf("messages = %d, want 1", n)
	}
}

// --- package names -----------------------------------------------------------------------

// fakePro is a pro command that gives a fixed answer, optionally after a gate.
type fakePro struct {
	available bool
	out       []byte
	err       error
	gate      chan struct{} // when set, Run waits for it to close
	calls     atomic.Int32
}

func (p *fakePro) Available() bool { return p.available }

func (p *fakePro) Run(ctx context.Context) ([]byte, error) {
	p.calls.Add(1)
	if p.gate != nil {
		select {
		case <-p.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return p.out, p.err
}

const proAnswer = `{"packages":[{"package":"openssl","service_name":"standard-security","status":"upgrade_available"},` +
	`{"package":"linux-image-6.8.0-149-generic","service_name":"standard-security","status":"upgrade_available"},` +
	`{"package":"libfoo","service_name":"esm-infra","status":"pending_attach"}],"summary":{"num_standard_security_updates":2}}`

func ubuntuHost() fstest.MapFS {
	return fstest.MapFS{
		"etc/os-release": {Data: []byte("ID=ubuntu\nVERSION_ID=\"24.04\"\n")},
		"var/lib/update-notifier/updates-available": {Data: []byte("\n14 updates can be applied immediately.\n9 of these updates are standard security updates.\n"),
			ModTime: time.Date(2026, 11, 2, 11, 0, 0, 0, time.UTC)},
	}
}

func TestCheckPackages(t *testing.T) {
	ctx := context.Background()
	l := newUpdatesLab(t)
	l.svc.SetHostRoot(ubuntuHost())
	pro := &fakePro{available: true, out: []byte(proAnswer), gate: make(chan struct{})}
	l.svc.SetProRunner(pro)

	// Before a check there are no names, and the page may offer the check.
	if v := l.view(t).OS.Updates; len(v.Packages) != 0 || !v.PackagesAvailable || !v.PackagesCheckedAt.IsZero() {
		t.Fatalf("before = %+v", v)
	}

	if err := l.svc.CheckPackages(ctx); err != nil {
		t.Fatal(err)
	}
	if v := l.view(t); !v.Checking.Packages {
		t.Error("the view doesn't say the check is running")
	}
	// One at a time: asked again while it runs, it is too soon.
	if err := l.svc.CheckPackages(ctx); !errors.Is(err, ErrCheckTooSoon) {
		t.Errorf("a second check while one runs = %v, want ErrCheckTooSoon", err)
	}
	close(pro.gate)
	waitFor(t, "the names", func() bool { return !l.view(t).Checking.Packages })

	u := l.view(t).OS.Updates
	if !slices.Equal(u.Packages, []string{"linux-image-6.8.0-149-generic", "openssl"}) || !u.PackagesKernel || u.PackagesError != "" ||
		!u.PackagesCheckedAt.Equal(l.svc.clock.Now()) {
		t.Errorf("after = %+v", u)
	}
	if u.Total != 14 || u.Security != 9 {
		t.Errorf("the counts changed: %+v", u)
	}
	if n := pro.calls.Load(); n != 1 {
		t.Errorf("pro ran %d times", n)
	}

	// Within 10 minutes: refused, and pro doesn't run. After: allowed.
	l.svc.clock.(*fakeClock).Advance(9 * time.Minute)
	if err := l.svc.CheckPackages(ctx); !errors.Is(err, ErrCheckTooSoon) {
		t.Errorf("after 9 minutes = %v, want ErrCheckTooSoon", err)
	}
	if n := pro.calls.Load(); n != 1 {
		t.Errorf("a refused check ran pro: %d", n)
	}

	// Kept for an hour.
	l.svc.clock.(*fakeClock).Advance(50 * time.Minute) // 59 minutes
	if u := l.view(t).OS.Updates; len(u.Packages) != 2 {
		t.Errorf("at 59 minutes = %+v", u)
	}
	l.svc.clock.(*fakeClock).Advance(2 * time.Minute) // 61 minutes
	if u := l.view(t).OS.Updates; len(u.Packages) != 0 || !u.PackagesCheckedAt.IsZero() || u.PackagesKernel {
		t.Errorf("at 61 minutes = %+v", u)
	}
	if err := l.svc.CheckPackages(ctx); err != nil {
		t.Fatalf("after 10 minutes = %v", err)
	}
	waitFor(t, "the second run", func() bool { return pro.calls.Load() == 2 && !l.view(t).Checking.Packages })
}

func TestCheckPackagesRefusals(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		host fstest.MapFS
		pro  ProRunner
		want string
	}{
		{"not Debian or Ubuntu", fstest.MapFS{"etc/os-release": {Data: []byte("ID=fedora\n")}}, &fakePro{available: true}, reasonUnreadable},
		{"no os-release", fstest.MapFS{}, &fakePro{available: true}, reasonUnreadable},
		{"Debian", fstest.MapFS{"etc/os-release": {Data: []byte("ID=debian\nVERSION_ID=12\n")}}, &fakePro{available: true}, reasonNotUbuntu},
		{"no pro command", ubuntuHost(), &fakePro{available: false}, reasonNoPro},
		{"no runner", ubuntuHost(), nil, reasonNoPro},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := newUpdatesLab(t)
			l.svc.SetHostRoot(tc.host)
			l.svc.SetProRunner(tc.pro)
			err := l.svc.CheckPackages(ctx)
			var un *CheckUnavailableError
			if !errors.Is(err, ErrCheckUnavailable) || !errors.As(err, &un) || un.Reason != tc.want {
				t.Fatalf("CheckPackages = %v, want ErrCheckUnavailable: %s", err, tc.want)
			}
			if v := l.view(t); v.OS.Updates.PackagesAvailable || v.Checking.Packages {
				t.Errorf("view = %+v", v.OS.Updates)
			}
			// A refusal is not a check: it doesn't start the 10 minutes.
			if !l.svc.updates.packagesStarted.IsZero() {
				t.Error("a refused check started the clock")
			}
		})
	}
}

// Whatever pro says, and whatever goes wrong, nothing it printed reaches a log
// line, an error or the view.
func TestCheckPackagesFailures(t *testing.T) {
	ctx := context.Background()
	const sentinel = "SENTINEL-PRO-OUTPUT"
	tests := []struct {
		name string
		out  string
		err  error
		want string
	}{
		{"pro's answer isn't JSON", sentinel, nil, failPackagesBad},
		{"pro fails", sentinel, errors.New("pro exited: " + sentinel), failPackagesOther},
		{"pro times out", "", fmt.Errorf("pro security-status: %w", context.DeadlineExceeded), failPackagesSlow},
		{"pro is gone by now", "", hostos.ErrProUnavailable, reasonNoPro},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logs := &lockedBuffer{}
			l := newUpdatesLab(t)
			l.svc.log = slogTo(logs)
			l.svc.SetHostRoot(ubuntuHost())
			l.svc.SetProRunner(&fakePro{available: true, out: []byte(tc.out), err: tc.err})
			if err := l.svc.CheckPackages(ctx); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "the check to end", func() bool { return !l.view(t).Checking.Packages })
			u := l.view(t).OS.Updates
			if u.PackagesError != tc.want || len(u.Packages) != 0 || u.PackagesCheckedAt.IsZero() {
				t.Errorf("after = %+v, want the error %q", u, tc.want)
			}
			// The counts stay.
			if u.Total != 14 || u.Security != 9 {
				t.Errorf("the counts were lost: %+v", u)
			}
			if strings.Contains(logs.String(), sentinel) || strings.Contains(u.PackagesError, sentinel) {
				t.Errorf("pro's output is in the log or the view: %q", logs.String())
			}
		})
	}
}

// --- history ---------------------------------------------------------------------------

func TestUpdatesHistory(t *testing.T) {
	ctx := context.Background()
	l := newUpdatesLab(t)
	l.svc.sampler.node = &HostNode{NodeFacts: deploy.NodeFacts{Kernel: "6.8.0-149-generic"}}
	t0 := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	for i, e := range []store.HostEvent{
		{Kind: store.HostEventVersion, At: t0, Detail: `{"from":"","to":"0.12.0"}`, Key: "v1"},
		{Kind: store.HostEventVersion, At: t0.Add(24 * time.Hour), Detail: `{"from":"0.12.0","to":"0.13.0"}`, Key: "v2"},
		{Kind: store.HostEventBoot, At: t0.Add(48 * time.Hour), Detail: `{}`, Key: "b1"},
		{Kind: store.HostEventOSPackages, At: t0.Add(72 * time.Hour), Detail: `{"packages":14,"by":"unattended","reboot":true}`, Key: "o1"},
		{Kind: store.HostEventOSPackages, At: t0.Add(96 * time.Hour), Detail: `{"packages":1,"by":"hand"}`, Key: "o2"},
		{Kind: store.HostEventBoot, At: t0.Add(120 * time.Hour), Detail: `{}`, Key: "b2"},
	} {
		if ok, err := l.store.AppendHostEvent(ctx, e); err != nil || !ok {
			t.Fatalf("event %d: %v, %v", i, ok, err)
		}
	}
	var got []string
	for _, h := range l.view(t).History {
		got = append(got, h.At.Format("01-02")+" "+h.Kind+" | "+h.Text+" | "+h.Detail)
	}
	want := []string{
		"10-25 boot | The server restarted | kernel 6.8.0-149 since then",
		"10-24 os_packages | Ubuntu updates · 1 package | by hand · no reboot needed",
		"10-23 os_packages | Ubuntu updates · 14 packages | by Ubuntu's automatic updates · needed a reboot",
		"10-22 boot | The server restarted | ",
		"10-21 version | caravana 0.12.0 → 0.13.0 | ",
		// The first version recorded (from "") is where the history starts: it is not listed.
	}
	if !slices.Equal(got, want) {
		t.Errorf("history =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// At most ten.
	for i := range 12 {
		if _, err := l.store.AppendHostEvent(ctx, store.HostEvent{Kind: store.HostEventBoot, At: t0.Add(time.Duration(200+i) * time.Hour), Detail: `{}`, Key: fmt.Sprintf("x%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(l.view(t).History); n != historyShown {
		t.Errorf("history has %d entries, want %d", n, historyShown)
	}
}

func TestKernelShort(t *testing.T) {
	for in, want := range map[string]string{
		"6.8.0-149-generic": "6.8.0-149", "6.8.0-9-lowlatency": "6.8.0-9", "7.0.0-38-generic": "7.0.0-38", "6.8.0-149": "6.8.0-149",
		"weird": "weird", "": "",
	} {
		if got := kernelShort(in); got != want {
			t.Errorf("kernelShort(%q) = %q, want %q", in, got, want)
		}
	}
}
