package api

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/webauthntest"
)

var (
	passkeyRP    = service.RelyingParty{ID: "console.example.com", Origin: "https://console.example.com"}
	passkeyOther = service.RelyingParty{ID: "old.example.com", Origin: "https://old.example.com"}
)

const passkeyAddress = "203.0.113.9"

// addTestPasskey adds a passkey for rp, as the console would: sign in (which confirms the
// session), begin, let a software authenticator answer, finish.
func addTestPasskey(t *testing.T, e *testEnv, rp service.RelyingParty, name string) *webauthntest.Authenticator {
	t.Helper()
	ctx := context.Background()
	session, err := e.Auth.SignIn(ctx, testAdminEmail, testAdminPassword, "", passkeyAddress)
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	opts, err := e.Auth.BeginPasskeyRegistration(ctx, session.Value, rp, passkeyAddress)
	if err != nil {
		t.Fatalf("BeginPasskeyRegistration: %v", err)
	}
	a := webauthntest.New(rp.ID)
	posted, err := a.Create(opts.Options, rp.Origin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Auth.FinishPasskeyRegistration(ctx, session.Value, rp, name, opts.Ceremony, posted); err != nil {
		t.Fatalf("FinishPasskeyRegistration: %v", err)
	}
	return a
}

type passkeyResponse struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Domain     string   `json:"domain"`
	CreatedAt  string   `json:"createdAt"`
	LastUsedAt *string  `json:"lastUsedAt"`
	Transports []string `json:"transports"`
	BackedUp   bool     `json:"backedUp"`
}

type passkeyList struct {
	Passkeys []json.RawMessage `json:"passkeys"`
}

func listPasskeyResponses(t *testing.T, e *testEnv) []passkeyResponse {
	t.Helper()
	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/account/passkeys", "", "")
	if status != http.StatusOK {
		t.Fatalf("GET /v1/account/passkeys = %d %s", status, raw)
	}
	doc := loadContract(t)
	var out []passkeyResponse
	for _, item := range decodeAs[passkeyList](t, raw).Passkeys {
		checkSchema(t, doc, item, "Passkey")
		out = append(out, decodeAs[passkeyResponse](t, item))
	}
	return out
}

func TestListPasskeys(t *testing.T) {
	e := newTestEnv(t, "")

	t.Run("none yet is an empty list", func(t *testing.T) {
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/account/passkeys", "", "")
		if status != http.StatusOK || strings.TrimSpace(string(raw)) != `{"passkeys":[]}` {
			t.Errorf("GET = %d %s, want 200 {\"passkeys\":[]}", status, raw)
		}
	})

	t.Run("oldest first, with what the contract says and nothing the authenticator holds", func(t *testing.T) {
		a := addTestPasskey(t, e, passkeyRP, "MacBook")
		addTestPasskey(t, e, passkeyOther, "Old domain")
		list := listPasskeyResponses(t, e)
		if len(list) != 2 {
			t.Fatalf("list = %+v, want 2", list)
		}
		first := list[0]
		if first.Name != "MacBook" || first.Domain != "console.example.com" || first.LastUsedAt != nil ||
			first.BackedUp || len(first.ID) != 26 || len(first.Transports) != 1 || first.Transports[0] != "internal" {
			t.Errorf("first passkey = %+v", first)
		}
		if list[1].Domain != "old.example.com" {
			t.Errorf("second passkey = %+v", list[1])
		}

		_, _, raw := call(t, http.MethodGet, e.URL+"/v1/account/passkeys", "", "")
		leaks := []string{base64.RawURLEncoding.EncodeToString(a.CredentialID()), hex.EncodeToString(a.CredentialID()),
			"credentialId", "publicKey", "signCount"}
		leaks = append(leaks, a.PrivateKeySentinel()...)
		for _, leak := range leaks {
			if strings.Contains(string(raw), leak) {
				t.Errorf("the list holds %q", leak)
			}
		}
	})

	t.Run("it needs credentials", func(t *testing.T) {
		status, _, raw := call(t, http.MethodGet, e.RawURL+"/v1/account/passkeys", "", "")
		wantError(t, "no credentials", status, raw, http.StatusUnauthorized, codeUnauthorized, "")
	})
}

