package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/notify"
)

// notifySecret stands for the secret path of a Slack URL or an ntfy token: no
// answer, log line or error may contain it.
const notifySecret = "SENTINEL-notify-secret-0123"

// jsonBody makes a JSON object from alternating keys and raw JSON values.
func jsonBody(pairs ...string) string {
	var b strings.Builder
	b.WriteString("{")
	for i := 0; i < len(pairs); i += 2 {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`"` + pairs[i] + `":` + pairs[i+1])
	}
	b.WriteString("}")
	return b.String()
}

func quote(s string) string { return `"` + s + `"` }

// notifyReceiver answers every request with status and counts them.
func notifyReceiver(t *testing.T, status int) (url string, hits func() int) {
	t.Helper()
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		n++
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() int { return n }
}

// US3: the seven operations, their schemas, the 422 and 404 errors, and the
// secrets absent from every answer and from the request log.
func TestNotificationEndpoints(t *testing.T) {
	e := newTestEnv(t, "")
	doc := loadContract(t)
	base := e.URL + "/v1/notifications/channels"
	var seen []string // every body the API answered, to search for the secrets
	record := func(raw []byte) { seen = append(seen, string(raw)) }
	var signing []string // signing secrets the API showed, each allowed once

	okURL, okHits := notifyReceiver(t, http.StatusOK)
	badURL, _ := notifyReceiver(t, http.StatusInternalServerError)
	events := `["problems","deploy_failed"]`

	status, _, raw := call(t, http.MethodGet, base, "", "")
	if status != http.StatusOK || strings.TrimSpace(string(raw)) != "[]" {
		t.Fatalf("GET with none = %d %s, want 200 []", status, raw)
	}

	t.Run("422 field errors", func(t *testing.T) {
		for _, tc := range []struct{ name, body, field string }{
			{"unknown kind", jsonBody("kind", quote("pigeon"), "name", quote("x"), "url", quote(okURL), "events", events), "kind"},
			{"no name", jsonBody("kind", quote("webhook"), "name", quote(""), "url", quote(okURL), "events", events), "name"},
			{"no url", jsonBody("kind", quote("webhook"), "name", quote("x"), "url", quote(""), "events", events), "url"},
			{"not http", jsonBody("kind", quote("slack"), "name", quote("x"), "url", quote("ftp://hooks.slack.com/services/"+notifySecret), "events", events), "url"},
			{"no events", jsonBody("kind", quote("webhook"), "name", quote("x"), "url", quote(okURL), "events", "[]"), "events"},
			{"unknown event", jsonBody("kind", quote("webhook"), "name", quote("x"), "url", quote(okURL), "events", `["all"]`), "events"},
			{"token on a webhook", jsonBody("kind", quote("webhook"), "name", quote("x"), "url", quote(okURL), "token", quote("t"), "events", events), "token"},
		} {
			status, _, raw := call(t, http.MethodPost, base, "application/json", tc.body)
			record(raw)
			got := decodeAs[errorBody](t, raw).Error
			if status != http.StatusUnprocessableEntity || got.Code != codeInvalidField || got.Field != tc.field {
				t.Errorf("%s: POST = %d %s, want 422 invalid_field on %q", tc.name, status, raw, tc.field)
			}
			checkSchema(t, doc, raw, "Error")
		}
		status, _, raw := call(t, http.MethodPost, base, "application/json", `{"kind":"webhook","name":"x","url":"`+okURL+`","events":["problems"],"extra":1}`)
		if status != http.StatusBadRequest || decodeAs[errorBody](t, raw).Error.Code != codeUnknownField {
			t.Errorf("POST with an unknown field = %d %s, want 400 unknown_field", status, raw)
		}
		if status, _, raw := call(t, http.MethodGet, base, "", ""); strings.TrimSpace(string(raw)) != "[]" {
			t.Errorf("after the refusals GET = %d %s, want none stored", status, raw)
		}
	})

	var hookID, slackID, ntfyID string

	t.Run("create: the signing secret only for a webhook, and only here", func(t *testing.T) {
		status, _, raw := call(t, http.MethodPost, base, "application/json",
			jsonBody("kind", quote("webhook"), "name", quote("ci"), "url", quote(okURL+"/hook"), "events", events))
		record(raw)
		if status != http.StatusCreated {
			t.Fatalf("POST webhook = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "NotificationChannelCreated")
		hook := decodeAs[createdNotificationChannelJSON](t, raw)
		if len(hook.SigningSecret) != 64 || hook.Kind != "webhook" || hook.Address != okURL+"/hook" || hook.HasToken || hook.Paused {
			t.Fatalf("POST webhook answered %+v", hook)
		}
		hookID = hook.ID
		signing = append(signing, hook.SigningSecret)

		status, _, raw = call(t, http.MethodPost, base, "application/json",
			jsonBody("kind", quote("slack"), "name", quote("team"), "url", quote(okURL+"/services/T0/B0/"+notifySecret), "events", events))
		record(raw)
		if status != http.StatusCreated || strings.Contains(string(raw), "signingSecret") {
			t.Fatalf("POST slack = %d %s, want 201 without a signing secret", status, raw)
		}
		checkSchema(t, doc, raw, "NotificationChannelCreated")
		slack := decodeAs[notificationChannelJSON](t, raw)
		if !strings.HasSuffix(slack.Address, "/••••") || strings.Contains(slack.Address, notifySecret) {
			t.Errorf("slack address = %q, want the path masked", slack.Address)
		}
		slackID = slack.ID

		status, _, raw = call(t, http.MethodPost, base, "application/json",
			jsonBody("kind", quote("ntfy"), "name", quote("phone"), "url", quote(okURL+"/topic"), "token", quote("tk_"+notifySecret), "events", `["problems"]`, "paused", "true"))
		record(raw)
		ntfy := decodeAs[notificationChannelJSON](t, raw)
		if status != http.StatusCreated || !ntfy.HasToken || !ntfy.Paused || strings.Contains(string(raw), "signingSecret") {
			t.Fatalf("POST ntfy = %d %s, want 201, hasToken and paused", status, raw)
		}
		ntfyID = ntfy.ID
	})

	t.Run("list and the channel schema", func(t *testing.T) {
		status, _, raw := call(t, http.MethodGet, base, "", "")
		record(raw)
		list := decodeAs[[]notificationChannelJSON](t, raw)
		if status != http.StatusOK || len(list) != 3 || list[0].ID != hookID || list[1].ID != slackID || list[2].ID != ntfyID {
			t.Fatalf("GET = %d %s, want the three channels oldest first", status, raw)
		}
		// Every element matches the schema, which is closed: no signing secret.
		var elements []map[string]any
		if err := decodeInto(raw, &elements); err != nil {
			t.Fatal(err)
		}
		for _, el := range elements {
			if err := doc.Components.Schemas["NotificationChannel"].Value.VisitJSON(el); err != nil {
				t.Errorf("list element %v does not match NotificationChannel: %v", el, err)
			}
		}
	})

	t.Run("patch: keeps what it doesn't name", func(t *testing.T) {
		status, _, raw := call(t, http.MethodPatch, base+"/"+slackID, "application/json",
			jsonBody("name", quote("team-chat"), "paused", "true", "events", `["deploy_succeeded"]`))
		record(raw)
		got := decodeAs[notificationChannelJSON](t, raw)
		if status != http.StatusOK || got.Name != "team-chat" || !got.Paused || len(got.Events) != 1 || got.Events[0] != "deploy_succeeded" ||
			!strings.HasSuffix(got.Address, "/••••") {
			t.Fatalf("PATCH = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "NotificationChannel")

		// Token removal and an empty url.
		status, _, raw = call(t, http.MethodPatch, base+"/"+ntfyID, "application/json", jsonBody("token", quote("")))
		record(raw)
		if status != http.StatusOK || decodeAs[notificationChannelJSON](t, raw).HasToken {
			t.Errorf("PATCH token \"\" = %d %s, want hasToken false", status, raw)
		}
		status, _, raw = call(t, http.MethodPatch, base+"/"+ntfyID, "application/json", jsonBody("url", quote("")))
		record(raw)
		if status != http.StatusUnprocessableEntity || decodeAs[errorBody](t, raw).Error.Field != "url" {
			t.Errorf("PATCH empty url = %d %s, want 422 on url", status, raw)
		}
		status, _, raw = call(t, http.MethodPatch, base+"/"+hookID, "application/json", jsonBody("token", quote("x")))
		record(raw)
		if status != http.StatusUnprocessableEntity || decodeAs[errorBody](t, raw).Error.Field != "token" {
			t.Errorf("PATCH token on a webhook = %d %s, want 422 on token", status, raw)
		}
	})

	t.Run("test: sent, and a refusal is an outcome", func(t *testing.T) {
		status, _, raw := call(t, http.MethodPost, base+"/"+hookID+"/test", "", "")
		record(raw)
		d := decodeAs[deliveryJSON](t, raw)
		if status != http.StatusOK || d.Status != "sent" || d.EventType != "test" || d.Attempts != 1 || d.Error != "" || okHits() != 1 {
			t.Fatalf("test = %d %s (receiver hits %d), want 200 sent", status, raw, okHits())
		}
		checkSchema(t, doc, raw, "NotificationDelivery")

		// Point a channel at a receiver that answers 500.
		_, _, raw = call(t, http.MethodPost, base, "application/json",
			jsonBody("kind", quote("discord"), "name", quote("broken"), "url", quote(badURL+"/api/webhooks/1/"+notifySecret), "events", events))
		record(raw)
		brokenID := decodeAs[notificationChannelJSON](t, raw).ID
		status, _, raw = call(t, http.MethodPost, base+"/"+brokenID+"/test", "", "")
		record(raw)
		d = decodeAs[deliveryJSON](t, raw)
		if status != http.StatusOK || d.Status != "failed" || !strings.Contains(d.Error, "500") || strings.Contains(d.Error, notifySecret) {
			t.Fatalf("test of a failing receiver = %d %s, want 200 failed with the status and no secret", status, raw)
		}
		checkSchema(t, doc, raw, "NotificationDelivery")

		status, _, raw = call(t, http.MethodGet, base+"/"+brokenID+"/deliveries", "", "")
		record(raw)
		list := decodeAs[[]deliveryJSON](t, raw)
		if status != http.StatusOK || len(list) != 1 || list[0].Status != "failed" {
			t.Errorf("deliveries = %d %s, want the failed one", status, raw)
		}
		status, _, raw = call(t, http.MethodGet, base+"/"+hookID+"/deliveries", "", "")
		record(raw)
		if status != http.StatusOK || len(decodeAs[[]deliveryJSON](t, raw)) != 1 {
			t.Errorf("deliveries of the webhook = %d %s, want 1", status, raw)
		}
		if status, _, raw := call(t, http.MethodDelete, base+"/"+brokenID, "", ""); status != http.StatusNoContent {
			t.Errorf("DELETE broken = %d %s", status, raw)
		}
	})

	t.Run("rotate: a new secret, webhook only", func(t *testing.T) {
		status, _, raw := call(t, http.MethodPost, base+"/"+hookID+"/secret", "", "")
		record(raw)
		got := decodeAs[struct {
			SigningSecret string `json:"signingSecret"`
		}](t, raw)
		if status != http.StatusOK || len(got.SigningSecret) != 64 || got.SigningSecret == signing[0] {
			t.Fatalf("rotate = %d %s, want a new 64-digit secret", status, raw)
		}
		signing = append(signing, got.SigningSecret)

		status, _, raw = call(t, http.MethodPost, base+"/"+slackID+"/secret", "", "")
		record(raw)
		if status != http.StatusConflict || decodeAs[errorBody](t, raw).Error.Code != codeNotAWebhook {
			t.Errorf("rotate a Slack channel = %d %s, want 409 not_a_webhook", status, raw)
		}
	})

	t.Run("404", func(t *testing.T) {
		for _, tc := range []struct{ method, path, body string }{
			{http.MethodPatch, "/nch_000000000000", `{"name":"x"}`},
			{http.MethodDelete, "/nch_000000000000", ""},
			{http.MethodPost, "/nch_000000000000/test", ""},
			{http.MethodGet, "/nch_000000000000/deliveries", ""},
			{http.MethodPost, "/nch_000000000000/secret", ""},
		} {
			status, _, raw := call(t, tc.method, base+tc.path, "application/json", tc.body)
			record(raw)
			if status != http.StatusNotFound || decodeAs[errorBody](t, raw).Error.Code != codeNotFound {
				t.Errorf("%s %s = %d %s, want 404 not_found", tc.method, tc.path, status, raw)
			}
		}
	})

	t.Run("delete", func(t *testing.T) {
		if status, _, raw := call(t, http.MethodDelete, base+"/"+ntfyID, "", ""); status != http.StatusNoContent || len(raw) != 0 {
			t.Fatalf("DELETE = %d %s, want 204", status, raw)
		}
		status, _, raw := call(t, http.MethodGet, base, "", "")
		record(raw)
		if status != http.StatusOK || len(decodeAs[[]notificationChannelJSON](t, raw)) != 2 {
			t.Errorf("GET after a delete = %d %s, want 2", status, raw)
		}
		if v := e.Nomad.Variable("caravana/notifications/" + ntfyID); len(v) != 0 {
			t.Errorf("the deleted channel's variable is still there: %d items", len(v))
		}
	})

	t.Run("they need a token", func(t *testing.T) {
		for _, tc := range []struct{ method, path string }{
			{http.MethodGet, ""},
			{http.MethodPost, ""},
			{http.MethodPatch, "/" + hookID},
			{http.MethodDelete, "/" + hookID},
			{http.MethodPost, "/" + hookID + "/test"},
			{http.MethodGet, "/" + hookID + "/deliveries"},
			{http.MethodPost, "/" + hookID + "/secret"},
		} {
			status, _, raw := call(t, tc.method, e.RawURL+"/v1/notifications/channels"+tc.path, "application/json", `{}`)
			if status != http.StatusUnauthorized {
				t.Errorf("%s %s without a token = %d %s, want 401", tc.method, tc.path, status, raw)
			}
		}
	})

	// The secrets are in no answer except the two that show a signing secret
	// once, and in no log line.
	for _, s := range signing {
		for i, out := range seen {
			if strings.Contains(out, s) && !strings.Contains(out, `"signingSecret"`) {
				t.Errorf("output %d (%.80s...) holds a signing secret outside signingSecret", i, out)
			}
		}
		if strings.Contains(e.Logs.String(), s) {
			t.Error("the request log holds a signing secret")
		}
	}
	seen = append(seen, e.Logs.String())
	for i, out := range seen {
		if strings.Contains(out, notifySecret) {
			t.Errorf("output %d (%.80s...) contains the sentinel", i, out)
		}
	}
	// A signing secret shows in its own answer only, never twice.
	count := 0
	for _, out := range seen {
		for _, s := range signing {
			count += strings.Count(out, s)
		}
	}
	if count != len(signing) {
		t.Errorf("signing secrets were shown %d times, want %d (once each)", count, len(signing))
	}
}

// decodeInto decodes raw JSON into v.
func decodeInto(raw []byte, v any) error { return json.Unmarshal(raw, v) }

// The channel schemas are closed, so a token, a URL or a signing secret could
// not be added to a channel answer without the contract test noticing.
func TestNotificationSchemasHaveNoSecrets(t *testing.T) {
	doc := loadContract(t)
	channel := func() map[string]any {
		return map[string]any{
			"id": "nch_0123456789ab", "kind": "slack", "name": "n", "address": "hooks.slack.com/services/T…/••••",
			"events": []any{"problems"}, "paused": false, "hasToken": false,
			"createdAt": "2026-10-16T10:00:00Z", "updatedAt": "2026-10-16T10:00:00Z",
		}
	}
	if err := doc.Components.Schemas["NotificationChannel"].Value.VisitJSON(channel()); err != nil {
		t.Fatalf("NotificationChannel refused a plain channel: %v", err)
	}
	for _, extra := range []string{"url", "token", "signingSecret"} {
		body := channel()
		body[extra] = "x"
		if err := doc.Components.Schemas["NotificationChannel"].Value.VisitJSON(body); err == nil {
			t.Errorf("NotificationChannel accepted a %q property", extra)
		}
	}
	body := channel()
	body["signingSecret"] = "x"
	if err := doc.Components.Schemas["NotificationChannelCreated"].Value.VisitJSON(body); err != nil {
		t.Errorf("NotificationChannelCreated refused a signingSecret: %v", err)
	}
}

// TestNotificationEventSchema keeps the webhook message documented for
// receivers in step with what notify really sends.
func TestNotificationEventSchema(t *testing.T) {
	doc := loadContract(t)
	version := uint64(8)
	ev := notify.Event{
		Type: notify.EventDeployFailed, Kind: "deploy_failed", AppID: "hollo", Service: "web", Title: "t", Text: "x",
		Detail: "d", Deployment: "dep1", Version: &version, Outcome: "reverted", Trigger: "webhook",
		URL: "https://caravana.example.com/apps/hollo", At: time.Date(2026, 10, 17, 3, 0, 41, 0, time.UTC),
	}
	req, err := notify.Render(notify.Channel{Kind: notify.KindWebhook, Address: "https://hooks.example.com/h", Secret: "s"}, ev, "ndl_3f9a1c2b4d5e", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(req.Body)
	checkSchema(t, doc, raw, "NotificationEvent")

	// A minimal event leaves the optional fields out.
	req, err = notify.Render(notify.Channel{Kind: notify.KindWebhook, Address: "https://hooks.example.com/h", Secret: "s"},
		notify.Event{Type: notify.EventTest, Title: "t", Text: "x", At: ev.At}, "ndl_3f9a1c2b4d5e", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = io.ReadAll(req.Body)
	checkSchema(t, doc, raw, "NotificationEvent")

	// A problem about a schedule (feature 022) and a deploy from CI (feature 020).
	for _, ev := range []notify.Event{
		{Type: notify.EventProblemOpened, Kind: "schedule_failed", AppID: "booking", Service: "web", Title: "t", Text: "x",
			Detail: "Exited with code 3.", Schedule: "rates-sync", ExitCode: new(3), At: ev.At},
		{Type: notify.EventDeploySucceeded, AppID: "shop", Title: "t", Text: "x", At: ev.At,
			Origin: &notify.Origin{Commit: "abc1234", Ref: "refs/heads/main", Actor: "ana", Run: "https://ci.example.com/1"}},
		// Problems about a domain (feature 023).
		{Type: notify.EventProblemOpened, Kind: "domain_dns_elsewhere", AppID: "shop", Service: "web", Title: "t", Text: "x",
			Domain: "shop.example.com", AddressSeen: "198.51.100.9", At: ev.At},
		{Type: notify.EventProblemOpened, Kind: "domain_cert_expiring", AppID: "shop", Service: "web", Title: "t", Text: "x",
			Domain: "bi.example.com", ExpiresAt: new(ev.At.Add(time.Hour)), At: ev.At},
	} {
		req, err = notify.Render(notify.Channel{Kind: notify.KindWebhook, Address: "https://hooks.example.com/h", Secret: "s"},
			ev, "ndl_3f9a1c2b4d5e", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		raw, _ = io.ReadAll(req.Body)
		checkSchema(t, doc, raw, "NotificationEvent")
	}
}
