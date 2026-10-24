package main

// The domains gate step (feature 023, quickstart §6), through the API with the
// gate's token. It is in its own file only because gate_test.go is long;
// runGate lists it after schedules.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/nomadjob"
)

// gateDomain is the part of a Domain (api/openapi.yaml) this step reads.
type gateDomain struct {
	Host        string `json:"host"`
	App         string `json:"app"`
	Service     string `json:"service"`
	Path        string `json:"path"`
	WWWRedirect bool   `json:"wwwRedirect"`
	TeamOnly    bool   `json:"teamOnly"`
	Router      string `json:"router"`
	RedirectOf  string `json:"redirectOf"`
	State       string `json:"state"`
	DNS         struct {
		Status string   `json:"status"`
		Seen   []string `json:"seen"`
	} `json:"dns"`
	Certificate struct {
		Status string `json:"status"`
		Issuer string `json:"issuer"`
	} `json:"certificate"`
}

// domainCall calls https://host:8443<path> the way hostStatus does (the lab's
// port 8443 on 127.0.0.1, the host as the server name), but with any method,
// optional basic-auth credentials, and without following a redirect. It
// returns the status, the headers and the start of the body. It never puts the
// credentials in its error.
func (g *gate) domainCall(host, method, path, user, pass string) (int, http.Header, string, error) {
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: host}, // routing only, as in hostStatus
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:8443")
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequest(method, "https://"+host+":8443"+path, nil)
	if err != nil {
		return 0, nil, "", err
	}
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, "", fmt.Errorf("%s https://%s%s: %w", method, host, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, resp.Header, string(raw), nil
}

// waitDomainStatus waits for the call to answer want and returns the headers.
func (g *gate) waitDomainStatus(t *testing.T, want int, within time.Duration, host, method, path, user, pass string) http.Header {
	t.Helper()
	var status int
	var header http.Header
	var lastErr error
	if !waitUntil(within, func() bool {
		status, header, _, lastErr = g.domainCall(host, method, path, user, pass)
		return lastErr == nil && status == want
	}) {
		t.Fatalf("%s https://%s%s (credentials %q) didn't answer %d within %v (last status %d, error %v)",
			method, host, path, user, want, within, status, lastErr)
	}
	return header
}

// domainRows reads listDomains for the app (fresh runs the checks first) and
// returns the rows by host.
func (g *gate) domainRows(t *testing.T, appID string, fresh bool) map[string]gateDomain {
	t.Helper()
	path := "/v1/domains?app=" + appID
	if fresh {
		path += "&fresh=true"
	}
	var list struct {
		Domains []gateDomain `json:"domains"`
	}
	g.getJSON(t, path, &list)
	rows := map[string]gateDomain{}
	for _, d := range list.Domains {
		rows[d.Host] = d
	}
	return rows
}

// domainAttention returns the needs-you items of the domain kinds, as
// (kind, domain, addressSeen).
func (g *gate) domainAttention(t *testing.T) [][3]string {
	t.Helper()
	var att struct {
		Items []struct {
			Kind        string `json:"kind"`
			Domain      string `json:"domain"`
			AddressSeen string `json:"addressSeen"`
		} `json:"items"`
	}
	g.getJSON(t, "/v1/attention", &att)
	var out [][3]string
	for _, it := range att.Items {
		if strings.HasPrefix(it.Kind, "domain_") {
			out = append(out, [3]string{it.Kind, it.Domain, it.AddressSeen})
		}
	}
	return out
}

