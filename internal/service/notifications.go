package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/audit"
	"github.com/cosmic-chimps/caravana/internal/notify"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Notification channels (feature 018). SQLite keeps what the console shows;
// the secret parts (a Slack, Discord or ntfy URL, an ntfy or Telegram token, an
// SMTP password, a webhook's signing secret) live in a Nomad Variable (package
// deploy), written before the row that lists the channel, so a listed channel
// always has what it needs.

// The events a channel can ask for.
const (
	EventProblems        = "problems"         // a needs-you item opened or resolved
	EventDeployFailed    = "deploy_failed"    // a deploy ended failed, reverted or superseded
	EventDeploySucceeded = "deploy_succeeded" // a deploy ended healthy
)

// NotificationEvents lists the events in the order the console shows them.
var NotificationEvents = []string{EventProblems, EventDeployFailed, EventDeploySucceeded}

// The items of a channel's secret variable.
const (
	secretURL           = "url"            // slack, discord and ntfy: the whole URL
	secretToken         = "token"          // ntfy: the access token; telegram: the bot token
	secretSigningSecret = "signing_secret" // webhook
)

// recentDeliveries is how many deliveries a channel keeps and lists (FR-010).
const recentDeliveries = 20

// ErrNotAWebhook is returned when a signing secret is asked for a channel that
// is not a webhook.
var ErrNotAWebhook = errors.New("only a webhook channel has a signing secret")

// NotificationChannel is a channel as the console and the API show it: no
// secret, only whether one is set. HasToken is whether an ntfy token or an SMTP
// password is set; it is always true for Telegram, which can't work without its
// bot token. Email and Telegram carry their settings, decoded from the row.
type NotificationChannel struct {
	store.NotificationChannel
	HasToken bool
	Email    *EmailSettings
	Telegram *TelegramSettings
}

// channelView is the view of a stored channel: the row with its settings read.
func channelView(row store.NotificationChannel, hasToken bool) NotificationChannel {
	settings := decodeSettings(row.Settings)
	v := NotificationChannel{NotificationChannel: row, HasToken: hasToken}
	switch row.Kind {
	case notify.KindEmail:
		v.Email = settings.Email
	case notify.KindTelegram:
		v.Telegram = settings.Telegram
		v.HasToken = true
	}
	return v
}

// NotificationChannelInput is what adding a channel takes. Token is the access
// token of an ntfy channel (optional) or the bot token of a Telegram one
// (required). Email and Password are for an email channel (the password is
// optional), Telegram for a Telegram one; an email or Telegram channel has no URL.
type NotificationChannelInput struct {
	Kind     string
	Name     string
	URL      string
	Token    string
	Password string
	Email    *EmailSettings
	Telegram *TelegramSettings
	Events   []string
	Paused   bool
}

// NotificationChannelPatch is what editing a channel takes; a nil field keeps
// the stored value. Token set to "" removes the ntfy token; a Telegram channel
// can't lose its bot token, so "" is refused there. Password set to "" removes
// the SMTP password. Email and Telegram replace the channel's settings whole. URL
// may not be empty: leave it nil to keep the address (which for Slack, Discord
// and ntfy is the secret and is never shown).
type NotificationChannelPatch struct {
	Name     *string
	URL      *string
	Token    *string
	Password *string
	Email    *EmailSettings
	Telegram *TelegramSettings
	Events   *[]string
	Paused   *bool
}

// CreatedNotificationChannel is the answer to adding a channel: the channel,
// and for a webhook its signing secret, shown this once.
type CreatedNotificationChannel struct {
	NotificationChannel
	SigningSecret string
}

// NotificationChannels returns every channel, oldest first, without secrets.
// When the secret store can't be read, HasToken is false rather than the whole
// list failing, so the page that lists channels still opens.
func (s *Service) NotificationChannels(ctx context.Context) ([]NotificationChannel, error) {
	rows, err := s.store.ListNotificationChannels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]NotificationChannel, 0, len(rows))
	for _, c := range rows {
		hasToken := false
		if c.Kind == notify.KindNtfy || c.Kind == notify.KindEmail {
			items, err := s.deployer.NotificationSecret(ctx, c.ID)
			if err != nil {
				s.log.Warn("read the secrets of a notification channel", "channel", c.ID, "error", err)
			} else if c.Kind == notify.KindEmail {
				hasToken = items[secretPassword] != ""
			} else {
				// A channel made before the topic was a secret moves now.
				if err := s.moveNtfyTopic(ctx, &c, items); err != nil {
					s.log.Warn("move the topic of an ntfy channel into the secret store", "channel", c.ID, "error", err)
				}
				hasToken = items[secretToken] != ""
			}
		}
		// Whatever happened above, the list never returns a whole topic.
		if wholeURL(c.Address) {
			c.Address = shownAddress(c.Kind, c.Address)
		}
		out = append(out, channelView(c, hasToken))
	}
	return out, nil
}

