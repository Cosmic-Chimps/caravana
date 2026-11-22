package api

import (
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Notification channels (feature 018). No type here has a field for a Slack or
// Discord URL, an ntfy or bot token, an SMTP password or a signing secret,
// except the two answers that show a signing secret once (a webhook's creation
// and its rotation), so no other answer can carry one.

// notificationChannelJSON is a channel as every answer but a webhook's creation
// shows it (schema NotificationChannel).
type notificationChannelJSON struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Name     string   `json:"name"`
	Address  string   `json:"address"`
	Events   []string `json:"events"`
	Paused   bool     `json:"paused"`
	HasToken bool     `json:"hasToken"`
	// Email and Telegram are the non-secret settings of their kinds.
	Email     *emailJSON    `json:"email,omitempty"`
	Telegram  *telegramJSON `json:"telegram,omitempty"`
	CreatedAt time.Time     `json:"createdAt"`
	UpdatedAt time.Time     `json:"updatedAt"`
}

// emailJSON is the mail server and addresses of an email channel (schema
// NotificationEmail), in a request and in an answer. The password is not part of it.
type emailJSON struct {
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	Security string   `json:"security"`
	Username string   `json:"username,omitempty"`
	From     string   `json:"from"`
	To       []string `json:"to"`
}

// telegramJSON is where a Telegram channel posts (schema NotificationTelegram).
// The bot token is not part of it.
type telegramJSON struct {
	ChatID string `json:"chatId"`
	APIURL string `json:"apiUrl,omitempty"`
}

func (e *emailJSON) settings() *service.EmailSettings {
	if e == nil {
		return nil
	}
	return &service.EmailSettings{Host: e.Host, Port: e.Port, Security: e.Security, Username: e.Username, From: e.From, To: e.To}
}

func (t *telegramJSON) settings() *service.TelegramSettings {
	if t == nil {
		return nil
	}
	return &service.TelegramSettings{ChatID: t.ChatID, APIURL: t.APIURL}
}

// createdNotificationChannelJSON is the answer to creating a channel (schema
// NotificationChannelCreated): the channel, and a webhook's signing secret,
// which is never shown again.
type createdNotificationChannelJSON struct {
	notificationChannelJSON
	SigningSecret string `json:"signingSecret,omitempty"`
}

