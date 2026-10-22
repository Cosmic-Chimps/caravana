package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app/apptest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// TestNoSecretReachesLogsOrResponses is SC-005: a password, an API token, a
// webhook token and a session cookie go through the CLI, the API and the
// pages, successfully and not, and none of them shows up in the server's log
// or in any response, except in the response that creates it.
func TestNoSecretReachesLogsOrResponses(t *testing.T) {
	const (
		password    = "first-password-that-is-long"
		newPassword = "second-password-that-is-long"
		wrongPass   = "wrong-password-that-is-long"
	)
	dir := t.TempDir()

	// The webhook token needs a stored app. Seed it before the server holds the
	// store, so the test needs no registry and no Nomad.
	seed, err := store.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := seed.SaveApp(context.Background(), apptest.Whoami().App, nil); err != nil {
		t.Fatalf("seed app: %v", err)
	}
	seed.Close()

	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	var serverLog syncBuffer
	exit := make(chan int, 1)
	go func() { exit <- runServer(ctx, []string{"-listen", addr, "-data-dir", dir}, noEnv, &serverLog) }()
	t.Cleanup(func() {
		cancel()
		<-exit
	})
	waitHealthy(t, "http://"+addr)
	base := "http://" + addr

	// secrets maps a name to a value no response or log line may hold.
	secrets := map[string]string{
		"password": password, "new password": newPassword, "wrong password": wrongPass,
		"wrong API token":     "cvat_" + strings.Repeat("W", 43),
		"wrong webhook token": "cvwh_" + strings.Repeat("V", 43),
	}
	// seen is every response and CLI output, by label, with the secrets that
	// label is allowed to hold: the response that creates a thing shows it.
	type output struct {
		text    string
		allowed []string
	}
	seen := map[string]output{}
	record := func(label, text string, allowed ...string) { seen[label] = output{text, allowed} }

	// --- The CLI, as the owner on the host. ---
	var out, errOut bytes.Buffer
	cli := func(label, stdin string, args ...string) string {
		t.Helper()
		out.Reset()
		errOut.Reset()
		code := runAdmin(ctx, append(args, "-data-dir", dir), noEnv, strings.NewReader(stdin), &out, &errOut)
		if code != 0 {
			t.Fatalf("%s exited %d: %s", label, code, errOut.String())
		}
		return out.String()
	}
	cli("init", password+"\n", "init", "--email", "owner@example.com")
	record("cli init", out.String()+errOut.String())
	apiToken := strings.TrimSpace(cli("create-token", "", "create-token", "--name", "secrets"))
	secrets["API token"] = apiToken
	record("cli create-token stdout", out.String(), "API token") // the creation response
	record("cli create-token stderr", errOut.String())
	cli("status", "", "status")
	record("cli status", out.String()+errOut.String())

	// call sends a request and records what came back.
	call := func(label, method, path string, headers map[string]string, form url.Values, body string, allowed ...string) (*http.Response, string) {
		t.Helper()
		var reader io.Reader = strings.NewReader(body)
		if form != nil {
			reader = strings.NewReader(form.Encode())
		}
		req, err := http.NewRequest(method, base+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		// Headers count too, except the cookie the sign-in sets (checked below).
		var headerText strings.Builder
		for k, vs := range resp.Header {
			if k == "Set-Cookie" {
				continue
			}
			headerText.WriteString(k + ": " + strings.Join(vs, ",") + "\n")
		}
		record(label, headerText.String()+string(raw), allowed...)
		return resp, string(raw)
	}
	bearer := func(token string) map[string]string { return map[string]string{"Authorization": "Bearer " + token} }
	jsonBody := map[string]string{"Content-Type": "application/json"}
	withBearer := func(token string) map[string]string {
		h := bearer(token)
		h["Content-Type"] = "application/json"
		return h
	}

	// --- The API. ---
	call("api wrong token", http.MethodGet, "/v1/apps/whoami", bearer(secrets["wrong API token"]), nil, "")
	call("api no token", http.MethodGet, "/v1/apps/whoami", nil, nil, "")
	call("api get app", http.MethodGet, "/v1/apps/whoami", bearer(apiToken), nil, "")

	_, raw := call("api create webhook token", http.MethodPost, "/v1/apps/whoami/webhook", bearer(apiToken), nil, "", "webhook token")
	secrets["webhook token"] = regexp.MustCompile(`cvwh_[A-Za-z0-9_-]{43}`).FindString(raw)
	if secrets["webhook token"] == "" {
		t.Fatalf("no webhook token in %s", raw)
	}
	call("api webhook status", http.MethodGet, "/v1/apps/whoami/webhook", bearer(apiToken), nil, "")
	// A valid webhook token with a body too big to read: it passes the token
	// check and stops before any deploy, so it needs no registry or Nomad.
	big := strings.Repeat("x", 2<<20)
	call("webhook valid token in the query", http.MethodPost, "/v1/webhooks/whoami?token="+secrets["webhook token"], nil, nil, big)
	call("webhook valid token in the header", http.MethodPost, "/v1/webhooks/whoami", bearer(secrets["webhook token"]), nil, big)
	call("webhook wrong token", http.MethodPost, "/v1/webhooks/whoami?token="+secrets["wrong webhook token"], nil, nil, "{}")

	_, raw = call("api create token", http.MethodPost, "/v1/tokens", withBearer(apiToken), nil, `{"name":"made-by-api"}`, "API token 2")
	secrets["API token 2"] = regexp.MustCompile(`cvat_[A-Za-z0-9_-]{43}`).FindString(raw)
	call("api list tokens", http.MethodGet, "/v1/tokens", bearer(apiToken), nil, "")
	call("api wrong current password", http.MethodPut, "/v1/account/password", withBearer(apiToken), nil,
		`{"currentPassword":"`+wrongPass+`","newPassword":"`+newPassword+`"}`)
	call("api bad body", http.MethodPut, "/v1/account/password", jsonBody, nil, `{"currentPassword":"`+password+`"`)

	// --- The pages. ---
	call("login page", http.MethodGet, "/login", nil, nil, "")
	call("login wrong password", http.MethodPost, "/login", nil,
		url.Values{"email": {"owner@example.com"}, "password": {wrongPass}}, "")
	resp, _ := call("login", http.MethodPost, "/login", nil,
		url.Values{"email": {"owner@example.com"}, "password": {password}}, "")
	var session string
	for _, c := range resp.Cookies() {
		if c.Name == "caravana_session" {
			session = c.Value
		}
	}
	if session == "" {
		t.Fatal("sign-in set no session cookie")
	}
	secrets["session"] = session
	cookie := map[string]string{"Cookie": "caravana_session=" + session}

	call("account page", http.MethodGet, "/settings", cookie, nil, "")
	call("registries page", http.MethodGet, "/settings/registries", cookie, nil, "")
	call("backup storage page", http.MethodGet, "/settings/backup-storage", cookie, nil, "")
	call("notifications page", http.MethodGet, "/settings/notifications", cookie, nil, "")
	call("tokens page", http.MethodGet, "/settings/tokens", cookie, nil, "")
	_, raw = call("ui create token", http.MethodPost, "/settings/tokens", cookie, url.Values{"name": {"made-by-ui"}}, "", "API token 3")
	secrets["API token 3"] = regexp.MustCompile(`cvat_[A-Za-z0-9_-]{43}`).FindString(raw)
	call("account with the token listed", http.MethodGet, "/settings/tokens", cookie, nil, "")
	call("ui wrong current password", http.MethodPost, "/settings/password", cookie,
		url.Values{"currentPassword": {wrongPass}, "newPassword": {newPassword}}, "")
	call("api with the session cookie", http.MethodGet, "/v1/tokens", cookie, nil, "")
	call("ui change password", http.MethodPost, "/settings/password", cookie,
		url.Values{"currentPassword": {password}, "newPassword": {newPassword}}, "")
	call("ui sign out", http.MethodPost, "/logout", cookie, nil, "")
	call("account page after sign-out", http.MethodGet, "/settings", cookie, nil, "")

	cli("reset-password", "a-reset-password-that-is-long\n", "reset-password")
	record("cli reset-password", out.String()+errOut.String())
	secrets["reset password"] = "a-reset-password-that-is-long"
	cli("revoke-token", "", "revoke-token", "--name", "secrets")
	record("cli revoke-token", out.String()+errOut.String())

	// --- Nothing leaked. ---
	for k, v := range secrets {
		if v == "" {
			t.Fatalf("secret %q was never captured", k)
		}
	}
	names := make([]string, 0, len(secrets))
	for name := range secrets {
		names = append(names, name)
	}
	sort.Strings(names)

	logText := serverLog.String()
	for _, name := range names {
		if strings.Contains(logText, secrets[name]) {
			t.Errorf("the %s reached the server log", name)
		}
	}
	for label, o := range seen {
		for _, name := range names {
			if strings.Contains(o.text, secrets[name]) && !contains(o.allowed, name) {
				t.Errorf("the %s appears in %q", name, label)
			}
		}
	}
	if !strings.Contains(logText, `"event":"signin"`) || !strings.Contains(logText, `"event":"token_created"`) {
		t.Errorf("the log lacks the security events this test should have produced:\n%s", logText)
	}
}

func contains(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}
