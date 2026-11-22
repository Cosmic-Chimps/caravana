package ui

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/dns"
	"github.com/cosmic-chimps/caravana/internal/dns/dnstest"
	"github.com/cosmic-chimps/caravana/internal/edge/edgetest"
)

// Feature 048's console: Settings › DNS providers, and what the Domains page
// says about the records Caravana makes. The provider is dnstest's in-memory
// one, which refuses any token but the sentinel.

// dnsSentinel is a token no page, address or log line may hold.
const dnsSentinel = "cf-s3cret-sentinel-048-ui"

// dnsEnv is a console with a provider that knows example.com, other.org, and
// this host's address (console.example.com is 203.0.113.1).
type dnsEnv struct {
	*consoleEnv
	p *dnstest.Provider
}

func newDNSEnv(t *testing.T) *dnsEnv {
	t.Helper()
	e := &dnsEnv{consoleEnv: newConsoleEnv(t), p: dnstest.NewProvider()}
	e.p.AddZone("example.com")
	e.p.AddZone("other.org")
	e.p.WantToken(dnsSentinel)
	e.Svc.SetDNSProviders(e.p.Factory())
	e.Svc.SetPublicURL("https://console.example.com")
	e.Svc.SetEdge(edgetest.FakeResolver{Hosts: map[string][]string{"console.example.com": {"203.0.113.1"}}}, nil)
	return e
}

// dnsAddForm is the page's add form: the one that posts without edit=1.
func dnsAddForm(t *testing.T, page string) plainForm {
	t.Helper()
	for _, f := range formsIn(page) {
		if f.Action == "/settings/dns" && f.Values.Get("edit") != "1" {
			return f
		}
	}
	t.Fatalf("no add form in:\n%s", page)
	return plainForm{}
}

// dnsZoneForm is the form of a row that posts to action for zone.
func dnsZoneForm(t *testing.T, page, action, zone string) plainForm {
	t.Helper()
	for _, f := range formsIn(page) {
		if f.Action == action && f.Values.Get("zone") == zone {
			return f
		}
	}
	t.Fatalf("no form posts to %s for %q:\n%s", action, zone, page)
	return plainForm{}
}

// addZone saves a zone through the page's own add form.
func (e *dnsEnv) addZone(t *testing.T, zone string, proxied bool) {
	t.Helper()
	_, page := e.get(t, "/settings/dns")
	set := url.Values{"zone": {zone}, "token": {dnsSentinel}}
	f := dnsAddForm(t, page)
	if !proxied {
		f.Values.Del("proxied")
	}
	resp, body := e.submit(t, f, set)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("add %s = %d, want 303:\n%s", zone, resp.StatusCode, text(body))
	}
}

// noDNSToken fails the test when an output holds the token.
func noDNSToken(t *testing.T, what string, outputs ...string) {
	t.Helper()
	for i, out := range outputs {
		if strings.Contains(out, dnsSentinel) {
			t.Errorf("%s: output %d holds the token", what, i)
		}
	}
}

