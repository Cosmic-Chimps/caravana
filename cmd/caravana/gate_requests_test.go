package main

// The requests gate step (feature 047, plan "Phase C"), through the API with
// the gate's token. It is in its own file only because gate_test.go is long;
// runGate lists it after logs. The app and its requests are the console step's
// (console_requests_test.go: requestsWeb, requestsEdge).

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gateRequest is a request of listRequests (api/openapi.yaml RequestEntry).
type gateRequest struct {
	Time        string  `json:"time"`
	App         string  `json:"app"`
	Service     string  `json:"service"`
	Method      string  `json:"method"`
	Host        string  `json:"host"`
	Path        string  `json:"path"`
	Status      int     `json:"status"`
	StatusClass string  `json:"statusClass"`
	DurationMs  float64 `json:"durationMs"`
	Bytes       int     `json:"bytes"`
	Client      string  `json:"client"`
}

// gateRequestRead is the part of a RequestRead this step reads.
type gateRequestRead struct {
	AccessLog bool          `json:"accessLog"`
	Requests  []gateRequest `json:"requests"`
	Total     int           `json:"total"`
	Coverage  struct {
		From     string `json:"from"`
		Complete bool   `json:"complete"`
	} `json:"coverage"`
	Histogram json.RawMessage `json:"histogram"`
	Services  json.RawMessage `json:"services"`
}

