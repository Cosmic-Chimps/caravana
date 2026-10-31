package main

// The console-ops-updates step of the lab test (feature 027 page 3, quickstart §4): the Operator
// Updates page against real Nomad, with an Ubuntu host's files written into the host root and a
// stand-in for the releases API. It is in its own file because console_ops_test.go is long;
// runConsole lists it after console-ops-server.

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

// labUpdates is the part of GET /v1/updates the steps read.
type labUpdates struct {
	Caravana struct {
		Current      string `json:"current"`
		Latest       string `json:"latest"`
		CheckEnabled bool   `json:"checkEnabled"`
		Install      string `json:"install"`
		// CheckedAt is the last attempt, CheckedOKAt the last one that worked (feature 027's review).
		CheckedAt   *string `json:"checkedAt"`
		CheckedOKAt *string `json:"checkedOkAt"`
	} `json:"caravana"`
	OS struct {
		Readable bool `json:"readable"`
		Distro   struct {
			ID string `json:"id"`
		} `json:"distro"`
		Updates struct {
			Total         int      `json:"total"`
			Security      int      `json:"security"`
			Packages      []string `json:"packages"`
			PackagesError string   `json:"packagesError"`
		} `json:"updates"`
		Reboot struct {
			Required        bool   `json:"required"`
			RunningKernel   string `json:"runningKernel"`
			InstalledKernel string `json:"installedKernel"`
			InstalledBy     string `json:"installedBy"`
		} `json:"reboot"`
	} `json:"os"`
}

// updates reads GET /v1/updates.
func (g *gate) updates(t *testing.T) labUpdates {
	t.Helper()
	var u labUpdates
	g.getJSON(t, "/v1/updates", &u)
	return u
}

// attentionKinds are the kinds of the items on GET /v1/attention.
func (g *gate) attentionKinds(t *testing.T) []string {
	t.Helper()
	var list struct {
		Items []struct {
			Kind string `json:"kind"`
		} `json:"items"`
	}
	g.getJSON(t, "/v1/attention", &list)
	var out []string
	for _, it := range list.Items {
		out = append(out, it.Kind)
	}
	return out
}

