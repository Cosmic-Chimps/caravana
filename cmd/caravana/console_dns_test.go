package main

// console-dns-providers (feature 048, tasks.md T009): what the owner does in Settings › DNS providers
// and on an app's Domains page, over HTTP the way a browser does it, against the lab's fake Cloudflare.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/dns"
)

// dnsProviders:
//
//  1. Settings › DNS providers: the nav entry and the empty state, then Add zone through the form
//     (the sentinel token, proxy off): the row says Cloudflare · direct, "no records yet" and
//     "not tested yet";
//  2. Test answers 200 with the records table listing the fake's seeded records (the hand-made one
//     with no "made by"); Edit with the proxy on and no token changes the row to Cloudflare · proxied;
//  3. an app's Domains page: the Add panel says Caravana creates the records under lab.test, and Check
//     now for new.lab.test says Caravana creates the record at Cloudflare and that there is none yet;
//  4. with CARAVANA_PUBLIC_URL set, adding the domain deploys and makes the record at the fake
//     (proxied), and the row's detail says "made by Caravana in lab.test (Cloudflare, proxied)";
//  5. Remove the zone: its confirmation says the records stay at Cloudflare, and they do;
//  6. the token is in no page and no log line.
func (c *console) dnsProviders(t *testing.T) {
	cf := c.g.target.DNS
	if cf == nil {
		t.Skip("the fake Cloudflare runs beside an in-process Caravana only: the lab")
	}
	const (
		id   = "con048"
		zone = "lab.test"
		host = "new.lab.test"
	)
	c.ensureSignedIn(t)
	havePublic := strings.TrimSpace(os.Getenv("CARAVANA_PUBLIC_URL")) != ""
	sentinel := "cf-s3cret-sentinel-048-ui-" + c.g.run
	pages := map[string]string{} // every page read, searched for the token at the end

	remove := func() {
		if resp, err := c.g.apiRequest(http.MethodDelete, "/v1/apps/"+id, "", ""); err == nil {
			resp.Body.Close()
		}
		if resp, err := c.g.apiRequest(http.MethodDelete, "/v1/dns/zones/"+zone, "", ""); err == nil {
			resp.Body.Close()
		}
	}
	remove() // leftovers from an aborted run, if any
	c.appGone(t, id)
	t.Cleanup(func() {
		cf.SetToken(sentinel)
		remove()
		c.appGone(t, id)
	})
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
		pages["POST "+path] = body
	}
	// rowOf is the text of the zone's row: what the owner reads under its name.
	rowOf := func(page string) string {
		t.Helper()
		text := prose(page)
		i := strings.Index(text, zone)
		if i < 0 {
			t.Fatalf("the zone %s isn't on the page:\n%s", zone, text)
		}
		return text[i:]
	}

	// The fake's zone with one hand-made record.
	cf.SetToken(sentinel)
	cf.AddZone(zone)
	cf.Seed(zone, dns.Record{Type: "A", Name: "mail." + zone, Content: "203.0.113.50", Comment: "mine"})
	recordsAt := func(name string) []dns.Record {
		var out []dns.Record
		for _, r := range cf.Records(zone) {
			if r.Name == name {
				out = append(out, r)
			}
		}
		return out
	}

	// 1. The empty page, then the zone added through the form.
	page := get("/settings/dns")
	text := pageText(page)
	if !strings.Contains(text, "DNS providers") || !strings.Contains(text, "No DNS providers.") || strings.Contains(text, zone+"\n") {
		t.Errorf("the empty DNS providers page lacks its title or empty state:\n%s", text)
	}
	post303("/settings/dns", url.Values{"zone": {zone}, "token": {sentinel}}, "/settings/dns?dns=saved")
	page = get("/settings/dns?dns=saved")
	row := rowOf(page)
	for _, want := range []string{"Cloudflare · direct", "no records yet", "not tested yet"} {
		if !strings.Contains(row, want) {
			t.Errorf("the saved zone's row lacks %q:\n%s", want, row)
		}
	}
	if strings.Contains(page, sentinel) || strings.Contains(page, `type="password" value="`) {
		t.Errorf("the page holds the token or a token input with a value")
	}

	// 2. Test lists what Cloudflare has; Edit turns the proxy on.
	resp, body := c.post(t, "/settings/dns/test", url.Values{"zone": {zone}})
	pages["POST /settings/dns/test"] = body
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /settings/dns/test = %d, want 200:\n%s", resp.StatusCode, pageText(body))
	}
	text = prose(body)
	for _, want := range []string{"The records of " + zone + " as Cloudflare has them now", "mail." + zone, "203.0.113.50"} {
		if !strings.Contains(text, want) {
			t.Errorf("the Test answer lacks %q:\n%s", want, text)
		}
	}
	post303("/settings/dns", url.Values{"edit": {"1"}, "zone": {zone}, "token": {""}, "proxied": {"1"}}, "/settings/dns?dns=saved")
	if row = rowOf(get("/settings/dns?dns=saved")); !strings.Contains(row, "Cloudflare · proxied") {
		t.Errorf("after Edit with the proxy on, the row doesn't say Cloudflare · proxied:\n%s", row)
	}

	// 3. An app that was defined and not deployed, and its Domains page.
	raw, _ := json.Marshal(map[string]any{"id": id, "source": map[string]string{"kind": "image"}, "services": []any{actionsWeb("", 1)}})
	resp2 := c.g.api(t, http.MethodPost, "/v1/apps?deploy=false", "application/json", string(raw))
	created, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusCreated {
		t.Fatalf("POST /v1/apps?deploy=false = %d, want 201: %s", resp2.StatusCode, created)
	}
	domainsPage := func() string { return get("/apps/" + id + "/domains") }
	if text = prose(domainsPage()); !strings.Contains(text, "Under "+zone+" Caravana creates the record itself") {
		t.Errorf("the Add panel doesn't name %s as a zone Caravana handles:\n%s", zone, text)
	}
	form := func() url.Values {
		t.Helper()
		forms := formsPosting(domainsPage(), "/apps/"+id+"/domains")
		if len(forms) != 1 || forms[0].Get("revision") == "" {
			t.Fatalf("the Add panel of %s's page = %v, want one form with a revision", id, forms)
		}
		return forms[0]
	}
	f := form()
	f.Set("host", host)
	f.Set("action", "check")
	post303("/apps/"+id+"/domains", f, "/apps/"+id+"/domains?checked="+host)
	text = prose(get("/apps/" + id + "/domains?checked=" + host))
	for _, want := range []string{"Caravana creates the record in " + zone + " at Cloudflare", "none yet: Caravana will create it"} {
		if !strings.Contains(text, want) {
			t.Errorf("Check now for %s lacks %q:\n%s", host, want, text)
		}
	}
	if n := len(recordsAt(host)); n != 0 {
		t.Errorf("Check now made %d records at the provider", n)
	}

	// 4. Add the domain: Caravana makes the record.
	if !havePublic {
		t.Log("CARAVANA_PUBLIC_URL isn't set: Caravana can't tell its own address, so the add is refused (dns_address_unknown) and the record isn't made; set it to https://console.lab.test to run the rest")
	} else {
		f = form()
		f.Set("host", host)
		f.Set("action", "add")
		resp, body = c.post(t, "/apps/"+id+"/domains", f)
		pages["POST add domain"] = body
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("Add domain %s = %d, want 303:\n%s", host, resp.StatusCode, pageText(body))
		}
		var deps struct {
			Deployments []gateDeployment `json:"deployments"`
		}
		c.g.getJSON(t, "/v1/apps/"+id+"/deployments", &deps)
		if len(deps.Deployments) != 1 {
			t.Fatalf("after Add domain, %s has %d deployments, want 1", id, len(deps.Deployments))
		}
		if final := c.g.waitFinalOf(t, id, deps.Deployments[0].ID, 4*time.Minute); final.Status != "healthy" {
			t.Fatalf("the deploy that added %s ended %s: %s", host, final.Status, final.Error)
		}
		made := recordsAt(host)
		if len(made) != 1 || made[0].Content != "203.0.113.1" || !made[0].Proxied || made[0].Comment != "caravana:"+id {
			t.Fatalf("the records at the provider for %s = %+v, want one proxied A 203.0.113.1 commented caravana:%s", host, made, id)
		}
		detail := prose(get("/apps/" + id + "/domains?open=" + host))
		if !strings.Contains(detail, "made by Caravana in "+zone+" (Cloudflare, proxied)") {
			t.Errorf("the row's detail for %s lacks \"made by Caravana in %s (Cloudflare, proxied)\":\n%s", host, zone, detail)
		}
		pages["/domains"] = get("/domains")
		if row = rowOf(get("/settings/dns")); !strings.Contains(row, "1 record made by Caravana") {
			t.Errorf("the zone's row doesn't count the record Caravana made:\n%s", row)
		}
	}

	// 5. Remove the zone: the records stay at Cloudflare.
	page = get("/settings/dns")
	confirm := prose(formMarkup(page, "/settings/dns/remove"))
	if havePublic && !strings.Contains(confirm, "The record Caravana made in this zone stays at Cloudflare") {
		t.Errorf("the Remove confirmation doesn't say the record stays at Cloudflare:\n%s", confirm)
	} else if !havePublic && !strings.Contains(confirm, "Caravana made no records in this zone") {
		t.Errorf("the Remove confirmation of a zone with no records doesn't say so:\n%s", confirm)
	}
	post303("/settings/dns/remove", url.Values{"zone": {zone}}, "/settings/dns?dns=removed")
	page = get("/settings/dns?dns=removed")
	if text = pageText(page); !strings.Contains(text, "Removed. The records Caravana made stay at Cloudflare.") || !strings.Contains(text, "No DNS providers.") {
		t.Errorf("after Remove the page doesn't say so or still lists the zone:\n%s", text)
	}
	if havePublic {
		if n := len(recordsAt(host)); n != 1 {
			t.Errorf("removing the zone left %d records at the provider for %s, want the one record", n, host)
		}
	}

	// 6. The token is in no page and no log line.
	for where, html := range pages {
		if strings.Contains(html, sentinel) {
			t.Errorf("%s holds the zone's token", where)
		}
	}
	if log := c.g.target.ServerLog; log != nil && strings.Contains(log(), sentinel) {
		t.Errorf("Caravana's log holds the zone's token")
	}
}
