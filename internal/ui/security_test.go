package ui

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
	"github.com/cosmic-chimps/caravana/internal/vulns"
)

// Feature 044, phase 4: Operator › Security, an app's Security panel and the history's counts. The pages are
// checked with the add-on's record and the stored scans written straight into the store, so each state is
// drawn without waiting for the scanner to run.

const (
	secImageRef  = "traefik/whoami@" + sampleDigest
	secSentinel  = "s3cret-sentinel-044-registry"
	secDBBuilt   = "2026-11-19T04:00:00Z"
	secScannedAt = "2026-11-19T10:30:00Z"
)

func secTime(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

// setAddon writes the image scanner's record.
func (e *consoleEnv) setAddon(t *testing.T, state store.AddonState, reason string) {
	t.Helper()
	a := store.Addon{Name: store.AddonImageScanner, State: state, Version: "0.117.0", Reason: reason}
	if state == store.AddonInstalled {
		a.InstalledAt = secTime(t, "2026-11-19T04:05:00Z")
	}
	if err := e.Store.SaveAddon(context.Background(), a); err != nil {
		t.Fatalf("SaveAddon: %v", err)
	}
}

// secFinding makes a finding with the fields the pages draw.
func secFindingFor(id, advisory string, sev vulns.Severity, pkg, version, fixed string) vulns.Finding {
	f := vulns.Finding{ID: id, Advisory: advisory, CVE: advisory, Package: pkg, Version: version, Type: "apk", Severity: sev,
		URL: "https://nvd.nist.gov/vuln/detail/" + advisory}
	if fixed != "" {
		f.FixState, f.FixedIn = vulns.FixStateFixed, []string{fixed}
	} else {
		f.FixState = "not-fixed"
	}
	return f
}

// scanWith stores a successful scan of the whoami image with the findings.
func (e *consoleEnv) scanWith(t *testing.T, image string, findings ...vulns.Finding) {
	t.Helper()
	res := vulns.Result{Findings: findings, DBBuilt: secTime(t, secDBBuilt), DBSchema: "6", Scanner: "0.117.0"}
	if err := e.Store.SaveScanResult(context.Background(), image, res, false, secTime(t, secScannedAt)); err != nil {
		t.Fatalf("SaveScanResult: %v", err)
	}
}

// installedWithScan is a console whose whoami app is live and scanned: one critical with a fix, one high
// that is known to be exploited and has none, one medium.
func installedWithScan(t *testing.T) *consoleEnv {
	t.Helper()
	e := newConsoleEnv(t)
	e.Clock.Set(secTime(t, "2026-11-19T12:00:00Z"))
	e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 7})
	e.copies("whoami", "web", 7, 2, 0)
	e.setAddon(t, store.AddonInstalled, "")
	kev := secFindingFor("f2", "CVE-2024-0002", vulns.High, "busybox", "1.35.0", "")
	kev.KEV = true
	kev.EPSS = &vulns.EPSS{Score: 0.5, Percentile: 0.97}
	e.scanWith(t, secImageRef,
		secFindingFor("f1", "CVE-2024-0001", vulns.Critical, "zlib", "1.2.11-r3", "1.2.12-r0"),
		kev,
		secFindingFor("f3", "CVE-2024-0003", vulns.Medium, "openssl", "3.0.1", ""))
	return e
}

