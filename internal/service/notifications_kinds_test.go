package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/notify"
	"github.com/cosmic-chimps/caravana/internal/notify/smtptest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Values no answer, page, log, row or file may hold (task c6v8).
const (
	mailPassword = "s3cret-sentinel-c6v8-pass"
	botToken     = "123456789:s3cret-sentinel-c6v8-tokenAAAAAAAAAA"
)

// mailServer starts a fake mail server that wants this login, in plain (it is on
// this host).
func mailServer(t *testing.T, opt smtptest.Options) *smtptest.Server {
	t.Helper()
	opt.AuthUser, opt.AuthPassword, opt.AllowPlainAuth = "caravana", mailPassword, true
	return smtptest.Start(t, opt)
}

func emailInput(srv *smtptest.Server) NotificationChannelInput {
	return NotificationChannelInput{
		Kind: notify.KindEmail, Name: "mail", Events: []string{EventProblems, EventDeployFailed}, Password: mailPassword,
		Email: &EmailSettings{Host: srv.Host(), Port: srv.Port(), Security: notify.SecurityNone, Username: "caravana",
			From: "Caravana <ops@example.com>", To: []string{"ana@example.com", "ben@example.com", "cy@example.com"}},
	}
}

// botAPI is a fake Telegram Bot API that records each request.
type botAPI struct {
	*httptest.Server
	paths  []string
	bodies []string
}

func newBotAPI(t *testing.T) *botAPI {
	t.Helper()
	b := &botAPI{}
	b.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		b.paths, b.bodies = append(b.paths, r.URL.Path), append(b.bodies, string(raw))
		io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	t.Cleanup(b.Close)
	return b
}

func telegramInput(api *botAPI) NotificationChannelInput {
	return NotificationChannelInput{
		Kind: notify.KindTelegram, Name: "chat", Events: []string{EventProblems}, Token: botToken,
		Telegram: &TelegramSettings{ChatID: "-1001234567890", APIURL: api.URL},
	}
}

func TestCreateEmailChannelValidation(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	srv := mailServer(t, smtptest.Options{})

	tests := []struct {
		name  string
		mod   func(in *NotificationChannelInput)
		field string // "" means valid
	}{
		{"valid", func(in *NotificationChannelInput) {}, ""},
		{"no email settings", func(in *NotificationChannelInput) { in.Email = nil }, "email"},
		{"no host", func(in *NotificationChannelInput) { in.Email.Host = " " }, "email.host"},
		{"host with a scheme", func(in *NotificationChannelInput) { in.Email.Host = "smtp://mail.example.com" }, "email.host"},
		{"host with a port", func(in *NotificationChannelInput) { in.Email.Host = "mail.example.com:587" }, "email.host"},
		{"host with spaces", func(in *NotificationChannelInput) { in.Email.Host = "mail example.com" }, "email.host"},
		{"host too long", func(in *NotificationChannelInput) { in.Email.Host = strings.Repeat("a", 254) }, "email.host"},
		{"an IPv6 literal", func(in *NotificationChannelInput) { in.Email.Host, in.Email.Security = "::1", notify.SecurityNone }, ""},
		{"port too big", func(in *NotificationChannelInput) { in.Email.Port = 70000 }, "email.port"},
		{"negative port", func(in *NotificationChannelInput) { in.Email.Port = -1 }, "email.port"},
		{"unknown security", func(in *NotificationChannelInput) { in.Email.Security = "ssl" }, "email.security"},
		{"none for a host that isn't this one", func(in *NotificationChannelInput) { in.Email.Host = "mail.example.com" }, "email.security"},
		{"username with a line break", func(in *NotificationChannelInput) { in.Email.Username = "a\nb" }, "email.username"},
		{"username too long", func(in *NotificationChannelInput) { in.Email.Username = strings.Repeat("u", 257) }, "email.username"},
		{"no from", func(in *NotificationChannelInput) { in.Email.From = "" }, "email.from"},
		{"from that isn't an address", func(in *NotificationChannelInput) { in.Email.From = "ops" }, "email.from"},
		{"from with a line break", func(in *NotificationChannelInput) { in.Email.From = "ops@example.com\nBcc: x@example.com" }, "email.from"},
		{"no recipient", func(in *NotificationChannelInput) { in.Email.To = nil }, "email.to"},
		{"eleven recipients", func(in *NotificationChannelInput) {
			in.Email.To = nil
			for i := range 11 {
				in.Email.To = append(in.Email.To, fmt.Sprintf("u%d@example.com", i))
			}
		}, "email.to"},
		{"a bad second recipient", func(in *NotificationChannelInput) { in.Email.To = []string{"ana@example.com", "nobody"} }, "email.to[1]"},
		{"a repeated recipient", func(in *NotificationChannelInput) {
			in.Email.To = []string{"ana@example.com", "Ana <ANA@example.com>"}
		}, "email.to[1]"},
		{"password with a line break", func(in *NotificationChannelInput) { in.Password = "a\nb" }, "password"},
		{"password too long", func(in *NotificationChannelInput) { in.Password = strings.Repeat("p", 1025) }, "password"},
		{"no password is fine", func(in *NotificationChannelInput) { in.Password = "" }, ""},
		{"a url", func(in *NotificationChannelInput) { in.URL = "https://mail.example.com" }, "url"},
		{"a token", func(in *NotificationChannelInput) { in.Token = "tk" }, "token"},
		{"telegram settings", func(in *NotificationChannelInput) { in.Telegram = &TelegramSettings{ChatID: "1"} }, "telegram"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := emailInput(srv)
			tc.mod(&in)
			got, err := e.svc.CreateNotificationChannel(ctx, in)
			if tc.field != "" {
				if fieldOf(err) != tc.field {
					t.Fatalf("CreateNotificationChannel() = %v, want a field error on %q", err, tc.field)
				}
				if strings.Contains(err.Error(), mailPassword) {
					t.Errorf("error %q holds the password", err)
				}
				return
			}
			if err != nil || got.Email == nil {
				t.Fatalf("CreateNotificationChannel() = %+v, %v", got, err)
			}
		})
	}
}