// deliveryJSON is one message to a channel (schema NotificationDelivery).
type deliveryJSON struct {
	ID         string     `json:"id"`
	EventType  string     `json:"eventType"`
	Title      string     `json:"title"`
	Status     string     `json:"status"`
	Attempts   int        `json:"attempts"`
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

func toNotificationChannelJSON(c service.NotificationChannel) notificationChannelJSON {
	events := c.Events
	if events == nil {
		events = []string{}
	}
	out := notificationChannelJSON{
		ID: c.ID, Kind: c.Kind, Name: c.Name, Address: c.Address, Events: events, Paused: c.Paused,
		HasToken: c.HasToken, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
	if e := c.Email; e != nil {
		out.Email = &emailJSON{Host: e.Host, Port: e.Port, Security: e.Security, Username: e.Username, From: e.From, To: e.To}
		if out.Email.To == nil {
			out.Email.To = []string{}
		}
	}
	if t := c.Telegram; t != nil {
		out.Telegram = &telegramJSON{ChatID: t.ChatID, APIURL: t.APIURL}
	}
	return out
}

func toDeliveryJSON(d store.Delivery) deliveryJSON {
	return deliveryJSON{
		ID: d.ID, EventType: d.EventType, Title: d.Title, Status: d.Status, Attempts: d.Attempts,
		Error: d.Error, CreatedAt: d.CreatedAt, FinishedAt: d.FinishedAt,
	}
}

// listNotificationChannels answers GET /v1/notifications/channels.
func (s *server) listNotificationChannels(w http.ResponseWriter, r *http.Request) error {
	channels, err := s.svc.NotificationChannels(r.Context())
	if err != nil {
		return err
	}
	out := make([]notificationChannelJSON, 0, len(channels))
	for _, c := range channels {
		out = append(out, toNotificationChannelJSON(c))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// createNotificationChannel answers POST /v1/notifications/channels with 201.
// The answer for a webhook carries its signing secret, this once.
func (s *server) createNotificationChannel(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Kind     string        `json:"kind"`
		Name     string        `json:"name"`
		URL      string        `json:"url"`
		Token    string        `json:"token"`
		Password string        `json:"password"`
		Email    *emailJSON    `json:"email"`
		Telegram *telegramJSON `json:"telegram"`
		Events   []string      `json:"events"`
		Paused   bool          `json:"paused"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	created, err := s.svc.CreateNotificationChannel(r.Context(), service.NotificationChannelInput{
		Kind: in.Kind, Name: in.Name, URL: in.URL, Token: in.Token, Password: in.Password,
		Email: in.Email.settings(), Telegram: in.Telegram.settings(), Events: in.Events, Paused: in.Paused,
	})
	if err != nil {
		return registryInvalid(err) // field errors are 422 here, as for registries
	}
	writeJSON(w, http.StatusCreated, createdNotificationChannelJSON{
		notificationChannelJSON: toNotificationChannelJSON(created.NotificationChannel),
		SigningSecret:           created.SigningSecret,
	})
	return nil
}

// updateNotificationChannel answers PATCH /v1/notifications/channels/{id}. A
// field left out keeps its value; "token" set to "" removes an ntfy token and
// "password" set to "" an SMTP password.
func (s *server) updateNotificationChannel(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name     *string       `json:"name"`
		URL      *string       `json:"url"`
		Token    *string       `json:"token"`
		Password *string       `json:"password"`
		Email    *emailJSON    `json:"email"`
		Telegram *telegramJSON `json:"telegram"`
		Events   *[]string     `json:"events"`
		Paused   *bool         `json:"paused"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	c, err := s.svc.UpdateNotificationChannel(r.Context(), r.PathValue("id"), service.NotificationChannelPatch{
		Name: in.Name, URL: in.URL, Token: in.Token, Password: in.Password,
		Email: in.Email.settings(), Telegram: in.Telegram.settings(), Events: in.Events, Paused: in.Paused,
	})
	if err != nil {
		return registryInvalid(err)
	}
	writeJSON(w, http.StatusOK, toNotificationChannelJSON(c))
	return nil
}

// deleteNotificationChannel answers DELETE /v1/notifications/channels/{id}.
func (s *server) deleteNotificationChannel(w http.ResponseWriter, r *http.Request) error {
	if err := s.svc.DeleteNotificationChannel(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// testNotificationChannel answers POST /v1/notifications/channels/{id}/test.
// The message is sent before the answer; a receiver that refuses it is an
// outcome in the delivery's status, so the answer is 200 either way.
func (s *server) testNotificationChannel(w http.ResponseWriter, r *http.Request) error {
	d, err := s.svc.TestNotificationChannel(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toDeliveryJSON(d))
	return nil
}

// listNotificationDeliveries answers GET /v1/notifications/channels/{id}/deliveries.
func (s *server) listNotificationDeliveries(w http.ResponseWriter, r *http.Request) error {
	ds, err := s.svc.NotificationDeliveries(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	out := make([]deliveryJSON, 0, len(ds))
	for _, d := range ds {
		out = append(out, toDeliveryJSON(d))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// rotateNotificationSecret answers POST /v1/notifications/channels/{id}/secret:
// a new signing secret for a webhook channel, shown this once. Another kind of
// channel is 409 not_a_webhook (see mapError).
func (s *server) rotateNotificationSecret(w http.ResponseWriter, r *http.Request) error {
	secret, err := s.svc.RotateNotificationSecret(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, struct {
		SigningSecret string `json:"signingSecret"`
	}{secret})
	return nil
}