func TestSecurityPageInEveryState(t *testing.T) {
	cases := []struct {
		name   string
		state  store.AddonState
		reason string
		want   []string
		not    []string
	}{
		{"not installed", "", "",
			[]string{"Checks the images your apps run", "about 3.5 GB on one server", "about 204 MB a day", "512 MB while it scans",
				"sends nothing about this server or its apps anywhere", `action="/ops/security/install"`, "Install the image scanner"},
			[]string{`action="/ops/security/settings"`, "Scan all now", "Try again"}},
		{"installing", store.AddonInstalling, "",
			[]string{"Installing the image scanner.", `hx-get="/parts/ops/security?state=installing"`, `<meta http-equiv="refresh" content="15">`},
			[]string{"Install the image scanner", `action="/ops/security/settings"`}},
		{"failed", store.AddonFailed, "the scanner's volume couldn't be created",
			[]string{"The image scanner didn't finish.", "the scanner&#39;s volume couldn&#39;t be created", "Try again", `href="/ops/security/remove"`},
			[]string{`action="/ops/security/settings"`}},
		{"removing", store.AddonRemoving, "",
			[]string{"Removing the image scanner."}, []string{"Install the image scanner", "Scan all now"}},
		{"installed", store.AddonInstalled, "",
			[]string{"Image scanner", "0.117.0", "Scan all now", `href="/ops/security/remove"`, `action="/ops/security/settings"`,
				`name="notifyLevel"`, `name="refuseFixableCritical"`, `name="dailyHour"`, `name="databaseUrl"`, "No images to scan yet.", "No scans yet."},
			[]string{"Install the image scanner", "Checks the images your apps run"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newConsoleEnv(t)
			if c.state != "" {
				e.setAddon(t, c.state, c.reason)
			}
			resp, body := e.get(t, "/ops/security")
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("GET /ops/security = %d:\n%s", resp.StatusCode, text(body))
			}
			if got := h1Text(t, body); got != "Security" {
				t.Errorf("headline = %q", got)
			}
			for _, want := range append(c.want, `<a href="/ops/security" class="cv-nav-item" aria-current="page">`, `sse-connect="/live?watch=ops"`) {
				if !strings.Contains(body, want) && !strings.Contains(text(body), want) {
					t.Errorf("the page lacks %q", want)
				}
			}
			for _, not := range c.not {
				if strings.Contains(body, not) || strings.Contains(text(body), not) {
					t.Errorf("the page has %q", not)
				}
			}
			if strings.Contains(body, "<script>") || strings.Contains(body, "style=") {
				t.Error("the page has an inline script or style")
			}
		})
	}
}

func TestSecurityPageListsImagesAndRuns(t *testing.T) {
	e := installedWithScan(t)
	ctx := context.Background()
	run := store.ScanRun{ID: "run1", Image: secImageRef, Trigger: store.ScanTriggerDaily, Source: store.ScanSourceRegistry,
		CreatedAt: secTime(t, "2026-11-19T04:10:00Z")}
	if _, _, err := e.Store.QueueScanRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.StartScanRun(ctx, "run1", store.ScanSourceRegistry, secTime(t, "2026-11-19T04:10:05Z")); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.EndScanRun(ctx, "run1", store.ScanOK, "", "0.117.0", secTime(t, secDBBuilt), secTime(t, "2026-11-19T04:10:47Z")); err != nil {
		t.Fatal(err)
	}
	resp, body := e.get(t, "/ops/security")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d", resp.StatusCode)
	}
	words := text(body)
	for _, want := range []string{
		"traefik/whoami@sha256:", `<a href="/apps/whoami/deployments#security" class="cv-inline-mono">whoami</a>`, "/web",
		"1 critical", "1 high", "1 medium", "1 fixable", "1 known exploited",
		"database of Nov 19", "Scanned", "daily", "the registry", "took 42s", "0.117.0",
	} {
		if !strings.Contains(body, want) && !strings.Contains(words, want) {
			t.Errorf("the page lacks %q:\n%s", want, words)
		}
	}
	// Every clock is a clock element, in UTC.
	if !strings.Contains(body, `class="cv-clock" datetime="2026-11-19T10:30:00Z"`) {
		t.Errorf("the scanned time isn't a clock element:\n%s", body)
	}
	// The settings are the stored ones, with their selected options.
	if !strings.Contains(body, `<option value="critical" selected>`) || !strings.Contains(body, `<option value="4" selected>04:00 UTC</option>`) {
		t.Errorf("the form doesn't show the defaults:\n%s", words)
	}
}