// CreateNotificationChannel adds a channel. Errors are *app.FieldError naming
// "kind", "name", "url", "token", "password", "email.…", "telegram.…" or
// "events", or the secret store's.
func (s *Service) CreateNotificationChannel(ctx context.Context, in NotificationChannelInput) (CreatedNotificationChannel, error) {
	if !slices.Contains(notify.Kinds, in.Kind) {
		return CreatedNotificationChannel{}, app.NewFieldError("kind", "must be one of: %s", strings.Join(notify.Kinds, ", "))
	}
	name, err := channelName(in.Name)
	if err != nil {
		return CreatedNotificationChannel{}, err
	}
	if err := checkKindFields(in); err != nil {
		return CreatedNotificationChannel{}, err
	}
	events, err := channelEvents(in.Events)
	if err != nil {
		return CreatedNotificationChannel{}, err
	}

	// What is shown, what is kept in the row, and what is secret, by kind.
	var shown string
	var settings channelSettings
	secrets := map[string]string{}
	var signing string
	hasToken := false
	switch in.Kind {
	case notify.KindEmail:
		email, err := checkEmail(*in.Email)
		if err != nil {
			return CreatedNotificationChannel{}, err
		}
		if err := checkSMTPPassword(in.Password); err != nil {
			return CreatedNotificationChannel{}, err
		}
		settings.Email = &email
		shown = emailAddress(email)
		secrets[secretPassword] = in.Password
		hasToken = in.Password != ""
	case notify.KindTelegram:
		if err := checkBotToken(in.Token); err != nil {
			return CreatedNotificationChannel{}, err
		}
		telegram, err := checkTelegram(*in.Telegram)
		if err != nil {
			return CreatedNotificationChannel{}, err
		}
		settings.Telegram = &telegram
		shown = telegramAddress(telegram)
		secrets[secretToken] = in.Token
	default:
		address, err := channelURL(in.Kind, in.URL)
		if err != nil {
			return CreatedNotificationChannel{}, err
		}
		shown = shownAddress(in.Kind, address)
		switch in.Kind {
		case notify.KindSlack, notify.KindDiscord:
			secrets[secretURL] = address
		case notify.KindNtfy:
			secrets[secretURL] = address
			secrets[secretToken] = in.Token
			hasToken = in.Token != ""
		case notify.KindWebhook:
			if signing, err = newSigningSecret(); err != nil {
				return CreatedNotificationChannel{}, err
			}
			secrets[secretSigningSecret] = signing
		}
	}

	id, err := newID("nch_")
	if err != nil {
		return CreatedNotificationChannel{}, err
	}
	// The secret first: a channel listed in the console always has one.
	if err := s.deployer.SaveNotificationSecret(ctx, id, secrets); err != nil {
		return CreatedNotificationChannel{}, err
	}
	now := time.Now().UTC()
	row := store.NotificationChannel{
		ID: id, Kind: in.Kind, Name: name, Address: shown,
		Events: events, Paused: in.Paused, Settings: encodeSettings(settings), CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.SaveNotificationChannel(ctx, row); err != nil {
		// Don't leave a secret nobody can reach.
		_ = s.deployer.DeleteNotificationSecret(context.WithoutCancel(ctx), id)
		return CreatedNotificationChannel{}, err
	}
	s.noteSetting(ctx, audit.ActionCreated, channelResource(row), nil,
		diffMaps(nil, channelAuditView(row, channelSecrets{}, channelSecretsOf(secrets)))...)
	return CreatedNotificationChannel{NotificationChannel: channelView(row, hasToken), SigningSecret: signing}, nil
}

// checkKindFields refuses an input that carries a field of another kind, naming
// it, so a form that sent too much (or the wrong thing) is told at once. It
// checks which fields are present, not their values.
func checkKindFields(in NotificationChannelInput) error {
	email, telegram := in.Kind == notify.KindEmail, in.Kind == notify.KindTelegram
	switch {
	case (email || telegram) && strings.TrimSpace(in.URL) != "":
		return app.NewFieldError("url", "is not used by an email or a Telegram channel")
	case email && in.Email == nil:
		return app.NewFieldError("email", "is required for an email channel")
	case !email && in.Email != nil:
		return app.NewFieldError("email", "is only for an email channel")
	case !email && in.Password != "":
		return app.NewFieldError("password", "is only for an email channel")
	case telegram && in.Telegram == nil:
		return app.NewFieldError("telegram", "is required for a Telegram channel")
	case !telegram && in.Telegram != nil:
		return app.NewFieldError("telegram", "is only for a Telegram channel")
	case in.Token != "" && in.Kind != notify.KindNtfy && !telegram:
		return app.NewFieldError("token", "is only for an ntfy or a Telegram channel")
	}
	return nil
}

// UpdateNotificationChannel changes what the patch names and keeps the rest. An
// unknown channel is store.ErrNotFound. The kind never changes.
func (s *Service) UpdateNotificationChannel(ctx context.Context, id string, p NotificationChannelPatch) (NotificationChannel, error) {
	row, err := s.store.GetNotificationChannel(ctx, id)
	if err != nil {
		return NotificationChannel{}, err
	}
	if err := checkPatchFields(row.Kind, p); err != nil {
		return NotificationChannel{}, err
	}
	was := row
	if p.Name != nil {
		if row.Name, err = channelName(*p.Name); err != nil {
			return NotificationChannel{}, err
		}
	}
	if p.Events != nil {
		if row.Events, err = channelEvents(*p.Events); err != nil {
			return NotificationChannel{}, err
		}
	}
	if p.Paused != nil {
		row.Paused = *p.Paused
	}

	// The settings of an email or Telegram channel are replaced whole, and the
	// address the list shows follows them.
	settings := decodeSettings(row.Settings)
	if p.Email != nil {
		email, err := checkEmail(*p.Email)
		if err != nil {
			return NotificationChannel{}, err
		}
		settings.Email = &email
		row.Address = emailAddress(email)
	}
	if p.Telegram != nil {
		telegram, err := checkTelegram(*p.Telegram)
		if err != nil {
			return NotificationChannel{}, err
		}
		settings.Telegram = &telegram
		row.Address = telegramAddress(telegram)
	}
	if p.Email != nil || p.Telegram != nil {
		row.Settings = encodeSettings(settings)
	}

	// Work out the new secret items, if any change. The kinds that show whether
	// a secret is set (ntfy, email) read them every time.
	var items map[string]string
	if p.URL != nil || p.Token != nil || p.Password != nil || row.Kind == notify.KindNtfy || row.Kind == notify.KindEmail {
		if items, err = s.deployer.NotificationSecret(ctx, id); err != nil {
			return NotificationChannel{}, err
		}
	}
	// A channel made before the topic was a secret moves with this save.
	stored := channelSecretsOf(items)
	changed := carryNtfyTopic(&row, items)
	sent := channelSecrets{}
	if p.URL != nil {
		if strings.TrimSpace(*p.URL) == "" {
			return NotificationChannel{}, app.NewFieldError("url", "is empty; leave it out to keep the address set before")
		}
		address, err := channelURL(row.Kind, *p.URL)
		if err != nil {
			return NotificationChannel{}, err
		}
		row.Address = shownAddress(row.Kind, address)
		if row.Kind != notify.KindWebhook {
			items[secretURL] = address
			changed = true
			sent.url = true
		}
	}
	if p.Token != nil {
		if row.Kind == notify.KindTelegram {
			if err := checkBotToken(*p.Token); err != nil {
				return NotificationChannel{}, err
			}
		}
		items[secretToken] = *p.Token
		changed = true
		sent.token = *p.Token != ""
	}
	if p.Password != nil {
		if err := checkSMTPPassword(*p.Password); err != nil {
			return NotificationChannel{}, err
		}
		items[secretPassword] = *p.Password
		changed = true
		sent.password = *p.Password != ""
	}
	if changed {
		if err := s.deployer.SaveNotificationSecret(ctx, id, items); err != nil {
			return NotificationChannel{}, err
		}
	}

	row.UpdatedAt = time.Now().UTC()
	if err := s.store.SaveNotificationChannel(ctx, row); err != nil {
		return NotificationChannel{}, err
	}
	if changes := diffMaps(channelAuditView(was, stored, channelSecrets{}), channelAuditView(row, stored.with(sent), sent)); len(changes) > 0 {
		action := audit.ActionUpdated
		s.noteSetting(ctx, action, channelResource(row), nil, changes...)
	} else {
		s.noChange(ctx)
	}
	hasToken := false
	switch row.Kind {
	case notify.KindNtfy:
		hasToken = items[secretToken] != ""
	case notify.KindEmail:
		hasToken = items[secretPassword] != ""
	}
	return channelView(row, hasToken), nil
}

// checkPatchFields refuses a patch that names a field of another kind than the
// channel's, naming it.
func checkPatchFields(kind string, p NotificationChannelPatch) error {
	isURLKind := kind != notify.KindEmail && kind != notify.KindTelegram
	switch {
	case p.URL != nil && !isURLKind:
		return app.NewFieldError("url", "is not used by an email or a Telegram channel")
	case p.Email != nil && kind != notify.KindEmail:
		return app.NewFieldError("email", "is only for an email channel")
	case p.Password != nil && kind != notify.KindEmail:
		return app.NewFieldError("password", "is only for an email channel")
	case p.Telegram != nil && kind != notify.KindTelegram:
		return app.NewFieldError("telegram", "is only for a Telegram channel")
	case p.Token != nil && kind != notify.KindNtfy && kind != notify.KindTelegram:
		return app.NewFieldError("token", "is only for an ntfy or a Telegram channel")
	case p.Token != nil && kind == notify.KindTelegram && *p.Token == "":
		return app.NewFieldError("token", "is required for a Telegram channel")
	}
	return nil
}

// DeleteNotificationChannel removes a channel, its secrets and its deliveries.
// An unknown channel is store.ErrNotFound.
func (s *Service) DeleteNotificationChannel(ctx context.Context, id string) error {
	row, err := s.store.GetNotificationChannel(ctx, id)
	if err != nil {
		return err
	}
	// The secret first, so a failure leaves the channel listed and the delete
	// can be tried again.
	if err := s.deployer.DeleteNotificationSecret(ctx, id); err != nil {
		return err
	}
	if err := s.store.DeleteNotificationChannel(ctx, id); err != nil {
		return err
	}
	s.noteSetting(ctx, audit.ActionDeleted, channelResource(row), nil)
	return nil
}

// RotateNotificationSecret makes a new signing secret for a webhook channel and
// returns it, this once; the old one stops working at once. A channel of
// another kind is ErrNotAWebhook, an unknown one store.ErrNotFound.
func (s *Service) RotateNotificationSecret(ctx context.Context, id string) (string, error) {
	row, err := s.store.GetNotificationChannel(ctx, id)
	if err != nil {
		return "", err
	}
	if row.Kind != notify.KindWebhook {
		return "", ErrNotAWebhook
	}
	signing, err := newSigningSecret()
	if err != nil {
		return "", err
	}
	if err := s.deployer.SaveNotificationSecret(ctx, id, map[string]string{secretSigningSecret: signing}); err != nil {
		return "", err
	}
	row.UpdatedAt = time.Now().UTC()
	if err := s.store.SaveNotificationChannel(ctx, row); err != nil {
		return "", err
	}
	s.noteSetting(ctx, audit.ActionRotated, channelResource(row), nil)
	return signing, nil
}

// TestNotificationChannel sends a sample message to a channel at once, records
// the delivery (sent, or failed with the reason) and returns it. A receiver
// that refuses the message is an outcome, not an error. A paused channel can
// be tested too. An unknown channel is store.ErrNotFound.
func (s *Service) TestNotificationChannel(ctx context.Context, id string) (store.Delivery, error) {
	row, err := s.store.GetNotificationChannel(ctx, id)
	if err != nil {
		return store.Delivery{}, err
	}
	ch, err := s.notifyChannel(ctx, row)
	if err != nil {
		return store.Delivery{}, err
	}

	now := time.Now().UTC()
	ev := notify.Event{
		Type:  notify.EventTest,
		Title: "Test message from Caravana",
		Text:  fmt.Sprintf("If you can read this, the channel %q is set up.", row.Name),
		At:    now,
	}
	if s.PublicURL() != "" {
		ev.URL = s.PublicURL() + "/settings/notifications"
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		return store.Delivery{}, fmt.Errorf("encode the test message: %w", err)
	}
	deliveryID, err := newID("ndl_")
	if err != nil {
		return store.Delivery{}, err
	}

	d := store.Delivery{
		ID: deliveryID, ChannelID: row.ID, EventType: ev.Type, Title: ev.Title, Payload: string(payload),
		Status: store.DeliverySent, Attempts: 1, NextAt: now, CreatedAt: now,
	}
	// notify's errors never hold the address or a secret, so this is safe to store.
	if err := notify.Deliver(ctx, s.notifyClient, ch, ev, deliveryID, now); err != nil {
		d.Status, d.Error = store.DeliveryFailed, err.Error()
	}
	finished := time.Now().UTC()
	d.FinishedAt = &finished
	if err := s.store.EnqueueDelivery(ctx, d); err != nil {
		return store.Delivery{}, err
	}
	s.noteSetting(ctx, audit.ActionTested, channelResource(row), nil)
	if err := s.store.PruneDeliveries(ctx, row.ID, recentDeliveries); err != nil {
		s.log.Warn("prune notification deliveries", "channel", row.ID, "error", err)
	}
	return d, nil
}

// NotificationDeliveries returns the last 20 deliveries of a channel, newest
// first. An unknown channel is store.ErrNotFound.
func (s *Service) NotificationDeliveries(ctx context.Context, id string) ([]store.Delivery, error) {
	if _, err := s.store.GetNotificationChannel(ctx, id); err != nil {
		return nil, err
	}
	return s.store.ListDeliveries(ctx, id, recentDeliveries)
}

// notifyChannel reads a channel's secrets and returns what notify needs to
// send to it. The secrets are held only for the call.
func (s *Service) notifyChannel(ctx context.Context, row store.NotificationChannel) (notify.Channel, error) {
	items, err := s.deployer.NotificationSecret(ctx, row.ID)
	if err != nil {
		return notify.Channel{}, err
	}
	if err := s.moveNtfyTopic(ctx, &row, items); err != nil {
		return notify.Channel{}, err
	}
	ch := notify.Channel{Kind: row.Kind, Address: row.Address}
	settings := decodeSettings(row.Settings)
	switch row.Kind {
	case notify.KindEmail:
		e := settings.Email
		if e == nil {
			return notify.Channel{}, fmt.Errorf("the mail settings of channel %q are missing", row.Name)
		}
		// No password is fine: not every server asks for a login.
		ch.Address = ""
		ch.Secret = items[secretPassword]
		ch.SMTP = &notify.SMTP{Host: e.Host, Port: e.Port, Security: e.Security, Username: e.Username, From: e.From, To: e.To}
	case notify.KindTelegram:
		t := settings.Telegram
		if t == nil {
			return notify.Channel{}, fmt.Errorf("the settings of channel %q are missing", row.Name)
		}
		if items[secretToken] == "" {
			return notify.Channel{}, fmt.Errorf("the bot token of channel %q is missing from the secret store", row.Name)
		}
		ch.Address = notify.DefaultTelegramAPI
		if t.APIURL != "" {
			ch.Address = t.APIURL
		}
		ch.ChatID = t.ChatID
		ch.Secret = items[secretToken]
	case notify.KindSlack, notify.KindDiscord, notify.KindNtfy:
		if items[secretURL] == "" {
			return notify.Channel{}, fmt.Errorf("the address of channel %q is missing from the secret store", row.Name)
		}
		ch.Address = items[secretURL]
		if row.Kind == notify.KindNtfy {
			ch.Secret = items[secretToken]
		}
	case notify.KindWebhook:
		if items[secretSigningSecret] == "" {
			return notify.Channel{}, fmt.Errorf("the signing secret of channel %q is missing from the secret store", row.Name)
		}
		ch.Secret = items[secretSigningSecret]
	}
	return ch, nil
}

// channelName trims and checks a channel's name.
func channelName(name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", app.NewFieldError("name", "is required")
	case len(name) > 80:
		return "", app.NewFieldError("name", "is too long: at most 80 characters")
	}
	return name, nil
}

