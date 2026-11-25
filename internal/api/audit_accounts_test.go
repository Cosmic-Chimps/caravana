package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/audit"
)

// Phase C of the audit log (feature 057), through the real handlers: the installation's settings and the
// accounts leave exactly one entry per request, noted by the service (never the fallback), and no secret
// is in any of them.

const apiSentinel = "s3cret-sentinel-057-api"

func TestAuditRegistryPutThroughTheAPI(t *testing.T) {
	e := newTestEnv(t, "")
	got := oneEntry(t, e, func() {
		status, _, raw := call(t, http.MethodPut, e.URL+"/v1/registries/ghcr.io", "application/json",
			`{"username": "ana", "password": "`+apiSentinel+`"}`)
		if status != http.StatusOK {
			t.Fatalf("putRegistry = %d %s", status, raw)
		}
	})
	if got.Action != audit.ActionCreated || got.Resource.Kind != audit.KindRegistry || got.Resource.ID != "ghcr.io" || got.Door != audit.DoorAPI {
		t.Errorf("entry = %+v", got)
	}
	if c := findChange(got.Changes, "password"); c == nil || c.Op != audit.OpAdded || c.From != "" || c.To != "" {
		t.Errorf("password = %+v, want added with no value", c)
	}
	// The same call again changes nothing: no entry, no fallback.
	n := auditRowCount(t, e.Store)
	if status, _, raw := call(t, http.MethodPut, e.URL+"/v1/registries/ghcr.io", "application/json", `{"username": "ana"}`); status != http.StatusOK {
		t.Fatalf("putRegistry again = %d %s", status, raw)
	}
	if rows := auditSince(t, e, n); len(rows) != 0 {
		t.Errorf("a put that changed nothing left %+v", rows)
	}
	if strings.Contains(allEntries(t, e), apiSentinel) {
		t.Error("the password is in an entry")
	}
}

func TestAuditNotificationChannelThroughTheAPI(t *testing.T) {
	e := newTestEnv(t, "")
	slack := "https://hooks.slack.com/services/T000/B000/" + apiSentinel
	var id string
	got := oneEntry(t, e, func() {
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/notifications/channels", "application/json",
			`{"kind": "slack", "name": "ops", "url": "`+slack+`", "events": ["problems"]}`)
		if status != http.StatusCreated {
			t.Fatalf("createNotificationChannel = %d %s", status, raw)
		}
		id = decodeAs[struct {
			ID string `json:"id"`
		}](t, raw).ID
	})
	if got.Action != audit.ActionCreated || got.Resource.Kind != audit.KindNotificationChannel || got.Resource.ID != id || got.Resource.Name != "ops" {
		t.Errorf("entry = %+v", got)
	}
	if c := findChange(got.Changes, "secret"); c == nil || c.Op != audit.OpAdded || c.To != "" {
		t.Errorf("the Slack address = %+v, want a redacted secret", c)
	}
	if all := allEntries(t, e); strings.Contains(all, apiSentinel) || strings.Contains(all, "hooks.slack.com/services/T000") {
		t.Error("the Slack address is in an entry")
	}
}

func TestAuditPersonInviteThroughTheAPI(t *testing.T) {
	e := newTestEnv(t, "")
	var token, id string
	got := oneEntry(t, e, func() {
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/people", "application/json",
			`{"email": "ana@example.com", "orgRole": "admin"}`)
		if status != http.StatusCreated {
			t.Fatalf("invitePerson = %d %s", status, raw)
		}
		out := decodeAs[struct {
			Person      struct{ ID string } `json:"person"`
			InviteToken string              `json:"inviteToken"`
		}](t, raw)
		token, id = out.InviteToken, out.Person.ID
	})
	if got.Action != audit.ActionInvited || got.Resource.Kind != audit.KindPerson || got.Resource.ID != id || got.Resource.Name != "ana@example.com" {
		t.Errorf("entry = %+v", got)
	}
	if c := findChange(got.Changes, "orgRole"); c == nil || c.To != "admin" {
		t.Errorf("changes = %+v", got.Changes)
	}
	if token == "" || strings.Contains(allEntries(t, e), token) {
		t.Errorf("the invite token (%q) is in an entry", token)
	}
}

func TestAuditTokenCreateAndRevokeThroughTheAPI(t *testing.T) {
	e := newTestEnv(t, "")
	var id, token string
	got := oneEntry(t, e, func() {
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/tokens", "application/json", `{"name": "laptop"}`)
		if status != http.StatusCreated {
			t.Fatalf("createToken = %d %s", status, raw)
		}
		out := decodeAs[struct {
			ID    string `json:"id"`
			Token string `json:"token"`
		}](t, raw)
		id, token = out.ID, out.Token
	})
	if got.Action != audit.ActionCreated || got.Resource.Kind != audit.KindToken || got.Resource.ID != id || got.Resource.Name != "laptop" {
		t.Errorf("create entry = %+v", got)
	}
	got = oneEntry(t, e, func() {
		if status, _, raw := call(t, http.MethodDelete, e.URL+"/v1/tokens/"+id, "", ""); status != http.StatusNoContent {
			t.Fatalf("revokeToken = %d %s", status, raw)
		}
	})
	if got.Action != audit.ActionRevoked || got.Resource.Kind != audit.KindToken || got.Resource.ID != id {
		t.Errorf("revoke entry = %+v", got)
	}
	if token == "" || strings.Contains(allEntries(t, e), token) {
		t.Error("the token is in an entry")
	}
}

func TestAuditSignInSettingsThroughTheAPI(t *testing.T) {
	e := newTestEnv(t, "")
	addTestPasskey(t, e, passkeyRP, "MacBook")
	got := oneEntry(t, e, func() {
		if status, _, raw := call(t, http.MethodPut, e.URL+"/v1/account/sign-in", "application/json", `{"passwordSignIn": false}`); status != http.StatusOK {
			t.Fatalf("putSignIn = %d %s", status, raw)
		}
	})
	if got.Action != audit.ActionUpdated || got.Resource.Kind != audit.KindSettings || got.Resource.ID != "sign-in" {
		t.Errorf("entry = %+v", got)
	}
	if c := findChange(got.Changes, "passwordSignIn"); c == nil || c.From != "true" || c.To != "false" {
		t.Errorf("changes = %+v", got.Changes)
	}
	if got.Actor.Kind != audit.ActorToken || got.Actor.Email == "" {
		t.Errorf("actor = %+v, want the owner through a token", got.Actor)
	}
	// Again: nothing changed, and no entry, no fallback.
	n := auditRowCount(t, e.Store)
	if status, _, raw := call(t, http.MethodPut, e.URL+"/v1/account/sign-in", "application/json", `{"passwordSignIn": false}`); status != http.StatusOK {
		t.Fatalf("putSignIn again = %d %s", status, raw)
	}
	if rows := auditSince(t, e, n); len(rows) != 0 {
		t.Errorf("a setting that changed nothing left %+v", rows)
	}
}