func TestSecurityPartAsksForAReloadWhenTheStateMoved(t *testing.T) {
	e := installedWithScan(t)
	resp, body := e.get(t, "/parts/ops/security?state=installing")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("HX-Refresh") != "true" {
		t.Errorf("a part drawn for another state = %d, HX-Refresh %q; want 200 and true", resp.StatusCode, resp.Header.Get("HX-Refresh"))
	}
	if !strings.Contains(body, `hx-swap-oob="innerHTML:#sec-lists"`) || !strings.Contains(body, "Scan all now") {
		t.Errorf("the part lacks the region or the lists:\n%s", text(body))
	}
	resp, _ = e.get(t, "/parts/ops/security?state=installed")
	if resp.Header.Get("HX-Refresh") != "" {
		t.Error("a part drawn for the current state asks for a reload")
	}
}

func TestSecurityInstallAndRemovePosts(t *testing.T) {
	t.Run("install starts and answers 303", func(t *testing.T) {
		e := newConsoleEnv(t)
		resp, body := e.postForm(t, "/ops/security/install", url.Values{})
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/ops/security" {
			t.Fatalf("POST install = %d to %q:\n%s", resp.StatusCode, location(t, resp), text(body))
		}
		if _, err := e.Store.GetAddon(context.Background(), store.AddonImageScanner); err != nil {
			t.Errorf("the add-on has no record after the install started: %v", err)
		}
	})
	t.Run("install while busy is 409 with the reason", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.setAddon(t, store.AddonInstalling, "")
		resp, body := e.postForm(t, "/ops/security/install", url.Values{})
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "The add-on is being installed or removed; wait for it to finish.") {
			t.Errorf("POST install while installing = %d:\n%s", resp.StatusCode, text(body))
		}
	})
	t.Run("the confirmation says what goes", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.setAddon(t, store.AddonInstalled, "")
		resp, body := e.get(t, "/ops/security/remove")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET remove = %d:\n%s", resp.StatusCode, text(body))
		}
		for _, want := range []string{"Remove the image scanner?", "vulnerability database", "SBOMs", "needs-you items", "Your apps are not touched",
			`action="/ops/security/remove"`, `href="/ops/security"`} {
			if !strings.Contains(text(body), want) && !strings.Contains(body, want) {
				t.Errorf("the confirmation lacks %q", want)
			}
		}
	})
	t.Run("the confirmation refuses when there is nothing to remove or the add-on is busy", func(t *testing.T) {
		e := newConsoleEnv(t)
		resp, body := e.get(t, "/ops/security/remove")
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "isn't installed, so there is nothing to remove.") ||
			strings.Contains(body, `action="/ops/security/remove"`) {
			t.Errorf("GET remove of an add-on that isn't installed = %d:\n%s", resp.StatusCode, text(body))
		}
		e.setAddon(t, store.AddonRemoving, "")
		resp, body = e.get(t, "/ops/security/remove")
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "being installed or removed") {
			t.Errorf("GET remove while removing = %d:\n%s", resp.StatusCode, text(body))
		}
		resp, body = e.postForm(t, "/ops/security/remove", url.Values{})
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "being installed or removed") {
			t.Errorf("POST remove while removing = %d:\n%s", resp.StatusCode, text(body))
		}
	})
	t.Run("remove starts and answers 303", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.setAddon(t, store.AddonInstalled, "")
		resp, body := e.postForm(t, "/ops/security/remove", url.Values{})
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/ops/security" {
			t.Fatalf("POST remove = %d to %q:\n%s", resp.StatusCode, location(t, resp), text(body))
		}
	})
}

