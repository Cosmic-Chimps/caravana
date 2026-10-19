package api

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

var apiTokenFormat = regexp.MustCompile(`^cvat_[A-Za-z0-9_-]{43}$`)

// tokenResponse is TokenInfo, plus the token on creation.
type tokenResponse struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	CreatedAt  string  `json:"createdAt"`
	LastUsedAt *string `json:"lastUsedAt"`
	ExpiresAt  *string `json:"expiresAt"`
	RevokedAt  *string `json:"revokedAt"`
	State      string  `json:"state"`
	Token      string  `json:"token"`
}

// callWith is call with an Authorization header (when auth is not "").
func callWith(t *testing.T, auth, method, url, contentType, body string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := noRedirects.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	var raw strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		raw.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, resp.Header, []byte(raw.String())
}

func createTokenOK(t *testing.T, e *testEnv, body string) tokenResponse {
	t.Helper()
	status, header, raw := call(t, http.MethodPost, e.URL+"/v1/tokens", "application/json", body)
	if status != http.StatusCreated {
		t.Fatalf("POST /v1/tokens %s = %d %s, want 201", body, status, raw)
	}
	checkSchema(t, loadContract(t), raw, "CreatedToken")
	tok := decodeAs[tokenResponse](t, raw)
	if loc := header.Get("Location"); loc != "/v1/tokens/"+tok.ID {
		t.Errorf("Location = %q, want /v1/tokens/%s", loc, tok.ID)
	}
	return tok
}

func TestCreateToken(t *testing.T) {
	e := newTestEnv(t, "")

	t.Run("201, shown once", func(t *testing.T) {
		tok := createTokenOK(t, e, `{"name":"ci-github"}`)
		if !apiTokenFormat.MatchString(tok.Token) || tok.Name != "ci-github" || tok.State != "active" || tok.ExpiresAt != nil {
			t.Errorf("created token = %+v", tok)
		}

		// The listing never has the value.
		_, _, raw := call(t, http.MethodGet, e.URL+"/v1/tokens", "", "")
		checkSchema(t, loadContract(t), raw, "TokenList")
		if strings.Contains(string(raw), tok.Token) || strings.Contains(string(raw), tok.Token[len("cvat_"):]) {
			t.Errorf("GET /v1/tokens shows the token value: %s", raw)
		}

		// And the new token works.
		if status, _, _ := callWith(t, "Bearer "+tok.Token, http.MethodGet, e.RawURL+"/v1/tokens", "", ""); status != http.StatusOK {
			t.Errorf("the new token got %d, want 200", status)
		}
	})

	t.Run("accepts a charset parameter", func(t *testing.T) {
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/tokens", "application/json; charset=UTF-8", `{"name":"charset"}`)
		if status != http.StatusCreated {
			t.Errorf("status %d %s, want 201", status, raw)
		}
	})

	t.Run("with an expiry", func(t *testing.T) {
		exp := e.Clock.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
		tok := createTokenOK(t, e, `{"name":"expiring","expiresAt":"`+exp+`"}`)
		if tok.ExpiresAt == nil || *tok.ExpiresAt != exp {
			t.Errorf("expiresAt = %v, want %s", tok.ExpiresAt, exp)
		}
	})

	t.Run("409 for a taken name", func(t *testing.T) {
		createTokenOK(t, e, `{"name":"taken"}`)
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/tokens", "application/json", `{"name":"taken"}`)
		if status != http.StatusConflict {
			t.Fatalf("status %d %s, want 409", status, raw)
		}
		body := decodeAs[errorBody](t, raw)
		if body.Error.Code != "token_name_taken" || body.Error.Field != "name" {
			t.Errorf("error = %+v, want token_name_taken on name", body.Error)
		}
		checkSchema(t, loadContract(t), raw, "Error")
	})

	t.Run("400s", func(t *testing.T) {
		past := e.Clock.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
		for name, tc := range map[string]struct {
			body, code, field string
		}{
			"no name":          {`{}`, "invalid_field", "name"},
			"empty name":       {`{"name":""}`, "invalid_field", "name"},
			"bad characters":   {`{"name":"has space"}`, "invalid_field", "name"},
			"too long":         {`{"name":"` + strings.Repeat("a", 65) + `"}`, "invalid_field", "name"},
			"past expiry":      {`{"name":"x","expiresAt":"` + past + `"}`, "invalid_field", "expiresAt"},
			"bad expiry":       {`{"name":"x","expiresAt":"tomorrow"}`, "invalid_field", "expiresAt"},
			"unknown field":    {`{"name":"x","color":"red"}`, "unknown_field", "color"},
			"scope wrong type": {`{"name":"x","scope":"all"}`, "invalid_field", "scope"},
			"not JSON":         {`{"name":`, "bad_request", ""},
		} {
			status, _, raw := call(t, http.MethodPost, e.URL+"/v1/tokens", "application/json", tc.body)
			if status != http.StatusBadRequest {
				t.Errorf("%s: status %d %s, want 400", name, status, raw)
				continue
			}
			body := decodeAs[errorBody](t, raw)
			if body.Error.Code != tc.code || body.Error.Field != tc.field {
				t.Errorf("%s: error = %+v, want %s on %q", name, body.Error, tc.code, tc.field)
			}
		}
	})

	t.Run("415 for a non-JSON body", func(t *testing.T) {
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/tokens", "text/plain", `name=x`)
		if status != http.StatusUnsupportedMediaType {
			t.Errorf("status %d %s, want 415", status, raw)
		}
	})
}