func TestUpdatePasskey(t *testing.T) {
	e := newTestEnv(t, "")
	addTestPasskey(t, e, passkeyRP, "MacBook")
	id := listPasskeyResponses(t, e)[0].ID
	url := e.URL + "/v1/account/passkeys/" + id

	t.Run("200 with the passkey", func(t *testing.T) {
		status, _, raw := call(t, http.MethodPatch, url, "application/json", `{"name": "  Work laptop "}`)
		if status != http.StatusOK {
			t.Fatalf("PATCH = %d %s", status, raw)
		}
		checkSchema(t, loadContract(t), raw, "Passkey")
		got := decodeAs[passkeyResponse](t, raw)
		if got.ID != id || got.Name != "Work laptop" || got.Domain != "console.example.com" {
			t.Errorf("answer = %+v", got)
		}
		if list := listPasskeyResponses(t, e); list[0].Name != "Work laptop" {
			t.Errorf("listed name = %q", list[0].Name)
		}
	})

	t.Run("422 for a bad name", func(t *testing.T) {
		for _, name := range []string{`""`, `"   "`, `"` + strings.Repeat("a", 65) + `"`, `"two\nlines"`} {
			status, _, raw := call(t, http.MethodPatch, url, "application/json", `{"name": `+name+`}`)
			wantError(t, "name "+name, status, raw, http.StatusUnprocessableEntity, codeInvalidField, "name")
		}
		if list := listPasskeyResponses(t, e); list[0].Name != "Work laptop" {
			t.Errorf("a refused rename changed the name to %q", list[0].Name)
		}
	})

	t.Run("404 for an unknown passkey", func(t *testing.T) {
		status, _, raw := call(t, http.MethodPatch, e.URL+"/v1/account/passkeys/nope", "application/json", `{"name": "x"}`)
		wantError(t, "unknown passkey", status, raw, http.StatusNotFound, codePasskeyNotFound, "")
	})

	t.Run("a body the API refuses", func(t *testing.T) {
		if status, _, _ := call(t, http.MethodPatch, url, "text/plain", "x"); status != http.StatusUnsupportedMediaType {
			t.Errorf("text/plain = %d, want 415", status)
		}
		status, _, raw := call(t, http.MethodPatch, url, "application/json", `{"name": "x", "extra": 1}`)
		wantError(t, "unknown field", status, raw, http.StatusBadRequest, codeUnknownField, "extra")
	})
}

func TestDeletePasskey(t *testing.T) {
	e := newTestEnv(t, "")
	addTestPasskey(t, e, passkeyRP, "MacBook")
	id := listPasskeyResponses(t, e)[0].ID

	t.Run("409 while it is the last passkey of its domain and password sign-in is off", func(t *testing.T) {
		if status, _, raw := call(t, http.MethodPut, e.URL+"/v1/account/sign-in", "application/json", `{"passwordSignIn": false}`); status != http.StatusOK {
			t.Fatalf("turning password sign-in off = %d %s", status, raw)
		}
		status, _, raw := call(t, http.MethodDelete, e.URL+"/v1/account/passkeys/"+id, "", "")
		body := wantError(t, "delete the last passkey", status, raw, http.StatusConflict, codeLastPasskey, "")
		if !strings.Contains(body.Error.Message, "console.example.com") || !strings.Contains(body.Error.Message, "turn password sign-in on first") {
			t.Errorf("message = %q", body.Error.Message)
		}
		if len(listPasskeyResponses(t, e)) != 1 {
			t.Error("a refused delete removed the passkey")
		}
	})

	t.Run("204, then 404", func(t *testing.T) {
		if status, _, raw := call(t, http.MethodPut, e.URL+"/v1/account/sign-in", "application/json", `{"passwordSignIn": true}`); status != http.StatusOK {
			t.Fatalf("turning password sign-in on = %d %s", status, raw)
		}
		if status, _, raw := call(t, http.MethodDelete, e.URL+"/v1/account/passkeys/"+id, "", ""); status != http.StatusNoContent || len(raw) != 0 {
			t.Fatalf("DELETE = %d %q, want 204 and no body", status, raw)
		}
		status, _, raw := call(t, http.MethodDelete, e.URL+"/v1/account/passkeys/"+id, "", "")
		wantError(t, "delete again", status, raw, http.StatusNotFound, codePasskeyNotFound, "")
		if len(listPasskeyResponses(t, e)) != 0 {
			t.Error("the passkey is still listed")
		}
	})
}

