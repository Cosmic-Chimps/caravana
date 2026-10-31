package ui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
)

// The designer's review of page 3 (feature 027, 2026-10-31): the release notes as bullets, version
// tokens that never break, the banner's action, one Check now, Past updates in the past tense and
// the console's one date format, the hint without pro and who installed what the reboot is for.

// releaseNotesBody is a real CHANGELOG section (0.15.0's), the body GitHub would serve for it.
func releaseNotesBody(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "service", "testdata", "changelog-0.15.0.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// releaseRowWith is the caravana row of the page when GitHub's latest release has these notes.
func releaseRowWith(t *testing.T, notes string) string {
	t.Helper()
	e, gh := updEnv(t, updOpts{fixture: "ubuntu-clean"})
	gh.mu.Lock()
	gh.body = notes
	gh.mu.Unlock()
	releaseFound(t, e)
	_, body := e.get(t, "/ops/updates")
	return regionBetween(t, body, `data-up="release"`, `id="how"`)
}

// The notes of a release are up to six bullets: a bullet's bold lead (500, fg-1, no `**`), then ": " and
// the first sentence of the rest (fg-2), or a bullet's first sentence, never cut; then a link to the
// release, underlined, in the same tab.
func TestOpsUpdatesReleaseNotesAreBullets(t *testing.T) {
	row := releaseRowWith(t, releaseNotesBody(t)+"\n## Other\n- `Deployment.registeredAt` is mono.\n")

	if n := strings.Count(row, `class="cv-upnote-b"`); n != 6 {
		t.Errorf("%d bullets, want 6:\n%s", n, row)
	}
	for _, want := range []string{
		// A lead with its colon, then the rest's first sentence (backticks are mono).
		`<span class="cv-upbullet" aria-hidden="true">•</span><span><span class="cv-upnotelead">API 0.28.0</span>: <span class="cv-inline-mono text-fg-1 cv-nowrap">Deployment.registeredAt</span> (the time Nomad accepted the job, `,
		`<span class="cv-upnotelead">MCP</span>: <span class="cv-inline-mono text-fg-1 cv-nowrap">deploy_image</span>, <span class="cv-inline-mono text-fg-1 cv-nowrap">promote_app</span> and <span class="cv-inline-mono text-fg-1 cv-nowrap">redeploy_version</span> are marked destructive, so agent clients ask before running them.</span>`,
		// A lead the text goes on from takes a space, not a colon.
		`<span class="cv-upnotelead">Configure of an app deployed from a Compose file</span> says why it looks different: Caravana keeps the file`,
		`<span class="cv-upnotelead">The installer validates and quotes its options.</span> <span class="cv-inline-mono text-fg-1 cv-nowrap">--email</span>`,
		// A bullet without a lead is its first sentence, never cut.
		`<span>An upgrade that rolls back restores Caravana&#39;s binary and data only.</span>`,
		// The link is underlined by the stylesheet and opens in the same tab.
		`<a href="https://github.com/Cosmic-Chimps/caravana/releases/tag/v0.16.0" class="cv-upreleaselink">Release notes on GitHub</a>`,
	} {
		if !strings.Contains(row, want) {
			t.Errorf("the row lacks %q:\n%s", want, row)
		}
	}
	if strings.Contains(row, "**") || strings.Contains(row, "`") || strings.Contains(row, "_blank") {
		t.Errorf("raw Markdown or a new tab is on the page:\n%s", text(row))
	}
	// Only the first sentence of a bullet's rest: the MCP bullet's later sentences are left out. The
	// seventh bullet and the one of another section are past the six.
	if strings.Contains(text(row), "docs/mcp.md") || strings.Contains(text(row), "An exported jobspec") || strings.Contains(text(row), "is mono.") {
		t.Errorf("more than a first sentence, or a bullet past the sixth, is on the page:\n%s", text(row))
	}
}

// Backticks become mono spans, no other Markdown is read, and everything is escaped.
func TestOpsUpdatesReleaseNotesMonoAndEscaping(t *testing.T) {
	row := releaseRowWith(t, "- `Deployment.registeredAt` is mono, <b>x</b> is escaped.\n- _Not_ [a link](https://x.test) here.\n")
	if !strings.Contains(row, `<span class="cv-inline-mono text-fg-1 cv-nowrap">Deployment.registeredAt</span> is mono, `) {
		t.Errorf("the backticks aren't mono:\n%s", row)
	}
	if !strings.Contains(row, "&lt;b&gt;x&lt;/b&gt; is escaped.") || strings.Contains(row, "<b>") || strings.Contains(row, "<a href=\"https://x.test") {
		t.Errorf("the notes are not escaped, or a link was made of Markdown:\n%s", row)
	}
	if !strings.Contains(text(row), "_Not_ [a link](https://x.test) here.") {
		t.Errorf("other Markdown isn't left as it is:\n%s", text(row))
	}
}

var byUnattended = regexp.MustCompile(`Installed by Ubuntu&#39;s automatic updates\. Rebooting stops every app`)

// The reboot row says who installed what it is for, when a run in apt's history names a package of the
// marker's list: "Installed by Ubuntu's automatic updates." (no date: the sub says when the reboot became
// needed, so the date is on the row once). Without such a run, nothing.
func TestOpsUpdatesSaysWhoInstalledIt(t *testing.T) {
	e, _ := updEnv(t, updOpts{fixture: "ubuntu-kernel-reboot", edit: automaticUpdatesOff(t)})
	_, body := e.get(t, "/ops/updates")
	if row := regionBetween(t, body, `data-up="reboot"`, `data-up="ubuntu"`); !byUnattended.MatchString(row) || strings.Contains(row, "Installed on") {
		t.Errorf("the reboot row:\n%s", text(row))
	}

	// A package that someone installed by hand.
	byHand := func(root string) {
		automaticUpdatesOff(t)(root)
		if err := os.WriteFile(filepath.Join(root, "run/reboot-required.pkgs"), []byte("htop\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e, _ = updEnv(t, updOpts{fixture: "ubuntu-kernel-reboot", edit: byHand})
	_, body = e.get(t, "/ops/updates")
	if got := text(body); !strings.Contains(got, "Installed by hand. Rebooting stops") {
		t.Errorf("by hand:\n%s", got)
	}

	// No run names it: the line is left out, and the row says only its consequence.
	none := func(root string) {
		automaticUpdatesOff(t)(root)
		if err := os.WriteFile(filepath.Join(root, "run/reboot-required.pkgs"), []byte("no-such-package\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e, _ = updEnv(t, updOpts{fixture: "ubuntu-kernel-reboot", edit: none})
	_, body = e.get(t, "/ops/updates")
	if strings.Contains(body, "Installed on") || !strings.Contains(text(body), "Rebooting stops every app for about a minute") {
		t.Errorf("a reboot no run names:\n%s", text(body))
	}
}

// Past updates speak in the past tense, the first version recorded isn't a row and dates read "Oct 22".
func TestOpsUpdatesPastIsPastTense(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	got := upPastOf(now, []service.UpdateEvent{
		{At: now.Add(-3 * 24 * time.Hour), Kind: "os_packages", Text: "Ubuntu updates · 14 packages", Detail: "by Ubuntu's automatic updates · needed a reboot"},
		{At: now.Add(-9 * 24 * time.Hour), Kind: "os_packages", Text: "Ubuntu updates · 1 package", Detail: "by hand · no reboot needed"},
	})
	if len(got) != 2 || got[0].Date.Text != "Oct 28" || got[1].Date.Text != "Oct 22" || got[0].Detail != "by Ubuntu's automatic updates · needed a reboot" {
		t.Errorf("rows = %+v", got)
	}
	// One format console-wide: Recent on the Server page writes the same.
	if a, b := shortDate(now, now.Add(-9*24*time.Hour)), shortTime(now, now.Add(-9*24*time.Hour)); a != "Oct 22" || b != "Oct 22" {
		t.Errorf("shortDate = %q, shortTime = %q, want Oct 22 for both", a, b)
	}
	if got := shortDate(now, time.Date(2025, 10, 22, 0, 0, 0, 0, time.UTC)); got != "Oct 22, 2025" {
		t.Errorf("a date of another year = %q", got)
	}
}

// The banner's action never shrinks and, with no room beside the text, moves under it: the rules are in
// the stylesheet the console serves, for the shared banner of Domains, the Server page and Updates.
func TestBannerActionNeverShrinks(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("static", "app.css"))
	if err != nil {
		t.Fatal(err)
	}
	css := strings.Join(strings.Fields(string(data)), " ")
	for _, want := range []string{
		`.cv-dmbanner>.cv-btn{`,
		`min-width:max-content`,
	} {
		if !strings.Contains(css, want) {
			t.Errorf("the stylesheet lacks %q", want)
		}
	}
	// Below 640px both kinds of action sit in the text's column, under it.
	if !regexp.MustCompile(`@media \(max-width:639\.98px\)\{\.cv-dmbanner\{[^}]*\}\.cv-dmbanner>\.cv-dmcheckform,\.cv-dmbanner>\.cv-btn\{grid-column:2`).MatchString(css) {
		t.Error("the narrow rule doesn't put the action under the text")
	}
	// Page 3's banner and page 2's draw the action as a button of the banner.
	e, _ := updEnv(t, updOpts{fixture: "ubuntu-kernel-reboot", edit: automaticUpdatesOff(t)})
	for _, path := range []string{"/ops/updates", "/ops/servers/lab"} {
		_, body := e.get(t, path)
		banner := regionBetween(t, body, `<div class="cv-panel cv-dmbanner">`, "\n</div>")
		if !strings.Contains(banner, `class="cv-btn cv-btn-secondary cv-btn-sm"`) {
			t.Errorf("%s: the banner's action isn't a banner button:\n%s", path, banner)
		}
	}
}

// Without pro there is no hint-less row and no raw backtick: checked in TestOpsUpdatesNoPro. Here: a
// stale list's dim count in the sidebar, drawn by every Operator page.
func TestStaleCountIsDimOnEveryPage(t *testing.T) {
	e, _ := updEnv(t, updOpts{fixture: "ubuntu-stale-list", listAge: 3 * 24 * time.Hour, edit: automaticUpdatesOff(t)})
	_, body := e.get(t, "/ops")
	if side := sidebarRegion.FindString(body); !strings.Contains(side, `class="cv-nav-mark-dim"`) || strings.Contains(side, "◆") {
		t.Errorf("/ops sidebar:\n%s", side)
	}
}