func TestCreateTelegramChannelValidation(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	api := newBotAPI(t)

	tests := []struct {
		name  string
		mod   func(in *NotificationChannelInput)
		field string
	}{
		{"valid", func(in *NotificationChannelInput) {}, ""},
		{"a channel name as the chat", func(in *NotificationChannelInput) { in.Telegram.ChatID = "@caravana_ops" }, ""},
		{"the default Bot API", func(in *NotificationChannelInput) { in.Telegram.APIURL = "https://api.telegram.org/" }, ""},
		{"no telegram settings", func(in *NotificationChannelInput) { in.Telegram = nil }, "telegram"},
		{"no chat", func(in *NotificationChannelInput) { in.Telegram.ChatID = "" }, "telegram.chatId"},
		{"a chat that is neither", func(in *NotificationChannelInput) { in.Telegram.ChatID = "my chat" }, "telegram.chatId"},
		{"a channel name too short", func(in *NotificationChannelInput) { in.Telegram.ChatID = "@abc" }, "telegram.chatId"},
		{"an API URL with a path", func(in *NotificationChannelInput) { in.Telegram.APIURL = "https://botapi.example.com/x" }, "telegram.apiUrl"},
		{"an API URL that isn't a URL", func(in *NotificationChannelInput) { in.Telegram.APIURL = "botapi.example.com" }, "telegram.apiUrl"},
		{"an API URL with a password", func(in *NotificationChannelInput) { in.Telegram.APIURL = "https://u:p@botapi.example.com" }, "telegram.apiUrl"},
		{"plain http for another host", func(in *NotificationChannelInput) { in.Telegram.APIURL = "http://botapi.example.com" }, "telegram.apiUrl"},
		{"no token", func(in *NotificationChannelInput) { in.Token = "" }, "token"},
		{"a token that isn't one", func(in *NotificationChannelInput) { in.Token = "s3cret-sentinel-not-a-bot-token" }, "token"},
		{"a url", func(in *NotificationChannelInput) { in.URL = "https://example.com" }, "url"},
		{"a password", func(in *NotificationChannelInput) { in.Password = "x" }, "password"},
		{"email settings", func(in *NotificationChannelInput) { in.Email = &EmailSettings{} }, "email"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := telegramInput(api)
			tc.mod(&in)
			_, err := e.svc.CreateNotificationChannel(ctx, in)
			if tc.field != "" {
				if fieldOf(err) != tc.field {
					t.Fatalf("CreateNotificationChannel() = %v, want a field error on %q", err, tc.field)
				}
				if strings.Contains(err.Error(), "s3cret-sentinel") {
					t.Errorf("error %q holds the token", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("CreateNotificationChannel() = %v", err)
			}
		})
	}
}

func TestEmailChannelLifecycle(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	srv := mailServer(t, smtptest.Options{})
	e.svc.SetPublicURL("https://caravana.example.com")

	ch, err := e.svc.CreateNotificationChannel(ctx, emailInput(srv))
	if err != nil {
		t.Fatal(err)
	}
	wantAddress := fmt.Sprintf("ana@example.com, ben@example.com +1 more via 127.0.0.1:%d", srv.Port())
	if ch.Address != wantAddress || !ch.HasToken || ch.Email == nil || ch.Email.Host != srv.Host() || len(ch.Email.To) != 3 || ch.SigningSecret != "" {
		t.Fatalf("created = %+v (address %q), want %q, a password set and the settings", ch, ch.Address, wantAddress)
	}
	if items := e.secretItems(t, ch.ID); len(items) != 1 || items[secretPassword] != mailPassword {
		t.Errorf("secret items = %v, want only the password", items)
	}

	// The list reads the settings back from the row.
	list, err := e.svc.NotificationChannels(ctx)
	if err != nil || len(list) != 1 || list[0].Email == nil || list[0].Email.Port != srv.Port() || !list[0].HasToken {
		t.Fatalf("list = %+v, %v", list, err)
	}

	// Send a test: the message reaches the server over the login, with the link.
	d, err := e.svc.TestNotificationChannel(ctx, ch.ID)
	if err != nil || d.Status != store.DeliverySent {
		t.Fatalf("TestNotificationChannel() = %+v, %v", d, err)
	}
	msg, ok := srv.WaitFor(func(m smtptest.Message) bool { return strings.Contains(string(m.Data), "Test message from Caravana") }, time.Second)
	if !ok || msg.AuthUser != "caravana" || msg.From != "ops@example.com" || len(msg.To) != 3 ||
		!strings.Contains(string(msg.Data), "https://caravana.example.com/settings/notifications") {
		t.Fatalf("message = %+v (ok %v)", msg, ok)
	}

	// Edit the settings (replaced whole) and keep the password.
	next := *ch.Email
	next.To = []string{"dev@example.com"}
	next.Security = notify.SecurityNone
	got, err := e.svc.UpdateNotificationChannel(ctx, ch.ID, NotificationChannelPatch{Email: &next})
	if err != nil || len(got.Email.To) != 1 || !got.HasToken || got.Address != fmt.Sprintf("dev@example.com via 127.0.0.1:%d", srv.Port()) {
		t.Fatalf("update = %+v, %v; want the new recipient, the address redrawn and the password kept", got, err)
	}
	if items := e.secretItems(t, ch.ID); items[secretPassword] != mailPassword {
		t.Error("an edit that didn't name the password changed it")
	}

	// Change the password, then remove it: the variable goes with the last item.
	newPass := "another-" + mailPassword
	if got, err = e.svc.UpdateNotificationChannel(ctx, ch.ID, NotificationChannelPatch{Password: &newPass}); err != nil || !got.HasToken {
		t.Fatalf("set the password: %+v, %v", got, err)
	}
	if items := e.secretItems(t, ch.ID); items[secretPassword] != newPass {
		t.Errorf("secret items = %v, want the new password", items)
	}
	none := ""
	if got, err = e.svc.UpdateNotificationChannel(ctx, ch.ID, NotificationChannelPatch{Password: &none}); err != nil || got.HasToken {
		t.Fatalf("remove the password: %+v, %v", got, err)
	}
	if items := e.secretItems(t, ch.ID); len(items) != 0 {
		t.Errorf("secret items = %v, want none", items)
	}
	// A channel without a password still sends (the fake wants one, so this
	// delivery fails at login (535), with the server's words and no secret).
	d, err = e.svc.TestNotificationChannel(ctx, ch.ID)
	if err != nil || d.Status != store.DeliveryFailed || !strings.Contains(d.Error, "535") || strings.Contains(d.Error, "s3cret-sentinel") {
		t.Errorf("a test without the password = %+v, %v", d, err)
	}

	// Fields of other kinds are refused by name.
	tk := "x"
	for field, patch := range map[string]NotificationChannelPatch{
		"token":    {Token: &tk},
		"url":      {URL: &tk},
		"telegram": {Telegram: &TelegramSettings{ChatID: "1"}},
	} {
		if _, err := e.svc.UpdateNotificationChannel(ctx, ch.ID, patch); fieldOf(err) != field {
			t.Errorf("patch with %s on an email channel = %v, want a field error on it", field, err)
		}
	}

	if err := e.svc.DeleteNotificationChannel(ctx, ch.ID); err != nil {
		t.Fatal(err)
	}
}

func TestTelegramChannelLifecycle(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	api := newBotAPI(t)

	ch, err := e.svc.CreateNotificationChannel(ctx, telegramInput(api))
	if err != nil {
		t.Fatal(err)
	}
	if want := "chat -1001234567890 on " + strings.TrimPrefix(api.URL, "http://"); ch.Address != want || !ch.HasToken || ch.Telegram == nil || ch.Telegram.ChatID != "-1001234567890" {
		t.Fatalf("created = %+v (address %q), want %q", ch, ch.Address, want)
	}
	if items := e.secretItems(t, ch.ID); len(items) != 1 || items[secretToken] != botToken {
		t.Errorf("secret items = %v, want only the token", items)
	}
	list, _ := e.svc.NotificationChannels(ctx)
	if len(list) != 1 || !list[0].HasToken || list[0].Telegram == nil {
		t.Fatalf("list = %+v", list)
	}

	d, err := e.svc.TestNotificationChannel(ctx, ch.ID)
	if err != nil || d.Status != store.DeliverySent {
		t.Fatalf("TestNotificationChannel() = %+v, %v", d, err)
	}
	if len(api.paths) != 1 || api.paths[0] != "/bot"+botToken+"/sendMessage" ||
		!strings.Contains(api.bodies[0], `"chat_id":"-1001234567890"`) || !strings.Contains(api.bodies[0], "Test message from Caravana") {
		t.Errorf("the Bot API got %v with %v", api.paths, api.bodies)
	}

	// The chat changes, the token stays; an empty token is refused, a new one replaces it.
	got, err := e.svc.UpdateNotificationChannel(ctx, ch.ID, NotificationChannelPatch{Telegram: &TelegramSettings{ChatID: "@caravana_ops"}})
	if err != nil || got.Address != "chat @caravana_ops" || got.Telegram.APIURL != "" {
		t.Fatalf("update the chat = %+v, %v", got, err)
	}
	if items := e.secretItems(t, ch.ID); items[secretToken] != botToken {
		t.Error("an edit that didn't name the token changed it")
	}
	empty := ""
	if _, err := e.svc.UpdateNotificationChannel(ctx, ch.ID, NotificationChannelPatch{Token: &empty}); fieldOf(err) != "token" {
		t.Errorf("an empty token = %v, want a field error on token", err)
	}
	bad := "not-a-token"
	if _, err := e.svc.UpdateNotificationChannel(ctx, ch.ID, NotificationChannelPatch{Token: &bad}); fieldOf(err) != "token" {
		t.Errorf("a bad token = %v, want a field error on token", err)
	}
	fresh := "987654321:s3cret-sentinel-c6v8-freshAAAAAAAAAA"
	if got, err = e.svc.UpdateNotificationChannel(ctx, ch.ID, NotificationChannelPatch{Token: &fresh}); err != nil || !got.HasToken {
		t.Fatalf("a new token: %+v, %v", got, err)
	}
	if items := e.secretItems(t, ch.ID); items[secretToken] != fresh {
		t.Errorf("secret items = %v, want the new token", items)
	}
	for field, patch := range map[string]NotificationChannelPatch{
		"password": {Password: &empty},
		"url":      {URL: &empty},
		"email":    {Email: &EmailSettings{}},
	} {
		if _, err := e.svc.UpdateNotificationChannel(ctx, ch.ID, patch); fieldOf(err) != field {
			t.Errorf("patch with %s on a Telegram channel = %v, want a field error on it", field, err)
		}
	}
}

// An email channel's security and port default together, and are stored as the
// numbers they mean.
func TestCheckEmailDefaults(t *testing.T) {
	for _, tc := range []struct {
		security string
		port     int
		want     int
	}{{"", 0, 587}, {notify.SecuritySTARTTLS, 0, 587}, {notify.SecurityTLS, 0, 465}, {notify.SecurityTLS, 2465, 2465}} {
		got, err := checkEmail(EmailSettings{Host: "smtp.example.com", Security: tc.security, Port: tc.port, From: "a@example.com", To: []string{"b@example.com"}})
		wantSecurity := tc.security
		if wantSecurity == "" {
			wantSecurity = notify.SecuritySTARTTLS
		}
		if err != nil || got.Port != tc.want || got.Security != wantSecurity {
			t.Errorf("checkEmail(%q, %d) = %+v, %v; want port %d, security %q", tc.security, tc.port, got, err, tc.want, wantSecurity)
		}
	}
	if got, err := checkEmail(EmailSettings{Host: "localhost", Security: notify.SecurityNone, From: "a@example.com", To: []string{"b@example.com"}}); err != nil || got.Port != 25 {
		t.Errorf("checkEmail(none) = %+v, %v; want port 25", got, err)
	}
}

// A Telegram channel's address is the default Bot API when none is named.
func TestTelegramDefaultAPI(t *testing.T) {
	got, err := checkTelegram(TelegramSettings{ChatID: "42", APIURL: "https://api.telegram.org/"})
	if err != nil || got.APIURL != "" {
		t.Errorf("checkTelegram = %+v, %v; want the default stored as empty", got, err)
	}
	if want := "chat 42"; telegramAddress(got) != want {
		t.Errorf("telegramAddress = %q, want %q", telegramAddress(got), want)
	}
}

// What the sender queue does with a failing mail server: the rejection is kept
// as the delivery's error, without the password.
func TestEmailDeliveryErrorsHoldNoPassword(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	srv := mailServer(t, smtptest.Options{RefuseRcpt: "ben@"})
	ch, err := e.svc.CreateNotificationChannel(ctx, emailInput(srv))
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.svc.TestNotificationChannel(ctx, ch.ID)
	if err != nil || d.Status != store.DeliveryFailed || !strings.Contains(d.Error, "550") {
		t.Fatalf("TestNotificationChannel() = %+v, %v; want a failed delivery with the 550", d, err)
	}
	if strings.Contains(d.Error, mailPassword) {
		t.Errorf("the recorded error holds the password: %s", d.Error)
	}
}

// The SMTP password and the bot token are in no value the service hands back, no
// log line and no file of the store, for every operation on both kinds.
func TestMailAndBotSecretsStayInTheVariable(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := newEnv(t)
	var logs bytes.Buffer
	svc := New(st, newDeployer(t, e.registry, e.nomad), e.secrets, slog.New(slog.NewTextHandler(&logs, nil)))

	srv := mailServer(t, smtptest.Options{RefuseRcpt: "cy@"}) // failing, so errors are recorded
	api := newBotAPI(t)
	var returned strings.Builder
	for _, in := range []NotificationChannelInput{emailInput(srv), telegramInput(api)} {
		created, err := svc.CreateNotificationChannel(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		d, err := svc.TestNotificationChannel(ctx, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		listed, err := svc.NotificationDeliveries(ctx, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		bad := "x"
		_, patchErr := svc.UpdateNotificationChannel(ctx, created.ID, NotificationChannelPatch{Token: &bad, Password: &bad})
		fmt.Fprintf(&returned, "%+v %+v %+v %v", created, d, listed, patchErr)
	}
	list, err := svc.NotificationChannels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(&returned, "%+v", list)

	check := func(what, text string) {
		for _, s := range []string{mailPassword, "s3cret-sentinel-c6v8"} {
			if strings.Contains(text, s) {
				t.Errorf("%s holds a secret", what)
			}
		}
	}
	check("a returned value", returned.String())
	check("the log", logs.String())
	files, err := os.ReadDir(dir)
	if err != nil || len(files) == 0 {
		t.Fatalf("data dir: %v, %v", files, err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(filepath.Join(dir, f.Name()))
		if err != nil {
			t.Fatal(err)
		}
		check(f.Name(), string(raw))
	}
}