func TestSecuritySettingsPost(t *testing.T) {
	ctx := context.Background()
	t.Run("a valid form is saved and answers 303", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.setAddon(t, store.AddonInstalled, "")
		resp, body := e.postForm(t, "/ops/security/settings", url.Values{"notifyLevel": {"high"}, "refuseFixableCritical": {"1"},
			"dailyHour": {"6"}, "databaseUrl": {"https://mirror.example.com/latest.json"}})
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/ops/security?saved=1#settings" {
			t.Fatalf("POST settings = %d to %q:\n%s", resp.StatusCode, location(t, resp), text(body))
		}
		set, err := e.Svc.ScanSettings(ctx)
		if err != nil || set.NotifyLevel != "high" || !set.RefuseFixableCritical || set.DailyHour != 6 || set.DatabaseURL != "https://mirror.example.com/latest.json" {
			t.Errorf("settings = %+v, %v", set, err)
		}
		_, page := e.get(t, "/ops/security?saved=1")
		for _, want := range []string{"Saved.", `<option value="high" selected>`, `<option value="6" selected>06:00 UTC</option>`, ` checked>`, `value="https://mirror.example.com/latest.json"`} {
			if !strings.Contains(page, want) {
				t.Errorf("the saved page lacks %q", want)
			}
		}
	})
	t.Run("a bad value is drawn at its input with what was typed", func(t *testing.T) {
		for _, c := range []struct {
			name string
			form url.Values
			id   string
			text string
		}{
			{"level", url.Values{"notifyLevel": {"medium"}, "dailyHour": {"3"}}, "sec-level-error", "Must be critical or high."},
			{"hour range", url.Values{"notifyLevel": {"high"}, "dailyHour": {"99"}}, "sec-hour-error", "Must be an hour from 0 to 23 (UTC)."},
			{"hour text", url.Values{"notifyLevel": {"high"}, "dailyHour": {"noon"}}, "sec-hour-error", secBadHour},
			{"address", url.Values{"notifyLevel": {"high"}, "dailyHour": {"3"}, "databaseUrl": {"http://mirror.example.com/x"}}, "sec-url-error", "Must be an https:// address"},
			{"address with a password", url.Values{"notifyLevel": {"high"}, "dailyHour": {"3"}, "databaseUrl": {"https://u:" + secSentinel + "@mirror.example.com/x"}}, "sec-url-error", "Must not hold a user name or password."},
		} {
			e := newConsoleEnv(t)
			e.setAddon(t, store.AddonInstalled, "")
			resp, body := e.postForm(t, "/ops/security/settings", c.form)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Errorf("%s: POST = %d, want 422", c.name, resp.StatusCode)
				continue
			}
			if !strings.Contains(body, `id="`+c.id+`"`) || !strings.Contains(body, `role="alert"`) || !strings.Contains(text(body), c.text) {
				t.Errorf("%s: the error %q isn't at %s:\n%s", c.name, c.text, c.id, text(body))
			}
			if set, _ := e.Svc.ScanSettings(ctx); set.NotifyLevel != "critical" || set.DailyHour != 4 {
				t.Errorf("%s: a refused form changed the settings: %+v", c.name, set)
			}
		}
	})
	t.Run("the gate without the scanner is 409", func(t *testing.T) {
		e := newConsoleEnv(t)
		resp, body := e.postForm(t, "/ops/security/settings", url.Values{"notifyLevel": {"critical"}, "dailyHour": {"3"}, "refuseFixableCritical": {"1"}})
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "The image scanner isn't installed: install it in Operator › Security first.") {
			t.Errorf("POST the gate while not installed = %d:\n%s", resp.StatusCode, text(body))
		}
		if set, _ := e.Svc.ScanSettings(ctx); set.RefuseFixableCritical {
			t.Error("the gate was turned on without the scanner")
		}
	})
}

