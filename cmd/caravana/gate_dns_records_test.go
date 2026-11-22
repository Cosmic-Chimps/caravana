package main

// The dns-records gate step (feature 048, tasks.md T009). It is in its own file only because
// gate_test.go is long; runGate lists it with the other steps.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/dns"
)

// dnsRecordRow is a row of listDNSRecords (schema DNSRecordMade).
type dnsRecordRow struct {
	Host    string `json:"host"`
	Type    string `json:"type"`
	Zone    string `json:"zone"`
	App     string `json:"app"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
}

// dnsRows reads the records Caravana made, as it has them.
func (g *gate) dnsRows(t *testing.T) []dnsRecordRow {
	t.Helper()
	var list struct {
		Records []dnsRecordRow `json:"records"`
	}
	g.getJSON(t, "/v1/dns/records", &list)
	return list.Records
}

// dnsRecordsStep (feature 048): a zone connected to the lab's fake Cloudflare (dnstest.Server, which
// counts its calls and checks the bearer token), and what Caravana makes, keeps and removes in it:
//
//  1. the zone lab.test saved with a sentinel token, and its Test listing the fake's seeded records;
//  2. (without CARAVANA_PUBLIC_URL, Caravana doesn't know its address: the deploy is 409
//     dns_address_unknown and the rest of the step is skipped);
//  3. an app deployed with web.lab.test has an A record 203.0.113.1 commented caravana:lab048 at the
//     fake, a row, and Domain.record set; a redeploy makes no provider call;
//  4. a deploy adding taken.lab.test (a hand-made record pointing at 203.0.113.9) is 409
//     dns_record_exists on that domain and leaves the app as it was;
//  5. here.lab.test (hand-made, pointing here) deploys with no row and its record untouched;
//  6. a deploy without web.lab.test removes its record, and the proxy setting turned on updates the
//     records that remain;
//  7. a wrong token: Test says refused, and a deploy adding a host is 422 dns_provider_refused;
//  8. deleting the app removes its records; the zone removed forgets the rows and leaves the records;
//  9. the token is in no answer, Caravana's log, the Nomad job, its export or the SQLite file.
//
// It needs the fake Cloudflare on Caravana's loopback, so it runs only in the lab.
func (g *gate) dnsRecordsStep(t *testing.T) {
	cf := g.target.DNS
	if cf == nil {
		t.Skip("the fake Cloudflare runs beside an in-process Caravana only: the lab")
	}
	const (
		id     = "lab048"
		zone   = "lab.test"
		webH   = "web.lab.test"
		apiH   = "api.lab.test"
		takenH = "taken.lab.test"
		hereH  = "here.lab.test"
		newH   = "new.lab.test"
		here   = "203.0.113.1" // what the lab's resolver says CARAVANA_PUBLIC_URL's host is
	)
	sentinel := "cf-s3cret-sentinel-048-" + g.run
	havePublic := strings.TrimSpace(os.Getenv("CARAVANA_PUBLIC_URL")) != ""

	removeApp := func() {
		if resp, err := g.apiRequest(http.MethodDelete, "/v1/apps/"+id, "", ""); err == nil {
			resp.Body.Close()
		}
	}
	removeZone := func() {
		if resp, err := g.apiRequest(http.MethodDelete, "/v1/dns/zones/"+zone, "", ""); err == nil {
			resp.Body.Close()
		}
	}
	removeApp() // leftovers from an aborted run, if any
	removeZone()
	waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool {
		_, _, err := g.target.Nomad.Jobs().Info(id, nil)
		return err != nil
	})
	t.Cleanup(func() {
		cf.SetToken(sentinel)
		removeApp()
		removeZone()
	})
	answers := map[string]string{} // everything read, searched for the token at the end

	// The fake's zone, with a hand-made record Caravana must never touch, one that points elsewhere
	// and one that already points here.
	cf.SetToken(sentinel)
	cf.AddZone(zone)
	mail := cf.Seed(zone, dns.Record{Type: "A", Name: "mail." + zone, Content: "203.0.113.50", Comment: "mine"})
	taken := cf.Seed(zone, dns.Record{Type: "A", Name: takenH, Content: "203.0.113.9"})
	hereRec := cf.Seed(zone, dns.Record{Type: "A", Name: hereH, Content: here})
	recordsAt := func(host string) []dns.Record {
		var out []dns.Record
		for _, r := range cf.Records(zone) {
			if r.Name == host {
				out = append(out, r)
			}
		}
		return out
	}

	// 1. The zone, and its test.
	putZone := func(proxied bool, token *string) (int, []byte) {
		body := map[string]any{"provider": "cloudflare", "proxied": proxied}
		if token != nil {
			body["token"] = *token
		}
		raw, _ := json.Marshal(body)
		return g.send(t, http.MethodPut, "/v1/dns/zones/"+zone, string(raw))
	}
	status, raw := putZone(false, &sentinel)
	if status != http.StatusCreated {
		t.Fatalf("putDNSZone = %d, want 201: %s", status, raw)
	}
	answers["putDNSZone"] = string(raw)
	type testAnswer struct {
		LastTest struct {
			OK      bool   `json:"ok"`
			Message string `json:"message"`
		} `json:"lastTest"`
		Records []struct {
			Name    string `json:"name"`
			Content string `json:"content"`
			Managed bool   `json:"managed"`
		} `json:"records"`
	}
	testZone := func() testAnswer {
		t.Helper()
		raw := g.sendWant(t, http.StatusOK, http.MethodPost, "/v1/dns/zones/"+zone+"/test", "")
		answers["testDNSZone"+time.Now().String()] = string(raw)
		var out testAnswer
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("testDNSZone: %v: %s", err, raw)
		}
		return out
	}
	tested := testZone()
	names := map[string]bool{}
	for _, r := range tested.Records {
		names[r.Name] = true
	}
	if !tested.LastTest.OK || !names[mail.Name] || !names[takenH] || !names[hereH] {
		t.Fatalf("testDNSZone = %+v, want ok and the fake's three seeded records", tested)
	}

	// deploy posts the app with these domains (one service, web) and returns the answer's status.
	deploy := func(hosts ...string) (int, []byte) {
		web := actionsWeb("", 1)
		var domains []map[string]any
		for _, h := range hosts {
			domains = append(domains, map[string]any{"host": h, "port": "http"})
		}
		web["domains"] = domains
		return g.send(t, http.MethodPost, "/v1/apps/"+id+"/deployments", labApp(web))
	}
	deployHealthy := func(what string, hosts ...string) gateDeployment {
		t.Helper()
		status, raw := deploy(hosts...)
		answers["deploy "+what] = string(raw)
		var d gateDeployment
		if status != http.StatusAccepted || json.Unmarshal(raw, &d) != nil || d.ID == "" {
			t.Fatalf("deploy %s (%s) = %d: %s", id, what, status, raw)
		}
		if final := g.waitFinalOf(t, id, d.ID, 4*time.Minute); final.Status != "healthy" {
			t.Fatalf("deploy %s (%s) ended %s: %s", id, what, final.Status, final.Error)
		}
		return d
	}

	// 2. Without the installation's public address Caravana can't make the record.
	if !havePublic {
		msg := g.refusal(t, http.StatusConflict, "dns_address_unknown", "", http.MethodPost, "/v1/apps/"+id+"/deployments", labApp(func() map[string]any {
			web := actionsWeb("", 1)
			web["domains"] = []map[string]any{{"host": webH, "port": "http"}}
			return web
		}()))
		if !strings.Contains(msg, "CARAVANA_PUBLIC_URL") {
			t.Errorf("the dns_address_unknown message %q doesn't say to set CARAVANA_PUBLIC_URL", msg)
		}
		if n := len(recordsAt(webH)); n != 0 {
			t.Errorf("the refused deploy made %d records at the provider", n)
		}
		t.Skip("CARAVANA_PUBLIC_URL isn't set, so Caravana can't tell its own address: set it to https://console.lab.test " +
			"(the lab's resolver answers 203.0.113.1 for that host) to run the record steps")
	}

	// 3. A deploy makes the record.
	deployHealthy("first", webH)
	made := recordsAt(webH)
	if len(made) != 1 || made[0].Type != "A" || made[0].Content != here || made[0].Comment != "caravana:"+id || made[0].Proxied {
		t.Fatalf("the records at the provider for %s = %+v, want one direct A %s commented caravana:%s", webH, made, here, id)
	}
	if rows := g.dnsRows(t); len(rows) != 1 || rows[0].Host != webH || rows[0].App != id || rows[0].Zone != zone || rows[0].Content != here {
		t.Fatalf("listDNSRecords = %+v, want one row for %s", rows, webH)
	}
	var domain struct {
		Record *struct {
			Zone     string `json:"zone"`
			Provider string `json:"provider"`
			Proxied  bool   `json:"proxied"`
		} `json:"record"`
	}
	g.getJSON(t, "/v1/domains/"+webH, &domain)
	if domain.Record == nil || domain.Record.Zone != zone || domain.Record.Provider != "cloudflare" || domain.Record.Proxied {
		t.Errorf("Domain.record of %s = %+v, want the zone %s at cloudflare, direct", webH, domain.Record, zone)
	}
	// A redeploy asks the provider nothing: the host has its row.
	cf.ResetCalls()
	status, raw = g.send(t, http.MethodPost, "/v1/apps/"+id+"/deployments", "")
	var again gateDeployment
	if status != http.StatusAccepted || json.Unmarshal(raw, &again) != nil || again.ID == "" {
		t.Fatalf("redeploy %s = %d: %s", id, status, raw)
	}
	if final := g.waitFinalOf(t, id, again.ID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("the redeploy ended %s: %s", final.Status, final.Error)
	}
	if n := cf.Calls(); n != 0 {
		t.Errorf("a redeploy made %d provider calls (%v), want none", n, cf.Log())
	}

	// 4. A record that points elsewhere is refused, and nothing changes.
	versions := g.deploymentsOf(t, id)
	msg := g.refusal(t, http.StatusConflict, "dns_record_exists", "services[0].domains[1].host", http.MethodPost, "/v1/apps/"+id+"/deployments", func() string {
		web := actionsWeb("", 1)
		web["domains"] = []map[string]any{{"host": webH, "port": "http"}, {"host": takenH, "port": "http"}}
		return labApp(web)
	}())
	if !strings.Contains(msg, takenH) || !strings.Contains(msg, taken.Content) {
		t.Errorf("the dns_record_exists message %q doesn't name %s and %s", msg, takenH, taken.Content)
	}
	if got := g.deploymentsOf(t, id); got != versions {
		t.Errorf("the refused deploy left %d deployments, want %d", got, versions)
	}
	var stored struct {
		App struct {
			Services []struct {
				Domains []struct {
					Host string `json:"host"`
				} `json:"domains"`
			} `json:"services"`
		} `json:"app"`
	}
	g.getJSON(t, "/v1/apps/"+id, &stored)
	if len(stored.App.Services) != 1 || len(stored.App.Services[0].Domains) != 1 || stored.App.Services[0].Domains[0].Host != webH {
		t.Errorf("the stored app after the refused deploy = %+v, want web.lab.test alone", stored.App)
	}
	if got := recordsAt(takenH); len(got) != 1 || got[0].ID != taken.ID || got[0].Content != taken.Content {
		t.Errorf("the hand-made record of %s changed: %+v", takenH, got)
	}

	// 5. A record that already points here is left as it is.
	deployHealthy("here", webH, hereH)
	if got := recordsAt(hereH); len(got) != 1 || got[0].ID != hereRec.ID || got[0].Comment != "" {
		t.Errorf("the hand-made record of %s = %+v, want the seeded one untouched", hereH, got)
	}
	for _, r := range g.dnsRows(t) {
		if r.Host == hereH {
			t.Errorf("a row exists for %s: %+v, want none for a record Caravana didn't make", hereH, r)
		}
	}

	// 6. Dropping a domain removes its record, and the proxy setting reaches the records that remain.
	deployHealthy("api", apiH, hereH)
	waitFor(t, "the record of "+webH+" to be removed at the provider", 30*time.Second, func() bool { return len(recordsAt(webH)) == 0 })
	if got := recordsAt(apiH); len(got) != 1 || got[0].Content != here || got[0].Proxied {
		t.Fatalf("the records at the provider for %s = %+v, want one direct A %s", apiH, got, here)
	}
	rows := g.dnsRows(t)
	if len(rows) != 1 || rows[0].Host != apiH {
		t.Errorf("listDNSRecords after dropping %s = %+v, want a row for %s alone", webH, rows, apiH)
	}
	status, raw = putZone(true, nil)
	if status != http.StatusOK {
		t.Fatalf("putDNSZone proxied (no token) = %d, want 200: %s", status, raw)
	}
	answers["putDNSZone proxied"] = string(raw)
	if got := recordsAt(apiH); len(got) != 1 || !got[0].Proxied {
		t.Errorf("after turning the proxy on, %s at the provider = %+v, want proxied", apiH, got)
	}
	if rows := g.dnsRows(t); len(rows) != 1 || !rows[0].Proxied {
		t.Errorf("after turning the proxy on, listDNSRecords = %+v, want proxied", rows)
	}
	if got := recordsAt(mail.Name); len(got) != 1 || got[0].Proxied || got[0].Comment != "mine" {
		t.Errorf("the hand-made record %s changed: %+v", mail.Name, got)
	}
	if got := recordsAt(hereH); len(got) != 1 || got[0].Proxied {
		t.Errorf("the hand-made record %s was proxied: %+v", hereH, got)
	}

	// 7. A wrong token.
	cf.SetToken("not-the-token-" + g.run)
	if wrong := testZone(); wrong.LastTest.OK {
		t.Errorf("Test with a wrong token = %+v, want not ok", wrong.LastTest)
	}
	g.refusal(t, http.StatusUnprocessableEntity, "dns_provider_refused", "", http.MethodPost, "/v1/apps/"+id+"/deployments", func() string {
		web := actionsWeb("", 1)
		web["domains"] = []map[string]any{{"host": apiH, "port": "http"}, {"host": hereH, "port": "http"}, {"host": newH, "port": "http"}}
		return labApp(web)
	}())
	cf.SetToken(sentinel)
	if ok := testZone(); !ok.LastTest.OK {
		t.Errorf("Test with the right token again = %+v, want ok", ok.LastTest)
	}

	// 8. Deleting the app removes its records.
	g.sendWant(t, http.StatusNoContent, http.MethodDelete, "/v1/apps/"+id, "")
	waitFor(t, "the app's record to be removed at the provider", 30*time.Second, func() bool { return len(recordsAt(apiH)) == 0 })
	if rows := g.dnsRows(t); len(rows) != 0 {
		t.Errorf("listDNSRecords after deleting the app = %+v, want none", rows)
	}
	waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool {
		_, _, err := g.target.Nomad.Jobs().Info(id, nil)
		return err != nil
	})
	// Removing the zone forgets the rows and leaves the records where they are.
	deployHealthy("again", webH)
	if got := recordsAt(webH); len(got) != 1 {
		t.Fatalf("the records at the provider for %s = %+v, want one", webH, got)
	}
	// What the later checks read, while the app has its job and its record.
	for _, p := range []string{"/v1/apps/" + id, "/v1/apps/" + id + "/deployments", "/v1/apps/" + id + "/jobspec",
		"/v1/apps", "/v1/dns/zones", "/v1/dns/records", "/v1/domains", "/v1/attention"} {
		status, body := g.send(t, http.MethodGet, p, "")
		if status != http.StatusOK {
			t.Errorf("GET %s = %d: %s", p, status, body)
		}
		answers[p] = string(body)
	}
	if job, _, err := g.target.Nomad.Jobs().Info(id, nil); err == nil {
		answers["the Nomad job"] = string(mustJSON(t, job))
	} else {
		t.Errorf("read the job %s: %v", id, err)
	}
	g.sendWant(t, http.StatusNoContent, http.MethodDelete, "/v1/dns/zones/"+zone, "")
	if rows := g.dnsRows(t); len(rows) != 0 {
		t.Errorf("listDNSRecords after removing the zone = %+v, want none", rows)
	}
	if got := recordsAt(webH); len(got) != 1 {
		t.Errorf("removing the zone changed the provider's records for %s: %+v, want the one record left", webH, got)
	}
	g.refusal(t, http.StatusNotFound, "dns_zone_not_found", "", http.MethodDelete, "/v1/dns/zones/"+zone, "")
	if v, _, err := g.target.Nomad.Variables().Read("caravana/dns/lab~test", nil); err == nil && v != nil {
		t.Errorf("the zone's token is still in Nomad after the zone was removed")
	}
	// With its zone gone, deleting the app asks the provider nothing and leaves the record.
	cf.ResetCalls()
	g.sendWant(t, http.StatusNoContent, http.MethodDelete, "/v1/apps/"+id, "")
	if n := cf.Calls(); n != 0 {
		t.Errorf("deleting an app whose zone was removed made %d provider calls (%v)", n, cf.Log())
	}

	// 9. The token is nowhere.
	if g.target.ServerLog != nil {
		answers["Caravana's log"] = g.target.ServerLog()
	}
	if dir := g.target.DataDir; dir != "" {
		for _, f := range []string{"caravana.db", "caravana.db-wal"} {
			b, _ := os.ReadFile(filepath.Join(dir, f))
			answers["the SQLite file "+f] = string(b)
		}
	}
	for where, text := range answers {
		if strings.Contains(text, sentinel) {
			t.Errorf("%s holds the zone's token", where)
		}
	}
}
