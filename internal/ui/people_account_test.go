package ui

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/store"
)

// A second person signs in with their own email and password, sees their own email in the shell
// and their own tokens, and can't revoke anyone else's (feature 053).
func TestSecondPersonUsesTheirOwnAccount(t *testing.T) {
	ctx := context.Background()
	e := newUIEnv(t, true, "")
	err := e.Store.CreatePerson(ctx, store.PersonRecord{ID: "ana00000000000000000000000", Email: "ana@example.com", OrgRole: "member",
		State: "active", PasswordHash: "placeholder", PasswordSignIn: true, CreatedAt: e.Clock.Now()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Auth.ResetPassword(ctx, "ana@example.com", testPassword); err != nil {
		t.Fatal(err)
	}
	ownerToken, err := e.Auth.CreateAPIToken(ctx, e.OwnerID, "owner-ci", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Auth.CreateAPIToken(ctx, "ana00000000000000000000000", "ana-laptop", nil); err != nil {
		t.Fatal(err)
	}

	resp, body := e.do(t, request{method: http.MethodPost, path: "/login",
		form: url.Values{"email": {"Ana@Example.com"}, "password": {testPassword}}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("Ana's sign-in = %d, want 303; body:\n%s", resp.StatusCode, body)
	}
	var anaCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if strings.HasSuffix(c.Name, "caravana_session") {
			anaCookie = c
		}
	}
	if anaCookie == nil {
		t.Fatal("Ana's sign-in set no session cookie")
	}

	get := func(c *http.Cookie, path string) string {
		t.Helper()
		resp, body := e.do(t, request{path: path, cookies: []*http.Cookie{c}})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, resp.StatusCode)
		}
		return body
	}
	page := get(anaCookie, "/settings/tokens")
	if !strings.Contains(page, "ana-laptop") || strings.Contains(page, "owner-ci") {
		t.Errorf("Ana's tokens page lists the wrong tokens:\n%s", page)
	}
	if !strings.Contains(page, "ana@example.com") || strings.Contains(page, "you@example.com") {
		t.Errorf("the shell doesn't show Ana's email, or shows the owner's")
	}

	// Revoking the owner's token as Ana changes nothing.
	owner := e.signIn(t, nil)
	infos, _ := e.Auth.ListAPITokens(ctx, e.OwnerID)
	e.post(t, anaCookie, "/settings/tokens/"+infos[0].ID+"/revoke", nil)
	if _, err := e.Auth.CheckAPIToken(ctx, ownerToken.Token); err != nil {
		t.Errorf("the owner's token after Ana tried to revoke it: %v", err)
	}
	if !strings.Contains(get(owner, "/settings/tokens"), "owner-ci") {
		t.Error("the owner's own page lost their token")
	}
}
