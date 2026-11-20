package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/notify/smtptest"
)

// Values no answer, log line or store row may hold (task c6v8).
const (
	apiMailPassword = "s3cret-sentinel-c6v8-pass"
	apiBotToken     = "123456789:s3cret-sentinel-c6v8-tokenAAAAAAAAAA"
)

// Email and Telegram channels through the API: create, list, patch, test and the
// field errors, with the schemas checked and the password and bot token in no
// answer, log line or store file.
func TestEmailAndTelegramChannelEndpoints(t *testing.T) {
	e := newTestEnv(t, "")
	doc := loadContract(t)
	base := e.URL + "/v1/notifications/channels"
	events := `["problems","deploy_failed"]`
	var seen []string
	record := func(raw []byte) { seen = append(seen, string(raw)) }

	mail := smtptest.Start(t, smtptest.Options{AuthUser: "caravana", AuthPassword: apiMailPassword, AllowPlainAuth: true})
	var botPaths, botBodies []string
	bot := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		botPaths, botBodies = append(botPaths, r.URL.Path), append(botBodies, string(raw))
		_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	t.Cleanup(bot.Close)

	emailBody := func(extra ...string) string {
		settings := `{"host":"` + mail.Host() + `","port":` + itoa(mail.Port()) + `,"security":"none","username":"caravana","from":"Caravana <ops@example.com>","to":["ana@example.com","ben@example.com"]}`
		pairs := append([]string{"kind", quote("email"), "name", quote("mail"), "events", events, "email", settings, "password", quote(apiMailPassword)}, extra...)
		return jsonBody(pairs...)
	}

	t.Run("422 field errors", func(t *testing.T) {
		bad := func(host, to string) string {
			return jsonBody("kind", quote("email"), "name", quote("x"), "events", events,
				"email", `{"host":`+quote(host)+`,"security":"none","from":"ops@example.com","to":`+to+`}`)
		}
		for _, tc := range []struct{ name, body, field string }{
			{"no email settings", jsonBody("kind", quote("email"), "name", quote("x"), "events", events), "email"},
			{"a url on email", emailBody("url", quote("https://x.example.com")), "url"},
			{"a bad host", bad("mail.example.com:25", `["a@example.com"]`), "email.host"},
			{"a bad second recipient", bad("localhost", `["a@example.com","nobody"]`), "email.to[1]"},
			{"no telegram settings", jsonBody("kind", quote("telegram"), "name", quote("x"), "events", events, "token", quote(apiBotToken)), "telegram"},
			{"a bad chat", jsonBody("kind", quote("telegram"), "name", quote("x"), "events", events, "token", quote(apiBotToken), "telegram", `{"chatId":"my chat"}`), "telegram.chatId"},
			{"a bad bot token", jsonBody("kind", quote("telegram"), "name", quote("x"), "events", events, "token", quote("s3cret-sentinel-c6v8-nope"), "telegram", `{"chatId":"1"}`), "token"},
			{"no bot token", jsonBody("kind", quote("telegram"), "name", quote("x"), "events", events, "telegram", `{"chatId":"1"}`), "token"},
		} {
			status, _, raw := call(t, http.MethodPost, base, "application/json", tc.body)
			record(raw)
			got := decodeAs[errorBody](t, raw).Error
			if status != http.StatusUnprocessableEntity || got.Code != codeInvalidField || got.Field != tc.field {
				t.Errorf("%s: POST = %d %s, want 422 invalid_field on %q", tc.name, status, raw, tc.field)
			}
			checkSchema(t, doc, raw, "Error")
		}
		if status, _, raw := call(t, http.MethodGet, base, "", ""); strings.TrimSpace(string(raw)) != "[]" {
			t.Errorf("after the refusals GET = %d %s, want none stored", status, raw)
		}
	})

	var mailID, botID string
	t.Run("create", func(t *testing.T) {
		status, _, raw := call(t, http.MethodPost, base, "application/json", emailBody())
		record(raw)
		if status != http.StatusCreated || strings.Contains(string(raw), "signingSecret") || strings.Contains(string(raw), "password") {
			t.Fatalf("POST email = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "NotificationChannelCreated")
		got := decodeAs[notificationChannelJSON](t, raw)
		if got.Kind != "email" || !got.HasToken || got.Email == nil || got.Email.Port != mail.Port() || got.Email.Username != "caravana" ||
			len(got.Email.To) != 2 || !strings.HasPrefix(got.Address, "ana@example.com, ben@example.com via ") {
			t.Fatalf("POST email answered %+v", got)
		}
		mailID = got.ID

		status, _, raw = call(t, http.MethodPost, base, "application/json", jsonBody("kind", quote("telegram"), "name", quote("chat"),
			"events", events, "token", quote(apiBotToken), "telegram", `{"chatId":"-1001234567890","apiUrl":`+quote(bot.URL)+`}`))
		record(raw)
		if status != http.StatusCreated {
			t.Fatalf("POST telegram = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "NotificationChannelCreated")
		tg := decodeAs[notificationChannelJSON](t, raw)
		if tg.Kind != "telegram" || !tg.HasToken || tg.Telegram == nil || tg.Telegram.ChatID != "-1001234567890" || tg.Telegram.APIURL != bot.URL ||
			tg.Address != "chat -1001234567890 on "+strings.TrimPrefix(bot.URL, "http://") {
			t.Fatalf("POST telegram answered %+v", tg)
		}
		botID = tg.ID
	})

	t.Run("list matches the schema and holds no secret", func(t *testing.T) {
		status, _, raw := call(t, http.MethodGet, base, "", "")
		record(raw)
		list := decodeAs[[]notificationChannelJSON](t, raw)
		if status != http.StatusOK || len(list) != 2 || list[0].ID != mailID || list[1].ID != botID || list[0].Email == nil || list[1].Telegram == nil {
			t.Fatalf("GET = %d %s", status, raw)
		}
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

	t.Run("test", func(t *testing.T) {
		status, _, raw := call(t, http.MethodPost, base+"/"+mailID+"/test", "", "")
		record(raw)
		d := decodeAs[deliveryJSON](t, raw)
		if status != http.StatusOK || d.Status != "sent" || d.Error != "" {
			t.Fatalf("test of the email channel = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "NotificationDelivery")
		msg, ok := mail.WaitFor(func(m smtptest.Message) bool { return strings.Contains(string(m.Data), "Test message from Caravana") }, time.Second)
		if !ok || msg.AuthUser != "caravana" || len(msg.To) != 2 {
			t.Errorf("the mail server got %+v (ok %v)", msg, ok)
		}

		status, _, raw = call(t, http.MethodPost, base+"/"+botID+"/test", "", "")
		record(raw)
		d = decodeAs[deliveryJSON](t, raw)
		if status != http.StatusOK || d.Status != "sent" || len(botPaths) != 1 || botPaths[0] != "/bot"+apiBotToken+"/sendMessage" ||
			!strings.Contains(botBodies[0], `"chat_id":"-1001234567890"`) {
			t.Fatalf("test of the telegram channel = %d %s; the bot API got %v %v", status, raw, botPaths, botBodies)
		}

		status, _, raw = call(t, http.MethodGet, base+"/"+mailID+"/deliveries", "", "")
		record(raw)
		if status != http.StatusOK || len(decodeAs[[]deliveryJSON](t, raw)) != 1 {
			t.Errorf("deliveries = %d %s", status, raw)
		}
	})

	t.Run("patch", func(t *testing.T) {
		// Settings replace whole; the password and name are kept.
		status, _, raw := call(t, http.MethodPatch, base+"/"+mailID, "application/json", jsonBody("email",
			`{"host":`+quote(mail.Host())+`,"port":`+itoa(mail.Port())+`,"security":"none","from":"ops@example.com","to":["dev@example.com"]}`))
		record(raw)
		got := decodeAs[notificationChannelJSON](t, raw)
		if status != http.StatusOK || got.Name != "mail" || !got.HasToken || got.Email.Username != "" || len(got.Email.To) != 1 {
			t.Fatalf("PATCH email = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "NotificationChannel")

		status, _, raw = call(t, http.MethodPatch, base+"/"+mailID, "application/json", jsonBody("password", quote("")))
		record(raw)
		if status != http.StatusOK || decodeAs[notificationChannelJSON](t, raw).HasToken {
			t.Errorf("PATCH password \"\" = %d %s, want hasToken false", status, raw)
		}

		status, _, raw = call(t, http.MethodPatch, base+"/"+botID, "application/json", jsonBody("token", quote("")))
		record(raw)
		if status != http.StatusUnprocessableEntity || decodeAs[errorBody](t, raw).Error.Field != "token" {
			t.Errorf("PATCH token \"\" on telegram = %d %s, want 422 on token", status, raw)
		}
		status, _, raw = call(t, http.MethodPatch, base+"/"+botID, "application/json", jsonBody("email", `{"host":"x"}`))
		record(raw)
		if status != http.StatusUnprocessableEntity || decodeAs[errorBody](t, raw).Error.Field != "email" {
			t.Errorf("PATCH email on telegram = %d %s, want 422 on email", status, raw)
		}
		status, _, raw = call(t, http.MethodPatch, base+"/"+botID, "application/json", jsonBody("telegram", `{"chatId":"@caravana_ops"}`))
		record(raw)
		if tg := decodeAs[notificationChannelJSON](t, raw); status != http.StatusOK || tg.Address != "chat @caravana_ops" || tg.Telegram.APIURL != "" {
			t.Errorf("PATCH telegram = %d %s", status, raw)
		}
	})

	t.Run("delete", func(t *testing.T) {
		for _, id := range []string{mailID, botID} {
			if status, _, raw := call(t, http.MethodDelete, base+"/"+id, "", ""); status != http.StatusNoContent {
				t.Errorf("DELETE %s = %d %s", id, status, raw)
			}
			if v := e.Nomad.Variable("caravana/notifications/" + id); len(v) != 0 {
				t.Errorf("the deleted channel's variable is still there: %d items", len(v))
			}
		}
	})

	// The password and the bot token are in no answer, no log line, no store file.
	seen = append(seen, e.Logs.String())
	for i, out := range seen {
		for _, s := range []string{apiMailPassword, "s3cret-sentinel-c6v8"} {
			if strings.Contains(out, s) {
				t.Errorf("output %d (%.80s...) holds a secret", i, out)
			}
		}
	}
}
