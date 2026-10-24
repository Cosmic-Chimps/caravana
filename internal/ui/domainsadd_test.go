package ui

import (
	"crypto/x509"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/edge/edgetest"
)

// Feature 023's Add panel: on /domains (an app select, the services of an app with several) and on
// the app's page (the app fixed); Check now draws the record box and the two check lines from a
// fresh check; Add domain and deploy deploys the app with one more domain on the page's revision.

// addEnv has whoami (one service) and duet (two) deployed, and a resolver that knows this host
// (console.example.com → 203.0.113.1 and 2001:db8::1), a host that points here, one that points
// elsewhere and one with no record.
func addEnv(t *testing.T) *consoleEnv {
	t.Helper()
	e := duetEnv(t)
	e.deploy(t, whoamiApp())
	e.Svc.SetPublicURL("https://console.example.com")
	e.Svc.SetEdge(edgetest.FakeResolver{Hosts: map[string][]string{
		"console.example.com": {"203.0.113.1", "2001:db8::1"},
		"here.example.com":    {"203.0.113.1", "2001:db8::1"},
		"away.example.com":    {"185.199.108.153"},
	}}, nil)
	return e
}

// addPanel is the Add panel's section of a page.
func addPanel(body string) string {
	return regexp.MustCompile(`(?s)<section id="add".*?</section>`).FindString(body)
}

func TestDomainAddPanel(t *testing.T) {
	e := addEnv(t)
	body := e.pageOf(t, "/domains")
	panel := addPanel(body)
	for _, want := range []string{"Add a domain", "Domain", "Sends traffic to", "Path", "Optional. Only requests under this path reach the app; the path is kept.",
		"Point it at caravana", "Type A Name @ or the first label Value 203.0.113.1", "Type AAAA Name @ or the first label Value 2001:db8::1",
		"The root of a domain needs an A record. A subdomain can use one too.", "Check", "DNS points at caravana", "HTTPS certificate", "Check now",
		"Add domain and deploy", "Cancel", "Choose an app", "duet › api · :8080", "whoami › web · :80", "The app's first service"} {
		if !strings.Contains(text(panel), want) {
			t.Errorf("the panel lacks %q:\n%s", want, text(panel))
		}
	}
	// Each app's revision rides along, so the add is checked against the page.
	for _, id := range []string{"duet", "whoami"} {
		if !strings.Contains(panel, `name="revision-`+id+`" value="`+e.revisionOf(t, id)+`"`) {
			t.Errorf("no revision for %s", id)
		}
	}
	// Before a check both lines are rings.
	if strings.Count(panel, `cv-mark cv-mark-idle`) != 2 {
		t.Error("before a check the lines aren't both rings")
	}

	// An app picked (?app=) lists its own services, with no pairs.
	panel = addPanel(e.pageOf(t, "/domains?app=duet"))
	if !strings.Contains(panel, `<option value="duet" selected>`) || !strings.Contains(panel, `<option value="api">api · :8080</option>`) || strings.Contains(panel, "duet/api") {
		t.Errorf("?app=duet's panel:\n%s", panel)
	}
	// One service: the port as text.
	panel = addPanel(e.pageOf(t, "/domains?app=whoami"))
	if !strings.Contains(text(panel), ":80, set in Configure") || strings.Contains(panel, `id="domain-service"`) {
		t.Errorf("?app=whoami's panel:\n%s", text(panel))
	}
	// The app's page: the app fixed as text.
	panel = addPanel(e.pageOf(t, "/apps/whoami/domains"))
	if !strings.Contains(text(panel), "Sends traffic to whoami :80, set in Configure") || strings.Contains(panel, `id="domain-app"`) {
		t.Errorf("the app page's panel:\n%s", text(panel))
	}
}

// checkNow posts the Add form's Check now and follows its 303 to the page it leads to, at the
// panel: the panel as that page draws it, and the address it was drawn from.
func (e *consoleEnv) checkNow(t *testing.T, f plainForm, set url.Values) (panel, address string) {
	t.Helper()
	set.Set("action", "check")
	resp, body := e.submit(t, f, set)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("Check now = %d, want 303:\n%s", resp.StatusCode, text(body))
	}
	loc := location(t, resp)
	page, frag, _ := strings.Cut(loc, "#")
	if frag != "add" {
		t.Errorf("Check now leads to %q, want the panel (#add)", loc)
	}
	return addPanel(e.pageOf(t, page)), loc
}

