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
	"github.com/cosmic-chimps/caravana/internal/notify"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Notification channels (feature 018). SQLite keeps what the console shows;
// the secret parts (a Slack or Discord URL, an ntfy token, a webhook's signing
// secret) live in a Nomad Variable (package deploy), written before the row
// that lists the channel, so a listed channel always has what it needs.

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
	secretURL           = "url"            // slack and discord: the whole URL
	secretToken         = "token"          // ntfy: the access token
	secretSigningSecret = "signing_secret" // webhook
)

// recentDeliveries is how many deliveries a channel keeps and lists (FR-010).
const recentDeliveries = 20

// ErrNotAWebhook is returned when a signing secret is asked for a channel that
// is not a webhook.
var ErrNotAWebhook = errors.New("only a webhook channel has a signing secret")

// NotificationChannel is a channel as the console and the API show it: no
// secret, only whether an ntfy token is set.
type NotificationChannel struct {
	store.NotificationChannel
	HasToken bool
}

// NotificationChannelInput is what adding a channel takes. Token is for ntfy
// only, and optional.
type NotificationChannelInput struct {
	Kind   string
	Name   string
	URL    string
	Token  string
	Events []string
	Paused bool
}

// NotificationChannelPatch is what editing a channel takes; a nil field keeps
// the stored value. Token set to "" removes the ntfy token. URL may not be
// empty: leave it nil to keep the address (which for Slack and Discord is the
// secret and is never shown).
type NotificationChannelPatch struct {
	Name   *string
	URL    *string
	Token  *string
	Events *[]string
	Paused *bool
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
		view := NotificationChannel{NotificationChannel: c}
		if c.Kind == notify.KindNtfy {
			items, err := s.deployer.NotificationSecret(ctx, c.ID)
			if err != nil {
				s.log.Warn("read the secrets of a notification channel", "channel", c.ID, "error", err)
			}
			view.HasToken = items[secretToken] != ""
		}
		out = append(out, view)
	}
	return out, nil
}

