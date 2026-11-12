package ui

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/httpx"
	"github.com/cosmic-chimps/caravana/internal/webauthntest"
)

// Feature 033: passkeys in the console. The tests play the browser's script with the software
// authenticator: they ask for options, let the authenticator answer, and post the form the script
// would, all over the console's own routes.

// browser is one visitor reaching the test server as localhost:<port>, the way a browser at
// http://localhost:7070 does: a secure context, so passkeys work there.
type browser struct {
	t      *testing.T
	e      *uiEnv
	host   string // the Host header
	origin string // what an authenticator signs for
	cookie *http.Cookie
}

func newBrowser(t *testing.T, e *uiEnv) *browser {
	t.Helper()
	u, err := url.Parse(e.URL)
	if err != nil {
		t.Fatal(err)
	}
	host := "localhost:" + u.Port()
	return &browser{t: t, e: e, host: host, origin: "http://" + host}
}

func (b *browser) do(r request) (*http.Response, string) {
	b.t.Helper()
	r.host = b.host
	if b.cookie != nil && r.cookies == nil {
		r.cookies = []*http.Cookie{b.cookie}
	}
	resp, body := b.e.do(b.t, r)
	return resp, body
}

func (b *browser) post(path string, form url.Values) (*http.Response, string) {
	b.t.Helper()
	return b.do(request{method: http.MethodPost, path: path, form: form})
}

// signInWithPassword signs in with the admin's password and keeps the cookie.
func (b *browser) signInWithPassword() {
	b.t.Helper()
	resp, body := b.post("/login", url.Values{"email": {testEmail}, "password": {testPassword}})
	if resp.StatusCode != http.StatusSeeOther {
		b.t.Fatalf("password sign-in = %d:\n%s", resp.StatusCode, text(body))
	}
	b.cookie = sessionCookie(b.t, resp)
}

func sessionCookie(t *testing.T, resp *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range resp.Cookies() {
		if strings.HasSuffix(c.Name, "caravana_session") {
			return c
		}
	}
	t.Fatal("no session cookie was set")
	return nil
}