// domainsStep (feature 023): the options a domain can carry and the states it
// can be in, the way the lab can reach them. A busybox app gate023 (not lab023: a
// leftover spike job in the lab holds lab023-web and its hosts) with seven
// hosts, then:
//
//  1. deploy, healthy; the deploy answer holds the Team-only password once;
//  2. checkDomain and listDomains: each host's state, router, DNS and
//     certificate (the lab's DNS is the injected resolver and Traefik's own
//     certificate), getDomain equals its row;
//  3. through the edge: the plain domain 200, the redirect's source 301 (GET)
//     or 308 with the path and query kept, Team only 401 and then 200, the
//     path domain answers only under its prefix;
//  4. rotateTeamPassword: the new password works and the old one stops;
//  5. the needs-you list names a domain pointing elsewhere; stopApp makes every
//     domain not_served and clears the item; startApp brings them back;
//  6. Settings › Certificates (warn window) and listCertificates;
//  7. a deploy token is refused on the domain routes, and may turn Team only
//     on for its own app (the password in that answer once);
//  8. no password or hash in any later answer, the job, or Caravana's log;
//  9. the app is deleted and its Team-only items leave Traefik's variable.
//
// The DNS side needs the lab's fake resolver, so the step skips on a real host
// (host.sh prove). dns_elsewhere needs the console's address
// (CARAVANA_PUBLIC_URL) to know which addresses are this host's; without it
// the step expects "unknown" for elsewhere.test and says so.
func (g *gate) domainsStep(t *testing.T) {
	if g.target.Public != nil {
		t.Skip("the domain states need the lab's fake resolver and its edge on 127.0.0.1:8443")
	}
	const id = "gate023"
	const (
		apex      = "gate023.localhost"
		www       = "www.gate023.localhost"
		plain     = "plain.gate023.localhost"
		team      = "team.gate023.localhost"
		team2     = "team2.gate023.localhost"
		pathHost  = "path.gate023.localhost"
		elsewhere = "elsewhere.test"
		nothing   = "nothing.invalid"
	)
	expectElsewhere := strings.TrimSpace(os.Getenv("CARAVANA_PUBLIC_URL")) != ""
	if !expectElsewhere {
		t.Log("CARAVANA_PUBLIC_URL isn't set: Caravana doesn't know this host's addresses, so the DNS of elsewhere.test is unknown here, not dns_elsewhere")
	}

	tokenID := ""
	remove := func() {
		if resp, err := g.apiRequest(http.MethodDelete, "/v1/apps/"+id, "", ""); err == nil {
			resp.Body.Close()
		}
		if tokenID != "" {
			if resp, err := g.apiRequest(http.MethodDelete, "/v1/tokens/"+tokenID, "", ""); err == nil {
				resp.Body.Close()
			}
		}
	}
	remove() // a leftover from an aborted run, if any
	waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool {
		_, _, err := g.target.Nomad.Jobs().Info(id, nil)
		return err != nil && strings.Contains(err.Error(), "404")
	})
	t.Cleanup(remove)

	web := actionsWeb("", 1)
	web["args"] = []string{"-c", "mkdir -p /www/api; echo ok > /www/index.html; echo ok > /www/api/x; exec httpd -f -p 8080 -h /www"}
	domains := func(extra ...map[string]any) []map[string]any {
		list := []map[string]any{
			{"host": plain, "port": "http"},
			{"host": www, "port": "http", "wwwRedirect": true},
			{"host": team, "port": "http", "teamOnly": true},
			{"host": pathHost, "port": "http", "path": "/api"},
			{"host": elsewhere, "port": "http"},
			{"host": nothing, "port": "http"},
		}
		return append(list, extra...)
	}
	deployWith := func(token string, list []map[string]any) (int, gateDeployment, map[string]string, string) {
		t.Helper()
		web["domains"] = list
		body := labApp(web)
		var status int
		var raw []byte
		if token == "" {
			status, raw = g.send(t, http.MethodPost, "/v1/apps/"+id+"/deployments", body)
		} else {
			status, raw = g.sendAs(t, token, http.MethodPost, "/v1/apps/"+id+"/deployments", body)
		}
		var d struct {
			gateDeployment
			TeamPasswords map[string]string `json:"teamPasswords"`
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatalf("deploy %s: %v: %.200s", id, err, redact(string(raw), d.TeamPasswords))
		}
		return status, d.gateDeployment, d.TeamPasswords, string(raw)
	}

	// 1. Deploy; the Team-only password comes once, in this answer.
	status, dep, passwords, _ := deployWith("", domains())
	if status != http.StatusAccepted || dep.ID == "" {
		t.Fatalf("deployApp %s = %d (%+v), want 202 with a deployment", id, status, dep)
	}
	teamPassword := passwords[team]
	if len(passwords) != 1 || teamPassword == "" {
		t.Fatalf("the deploy answer has %d teamPasswords (for team: %v), want exactly one, for %s", len(passwords), teamPassword != "", team)
	}
	if final := g.waitFinalOf(t, id, dep.ID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("deploy %s ended %s: %s", id, final.Status, final.Error)
	}
	g.waitHostAnswers(t, plain)

	// 2. Every host checked now; the list says what each is.
	hosts := []string{apex, www, plain, team, pathHost, elsewhere, nothing}
	for _, h := range hosts {
		raw := g.sendWant(t, http.StatusOK, http.MethodPost, "/v1/domains/check", string(mustJSON(t, map[string]string{"host": h, "app": id})))
		var c struct {
			Host string `json:"host"`
			DNS  struct {
				Status string `json:"status"`
			} `json:"dns"`
			Certificate struct {
				Status string `json:"status"`
			} `json:"certificate"`
		}
		if err := json.Unmarshal(raw, &c); err != nil || c.Host != h {
			t.Fatalf("checkDomain %s = %s", h, raw)
		}
	}
	rows := g.domainRows(t, id, false)
	routerOf := func(h string) string { return nomadjob.DomainRouterName(id, "web", h) }
	wantElsewhere := gateDomain{State: "dns_elsewhere"}
	if !expectElsewhere {
		wantElsewhere.State = "self_signed"
	}
	type want struct {
		state, router, path, redirectOf, dns, cert string
		www, team                                  bool
	}
	wants := map[string]want{
		plain: {state: "self_signed", router: id + "-web", dns: "ok", cert: "self"},
		www:   {state: "self_signed", router: routerOf(www), www: true, dns: "ok", cert: "self"},
		// The apex only redirects to www. Traefik's own certificate beats the
		// redirect in the row's words (edge.StateOf), so it reads self_signed.
		apex:      {state: "self_signed", router: routerOf(www), www: true, redirectOf: www, dns: "ok", cert: "self"},
		team:      {state: "self_signed", router: routerOf(team), team: true, dns: "ok", cert: "self"},
		pathHost:  {state: "self_signed", router: routerOf(pathHost), path: "/api", dns: "ok", cert: "self"},
		elsewhere: {state: wantElsewhere.State, router: id + "-web", dns: "elsewhere", cert: "self"},
		nothing:   {state: "dns_none", router: id + "-web", dns: "none", cert: "self"},
	}
	if !expectElsewhere {
		w := wants[elsewhere]
		w.dns = "unknown"
		wants[elsewhere] = w
	}
	if len(rows) != len(wants) {
		t.Errorf("listDomains has %d rows for %s, want %d: %v", len(rows), id, len(wants), slices.Sorted(mapKeys(rows)))
	}
	for h, w := range wants {
		r, ok := rows[h]
		if !ok {
			t.Errorf("listDomains has no row for %s", h)
			continue
		}
		if r.App != id || r.Service != "web" || r.State != w.state || r.Router != w.router || r.Path != w.path ||
			r.WWWRedirect != w.www || r.TeamOnly != w.team || r.RedirectOf != w.redirectOf ||
			r.DNS.Status != w.dns || r.Certificate.Status != w.cert {
			t.Errorf("the row of %s = %+v, want state %s, router %s, path %q, wwwRedirect %v, teamOnly %v, redirectOf %q, dns %s, certificate %s",
				h, r, w.state, w.router, w.path, w.www, w.team, w.redirectOf, w.dns, w.cert)
		}
		if w.cert == "self" && r.Certificate.Issuer != "Traefik's own" {
			t.Errorf("the certificate issuer of %s = %q, want Traefik's own", h, r.Certificate.Issuer)
		}
	}
	if expectElsewhere {
		if seen := rows[elsewhere].DNS.Seen; !slices.Equal(seen, []string{"203.0.113.9"}) {
			t.Errorf("dns.seen of %s = %v, want [203.0.113.9]", elsewhere, seen)
		}
	}
	// getDomain answers the row listDomains has.
	var list struct {
		Domains []map[string]any `json:"domains"`
	}
	g.getJSON(t, "/v1/domains?app="+id, &list)
	for _, row := range list.Domains {
		host, _ := row["host"].(string)
		var one map[string]any
		g.getJSON(t, "/v1/domains/"+url.PathEscape(host), &one)
		if !reflect.DeepEqual(one, row) {
			t.Errorf("getDomain %s = %v, listDomains has %v", host, one, row)
		}
	}
	g.refusal(t, http.StatusNotFound, "domain_not_found", "", http.MethodGet, "/v1/domains/nobody.gate023.localhost", "")

	// 3. Through the edge.
	g.waitDomainStatus(t, http.StatusOK, time.Minute, plain, http.MethodGet, "/", "", "")
	redirectTo := func(method string, wantStatuses ...int) {
		t.Helper()
		var status int
		var header http.Header
		var err error
		waitFor(t, "the redirect of "+apex, time.Minute, func() bool {
			status, header, _, err = g.domainCall(apex, method, "/some/path?a=1&b=two", "", "")
			return err == nil && status != http.StatusOK && status != http.StatusNotFound
		})
		loc := header.Get("Location")
		okLocation := loc == "https://"+www+":8443/some/path?a=1&b=two" || loc == "https://"+www+"/some/path?a=1&b=two"
		if !slices.Contains(wantStatuses, status) || !okLocation {
			t.Errorf("%s https://%s/some/path?a=1&b=two = %d, Location %q, want one of %v and https://%s[:8443]/some/path?a=1&b=two",
				method, apex, status, loc, wantStatuses, www)
		}
	}
	redirectTo(http.MethodGet, http.StatusMovedPermanently, http.StatusPermanentRedirect)
	redirectTo(http.MethodPost, http.StatusPermanentRedirect)
	// The target itself isn't redirected.
	g.waitDomainStatus(t, http.StatusOK, time.Minute, www, http.MethodGet, "/", "", "")

	header := g.waitDomainStatus(t, http.StatusUnauthorized, time.Minute, team, http.MethodGet, "/", "", "")
	if got := header.Get("WWW-Authenticate"); got != `Basic realm="caravana"` {
		t.Errorf("WWW-Authenticate of %s = %q, want Basic realm=\"caravana\"", team, got)
	}
	g.waitDomainStatus(t, http.StatusOK, 30*time.Second, team, http.MethodGet, "/", "team", teamPassword)
	if status, _, _, err := g.domainCall(team, http.MethodGet, "/", "team", teamPassword+"x"); err != nil || status != http.StatusUnauthorized {
		t.Errorf("%s with a wrong password = %d (%v), want 401", team, status, err)
	}

	g.waitDomainStatus(t, http.StatusOK, time.Minute, pathHost, http.MethodGet, "/api/x", "", "")
	g.waitDomainStatus(t, http.StatusNotFound, 30*time.Second, pathHost, http.MethodGet, "/other", "", "")

	// 4. A new password: it works, the old one stops once Traefik reads the change.
	var rotated struct {
		Host         string `json:"host"`
		User         string `json:"user"`
		TeamPassword string `json:"teamPassword"`
	}
	resp := g.api(t, http.MethodPost, "/v1/apps/"+id+"/domains/"+team+"/team-password", "", "")
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("rotateTeamPassword = %d (Cache-Control %q), want 200 no-store: %s", resp.StatusCode, resp.Header.Get("Cache-Control"), redact(string(raw), map[string]string{"": teamPassword}))
	}
	if err := json.Unmarshal(raw, &rotated); err != nil || rotated.Host != team || rotated.User != "team" || rotated.TeamPassword == "" || rotated.TeamPassword == teamPassword {
		t.Fatalf("rotateTeamPassword answered host %q, user %q, a password (%v, new: %v)", rotated.Host, rotated.User, rotated.TeamPassword != "", rotated.TeamPassword != teamPassword)
	}
	oldPassword, teamPassword := teamPassword, rotated.TeamPassword
	g.waitDomainStatus(t, http.StatusOK, 30*time.Second, team, http.MethodGet, "/", "team", teamPassword)
	g.waitDomainStatus(t, http.StatusUnauthorized, 30*time.Second, team, http.MethodGet, "/", "team", oldPassword)
	g.refusal(t, http.StatusConflict, "not_team_only", "", http.MethodPost, "/v1/apps/"+id+"/domains/"+plain+"/team-password", "")
	g.refusal(t, http.StatusNotFound, "domain_not_found", "", http.MethodPost, "/v1/apps/"+id+"/domains/nobody.gate023.localhost/team-password", "")

	// 5. The needs-you list, then stop and start.
	items := g.domainAttention(t)
	if expectElsewhere {
		if len(items) != 1 || items[0] != [3]string{"domain_dns_elsewhere", elsewhere, "203.0.113.9"} {
			t.Errorf("the domain items of /v1/attention = %v, want only domain_dns_elsewhere for %s seeing 203.0.113.9", items, elsewhere)
		}
	} else if len(items) != 0 {
		t.Errorf("the domain items of /v1/attention = %v, want none (the host's addresses are unknown)", items)
	}

	g.sendWant(t, http.StatusOK, http.MethodPost, "/v1/apps/"+id+"/stop", "")
	for h, r := range g.domainRows(t, id, false) {
		if r.State != "not_served" {
			t.Errorf("after stopApp the state of %s = %s, want not_served", h, r.State)
		}
	}
	if items := g.domainAttention(t); len(items) != 0 {
		t.Errorf("after stopApp the domain items of /v1/attention = %v, want none", items)
	}
	g.sendWant(t, http.StatusOK, http.MethodPost, "/v1/apps/"+id+"/start", "")
	waitFor(t, id+" to be healthy again", 3*time.Minute, func() bool { return g.appState(t, id) == "healthy" })
	g.waitDomainStatus(t, http.StatusOK, time.Minute, plain, http.MethodGet, "/", "", "")
	g.waitDomainStatus(t, http.StatusOK, 30*time.Second, team, http.MethodGet, "/", "team", teamPassword) // the rotated one survives
	rows = g.domainRows(t, id, true)
	for h, w := range wants {
		if rows[h].State != w.state {
			t.Errorf("after startApp and a fresh check the state of %s = %s, want %s", h, rows[h].State, w.state)
		}
	}
	if expectElsewhere {
		if items := g.domainAttention(t); len(items) != 1 || items[0][1] != elsewhere {
			t.Errorf("after startApp the domain items of /v1/attention = %v, want one for %s", items, elsewhere)
		}
	}

	// 6. Settings › Certificates.
	var settings struct {
		Issuer         string `json:"issuer"`
		WarnBeforeDays int    `json:"warnBeforeDays"`
		EdgeAddress    string `json:"edgeAddress"`
	}
	g.getJSON(t, "/v1/settings/certificates", &settings)
	wantIssuer := map[string]string{"": "unknown", "production": "letsencrypt", "staging": "letsencrypt-staging", "none": "none"}[os.Getenv("CARAVANA_ACME")]
	if wantIssuer == "" {
		wantIssuer = "unknown"
	}
	if settings.Issuer != wantIssuer || settings.EdgeAddress != "127.0.0.1:8443" {
		t.Errorf("getCertificateSettings = %+v, want issuer %s (CARAVANA_ACME=%q) and the lab's edge 127.0.0.1:8443", settings, wantIssuer, os.Getenv("CARAVANA_ACME"))
	}
	originalDays := settings.WarnBeforeDays
	t.Cleanup(func() {
		if resp, err := g.apiRequest(http.MethodPut, "/v1/settings/certificates", "application/json", fmt.Sprintf(`{"warnBeforeDays":%d}`, originalDays)); err == nil {
			resp.Body.Close()
		}
	})
	if originalDays != 14 {
		t.Errorf("warnBeforeDays starts at %d, want 14 (a step before this one left it changed?)", originalDays)
	}
	for _, days := range []int{7, 14} {
		raw := g.sendWant(t, http.StatusOK, http.MethodPut, "/v1/settings/certificates", fmt.Sprintf(`{"warnBeforeDays":%d}`, days))
		var got struct {
			WarnBeforeDays int `json:"warnBeforeDays"`
		}
		if err := json.Unmarshal(raw, &got); err != nil || got.WarnBeforeDays != days {
			t.Errorf("putCertificateSettings %d = %s", days, raw)
		}
		g.getJSON(t, "/v1/settings/certificates", &got)
		if got.WarnBeforeDays != days {
			t.Errorf("getCertificateSettings after putting %d = %d", days, got.WarnBeforeDays)
		}
	}
	g.refusal(t, http.StatusUnprocessableEntity, "invalid_field", "warnBeforeDays", http.MethodPut, "/v1/settings/certificates", `{"warnBeforeDays":5}`)
	var certs struct {
		Certificates []struct {
			Host, App, Service, Issuer, Status string
		} `json:"certificates"`
	}
	g.getJSON(t, "/v1/certificates", &certs)
	seen := map[string]bool{}
	for _, c := range certs.Certificates {
		if c.App == id {
			seen[c.Host] = true
			if c.Status != "self" || c.Issuer != "Traefik's own" {
				t.Errorf("listCertificates has %s as %s by %q, want self by Traefik's own", c.Host, c.Status, c.Issuer)
			}
		}
	}
	for _, h := range hosts {
		if !seen[h] {
			t.Errorf("listCertificates has no certificate for %s", h)
		}
	}

	// 7. A deploy token for this app: refused on the domain routes, allowed
	// to turn Team only on for its own app.
	raw = g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/tokens",
		`{"name":"gate023-ci","scope":{"project":"default","app":"`+id+`"}}`)
	var made struct{ ID, Token string }
	if err := json.Unmarshal(raw, &made); err != nil || made.Token == "" {
		t.Fatalf("createToken with a scope: %v (token made: %v)", err, made.Token != "")
	}
	tokenID = made.ID
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/domains", ""},
		{http.MethodPost, "/v1/domains/check", `{"host":"` + plain + `"}`},
		{http.MethodPost, "/v1/apps/" + id + "/domains/" + team + "/team-password", ""},
		{http.MethodGet, "/v1/settings/certificates", ""},
	} {
		status, raw := g.sendAs(t, made.Token, c.method, c.path, c.body)
		var e struct {
			Error struct{ Code string } `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		if status != http.StatusForbidden || e.Error.Code != "token_scope" {
			t.Errorf("a deploy token on %s %s = %d %s, want 403 token_scope", c.method, c.path, status, e.Error.Code)
		}
	}
	status, dep2, passwords2, _ := deployWith(made.Token, domains(map[string]any{"host": team2, "port": "http", "teamOnly": true}))
	team2Password := passwords2[team2]
	if status != http.StatusAccepted || len(passwords2) != 1 || team2Password == "" {
		t.Fatalf("a deploy token's deploy turning Team only on = %d with %d teamPasswords (for %s: %v), want 202 and one, for %s",
			status, len(passwords2), team2, team2Password != "", team2)
	}
	if final := g.waitFinalOf(t, id, dep2.ID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("the deploy token's deploy ended %s: %s", final.Status, final.Error)
	}
	g.waitDomainStatus(t, http.StatusUnauthorized, time.Minute, team2, http.MethodGet, "/", "", "")
	g.waitDomainStatus(t, http.StatusOK, 30*time.Second, team2, http.MethodGet, "/", "team", team2Password)
	g.waitDomainStatus(t, http.StatusOK, 30*time.Second, team, http.MethodGet, "/", "team", teamPassword) // a deploy keeps the rotated one

	// 8. No password and no hash in any later answer, the job, or the log.
	secrets := []string{oldPassword, teamPassword, team2Password}
	answers := map[string]string{}
	read := func(path string) {
		t.Helper()
		answers[path] = string(g.sendWant(t, http.StatusOK, http.MethodGet, path, ""))
	}
	for _, path := range []string{
		"/v1/apps/" + id, "/v1/apps/" + id + "/deployments", "/v1/apps/" + id + "/deployments/" + dep.ID,
		"/v1/apps/" + id + "/deployments/" + dep2.ID, "/v1/apps/" + id + "/jobspec",
		"/v1/apps/" + id + "/deployments/" + dep.ID + "/jobspec", "/v1/apps/" + id + "/actions",
		"/v1/domains", "/v1/domains/" + team, "/v1/domains/" + team2, "/v1/certificates", "/v1/attention", "/v1/apps",
	} {
		read(path)
	}
	for _, h := range hosts {
		answers["checkDomain "+h] = string(g.sendWant(t, http.StatusOK, http.MethodPost, "/v1/domains/check", string(mustJSON(t, map[string]string{"host": h, "app": id}))))
	}
	job, _, err := g.target.Nomad.Jobs().Info(id, nil)
	if err != nil {
		t.Fatalf("read the job %s: %v", id, err)
	}
	answers["the Nomad job"] = string(mustJSON(t, job))
	if g.target.ServerLog != nil {
		answers["Caravana's log"] = g.target.ServerLog()
	}
	for where, text := range answers {
		for _, hash := range []string{"$2a$", "$2b$", "$2y$"} {
			if strings.Contains(text, hash) {
				t.Errorf("%s holds a bcrypt hash (%s…)", where, hash)
			}
		}
		for i, secret := range secrets {
			if strings.Contains(text, secret) {
				t.Errorf("%s holds Team-only password number %d", where, i+1)
			}
		}
	}

	// 9. Delete the app: its Team-only items leave Traefik's variable.
	g.sendWant(t, http.StatusNoContent, http.MethodDelete, "/v1/apps/"+id, "")
	waitFor(t, "gate023's items to leave nomad/jobs/traefik", time.Minute, func() bool {
		v, _, err := g.target.Nomad.Variables().Read("nomad/jobs/traefik", nil)
		if err != nil {
			return true // no variable, nothing of ours in it
		}
		for name := range v.Items {
			if strings.Contains(name, id) {
				return false
			}
		}
		return true
	})
}

// redact hides the passwords in a text that goes into a failure message.
func redact(text string, passwords map[string]string) string {
	for _, p := range passwords {
		if p != "" {
			text = strings.ReplaceAll(text, p, "<password>")
		}
	}
	return text
}

// mapKeys is the keys of m, as an iterator for slices.Sorted.
func mapKeys[V any](m map[string]V) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}