func TestDomainAddCheckNow(t *testing.T) {
	e := addEnv(t)
	form := addFormOf(t, e.pageOf(t, "/domains"))
	tests := []struct {
		host string
		dns  string // the DNS line's mark
		want []string
	}{
		{"here.example.com", "healthy", []string{"passed: DNS points at caravana", "not yet: HTTPS certificate issued after you add it", "Name here Value 203.0.113.1"}},
		{"away.example.com", "failed", []string{"failed: DNS points at caravana points at 185.199.108.153"}},
		{"nothing.example.com", "failed", []string{"failed: DNS points at caravana no record yet"}},
		{"example.com", "failed", []string{"Name @ Value 203.0.113.1"}},
	}
	for _, tc := range tests {
		panel, loc := e.checkNow(t, form, url.Values{"host": {tc.host}, "app": {"whoami"}})
		if want := "/domains?checked=" + tc.host + "#add"; loc != want {
			t.Errorf("Check now of %s leads to %q, want %q", tc.host, loc, want)
		}
		lines := regexp.MustCompile(`(?s)<ul class="cv-dmchecks".*?</ul>`).FindString(panel)
		if !strings.HasPrefix(strings.TrimSpace(strings.SplitN(lines, `<li class="cv-dmcheck">`, 2)[1]), `<span class="cv-mark cv-mark-`+tc.dns+`"`) {
			t.Errorf("%s: the DNS line's mark isn't %s:\n%s", tc.host, tc.dns, lines)
		}
		for _, want := range tc.want {
			if !strings.Contains(text(panel), want) {
				t.Errorf("%s: the panel lacks %q:\n%s", tc.host, want, text(panel))
			}
		}
		// What was typed is kept; the add is primary only when DNS points here.
		if !strings.Contains(panel, `value="`+tc.host+`"`) || !strings.Contains(panel, `<option value="whoami" selected>`) {
			t.Errorf("%s: the form lost what was typed", tc.host)
		}
		primary := strings.Contains(panel, `class="cv-btn cv-btn-primary cv-btn-sm">Add domain and deploy`)
		if primary != (tc.dns == "healthy") {
			t.Errorf("%s: Add is primary = %v", tc.host, primary)
		}
	}
	// A host that is served gets its real certificate state.
	e.Svc.SetEdge(nil, &edgetest.FakeDialer{Default: []*x509.Certificate{edgetest.TraefikDefault(t)}})
	panel, _ := e.checkNow(t, form, url.Values{"host": {"whoami.example.com"}, "app": {"whoami"}})
	if got := text(panel); !strings.Contains(got, "HTTPS certificate Traefik's own") {
		t.Errorf("a served host's certificate line:\n%s", got)
	}
	// The app's page checks with the app fixed and no revision needed.
	page := formWith(t, e.pageOf(t, "/apps/whoami/domains"), "/apps/whoami/domains")
	panel, loc := e.checkNow(t, page, url.Values{"host": {"here.example.com"}})
	if loc != "/apps/whoami/domains?checked=here.example.com#add" || !strings.Contains(text(panel), "passed: DNS points at caravana") {
		t.Errorf("the app page's check led to %q:\n%s", loc, text(panel))
	}
}

// The page Check now leads to draws the check from the console's cache: the check lines, the
// record box and what was typed. An unknown host, or one whose check has expired, draws the panel
// empty, and the address holds nothing but the host.
func TestDomainAddCheckedPage(t *testing.T) {
	e := addEnv(t)
	form := addFormOf(t, e.pageOf(t, "/domains"))
	_, loc := e.checkNow(t, form, url.Values{"host": {"here.example.com"}, "app": {"duet"}, "service": {"api"}, "path": {"/api"}})
	if strings.Contains(loc, "duet") || strings.Contains(loc, "/api") {
		t.Errorf("the address holds more than the host: %q", loc)
	}
	page, _, _ := strings.Cut(loc, "#")
	panel := addPanel(e.pageOf(t, page))
	for _, want := range []string{"passed: DNS points at caravana", "Name here Value 203.0.113.1"} {
		if !strings.Contains(text(panel), want) {
			t.Errorf("the checked panel lacks %q:\n%s", want, text(panel))
		}
	}
	for _, want := range []string{`value="here.example.com"`, `<option value="duet" selected>`, `value="/api"`} {
		if !strings.Contains(panel, want) {
			t.Errorf("the checked panel lost what was typed (%s):\n%s", want, panel)
		}
	}
	// Unknown: no check was kept for it.
	empty := addPanel(e.pageOf(t, "/domains?checked=never.example.com"))
	if strings.Contains(empty, "passed:") || strings.Contains(empty, `value="never.example.com"`) || strings.Count(empty, `cv-mark cv-mark-idle`) != 2 {
		t.Errorf("an unknown ?checked= draws a checked panel:\n%s", text(empty))
	}
	// Expired: a minute later it is gone.
	e.Clock.Advance(checkedFor + time.Second)
	expired := addPanel(e.pageOf(t, page))
	if strings.Contains(expired, "passed:") || strings.Contains(expired, `value="here.example.com"`) || strings.Count(expired, `cv-mark cv-mark-idle`) != 2 {
		t.Errorf("an expired check is still drawn:\n%s", text(expired))
	}
}