func TestSignInSetting(t *testing.T) {
	signIn := func(t *testing.T, e *testEnv) (int, []byte) {
		t.Helper()
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/account/sign-in", "", "")
		return status, raw
	}
	put := func(t *testing.T, e *testEnv, body string) (int, []byte) {
		t.Helper()
		status, _, raw := call(t, http.MethodPut, e.URL+"/v1/account/sign-in", "application/json", body)
		return status, raw
	}

	t.Run("password on and no passkeys by default", func(t *testing.T) {
		e := newTestEnv(t, "")
		status, raw := signIn(t, e)
		checkSchema(t, loadContract(t), raw, "SignIn")
		if status != http.StatusOK || strings.TrimSpace(string(raw)) != `{"passwordSignIn":true,"passkeys":0}` {
			t.Errorf("GET = %d %s", status, raw)
		}
	})

	t.Run("without a public URL any passkey will do", func(t *testing.T) {
		e := newTestEnv(t, "")
		status, raw := put(t, e, `{"passwordSignIn": false}`)
		wantError(t, "off with no passkey", status, raw, http.StatusConflict, codeNoPasskey, "")

		addTestPasskey(t, e, passkeyOther, "Old domain")
		status, raw = put(t, e, `{"passwordSignIn": false}`)
		checkSchema(t, loadContract(t), raw, "SignIn")
		if status != http.StatusOK || strings.TrimSpace(string(raw)) != `{"passwordSignIn":false,"passkeys":1}` {
			t.Errorf("PUT off = %d %s", status, raw)
		}
		if _, raw := signIn(t, e); !strings.Contains(string(raw), `"passwordSignIn":false`) {
			t.Errorf("GET after = %s", raw)
		}

		status, raw = put(t, e, `{"passwordSignIn": true}`)
		if status != http.StatusOK || !strings.Contains(string(raw), `"passwordSignIn":true`) {
			t.Errorf("PUT on = %d %s", status, raw)
		}
	})

	t.Run("with a public URL the passkey must be for its host", func(t *testing.T) {
		e := newTestEnvWith(t, "", func(s *service.Service) { s.SetPublicURL("https://Console.Example.com:8443") })
		addTestPasskey(t, e, passkeyOther, "Old domain")
		status, raw := put(t, e, `{"passwordSignIn": false}`)
		body := wantError(t, "off with a passkey for another domain", status, raw, http.StatusConflict, codeNoPasskey, "")
		if !strings.Contains(body.Error.Message, "console.example.com") {
			t.Errorf("message = %q, want the console's domain", body.Error.Message)
		}
		addTestPasskey(t, e, passkeyRP, "MacBook")
		if status, raw := put(t, e, `{"passwordSignIn": false}`); status != http.StatusOK {
			t.Errorf("off with a passkey for the console's domain = %d %s", status, raw)
		}
	})

	t.Run("a body without the field is refused, not read as off", func(t *testing.T) {
		e := newTestEnv(t, "")
		addTestPasskey(t, e, passkeyRP, "MacBook")
		status, raw := put(t, e, `{}`)
		wantError(t, "empty body", status, raw, http.StatusUnprocessableEntity, codeInvalidField, "passwordSignIn")
		if _, raw := signIn(t, e); !strings.Contains(string(raw), `"passwordSignIn":true`) {
			t.Errorf("an empty body changed the setting: %s", raw)
		}
		status, raw = put(t, e, `{"passwordSignIn": "off"}`)
		if status != http.StatusBadRequest {
			t.Errorf("a string instead of a boolean = %d %s, want 400", status, raw)
		}
	})
}

func TestPasskeyRoutesRefuseDeployTokens(t *testing.T) {
	e := newTestEnv(t, "")
	shopProject(t, e)
	auth := scopedToken(t, e, "shop", "web")
	addTestPasskey(t, e, passkeyRP, "MacBook")
	id := listPasskeyResponses(t, e)[0].ID

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/account/passkeys", ""},
		{http.MethodPatch, "/v1/account/passkeys/" + id, `{"name":"x"}`},
		{http.MethodDelete, "/v1/account/passkeys/" + id, ""},
		{http.MethodGet, "/v1/account/sign-in", ""},
		{http.MethodPut, "/v1/account/sign-in", `{"passwordSignIn":false}`},
	} {
		status, _, raw := callWith(t, auth, tc.method, e.RawURL+tc.path, "application/json", tc.body)
		wantError(t, tc.method+" "+tc.path+" with a deploy token", status, raw, http.StatusForbidden, codeTokenScope, "")
	}
	if len(listPasskeyResponses(t, e)) != 1 {
		t.Error("a deploy token changed the passkeys")
	}
}
