package main

// The console step for email and Telegram channels (CodeAlta task c6v8), after
// console-notifications: the same queue, tests and removal as the webhook's, with a
// fake mail server and a fake Telegram Bot API in this process standing for the
// owner's. Like every lab step it runs only with CARAVANA_LAB=1.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/notify/smtptest"
)

// emailTelegram adds an email channel through the console form and a Telegram
// channel through the API, sends a test to each, and deploys an app with a tag
// that doesn't exist (which fails before anything is registered, in seconds) to see
// the email channel, which asks for failed deploys only, get exactly one message.
// The SMTP password and the bot token must be in no page, no list and no delivery.
func (c *console) emailTelegram(t *testing.T) {
	const (
		id       = "cmail"
		password = "s3cret-sentinel-c6v8-pass"
		token    = "123456789:s3cret-sentinel-c6v8-tokenAAAAAAAAAA"
	)
	removeApp := func() {
		if resp, err := c.g.apiRequest(http.MethodDelete, "/v1/apps/"+id, "", ""); err == nil {
			resp.Body.Close()
		}
	}
	removeApp() // a leftover from an aborted run, if any
	t.Cleanup(removeApp)

	// The fake mail server allows AUTH in plain: it is on loopback.
	mail := smtptest.Start(t, smtptest.Options{AuthUser: "caravana", AuthPassword: password, AllowPlainAuth: true})
	var mu sync.Mutex
	var botPaths, botBodies []string
	bot := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		botPaths, botBodies = append(botPaths, r.URL.Path), append(botBodies, string(raw))
		mu.Unlock()
		_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	t.Cleanup(bot.Close)

	var channels []struct{ ID, Name, Kind, Address string }
	channelNamed := func(name string) string {
		c.g.getJSON(t, "/v1/notifications/channels", &channels)
		for _, ch := range channels {
			if ch.Name == name {
				return ch.ID
			}
		}
		return ""
	}
	removeChannel := func(name string) {
		if chID := channelNamed(name); chID != "" {
			if resp, err := c.g.apiRequest(http.MethodDelete, "/v1/notifications/channels/"+chID, "", ""); err == nil {
				resp.Body.Close()
			}
		}
	}
	t.Cleanup(func() { removeChannel("lab mail"); removeChannel("lab telegram") })

	// The email channel, through the console form: failed deploys only.
	resp, body := c.post(t, "/settings/notifications", url.Values{
		"kind": {"email"}, "name": {"lab mail"}, "events": {"deploy_failed"},
		"smtpHost": {mail.Host()}, "smtpPort": {strconv.Itoa(mail.Port())}, "smtpSecurity": {"none"}, "smtpUsername": {"caravana"},
		"password": {password}, "fromAddress": {"caravana@lab.example.com"}, "toAddresses": {"owner@example.com"},
	})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /settings/notifications (email) = %d, want 303: %s", resp.StatusCode, body)
	}
	mailID := channelNamed("lab mail")
	if mailID == "" {
		t.Fatalf("GET /v1/notifications/channels = %+v, want the channel %q", channels, "lab mail")
	}

	// Send a test through the console: accepted, and the server got the message.
	resp, body = c.post(t, "/settings/notifications/"+mailID+"/test", url.Values{})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST send a test (email) = %d, want 303: %s", resp.StatusCode, body)
	}
	if _, page := c.get(t, resp.Header.Get("Location")); !strings.Contains(page, "lab mail accepted the test message.") {
		t.Errorf("Settings after the email test doesn't say it was accepted:\n%s", page)
	}
	subject := regexp.MustCompile(`(?m)^Subject: (.*)\r?$`)
	hasSubject := func(want string) func(smtptest.Message) bool {
		return func(m smtptest.Message) bool {
			s := subject.FindStringSubmatch(string(m.Data))
			return s != nil && strings.Contains(s[1], want)
		}
	}
	testMail, ok := mail.WaitFor(hasSubject("Test message from Caravana"), 10*time.Second)
	if !ok {
		t.Fatal("the mail server got no test message")
	}
	if testMail.AuthUser != "caravana" || testMail.From != "caravana@lab.example.com" || len(testMail.To) != 1 || testMail.To[0] != "owner@example.com" {
		t.Errorf("the test message = from %q to %v as %q, want the form's addresses and login", testMail.From, testMail.To, testMail.AuthUser)
	}

	// The Telegram channel, through the API, at the fake Bot API.
	payload, _ := json.Marshal(map[string]any{
		"kind": "telegram", "name": "lab telegram", "events": []string{"problems"}, "token": token,
		"telegram": map[string]string{"chatId": "-1001234567890", "apiUrl": bot.URL},
	})
	resp = c.g.api(t, http.MethodPost, "/v1/notifications/channels", "application/json", string(payload))
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /v1/notifications/channels (telegram) = %d, want 201: %s", resp.StatusCode, raw)
	}
	botID := channelNamed("lab telegram")
	resp = c.g.api(t, http.MethodPost, "/v1/notifications/channels/"+botID+"/test", "", "")
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	var delivery struct{ Status, Error string }
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &delivery) != nil || delivery.Status != "sent" {
		t.Fatalf("test of the Telegram channel = %d %s, want 200 with a sent delivery", resp.StatusCode, raw)
	}
	mu.Lock()
	gotPaths, gotBodies := append([]string(nil), botPaths...), append([]string(nil), botBodies...)
	mu.Unlock()
	if len(gotPaths) != 1 || gotPaths[0] != "/bot"+token+"/sendMessage" ||
		!strings.Contains(gotBodies[0], `"chat_id":"-1001234567890"`) || !strings.Contains(gotBodies[0], "Test message from Caravana") {
		t.Errorf("the Bot API got %v with %v, want sendMessage for the chat with the test text", gotPaths, gotBodies)
	}

	// A deploy that fails sends the email channel one message naming the app.
	definition := `{"source": {"kind": "image"}, "services": [{"name": "web", "image": "busybox:does-not-exist",
    "ports": [{"name": "http", "containerPort": 8080}], "health": {"path": "/", "interval": "5s", "timeout": "2s"}}]}`
	resp = c.g.api(t, http.MethodPost, "/v1/apps/"+id+"/deployments", "application/json", definition)
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode == http.StatusAccepted {
		t.Fatalf("deploy of a missing tag = 202, want it refused: %s", raw)
	}
	failedMail, ok := mail.WaitFor(hasSubject(id), 15*time.Second)
	if !ok {
		t.Fatalf("the mail server got no message naming %s within 15 s; it has %d messages", id, len(mail.Messages()))
	}
	if failedMail.AuthUser != "caravana" || !strings.Contains(string(failedMail.Data), "text/html") {
		t.Errorf("the deploy.failed message = %+v, want a login and an HTML part", failedMail)
	}
	// One more sender turn (5 s): the message wasn't sent twice.
	time.Sleep(7 * time.Second)
	named := 0
	for _, m := range mail.Messages() {
		if hasSubject(id)(m) {
			named++
		}
	}
	if named != 1 {
		t.Errorf("%d messages name %s, want exactly 1", named, id)
	}

	// The password and the bot token are in no page, no list and no delivery list.
	var seen []string
	_, page := c.get(t, "/settings/notifications")
	seen = append(seen, page)
	for _, path := range []string{"/v1/notifications/channels", "/v1/notifications/channels/" + mailID + "/deliveries", "/v1/notifications/channels/" + botID + "/deliveries"} {
		resp := c.g.api(t, http.MethodGet, path, "", "")
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		seen = append(seen, string(raw))
	}
	for i, out := range seen {
		for _, secret := range []string{password, "s3cret-sentinel-c6v8"} {
			if strings.Contains(out, secret) {
				t.Errorf("output %d holds a secret (%q)", i, secret)
			}
		}
	}

	// Remove: the email channel through the console, the Telegram one through the API;
	// both secret variables go (the email channel's password, the bot token).
	resp, body = c.post(t, "/settings/notifications/"+mailID+"/remove", url.Values{})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST remove the email channel = %d, want 303: %s", resp.StatusCode, body)
	}
	resp = c.g.api(t, http.MethodDelete, "/v1/notifications/channels/"+botID, "", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("DELETE the Telegram channel = %d, want 204", resp.StatusCode)
	}
	for _, chID := range []string{mailID, botID} {
		if _, _, err := c.g.target.Nomad.Variables().Read("caravana/notifications/"+chID, nil); err == nil {
			t.Errorf("Nomad variable caravana/notifications/%s is still there after the channel was removed", chID)
		}
	}
	resp = c.g.api(t, http.MethodDelete, "/v1/apps/"+id, "", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		t.Errorf("DELETE /v1/apps/%s = %d, want 204", id, resp.StatusCode)
	}
}