func TestDomainAddDeploys(t *testing.T) {
	e := addEnv(t)
	form := addFormOf(t, e.pageOf(t, "/domains"))
	resp, body := e.submit(t, form, url.Values{"host": {"api2.example.com"}, "service": {"duet/api"}, "path": {"/api"}})
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location(t, resp), "/apps/duet/deployments#deployment-") {
		t.Fatalf("add = %d to %q:\n%s", resp.StatusCode, location(t, resp), text(body))
	}
	got := e.storedApp(t, "duet").Services[1].Domains
	if len(got) != 2 || got[1] != (app.Domain{Host: "api2.example.com", Port: "http", Path: "/api"}) {
		t.Errorf("duet's api domains = %+v", got)
	}
	// The new row is listed, with its path.
	if row := dmRowOf(e.pageOf(t, "/domains"), "api2.example.com"); !strings.Contains(row, `<span class="cv-dmpath">/api</span>`) {
		t.Errorf("the new row:\n%s", row)
	}
	// The app's page adds to its app.
	page := formWith(t, e.pageOf(t, "/apps/whoami/domains"), "/apps/whoami/domains")
	if resp, body = e.submit(t, page, url.Values{"host": {"two.whoami.example.com"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("add on the app page = %d:\n%s", resp.StatusCode, text(body))
	}
}

func TestDomainAddRefusals(t *testing.T) {
	e := addEnv(t)
	form := addFormOf(t, e.pageOf(t, "/domains"))
	n := e.deployCount(t, "whoami")
	tests := []struct {
		name   string
		set    url.Values
		status int
		want   string
		field  string // the input the error is under
	}{
		{"another app's host", url.Values{"app": {"whoami"}, "host": {"web.duet.example.com"}}, 422, "web.duet.example.com is already a domain of duet. Remove it there first.", "domain-host"},
		{"another app's host, checked", url.Values{"action": {"check"}, "app": {"whoami"}, "host": {"web.duet.example.com"}}, 422, "is already a domain of duet", "domain-host"},
		{"the console's own", url.Values{"app": {"whoami"}, "host": {"console.example.com"}}, 422, "console.example.com is the console's own address", "domain-host"},
		{"malformed", url.Values{"app": {"whoami"}, "host": {"Not A Host"}}, 422, "is not a lowercase hostname", "domain-host"},
		{"malformed, checked", url.Values{"action": {"check"}, "app": {"whoami"}, "host": {"not a host"}}, 422, "is not a domain name", "domain-host"},
		{"no host", url.Values{"app": {"whoami"}, "host": {""}}, 422, "Enter a domain.", "domain-host"},
		{"no app", url.Values{"host": {"x.example.com"}}, 422, "Choose the app the domain sends traffic to.", "domain-app"},
		{"a bad path", url.Values{"app": {"whoami"}, "host": {"p.example.com"}, "path": {"api"}}, 422, "", "domain-path"},
		{"a service of another app", url.Values{"app": {"whoami"}, "host": {"s.example.com"}, "service": {"duet/api"}}, 422, "That service is another app's", "domain-app"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := e.submit(t, form, tc.set)
			if resp.StatusCode != tc.status || !strings.Contains(text(body), tc.want) || !strings.Contains(body, `aria-describedby="`+tc.field) && !strings.Contains(body, tc.field+`-error`) {
				t.Errorf("= %d, want %d with %q under %s:\n%s", resp.StatusCode, tc.status, tc.want, tc.field, text(addPanel(body)))
			}
		})
	}
	if e.deployCount(t, "whoami") != n {
		t.Error("a refused add deployed")
	}

	t.Run("a stale page", func(t *testing.T) {
		other := e.storedApp(t, "whoami")
		other.Services[0].Resources.MemoryMB = 123
		e.deploy(t, other)
		n := e.deployCount(t, "whoami")
		resp, body := e.submit(t, form, url.Values{"app": {"whoami"}, "host": {"late.example.com"}})
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), changedSincePage) || e.deployCount(t, "whoami") != n {
			t.Errorf("a stale add = %d:\n%s", resp.StatusCode, text(body))
		}
	})

	t.Run("no revision", func(t *testing.T) {
		resp, _ := e.postForm(t, "/domains", url.Values{"app": {"whoami"}, "host": {"r.example.com"}})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("an add without the app's revision = %d, want 400", resp.StatusCode)
		}
	})

	t.Run("a kept Compose file takes the add", func(t *testing.T) {
		k, _ := keptShopEnv(t)
		page := addFormOf(t, k.pageOf(t, "/domains"))
		n := k.deployCount(t, "shop")
		resp, body := k.submit(t, page, url.Values{"app": {"shop"}, "service": {"shop/web"}, "host": {"x.shop.example.com"}})
		// A kept-file app takes the add through its overlay, like any app (feature 023).
		if resp.StatusCode != http.StatusSeeOther || k.deployCount(t, "shop") != n+1 {
			t.Errorf("add to a kept-file app = %d:\n%s", resp.StatusCode, text(addPanel(body)))
		}
	})
}

