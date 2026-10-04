package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

const newAdminPassword = "a brand new password"

func passwordBody(current, next string) string {
	return `{"currentPassword":"` + current + `","newPassword":"` + next + `"}`
}

func TestChangePasswordEndpoint(t *testing.T) {
	ctx := context.Background()

	t.Run("204, and the new password signs in", func(t *testing.T) {
		e := newTestEnv(t, "")
		status, _, raw := call(t, http.MethodPut, e.URL+"/v1/account/password", "application/json",
			passwordBody(testAdminPassword, newAdminPassword))
		if status != http.StatusNoContent || len(raw) != 0 {
			t.Fatalf("PUT = %d %s, want 204 with no body", status, raw)
		}
		if _, err := e.Auth.SignIn(ctx, testAdminEmail, newAdminPassword, "203.0.113.9"); err != nil {
			t.Errorf("the new password does not sign in: %v", err)
		}
		if _, err := e.Auth.SignIn(ctx, testAdminEmail, testAdminPassword, "203.0.113.9"); err == nil {
			t.Error("the old password still signs in")
		}
		// The API token that made the call keeps working.
		if status, _, _ := call(t, http.MethodGet, e.URL+"/v1/tokens", "", ""); status != http.StatusOK {
			t.Errorf("the API token stopped working: %d", status)
		}
	})

	t.Run("400 invalid_field on currentPassword and on newPassword", func(t *testing.T) {
		e := newTestEnv(t, "")
		for name, tc := range map[string]struct{ body, field string }{
			"wrong current":   {passwordBody("not the password", newAdminPassword), "currentPassword"},
			"empty current":   {passwordBody("", newAdminPassword), "currentPassword"},
			"short new":       {passwordBody(testAdminPassword, "short"), "newPassword"},
			"missing new":     {`{"currentPassword":"` + testAdminPassword + `"}`, "newPassword"},
			"missing current": {`{"newPassword":"` + newAdminPassword + `"}`, "currentPassword"},
		} {
			status, _, raw := call(t, http.MethodPut, e.URL+"/v1/account/password", "application/json", tc.body)
			if status != http.StatusBadRequest {
				t.Errorf("%s: %d %s, want 400", name, status, raw)
				continue
			}
			body := decodeAs[errorBody](t, raw)
			if body.Error.Code != "invalid_field" || body.Error.Field != tc.field {
				t.Errorf("%s: error = %+v, want invalid_field on %s", name, body.Error, tc.field)
			}
			if strings.Contains(string(raw), testAdminPassword) || strings.Contains(string(raw), newAdminPassword) {
				t.Errorf("%s: a password came back in the error: %s", name, raw)
			}
		}
		// Nothing changed.
		if _, err := e.Auth.SignIn(ctx, testAdminEmail, testAdminPassword, "203.0.113.9"); err != nil {
			t.Errorf("the password changed on refused requests: %v", err)
		}
	})

	t.Run("body rules", func(t *testing.T) {
		e := newTestEnv(t, "")
		if status, _, _ := call(t, http.MethodPut, e.URL+"/v1/account/password", "text/plain", "x"); status != http.StatusUnsupportedMediaType {
			t.Errorf("non-JSON body = %d, want 415", status)
		}
		status, _, raw := call(t, http.MethodPut, e.URL+"/v1/account/password", "application/json", `{"currentPassword":"x","newPassword":"y","extra":1}`)
		if status != http.StatusBadRequest || decodeAs[errorBody](t, raw).Error.Code != "unknown_field" {
			t.Errorf("unknown field = %d %s, want 400 unknown_field", status, raw)
		}
		if status, _, _ := call(t, http.MethodPut, e.URL+"/v1/account/password", "application/json; charset=UTF-8", passwordBody(testAdminPassword, newAdminPassword)); status != http.StatusNoContent {
			t.Errorf("charset parameter = %d, want 204", status)
		}
	})

	t.Run("401 without credentials", func(t *testing.T) {
		e := newTestEnv(t, "")
		status, _, raw := call(t, http.MethodPut, e.RawURL+"/v1/account/password", "application/json", passwordBody(testAdminPassword, newAdminPassword))
		if status != http.StatusUnauthorized {
			t.Errorf("PUT without credentials = %d %s, want 401", status, raw)
		}
	})

	t.Run("with a session cookie the calling session stays and the others end", func(t *testing.T) {
		e := newTestEnv(t, "")
		caller, other := sessionCookie(t, e), sessionCookie(t, e)

		status, raw := cookieCall(t, caller, http.MethodPut, e.RawURL+"/v1/account/password",
			passwordBody(testAdminPassword, newAdminPassword), nil)
		if status != http.StatusNoContent {
			t.Fatalf("PUT with a session = %d %s, want 204", status, raw)
		}
		if status, _ := cookieCall(t, caller, http.MethodGet, e.RawURL+"/v1/tokens", "", nil); status != http.StatusOK {
			t.Errorf("the calling session ended: %d", status)
		}
		if status, _ := cookieCall(t, other, http.MethodGet, e.RawURL+"/v1/tokens", "", nil); status != http.StatusUnauthorized {
			t.Errorf("another session survived: %d, want 401", status)
		}
	})

	t.Run("with an API token every session ends", func(t *testing.T) {
		e := newTestEnv(t, "")
		c := sessionCookie(t, e)
		if status, _, raw := call(t, http.MethodPut, e.URL+"/v1/account/password", "application/json", passwordBody(testAdminPassword, newAdminPassword)); status != http.StatusNoContent {
			t.Fatalf("PUT = %d %s", status, raw)
		}
		if status, _ := cookieCall(t, c, http.MethodGet, e.RawURL+"/v1/tokens", "", nil); status != http.StatusUnauthorized {
			t.Errorf("a session survived a password change made by token: %d, want 401", status)
		}
	})

	t.Run("a cross-site cookie request is refused", func(t *testing.T) {
		e := newTestEnv(t, "")
		c := sessionCookie(t, e)
		status, raw := cookieCall(t, c, http.MethodPut, e.RawURL+"/v1/account/password",
			passwordBody(testAdminPassword, newAdminPassword), map[string]string{"Sec-Fetch-Site": "cross-site"})
		if status != http.StatusForbidden {
			t.Fatalf("cross-site PUT = %d %s, want 403", status, raw)
		}
		if _, err := e.Auth.SignIn(ctx, testAdminEmail, testAdminPassword, "203.0.113.9"); err != nil {
			t.Errorf("the password changed on a cross-site request: %v", err)
		}
	})

	t.Run("the response matches the contract", func(t *testing.T) {
		e := newTestEnv(t, "")
		_, _, raw := call(t, http.MethodPut, e.URL+"/v1/account/password", "application/json", passwordBody("wrong password!!", newAdminPassword))
		checkSchema(t, loadContract(t), raw, "Error")
	})
}