// requestsStep: listRequests against one busybox app, gate047<hex>, with a domain.
//
//  1. a GET of /?token=<sentinel>, a POST to /cgi-bin/things (201) and a GET
//     of /cgi-bin/boom (500) go through Traefik, and within 20 s
//     listRequests?app=<id> has the three, newest first, each with its
//     method, host, path alone, status, statusClass, durationMs and client,
//     accessLog true and a coverage;
//  2. the filters: status=5xx is the boom row alone, path=/cgi-bin/t the POST,
//     method=post (any case) the POST, minDuration above every duration
//     nothing, a service of the app and lines=1;
//  3. the refusals are 422 invalid_field naming the field: status=6xx, path
//     without its slash, minDuration=-1, range=2h, a from after its to, range
//     with from, lines=0; an app that isn't one is 404;
//  4. a deploy token is 403 token_scope, and so is no token's 401;
//  5. the sentinel is in no answer, no line of Caravana's log and no file of
//     its data directory.
//
// A failure message never holds the sentinel.
func (g *gate) requestsStep(t *testing.T) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	// Traefik's log keeps the requests of earlier runs, and a request belongs to
	// the app ID it names (also an ID made again), so each run has an app of its
	// own and counts only its own three requests.
	id := "gate047" + hex.EncodeToString(raw[:2])
	sentinel := "s3cret-sentinel-047-gate-" + hex.EncodeToString(raw[:])
	hide := func(s string) string { return strings.ReplaceAll(s, sentinel, "<sentinel>") }
	fatalf := func(format string, args ...any) {
		t.Helper()
		t.Fatal(hide(fmt.Sprintf(format, args...)))
	}
	errorf := func(format string, args ...any) {
		t.Helper()
		t.Error(hide(fmt.Sprintf(format, args...)))
	}

	// Every answer, for the sentinel search at the end.
	var answers []string
	get := func(path string) (int, []byte) {
		t.Helper()
		status, body := g.send(t, http.MethodGet, path, "")
		answers = append(answers, string(body))
		return status, body
	}
	read := func(query string) gateRequestRead {
		t.Helper()
		path := "/v1/requests?app=" + id + query
		status, body := get(path)
		if status != http.StatusOK {
			fatalf("GET %s = %d, want 200: %.300s", path, status, body)
		}
		var r gateRequestRead
		if err := json.Unmarshal(body, &r); err != nil {
			fatalf("GET %s: %v: %.300s", path, err, body)
		}
		return r
	}
	paths := func(r gateRequestRead) []string {
		var out []string
		for _, q := range r.Requests {
			out = append(out, q.Method+" "+q.Path)
		}
		return out
	}

	remove := func() {
		if resp, err := g.apiRequest(http.MethodDelete, "/v1/apps/"+id, "", ""); err == nil {
			resp.Body.Close()
		}
	}
	t.Cleanup(remove)

	// 1. Deploy, then the three requests.
	host := g.extraHost(id)
	status, body := g.send(t, http.MethodPost, "/v1/apps/"+id+"/deployments", labApp(requestsWeb(host)))
	var d gateDeployment
	_ = json.Unmarshal(body, &d)
	if status != http.StatusAccepted || d.ID == "" {
		fatalf("deployApp %s = %d: %.300s", id, status, body)
	}
	if final := g.waitFinalOf(t, id, d.ID, 4*time.Minute); final.Status != "healthy" {
		fatalf("deploy %s ended %s: %s", id, final.Status, final.Error)
	}
	g.waitDomainStatus(t, http.StatusOK, 90*time.Second, host, http.MethodGet, "/", "", "")
	g.requestsEdge(t, host, sentinel)

	var all gateRequestRead
	var boom, things gateRequest
	var haveBoom, haveThings bool
	if !waitUntil(20*time.Second, func() bool {
		status, body := get("/v1/requests?app=" + id)
		if status != http.StatusOK || json.Unmarshal(body, &all) != nil {
			return false
		}
		haveBoom, haveThings = false, false
		for _, q := range all.Requests {
			switch q.Path {
			case "/cgi-bin/boom":
				boom, haveBoom = q, true
			case "/cgi-bin/things":
				things, haveThings = q, true
			}
		}
		return haveBoom && haveThings
	}) {
		fatalf("listRequests didn't show the three requests within 20 s: %v", paths(all))
	}
	if !all.AccessLog {
		errorf("listRequests says accessLog false, but Traefik writes it")
	}
	if all.Coverage.From == "" {
		errorf("listRequests has no coverage.from")
	}
	if boom.Method != "GET" || boom.Status != 500 || boom.StatusClass != "5xx" || boom.Host != host || boom.App != id || boom.Service != "web" {
		errorf("the boom request is %+v, want GET %s 500 5xx of %s/web", boom, host, id)
	}
	if things.Method != "POST" || things.Status != 201 || things.StatusClass != "2xx" {
		errorf("the POST request is %+v, want POST 201 2xx", things)
	}
	if boom.Client == "" || boom.DurationMs <= 0 || boom.Time == "" {
		errorf("the boom request lacks a client, a duration or a time: %+v", boom)
	}
	indexSeen := false
	for i, q := range all.Requests {
		if strings.Contains(q.Path, "?") || strings.Contains(q.Path, "token") {
			errorf("a request's path is %q: Traefik records the path alone", q.Path)
		}
		if q.Path == "/" && q.Status == 200 {
			indexSeen = true
		}
		if i > 0 && q.Time > all.Requests[i-1].Time {
			errorf("listRequests isn't newest first: %s is after %s", q.Time, all.Requests[i-1].Time)
		}
	}
	if !indexSeen {
		errorf("listRequests has no GET / answered 200: %v", paths(all))
	}
	if all.Total < 3 || len(all.Histogram) == 0 || len(all.Services) == 0 {
		errorf("listRequests total %d, histogram %d bytes, services %d bytes: want three requests and both", all.Total, len(all.Histogram), len(all.Services))
	}

	// 2. Filters.
	if r := read("&status=5xx"); len(r.Requests) != 1 || r.Requests[0].Path != "/cgi-bin/boom" {
		errorf("status=5xx = %v, want only the boom request", paths(r))
	}
	if r := read("&path=/cgi-bin/t"); len(r.Requests) != 1 || r.Requests[0].Path != "/cgi-bin/things" {
		errorf("path=/cgi-bin/t = %v, want only the POST", paths(r))
	}
	if r := read("&method=post"); len(r.Requests) != 1 || r.Requests[0].Method != "POST" {
		errorf("method=post = %v, want only the POST", paths(r))
	}
	if r := read("&status=2xx&path=/cgi-bin"); len(r.Requests) != 1 || r.Requests[0].Path != "/cgi-bin/things" {
		errorf("status=2xx&path=/cgi-bin = %v, want only the POST", paths(r))
	}
	if r := read("&status=4xx"); len(r.Requests) != 0 {
		errorf("status=4xx = %v, want none", paths(r))
	}
	if r := read(fmt.Sprintf("&minDuration=%d", int(boom.DurationMs)+3600000)); len(r.Requests) != 0 {
		errorf("minDuration above every duration = %v, want none", paths(r))
	}
	if r := read("&minDuration=0"); len(r.Requests) < 3 {
		errorf("minDuration=0 = %v, want every request", paths(r))
	}
	if r := read("&service=" + url.QueryEscape(id+"/web") + "&lines=1"); len(r.Requests) != 1 || r.Requests[0].Path != "/cgi-bin/boom" {
		errorf("service=<id>/web&lines=1 = %v, want the newest request alone", paths(r))
	}
	if r := read("&client=" + url.QueryEscape(boom.Client)); len(r.Requests) < 3 {
		errorf("client=%s = %v, want every request of the gate's one client", boom.Client, paths(r))
	}
	if r := read("&client=203.0.113.77"); len(r.Requests) != 0 {
		errorf("client=203.0.113.77 = %v, want none", paths(r))
	}
	from := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	if r := read("&from=" + from); len(r.Requests) < 3 {
		errorf("from=%s = %v, want the three requests", from, paths(r))
	}

	// 3. Refusals, each naming its field; the 404 for an app that isn't one.
	for _, c := range []struct{ query, field string }{
		{"&status=6xx", "status"},
		{"&path=nope", "path"},
		{"&minDuration=-1", "minDuration"},
		{"&range=2h", "range"},
		{"&lines=0", "lines"},
		{"&from=2026-11-20T10:00:00Z&to=2026-11-20T09:00:00Z", ""},
		{"&range=1h&from=2026-11-20T09:00:00Z", ""},
		{"&from=yesterday", "from"},
		{"&method=" + strings.Repeat("g", 30), "method"},
	} {
		path := "/v1/requests?app=" + id + c.query
		status, body := get(path)
		var e struct {
			Error struct{ Code, Message, Field string } `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		if status != http.StatusUnprocessableEntity || e.Error.Code != "invalid_field" || e.Error.Message == "" || (c.field != "" && e.Error.Field != c.field) || e.Error.Field == "" {
			errorf("GET %s = %d %s (field %q), want 422 invalid_field naming %q: %.200s", path, status, e.Error.Code, e.Error.Field, c.field, body)
		}
	}
	if status, body := get("/v1/requests?app=nosuchapp047"); status != http.StatusNotFound {
		errorf("GET /v1/requests?app=nosuchapp047 = %d, want 404: %.200s", status, body)
	}
	if status, body := get("/v1/requests?project=nosuchproject047"); status != http.StatusNotFound {
		errorf("GET /v1/requests?project=nosuchproject047 = %d, want 404: %.200s", status, body)
	}

	// 4. A deploy token and no token.
	tokenRaw := g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/tokens",
		`{"name":"`+id+`-deploy","scope":{"project":"default","app":"`+id+`"}}`)
	var made struct{ ID, Token string }
	if err := json.Unmarshal(tokenRaw, &made); err != nil || made.Token == "" {
		fatalf("createToken with a scope = %s (%v)", tokenRaw, err)
	}
	t.Cleanup(func() {
		if resp, err := g.apiRequest(http.MethodDelete, "/v1/tokens/"+made.ID, "", ""); err == nil {
			resp.Body.Close()
		}
	})
	for _, path := range []string{"/v1/requests", "/v1/requests?app=" + id} {
		status, body := g.sendAs(t, made.Token, http.MethodGet, path, "")
		var e struct {
			Error struct{ Code string } `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		if status != http.StatusForbidden || e.Error.Code != "token_scope" {
			errorf("GET %s with a deploy token = %d %s, want 403 token_scope", path, status, body)
		}
		answers = append(answers, string(body))
	}
	resp, err := g.client().Get(g.target.API + "/v1/requests?app=" + id)
	if err != nil {
		fatalf("GET /v1/requests without a token: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		errorf("GET /v1/requests without a token = %d, want 401", resp.StatusCode)
	}
	if resp, err := g.apiRequest(http.MethodGet, "/v1/requests?app="+id, "", ""); err != nil {
		fatalf("GET /v1/requests: %v", err)
	} else {
		resp.Body.Close()
		if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
			errorf("listRequests Cache-Control = %q, want no-store", cc)
		}
	}

	// 5. The sentinel is nowhere Caravana keeps or shows anything.
	for _, p := range []string{"/v1/logs?app=" + id, "/v1/apps/" + id, "/v1/apps/" + id + "/deployments", "/v1/apps/" + id + "/jobspec", "/v1/apps", "/v1/attention"} {
		status, body := get(p)
		if status != http.StatusOK {
			errorf("GET %s = %d, want 200", p, status)
		}
		_ = body
	}
	for i, a := range answers {
		if strings.Contains(a, sentinel) {
			errorf("the sentinel (a query string) is in an answer (#%d)", i)
		}
	}
	if g.target.ServerLog != nil && strings.Contains(g.target.ServerLog(), sentinel) {
		errorf("the sentinel is in Caravana's log")
	}
	if g.target.DataDir != "" {
		files, _ := filepath.Glob(filepath.Join(g.target.DataDir, "*"))
		for _, f := range files {
			if b, err := os.ReadFile(f); err == nil && strings.Contains(string(b), sentinel) {
				errorf("the sentinel is in the data file %s", filepath.Base(f))
			}
		}
	}
}