// channelURL checks a channel's address and returns it trimmed. It never
// echoes the address in an error, because a Slack or Discord URL is a secret.
// The address of a webhook is shown in the console, and an ntfy topic may not
// carry a user name and password because the token has its own field.
func channelURL(kind, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", app.NewFieldError("url", "is required")
	}
	u, err := notify.ParseAddress(raw)
	if err != nil {
		return "", app.NewFieldError("url", "must be an absolute http or https URL")
	}
	if u.User != nil && (kind == notify.KindWebhook || kind == notify.KindNtfy) {
		return "", app.NewFieldError("url", "must not hold a user name or password; an ntfy token has its own field")
	}
	return raw, nil
}

// channelEvents checks and normalizes the events a channel wants: at least one,
// all known, no repeats, in the order of NotificationEvents.
func channelEvents(events []string) ([]string, error) {
	for _, e := range events {
		if !slices.Contains(NotificationEvents, e) {
			return nil, app.NewFieldError("events", "has an unknown event; use %s", strings.Join(NotificationEvents, ", "))
		}
	}
	var out []string
	for _, e := range NotificationEvents {
		if slices.Contains(events, e) {
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		return nil, app.NewFieldError("events", "needs at least one event")
	}
	return out, nil
}

// shownAddress is what the console lists for a channel's address. A webhook's
// is shown whole. A Slack or Discord incoming-webhook URL is itself the secret,
// so only the host and the start of the path remain. An ntfy topic is the only
// access control of a public topic, so only its host is shown:
//
//	https://hooks.slack.com/services/T000/B000/XXXX → hooks.slack.com/services/T…/••••
//	https://discord.com/api/webhooks/123/abc        → discord.com/api/webhooks/1…/••••
//	https://ntfy.sh/my-topic                        → ntfy.sh/••••
func shownAddress(kind, address string) string {
	if kind == notify.KindWebhook {
		return address
	}
	u, err := url.Parse(address)
	if err != nil || u.Host == "" {
		return "••••" // can't happen after channelURL; never show the raw text
	}
	if kind == notify.KindNtfy {
		return u.Host + "/••••"
	}
	path := strings.Trim(u.Path, "/")
	if path == "" {
		return u.Host
	}
	anchor := "services"
	if kind == notify.KindDiscord {
		anchor = "webhooks"
	}
	segments := strings.Split(path, "/")
	if i := slices.Index(segments, anchor); i >= 0 && i+1 < len(segments) && segments[i+1] != "" {
		first := []rune(segments[i+1])[0] // one character of the next part, to tell channels apart
		return u.Host + "/" + strings.Join(segments[:i+1], "/") + "/" + string(first) + "…/••••"
	}
	return u.Host + "/••••"
}

// wholeURL reports whether address is still a whole URL rather than the masked
// form the row keeps.
func wholeURL(address string) bool {
	_, err := notify.ParseAddress(address)
	return err == nil
}

// carryNtfyTopic moves the topic of an ntfy channel made before the topic was
// a secret, whose row still holds the whole URL, into items (unless the
// variable already has one) and masks the row. It reports whether it moved
// anything; the caller writes the variable and the row.
func carryNtfyTopic(row *store.NotificationChannel, items map[string]string) bool {
	if row.Kind != notify.KindNtfy || !wholeURL(row.Address) {
		return false
	}
	if items[secretURL] == "" {
		items[secretURL] = row.Address
	}
	row.Address = shownAddress(row.Kind, row.Address)
	return true
}

// moveNtfyTopic finishes the move of an ntfy topic into the secret store for
// a channel made before the topic was a secret: the variable gets the url
// item and the row the masked address. A channel already moved, or of
// another kind, is left alone.
func (s *Service) moveNtfyTopic(ctx context.Context, row *store.NotificationChannel, items map[string]string) error {
	if !carryNtfyTopic(row, items) {
		return nil
	}
	if err := s.deployer.SaveNotificationSecret(ctx, row.ID, items); err != nil {
		return err
	}
	return s.store.SaveNotificationChannel(ctx, *row)
}

// newID returns prefix and 12 random hex digits.
func newID(prefix string) (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("make an ID: %w", err)
	}
	return prefix + hex.EncodeToString(b[:]), nil
}

// newSigningSecret returns 32 random bytes in hex, the secret a webhook's
// receiver checks signatures with.
func newSigningSecret() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("make a signing secret: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// defaultNotifyClient is the HTTP client deliveries go through. Send adds the
// 10-second timeout per delivery.
func defaultNotifyClient() *http.Client { return &http.Client{} }