func TestListTokens(t *testing.T) {
	e := newTestEnv(t, "")
	// The environment's own "tests" token is the oldest.
	gone := createTokenOK(t, e, `{"name":"gone"}`)
	e.Clock.Advance(time.Minute)
	soon := e.Clock.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	createTokenOK(t, e, `{"name":"short","expiresAt":"`+soon+`"}`)
	if status, _, raw := call(t, http.MethodDelete, e.URL+"/v1/tokens/"+gone.ID, "", ""); status != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", status, raw)
	}
	e.Clock.Advance(2 * time.Hour)
	createTokenOK(t, e, `{"name":"fresh"}`)

	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/tokens", "", "")
	if status != http.StatusOK {
		t.Fatalf("list: %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "TokenList")
	var list struct {
		Tokens []tokenResponse `json:"tokens"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tok := range list.Tokens {
		got = append(got, tok.Name+":"+tok.State)
	}
	want := "fresh:active short:expired gone:revoked tests:active"
	if strings.Join(got, " ") != want {
		t.Errorf("list = %v, want %s", got, want)
	}
}

func TestRevokeToken(t *testing.T) {
	e := newTestEnv(t, "")
	tok := createTokenOK(t, e, `{"name":"victim"}`)
	auth := "Bearer " + tok.Token

	if status, _, _ := callWith(t, auth, http.MethodGet, e.RawURL+"/v1/tokens", "", ""); status != http.StatusOK {
		t.Fatalf("before revoke: %d, want 200", status)
	}
	if status, _, raw := call(t, http.MethodDelete, e.URL+"/v1/tokens/"+tok.ID, "", ""); status != http.StatusNoContent {
		t.Fatalf("revoke: %d %s, want 204", status, raw)
	}
	status, _, raw := callWith(t, auth, http.MethodGet, e.RawURL+"/v1/tokens", "", "")
	if status != http.StatusUnauthorized {
		t.Errorf("after revoke: %d %s, want 401", status, raw)
	}
	if status, _, _ := call(t, http.MethodDelete, e.URL+"/v1/tokens/"+tok.ID, "", ""); status != http.StatusNoContent {
		t.Errorf("revoke again: %d, want 204", status)
	}
	status, _, raw = call(t, http.MethodDelete, e.URL+"/v1/tokens/p4nzr2t6v3x7z5bqd7f4h6j3kq", "", "")
	if status != http.StatusNotFound {
		t.Errorf("revoke unknown: %d %s, want 404", status, raw)
	}

	// The list shows it revoked.
	_, _, raw = call(t, http.MethodGet, e.URL+"/v1/tokens", "", "")
	if !strings.Contains(string(raw), `"state":"revoked"`) {
		t.Errorf("the revoked token is not listed as revoked: %s", raw)
	}
}

func TestRevokeOwnToken(t *testing.T) {
	e := newTestEnv(t, "")
	tok := createTokenOK(t, e, `{"name":"suicide"}`)
	auth := "Bearer " + tok.Token

	if status, _, raw := callWith(t, auth, http.MethodDelete, e.RawURL+"/v1/tokens/"+tok.ID, "", ""); status != http.StatusNoContent {
		t.Fatalf("a token revoking itself: %d %s, want 204", status, raw)
	}
	if status, _, _ := callWith(t, auth, http.MethodGet, e.RawURL+"/v1/tokens", "", ""); status != http.StatusUnauthorized {
		t.Errorf("the next request with it: %d, want 401", status)
	}
}

func TestTokensNeverInTheLog(t *testing.T) {
	e := newTestEnv(t, "")
	tok := createTokenOK(t, e, `{"name":"quiet"}`)
	callWith(t, "Bearer "+tok.Token, http.MethodGet, e.RawURL+"/v1/tokens", "", "")
	call(t, http.MethodDelete, e.URL+"/v1/tokens/"+tok.ID, "", "")

	out := e.Logs.String()
	if strings.Contains(out, tok.Token) || strings.Contains(out, tok.Token[len("cvat_"):]) || strings.Contains(out, e.Token) {
		t.Errorf("an API token reached the log:\n%s", out)
	}
	if !strings.Contains(out, `"event":"token_created"`) || !strings.Contains(out, `"event":"token_revoked"`) {
		t.Errorf("the log lacks the token events:\n%s", out)
	}
}
