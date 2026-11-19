package main

// console-security (feature 044, tasks.md T012): the image scanner through the console's own
// pages and forms, over HTTP the way a browser does it. Install, the settings form, an app's
// Security panel and its history counts, Scan now, and the removal through its confirmation page.
// It installs the add-on (about 2 minutes: the database is 204 MB) and removes it again.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

const consoleSecurityApp = "con044"

func (c *console) consoleSecurity(t *testing.T) {
	const id = consoleSecurityApp
	c.ensureSignedIn(t)
	removeAddon := func() {
		waitUntil(10*time.Minute, func() bool {
			a := c.g.scanAddonQuiet()
			switch a.State {
			case "available":
				return true
			case "installed", "failed":
				if r, err := c.g.apiRequest(http.MethodDelete, "/v1/addons/image-scanner", "", ""); err == nil {
					r.Body.Close()
				}
			}
			return false
		})
	}
	clean := func() {
		if resp, err := c.g.apiRequest(http.MethodDelete, "/v1/apps/"+id, "", ""); err == nil {
			resp.Body.Close()
		}
		removeAddon()
	}
	clean() // leftovers of an aborted run
	c.appGone(t, id)
	t.Cleanup(clean)

	pages := map[string]string{}
	get := func(path string) string {
		t.Helper()
		resp, body := c.get(t, path)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d:\n%s", path, resp.StatusCode, pageText(body))
		}
		pages[path] = body
		return body
	}
	post303 := func(path string, form url.Values, wantPrefix string) {
		t.Helper()
		resp, body := c.post(t, path, form)
		loc := resp.Header.Get("Location")
		if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, wantPrefix) {
			t.Fatalf("POST %s = %d → %q, want 303 to %s…:\n%s", path, resp.StatusCode, loc, wantPrefix, pageText(body))
		}
	}

	// 1. Not installed: the page says what it costs and offers the install; the app's panel says so too.
	page := get("/ops/security")
	text := pageText(page)
	if !strings.Contains(text, "Install the image scanner") || !strings.Contains(text, "3.5 GB") ||
		!strings.Contains(page, `action="/ops/security/install"`) {
		t.Errorf("Operator › Security before the install lacks the cost or the button:\n%s", text)
	}
	if strings.Contains(page, `action="/ops/security/settings"`) {
		t.Error("the settings form is offered before the scanner is installed")
	}

	// An app to scan, deployed before the scanner exists: the install queues it.
	web := scanWeb(scanOldImage)
	raw := c.g.sendWant(t, http.StatusAccepted, http.MethodPost, "/v1/apps/"+id+"/deployments", labApp(web))
	var first gateDeployment
	if err := json.Unmarshal(raw, &first); err != nil || first.ID == "" {
		t.Fatalf("deploy %s: %s", id, raw)
	}
	if final := c.g.waitFinalOf(t, id, first.ID, 5*time.Minute); final.Status != "healthy" {
		t.Fatalf("deploy of %s ended %s: %s", id, final.Status, final.Error)
	}
	if text := pageText(get("/apps/" + id + "/deployments")); !strings.Contains(text, "The image scanner isn't installed") {
		t.Errorf("the app's Security panel before the install doesn't say it isn't installed:\n%s", text)
	}

	// 2. Install through the form; the page shows it going, then installed.
	post303("/ops/security/install", url.Values{}, "/ops/security")
	if text := pageText(get("/ops/security")); !strings.Contains(text, "Installing the image scanner") && !strings.Contains(text, "Image scanner 0.") {
		t.Errorf("the page after the install form says neither installing nor installed:\n%s", text)
	}
	waitFor(t, "the image scanner to be installed", 10*time.Minute, func() bool {
		a := c.g.scanAddonQuiet()
		if a.State == "failed" {
			t.Fatalf("the install failed: %s", a.Reason)
		}
		return a.State == "installed"
	})
	page = get("/ops/security")
	text = pageText(page)
	for _, want := range []string{"Image scanner 0.117.0", "Scan all now", "Remove…", "Recent runs"} {
		if !strings.Contains(text, want) {
			t.Errorf("Operator › Security, installed, lacks %q:\n%s", want, text)
		}
	}

	// 3. The settings form saves, and a bad address is refused at its input.
	form := formValuesAction(t, page, "/ops/security/settings")
	form.Set("notifyLevel", "high")
	form.Set("dailyHour", "5")
	form.Set("refuseFixableCritical", "1")
	post303("/ops/security/settings", form, "/ops/security?saved=1")
	saved := get("/ops/security?saved=1")
	if !strings.Contains(pageText(saved), "Saved.") ||
		!regexp.MustCompile(`<option value="high" selected`).MatchString(saved) ||
		!regexp.MustCompile(`<option value="5" selected`).MatchString(saved) ||
		!regexp.MustCompile(`name="refuseFixableCritical"[^>]*checked`).MatchString(saved) {
		t.Errorf("the saved settings aren't what was posted:\n%s", pageText(saved))
	}
	bad := formValuesAction(t, saved, "/ops/security/settings")
	bad.Set("notifyLevel", "high") // the helper doesn't read selects
	bad.Set("dailyHour", "5")
	bad.Set("databaseUrl", "http://mirror.example.com/latest.json")
	resp, body := c.post(t, "/ops/security/settings", bad)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `id="sec-url-error"`) ||
		!strings.Contains(pageText(body), "https://") {
		t.Errorf("a plain http database address = %d, want 422 with the error at its input:\n%s", resp.StatusCode, pageText(body))
	}
	// Put the settings back: the deploy check off, the notify level critical, the daily hour 4.
	form.Set("notifyLevel", "critical")
	form.Set("dailyHour", "4")
	form.Del("refuseFixableCritical")
	post303("/ops/security/settings", form, "/ops/security?saved=1")

	// 4. The app's panel and history entry, once its scan is done.
	waitFor(t, "the app's scan to finish", 10*time.Minute, func() bool {
		v := c.g.appScan(t, id)
		return len(v.Services) == 1 && v.Services[0].Scan != nil && v.Services[0].Scan.Status == "ok" && v.Services[0].Scan.ScannedAt != nil
	})
	if text := pageText(get("/ops/security")); !strings.Contains(text, "database of") {
		t.Errorf("Operator › Security doesn't give the database's date after the first scan:\n%s", text)
	}
	page = get("/apps/" + id + "/deployments")
	text = pageText(page)
	if !strings.Contains(text, "critical") || !strings.Contains(text, "zlib") || !strings.Contains(text, scanAdvisory) ||
		!strings.Contains(text, "Fixable") || !regexp.MustCompile(`All \d+ findings`).MatchString(text) {
		t.Errorf("the app's Security panel lacks the chips, the fixable list or the table:\n%s", text)
	}
	if !regexp.MustCompile(`\d+ critical · \d+ high`).MatchString(text) && !regexp.MustCompile(`\d+ critical`).MatchString(text) {
		t.Errorf("the history entry has no counts:\n%s", text)
	}
	if strings.Contains(page, "<script") && strings.Contains(page, "CVE-") && strings.Contains(page, "javascript:") {
		t.Error("the Security panel draws a script link")
	}
	if part := get("/parts/apps/" + id + "/security"); !strings.Contains(part, scanAdvisory) {
		t.Errorf("/parts/apps/%s/security lacks %s", id, scanAdvisory)
	}
	// The images table on Operator › Security lists it with its app.
	if text := pageText(get("/ops/security")); !strings.Contains(text, id) || !strings.Contains(text, "alpine") {
		t.Errorf("Operator › Security's images table lacks %s:\n%s", id, text)
	}

	// 5. Scan now: a new owner run for the image.
	before := len(c.g.scanRuns(t))
	post303("/apps/"+id+"/security/scan", url.Values{}, "/apps/"+id+"/deployments")
	waitFor(t, "the owner's scan run to be recorded and end", 5*time.Minute, func() bool {
		for _, r := range c.g.scanRuns(t) {
			if r.Trigger == "owner" && r.Image != "" && (r.Status == "ok" || r.Status == "failed") {
				return true
			}
		}
		return false
	})
	if after := len(c.g.scanRuns(t)); after <= before {
		t.Errorf("Scan now made no run (%d runs before, %d after)", before, after)
	}
	post303("/ops/security/scan", url.Values{}, "/ops/security")

	// 6. Remove through the confirmation page.
	removePage := get("/ops/security/remove")
	if !strings.Contains(pageText(removePage), "Remove the image scanner?") || !strings.Contains(removePage, `action="/ops/security/remove"`) {
		t.Errorf("the confirmation page is wrong:\n%s", pageText(removePage))
	}
	post303("/ops/security/remove", url.Values{}, "/ops/security")
	waitFor(t, "the image scanner to be removed", 10*time.Minute, func() bool { return c.g.scanAddonQuiet().State == "available" })
	if text := pageText(get("/ops/security")); !strings.Contains(text, "Install the image scanner") {
		t.Errorf("Operator › Security after the removal doesn't offer the install:\n%s", text)
	}
	if text := pageText(get("/apps/" + id + "/deployments")); !strings.Contains(text, "The image scanner isn't installed") {
		t.Errorf("the app's panel after the removal doesn't say it isn't installed:\n%s", text)
	}
	if _, rbody := c.get(t, "/ops/security/remove"); !strings.Contains(pageText(rbody), "isn't installed") {
		t.Errorf("the confirmation page for a removed scanner doesn't say there is nothing to remove:\n%s", pageText(rbody))
	}
}
