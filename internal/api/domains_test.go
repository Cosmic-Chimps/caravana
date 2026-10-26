package api

import (
	"context"
	"crypto/x509"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/edge/edgetest"
)

// Feature 023: the domains API. Every answer is checked against the contract.

// teamBody is whoamiBody with admin.whoami.localhost as a second, Team-only
// domain.
var teamBody = strings.Replace(whoamiBody, `"domains": [{"host": "whoami.localhost", "port": "http"}]`,
	`"domains": [{"host": "whoami.localhost", "port": "http"}, {"host": "admin.whoami.localhost", "port": "http", "teamOnly": true}]`, 1)

// domainsEnv is a test API whose edge presents Traefik's own certificate for
// every host, and whose console is console.example.com at 203.0.113.1.
func domainsEnv(t *testing.T) *testEnv {
	t.Helper()
	e := newTestEnv(t, "")
	e.Service.SetPublicURL("https://console.example.com")
	e.Service.SetEdge(edgetest.FakeResolver{Hosts: map[string][]string{
		"console.example.com": {"203.0.113.1"},
		"new.example.com":     {"198.51.100.9"},
	}}, &edgetest.FakeDialer{Default: []*x509.Certificate{edgetest.TraefikDefault(t)}})
	return e
}

func TestDomainsAPI(t *testing.T) {
	e := domainsEnv(t)
	doc := loadContract(t)
	d := deployOK(t, e, "whoami", whoamiBody)
	waitHealthy(t, e, "whoami", d.ID)

	t.Run("listDomains from the cache, then fresh", func(t *testing.T) {
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/domains", "", "")
		if status != http.StatusOK {
			t.Fatalf("listDomains = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "DomainList")
		got := decodeAs[struct {
			Domains []map[string]any `json:"domains"`
		}](t, raw).Domains
		if len(got) != 1 || got[0]["host"] != "whoami.localhost" || got[0]["state"] != "unknown" || got[0]["checkedAt"] != nil ||
			got[0]["router"] != "whoami-web" || got[0]["path"] != "" || got[0]["app"] != "whoami" {
			t.Fatalf("domains = %s, want whoami.localhost never checked", raw)
		}

		status, _, raw = call(t, http.MethodGet, e.URL+"/v1/domains?fresh=true&app=whoami", "", "")
		if status != http.StatusOK {
			t.Fatalf("listDomains fresh = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "DomainList")
		got = decodeAs[struct {
			Domains []map[string]any `json:"domains"`
		}](t, raw).Domains
		if len(got) != 1 || got[0]["state"] != "self_signed" || got[0]["checkedAt"] == nil {
			t.Errorf("fresh domains = %s, want self_signed and checked", raw)
		}
	})

	t.Run("listDomains refusals", func(t *testing.T) {
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/domains?fresh=maybe", "", "")
		wantError(t, "fresh=maybe", status, raw, http.StatusBadRequest, codeInvalidField, "fresh")
		status, _, raw = call(t, http.MethodGet, e.URL+"/v1/domains?app=nope", "", "")
		wantError(t, "app=nope", status, raw, http.StatusNotFound, codeNotFound, "")
		status, _, raw = call(t, http.MethodGet, e.URL+"/v1/domains?project=nope", "", "")
		wantError(t, "project=nope", status, raw, http.StatusNotFound, codeProjectNotFound, "project")
	})

	t.Run("getDomain", func(t *testing.T) {
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/domains/whoami.localhost", "", "")
		if status != http.StatusOK {
			t.Fatalf("getDomain = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "Domain")
		status, _, raw = call(t, http.MethodGet, e.URL+"/v1/domains/nope.example.com", "", "")
		wantError(t, "getDomain unknown", status, raw, http.StatusNotFound, codeDomainNotFound, "")
	})

	t.Run("checkDomain", func(t *testing.T) {
		status, header, raw := call(t, http.MethodPost, e.URL+"/v1/domains/check", "application/json", `{"host": "new.example.com"}`)
		if status != http.StatusOK || header.Get("Cache-Control") != "no-store" {
			t.Fatalf("checkDomain = %d %v %s, want 200 no-store", status, header, raw)
		}
		checkSchema(t, doc, raw, "DomainCheck")
		got := decodeAs[map[string]any](t, raw)
		dns := got["dns"].(map[string]any)
		cert := got["certificate"].(map[string]any)
		records := got["record"].([]any)
		if dns["status"] != "elsewhere" || cert["status"] != "none" || len(records) != 1 ||
			records[0].(map[string]any)["value"] != "203.0.113.1" || records[0].(map[string]any)["name"] != "new" {
			t.Errorf("check = %s, want DNS elsewhere, no certificate, one A record new → 203.0.113.1", raw)
		}

		for _, tc := range []struct{ body, field string }{
			{`{"host": "https://x.example.com"}`, "host"},
			{`{"host": "*.example.com"}`, "host"},
			{`{"host": "console.example.com"}`, "host"},
		} {
			status, _, raw := call(t, http.MethodPost, e.URL+"/v1/domains/check", "application/json", tc.body)
			wantError(t, "checkDomain "+tc.body, status, raw, http.StatusUnprocessableEntity, codeInvalidField, tc.field)
		}
		status, _, raw = call(t, http.MethodPost, e.URL+"/v1/domains/check", "application/json", `{"host": "x.example.com", "app": "nope"}`)
		wantError(t, "checkDomain unknown app", status, raw, http.StatusNotFound, codeNotFound, "")
	})

	t.Run("certificate settings and the list", func(t *testing.T) {
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/settings/certificates", "", "")
		if status != http.StatusOK {
			t.Fatalf("getCertificateSettings = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "CertificateSettings")
		if got := decodeAs[map[string]any](t, raw); got["issuer"] != "unknown" || got["warnBeforeDays"] != 14.0 || got["edgeAddress"] != "127.0.0.1:443" {
			t.Errorf("settings = %s", raw)
		}
		status, _, raw = call(t, http.MethodPut, e.URL+"/v1/settings/certificates", "application/json", `{"warnBeforeDays": 7}`)
		if status != http.StatusOK {
			t.Fatalf("putCertificateSettings = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "CertificateSettings")
		if got := decodeAs[map[string]any](t, raw); got["warnBeforeDays"] != 7.0 {
			t.Errorf("after PUT = %s, want 7", raw)
		}
		status, _, raw = call(t, http.MethodPut, e.URL+"/v1/settings/certificates", "application/json", `{"warnBeforeDays": 10}`)
		wantError(t, "warnBeforeDays 10", status, raw, http.StatusUnprocessableEntity, codeInvalidField, "warnBeforeDays")

		status, _, raw = call(t, http.MethodGet, e.URL+"/v1/certificates", "", "")
		if status != http.StatusOK {
			t.Fatalf("listCertificates = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "CertificateList")
		if !strings.Contains(string(raw), `"status":"self"`) || !strings.Contains(string(raw), `"issuer":"Traefik's own"`) {
			t.Errorf("certificates = %s, want whoami.localhost with Traefik's own", raw)
		}
	})
}

func TestDomainTakenAPI(t *testing.T) {
	e := domainsEnv(t)
	deployOK(t, e, "whoami", whoamiBody)

	status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/other/deployments", "application/json", whoamiBody)
	got := wantError(t, "deployApp with another app's host", status, raw, http.StatusConflict, codeDomainTaken, "services[0].domains[0].host")
	if !strings.Contains(got.Error.Message, "whoami") {
		t.Errorf("message = %q, want the other app named", got.Error.Message)
	}

	body := `{"compose": "services:\n  web:\n    image: traefik/whoami:latest\n    ports: [\"80\"]\n", "services": {"web": {"domains": [{"host": "whoami.localhost"}]}}}`
	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/other/compose", "application/json", body)
	wantError(t, "deployCompose with another app's host", status, raw, http.StatusConflict, codeDomainTaken, "services.web.domains[0].host")

	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/other/deployments", "application/json",
		strings.Replace(whoamiBody, `"host": "whoami.localhost", "port": "http"`, `"host": "example.com", "port": "http", "wwwRedirect": true, "path": "no-slash"`, 1))
	wantError(t, "a path without its slash", status, raw, http.StatusBadRequest, codeInvalidField, "services[0].domains[0].path")
}

// The Team-only password is in the deploy answer that turned the option on
// and in rotateTeamPassword's, each no-store, and in no other answer; its hash
// in none.
func TestTeamPasswordAPI(t *testing.T) {
	ctx := context.Background()
	e := domainsEnv(t)
	doc := loadContract(t)

	status, header, raw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/deployments", "application/json", teamBody)
	if status != http.StatusAccepted || header.Get("Cache-Control") != "no-store" {
		t.Fatalf("deploy = %d %v %s, want 202 no-store", status, header, raw)
	}
	checkSchema(t, doc, raw, "Deployment")
	first := decodeAs[struct {
		ID            string            `json:"id"`
		TeamPasswords map[string]string `json:"teamPasswords"`
	}](t, raw)
	password := first.TeamPasswords["admin.whoami.localhost"]
	if len(first.TeamPasswords) != 1 || password == "" {
		t.Fatalf("teamPasswords = %v, want admin.whoami.localhost's", first.TeamPasswords)
	}
	waitHealthy(t, e, "whoami", first.ID)
	row, err := e.Store.TeamPassword(ctx, "admin.whoami.localhost")
	if err != nil || !deploy.CheckTeamPassword(row.Hash, password) {
		t.Fatalf("stored hash doesn't verify the answered password (%v)", err)
	}

	// The same body again: nothing shown, and the answer may be cached.
	status, header, raw = call(t, http.MethodPost, e.URL+"/v1/apps/whoami/deployments", "application/json", teamBody)
	if status != http.StatusAccepted || strings.Contains(string(raw), "teamPasswords") || header.Get("Cache-Control") == "no-store" {
		t.Errorf("second deploy = %d %v %s, want no passwords", status, header, raw)
	}
	waitHealthy(t, e, "whoami", decodeAs[deployment](t, raw).ID)

	// Rotation: a new one, once.
	status, header, raw = call(t, http.MethodPost, e.URL+"/v1/apps/whoami/domains/admin.whoami.localhost/team-password", "", "")
	if status != http.StatusOK || header.Get("Cache-Control") != "no-store" {
		t.Fatalf("rotate = %d %v %s, want 200 no-store", status, header, raw)
	}
	checkSchema(t, doc, raw, "TeamPassword")
	rotated := decodeAs[struct {
		Host, User, TeamPassword string
	}](t, raw)
	if rotated.User != "team" || rotated.TeamPassword == "" || rotated.TeamPassword == password {
		t.Errorf("rotate = %s", raw)
	}
	row, _ = e.Store.TeamPassword(ctx, "admin.whoami.localhost")

	for _, tc := range []struct {
		path, code string
		status     int
	}{
		{"/v1/apps/whoami/domains/whoami.localhost/team-password", codeNotTeamOnly, http.StatusConflict},
		{"/v1/apps/whoami/domains/nope.example.com/team-password", codeDomainNotFound, http.StatusNotFound},
		{"/v1/apps/nope/domains/admin.whoami.localhost/team-password", codeNotFound, http.StatusNotFound},
	} {
		status, _, raw := call(t, http.MethodPost, e.URL+tc.path, "", "")
		wantError(t, tc.path, status, raw, tc.status, tc.code, "")
	}

	// Every later answer, and the log: no password, no hash.
	for _, path := range []string{
		"/v1/apps/whoami", "/v1/apps/whoami/deployments", "/v1/apps/whoami/deployments/" + first.ID,
		"/v1/domains", "/v1/domains/admin.whoami.localhost", "/v1/apps/whoami/jobspec", "/v1/attention",
		"/v1/certificates", "/v1/apps", "/v1/deployments",
	} {
		status, _, raw := call(t, http.MethodGet, e.URL+path, "", "")
		if status != http.StatusOK {
			t.Errorf("GET %s = %d %s", path, status, raw)
		}
		for what, secret := range map[string]string{"the first password": password, "the rotated password": rotated.TeamPassword, "the hash": row.Hash} {
			if strings.Contains(string(raw), secret) {
				t.Errorf("GET %s holds %s", path, what)
			}
		}
	}
	for what, secret := range map[string]string{"the first password": password, "the rotated password": rotated.TeamPassword, "the hash": row.Hash} {
		if strings.Contains(e.Logs.String(), secret) {
			t.Errorf("the log holds %s", what)
		}
	}

	// A stopped app can't rotate.
	if err := e.Store.SetAppStopped(ctx, "whoami", new(time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/whoami/domains/admin.whoami.localhost/team-password", "", "")
	wantError(t, "rotate on a stopped app", status, raw, http.StatusConflict, codeAppStopped, "")
}

// A deploy token may turn Team only on for its own app and gets the password
// once; it may not rotate it, nor read the domains.
func TestTeamPasswordDeployToken(t *testing.T) {
	e := domainsEnv(t)
	shopProject(t, e)
	auth := scopedToken(t, e, "shop", "web")
	body := strings.ReplaceAll(teamBody, "whoami.localhost", "web.localhost")

	status, header, raw := callWith(t, auth, http.MethodPost, e.RawURL+"/v1/apps/shop--staging--web/deployments", "application/json", body)
	if status != http.StatusAccepted || header.Get("Cache-Control") != "no-store" {
		t.Fatalf("deploy with the deploy token = %d %v %s", status, header, raw)
	}
	got := decodeAs[struct {
		ID            string            `json:"id"`
		TeamPasswords map[string]string `json:"teamPasswords"`
	}](t, raw)
	password := got.TeamPasswords["admin.web.localhost"]
	if password == "" {
		t.Fatalf("teamPasswords = %v, want admin.web.localhost's", got.TeamPasswords)
	}
	waitHealthy(t, e, "shop--staging--web", got.ID)

	for _, rt := range []struct{ method, path string }{
		{http.MethodPost, "/v1/apps/shop--staging--web/domains/admin.web.localhost/team-password"},
		{http.MethodGet, "/v1/domains"},
		{http.MethodGet, "/v1/domains/admin.web.localhost"},
		{http.MethodPost, "/v1/domains/check"},
		{http.MethodGet, "/v1/settings/certificates"},
		{http.MethodPut, "/v1/settings/certificates"},
		{http.MethodGet, "/v1/certificates"},
	} {
		status, _, raw := callWith(t, auth, rt.method, e.RawURL+rt.path, "application/json", `{}`)
		wantError(t, rt.method+" "+rt.path+" with a deploy token", status, raw, http.StatusForbidden, codeTokenScope, "")
	}
	for _, path := range []string{"/v1/apps/shop--staging--web", "/v1/apps/shop--staging--web/deployments/" + got.ID} {
		_, _, raw := callWith(t, auth, http.MethodGet, e.RawURL+path, "", "")
		if strings.Contains(string(raw), password) {
			t.Errorf("GET %s holds the password", path)
		}
	}
}

// deployCompose answers the password of a domain it turned Team only the same
// way deployApp does: once, no-store.
func TestTeamPasswordCompose(t *testing.T) {
	e := domainsEnv(t)
	body := `{"compose": "services:\n  web:\n    image: traefik/whoami:latest\n    ports: [\"80\"]\n", "services": {"web": {"domains": [{"host": "team.localhost", "teamOnly": true}]}}}`
	status, header, raw := call(t, http.MethodPost, e.URL+"/v1/apps/composed/compose", "application/json", body)
	if status != http.StatusAccepted || header.Get("Cache-Control") != "no-store" {
		t.Fatalf("deployCompose = %d %v %s, want 202 no-store", status, header, raw)
	}
	got := decodeAs[struct {
		ID            string            `json:"id"`
		TeamPasswords map[string]string `json:"teamPasswords"`
	}](t, raw)
	if got.TeamPasswords["team.localhost"] == "" {
		t.Fatalf("teamPasswords = %v, want team.localhost's", got.TeamPasswords)
	}
	waitHealthy(t, e, "composed", got.ID)
}

// DNS names are case-insensitive: a host sent with capitals is stored in lowercase, by deployApp
// and by checkDomain, and an app whose hosts are lowercase is stored as it was sent.
func TestDomainHostCaseAPI(t *testing.T) {
	e := domainsEnv(t)
	body := strings.Replace(whoamiBody, `"host": "whoami.localhost"`, `"host": "WWW.Whoami.LOCALHOST"`, 1)
	d := deployOK(t, e, "whoami", body)
	waitHealthy(t, e, "whoami", d.ID)

	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami", "", "")
	if status != http.StatusOK {
		t.Fatalf("getApp = %d %s", status, raw)
	}
	got := decodeAs[struct {
		App struct {
			Services []struct {
				Domains []struct {
					Host string `json:"host"`
				} `json:"domains"`
			} `json:"services"`
		} `json:"app"`
	}](t, raw)
	if len(got.App.Services) != 1 || len(got.App.Services[0].Domains) != 1 || got.App.Services[0].Domains[0].Host != "www.whoami.localhost" {
		t.Errorf("stored app = %s, want the host www.whoami.localhost", raw)
	}

	// The same payload again, in lowercase, is the same app: nothing else changes.
	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/whoami/deployments", "application/json",
		strings.Replace(whoamiBody, `"host": "whoami.localhost"`, `"host": "www.whoami.localhost"`, 1))
	if status != http.StatusAccepted {
		t.Errorf("the lowercase payload = %d %s", status, raw)
	}

	// A host that is no hostname is still refused, naming the field.
	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/bad/deployments", "application/json",
		strings.Replace(whoamiBody, `"host": "whoami.localhost"`, `"host": "Not A Host"`, 1))
	wantError(t, "a host with spaces", status, raw, http.StatusBadRequest, codeInvalidField, "services[0].domains[0].host")

	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/domains/check", "application/json", `{"host": "NEW.Example.com"}`)
	if status != http.StatusOK {
		t.Fatalf("checkDomain with capitals = %d %s", status, raw)
	}
	check := decodeAs[map[string]any](t, raw)
	if check["host"] != "new.example.com" || check["record"].([]any)[0].(map[string]any)["name"] != "new" {
		t.Errorf("check = %s, want the host new.example.com", raw)
	}
}