func TestSecurityScanAllNow(t *testing.T) {
	e := installedWithScan(t)
	resp, body := e.postForm(t, "/ops/security/scan", url.Values{})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/ops/security" {
		t.Fatalf("POST scan = %d to %q:\n%s", resp.StatusCode, location(t, resp), text(body))
	}
	runs, err := e.Svc.ScanRuns(context.Background(), 10)
	if err != nil || len(runs) != 1 || runs[0].Image != secImageRef || runs[0].Trigger != store.ScanTriggerOwner {
		t.Errorf("runs = %+v, %v; want one owner scan of the whoami image", runs, err)
	}

	empty := newConsoleEnv(t)
	resp, body = empty.postForm(t, "/ops/security/scan", url.Values{})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "isn't installed") {
		t.Errorf("POST scan while not installed = %d:\n%s", resp.StatusCode, text(body))
	}
}

func TestSecurityPanelOnTheDeploymentsTab(t *testing.T) {
	t.Run("not installed", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 7})
		resp, body := e.get(t, "/apps/whoami/deployments")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET = %d", resp.StatusCode)
		}
		for _, want := range []string{"The image scanner isn't installed.", `<a href="/ops/security">Open Security</a>`,
			`hx-get="/parts/apps/whoami/security" hx-trigger="sse:scans, sse:sync"`} {
			if !strings.Contains(body, want) {
				t.Errorf("the panel lacks %q", want)
			}
		}
		if strings.Contains(body, "Scan now") || strings.Contains(body, "cv-deploy-vulns") {
			t.Error("the panel offers a scan, or a history line, without the scanner")
		}
	})
	t.Run("scanned", func(t *testing.T) {
		e := installedWithScan(t)
		resp, body := e.get(t, "/apps/whoami/deployments")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET = %d", resp.StatusCode)
		}
		panel := regionBetween(t, body, `id="security"`, `id="history-title"`)
		words := text(panel)
		for _, want := range []string{
			"web", "traefik/whoami@sha256:", "1 critical", "1 high", "1 medium", "1 fixable", "1 known exploited",
			"scanned", "database of Nov 19",
			"Fixable", "CVE-2024-0001", "zlib 1.2.11-r3 → 1.2.12-r0", "Upgrade zlib to 1.2.12-r0 or later and rebuild the image.",
			"All 3 findings", "busybox", "none yet", "97th percentile",
		} {
			if !strings.Contains(words, want) {
				t.Errorf("the panel lacks %q:\n%s", want, words)
			}
		}
		for _, want := range []string{`<form method="post" action="/apps/whoami/security/scan"`, `class="cv-btn cv-btn-secondary cv-btn-sm">Scan now</button>`,
			`<a href="https://nvd.nist.gov/vuln/detail/CVE-2024-0001" rel="noopener noreferrer" target="_blank">CVE-2024-0001</a>`,
			`<details class="cv-secall">`} {
			if !strings.Contains(panel, want) {
				t.Errorf("the panel lacks %q", want)
			}
		}
		if strings.Contains(strings.SplitN(panel, "<details", 2)[0], "busybox") {
			t.Error("a finding without a fix is in the fixable list")
		}
		// The history says it in one line.
		if !strings.Contains(text(body), "1 critical · 1 high") {
			t.Errorf("the history entry lacks its counts:\n%s", text(body))
		}
	})
	t.Run("other states of the scan", func(t *testing.T) {
		e := installedWithScan(t)
		ctx := context.Background()
		if err := e.Store.RecordScanTry(ctx, secImageRef, store.ScanFailed, "the registry refused the credentials", secTime(t, "2026-11-19T11:00:00Z")); err != nil {
			t.Fatal(err)
		}
		_, part := e.get(t, "/parts/apps/whoami/security")
		for _, want := range []string{"Scan failed", "the registry refused the credentials", "1 critical"} {
			if !strings.Contains(text(part), want) {
				t.Errorf("a failed try's panel lacks %q:\n%s", want, text(part))
			}
		}
		if _, _, err := e.Store.QueueScanRun(ctx, store.ScanRun{ID: "q1", Image: secImageRef, Trigger: store.ScanTriggerOwner, Source: store.ScanSourceRegistry}); err != nil {
			t.Fatal(err)
		}
		_, part = e.get(t, "/parts/apps/whoami/security")
		if !strings.Contains(text(part), "Queued to scan") {
			t.Errorf("a queued scan isn't said:\n%s", text(part))
		}
		if err := e.Store.StartScanRun(ctx, "q1", store.ScanSourceRegistry, secTime(t, "2026-11-19T11:05:00Z")); err != nil {
			t.Fatal(err)
		}
		_, part = e.get(t, "/parts/apps/whoami/security")
		if !strings.Contains(text(part), "Scanning now") {
			t.Errorf("a running scan isn't said:\n%s", text(part))
		}
	})
	t.Run("never scanned, clean, and nothing live", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 7})
		e.setAddon(t, store.AddonInstalled, "")
		_, part := e.get(t, "/parts/apps/whoami/security")
		if !strings.Contains(text(part), "Not scanned yet") || !strings.Contains(part, "Scan now") {
			t.Errorf("an app never scanned:\n%s", text(part))
		}
		e.scanWith(t, secImageRef)
		_, part = e.get(t, "/parts/apps/whoami/security")
		if !strings.Contains(text(part), "no known vulnerabilities") || strings.Contains(part, "All 0 findings") {
			t.Errorf("a clean scan:\n%s", text(part))
		}
		_, page := e.get(t, "/apps/whoami/deployments")
		if !strings.Contains(text(page), "no known vulnerabilities") {
			t.Error("the history lacks the clean line")
		}

		e.seed(t, apiApp())
		_, part = e.get(t, "/parts/apps/api/security")
		if !strings.Contains(text(part), "Nothing to scan yet.") || strings.Contains(part, "Scan now") {
			t.Errorf("an app with nothing live:\n%s", text(part))
		}
	})
	t.Run("a part of an app that isn't there is 404", func(t *testing.T) {
		e := newConsoleEnv(t)
		if resp, _ := e.get(t, "/parts/apps/nope/security"); resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET = %d, want 404", resp.StatusCode)
		}
	})
}

