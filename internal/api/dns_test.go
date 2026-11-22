package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/dns"
	"github.com/cosmic-chimps/caravana/internal/dns/dnstest"
	"github.com/cosmic-chimps/caravana/internal/edge/edgetest"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// Feature 048: the DNS provider API. Every answer is checked against the
// contract, and the token is searched for in every answer, the log and the file.

const dnsToken = "cf-s3cret-sentinel-048-api"

// dnsEnv is a test API whose console is console.example.com at 203.0.113.1 and
// whose provider is a fake that knows example.com and wants dnsToken.
func dnsEnv(t *testing.T) (*testEnv, *dnstest.Provider) {
	t.Helper()
	p := dnstest.NewProvider()
	p.AddZone("example.com")
	p.WantToken(dnsToken)
	e := newTestEnvWith(t, "", func(svc *service.Service) {
		svc.SetPublicURL("https://console.example.com")
		svc.SetEdge(edgetest.FakeResolver{Hosts: map[string][]string{"console.example.com": {"203.0.113.1"}}}, nil)
		svc.SetDNSProviders(p.Factory())
	})
	return e, p
}

func putZone(t *testing.T, e *testEnv, zone, body string) (int, []byte) {
	t.Helper()
	status, _, raw := call(t, http.MethodPut, e.URL+"/v1/dns/zones/"+zone, "application/json", body)
	return status, raw
}

const zoneBody = `{"provider": "cloudflare", "proxied": false, "token": "` + dnsToken + `"}`

func domainBody(hosts ...string) string {
	var ds []string
	for _, h := range hosts {
		ds = append(ds, `{"host": "`+h+`", "port": "http"}`)
	}
	return `{"source": {"kind": "image"}, "services": [{"name": "web", "image": "traefik/whoami:latest",
		"ports": [{"name": "http", "containerPort": 80}], "domains": [` + strings.Join(ds, ",") + `],
		"health": {"path": "/", "interval": "10s", "timeout": "2s"}}]}`
}