// CreateNotificationChannel adds a channel. Errors are *app.FieldError naming
// "kind", "name", "url", "token" or "events", or the secret store's.
func (s *Service) CreateNotificationChannel(ctx context.Context, in NotificationChannelInput) (CreatedNotificationChannel, error) {
	if !slices.Contains(notify.Kinds, in.Kind) {
		return CreatedNotificationChannel{}, app.NewFieldError("kind", "must be one of: %s", strings.Join(notify.Kinds, ", "))
	}
	name, err := channelName(in.Name)
	if err != nil {
		return CreatedNotificationChannel{}, err
	}
	address, err := channelURL(in.Kind, in.URL)
	if err != nil {
		return CreatedNotificationChannel{}, err
	}
	if in.Token != "" && in.Kind != notify.KindNtfy {
		return CreatedNotificationChannel{}, app.NewFieldError("token", "is only for an ntfy channel")
	}
	events, err := channelEvents(in.Events)
	if err != nil {
		return CreatedNotificationChannel{}, err
	}

	id, err := newID("nch_")
	if err != nil {
		return CreatedNotificationChannel{}, err
	}
	secrets := map[string]string{}
	var signing string
	switch in.Kind {
	case notify.KindSlack, notify.KindDiscord:
		secrets[secretURL] = address
	case notify.KindNtfy:
		secrets[secretToken] = in.Token
	case notify.KindWebhook:
		if signing, err = newSigningSecret(); err != nil {
			return CreatedNotificationChannel{}, err
		}
		secrets[secretSigningSecret] = signing
	}

	// The secret first: a channel listed in the console always has one.
	if err := s.deployer.SaveNotificationSecret(ctx, id, secrets); err != nil {
		return CreatedNotificationChannel{}, err
	}
	now := time.Now().UTC()
	row := store.NotificationChannel{
		ID: id, Kind: in.Kind, Name: name, Address: shownAddress(in.Kind, address),
		Events: events, Paused: in.Paused, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.SaveNotificationChannel(ctx, row); err != nil {
		// Don't leave a secret nobody can reach.
		_ = s.deployer.DeleteNotificationSecret(context.WithoutCancel(ctx), id)
		return CreatedNotificationChannel{}, err
	}
	return CreatedNotificationChannel{
		NotificationChannel: NotificationChannel{NotificationChannel: row, HasToken: in.Token != ""},
		SigningSecret:       signing,
	}, nil
}

// UpdateNotificationChannel changes what the patch names and keeps the rest. An
// unknown channel is store.ErrNotFound. The kind never changes.
func (s *Service) UpdateNotificationChannel(ctx context.Context, id string, p NotificationChannelPatch) (NotificationChannel, error) {
	row, err := s.store.GetNotificationChannel(ctx, id)
	if err != nil {
		return NotificationChannel{}, err
	}
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
	if p.Token != nil && row.Kind != notify.KindNtfy {
		return NotificationChannel{}, app.NewFieldError("token", "is only for an ntfy channel")
	}

	// Work out the new secret items, if any change.
	var items map[string]string
	if p.URL != nil || p.Token != nil {
		if items, err = s.deployer.NotificationSecret(ctx, id); err != nil {
			return NotificationChannel{}, err
		}
	}
	changed := false
	if p.URL != nil {
		if strings.TrimSpace(*p.URL) == "" {
			return NotificationChannel{}, app.NewFieldError("url", "is empty; leave it out to keep the address set before")
		}
		address, err := channelURL(row.Kind, *p.URL)
		if err != nil {
			return NotificationChannel{}, err
		}
		row.Address = shownAddress(row.Kind, address)
		if row.Kind == notify.KindSlack || row.Kind == notify.KindDiscord {
			items[secretURL] = address
			changed = true
		}
	}
	if p.Token != nil {
		items[secretToken] = *p.Token
		changed = true
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
	view := NotificationChannel{NotificationChannel: row}
	if row.Kind == notify.KindNtfy {
		stored, err := s.deployer.NotificationSecret(ctx, id)
		if err != nil {
			return NotificationChannel{}, err
		}
		view.HasToken = stored[secretToken] != ""
	}
	return view, nil
}

// DeleteNotificationChannel removes a channel, its secrets and its deliveries.
// An unknown channel is store.ErrNotFound.
func (s *Service) DeleteNotificationChannel(ctx context.Context, id string) error {
	if _, err := s.store.GetNotificationChannel(ctx, id); err != nil {
		return err
	}
	// The secret first, so a failure leaves the channel listed and the delete
	// can be tried again.
	if err := s.deployer.DeleteNotificationSecret(ctx, id); err != nil {
		return err
	}
	return s.store.DeleteNotificationChannel(ctx, id)
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
	if s.publicURL != "" {
		ev.URL = s.publicURL + "/settings#notifications"
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
	req, err := notify.Render(ch, ev, deliveryID, now)
	if err == nil {
		// notify's errors never hold the address, so this is safe to store.
		err = notify.Send(ctx, s.notifyClient, req)
	}
	if err != nil {
		d.Status, d.Error = store.DeliveryFailed, err.Error()
	}
	finished := time.Now().UTC()
	d.FinishedAt = &finished
	if err := s.store.EnqueueDelivery(ctx, d); err != nil {
		return store.Delivery{}, err
	}
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
	ch := notify.Channel{Kind: row.Kind, Address: row.Address}
	switch row.Kind {
	case notify.KindSlack, notify.KindDiscord:
		if items[secretURL] == "" {
			return notify.Channel{}, fmt.Errorf("the address of channel %q is missing from the secret store", row.Name)
		}
		ch.Address = items[secretURL]
	case notify.KindNtfy:
		ch.Secret = items[secretToken]
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
// The address of a webhook or ntfy topic is shown in the console, so it may
// not carry a user name and password.
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

// shownAddress is what the console lists for a channel's address. A webhook or
// ntfy topic is shown whole. A Slack or Discord incoming-webhook URL is itself
// the secret, so only the host and the start of the path remain:
//
//	https://hooks.slack.com/services/T000/B000/XXXX → hooks.slack.com/services/T…/••••
//	https://discord.com/api/webhooks/123/abc        → discord.com/api/webhooks/1…/••••
func shownAddress(kind, address string) string {
	if kind != notify.KindSlack && kind != notify.KindDiscord {
		return address
	}
	u, err := url.Parse(address)
	if err != nil || u.Host == "" {
		return "••••" // can't happen after channelURL; never show the raw text
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