// options asks for a ceremony, as the script does, and returns it with the publicKey object.
func (b *browser) options(path string) (ceremony string, publicKey []byte) {
	b.t.Helper()
	resp, body := b.do(request{method: http.MethodPost, path: path, headers: map[string]string{"Accept": "application/json"}})
	if resp.StatusCode != http.StatusOK {
		b.t.Fatalf("POST %s = %d: %s", path, resp.StatusCode, body)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		b.t.Errorf("POST %s: Cache-Control = %q, want no-store", path, got)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		b.t.Errorf("POST %s: Content-Type = %q", path, got)
	}
	var out struct {
		Ceremony  string          `json:"ceremony"`
		PublicKey json.RawMessage `json:"publicKey"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil || out.Ceremony == "" || len(out.PublicKey) == 0 {
		b.t.Fatalf("POST %s answered %s, want {ceremony, publicKey}", path, body)
	}
	return out.Ceremony, out.PublicKey
}

// addPasskey adds a passkey from a confirmed session, as Settings › Sign-in does.
func (b *browser) addPasskey(name string) *webauthntest.Authenticator {
	b.t.Helper()
	ceremony, key := b.options("/settings/passkeys/options")
	a := webauthntest.New("localhost")
	posted, err := a.Create(key, b.origin)
	if err != nil {
		b.t.Fatal(err)
	}
	resp, body := b.post("/settings/passkeys", url.Values{"name": {name}, "ceremony": {ceremony}, "credential": {string(posted)}})
	if resp.StatusCode != http.StatusSeeOther || location(b.t, resp) != "/settings/sign-in?added=1" {
		b.t.Fatalf("add a passkey = %d to %q:\n%s", resp.StatusCode, location(b.t, resp), text(body))
	}
	return a
}

// passkeySignIn posts what the sign-in script posts, for an answer made by a, and returns the response.
func (b *browser) passkeySignIn(a *webauthntest.Authenticator, next string) (*http.Response, string) {
	b.t.Helper()
	ceremony, key := b.options("/login/passkey/options")
	posted, err := a.Get(key, b.origin)
	if err != nil {
		b.t.Fatal(err)
	}
	return b.post("/login/passkey", url.Values{"ceremony": {ceremony}, "credential": {string(posted)}, "next": {next}})
}

// signedInBrowser is a browser that has signed in with the password.
func signedInBrowser(t *testing.T) (*browser, *uiEnv) {
	t.Helper()
	e := newUIEnv(t, true, "")
	b := newBrowser(t, e)
	b.signInWithPassword()
	return b, e
}

func TestPasskeyPlace(t *testing.T) {
	e := newUIEnv(t, true, "")
	s, err := newServer(Deps{Auth: e.Auth, Log: slogDiscard(), Now: e.Clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		host      string
		https     bool
		public    string
		available bool
		id        string
		origin    string
		console   string
		why       string // a part of the sentence, for an unavailable place
	}{
		{"an https domain", "console.example.com", true, "", true, "console.example.com", "https://console.example.com", "console.example.com", ""},
		{"the port stays in the origin only", "Console.Example.com:8443", true, "", true, "console.example.com", "https://console.example.com:8443", "console.example.com", ""},
		{"localhost over http", "localhost:7070", false, "", true, "localhost", "http://localhost:7070", "localhost", ""},
		{"a .localhost name over http", "caravana.localhost:8443", false, "", true, "caravana.localhost", "http://caravana.localhost:8443", "caravana.localhost", ""},
		{"an IPv4 address", "127.0.0.1:7070", false, "", false, "127.0.0.1", "http://127.0.0.1:7070", "127.0.0.1", "http://localhost:7070"},
		{"an IPv4 address over https", "203.0.113.9", true, "", false, "203.0.113.9", "https://203.0.113.9", "203.0.113.9", "http://localhost:7070"},
		{"IPv6 loopback", "[::1]:7070", false, "", false, "::1", "http://[::1]:7070", "::1", "http://localhost:7070"},
		{"a domain over plain http", "console.example.com", false, "", false, "console.example.com", "http://console.example.com", "console.example.com", "https domain"},
		{"a domain that is not plain ASCII", "bücher.example.com", true, "", false, "bücher.example.com", "https://bücher.example.com", "bücher.example.com", "https domain"},
		{"the public address's host", "console.example.com", true, "https://console.example.com", true, "console.example.com", "https://console.example.com", "console.example.com", ""},
		{"another host while the public address is set", "other.example.com", true, "https://console.example.com", false, "other.example.com", "https://other.example.com", "console.example.com", "https://console.example.com"},
		{"localhost while the public address is set", "localhost:7070", false, "https://console.example.com", true, "localhost", "http://localhost:7070", "console.example.com", ""},
		{"the console's domain is the public host, not the request's", "caravana.localhost", false, "https://Console.Example.com:8443/", true, "caravana.localhost", "http://caravana.localhost", "console.example.com", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s.publicURL = func() string { return tc.public }
			r := &http.Request{Host: tc.host, Header: http.Header{}}
			if tc.https {
				r.TLS = &tls.ConnectionState{}
			}
			got := s.passkeyPlace(r)
			if got.Available != tc.available || got.RP.ID != tc.id || got.RP.Origin != tc.origin || got.Console != tc.console {
				t.Errorf("passkeyPlace = %+v, want available %v, id %q, origin %q, console %q", got, tc.available, tc.id, tc.origin, tc.console)
			}
			if tc.available && got.Why != "" {
				t.Errorf("an available place says %q", got.Why)
			}
			if !tc.available && !strings.Contains(got.Why, tc.why) {
				t.Errorf("why = %q, want it to hold %q", got.Why, tc.why)
			}
		})
	}

	t.Run("a trusted proxy's https counts", func(t *testing.T) {
		trusted, err := httpx.ParseProxies("127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		s, err := newServer(Deps{Auth: e.Auth, Proxies: trusted, Log: slogDiscard(), Now: e.Clock.Now})
		if err != nil {
			t.Fatal(err)
		}
		s.publicURL = func() string { return "" }
		r := &http.Request{Host: "console.example.com", RemoteAddr: "127.0.0.1:5555", Header: http.Header{"X-Forwarded-Proto": {"https"}}}
		if got := s.passkeyPlace(r); !got.Available || got.RP.Origin != "https://console.example.com" {
			t.Errorf("behind a trusted proxy: %+v", got)
		}
	})
}

func TestPasskeySignInThroughTheConsole(t *testing.T) {
	b, e := signedInBrowser(t)
	a := b.addPasskey("MacBook")

	// The Sign-in page lists it, never used.
	resp, body := b.do(request{path: "/settings/sign-in"})
	page := text(body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /settings/sign-in = %d", resp.StatusCode)
	}
	if !strings.Contains(page, "MacBook") || !strings.Contains(page, "Never used") || !strings.Contains(page, "localhost") {
		t.Errorf("the page doesn't list the passkey:\n%s", page)
	}

	// A new browser signs in with it, no email typed, and lands where it was going.
	other := newBrowser(t, e)
	e.Clock.Advance(time.Hour)
	resp, _ = other.passkeySignIn(a, "/settings/tokens")
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/settings/tokens" {
		t.Fatalf("passkey sign-in = %d to %q, want 303 to /settings/tokens", resp.StatusCode, location(t, resp))
	}
	other.cookie = sessionCookie(t, resp)
	if c := other.cookie; !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
		t.Errorf("session cookie = %+v", c)
	}
	if resp, _ := other.do(request{path: "/settings/sign-in"}); resp.StatusCode != http.StatusOK {
		t.Error("the session from a passkey sign-in isn't signed in")
	}

	// Last used is recorded.
	_, body = other.do(request{path: "/settings/sign-in"})
	if page := text(body); !strings.Contains(page, "Last used just now") || strings.Contains(page, "Never used") {
		t.Errorf("the page doesn't say when the passkey was last used:\n%s", page)
	}

	// An unsafe next lands on /apps.
	resp, _ = newBrowser(t, e).passkeySignIn(a, "//evil.example")
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps" {
		t.Errorf("an unsafe next = %d to %q, want 303 to /apps", resp.StatusCode, location(t, resp))
	}
}

func TestPasskeySignInFailuresAreCounted(t *testing.T) {
	b, e := signedInBrowser(t)
	a := b.addPasskey("MacBook")
	visitor := newBrowser(t, e)

	const sentinel = "credential-sentinel-0033"
	for _, want := range []string{"4 tries left", "3 tries left", "2 tries left", "1 try left"} {
		resp, body := visitor.post("/login/passkey", url.Values{"ceremony": {"ceremony-sentinel-0033"}, "credential": {sentinel}})
		if resp.StatusCode != http.StatusOK || len(resp.Cookies()) != 0 {
			t.Fatalf("a bad passkey = %d with %d cookies, want 200 and none", resp.StatusCode, len(resp.Cookies()))
		}
		page := text(body)
		if !strings.Contains(page, "That passkey didn't work here. "+want+" before a 15-minute wait.") {
			t.Fatalf("the page lacks %q:\n%s", want, page)
		}
		if !strings.Contains(body, `role="alert"`) || !strings.Contains(body, "cv-mark-warning") {
			t.Error("the sentence is not an announced alert with its mark")
		}
		for _, leak := range []string{sentinel, "ceremony-sentinel-0033"} {
			if strings.Contains(body, leak) {
				t.Errorf("the page echoes %q", leak)
			}
		}
	}
	// The fifth locks the address: even the right passkey is refused.
	_, body := visitor.post("/login/passkey", url.Values{"ceremony": {"x"}, "credential": {"{}"}})
	if !strings.Contains(text(body), "Too many failed sign-ins from this address. Try again in 15 minutes.") {
		t.Errorf("the fifth failure doesn't say how long to wait:\n%s", text(body))
	}
	resp, body := visitor.passkeySignIn(a, "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(text(body), "Too many failed sign-ins") || len(resp.Cookies()) != 0 {
		t.Errorf("a good passkey from a locked address = %d:\n%s", resp.StatusCode, text(body))
	}
	if strings.Contains(e.Logs.String(), sentinel) || strings.Contains(e.Logs.String(), "ceremony-sentinel-0033") {
		t.Error("the log holds what the browser posted")
	}
}

func TestConfirmItsYou(t *testing.T) {
	t.Run("with the password", func(t *testing.T) {
		b, e := signedInBrowser(t)
		e.Clock.Advance(11 * time.Minute) // the sign-in no longer counts

		_, body := b.do(request{path: "/settings/sign-in"})
		page := text(body)
		if !strings.Contains(page, "Confirm it's you first") || strings.Contains(body, `name="credential"`) && strings.Contains(body, "data-passkey-add") {
			t.Errorf("an unconfirmed session isn't asked to confirm:\n%s", page)
		}
		if !strings.Contains(body, `action="/settings/sign-in/verify"`) || !strings.Contains(body, `name="password"`) {
			t.Error("the page has no password form to confirm with")
		}
		if strings.Contains(body, "data-passkey-add") {
			t.Error("the Add form is drawn before the session is confirmed")
		}

		// Registration options are refused until then.
		resp, body := b.do(request{method: http.MethodPost, path: "/settings/passkeys/options"})
		var refused struct {
			Error struct{ Code, Message string }
		}
		if err := json.Unmarshal([]byte(body), &refused); err != nil || resp.StatusCode != http.StatusForbidden || refused.Error.Code != "not_confirmed" {
			t.Errorf("options before confirming = %d %s, want 403 not_confirmed", resp.StatusCode, body)
		}

		// A wrong password is counted, in the sign-in's own words.
		resp, body = b.post("/settings/sign-in/verify", url.Values{"password": {"not the password"}})
		if resp.StatusCode != http.StatusOK || !strings.Contains(text(body), "That isn't your password. 4 tries left before a 15-minute wait.") {
			t.Errorf("a wrong password = %d:\n%s", resp.StatusCode, text(body))
		}
		if strings.Contains(body, "not the password") {
			t.Error("the page echoes the password")
		}
		// The right one confirms and goes to the Add panel.
		resp, _ = b.post("/settings/sign-in/verify", url.Values{"password": {testPassword}})
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/settings/sign-in?verified=1#add" {
			t.Fatalf("the right password = %d to %q", resp.StatusCode, location(t, resp))
		}
		_, body = b.do(request{path: "/settings/sign-in?verified=1"})
		if !strings.Contains(text(body), "Confirmed. You can add a passkey now.") || !strings.Contains(body, "data-passkey-add") {
			t.Errorf("after confirming, the page lacks the notice or the Add form:\n%s", text(body))
		}
		b.addPasskey("Phone")
	})

	t.Run("with a passkey", func(t *testing.T) {
		b, e := signedInBrowser(t)
		a := b.addPasskey("MacBook")
		e.Clock.Advance(11 * time.Minute)

		_, body := b.do(request{path: "/settings/sign-in"})
		if !strings.Contains(body, "data-passkey-confirm") || !strings.Contains(body, "Use a passkey") {
			t.Errorf("the page offers no passkey to confirm with:\n%s", text(body))
		}
		ceremony, key := b.options("/settings/sign-in/verify/options")
		posted, err := a.Get(key, b.origin)
		if err != nil {
			t.Fatal(err)
		}
		resp, _ := b.post("/settings/sign-in/verify", url.Values{"ceremony": {ceremony}, "credential": {string(posted)}})
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/settings/sign-in?verified=1#add" {
			t.Fatalf("confirming with a passkey = %d to %q", resp.StatusCode, location(t, resp))
		}
		b.addPasskey("Phone")

		// A passkey that fails is counted like a wrong password.
		e.Clock.Advance(11 * time.Minute)
		resp, body = b.post("/settings/sign-in/verify", url.Values{"ceremony": {"x"}, "credential": {"{}"}})
		if resp.StatusCode != http.StatusOK || !strings.Contains(text(body), "That passkey didn't work here. 4 tries left") {
			t.Errorf("a bad passkey = %d:\n%s", resp.StatusCode, text(body))
		}
	})

	t.Run("a signed-out script gets JSON, not a redirect", func(t *testing.T) {
		e := newUIEnv(t, true, "")
		b := newBrowser(t, e)
		for _, path := range []string{"/settings/passkeys/options", "/settings/sign-in/verify/options"} {
			resp, body := b.do(request{method: http.MethodPost, path: path})
			if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, `"code":"unauthorized"`) {
				t.Errorf("POST %s signed out = %d %s, want 401 JSON", path, resp.StatusCode, body)
			}
		}
		resp, _ := b.post("/settings/passkeys", url.Values{"name": {"x"}})
		if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location(t, resp), "/login") {
			t.Errorf("the add form signed out = %d to %q, want 303 to /login", resp.StatusCode, location(t, resp))
		}
	})
}

func TestAddingAPasskeyRefusals(t *testing.T) {
	b, _ := signedInBrowser(t)

	t.Run("a bad name keeps what was typed", func(t *testing.T) {
		ceremony, key := b.options("/settings/passkeys/options")
		posted, _ := webauthntest.New("localhost").Create(key, b.origin)
		long := strings.Repeat("a", 65)
		resp, body := b.post("/settings/passkeys", url.Values{"name": {long}, "ceremony": {ceremony}, "credential": {string(posted)}})
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("a 65-character name = %d, want 422", resp.StatusCode)
		}
		if !strings.Contains(body, `value="`+long+`"`) || !strings.Contains(text(body), "The name must be 1 to 64 characters.") {
			t.Errorf("the page lacks the typed name or the sentence:\n%s", text(body))
		}
		// The credential and ceremony are never drawn back.
		if strings.Contains(body, string(posted)) || strings.Contains(body, ceremony) {
			t.Error("the page echoes the credential or the ceremony")
		}
		if got := passkeyCount(t, b); got != 0 {
			t.Errorf("a refused name left %d passkeys", got)
		}
	})

	t.Run("a passkey that doesn't verify", func(t *testing.T) {
		ceremony, key := b.options("/settings/passkeys/options")
		bad := webauthntest.New("localhost")
		bad.UserVerified = false
		posted, _ := bad.Create(key, b.origin)
		resp, body := b.post("/settings/passkeys", url.Values{"name": {"Phone"}, "ceremony": {ceremony}, "credential": {string(posted)}})
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "That passkey couldn't be added. Nothing was saved.") {
			t.Errorf("a passkey without UV = %d:\n%s", resp.StatusCode, text(body))
		}
		if !strings.Contains(body, `value="Phone"`) {
			t.Error("the typed name was lost")
		}
		if got := passkeyCount(t, b); got != 0 {
			t.Errorf("a refused passkey left %d passkeys", got)
		}
	})

	t.Run("a ceremony can be used once", func(t *testing.T) {
		ceremony, key := b.options("/settings/passkeys/options")
		posted, _ := webauthntest.New("localhost").Create(key, b.origin)
		form := url.Values{"name": {"Phone"}, "ceremony": {ceremony}, "credential": {string(posted)}}
		if resp, _ := b.post("/settings/passkeys", form); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("first use = %d", resp.StatusCode)
		}
		if resp, _ := b.post("/settings/passkeys", form); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("second use = %d, want 422", resp.StatusCode)
		}
	})
}

// passkeyCount is how many passkeys the Sign-in page lists.
func passkeyCount(t *testing.T, b *browser) int {
	t.Helper()
	_, body := b.do(request{path: "/settings/sign-in"})
	return strings.Count(body, `action="/settings/passkeys/`) / 2 // each row has a rename and a remove form
}

func TestManagePasskeysAndPasswordSignIn(t *testing.T) {
	b, e := signedInBrowser(t)

	// With no passkey the password can't be turned off, and the page says why.
	_, body := b.do(request{path: "/settings/sign-in"})
	if !strings.Contains(text(body), "No passkeys yet.") || !strings.Contains(text(body), "Add a passkey for localhost first.") ||
		!regexp.MustCompile(`<button[^>]*value="off"[^>]*disabled`).MatchString(body) {
		t.Errorf("the page doesn't explain why password sign-in can't go off:\n%s", text(body))
	}
	resp, body := b.post("/settings/sign-in", url.Values{"password": {"off"}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "Add a passkey for localhost before turning password sign-in off.") {
		t.Errorf("turning it off without a passkey = %d:\n%s", resp.StatusCode, text(body))
	}
	if resp, _ := b.post("/settings/sign-in", url.Values{"password": {"maybe"}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a value that is neither on nor off = %d, want 422", resp.StatusCode)
	}

	a := b.addPasskey("MacBook")
	b.addPasskey("Phone")

	// Rename.
	id := firstPasskeyID(t, b)
	resp, _ = b.post("/settings/passkeys/"+id, url.Values{"name": {"  Work laptop "}})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/settings/sign-in?renamed=1" {
		t.Fatalf("rename = %d to %q", resp.StatusCode, location(t, resp))
	}
	_, body = b.do(request{path: "/settings/sign-in?renamed=1"})
	if !strings.Contains(text(body), "Work laptop") || !strings.Contains(text(body), "Passkey renamed.") {
		t.Errorf("the rename isn't shown:\n%s", text(body))
	}
	resp, body = b.post("/settings/passkeys/"+id, url.Values{"name": {""}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "The name must be 1 to 64 characters.") {
		t.Errorf("an empty name = %d:\n%s", resp.StatusCode, text(body))
	}

	// Turn password sign-in off.
	resp, _ = b.post("/settings/sign-in", url.Values{"password": {"off"}})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/settings/sign-in?saved=1" {
		t.Fatalf("turning it off = %d to %q", resp.StatusCode, location(t, resp))
	}
	_, body = b.do(request{path: "/settings/sign-in?saved=1"})
	if !strings.Contains(text(body), "Off (passkeys only)") || !strings.Contains(text(body), "Turn password sign-in back on") || !strings.Contains(text(body), "Saved.") {
		t.Errorf("the page doesn't show the setting:\n%s", text(body))
	}

	// The password form refuses, and the sign-in page says so and how to get back in.
	resp, body = b.post("/login", url.Values{"email": {testEmail}, "password": {testPassword}})
	if resp.StatusCode != http.StatusOK || len(resp.Cookies()) != 0 || !strings.Contains(text(body), "Password sign-in is off. Sign in with a passkey.") {
		t.Errorf("a password sign-in while it is off = %d:\n%s", resp.StatusCode, text(body))
	}
	_, body = newBrowser(t, e).do(request{path: "/login"})
	if strings.Contains(body, `name="password"`) || !strings.Contains(text(body), "Password sign-in is off on this caravana. Lost your passkeys? On the host, run:") ||
		!strings.Contains(body, "caravana admin reset-password") || !strings.Contains(text(body), "Passkeys need JavaScript.") {
		t.Errorf("the sign-in page while password sign-in is off:\n%s", text(body))
	}
	// And the passkey button is the primary one.
	if !regexp.MustCompile(`cv-btn-primary[^>]*data-passkey-button`).MatchString(body) {
		t.Error("the passkey button isn't the primary one while the password is off")
	}
	other := newBrowser(t, e)
	if resp, _ := other.passkeySignIn(a, ""); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("a passkey sign-in while password sign-in is off = %d, want 303", resp.StatusCode)
	}

	// Removing one of two is fine; the last one of the domain is refused.
	resp, _ = b.post("/settings/passkeys/"+id+"/remove", nil)
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/settings/sign-in?removed=1" {
		t.Fatalf("remove one of two = %d to %q", resp.StatusCode, location(t, resp))
	}
	_, body = b.do(request{path: "/settings/sign-in"})
	if !strings.Contains(text(body), "Your only passkey for localhost, and password sign-in is off: turn it on first, or add another passkey.") {
		t.Errorf("the last passkey's row doesn't say why it can't go:\n%s", text(body))
	}
	if strings.Contains(body, "/remove") {
		t.Error("the last passkey has a Remove while password sign-in is off")
	}
	last := firstPasskeyID(t, b)
	resp, body = b.post("/settings/passkeys/"+last+"/remove", nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "That is your only passkey for localhost, and password sign-in is off. Turn password sign-in on first, or add another passkey.") {
		t.Errorf("removing the last passkey = %d:\n%s", resp.StatusCode, text(body))
	}

	// Back on: the last one may go.
	if resp, _ := b.post("/settings/sign-in", url.Values{"password": {"on"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("turning it on = %d", resp.StatusCode)
	}
	if resp, _ := b.post("/settings/passkeys/"+last+"/remove", nil); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("removing the last passkey with the password on = %d, want 303", resp.StatusCode)
	}
	// A passkey that is already gone is not an error page.
	if resp, _ := b.post("/settings/passkeys/"+last+"/remove", nil); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("removing it again = %d, want 303", resp.StatusCode)
	}
	if got := passkeyCount(t, b); got != 0 {
		t.Errorf("%d passkeys are left", got)
	}
}

