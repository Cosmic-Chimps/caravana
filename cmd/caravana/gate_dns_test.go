package main

// The service-dns gate step (task dn53): a service sets its own resolvers. It
// is in its own file only because gate_test.go is long; runGate lists it with
// the other steps.

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// dnsCompose is a two-service Compose file whose web service sets its DNS.
// The servers are TEST-NET addresses nothing answers on, so web reaching store
// by name proves the names sidecar's /etc/hosts still does the work.
const dnsCompose = `services:
  store:
    image: busybox:1.37
    command: ["sh", "-c", "mkdir -p /www; echo dns-ok > /www/index.html; exec httpd -f -p 8080 -h /www"]
    ports: ["8080"]
    mem_reservation: 16m
    mem_limit: 32m
  web:
    image: busybox:1.37
    command: ["sh", "-c", "trap 'exit 0' TERM; while :; do sleep 3600 & wait $$!; done"]
    dns: [192.0.2.53, 192.0.2.54]
    dns_search: lab.test
    dns_opt: ["ndots:1", "timeout:1"]
    mem_reservation: 16m
    mem_limit: 32m
`

// serviceDNSStep (task dn53):
//
//  1. a hostname as a server is refused before anything deploys, naming the
//     Compose path;
//  2. the Compose app deploys healthy, and the job's web group network has the
//     dns block while store's has none;
//  3. web's /etc/resolv.conf has the servers, the search domain and the
//     options, and store's keeps the host's;
//  4. web still reaches store by its name (the names sidecar's /etc/hosts);
//  5. the export plans against the live job with no diff.
func (g *gate) serviceDNSStep(t *testing.T) {
	const appID = "gate-dns"
	remove := func() {
		if resp, err := g.apiRequest(http.MethodDelete, "/v1/apps/"+appID, "", ""); err == nil {
			resp.Body.Close()
		}
	}
	remove() // a leftover from an aborted run, if any
	waitFor(t, appID+"'s Nomad job to be gone", time.Minute, func() bool {
		_, _, err := g.target.Nomad.Jobs().Info(appID, nil)
		return err != nil
	})
	t.Cleanup(remove)

	// 1. Refused, naming the path the file has.
	bad, _ := json.Marshal(map[string]string{"compose": strings.Replace(dnsCompose, "192.0.2.53", "resolver.example", 1)})
	resp := g.api(t, http.MethodPost, "/v1/apps/"+appID+"/compose/preview", "application/json", string(bad))
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var refusal struct {
		Error struct{ Code, Field string } `json:"error"`
	}
	if resp.StatusCode < 400 || resp.StatusCode >= 500 || json.Unmarshal(raw, &refusal) != nil || refusal.Error.Field != "services.web.dns[0]" {
		t.Fatalf("a hostname as a DNS server = %d %s, want a field error on services.web.dns[0]", resp.StatusCode, raw)
	}

	// 2. Deploy.
	body, _ := json.Marshal(map[string]string{"compose": dnsCompose})
	raw = g.sendWant(t, http.StatusAccepted, http.MethodPost, "/v1/apps/"+appID+"/compose", string(body))
	var d gateDeployment
	if err := json.Unmarshal(raw, &d); err != nil || d.ID == "" {
		t.Fatalf("deploy %s: %s", appID, raw)
	}
	if final := g.waitFinalOf(t, appID, d.ID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("deploy %s ended %s: %s", appID, final.Status, final.Error)
	}
	job, _, err := g.target.Nomad.Jobs().Info(appID, nil)
	if err != nil {
		t.Fatalf("read the job %s: %v", appID, err)
	}
	for _, tg := range job.TaskGroups {
		if len(tg.Networks) != 1 {
			t.Fatalf("group %s has %d networks, want 1", *tg.Name, len(tg.Networks))
		}
		dns := tg.Networks[0].DNS
		switch *tg.Name {
		case "web":
			if dns == nil || !slices.Equal(dns.Servers, []string{"192.0.2.53", "192.0.2.54"}) ||
				!slices.Equal(dns.Searches, []string{"lab.test"}) || !slices.Equal(dns.Options, []string{"ndots:1", "timeout:1"}) {
				t.Errorf("web's network dns = %+v, want the file's servers, search and options", dns)
			}
		case "store":
			if dns != nil {
				t.Errorf("store's network dns = %+v, want none", dns)
			}
		}
	}

	// 3. resolv.conf.
	resolv := func(service string) string {
		t.Helper()
		out, err := g.exec(t, appID, service, d.ID, "cat", "/etc/resolv.conf")
		if err != nil {
			t.Fatalf("read %s's /etc/resolv.conf: %v", service, err)
		}
		return out
	}
	web := resolv("web")
	for _, want := range []string{"nameserver 192.0.2.53", "nameserver 192.0.2.54", "search lab.test", "ndots:1", "timeout:1"} {
		if !strings.Contains(web, want) {
			t.Errorf("web's /etc/resolv.conf has no %q:\n%s", want, web)
		}
	}
	if store := resolv("store"); strings.Contains(store, "192.0.2.53") {
		t.Errorf("store's /etc/resolv.conf has web's servers:\n%s", store)
	}

	// 4. Names beside the resolvers.
	var got string
	var lastErr error
	if !waitUntil(time.Minute, func() bool {
		got, lastErr = g.exec(t, appID, "web", d.ID, "wget", "-qO-", "-T", "5", "http://store:8080/")
		return lastErr == nil && strings.TrimSpace(got) == "dns-ok"
	}) {
		t.Fatalf("web can't reach http://store:8080/ by name beside its own DNS: %q, %v", got, lastErr)
	}

	// 5. Export.
	g.exportMatchesLive(t, appID)
}
