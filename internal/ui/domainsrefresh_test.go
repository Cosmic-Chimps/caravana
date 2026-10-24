package ui

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/edge"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// knownOf is the hosts key a page was drawn with, which its refresh carries back.
func knownOf(t *testing.T, body string) string {
	t.Helper()
	m := regexp.MustCompile(`<input type="hidden" id="domains-known" name="known" value="([0-9a-f]+)">`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("the page has no domains-known input:\n%s", text(body))
	}
	return m[1]
}

// The live region swaps nothing of its own: its refresh answers out-of-band fragments by id (each
// row's cells and left column, and the summary above the table), so the Options form, Remove…, New
// password and the Add panel are never replaced while the owner types in them. When a host was
// added or removed since the page was drawn the answer is the whole region, which htmx swaps in
// over the region's own "none" (HX-Reswap).
func TestDomainsRefreshSwapsPerRow(t *testing.T) {
	e := statesEnv(t)
	hosts := []string{"live", "elsewhere", "expiring", "pending", "none", "self", "unknown", "idle", "paused"}
	for _, path := range []string{"/domains", "/apps/shop/domains"} {
		page := e.pageOf(t, path)
		section := regexp.MustCompile(`<section class="cv-sregion"[^>]*>`).FindString(page)
		for _, want := range []string{`hx-trigger="sse:domains, sse:sync"`, `hx-swap="none"`, `hx-include="#domains-known"`, `hx-get="/parts/domains`} {
			if !strings.Contains(section, want) {
				t.Errorf("%s: the region is %s, lacks %s", path, section, want)
			}
		}
		// Every <details> has a stable id, for console.js to keep it open or closed.
		for _, d := range regexp.MustCompile(`<details\b[^>]*>`).FindAllString(page, -1) {
			if !strings.Contains(d, ` id="`) && !strings.Contains(d, "cv-menu") { // the shell's own menu is not the page's
				t.Errorf("%s: %s has no id", path, d)
			}
		}
		if path == "/domains" {
			for _, h := range hosts {
				for _, id := range []string{"domain-status-", "domain-left-"} {
					if !strings.Contains(page, `id="`+id+h+`.example.com"`) {
						t.Errorf("%s: no element %s%s.example.com", path, id, h)
					}
				}
			}
		}
	}

	page := e.pageOf(t, "/domains")
	known := knownOf(t, page)
	resp, body := e.get(t, "/parts/domains?known="+known)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("HX-Reswap") != "" {
		t.Fatalf("the refresh = %d, HX-Reswap %q, want 200 and none", resp.StatusCode, resp.Header.Get("HX-Reswap"))
	}
	if got, want := strings.Count(body, `hx-swap-oob="true"`), 1+2*len(hosts); got != want {
		t.Errorf("the refresh has %d out-of-band fragments, want %d (the summary, then each row's cells and left column):\n%s", got, want, body)
	}
	for _, id := range []string{"domains-summary", "domain-status-live.example.com", "domain-left-elsewhere.example.com"} {
		if !strings.Contains(body, `id="`+id+`"`) {
			t.Errorf("the refresh lacks %s", id)
		}
	}
	// Nothing the owner types into, and nothing that opens, is in it.
	for _, never := range []string{"cv-dmform", `action="/domains/live.example.com/options"`, "<details", `name="path"`, `id="add"`, "Save and deploy", "Remove…", "domain-once"} {
		if strings.Contains(body, never) {
			t.Errorf("the refresh holds %q", never)
		}
	}

	// A state that changed travels in those fragments, with no whole-region swap.
	here := []string{"203.0.113.1"}
	e.seedCheck(t, checkSeed{host: "pending.example.com", app: "shop", dns: edge.DNSOK, seen: here, expected: here,
		cert: edge.CertIssued, issuer: "Let's Encrypt", notAfterFromNow: 80 * day})
	resp, body = e.get(t, "/parts/domains?known="+known)
	cells := regexp.MustCompile(`(?s)<span id="domain-status-pending\.example\.com".*?</span></span></span>`).FindString(body)
	if resp.Header.Get("HX-Reswap") != "" || !strings.Contains(text(cells), "live · HTTPS") {
		t.Errorf("the pending row's cells after it was issued (HX-Reswap %q): %s", resp.Header.Get("HX-Reswap"), text(cells))
	}

	// A host added since the page was drawn: the whole region.
	e.seed(t, domainsApp("extra", "extra.example.com"), seedDeploy{ID: "x1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	resp, body = e.get(t, "/parts/domains?known="+known)
	if resp.Header.Get("HX-Reswap") != "innerHTML" || strings.Contains(body, "hx-swap-oob") || !strings.Contains(body, `<details class="cv-dmdetails" id="domain-extra.example.com"`) ||
		!strings.Contains(body, "cv-dmform") {
		t.Errorf("after a host was added (HX-Reswap %q) the refresh isn't the whole region:\n%s", resp.Header.Get("HX-Reswap"), text(body))
	}
	// The whole region carries the new key, so the next refresh is out-of-band again.
	known2 := knownOf(t, body)
	if known2 == known {
		t.Error("the key didn't change with the hosts")
	}
	if resp, body = e.get(t, "/parts/domains?known="+known2); resp.Header.Get("HX-Reswap") != "" || !strings.Contains(body, "hx-swap-oob") {
		t.Errorf("with the new key the refresh = HX-Reswap %q:\n%s", resp.Header.Get("HX-Reswap"), text(body))
	}
	// A host removed since: the whole region again.
	if err := e.Svc.DeleteApp(context.Background(), "extra"); err != nil {
		t.Fatal(err)
	}
	if resp, body = e.get(t, "/parts/domains?known="+known2); resp.Header.Get("HX-Reswap") != "innerHTML" || strings.Contains(body, "domain-extra.example.com") {
		t.Errorf("after a host was removed (HX-Reswap %q):\n%s", resp.Header.Get("HX-Reswap"), text(body))
	}
	// A page with no key (an old one) gets the whole region too.
	if resp, _ = e.get(t, "/parts/domains"); resp.Header.Get("HX-Reswap") != "innerHTML" {
		t.Errorf("a refresh with no key = HX-Reswap %q, want innerHTML", resp.Header.Get("HX-Reswap"))
	}

	// The app's own page: its key is its own hosts'.
	appPage := e.pageOf(t, "/apps/shop/domains")
	resp, body = e.get(t, "/parts/domains?app=shop&page=app&known="+knownOf(t, appPage))
	if resp.Header.Get("HX-Reswap") != "" || !strings.Contains(body, `id="domain-status-live.example.com"`) || strings.Contains(body, "idle.example.com") {
		t.Errorf("the app page's refresh = HX-Reswap %q:\n%s", resp.Header.Get("HX-Reswap"), text(body))
	}
}
