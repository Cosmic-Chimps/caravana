package main

// The console-passkeys step of the lab test (feature 033): signing in with a passkey, driven by the
// software authenticator (internal/webauthntest) over the console's own routes, the way the page's
// script drives a browser's. The step reaches Caravana as http://localhost:<port>, the same port as
// the lab's console address: localhost is a secure context, so passkeys work there, while the lab's
// own address, 127.0.0.1, is an IP and they don't (the step checks that too). Its browser has a
// cookie jar of its own, so the lab's console session isn't touched until `caravana admin
// reset-password` ends every session; the step signs the lab's console in again on the way out.
// runConsole lists it right before console-appearance.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/store"
	"github.com/cosmic-chimps/caravana/internal/webauthntest"
)

// labBrowser is one visitor with its own cookies. Whatever host its URLs name, its connections go
// to the lab's Caravana, so "old.localhost" needs no DNS.
type labBrowser struct {
	t      *testing.T
	client *http.Client
	base   string // http://localhost:<port>
	origin string // what an authenticator signs for: the same
	pages  []string
}

// newLabBrowser makes a visitor at http://<name>:<port>, where port is the lab console's own.
func (c *console) newLabBrowser(t *testing.T, name string) *labBrowser {
	t.Helper()
	target, err := url.Parse(c.g.target.API)
	if err != nil {
		t.Fatal(err)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	dial := target.Host
	b := &labBrowser{t: t, base: "http://" + name + ":" + target.Port(), origin: "http://" + name + ":" + target.Port()}
	b.client = &http.Client{
		Jar:     jar,
		Timeout: 30 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, dial)
		}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return b
}

func (b *labBrowser) read(resp *http.Response, err error) (*http.Response, string) {
	b.t.Helper()
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	b.pages = append(b.pages, string(raw)) // every answer is searched for secrets at the end
	return resp, string(raw)
}

func (b *labBrowser) get(path string) (*http.Response, string) {
	b.t.Helper()
	resp, err := b.client.Get(b.base + path)
	return b.read(resp, err)
}

func (b *labBrowser) post(path string, form url.Values) (*http.Response, string) {
	b.t.Helper()
	resp, err := b.client.PostForm(b.base+path, form)
	return b.read(resp, err)
}

