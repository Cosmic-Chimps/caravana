package service

import (
	"encoding/json"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/notify"
)

// The email and Telegram kinds of notification channel. What they keep besides
// the name and events is small: the mail server and addresses, or the chat. It
// is not secret, so it lives as JSON in the channel's row; the SMTP password
// and the bot token live in the Nomad Variable like every other channel secret.

// secretPassword is the item of an email channel's variable that holds its SMTP
// password.
const secretPassword = "password"

// Limits of an email channel.
const (
	maxRecipients  = 10
	maxHostLength  = 253
	maxUserLength  = 256
	maxPasswordLen = 1024
)

// EmailSettings is the mail server and the addresses of an email channel.
type EmailSettings struct {
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	Security string   `json:"security"` // starttls, tls or none
	Username string   `json:"username,omitempty"`
	From     string   `json:"from"`
	To       []string `json:"to"`
}

// TelegramSettings is where a Telegram channel posts. The bot token is a secret
// and lives in the Nomad Variable.
type TelegramSettings struct {
	ChatID string `json:"chatId"`
	// APIURL is the Bot API server; "" means notify.DefaultTelegramAPI.
	APIURL string `json:"apiUrl,omitempty"`
}

// channelSettings is the JSON in a channel's row: the settings of its kind.
type channelSettings struct {
	Email    *EmailSettings    `json:"email,omitempty"`
	Telegram *TelegramSettings `json:"telegram,omitempty"`
}

// decodeSettings reads a row's settings. An unreadable one is the zero value,
// as for a channel of a kind with none.
func decodeSettings(raw []byte) channelSettings {
	var s channelSettings
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}

func encodeSettings(s channelSettings) []byte {
	raw, err := json.Marshal(s)
	if err != nil { // plain structs of strings and numbers: can't happen
		return []byte("{}")
	}
	return raw
}