// addFormOf is /domains' Add form: the post to /domains (the filter is a get to the same address).
func addFormOf(t *testing.T, body string) plainForm {
	t.Helper()
	for _, f := range formsIn(body) {
		if f.Action == "/domains" && f.Method == "post" {
			return f
		}
	}
	t.Fatalf("no Add form in:\n%s", text(body))
	return plainForm{}
}

// DNS names are case-insensitive, so a host typed with capitals is lowercased, not refused: the
// Add panel's Check now and its add both read "Shop.Example.COM" as shop.example.com. A host with
// spaces is still no hostname.
func TestDomainAddLowercasesTheHost(t *testing.T) {
	e := addEnv(t)
	form := addFormOf(t, e.pageOf(t, "/domains"))

	panel, loc := e.checkNow(t, form, url.Values{"host": {"HERE.Example.COM"}, "app": {"whoami"}})
	if got := text(panel); loc != "/domains?checked=here.example.com#add" || !strings.Contains(got, "passed: DNS points at caravana") || !strings.Contains(got, "Name here Value 203.0.113.1") {
		t.Errorf("check with capitals led to %q:\n%s", loc, got)
	}

	n := e.deployCount(t, "whoami")
	resp, body := e.submit(t, form, url.Values{"host": {"Shop.Example.COM"}, "app": {"whoami"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("add with capitals = %d:\n%s", resp.StatusCode, text(body))
	}
	var hosts []string
	for _, d := range e.storedApp(t, "whoami").Services[0].Domains {
		hosts = append(hosts, d.Host)
	}
	if !slices.Contains(hosts, "shop.example.com") || slices.Contains(hosts, "Shop.Example.COM") || e.deployCount(t, "whoami") != n+1 {
		t.Errorf("stored hosts = %v after %d deploys, want shop.example.com stored once", hosts, e.deployCount(t, "whoami")-n)
	}

	// Taken is judged on the lowercase name too.
	form = addFormOf(t, e.pageOf(t, "/domains"))
	resp, body = e.submit(t, form, url.Values{"host": {"WEB.Duet.Example.com"}, "app": {"whoami"}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "web.duet.example.com is already a domain of duet") {
		t.Errorf("a taken host with capitals = %d:\n%s", resp.StatusCode, text(addPanel(body)))
	}
	// A dot at the end is still refused, in its own words.
	resp, body = e.submit(t, form, url.Values{"host": {"dot.example.com."}, "app": {"whoami"}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "is not a hostname: leave out the final dot") {
		t.Errorf("a final dot = %d:\n%s", resp.StatusCode, text(addPanel(body)))
	}
}