// options is what the page's script does first: POST for a ceremony, as JSON.
func (b *labBrowser) options(path string) (ceremony string, publicKey []byte) {
	b.t.Helper()
	req, err := http.NewRequest(http.MethodPost, b.base+path, nil)
	if err != nil {
		b.t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json")
	resp, body := b.read(b.client.Do(req))
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" {
		b.t.Fatalf("POST %s = %d (Cache-Control %q), want 200 no-store: %s", path, resp.StatusCode, resp.Header.Get("Cache-Control"), body)
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

// signInWithPassword signs the browser in with the lab's password and wants the usual 303 to /apps.
func (b *labBrowser) signInWithPassword() {
	b.t.Helper()
	resp, body := b.post("/login", url.Values{"email": {labEmail}, "password": {labPassword}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/apps" {
		b.t.Fatalf("POST /login at %s = %d → %q, want 303 to /apps: %s", b.base, resp.StatusCode, resp.Header.Get("Location"), text(body))
	}
}

// addPasskey adds a passkey for rp through the registration routes, from a confirmed session.
func (b *labBrowser) addPasskey(rp, name string) *webauthntest.Authenticator {
	b.t.Helper()
	ceremony, key := b.options("/settings/passkeys/options")
	a := webauthntest.New(rp)
	posted, err := a.Create(key, b.origin)
	if err != nil {
		b.t.Fatal(err)
	}
	resp, body := b.post("/settings/passkeys", url.Values{"name": {name}, "ceremony": {ceremony}, "credential": {string(posted)}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/settings/sign-in?added=1" {
		b.t.Fatalf("add passkey %q = %d → %q, want 303 to /settings/sign-in?added=1: %s", name, resp.StatusCode, resp.Header.Get("Location"), text(body))
	}
	return a
}

// signInWithPasskey is the sign-in script's whole exchange for a's answer to a fresh ceremony.
func (b *labBrowser) signInWithPasskey(a *webauthntest.Authenticator, next string) (*http.Response, string) {
	b.t.Helper()
	ceremony, key := b.options("/login/passkey/options")
	posted, err := a.Get(key, b.origin)
	if err != nil {
		b.t.Fatal(err)
	}
	return b.post("/login/passkey", url.Values{"ceremony": {ceremony}, "credential": {string(posted)}, "next": {next}})
}

// text reads a page as the owner does, without its tags.
var tagsInPage = regexp.MustCompile(`<[^>]*>`)

func text(body string) string {
	return strings.Join(strings.Fields(strings.NewReplacer("&#39;", "'", "&amp;", "&").Replace(tagsInPage.ReplaceAllString(body, " "))), " ")
}

// passkeys, through the console's own routes:
//
//  1. Where passkeys can't be used: the sign-in page at the lab's own address, 127.0.0.1, says
//     data-available="false" and the options route answers 409 passkeys_unavailable; at
//     localhost it says "true".
//  2. A session confirms by signing in: a passkey is added (options, the authenticator's answer,
//     the form), and the list shows it, never used.
//  3. A session from before confirmations (a row with no verified_at, made in the store) is asked
//     to confirm: registration options are 403 not_confirmed, a wrong password counts, the right one
//     confirms; so does a passkey. Adding works afterwards.
//  4. Signing out, then signing in with the passkey: 303 to where it was going, last used recorded.
//  5. Rename changes the name.
//  6. Turning password sign-in off needs a passkey for localhost; then the password form refuses
//     (the page has no password field), the last passkey can't be removed, and
//     `caravana admin reset-password` turns password sign-in back on, ends every session and keeps
//     the passkey.
//  7. A passkey made for old.localhost is listed with "doesn't work at this address". At
//     localhost the other domain's passkey, a replay, a sign count that went backwards and an
//     assertion without user verification are each refused, counted (4, 3, 2 and 1 tries left;
//     never five, so the lab's own address stays unlocked: the limit itself is unit-tested), and a
//     good passkey then signs in and clears the count.
//  8. What an authenticator keeps private (its key, in hex and base64), and a credential ID, is
//     in no page, in the server's log or in the database file.
//  9. Both passkeys are removed and password sign-in is on, whatever happened above.
func (c *console) passkeys(t *testing.T) {
	g := c.g
	// reset-password ends every session, the lab console's too, and the live stream with it:
	// sign it in again and open the app's stream on the way out, for console-delete.
	defer func() {
		c.post(t, "/login", url.Values{"email": {labEmail}, "password": {labPassword}})
		c.openLive(t, "/live/apps/whoami")
		c.expectEvent(t, "sync", 5*time.Second)
	}()
	// Leave nothing behind, even when a check fails: password sign-in on, no passkeys.
	t.Cleanup(func() {
		for _, call := range []struct{ method, path, body string }{{http.MethodPut, "/v1/account/sign-in", `{"passwordSignIn": true}`}} {
			if resp, err := g.apiRequest(call.method, call.path, "application/json", call.body); err == nil {
				resp.Body.Close()
			}
		}
		var list struct {
			Passkeys []struct{ ID string } `json:"passkeys"`
		}
		if resp, err := g.apiRequest(http.MethodGet, "/v1/account/passkeys", "", ""); err == nil {
			_ = json.NewDecoder(resp.Body).Decode(&list)
			resp.Body.Close()
		}
		for _, p := range list.Passkeys {
			if resp, err := g.apiRequest(http.MethodDelete, "/v1/account/passkeys/"+p.ID, "", ""); err == nil {
				resp.Body.Close()
			}
		}
	})

	// 1. Where they can't be used, and where they can.
	ip := c.newLabBrowser(t, "127.0.0.1")
	if _, body := ip.get("/login"); !strings.Contains(body, `data-available="false"`) {
		t.Fatalf("the sign-in page at 127.0.0.1 doesn't say passkeys are unavailable:\n%s", text(body))
	}
	resp, body := ip.post("/login/passkey/options", nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(body, `"code":"passkeys_unavailable"`) {
		t.Fatalf("passkey options at 127.0.0.1 = %d %s, want 409 passkeys_unavailable", resp.StatusCode, body)
	}
	web := c.newLabBrowser(t, "localhost")
	if _, body := web.get("/login"); !strings.Contains(body, `data-available="true"`) || !strings.Contains(body, "data-passkey-signin") {
		t.Fatalf("the sign-in page at localhost doesn't offer passkeys:\n%s", text(body))
	}

	// 2. Sign in with the password, which confirms, and add a passkey.
	web.signInWithPassword()
	if _, body := web.get("/settings/sign-in"); !strings.Contains(text(body), "No passkeys yet.") || !strings.Contains(body, "data-passkey-add") {
		t.Fatalf("Settings › Sign-in at localhost, signed in: no empty list or Add form:\n%s", text(body))
	}
	laptop := web.addPasskey("localhost", "Lab laptop")
	if _, body := web.get("/settings/sign-in?added=1"); !strings.Contains(text(body), "Lab laptop") || !strings.Contains(text(body), "Never used") ||
		!strings.Contains(text(body), "Passkey added.") {
		t.Fatalf("the list after adding:\n%s", text(body))
	}

	// 3. A session that never confirmed.
	st, err := store.OpenShared(context.Background(), g.target.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	oldSession := func(name string) *labBrowser {
		value := "lab-old-session-" + name + "-" + g.run
		sum := sha256.Sum256([]byte(value))
		now := time.Now().UTC()
		if err := st.CreateSession(context.Background(), store.SessionRecord{
			IDHash: hex.EncodeToString(sum[:]), CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		b := c.newLabBrowser(t, "localhost")
		u, _ := url.Parse(b.base)
		b.client.Jar.SetCookies(u, []*http.Cookie{{Name: "caravana_session", Value: value, Path: "/"}})
		return b
	}
	old := oldSession("password")
	if _, body := old.get("/settings/sign-in"); !strings.Contains(text(body), "Confirm it's you first") || strings.Contains(body, "data-passkey-add") {
		t.Fatalf("a session that never confirmed isn't asked to:\n%s", text(body))
	}
	resp, body = old.post("/settings/passkeys/options", nil)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, `"code":"not_confirmed"`) {
		t.Fatalf("registration options before confirming = %d %s, want 403 not_confirmed", resp.StatusCode, body)
	}
	resp, body = old.post("/settings/sign-in/verify", url.Values{"password": {"not the lab password"}})
	if !regexp.MustCompile(`That isn't your password\. [1-4] tr(?:y|ies) left before a 15-minute wait\.`).MatchString(text(body)) || resp.StatusCode != http.StatusOK {
		t.Fatalf("a wrong password to confirm = %d:\n%s", resp.StatusCode, text(body))
	}
	resp, _ = old.post("/settings/sign-in/verify", url.Values{"password": {labPassword}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/settings/sign-in?verified=1#add" {
		t.Fatalf("the right password to confirm = %d → %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if _, body := old.get("/settings/sign-in?verified=1"); !strings.Contains(body, "data-passkey-add") {
		t.Fatalf("after confirming the Add form isn't there:\n%s", text(body))
	}
	// And a passkey confirms too.
	other := oldSession("passkey")
	if _, body := other.get("/settings/sign-in"); !strings.Contains(body, "data-passkey-confirm") {
		t.Fatalf("an unconfirmed session isn't offered a passkey to confirm with:\n%s", text(body))
	}
	ceremony, key := other.options("/settings/sign-in/verify/options")
	posted, err := laptop.Get(key, other.origin)
	if err != nil {
		t.Fatal(err)
	}
	resp, body = other.post("/settings/sign-in/verify", url.Values{"ceremony": {ceremony}, "credential": {string(posted)}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/settings/sign-in?verified=1#add" {
		t.Fatalf("a passkey to confirm = %d → %q: %s", resp.StatusCode, resp.Header.Get("Location"), text(body))
	}
	st.Close()

	// 4. Sign out; sign in with the passkey.
	if resp, _ := web.post("/logout", nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /logout = %d", resp.StatusCode)
	}
	resp, body = web.signInWithPasskey(laptop, "/settings/sign-in")
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/settings/sign-in" {
		t.Fatalf("passkey sign-in = %d → %q, want 303 to /settings/sign-in: %s", resp.StatusCode, resp.Header.Get("Location"), text(body))
	}
	if _, body := web.get("/settings/sign-in"); !strings.Contains(text(body), "Last used") || strings.Contains(text(body), "Never used") {
		t.Fatalf("the passkey's last use isn't recorded:\n%s", text(body))
	}

	// 5. Rename.
	id := regexp.MustCompile(`action="/settings/passkeys/([a-z2-7]{26})"`).FindStringSubmatch(func() string { _, b := web.get("/settings/sign-in"); return b }())
	if id == nil {
		t.Fatal("the page lists no passkey to rename")
	}
	if resp, _ := web.post("/settings/passkeys/"+id[1], url.Values{"name": {"Lab laptop 2"}}); resp.StatusCode != http.StatusSeeOther ||
		resp.Header.Get("Location") != "/settings/sign-in?renamed=1" {
		t.Fatalf("rename = %d → %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if _, body := web.get("/settings/sign-in"); !strings.Contains(text(body), "Lab laptop 2") {
		t.Fatalf("the new name isn't listed:\n%s", text(body))
	}

	// 6. Password sign-in off, refused, and back on from the host.
	if resp, _ := web.post("/settings/sign-in", url.Values{"password": {"off"}}); resp.StatusCode != http.StatusSeeOther ||
		resp.Header.Get("Location") != "/settings/sign-in?saved=1" {
		t.Fatalf("turning password sign-in off = %d → %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	anon := c.newLabBrowser(t, "localhost")
	resp, body = anon.post("/login", url.Values{"email": {labEmail}, "password": {labPassword}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(text(body), "Password sign-in is off. Sign in with a passkey.") || len(resp.Cookies()) != 0 {
		t.Fatalf("a password sign-in while it is off = %d:\n%s", resp.StatusCode, text(body))
	}
	if _, body := anon.get("/login"); strings.Contains(body, `name="password"`) || !strings.Contains(body, "caravana admin reset-password") {
		t.Fatalf("the sign-in page while password sign-in is off:\n%s", text(body))
	}
	resp, body = web.post("/settings/passkeys/"+id[1]+"/remove", nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "That is your only passkey for localhost, and password sign-in is off.") {
		t.Fatalf("removing the last passkey while password sign-in is off = %d:\n%s", resp.StatusCode, text(body))
	}
	var out, errOut bytes.Buffer
	if code := runAdmin(context.Background(), []string{"reset-password", "-data-dir", g.target.DataDir}, os.Getenv,
		strings.NewReader(labPassword+"\n"), &out, &errOut); code != 0 || !strings.Contains(out.String(), "Password sign-in is on again.") {
		t.Fatalf("caravana admin reset-password exited %d: %s%s", code, out.String(), errOut.String())
	}
	if resp, _ := web.get("/settings/sign-in"); resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/login") {
		t.Fatalf("a session after reset-password = %d → %q, want it ended", resp.StatusCode, resp.Header.Get("Location"))
	}
	web = c.newLabBrowser(t, "localhost")
	web.signInWithPassword()
	if _, body := web.get("/settings/sign-in"); !strings.Contains(text(body), "Lab laptop 2") || !strings.Contains(text(body), "Password") {
		t.Fatalf("the passkey was not kept by the reset:\n%s", text(body))
	}

	// 7. A passkey made for another domain, and the refusals.
	oldDomain := c.newLabBrowser(t, "old.localhost")
	oldDomain.signInWithPassword()
	elsewhere := oldDomain.addPasskey("old.localhost", "Old domain")
	// text() turns each tag into a space, so the domain's mono span leaves one before the colon.
	if resp, body := web.get("/settings/sign-in"); !strings.Contains(text(body), "Made for old.localhost : it doesn't work at this address.") {
		t.Fatalf("a passkey for another domain isn't marked (%d, %q):\n%s", resp.StatusCode, resp.Header.Get("Location"), text(body))
	}
	visitor := c.newLabBrowser(t, "localhost")
	refused := func(what string, triesLeft string, resp *http.Response, body string) {
		t.Helper()
		if resp.StatusCode != http.StatusOK || len(resp.Cookies()) != 0 ||
			!strings.Contains(text(body), "That passkey didn't work here. "+triesLeft+" before a 15-minute wait.") {
			t.Fatalf("%s = %d, want a refusal with %s:\n%s", what, resp.StatusCode, triesLeft, text(body))
		}
	}
	// A replay: the first use signs in (and clears the count); the second is refused.
	ceremony, key = visitor.options("/login/passkey/options")
	laptop.Counter = 50
	posted, _ = laptop.Get(key, visitor.origin)
	form := url.Values{"ceremony": {ceremony}, "credential": {string(posted)}}
	if resp, body := visitor.post("/login/passkey", form); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("the first use of an answer = %d:\n%s", resp.StatusCode, text(body))
	}
	visitor = c.newLabBrowser(t, "localhost")
	resp, body = visitor.post("/login/passkey", form)
	refused("a replayed answer", "4 tries left", resp, body)
	// Another domain's passkey, made to look right for this page: it exists, but not for localhost.
	fake := *elsewhere
	fake.RPID = "localhost"
	resp, body = visitor.signInWithPasskey(&fake, "")
	refused("a passkey made for another domain", "3 tries left", resp, body)
	// A sign count that went backwards.
	laptop.Counter = 3
	resp, body = visitor.signInWithPasskey(laptop, "")
	refused("a sign count that went backwards", "2 tries left", resp, body)
	// No user verification.
	laptop.Counter = 60
	laptop.UserVerified = false
	resp, body = visitor.signInWithPasskey(laptop, "")
	refused("an assertion without user verification", "1 try left", resp, body)
	// The count stops short of the limit, so the lab's own address is never locked: a good passkey
	// signs in and clears it.
	laptop.UserVerified = true
	laptop.Counter = 70
	if resp, body := visitor.signInWithPasskey(laptop, ""); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("a good passkey after four failures = %d:\n%s", resp.StatusCode, text(body))
	}

	// 9. Clean up through the console: remove both, with password sign-in on.
	for _, p := range []*labBrowser{web, oldDomain} {
		_, body := p.get("/settings/sign-in")
		for _, m := range regexp.MustCompile(`action="/settings/passkeys/([a-z2-7]{26})/remove"`).FindAllStringSubmatch(body, -1) {
			if resp, body := p.post("/settings/passkeys/"+m[1]+"/remove", nil); resp.StatusCode != http.StatusSeeOther {
				t.Fatalf("removing a passkey = %d:\n%s", resp.StatusCode, text(body))
			}
		}
	}
	if _, body := web.get("/settings/sign-in"); !strings.Contains(text(body), "No passkeys yet.") {
		t.Fatalf("passkeys are left:\n%s", text(body))
	}
	var state struct {
		PasswordSignIn bool `json:"passwordSignIn"`
		Passkeys       int  `json:"passkeys"`
	}
	g.getJSON(t, "/v1/account/sign-in", &state)
	if !state.PasswordSignIn || state.Passkeys != 0 {
		t.Fatalf("getSignIn = %+v, want password sign-in on and no passkeys", state)
	}

	// 8. Nothing private anywhere. A private key is in no answer, page or log line. A credential ID
	// isn't secret, and WebAuthn needs it in a ceremony's allowCredentials and excludeCredentials,
	// so it may be in the options answers (JSON with a ceremony) but in no page and no log line.
	var keys, ids []string
	for _, a := range []*webauthntest.Authenticator{laptop, elsewhere} {
		keys = append(keys, a.PrivateKeySentinel()...)
		ids = append(ids, base64.RawURLEncoding.EncodeToString(a.CredentialID()), hex.EncodeToString(a.CredentialID()))
	}
	db, _ := os.ReadFile(filepath.Join(g.target.DataDir, "caravana.db"))
	wal, _ := os.ReadFile(filepath.Join(g.target.DataDir, "caravana.db-wal"))
	var answers, pages []string
	for _, b := range []*labBrowser{ip, web, old, other, anon, visitor, oldDomain} {
		for _, body := range b.pages {
			answers = append(answers, body)
			if !strings.HasPrefix(body, `{"ceremony":`) {
				pages = append(pages, body)
			}
		}
	}
	log := g.target.ServerLog()
	everything := strings.Join(answers, "\n") + "\n" + log
	for _, s := range keys {
		if strings.Contains(everything, s) {
			t.Errorf("an answer or the server's log holds a private key (%s…)", s[:12])
		}
	}
	pagesAndLog := strings.Join(pages, "\n") + "\n" + log
	for _, s := range ids {
		if strings.Contains(pagesAndLog, s) {
			t.Errorf("a page or the server's log holds a credential ID (%s…)", s[:12])
		}
	}
	for _, a := range []*webauthntest.Authenticator{laptop, elsewhere} {
		for _, s := range a.PrivateKeySentinel() {
			if bytes.Contains(db, []byte(s)) || bytes.Contains(wal, []byte(s)) {
				t.Errorf("the database holds a private key (%s…)", s[:12])
			}
		}
	}
}