// The patterns of what Telegram and DNS accept.
var (
	botTokenPattern = regexp.MustCompile(`^[0-9]{1,20}:[A-Za-z0-9_-]{20,100}$`)
	chatIDPattern   = regexp.MustCompile(`^(-?[0-9]{1,20}|@[A-Za-z][A-Za-z0-9_]{4,31})$`)
	hostPattern     = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$`)
)

// isLoopbackHost reports whether host is this machine: "localhost" or a loopback
// address.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// checkEmail checks an email channel's settings and returns them normalized:
// the security defaulted, the port made explicit, the addresses trimmed. Errors
// are *app.FieldError naming "email.host", "email.port", "email.security",
// "email.username", "email.from" or "email.to[i]".
func checkEmail(e EmailSettings) (EmailSettings, error) {
	e.Host = strings.TrimSpace(e.Host)
	switch {
	case e.Host == "":
		return e, app.NewFieldError("email.host", "is required")
	case len(e.Host) > maxHostLength:
		return e, app.NewFieldError("email.host", "is too long: at most %d characters", maxHostLength)
	case net.ParseIP(e.Host) == nil && !hostPattern.MatchString(e.Host):
		return e, app.NewFieldError("email.host", "must be a host name or an IP address, without a scheme, a port or a path")
	}

	if e.Security == "" {
		e.Security = notify.SecuritySTARTTLS
	}
	switch e.Security {
	case notify.SecuritySTARTTLS, notify.SecurityTLS:
	case notify.SecurityNone:
		if !isLoopbackHost(e.Host) {
			return e, app.NewFieldError("email.security", "is none, which is only allowed for a mail server on this host (localhost); use starttls or tls")
		}
	default:
		return e, app.NewFieldError("email.security", "must be one of: starttls, tls, none")
	}

	if e.Port == 0 {
		e.Port = map[string]int{notify.SecuritySTARTTLS: 587, notify.SecurityTLS: 465, notify.SecurityNone: 25}[e.Security]
	}
	if e.Port < 1 || e.Port > 65535 {
		return e, app.NewFieldError("email.port", "must be between 1 and 65535")
	}

	e.Username = strings.TrimSpace(e.Username)
	if len(e.Username) > maxUserLength {
		return e, app.NewFieldError("email.username", "is too long: at most %d characters", maxUserLength)
	}
	if strings.IndexFunc(e.Username, func(r rune) bool { return unicode.IsControl(r) }) >= 0 {
		return e, app.NewFieldError("email.username", "must not hold control characters or line breaks")
	}

	e.From = strings.TrimSpace(e.From)
	if e.From == "" {
		return e, app.NewFieldError("email.from", "is required")
	}
	if err := checkMailAddress(e.From); err != nil {
		return e, app.NewFieldError("email.from", "%s", err)
	}

	if len(e.To) == 0 {
		return e, app.NewFieldError("email.to", "needs at least one address")
	}
	if len(e.To) > maxRecipients {
		return e, app.NewFieldError("email.to", "has too many addresses: at most %d", maxRecipients)
	}
	seen := map[string]bool{}
	to := make([]string, len(e.To))
	for i, raw := range e.To {
		to[i] = strings.TrimSpace(raw)
		field := fmt.Sprintf("email.to[%d]", i)
		if err := checkMailAddress(to[i]); err != nil {
			return e, app.NewFieldError(field, "%s", err)
		}
		a, _ := mail.ParseAddress(to[i])
		key := strings.ToLower(a.Address)
		if seen[key] {
			return e, app.NewFieldError(field, "is repeated")
		}
		seen[key] = true
	}
	e.To = to
	return e, nil
}

// checkMailAddress checks one address, with or without a display name. The
// error never holds the address.
func checkMailAddress(raw string) error {
	if strings.ContainsAny(raw, "\r\n") {
		return fmt.Errorf("must not hold a line break")
	}
	if _, err := mail.ParseAddress(raw); err != nil {
		return fmt.Errorf("must be an email address, like ops@example.com")
	}
	return nil
}

// checkSMTPPassword checks an SMTP password. The error never holds it.
func checkSMTPPassword(password string) error {
	if len(password) > maxPasswordLen {
		return app.NewFieldError("password", "is too long: at most %d characters", maxPasswordLen)
	}
	if strings.ContainsAny(password, "\r\n") {
		return app.NewFieldError("password", "must not hold a line break")
	}
	return nil
}

// checkBotToken checks a Telegram bot token. The error never holds it.
func checkBotToken(token string) error {
	if !botTokenPattern.MatchString(token) {
		return app.NewFieldError("token", "must be the bot token BotFather gave, like 123456789:AA…")
	}
	return nil
}

// checkTelegram checks a Telegram channel's settings and returns them
// normalized: the Bot API address without a trailing slash, and empty when it is
// the default one.
func checkTelegram(t TelegramSettings) (TelegramSettings, error) {
	t.ChatID = strings.TrimSpace(t.ChatID)
	switch {
	case t.ChatID == "":
		return t, app.NewFieldError("telegram.chatId", "is required")
	case !chatIDPattern.MatchString(t.ChatID):
		return t, app.NewFieldError("telegram.chatId", "must be a number like -1001234567890, or @channelname")
	}
	t.APIURL = strings.TrimSpace(t.APIURL)
	if t.APIURL == "" {
		return t, nil
	}
	u, err := notify.ParseAddress(t.APIURL)
	switch {
	case err != nil:
		return t, app.NewFieldError("telegram.apiUrl", "must be an absolute http or https URL")
	case u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/"):
		return t, app.NewFieldError("telegram.apiUrl", "must be the server's address only, like https://api.telegram.org")
	case u.Scheme != "https" && !isLoopbackHost(u.Hostname()):
		return t, app.NewFieldError("telegram.apiUrl", "must be https, unless the Bot API server runs on this host")
	}
	t.APIURL = u.Scheme + "://" + u.Host
	if t.APIURL == notify.DefaultTelegramAPI {
		t.APIURL = ""
	}
	return t, nil
}

// emailAddress is what the console and API list for an email channel: the
// recipients (two at most, then "+N more") and the server.
// "ops@example.com, dev@example.com via smtp.example.com:587".
func emailAddress(e EmailSettings) string {
	var shown []string
	for _, raw := range e.To {
		if a, err := mail.ParseAddress(raw); err == nil {
			shown = append(shown, a.Address)
		}
	}
	list := strings.Join(shown[:min(len(shown), 2)], ", ")
	if len(shown) > 2 {
		list += fmt.Sprintf(" +%d more", len(shown)-2)
	}
	return list + " via " + net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
}

// telegramAddress is what is listed for a Telegram channel: "chat -1001234567890",
// and " on botapi.example.com" when the Bot API server isn't Telegram's.
func telegramAddress(t TelegramSettings) string {
	shown := "chat " + t.ChatID
	if t.APIURL != "" {
		if u, err := url.Parse(t.APIURL); err == nil {
			shown += " on " + u.Host
		}
	}
	return shown
}