func TestSecurityScanNowOfAnApp(t *testing.T) {
	e := installedWithScan(t)
	resp, body := e.postForm(t, "/apps/whoami/security/scan", url.Values{})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/whoami/deployments#security" {
		t.Fatalf("POST scan = %d to %q:\n%s", resp.StatusCode, location(t, resp), text(body))
	}

	t.Run("not installed is 409 above the panel", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 7})
		resp, body := e.postForm(t, "/apps/whoami/security/scan", url.Values{})
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(regionBetween(t, body, `id="security"`, `id="history-title"`)), "The image scanner isn't installed: install it in Operator › Security first.") {
			t.Errorf("POST scan while not installed = %d:\n%s", resp.StatusCode, text(body))
		}
	})
	t.Run("an app with nothing live is 409", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, whoamiApp())
		e.setAddon(t, store.AddonInstalled, "")
		resp, body := e.postForm(t, "/apps/whoami/security/scan", url.Values{})
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "no healthy deployment") && !strings.Contains(text(body), "no live deployment") {
			t.Errorf("POST scan of an app with nothing live = %d:\n%s", resp.StatusCode, text(body))
		}
	})
	t.Run("an app that isn't there is 404", func(t *testing.T) {
		e := installedWithScan(t)
		if resp, _ := e.postForm(t, "/apps/nope/security/scan", url.Values{}); resp.StatusCode != http.StatusNotFound {
			t.Errorf("POST = %d, want 404", resp.StatusCode)
		}
	})
}

