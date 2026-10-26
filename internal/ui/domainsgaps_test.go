package ui

import (
	"net/url"
	"strings"
	"testing"
)

// The critique's smaller control gaps (feature 023): Cancel returns to the page, a pending
// certificate offers "Open it once", a Team-only domain says how visitors sign in, and the once panel
// speaks in lowercase.

func TestDomainAddCancelLeavesThePanel(t *testing.T) {
	e := addEnv(t)
	for path, want := range map[string]string{"/domains": `href="/domains" class="cv-btn cv-btn-secondary cv-btn-sm">Cancel`,
		"/apps/whoami/domains": `href="/apps/whoami/domains" class="cv-btn cv-btn-secondary cv-btn-sm">Cancel`} {
		if panel := addPanel(e.pageOf(t, path)); !strings.Contains(panel, want) {
			t.Errorf("%s: Cancel isn't a link to the page without a fragment:\n%s", path, panel)
		}
	}
}

func TestDomainPendingOffersOpenItOnce(t *testing.T) {
	e := statesEnv(t)
	row := dmRowOf(e.pageOf(t, "/domains"), "pending.example.com")
	if !strings.Contains(row, `<a href="https://pending.example.com/" class="cv-btn cv-btn-secondary cv-btn-sm" target="_blank" rel="noopener">Open it once</a>`) {
		t.Errorf("the pending row has no Open it once:\n%s", row)
	}
	// Only the pending state offers it.
	for _, h := range []string{"live", "self", "none", "unknown"} {
		if strings.Contains(dmRowOf(e.pageOf(t, "/domains"), h+".example.com"), "Open it once") {
			t.Errorf("%s offers Open it once", h)
		}
	}
}

func TestDomainTeamOnlySaysHowVisitorsSignIn(t *testing.T) {
	e := optionsEnv(t)
	const host = "app.example.com"
	if row := text(dmRowOf(e.pageOf(t, "/domains"), host)); strings.Contains(row, "Visitors sign in as") {
		t.Errorf("a plain domain says how visitors sign in:\n%s", row)
	}
	resp, body := e.postForm(t, "/domains/"+host+"/options", optionsOf(e, t, "shop", "web", url.Values{"teamOnly": {"on"}}))
	if resp.StatusCode != 200 || !strings.Contains(text(body), "caravana only keeps a hash and can't show it again.") {
		t.Fatalf("Team only = %d:\n%s", resp.StatusCode, text(body))
	}
	row := text(dmRowOf(e.pageOf(t, "/domains"), host))
	if !strings.Contains(row, "Visitors sign in as team.") {
		t.Errorf("a Team-only domain doesn't say how visitors sign in:\n%s", row)
	}
	if strings.Contains(text(body), "Caravana only") {
		t.Error("the once panel says Caravana")
	}
}