func TestDNSZoneEndpoints(t *testing.T) {
	e, p := dnsEnv(t)
	doc := loadContract(t)

	status, raw := putZone(t, e, "example.com", zoneBody)
	if status != http.StatusCreated {
		t.Fatalf("putDNSZone = %d %s, want 201", status, raw)
	}
	checkSchema(t, doc, raw, "DNSZone")
	got := decodeAs[struct {
		Zone     string `json:"zone"`
		Provider string `json:"provider"`
		Proxied  bool   `json:"proxied"`
		Records  int    `json:"records"`
		LastTest any    `json:"lastTest"`
	}](t, raw)
	if got.Zone != "example.com" || got.Provider != "cloudflare" || got.Proxied || got.Records != 0 || got.LastTest != nil {
		t.Errorf("answer = %s", raw)
	}

	// A save without a token keeps the stored one: 200.
	status, raw = putZone(t, e, "example.com", `{"provider": "cloudflare", "proxied": true}`)
	if status != http.StatusOK {
		t.Fatalf("second putDNSZone = %d %s, want 200", status, raw)
	}
	checkSchema(t, doc, raw, "DNSZone")

	status, _, raw = call(t, http.MethodGet, e.URL+"/v1/dns/zones", "", "")
	if status != http.StatusOK {
		t.Fatalf("listDNSZones = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "DNSZoneList")

	p.Seed("example.com", dns.Record{Type: "A", Name: "hand.example.com", Content: "198.51.100.7"})
	status, header, raw := call(t, http.MethodPost, e.URL+"/v1/dns/zones/example.com/test", "", "")
	if status != http.StatusOK || header.Get("Cache-Control") != "no-store" {
		t.Fatalf("testDNSZone = %d %v %s, want 200 no-store", status, header, raw)
	}
	checkSchema(t, doc, raw, "DNSZoneTest")
	test := decodeAs[struct {
		Zone     string `json:"zone"`
		LastTest struct {
			OK      bool   `json:"ok"`
			Message string `json:"message"`
		} `json:"lastTest"`
		Records []struct{ Name string } `json:"records"`
	}](t, raw)
	if test.Zone != "example.com" || !test.LastTest.OK || test.LastTest.Message != "1 record, 0 made by Caravana" || len(test.Records) != 1 {
		t.Errorf("test answer = %s", raw)
	}

	// A refusal is an outcome: 200 with ok false and no records.
	p.SetRefuse(true)
	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/dns/zones/example.com/test", "", "")
	p.SetRefuse(false)
	checkSchema(t, doc, raw, "DNSZoneTest")
	if status != http.StatusOK || !strings.Contains(string(raw), `"ok":false`) || !strings.Contains(string(raw), `"records":[]`) {
		t.Errorf("a refused test = %d %s, want 200 with ok false and no records", status, raw)
	}

	status, _, raw = call(t, http.MethodGet, e.URL+"/v1/dns/records", "", "")
	if status != http.StatusOK {
		t.Fatalf("listDNSRecords = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "DNSRecordList")

	status, _, raw = call(t, http.MethodDelete, e.URL+"/v1/dns/zones/example.com", "", "")
	if status != http.StatusNoContent {
		t.Fatalf("deleteDNSZone = %d %s", status, raw)
	}
	status, _, raw = call(t, http.MethodDelete, e.URL+"/v1/dns/zones/example.com", "", "")
	wantError(t, "delete twice", status, raw, http.StatusNotFound, codeDNSZoneNotFound, "")
	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/dns/zones/example.com/test", "", "")
	wantError(t, "test of an unknown zone", status, raw, http.StatusNotFound, codeDNSZoneNotFound, "")
}

func TestDNSZoneRefusals(t *testing.T) {
	e, _ := dnsEnv(t)
	for _, tc := range []struct {
		name, zone, body, field string
	}{
		{"no dot", "localhost", zoneBody, "zone"},
		{"another provider", "example.com", `{"provider": "route53", "token": "t"}`, "provider"},
		{"no token the first time", "example.com", `{"provider": "cloudflare"}`, "token"},
		{"an empty token", "example.com", `{"provider": "cloudflare", "token": ""}`, "token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, raw := putZone(t, e, tc.zone, tc.body)
			wantError(t, tc.name, status, raw, http.StatusBadRequest, codeInvalidField, tc.field)
		})
	}
	status, _, raw := call(t, http.MethodPut, e.URL+"/v1/dns/zones/example.com", "application/json", `{"provider": "cloudflare", "colour": "red"}`)
	wantError(t, "unknown field", status, raw, http.StatusBadRequest, codeUnknownField, "colour")
}

func TestDNSRecordsFollowDomainsThroughTheAPI(t *testing.T) {
	e, p := dnsEnv(t)
	doc := loadContract(t)
	if status, raw := putZone(t, e, "example.com", zoneBody); status != http.StatusCreated {
		t.Fatalf("putDNSZone = %d %s", status, raw)
	}

	d := deployOK(t, e, "whoami", domainBody("web.example.com"))
	waitHealthy(t, e, "whoami", d.ID)
	if recs := p.Records("example.com"); len(recs) != 1 || recs[0].Comment != "caravana:whoami" || recs[0].Content != "203.0.113.1" {
		t.Fatalf("provider records = %+v", recs)
	}

	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/domains/web.example.com", "", "")
	if status != http.StatusOK {
		t.Fatalf("getDomain = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "Domain")
	rec := decodeAs[struct {
		Record *struct {
			Zone, Provider string
			Proxied        bool
		} `json:"record"`
	}](t, raw).Record
	if rec == nil || rec.Zone != "example.com" || rec.Provider != "cloudflare" {
		t.Errorf("Domain.record = %s", raw)
	}

	status, _, raw = call(t, http.MethodGet, e.URL+"/v1/dns/records", "", "")
	checkSchema(t, doc, raw, "DNSRecordList")
	if status != http.StatusOK || !strings.Contains(string(raw), `"app":"whoami"`) {
		t.Errorf("listDNSRecords = %d %s", status, raw)
	}

	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/domains/check", "application/json", `{"host": "new.example.com"}`)
	if status != http.StatusOK {
		t.Fatalf("checkDomain = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "DomainCheck")
	if !strings.Contains(string(raw), `"managedZone":"example.com"`) {
		t.Errorf("checkDomain = %s, want managedZone example.com", raw)
	}

	// A hand-made record elsewhere refuses the deploy, naming the field.
	p.Seed("example.com", dns.Record{Type: "A", Name: "taken.example.com", Content: "198.51.100.9"})
	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/whoami/deployments", "application/json", domainBody("web.example.com", "taken.example.com"))
	wantError(t, "deploy onto a taken host", status, raw, http.StatusConflict, codeDNSRecordExists, "services[0].domains[1].host")

	// A provider that refuses or can't be reached refuses a deploy that needs it.
	p.SetRefuse(true)
	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/whoami/deployments", "application/json", domainBody("web.example.com", "second.example.com"))
	wantError(t, "provider refuses", status, raw, http.StatusUnprocessableEntity, codeDNSProviderRefused, "services[0].domains[1].host")
	p.SetRefuse(false)
	p.SetUnavailable(true)
	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/whoami/deployments", "application/json", domainBody("web.example.com", "second.example.com"))
	wantError(t, "provider down", status, raw, http.StatusBadGateway, codeDNSProviderUnavailable, "services[0].domains[1].host")
	p.SetUnavailable(false)

	// No public address known: 409.
	e.Service.SetPublicURL("")
	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/whoami/deployments", "application/json", domainBody("web.example.com", "third.example.com"))
	wantError(t, "no address", status, raw, http.StatusConflict, codeDNSAddressUnknown, "")
}

// The token is in no answer, no log line and no file.
func TestDNSTokenIsInNoAnswer(t *testing.T) {
	e, p := dnsEnv(t)
	var answers []string
	note := func(_ int, raw []byte) { answers = append(answers, string(raw)) }
	note(putZone(t, e, "example.com", zoneBody))
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/dns/zones", ""},
		{http.MethodPost, "/v1/dns/zones/example.com/test", ""},
		{http.MethodGet, "/v1/dns/records", ""},
		{http.MethodGet, "/v1/domains", ""},
		{http.MethodPut, "/v1/dns/zones/nope", `{"provider": "cloudflare", "token": "` + dnsToken + `"}`},
	} {
		_, _, raw := call(t, c.method, e.URL+c.path, "application/json", c.body)
		answers = append(answers, string(raw))
	}
	p.SetRefuse(true)
	_, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/deployments", "application/json", domainBody("web.example.com"))
	answers = append(answers, string(raw))
	_, _, raw = call(t, http.MethodPost, e.URL+"/v1/dns/zones/example.com/test", "", "")
	answers = append(answers, string(raw))
	p.SetRefuse(false)

	for _, a := range answers {
		if strings.Contains(a, dnsToken) {
			t.Errorf("an answer holds the token: %s", a)
		}
	}
	if strings.Contains(e.Logs.String(), dnsToken) {
		t.Error("the request log holds the token")
	}
	raw, err := os.ReadFile(filepath.Join(e.Dir, "caravana.db"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), dnsToken) {
		t.Error("the SQLite file holds the token")
	}
}
