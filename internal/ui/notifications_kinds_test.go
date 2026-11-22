package ui

import (
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

// Values no page, redirect or log line may hold (task c6v8).
const (
	uiMailPassword = "s3cret-sentinel-c6v8-pass"
	uiBotToken     = "123456789:s3cret-sentinel-c6v8-tokenAAAAAAAAAA"
)

// botAPI is a fake Telegram Bot API.
type botAPI struct {
	*httptest.Server
	mu     sync.Mutex
	paths  []string
	bodies []string
}

func newBotAPI(t *testing.T) *botAPI {
	t.Helper()
	b := &botAPI{}
	b.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		b.mu.Lock()
		b.paths, b.bodies = append(b.paths, r.URL.Path), append(b.bodies, string(raw))
		b.mu.Unlock()
		_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	t.Cleanup(b.Close)
	return b
}

func (b *botAPI) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.paths)
}

// Email and Telegram channels through the console's forms: the add form draws every
// kind's inputs, reads the chosen kind's, keeps no secret in a page, and each row
// edits, tests and removes like the others.
func TestSettingsEmailAndTelegramChannels(t *testing.T) {
	e := newConsoleEnv(t)
	mail := smtptest.Start(t, smtptest.Options{AuthUser: "caravana", AuthPassword: uiMailPassword, AllowPlainAuth: true})
	bot := newBotAPI(t)
	secrets := []string{uiMailPassword, uiBotToken, "s3cret-sentinel-c6v8"}

	_, page := e.get(t, "/settings/notifications")
	for _, want := range []string{">Email</option>", ">Telegram</option>", "cv-nf-email", "cv-nf-telegram", "cv-nf-url", "cv-nf-ntfy",
		`name="smtpHost"`, `name="smtpPort"`, `name="smtpSecurity"`, `name="smtpUsername"`, `name="password"`, `name="fromAddress"`,
		`name="toAddresses"`, `name="chatId"`, `name="botToken"`, "STARTTLS (port 587)", "TLS (port 465)", "None, for a mail server on this host"} {
		if !strings.Contains(page, want) {
			t.Errorf("the add form lacks %s", want)
		}
	}
	// The secrets' inputs are password inputs and never carry a value.
	for _, name := range []string{"password", "botToken"} {
		if !regexp.MustCompile(`<input[^>]*type="password"[^>]*name="` + name + `"[^>]*>`).MatchString(page) {
			t.Errorf("the %s input isn't a password input", name)
		}
	}
	add, _ := notifyForms(page)

	mailValues := func(set url.Values) url.Values {
		v := url.Values{"kind": {"email"}, "name": {"mail"}, "events": {"problems", "deploy_failed"},
			"smtpHost": {mail.Host()}, "smtpPort": {strconv.Itoa(mail.Port())}, "smtpSecurity": {"none"}, "smtpUsername": {"caravana"},
			"password": {uiMailPassword}, "fromAddress": {"ops@example.com"}, "toAddresses": {"ana@example.com, ben@example.com\ncy@example.com"},
			"url": {"https://stale.example.com/typed-before-switching"}}
		for k, val := range set {
			v[k] = val
		}
		return v
	}

	t.Run("a refused email form keeps what was typed, minus the password", func(t *testing.T) {
		for _, tc := range []struct {
			name, errID, errText string
			set                  url.Values
		}{
			{"a host with a port", "notify-add-smtpHost-error", "The mail server must be a host name or an IP address", url.Values{"smtpHost": {"mail.example.com:25"}}},
			{"a bad recipient", "notify-add-toAddresses-error", "The to addresses must be an email address", url.Values{"toAddresses": {"ana@example.com nobody"}}},
			{"a port that isn't a number", "notify-add-smtpPort-error", "The port must be a number", url.Values{"smtpPort": {"abc"}}},
			{"none for another host", "notify-add-smtpSecurity-error", "The security is none, which is only allowed", url.Values{"smtpHost": {"mail.example.com"}}},
			{"no sender", "notify-add-fromAddress-error", "The from address is required", url.Values{"fromAddress": {""}}},
		} {
			before := len(e.channelIDs(t))
			resp, body := e.submit(t, add, mailValues(tc.set))
			if resp.StatusCode != http.StatusOK || !strings.Contains(body, `id="`+tc.errID+`"`) || !strings.Contains(text(body), tc.errText) {
				t.Errorf("%s: POST = %d, want the page with %q at %s:\n%s", tc.name, resp.StatusCode, tc.errText, tc.errID, text(body))
			}
			if !strings.Contains(body, `value="ops@example.com"`) && tc.name != "no sender" {
				t.Errorf("%s: the form lost the from address", tc.name)
			}
			noSecret(t, tc.name, secrets, body)
			if len(e.channelIDs(t)) != before {
				t.Errorf("%s: a refused form stored a channel", tc.name)
			}
		}
	})

	var mailID, botID string

	t.Run("add an email channel; a URL typed before switching kind is ignored", func(t *testing.T) {
		resp, body := e.submit(t, add, mailValues(nil))
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/settings/notifications?notifications=saved" {
			t.Fatalf("POST = %d to %q:\n%s", resp.StatusCode, location(t, resp), text(body))
		}
		mailID = e.channelIDs(t)[0]
		if items := e.notifySecretItems(mailID); len(items) != 1 || items["password"] != uiMailPassword {
			t.Errorf("secret items = %v, want only the password", items)
		}
		_, page := e.get(t, "/settings/notifications?notifications=saved")
		got := text(page)
		for _, want := range []string{"Saved.", "mail", "Email", "ana@example.com, ben@example.com +1 more via 127.0.0.1:" + strconv.Itoa(mail.Port())} {
			if !strings.Contains(got, want) {
				t.Errorf("after the add the page lacks %q:\n%s", want, got)
			}
		}
		noSecret(t, "after add email", secrets, body, page, e.Logs.String())
	})

	t.Run("add a Telegram channel", func(t *testing.T) {
		resp, body := e.submit(t, add, url.Values{"kind": {"telegram"}, "name": {"chat"}, "events": {"problems"},
			"chatId": {"-1001234567890"}, "botToken": {uiBotToken}, "apiUrl": {bot.URL}, "url": {"https://stale.example.com"}, "token": {"stale-ntfy-token"}})
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("POST = %d:\n%s", resp.StatusCode, text(body))
		}
		botID = e.channelIDs(t)[1]
		if items := e.notifySecretItems(botID); len(items) != 1 || items["token"] != uiBotToken {
			t.Errorf("secret items = %v, want only the bot token", items)
		}
		_, page := e.get(t, "/settings/notifications")
		if got := text(page); !strings.Contains(got, "Telegram") || !strings.Contains(got, "chat -1001234567890 on "+strings.TrimPrefix(bot.URL, "http://")) {
			t.Errorf("the Telegram row is missing:\n%s", got)
		}
		noSecret(t, "after add telegram", secrets, body, page, e.Logs.String())
	})

	t.Run("a refused Telegram form never gives the token back", func(t *testing.T) {
		for _, tc := range []struct {
			name, errID, errText string
			set                  url.Values
		}{
			{"a token that isn't one", "notify-add-botToken-error", "The bot token must be the bot token BotFather gave", url.Values{"botToken": {"s3cret-sentinel-c6v8-nope"}}},
			{"a bad chat", "notify-add-chatId-error", "The chat ID must be a number", url.Values{"chatId": {"my chat"}}},
			{"a bad Bot API address", "notify-add-apiUrl-error", "The Bot API address must be", url.Values{"apiUrl": {"https://botapi.example.com/x"}}},
		} {
			v := url.Values{"kind": {"telegram"}, "name": {"chat2"}, "events": {"problems"}, "chatId": {"-1001234567890"}, "botToken": {uiBotToken}}
			for k, val := range tc.set {
				v[k] = val
			}
			resp, body := e.submit(t, add, v)
			if resp.StatusCode != http.StatusOK || !strings.Contains(body, `id="`+tc.errID+`"`) || !strings.Contains(text(body), tc.errText) {
				t.Errorf("%s: POST = %d, want the page with %q at %s:\n%s", tc.name, resp.StatusCode, tc.errText, tc.errID, text(body))
			}
			noSecret(t, tc.name, secrets, body)
		}
		if len(e.channelIDs(t)) != 2 {
			t.Error("a refused form stored a channel")
		}
	})

	t.Run("send a test to each", func(t *testing.T) {
		_, page := e.get(t, "/settings/notifications")
		resp, body := e.submit(t, formWith(t, page, "/settings/notifications/"+mailID+"/test"), nil)
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("POST test = %d:\n%s", resp.StatusCode, text(body))
		}
		if _, ok := mail.WaitFor(func(m smtptest.Message) bool { return strings.Contains(string(m.Data), "Test message from Caravana") }, 2*time.Second); !ok {
			t.Error("the mail server got no test message")
		}
		resp, body = e.submit(t, formWith(t, page, "/settings/notifications/"+botID+"/test"), nil)
		if resp.StatusCode != http.StatusSeeOther || bot.count() != 1 || bot.paths[0] != "/bot"+uiBotToken+"/sendMessage" {
			t.Fatalf("test of Telegram = %d, bot API got %v", resp.StatusCode, bot.paths)
		}
		_, page = e.get(t, "/settings/notifications?notified="+url.QueryEscape(botID))
		if got := text(page); !strings.Contains(got, "chat accepted the test message.") || !strings.Contains(got, "Test message from Caravana") {
			t.Errorf("after the test the page lacks the notice or the delivery:\n%s", got)
		}
		noSecret(t, "after tests", secrets, body, page, e.Logs.String())
	})

	t.Run("edit email: the settings show, an empty password keeps it, the box removes it", func(t *testing.T) {
		_, page := e.get(t, "/settings/notifications")
		f := notifyEditForm(t, page, mailID)
		for k, want := range map[string]string{"smtpHost": mail.Host(), "smtpPort": strconv.Itoa(mail.Port()), "smtpUsername": "caravana",
			"fromAddress": "ops@example.com", "toAddresses": "ana@example.com, ben@example.com, cy@example.com"} {
			if got := f.Values.Get(k); got != want {
				t.Errorf("edit form %s = %q, want %q", k, got, want)
			}
		}
		if v, has := f.Values["password"]; !has || v[0] != "" {
			t.Errorf("the password field is %v, want present and empty", v)
		}
		if !strings.Contains(text(page), "A password is set: leave it empty to keep it.") || !strings.Contains(text(page), "Remove the password") {
			t.Error("the email edit form lacks the password hint or the remove box")
		}
		// formsIn reads every checkbox as checked; a browser sends only a ticked one.
		set := url.Values{"smtpSecurity": {"none"}, "fromAddress": {"alerts@example.com"}, "removePassword": nil, "events": {"problems"}}
		resp, body := e.submit(t, f, set)
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("POST edit = %d:\n%s", resp.StatusCode, text(body))
		}
		if e.notifySecretItems(mailID)["password"] != uiMailPassword {
			t.Error("an edit with an empty password didn't keep it")
		}
		chans, _ := e.Svc.NotificationChannels(t.Context())
		if chans[0].Email.From != "alerts@example.com" || len(chans[0].Email.To) != 3 || !chans[0].HasToken {
			t.Errorf("the edit stored %+v", chans[0].Email)
		}

		set["removePassword"] = []string{"1"}
		_, page = e.get(t, "/settings/notifications")
		resp, _ = e.submit(t, notifyEditForm(t, page, mailID), set)
		if resp.StatusCode != http.StatusSeeOther || len(e.notifySecretItems(mailID)) != 0 {
			t.Errorf("remove the password = %d, items %v", resp.StatusCode, e.notifySecretItems(mailID))
		}
		_, page = e.get(t, "/settings/notifications")
		if strings.Contains(text(page), "A password is set") || strings.Contains(text(page), "Remove the password") {
			t.Error("a channel without a password still says one is set")
		}

		// A new password replaces it.
		set["removePassword"] = nil
		set["password"] = []string{"s3cret-sentinel-c6v8-second"}
		resp, _ = e.submit(t, notifyEditForm(t, page, mailID), set)
		if resp.StatusCode != http.StatusSeeOther || e.notifySecretItems(mailID)["password"] != "s3cret-sentinel-c6v8-second" {
			t.Errorf("set a new password = %d, items %v", resp.StatusCode, e.notifySecretItems(mailID))
		}

		// A refused edit opens that row with the error under its input.
		_, page = e.get(t, "/settings/notifications")
		set["toAddresses"] = []string{"nobody"}
		resp, body = e.submit(t, notifyEditForm(t, page, mailID), set)
		if resp.StatusCode != http.StatusOK || !regexp.MustCompile(`id="notify-edit-\d+-toAddresses-error"`).MatchString(body) ||
			!regexp.MustCompile(`name="conn-\d+" open>`).MatchString(body) {
			t.Errorf("a refused edit = %d, want the row open with the error under To:\n%s", resp.StatusCode, text(body))
		}
		noSecret(t, "edit email", secrets, body, page, e.Logs.String())
	})

	t.Run("edit Telegram: an empty token keeps it, a refused edit gives nothing back", func(t *testing.T) {
		_, page := e.get(t, "/settings/notifications")
		f := notifyEditForm(t, page, botID)
		if f.Values.Get("chatId") != "-1001234567890" || f.Values.Get("apiUrl") != bot.URL {
			t.Errorf("edit form chatId %q apiUrl %q", f.Values.Get("chatId"), f.Values.Get("apiUrl"))
		}
		if v, has := f.Values["botToken"]; !has || v[0] != "" {
			t.Errorf("the bot token field is %v, want present and empty", v)
		}
		if !strings.Contains(text(page), "A token is set: leave it empty to keep it.") {
			t.Error("the Telegram edit form lacks the token hint")
		}
		resp, body := e.submit(t, f, url.Values{"chatId": {"@caravana_ops"}, "events": {"problems"}})
		if resp.StatusCode != http.StatusSeeOther || e.notifySecretItems(botID)["token"] != uiBotToken {
			t.Fatalf("edit = %d, token kept %v:\n%s", resp.StatusCode, e.notifySecretItems(botID)["token"] == uiBotToken, text(body))
		}
		_, page = e.get(t, "/settings/notifications")
		resp, body = e.submit(t, notifyEditForm(t, page, botID), url.Values{"chatId": {"my chat"}, "botToken": {"s3cret-sentinel-c6v8-nope"}, "events": {"problems"}})
		if resp.StatusCode != http.StatusOK || !regexp.MustCompile(`id="notify-edit-\d+-chatId-error"`).MatchString(body) {
			t.Errorf("a refused edit = %d, want the error under the chat ID:\n%s", resp.StatusCode, text(body))
		}
		noSecret(t, "edit telegram", secrets, body, page, e.Logs.String())
	})

	t.Run("no secret is on the page, in the list of deliveries or in the log", func(t *testing.T) {
		_, page := e.get(t, "/settings/notifications")
		noSecret(t, "the final page", secrets, page, e.Logs.String())
	})

	t.Run("remove both", func(t *testing.T) {
		for _, id := range []string{mailID, botID} {
			_, page := e.get(t, "/settings/notifications")
			resp, _ := e.submit(t, formWith(t, page, "/settings/notifications/"+id+"/remove"), nil)
			if resp.StatusCode != http.StatusSeeOther || len(e.notifySecretItems(id)) != 0 {
				t.Errorf("remove %s = %d, items %v", id, resp.StatusCode, e.notifySecretItems(id))
			}
		}
	})
}