func hasKind(kinds []string, kind string) bool {
	for _, k := range kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// kernelNumbersRe is the numbers a kernel release starts with ("6.8.0-149" of "6.8.0-149-generic").
var kernelNumbersRe = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)*-[0-9]+`)

func kernelNumberOf(release string) string {
	if m := kernelNumbersRe.FindString(release); m != "" {
		return m
	}
	return release
}

// pageOrder says each marker is on the page and that they come in the order given.
func pageOrder(t *testing.T, what, page string, markers ...string) {
	t.Helper()
	last := -1
	for _, m := range markers {
		i := strings.Index(page, m)
		if i < 0 {
			t.Errorf("%s: the page lacks %q", what, m)
			return
		}
		if i < last {
			t.Errorf("%s: %q is out of order (markers %q)", what, m, markers)
		}
		last = i
	}
}

// consoleOpsUpdates: the Operator view's third page.
//
//  1. An Ubuntu host's files go into the host root: a kernel installed that the running one is behind
//     (the reboot marker names it), 14 updates waiting and 9 of them security, Ubuntu's automatic
//     updates off. The release stand-in announces 0.16.0 (the lab's Caravana runs 0.15.0 and asks it
//     every 30 s). Both are waited for through the API.
//  2. /ops/updates: the banners (the reboot before the updates, each the item's own sentence and a
//     link to How to apply), Available (Reboot, Ubuntu updates, caravana, in that order, with both
//     kernels, the counts and the release's notes), How to apply (the OS update, the installer of
//     0.16.0, the reboot last), the sidebar's ■ on this page and on the Overview, and the part
//     that draws the regions by id. The needs-you list says the two items with "Open updates".
//  3. Check package names: 303, then the names of the recorded pro answer; a second check inside 10
//     minutes is 429 with the service's sentence, as is Check now (the lab asks every 30 s).
//  4. The release check turned off through the form: the page says off, a check is 409, and the
//     stand-in sees no request over more than two gaps; turned on again it asks again.
//  5. The reboot marker removed: the ■ banner and the item go and the sidebar's mark is ◆. Every
//     file removed: the OS rows say the host can't be read.
//
// Budget: the sampler's tick and the release gap to see the files and the release (a minute), the
// off period (75 s) and the same again to see it ask again.
func (c *console) consoleOpsUpdates(t *testing.T) {
	root, rel := c.g.target.HostRoot, c.g.target.Release
	if root == "" || rel == nil {
		t.Skip("this target has no host root and release stand-in (only the lab has); the OS and release checks are skipped")
	}
	t.Cleanup(func() { logTail(t, c.g.target) })
	t.Cleanup(func() { rel.Set("v" + labCurrentVersion) })

	// 1. The host's files and the announced release.
	fx := newHostFixture(t, root)
	fx.ubuntu()
	fx.rebootNeeded()
	rel.Set("v0.16.0")
	var up labUpdates
	waitFor(t, "the sampler to read the host's files and the release check to see 0.16.0", 3*time.Minute, func() bool {
		up = c.g.updates(t)
		return up.OS.Reboot.Required && up.OS.Reboot.InstalledKernel == labInstalledKernel && up.OS.Reboot.RunningKernel != "" &&
			up.Caravana.Latest == "0.16.0"
	})
	if !up.OS.Readable || up.OS.Distro.ID != "ubuntu" || up.OS.Updates.Total != 14 || up.OS.Updates.Security != 9 || up.Caravana.Current != labCurrentVersion {
		t.Fatalf("GET /v1/updates = %+v, want Ubuntu with 14 updates (9 security) on caravana %s", up, labCurrentVersion)
	}
	// The check worked, so the last good check is the last attempt. The host has no apt history, so no
	// run is named as the one that installed the kernel.
	if up.Caravana.CheckedAt == nil || up.Caravana.CheckedOKAt == nil || *up.Caravana.CheckedAt != *up.Caravana.CheckedOKAt || up.OS.Reboot.InstalledBy != "" {
		t.Errorf("checkedAt = %v, checkedOkAt = %v, installedBy = %q; want the same time twice and no installer", up.Caravana.CheckedAt, up.Caravana.CheckedOKAt, up.OS.Reboot.InstalledBy)
	}
	running := kernelNumberOf(up.OS.Reboot.RunningKernel)
	waitFor(t, "both OS items on the needs-you list", 2*time.Minute, func() bool {
		kinds := c.g.attentionKinds(t)
		return hasKind(kinds, "os_reboot_required") && hasKind(kinds, "os_security_updates")
	})

	// 2. The page.
	resp, page := c.get(t, "/ops/updates")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /ops/updates = %d:\n%.600s", resp.StatusCode, pageText(page))
	}
	words := pageText(page)
	rebootWords := "This server needs a reboot: kernel 9.9.0-999 is installed, but " + running + " is still running."
	updatesWords := "14 updates are waiting, 9 of them security updates."
	for _, want := range []string{
		rebootWords, "Until it reboots, the server keeps running the old kernel.", updatesWords,
		"3 updates", "kernel " + running + " → 9.9.0-999", "14 packages · 9 security", "0.15.0 → 0.16.0",
		"Operator view.", "Updates page.", "Updates",
		"Install the waiting updates: sudo apt update && sudo apt upgrade",
		// the version is its own mono span, which pageText turns into "0.16.0 ."
		"Run the installer of 0.16.0", "releases/download/v0.16.0/install.sh | sudo bash",
		"Reboot the server: sudo reboot", "Every app stops for about a minute.",
		"caravana doesn't apply updates from the console yet.",
		"Ubuntu's automatic updates", "Off", "Automatic reboot", "Off: reboots wait for you.",
		"Check GitHub for new releases once a day", "Check package names", "Past updates", "times in UTC",
	} {
		if !strings.Contains(words, want) {
			t.Errorf("/ops/updates lacks %q:\n%.1500s", want, words)
		}
	}
	// The review's fixes, on the real page: the notes are bullets with a link to the release, the one Check
	// now is the one by the setting, a kernel is a token that never breaks, and no line says who installed
	// the kernel when no apt run names it.
	for _, want := range []string{
		`class="cv-upnote-b"`, `>Release notes</a>`, `kernel <span class="cv-uptok">` + running + ` → 9.9.0-999</span>`,
		`<span class="cv-inline-mono text-fg-1 cv-nowrap">9.9.0-999</span>`,
		"Only the release and its files are fetched; nothing is sent about this server.",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("/ops/updates lacks %q", want)
		}
	}
	if n := strings.Count(page, ">Check now</button>"); n != 1 || strings.Contains(page, "Installed on ") {
		t.Errorf("/ops/updates has %d Check now buttons and %v an \"Installed on\" line; want one button and no line", n, strings.Contains(page, "Installed on "))
	}
	// The banners' names are mono spans now (one source for the needs-you wording), so their order is read
	// in the page's text, not its markup.
	pageOrder(t, "the banners", words, rebootWords, updatesWords, "Available 3 updates")
	pageOrder(t, "Available's rows", page, `data-up="reboot"`, `data-up="ubuntu"`, `data-up="release"`, `id="apply"`)
	pageOrder(t, "How to apply's steps", page, `id="up-cmd-os"`, `id="up-cmd-caravana"`, `id="up-cmd-reboot"`, `id="how"`)
	for _, mark := range []string{"/ops/updates", "/ops"} {
		_, p := c.get(t, mark)
		if side := p[strings.Index(p, `<aside class="cv-side">`):]; !strings.Contains(side[:strings.Index(side, "</aside>")], `<span aria-hidden="true">■</span>`) {
			t.Errorf("the sidebar of %s has no ■ for the reboot", mark)
		}
	}
	if strings.Contains(strings.ToLower(words), "cve") {
		t.Error("the page names a CVE")
	}
	if strings.Contains(page, `text-fg-4`) || regexp.MustCompile(`\sstyle\s*=`).MatchString(page) {
		t.Error("the page has text in fg-4 or a style attribute")
	}
	resp, part := c.get(t, "/parts/ops/updates")
	if resp.StatusCode != http.StatusOK || !strings.Contains(part, `hx-swap-oob="innerHTML:#nav-updates-mark"`) ||
		!strings.Contains(part, `id="apply"`) || strings.Contains(part, "<html") {
		t.Errorf("the part = %d, want the regions and, by id, the mark:\n%.400s", resp.StatusCode, pageText(part))
	}
	// The needs-you list says both items in the service's words, with the action that opens this page.
	_, home := c.get(t, "/ops")
	for _, want := range []string{rebootWords, updatesWords, "Open updates"} {
		if !strings.Contains(pageText(home), want) {
			t.Errorf("the Overview's needs-you list lacks %q", want)
		}
	}

	// 3. Check package names.
	resp, body := c.post(t, "/ops/updates/check", url.Values{"what": {"packages"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ops/updates#available" {
		t.Fatalf("POST check packages = %d to %q, want 303 to /ops/updates#available:\n%.400s", resp.StatusCode, resp.Header.Get("Location"), pageText(body))
	}
	waitFor(t, "the package names on the page", time.Minute, func() bool {
		_, p := c.get(t, "/ops/updates")
		return strings.Contains(p, `class="cv-upnamelist"`) && strings.Contains(p, "linux-image-6.8.0-149-generic")
	})
	if u := c.g.updates(t); len(u.OS.Updates.Packages) == 0 || u.OS.Updates.PackagesError != "" {
		t.Errorf("GET /v1/updates after the check: packages %v, error %q", u.OS.Updates.Packages, u.OS.Updates.PackagesError)
	}
	for what, sentence := range map[string]string{"packages": "That check ran less than 10 minutes ago.", "release": "That check ran less than 10 minutes ago."} {
		resp, body = c.post(t, "/ops/updates/check", url.Values{"what": {what}})
		if resp.StatusCode != http.StatusTooManyRequests || !strings.Contains(pageText(body), sentence) {
			t.Errorf("a second %s check = %d, want 429 with %q:\n%.400s", what, resp.StatusCode, sentence, pageText(body))
		}
	}

	// 4. The release check turned off and on again.
	resp, _ = c.post(t, "/ops/updates/settings", url.Values{})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ops/updates?saved=1#how" {
		t.Fatalf("POST settings (off) = %d to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if u := c.g.updates(t); u.Caravana.CheckEnabled {
		t.Fatal("GET /v1/updates still says the release check is on")
	}
	_, off := c.get(t, "/ops/updates?saved=1")
	if !strings.Contains(pageText(off), "Saved.") || strings.Contains(off, `name="checkEnabled" value="1" checked`) || strings.Contains(off, ">Check now</button>") {
		t.Errorf("the page with the release check off:\n%.800s", pageText(off))
	}
	resp, body = c.post(t, "/ops/updates/check", url.Values{"what": {"release"}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(pageText(body), "The release check is turned off.") {
		t.Errorf("a release check while it is off = %d:\n%.400s", resp.StatusCode, pageText(body))
	}
	time.Sleep(3 * time.Second) // a check that started before the setting was saved may still be on its way
	before := rel.Requests()
	time.Sleep(2*labReleaseGap + 15*time.Second)
	if after := rel.Requests(); after != before {
		t.Errorf("the release check was off and the stand-in saw %d more requests", after-before)
	}
	if resp, _ = c.post(t, "/ops/updates/settings", url.Values{"checkEnabled": {"1"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST settings (on) = %d", resp.StatusCode)
	}
	waitFor(t, "the release check to ask again", 2*labReleaseGap+30*time.Second, func() bool { return rel.Requests() > before })

	// 5. The reboot marker goes, then everything.
	fx.rebootCleared()
	waitFor(t, "the reboot banner and item to go", 2*time.Minute, func() bool {
		_, p := c.get(t, "/ops/updates")
		return !c.g.hasAttention(t, "os_reboot_required") && !strings.Contains(pageText(p), rebootWords) && !strings.Contains(p, `data-up="reboot"`) &&
			strings.Contains(pageText(p), updatesWords)
	})
	_, p := c.get(t, "/ops/updates")
	if side := p[strings.Index(p, `<aside class="cv-side">`):]; !strings.Contains(side[:strings.Index(side, "</aside>")], `<span aria-hidden="true">◆</span>`) {
		t.Error("the sidebar's mark isn't ◆ with only security updates waiting")
	}
	if strings.Contains(p, `id="up-cmd-reboot"`) {
		t.Error("How to apply still has the reboot step")
	}
	fx.removeAll()
	waitFor(t, "the OS rows to say the host can't be read", 2*time.Minute, func() bool {
		_, p := c.get(t, "/ops/updates")
		return strings.Contains(pageText(p), "caravana can't read this server's pending updates (not Ubuntu or Debian).")
	})
	_, p = c.get(t, "/ops/updates")
	if strings.Contains(p, `data-up="ubuntu"`) || strings.Contains(p, "Check package names") || !strings.Contains(p, `data-up="release"`) {
		t.Errorf("an unreadable host's page should have the release row and no OS rows:\n%.800s", pageText(p))
	}
}

// hasAttention says the needs-you list has an item of the kind.
func (g *gate) hasAttention(t *testing.T, kind string) bool {
	t.Helper()
	return hasKind(g.attentionKinds(t), kind)
}