func TestSettingsDNSEmptyPage(t *testing.T) {
	e := newDNSEnv(t)
	resp, page := e.get(t, "/settings/dns")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /settings/dns = %d", resp.StatusCode)
	}
	got := text(page)
	for _, want := range []string{"DNS providers", "No DNS providers. Add a Cloudflare zone and Caravana makes the record for each domain you add under it.",
		"Add a Cloudflare zone", "Cloudflare API token", "Needs Zone › DNS › Edit for this zone", "Proxy through Cloudflare",
		"The orange cloud. It applies to the records Caravana makes; records you made are left alone."} {
		if !strings.Contains(got, want) {
			t.Errorf("the page lacks %q:\n%s", want, got)
		}
	}
	for _, want := range []string{`placeholder="example.com"`, `type="password"`, `name="proxied"`, `href="/settings/dns"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the add form lacks %s", want)
		}
	}
	if strings.Contains(got, "Leave empty") {
		t.Error("the add form says the token may stay empty")
	}
	// The password input never has a value.
	if regexp.MustCompile(`type="password"[^>]*value=`).MatchString(page) {
		t.Error("the token input has a value")
	}
}

func TestSettingsDNSZoneLifecycle(t *testing.T) {
	e := newDNSEnv(t)
	hand := e.p.Seed("example.com", dns.Record{Type: "A", Name: "hand.example.com", Content: "198.51.100.9"})
	_ = hand

	t.Run("add", func(t *testing.T) {
		e.addZone(t, "example.com", true)
		resp, page := e.get(t, "/settings/dns?dns=saved")
		got := text(page)
		for _, want := range []string{"Saved. Test it to see that Caravana can reach the zone.", "example.com", "Cloudflare · proxied", "no records yet", "not tested yet"} {
			if !strings.Contains(got, want) {
				t.Errorf("after the save the page lacks %q:\n%s", want, got)
			}
		}
		if !strings.Contains(page, `href="/settings/dns"`) || !strings.Contains(page, `<span class="cv-snav-count">1</span>`) {
			t.Error("the nav doesn't count the zone")
		}
		noDNSToken(t, "after add", page, e.Logs.String(), resp.Header.Get("Location"))
	})

	t.Run("test lists the zone's records", func(t *testing.T) {
		_, page := e.get(t, "/settings/dns")
		resp, body := e.submit(t, dnsZoneForm(t, page, "/settings/dns/test", "example.com"), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST test = %d, want 200:\n%s", resp.StatusCode, text(body))
		}
		got := text(body)
		for _, want := range []string{"example.com: 1 record, 0 made by Caravana.", "The records of example.com as Cloudflare has them now:",
			"hand.example.com", "198.51.100.9", "Proxied"} {
			if !strings.Contains(got, want) {
				t.Errorf("the test answer lacks %q:\n%s", want, got)
			}
		}
		if !strings.Contains(body, "cv-mark-healthy") {
			t.Error("a tested zone has no healthy mark")
		}
		noDNSToken(t, "test", body, e.Logs.String())
		// The records are drawn in that answer only.
		_, later := e.get(t, "/settings/dns")
		if strings.Contains(later, "hand.example.com") {
			t.Error("a later visit still lists the records")
		}
		if !strings.Contains(text(later), "1 record, 0 made by Caravana") {
			t.Errorf("a later visit lacks the stored outcome:\n%s", text(later))
		}
	})

	t.Run("a refused test is said, and marks the nav", func(t *testing.T) {
		e.p.SetRefuse(true)
		defer e.p.SetRefuse(false)
		_, page := e.get(t, "/settings/dns")
		resp, body := e.submit(t, dnsZoneForm(t, page, "/settings/dns/test", "example.com"), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST test = %d, want 200", resp.StatusCode)
		}
		got := text(body)
		for _, want := range []string{"example.com can't be reached", "Deploys that add a domain under this zone fail until Caravana can reach it again."} {
			if !strings.Contains(got, want) {
				t.Errorf("a refused test lacks %q:\n%s", want, got)
			}
		}
		if !strings.Contains(body, "cv-mark-failed") {
			t.Error("a failed zone has no failed mark")
		}
		if !strings.Contains(body, `class="cv-snav-item"`) || strings.Count(body, `cv-mark cv-mark-failed`) < 2 {
			t.Error("the nav doesn't show the failing zone")
		}
		noDNSToken(t, "refused test", body, e.Logs.String())
	})

	t.Run("edit keeps the token and changes the proxy setting", func(t *testing.T) {
		_, page := e.get(t, "/settings/dns?edit=example.com")
		f := dnsZoneForm(t, page, "/settings/dns", "example.com")
		if f.Values.Get("edit") != "1" {
			t.Fatal("the row's form isn't the edit form")
		}
		if !strings.Contains(text(page), "Leave empty to keep the one stored.") || !strings.Contains(page, `id="dns-edit-0-zone"`) || !strings.Contains(page, "readonly") {
			t.Errorf("the edit form: %s", text(page))
		}
		f.Values.Del("proxied") // the box is unchecked
		resp, body := e.submit(t, f, url.Values{"token": {""}})
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/settings/dns?dns=saved" {
			t.Fatalf("POST edit = %d to %q:\n%s", resp.StatusCode, location(t, resp), text(body))
		}
		_, after := e.get(t, "/settings/dns?dns=saved")
		got := text(after)
		if !strings.Contains(got, "Cloudflare · direct") || !strings.Contains(got, "changed · not tested since") && !strings.Contains(got, "not tested yet") {
			t.Errorf("after the edit:\n%s", got)
		}
	})

	t.Run("a refused save keeps what was typed, never the token", func(t *testing.T) {
		_, page := e.get(t, "/settings/dns")
		for _, tc := range []struct {
			name string
			set  url.Values
			want string
		}{
			{"not a domain", url.Values{"zone": {"nodot"}, "token": {dnsSentinel}}, "is not a domain name"},
			{"no token", url.Values{"zone": {"fresh.example"}, "token": {""}}, "The API token is required the first time"},
		} {
			resp, body := e.submit(t, dnsAddForm(t, page), tc.set)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Errorf("%s: POST = %d, want 422", tc.name, resp.StatusCode)
			}
			if got := text(body); !strings.Contains(got, tc.want) {
				t.Errorf("%s: the error is missing %q:\n%s", tc.name, tc.want, got)
			}
			if !strings.Contains(body, `value="`+tc.set.Get("zone")+`"`) {
				t.Errorf("%s: the zone typed isn't kept", tc.name)
			}
			if !strings.Contains(body, `aria-invalid="true"`) {
				t.Errorf("%s: no input is marked", tc.name)
			}
			noDNSToken(t, tc.name, body, e.Logs.String())
		}
	})

	t.Run("remove", func(t *testing.T) {
		_, page := e.get(t, "/settings/dns")
		if got := text(page); !strings.Contains(got, "Caravana made no records in this zone.") {
			t.Errorf("the remove confirmation of an empty zone:\n%s", got)
		}
		resp, body := e.submit(t, dnsZoneForm(t, page, "/settings/dns/remove", "example.com"), nil)
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/settings/dns?dns=removed" {
			t.Fatalf("POST remove = %d to %q:\n%s", resp.StatusCode, location(t, resp), text(body))
		}
		_, after := e.get(t, "/settings/dns?dns=removed")
		got := text(after)
		if !strings.Contains(got, "Removed. The records Caravana made stay at Cloudflare.") || !strings.Contains(got, "No DNS providers.") {
			t.Errorf("after the removal:\n%s", got)
		}
		// A zone that is gone is a redirect, not an error.
		resp, _ = e.postForm(t, "/settings/dns/test", url.Values{"zone": {"example.com"}})
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/settings/dns" {
			t.Errorf("test of a removed zone = %d to %q", resp.StatusCode, location(t, resp))
		}
	})
}

// A zone with records of Caravana's says how many, and the removal says they stay.
func TestSettingsDNSRowsCountRecords(t *testing.T) {
	e := newDNSEnv(t)
	e.addZone(t, "example.com", false)
	e.deploy(t, whoamiApp()) // whoami.example.com gets its record
	_, page := e.get(t, "/settings/dns")
	got := text(page)
	for _, want := range []string{"1 record made by Caravana", "The record Caravana made in this zone stays at Cloudflare; remove it there if you no longer want it."} {
		if !strings.Contains(got, want) {
			t.Errorf("the row lacks %q:\n%s", want, got)
		}
	}
	// Test names the app a record was made for.
	resp, body := e.submit(t, dnsZoneForm(t, page, "/settings/dns/test", "example.com"), nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(text(body), "made by Caravana for whoami") {
		t.Errorf("the test doesn't name the app:\n%s", text(body))
	}
}

// A proxy change that Cloudflare can't take for every record is saved, and the page says what is left.
func TestSettingsDNSSavedButRecordsKeepTheOldSetting(t *testing.T) {
	e := newDNSEnv(t)
	e.addZone(t, "example.com", false)
	e.deploy(t, whoamiApp())
	e.p.SetUnavailable(true)
	_, page := e.get(t, "/settings/dns")
	f := dnsZoneForm(t, page, "/settings/dns", "example.com")
	// The edit form is the second form of the row's zone: pick it by edit=1.
	for _, c := range formsIn(page) {
		if c.Action == "/settings/dns" && c.Values.Get("edit") == "1" && c.Values.Get("zone") == "example.com" {
			f = c
		}
	}
	resp, body := e.submit(t, f, url.Values{"proxied": {"1"}, "token": {""}})
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("POST = %d, want 502:\n%s", resp.StatusCode, text(body))
	}
	if got := text(body); !strings.Contains(got, "saved, but the old setting stays on 1 record; the next save tries again.") {
		t.Errorf("the notice:\n%s", got)
	}
	e.p.SetUnavailable(false)
	_, page = e.get(t, "/settings/dns")
	if got := text(page); !strings.Contains(got, "Cloudflare · proxied") {
		t.Errorf("the zone wasn't saved:\n%s", got)
	}
}

// The Domains page: a record Caravana made is a fact of its row, and the Add panel names the zones.
func TestDomainsPageManagedRecord(t *testing.T) {
	e := newDNSEnv(t)
	e.addZone(t, "example.com", false)
	e.deploy(t, whoamiApp())

	row := text(dmRowOf(e.pageOf(t, "/domains"), "whoami.example.com"))
	if !strings.Contains(row, "DNS record made by Caravana in example.com (Cloudflare, direct)") {
		t.Errorf("the row lacks the managed fact:\n%s", row)
	}

	// The proxy setting changes the fact.
	_, page := e.get(t, "/settings/dns")
	for _, c := range formsIn(page) {
		if c.Action == "/settings/dns" && c.Values.Get("edit") == "1" {
			if resp, body := e.submit(t, c, url.Values{"proxied": {"1"}, "token": {""}}); resp.StatusCode != http.StatusSeeOther {
				t.Fatalf("edit = %d:\n%s", resp.StatusCode, text(body))
			}
		}
	}
	row = text(dmRowOf(e.pageOf(t, "/domains"), "whoami.example.com"))
	if !strings.Contains(row, "made by Caravana in example.com (Cloudflare, proxied)") {
		t.Errorf("the row after the proxy change:\n%s", row)
	}

	// A domain outside every zone has no such fact.
	e2 := newDNSEnv(t)
	e2.addZone(t, "other.org", false)
	e2.deploy(t, whoamiApp())
	if row := text(dmRowOf(e2.pageOf(t, "/domains"), "whoami.example.com")); strings.Contains(row, "DNS record") {
		t.Errorf("a domain under no zone says Caravana made its record:\n%s", row)
	}
}

func TestDomainsAddPanelNamesTheZones(t *testing.T) {
	e := newDNSEnv(t)
	e.deploy(t, whoamiApp())
	panel := text(addPanel(e.pageOf(t, "/domains")))
	if strings.Contains(panel, "creates the record itself") {
		t.Errorf("no zone is saved, and the panel names some:\n%s", panel)
	}
	e.addZone(t, "example.com", false)
	e.addZone(t, "other.org", false)
	panel = text(addPanel(e.pageOf(t, "/domains")))
	for _, want := range []string{"Add this record where you manage DNS for the domain:", "Under example.com, other.org Caravana creates the record itself."} {
		if !strings.Contains(panel, want) {
			t.Errorf("the panel lacks %q:\n%s", want, panel)
		}
	}
	if panel = text(addPanel(e.pageOf(t, "/apps/whoami/domains"))); !strings.Contains(panel, "Under example.com, other.org Caravana creates the record itself.") {
		t.Errorf("the app's panel lacks the note:\n%s", panel)
	}
}

func TestDomainsCheckNowUnderAManagedZone(t *testing.T) {
	e := newDNSEnv(t)
	e.deploy(t, whoamiApp())
	e.addZone(t, "example.com", false)
	e.addZone(t, "other.org", false)
	_, page := e.get(t, "/domains")
	f := addFormOf(t, page)

	t.Run("a host with no record gets one at the add", func(t *testing.T) {
		panel, _ := e.checkNow(t, f, url.Values{"host": {"fresh.example.com"}, "app": {"whoami"}, "action": {"check"}})
		got := text(panel)
		for _, want := range []string{"Caravana creates the record in example.com at Cloudflare when you add the domain.", "DNS record", "none yet: Caravana will create it"} {
			if !strings.Contains(got, want) {
				t.Errorf("the panel lacks %q:\n%s", want, got)
			}
		}
		if strings.Contains(got, "Add this record where you manage DNS") {
			t.Errorf("the record box is still there:\n%s", got)
		}
		if !strings.Contains(panel, `class="cv-btn cv-btn-primary cv-btn-sm">Add domain and deploy`) {
			t.Error("Add isn't the primary button")
		}
	})

	t.Run("a host outside every zone keeps the box", func(t *testing.T) {
		panel, _ := e.checkNow(t, f, url.Values{"host": {"fresh.elsewhere.net"}, "app": {"whoami"}, "action": {"check"}})
		got := text(panel)
		if !strings.Contains(got, "Add this record where you manage DNS for the domain:") || strings.Contains(got, "creates the record in") {
			t.Errorf("a host under no zone:\n%s", got)
		}
	})
}

func TestDomainsAddRefusals(t *testing.T) {
	post := func(e *dnsEnv, host string) (int, string) {
		_, page := e.get(t, "/domains")
		resp, body := e.submit(t, addFormOf(t, page), url.Values{"host": {host}, "app": {"whoami"}, "action": {"add"}})
		return resp.StatusCode, body
	}

	t.Run("a record that points elsewhere is refused at the Domain field", func(t *testing.T) {
		e := newDNSEnv(t)
		e.deploy(t, whoamiApp())
		e.addZone(t, "example.com", false)
		e.p.Seed("example.com", dns.Record{Type: "A", Name: "taken.example.com", Content: "198.51.100.9"})
		status, body := post(e, "taken.example.com")
		got := text(body)
		want := "taken.example.com already has a record at Cloudflare pointing at 198.51.100.9. Remove it there, or point it here by hand."
		if status != http.StatusUnprocessableEntity || !strings.Contains(got, want) {
			t.Errorf("POST = %d, want the sentence %q:\n%s", status, want, got)
		}
		if !strings.Contains(body, `id="domain-host-error"`) {
			t.Error("the sentence isn't under the Domain field")
		}
		if rec, err := e.Svc.GetApp(t.Context(), "whoami"); err != nil || len(rec.App.Services[0].Domains) != 1 {
			t.Errorf("the refused add changed the app: %v", err)
		}
	})

	t.Run("a provider that refuses the token is a notice", func(t *testing.T) {
		e := newDNSEnv(t)
		e.deploy(t, whoamiApp())
		e.addZone(t, "example.com", false)
		e.p.SetRefuse(true)
		status, body := post(e, "new.example.com")
		want := "Cloudflare refused the token for zone example.com: check that it has Zone › DNS › Edit for it. Nothing was deployed."
		if status != http.StatusUnprocessableEntity || !strings.Contains(text(body), want) {
			t.Errorf("POST = %d, want the notice %q:\n%s", status, want, text(body))
		}
	})

	t.Run("a provider that can't be reached is a 502 notice", func(t *testing.T) {
		e := newDNSEnv(t)
		e.deploy(t, whoamiApp())
		e.addZone(t, "example.com", false)
		e.p.SetUnavailable(true)
		status, body := post(e, "new.example.com")
		if status != http.StatusBadGateway || !strings.Contains(text(body), "Cloudflare could not be reached for zone example.com. Nothing was deployed.") {
			t.Errorf("POST = %d:\n%s", status, text(body))
		}
	})

	t.Run("an unknown public address is a notice", func(t *testing.T) {
		e := newDNSEnv(t)
		e.deploy(t, whoamiApp())
		e.addZone(t, "example.com", false)
		e.Svc.SetEdge(edgetest.FakeResolver{}, nil)
		status, body := post(e, "new.example.com")
		want := "Caravana can't tell this installation's public address, so it can't make the DNS record for the domain. Set CARAVANA_PUBLIC_URL to an address that resolves. Nothing was deployed."
		if status != http.StatusConflict || !strings.Contains(text(body), want) {
			t.Errorf("POST = %d, want the notice %q:\n%s", status, want, text(body))
		}
	})
}

// The token is in no page, address or log line through the whole flow.
func TestDNSTokenIsInNoPageOrLog(t *testing.T) {
	e := newDNSEnv(t)
	var outputs []string
	keep := func(parts ...string) { outputs = append(outputs, parts...) }

	e.addZone(t, "example.com", true)
	e.deploy(t, whoamiApp())
	for _, path := range []string{"/settings/dns", "/settings/dns?dns=saved", "/settings/dns?edit=example.com", "/domains", "/apps/whoami/domains", "/apps/whoami/deployments"} {
		resp, page := e.get(t, path)
		keep(page, resp.Header.Get("Location"))
	}
	_, page := e.get(t, "/settings/dns")
	resp, body := e.submit(t, dnsZoneForm(t, page, "/settings/dns/test", "example.com"), nil)
	keep(body, resp.Header.Get("Location"))
	// A refused save and a refused add draw the forms again.
	resp, body = e.submit(t, dnsAddForm(t, page), url.Values{"zone": {"nodot"}, "token": {dnsSentinel}})
	keep(body, resp.Header.Get("Location"))
	e.p.SetRefuse(true)
	_, dpage := e.get(t, "/domains")
	resp, body = e.submit(t, addFormOf(t, dpage), url.Values{"host": {"new.example.com"}, "app": {"whoami"}, "action": {"add"}})
	keep(body, resp.Header.Get("Location"))
	for _, tc := range settingsPages {
		resp, page := e.get(t, tc.path)
		keep(page, resp.Header.Get("Location"))
	}
	noDNSToken(t, "the flow", append(outputs, e.Logs.String())...)
}