func TestVulnLine(t *testing.T) {
	ok := func(c vulns.Counts) store.ImageScan {
		return store.ImageScan{ScannedAt: time.Now(), Counts: c}
	}
	scans := map[string]store.ImageScan{
		"a@sha256:1": ok(vulns.Counts{Critical: 1, High: 9, Medium: 4}),
		"b@sha256:2": ok(vulns.Counts{Medium: 2}),
		"c@sha256:3": ok(vulns.Counts{}),
		"d@sha256:4": ok(vulns.Counts{High: 2}),
		"e@sha256:5": {Status: store.ScanFailed}, // a try that gave no result
	}
	for _, c := range []struct {
		name    string
		digests map[string]string
		want    string
	}{
		{"critical and high", map[string]string{"web": "a@sha256:1"}, "1 critical · 9 high"},
		{"high only", map[string]string{"web": "d@sha256:4"}, "2 high"},
		{"lesser ones only", map[string]string{"web": "b@sha256:2"}, "no critical or high vulnerabilities"},
		{"clean", map[string]string{"web": "c@sha256:3"}, "no known vulnerabilities"},
		{"two services add up", map[string]string{"web": "a@sha256:1", "worker": "d@sha256:4"}, "1 critical · 11 high"},
		{"an image with no result", map[string]string{"web": "e@sha256:5"}, ""},
		{"an image never scanned", map[string]string{"web": "z@sha256:9"}, ""},
		{"no scans at all", map[string]string{"web": "a@sha256:1"}, ""},
	} {
		in := scans
		if c.name == "no scans at all" {
			in = nil
		}
		if got := vulnLine(c.digests, in); got != c.want {
			t.Errorf("%s: vulnLine = %q, want %q", c.name, got, c.want)
		}
	}
}

// A registry's password is in a Nomad Variable and never on a page; the scan pages draw the parsed
// findings and Caravana's sentences only.
func TestSecurityPagesHoldNoSecret(t *testing.T) {
	e := installedWithScan(t)
	pass := secSentinel
	if _, err := e.Svc.SaveRegistry(context.Background(), "ghcr.io", "ana", &pass); err != nil {
		t.Fatalf("SaveRegistry: %v", err)
	}
	if err := e.Store.RecordScanTry(context.Background(), "ghcr.io/example/other@sha256:abc", store.ScanFailed, "the registry refused the credentials", time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/ops/security", "/ops/security/remove", "/parts/ops/security", "/apps/whoami/deployments",
		"/parts/apps/whoami/security", "/parts/apps/whoami/deployments", "/apps/whoami", "/ops"} {
		resp, body := e.get(t, path)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d", path, resp.StatusCode)
		}
		if strings.Contains(body, secSentinel) {
			t.Errorf("%s draws the registry password", path)
		}
	}
	for _, form := range []struct {
		path string
		vals url.Values
	}{
		{"/ops/security/settings", url.Values{"notifyLevel": {"high"}, "dailyHour": {"3"}, "databaseUrl": {"https://u:" + secSentinel + "@m.example.com/"}}},
		{"/ops/security/scan", nil},
		{"/apps/whoami/security/scan", nil},
	} {
		_, body := e.postForm(t, form.path, form.vals)
		if strings.Contains(body, secSentinel) {
			t.Errorf("POST %s draws the secret it was given", form.path)
		}
	}
}

// The scans notice reaches the Operator stream, so the page redraws the state and the lists.
func TestSecurityLiveKinds(t *testing.T) {
	e := newConsoleEnv(t)
	e.setAddon(t, store.AddonInstalled, "")
	ls := openStream(t, e.URL, "/live?watch=ops", e.Cookie.Value)
	defer ls.close()
	ls.expect("sync")
	if _, err := e.Svc.SetScanSettings(context.Background(), service.ScanSettings{NotifyLevel: "high", DailyHour: 5}); err != nil {
		t.Fatal(err)
	}
	for {
		switch got := ls.next(); got {
		case "scans":
			return
		case "":
			t.Fatal("the Operator stream ended without a scans notice")
		}
	}
}

// Set moves the test clock to a time (the pages' "today" for the clocks and dates they draw).
func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}