// firstPasskeyID reads the ID of the first rename form on the Sign-in page.
func firstPasskeyID(t *testing.T, b *browser) string {
	t.Helper()
	_, body := b.do(request{path: "/settings/sign-in"})
	m := regexp.MustCompile(`action="/settings/passkeys/([a-z2-7]{26})"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("the page lists no passkey:\n%s", text(body))
	}
	return m[1]
}

func TestAPasskeyMadeElsewhereIsMarked(t *testing.T) {
	b, e := signedInBrowser(t)
	// A passkey made at another domain, added the way the service does it.
	other := &browser{t: t, e: e, host: "old.localhost:" + strings.TrimPrefix(b.host, "localhost:"), origin: "http://old.localhost:" + strings.TrimPrefix(b.host, "localhost:"), cookie: b.cookie}
	ceremony, key := other.options("/settings/passkeys/options")
	posted, _ := webauthntest.New("old.localhost").Create(key, other.origin)
	if resp, body := other.post("/settings/passkeys", url.Values{"name": {"Old domain"}, "ceremony": {ceremony}, "credential": {string(posted)}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("add at old.localhost = %d:\n%s", resp.StatusCode, text(body))
	}

	_, body := b.do(request{path: "/settings/sign-in"})
	if !strings.Contains(text(body), "Made for old.localhost: it doesn't work at this address.") {
		t.Errorf("a passkey for another domain isn't marked:\n%s", text(body))
	}
	if !strings.Contains(text(body), "A passkey belongs to the domain it was made at, localhost.") {
		t.Errorf("the page doesn't say the domain a passkey belongs to:\n%s", text(body))
	}
}

func TestPasskeysWhereTheyCannotBeUsed(t *testing.T) {
	t.Run("at an IP address", func(t *testing.T) {
		e := newUIEnv(t, true, "")
		// The test server's own address is 127.0.0.1:<port>.
		resp, body := e.do(t, request{path: "/login"})
		if resp.StatusCode != http.StatusOK || !strings.Contains(body, `data-available="false"`) {
			t.Errorf("the sign-in page at an IP address: %d, no data-available=\"false\":\n%s", resp.StatusCode, text(body))
		}
		resp, body = e.do(t, request{method: http.MethodPost, path: "/login/passkey/options"})
		var refused struct {
			Error struct{ Code, Message string }
		}
		if err := json.Unmarshal([]byte(body), &refused); err != nil || resp.StatusCode != http.StatusConflict ||
			refused.Error.Code != "passkeys_unavailable" || !strings.Contains(refused.Error.Message, "http://localhost:") {
			t.Errorf("options at an IP address = %d %s, want 409 passkeys_unavailable naming localhost", resp.StatusCode, body)
		}
		if resp.Header.Get("Cache-Control") != "no-store" {
			t.Error("the refusal is cacheable")
		}

		cookie := e.signIn(t, nil)
		_, body = e.do(t, request{path: "/settings/sign-in", cookies: []*http.Cookie{cookie}})
		if !strings.Contains(text(body), "Passkeys work at the console's https domain or at http://localhost:") || strings.Contains(body, "data-passkey-add") {
			t.Errorf("Settings at an IP address:\n%s", text(body))
		}
		resp, body = e.do(t, request{method: http.MethodPost, path: "/settings/passkeys/options", cookies: []*http.Cookie{cookie}})
		if resp.StatusCode != http.StatusConflict || !strings.Contains(body, "passkeys_unavailable") {
			t.Errorf("registration options at an IP address = %d %s", resp.StatusCode, body)
		}
	})

	t.Run("at another host than the public address", func(t *testing.T) {
		e := newUIEnvAt(t, true, "", "https://console.example.com")
		resp, body := e.do(t, request{host: "evil.example.com", method: http.MethodPost, path: "/login/passkey/options"})
		if resp.StatusCode != http.StatusConflict || !strings.Contains(body, "Passkeys work at https://console.example.com") {
			t.Errorf("options at another host = %d %s", resp.StatusCode, body)
		}
		// A localhost name still works, for a visit from the host itself.
		b := newBrowser(t, e)
		b.signInWithPassword()
		b.options("/settings/passkeys/options")
	})

	t.Run("a refused finish says where", func(t *testing.T) {
		e := newUIEnv(t, true, "")
		resp, body := e.do(t, request{method: http.MethodPost, path: "/login/passkey", form: url.Values{"ceremony": {"x"}, "credential": {"{}"}}})
		if resp.StatusCode != http.StatusOK || !strings.Contains(text(body), "Passkeys work at the console's https domain") || len(resp.Cookies()) != 0 {
			t.Errorf("a passkey post at an IP address = %d:\n%s", resp.StatusCode, text(body))
		}
	})

	t.Run("no admin yet", func(t *testing.T) {
		e := newUIEnv(t, false, "")
		b := newBrowser(t, e)
		resp, body := b.do(request{method: http.MethodPost, path: "/login/passkey/options"})
		if resp.StatusCode != http.StatusConflict || !strings.Contains(body, `"code":"no_admin"`) {
			t.Errorf("options without an admin = %d %s", resp.StatusCode, body)
		}
		_, page := b.do(request{path: "/login"})
		if strings.Contains(page, "data-passkey-signin") {
			t.Error("the sign-in page offers passkeys without an admin")
		}
	})
}

func TestPasskeyPagesKeepTheConsolesPolicy(t *testing.T) {
	b, _ := signedInBrowser(t)
	b.addPasskey("MacBook")
	inline := regexp.MustCompile(`(?i)<script(?:\s[^>]*)?>`)
	handler := regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
	for _, path := range []string{"/login", "/settings/sign-in", "/settings/tokens"} {
		visitor := b
		if path == "/login" {
			visitor = newBrowser(t, b.e) // signed in, /login would redirect
		}
		resp, body := visitor.do(request{path: path})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d", path, resp.StatusCode)
		}
		if got := resp.Header.Get("Content-Security-Policy"); got != consoleCSP {
			t.Errorf("%s: CSP = %q, want the console's own", path, got)
		}
		for _, tag := range inline.FindAllString(body, -1) {
			if !strings.Contains(tag, " src=") {
				t.Errorf("%s has an inline script: %s", path, tag)
			}
		}
		if handler.MatchString(body) {
			t.Errorf("%s has an inline event handler", path)
		}
		loads := strings.Contains(body, "/static/js/passkeys.js?v=")
		if want := path != "/settings/tokens"; loads != want {
			t.Errorf("%s loads passkeys.js = %v, want %v", path, loads, want)
		}
		if strings.Contains(body, "script-src") {
			t.Errorf("%s has its own script policy", path)
		}
	}
}

func TestSignInEntryInTheSettingsNav(t *testing.T) {
	b, _ := signedInBrowser(t)
	_, body := b.do(request{path: "/settings/tokens"})
	sign, tokens := strings.Index(body, `href="/settings/sign-in"`), strings.Index(body, `href="/settings/tokens"`)
	if sign < 0 || tokens < 0 || sign > tokens {
		t.Fatalf("the Security group isn't Sign-in, then API tokens (sign-in at %d, tokens at %d)", sign, tokens)
	}
	if !strings.Contains(body[sign:tokens], "Sign-in") || !strings.Contains(body[sign:tokens], "cv-icon") {
		t.Error("the Sign-in entry has no label or icon")
	}
	_, body = b.do(request{path: "/settings/sign-in"})
	if !regexp.MustCompile(`href="/settings/sign-in" class="cv-snav-item" aria-current="page"`).MatchString(body) {
		t.Error("the Sign-in entry isn't current on its own page")
	}
	if !strings.Contains(text(body), "How you sign in to this caravana: passkeys and your password.") {
		t.Error("the lede is missing")
	}
}

func TestPasskeyPostsAreProtected(t *testing.T) {
	b, _ := signedInBrowser(t)
	for _, path := range []string{"/login/passkey/options", "/login/passkey", "/settings/sign-in", "/settings/sign-in/verify",
		"/settings/sign-in/verify/options", "/settings/passkeys", "/settings/passkeys/options"} {
		resp, _ := b.do(request{method: http.MethodPost, path: path, form: url.Values{"password": {"off"}}, headers: map[string]string{"Sec-Fetch-Site": "cross-site"}})
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("a cross-site POST %s = %d, want 403", path, resp.StatusCode)
		}
	}
	// Nothing changed.
	if _, body := b.do(request{path: "/settings/sign-in"}); !strings.Contains(text(body), "No passkeys yet.") {
		t.Error("a refused post changed something")
	}
}

// TestNoPasskeySecretReachesAPageOrTheLog runs the whole flow and searches every page, answer and
// log line for what the authenticator keeps private and what is sent once.
func TestNoPasskeySecretReachesAPageOrTheLog(t *testing.T) {
	b, e := signedInBrowser(t)
	var sentinels []string
	var seen []string // every body the visitor received
	note := func(a *webauthntest.Authenticator) {
		sentinels = append(sentinels, a.PrivateKeySentinel()...)
		sentinels = append(sentinels, base64.RawURLEncoding.EncodeToString(a.CredentialID()), hex.EncodeToString(a.CredentialID()))
	}

	// Add, sign in, confirm, rename, turn off and on, remove: each response is kept.
	ceremony, key := b.options("/settings/passkeys/options")
	seen = append(seen, string(key))
	a := webauthntest.New("localhost")
	note(a)
	posted, _ := a.Create(key, b.origin)
	sentinels = append(sentinels, ceremony)
	resp, body := b.post("/settings/passkeys", url.Values{"name": {"MacBook"}, "ceremony": {ceremony}, "credential": {string(posted)}})
	seen = append(seen, body)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatal("add failed")
	}
	var created struct {
		Response struct{ ClientDataJSON, AttestationObject string }
	}
	_ = json.Unmarshal(posted, &created)
	sentinels = append(sentinels, created.Response.ClientDataJSON, created.Response.AttestationObject)

	visitor := newBrowser(t, e)
	signCeremony, signKey := visitor.options("/login/passkey/options")
	seen = append(seen, string(signKey))
	sentinels = append(sentinels, signCeremony)
	answer, _ := a.Get(signKey, visitor.origin)
	var asserted struct {
		Response struct{ ClientDataJSON, Signature, AuthenticatorData string }
	}
	_ = json.Unmarshal(answer, &asserted)
	sentinels = append(sentinels, asserted.Response.ClientDataJSON, asserted.Response.Signature)
	resp, body = visitor.post("/login/passkey", url.Values{"ceremony": {signCeremony}, "credential": {string(answer)}})
	seen = append(seen, body)
	visitor.cookie = sessionCookie(t, resp)
	// A replay is refused, and the page it draws holds nothing either.
	_, body = newBrowser(t, e).post("/login/passkey", url.Values{"ceremony": {signCeremony}, "credential": {string(answer)}})
	seen = append(seen, body)

	for _, path := range []string{"/login", "/settings/sign-in", "/settings/sign-in?added=1", "/settings/tokens"} {
		_, body := b.do(request{path: path})
		seen = append(seen, body)
	}
	b.post("/settings/sign-in", url.Values{"password": {"off"}})
	_, body = b.do(request{path: "/settings/sign-in"})
	seen = append(seen, body)
	_, body = newBrowser(t, e).do(request{path: "/login"})
	seen = append(seen, body)

	all := strings.Join(seen, "\n") + "\n" + e.Logs.String()
	for _, s := range sentinels {
		if len(s) >= 16 && strings.Contains(all, s) && !strings.Contains(string(key)+string(signKey), s) {
			t.Errorf("a page, an answer or the log holds %s…", s[:12])
		}
	}
	// The options themselves hold the challenge, which the browser needs: it is a one-time value
	// the page never keeps. The pages and the log hold none.
	var opts struct{ Challenge string }
	_ = json.Unmarshal(signKey, &opts)
	for _, page := range append(seen[3:], e.Logs.String()) {
		if opts.Challenge != "" && strings.Contains(page, opts.Challenge) && page != string(signKey) {
			t.Error("a page or the log holds a challenge")
			break
		}
	}
}

func slogDiscard() *slog.Logger { return slog.New(slog.DiscardHandler) }
