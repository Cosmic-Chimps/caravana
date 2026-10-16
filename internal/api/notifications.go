package api

import (
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Notification channels (feature 018). No type here has a field for a Slack or
// Discord URL, an ntfy token or a signing secret, except the two answers that
// show a signing secret once (a webhook's creation and its rotation), so no
// other answer can carry one.

// notificationChannelJSON is a channel as every answer but a webhook's creation
// shows it (schema NotificationChannel).
type notificationChannelJSON struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	Address   string    `json:"address"`
	Events    []string  `json:"events"`
	Paused    bool      `json:"paused"`
	HasToken  bool      `json:"hasToken"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
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
	return notificationChannelJSON{
		ID: c.ID, Kind: c.Kind, Name: c.Name, Address: c.Address, Events: events, Paused: c.Paused,
		HasToken: c.HasToken, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
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
		Kind   string   `json:"kind"`
		Name   string   `json:"name"`
		URL    string   `json:"url"`
		Token  string   `json:"token"`
		Events []string `json:"events"`
		Paused bool     `json:"paused"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	created, err := s.svc.CreateNotificationChannel(r.Context(), service.NotificationChannelInput{
		Kind: in.Kind, Name: in.Name, URL: in.URL, Token: in.Token, Events: in.Events, Paused: in.Paused,
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
// field left out keeps its value; "token" set to "" removes an ntfy token.
func (s *server) updateNotificationChannel(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name   *string   `json:"name"`
		URL    *string   `json:"url"`
		Token  *string   `json:"token"`
		Events *[]string `json:"events"`
		Paused *bool     `json:"paused"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	c, err := s.svc.UpdateNotificationChannel(r.Context(), r.PathValue("id"), service.NotificationChannelPatch{
		Name: in.Name, URL: in.URL, Token: in.Token, Events: in.Events, Paused: in.Paused,
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
