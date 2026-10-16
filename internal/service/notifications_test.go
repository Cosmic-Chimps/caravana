package service

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
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

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/notify"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// notifySecret is a value no page, answer, record or SQLite file may hold. It
// stands for the secret path of a Slack URL, an ntfy token or a signing secret.
const notifySecret = "SENTINEL-notify-secret-0123"

// receiver is an httptest server that records what it is sent and answers with
// status.
type receiver struct {
	*httptest.Server
	status int
	got    []*http.Request
	bodies []string
}

func newReceiver(t *testing.T, status int) *receiver {
	t.Helper()
	r := &receiver{status: status}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.got, r.bodies = append(r.got, req), append(r.bodies, string(b))
		w.WriteHeader(r.status)
		if r.status >= 400 {
			_, _ = io.WriteString(w, "nope")
		}
	}))
	t.Cleanup(r.Close)
	return r
}

func channelInput(kind, url string) NotificationChannelInput {
	return NotificationChannelInput{Kind: kind, Name: "phone", URL: url, Events: []string{EventProblems, EventDeployFailed}}
}

func fieldOf(err error) string {
	var fe *app.FieldError
	if errors.As(err, &fe) {
		return fe.Field
	}
	return ""
}

func TestCreateNotificationChannelValidation(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)

	tests := []struct {
		name  string
		mod   func(in *NotificationChannelInput)
		field string // "" means valid
	}{
		{"valid ntfy", func(in *NotificationChannelInput) {}, ""},
		{"unknown kind", func(in *NotificationChannelInput) { in.Kind = "carrier-pigeon" }, "kind"},
		{"empty kind", func(in *NotificationChannelInput) { in.Kind = "" }, "kind"},
		{"no name", func(in *NotificationChannelInput) { in.Name = "  " }, "name"},
		{"name too long", func(in *NotificationChannelInput) { in.Name = strings.Repeat("n", 81) }, "name"},
		{"no url", func(in *NotificationChannelInput) { in.URL = "" }, "url"},
		{"relative url", func(in *NotificationChannelInput) { in.URL = "/topic" }, "url"},
		{"ftp url", func(in *NotificationChannelInput) { in.URL = "ftp://ntfy.sh/topic" }, "url"},
		{"url with a password", func(in *NotificationChannelInput) { in.URL = "https://u:p@ntfy.sh/topic" }, "url"},
		{"no events", func(in *NotificationChannelInput) { in.Events = nil }, "events"},
		{"unknown event", func(in *NotificationChannelInput) { in.Events = []string{"everything"} }, "events"},
		{"token on a webhook", func(in *NotificationChannelInput) { in.Kind, in.Token = notify.KindWebhook, "tk" }, "token"},
		{"token on slack", func(in *NotificationChannelInput) { in.Kind, in.Token = notify.KindSlack, "tk" }, "token"},
		{"token on ntfy", func(in *NotificationChannelInput) { in.Token = "tk_" + notifySecret }, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := channelInput(notify.KindNtfy, "https://ntfy.sh/my-topic")
			tc.mod(&in)
			got, err := e.svc.CreateNotificationChannel(ctx, in)
			if tc.field != "" {
				if fieldOf(err) != tc.field {
					t.Fatalf("CreateNotificationChannel() = %v, want a field error on %q", err, tc.field)
				}
				return
			}
			if err != nil || got.ID == "" || !strings.HasPrefix(got.ID, "nch_") || len(got.ID) != 16 {
				t.Fatalf("CreateNotificationChannel() = %+v, %v", got, err)
			}
		})
	}

	t.Run("an invalid address is never echoed", func(t *testing.T) {
		in := channelInput(notify.KindSlack, "ftp://hooks.slack.com/services/"+notifySecret)
		_, err := e.svc.CreateNotificationChannel(ctx, in)
		if fieldOf(err) != "url" || strings.Contains(err.Error(), notifySecret) {
			t.Errorf("error = %v, want a url field error without the address", err)
		}
	})

	t.Run("events are ordered and deduplicated", func(t *testing.T) {
		in := channelInput(notify.KindNtfy, "https://ntfy.sh/t")
		in.Events = []string{EventDeploySucceeded, EventProblems, EventProblems}
		got, err := e.svc.CreateNotificationChannel(ctx, in)
		if err != nil || strings.Join(got.Events, ",") != "problems,deploy_succeeded" {
			t.Errorf("events = %v, %v; want problems,deploy_succeeded", got.Events, err)
		}
	})
}

func TestShownAddress(t *testing.T) {
	tests := []struct{ kind, url, want string }{
		{notify.KindSlack, "https://hooks.slack.com/services/T000/B000/XXXX", "hooks.slack.com/services/T…/••••"},
		{notify.KindDiscord, "https://discord.com/api/webhooks/123/abc", "discord.com/api/webhooks/1…/••••"},
		{notify.KindSlack, "https://chat.example.com/hook/abc?k=v", "chat.example.com/••••"},
		{notify.KindSlack, "https://chat.example.com/", "chat.example.com"},
		{notify.KindSlack, "https://chat.example.com/services/", "chat.example.com/••••"},
		{notify.KindWebhook, "https://hooks.example.com/caravana?x=1", "https://hooks.example.com/caravana?x=1"},
		{notify.KindNtfy, "https://ntfy.sh/my-topic", "https://ntfy.sh/my-topic"},
	}
	for _, tc := range tests {
		if got := shownAddress(tc.kind, tc.url); got != tc.want {
			t.Errorf("shownAddress(%s, %q) = %q, want %q", tc.kind, tc.url, got, tc.want)
		}
		if tc.kind == notify.KindSlack || tc.kind == notify.KindDiscord {
			if strings.Contains(tc.url, "XXXX") && strings.Contains(shownAddress(tc.kind, tc.url), "XXXX") {
				t.Errorf("shownAddress(%q) shows the secret", tc.url)
			}
		}
	}
}

// Each kind keeps its secret in the variable under the item the contract names,
// and the channel the service returns holds none of it.
func TestNotificationChannelSecrets(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)

	slackURL := "https://hooks.slack.com/services/T000/B000/" + notifySecret
	slack, err := e.svc.CreateNotificationChannel(ctx, channelInput(notify.KindSlack, slackURL))
	if err != nil {
		t.Fatal(err)
	}
	discord, err := e.svc.CreateNotificationChannel(ctx, channelInput(notify.KindDiscord, "https://discord.com/api/webhooks/123/"+notifySecret))
	if err != nil {
		t.Fatal(err)
	}
	in := channelInput(notify.KindNtfy, "https://ntfy.sh/my-topic")
	in.Token = "tk_" + notifySecret
	ntfy, err := e.svc.CreateNotificationChannel(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	bare, err := e.svc.CreateNotificationChannel(ctx, channelInput(notify.KindNtfy, "https://ntfy.sh/open-topic"))
	if err != nil {
		t.Fatal(err)
	}
	hook, err := e.svc.CreateNotificationChannel(ctx, channelInput(notify.KindWebhook, "https://hooks.example.com/caravana"))
	if err != nil {
		t.Fatal(err)
	}

	items := func(id string) map[string]string {
		t.Helper()
		got, err := e.svc.deployer.NotificationSecret(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := items(slack.ID); got["url"] != slackURL || len(got) != 1 {
		t.Errorf("slack items = %v, want only the url", got)
	}
	if got := items(discord.ID); got["url"] == "" || len(got) != 1 {
		t.Errorf("discord items = %v, want only the url", got)
	}
	if got := items(ntfy.ID); got["token"] != "tk_"+notifySecret || len(got) != 1 {
		t.Errorf("ntfy items = %v, want only the token", got)
	}
	if got := items(bare.ID); len(got) != 0 {
		t.Errorf("an ntfy channel without a token has items %v", got)
	}

	// The webhook's secret: 32 bytes in hex, returned once, stored in the variable.
	if len(hook.SigningSecret) != 64 {
		t.Fatalf("signing secret = %q, want 64 hex digits", hook.SigningSecret)
	}
	if _, err := hex.DecodeString(hook.SigningSecret); err != nil {
		t.Errorf("signing secret is not hex: %v", err)
	}
	if got := items(hook.ID); got["signing_secret"] != hook.SigningSecret || len(got) != 1 {
		t.Errorf("webhook items do not hold the signing secret")
	}
	if slack.SigningSecret != "" || ntfy.SigningSecret != "" {
		t.Error("a channel that is not a webhook was given a signing secret")
	}

	// What the service lists and returns holds none of it.
	list, err := e.svc.NotificationChannels(ctx)
	if err != nil || len(list) != 5 {
		t.Fatalf("NotificationChannels() = %d, %v; want 5", len(list), err)
	}
	// The channel structs a caller gets back, with the webhook's secret shown
	// once at creation taken out, hold no secret.
	hook.SigningSecret = ""
	for _, v := range []any{list, slack, discord, ntfy, hook} {
		raw, _ := json.Marshal(v)
		if strings.Contains(string(raw), notifySecret) {
			t.Errorf("a returned value holds a secret: %s", raw)
		}
	}
	for _, c := range list {
		switch c.ID {
		case ntfy.ID:
			if !c.HasToken {
				t.Error("the ntfy channel with a token lists HasToken false")
			}
		default:
			if c.HasToken {
				t.Errorf("channel %s lists HasToken true", c.Name)
			}
		}
		if strings.Contains(c.Address, notifySecret) {
			t.Errorf("the address of %s shows the secret: %s", c.ID, c.Address)
		}
	}
	if list[0].Address != "hooks.slack.com/services/T…/••••" {
		t.Errorf("slack address = %q", list[0].Address)
	}

	// The list has no signing secret: its type has no field for one. The
	// stored one is still in the variable, so look it up for the check.
	stored := e.secretItems(t, hook.ID)["signing_secret"]
	if got, _ := e.svc.NotificationChannels(ctx); strings.Contains(fmt.Sprintf("%+v", got), stored) {
		t.Error("the list holds the signing secret")
	}
}

func TestUpdateNotificationChannel(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)

	slack, err := e.svc.CreateNotificationChannel(ctx, channelInput(notify.KindSlack, "https://hooks.slack.com/services/T000/B000/"+notifySecret))
	if err != nil {
		t.Fatal(err)
	}
	in := channelInput(notify.KindNtfy, "https://ntfy.sh/my-topic")
	in.Token = "tk_old"
	ntfy, err := e.svc.CreateNotificationChannel(ctx, in)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("name, events and paused change; the secret is kept", func(t *testing.T) {
		got, err := e.svc.UpdateNotificationChannel(ctx, slack.ID, NotificationChannelPatch{
			Name: new("team"), Events: &[]string{EventDeploySucceeded}, Paused: new(true),
		})
		if err != nil || got.Name != "team" || !got.Paused || strings.Join(got.Events, ",") != "deploy_succeeded" {
			t.Fatalf("UpdateNotificationChannel() = %+v, %v", got, err)
		}
		if got := e.secretItems(t, slack.ID); !strings.HasSuffix(got["url"], notifySecret) {
			t.Error("the Slack URL was lost by an edit that did not name it")
		}
		if !got.UpdatedAt.After(slack.UpdatedAt) && !got.UpdatedAt.Equal(slack.UpdatedAt) {
			t.Errorf("UpdatedAt went backwards")
		}
		if got.CreatedAt != slack.CreatedAt {
			t.Errorf("CreatedAt changed from %v to %v", slack.CreatedAt, got.CreatedAt)
		}
	})

	t.Run("a new slack url replaces the secret and the shown address", func(t *testing.T) {
		got, err := e.svc.UpdateNotificationChannel(ctx, slack.ID, NotificationChannelPatch{URL: new("https://hooks.slack.com/services/TNEW/BNEW/second")})
		if err != nil || got.Address != "hooks.slack.com/services/T…/••••" {
			t.Fatalf("UpdateNotificationChannel() = %+v, %v", got, err)
		}
		if items := e.secretItems(t, slack.ID); items["url"] != "https://hooks.slack.com/services/TNEW/BNEW/second" {
			t.Errorf("stored url = %q", items["url"])
		}
	})

	t.Run("an empty url is refused, nil keeps it", func(t *testing.T) {
		_, err := e.svc.UpdateNotificationChannel(ctx, slack.ID, NotificationChannelPatch{URL: new("")})
		if fieldOf(err) != "url" {
			t.Errorf("UpdateNotificationChannel() with an empty url = %v, want a url field error", err)
		}
	})

	t.Run("the ntfy token is replaced, kept, and removed", func(t *testing.T) {
		got, err := e.svc.UpdateNotificationChannel(ctx, ntfy.ID, NotificationChannelPatch{Token: new("tk_new")})
		if err != nil || !got.HasToken || e.secretItems(t, ntfy.ID)["token"] != "tk_new" {
			t.Fatalf("replace the token: %+v, %v", got, err)
		}
		got, err = e.svc.UpdateNotificationChannel(ctx, ntfy.ID, NotificationChannelPatch{Name: new("tablet")})
		if err != nil || !got.HasToken || e.secretItems(t, ntfy.ID)["token"] != "tk_new" {
			t.Fatalf("an edit that does not name the token lost it: %+v, %v", got, err)
		}
		got, err = e.svc.UpdateNotificationChannel(ctx, ntfy.ID, NotificationChannelPatch{Token: new("")})
		if err != nil || got.HasToken || len(e.secretItems(t, ntfy.ID)) != 0 {
			t.Fatalf("remove the token: %+v, %v", got, err)
		}
	})

	t.Run("a webhook's url changes and its signing secret stays", func(t *testing.T) {
		hook, err := e.svc.CreateNotificationChannel(ctx, channelInput(notify.KindWebhook, "https://one.example.com/h"))
		if err != nil {
			t.Fatal(err)
		}
		got, err := e.svc.UpdateNotificationChannel(ctx, hook.ID, NotificationChannelPatch{URL: new("https://two.example.com/h")})
		if err != nil || got.Address != "https://two.example.com/h" {
			t.Fatalf("UpdateNotificationChannel() = %+v, %v", got, err)
		}
		if e.secretItems(t, hook.ID)["signing_secret"] != hook.SigningSecret {
			t.Error("the signing secret changed on a URL edit")
		}
	})

	t.Run("refusals", func(t *testing.T) {
		tests := []struct {
			name  string
			id    string
			patch NotificationChannelPatch
			field string
			want  error
		}{
			{"unknown channel", "nch_000000000000", NotificationChannelPatch{Name: new("x")}, "", store.ErrNotFound},
			{"empty name", ntfy.ID, NotificationChannelPatch{Name: new("")}, "name", nil},
			{"no events", ntfy.ID, NotificationChannelPatch{Events: &[]string{}}, "events", nil},
			{"bad url", ntfy.ID, NotificationChannelPatch{URL: new("topic")}, "url", nil},
			{"token on slack", slack.ID, NotificationChannelPatch{Token: new("x")}, "token", nil},
		}
		for _, tc := range tests {
			_, err := e.svc.UpdateNotificationChannel(ctx, tc.id, tc.patch)
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("%s: error = %v, want %v", tc.name, err, tc.want)
			}
			if tc.field != "" && fieldOf(err) != tc.field {
				t.Errorf("%s: error = %v, want a field error on %q", tc.name, err, tc.field)
			}
		}
	})
}

func (e *env) secretItems(t *testing.T, id string) map[string]string {
	t.Helper()
	got, err := e.svc.deployer.NotificationSecret(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestRotateNotificationSecret(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	hook, err := e.svc.CreateNotificationChannel(ctx, channelInput(notify.KindWebhook, "https://hooks.example.com/h"))
	if err != nil {
		t.Fatal(err)
	}
	ntfy, err := e.svc.CreateNotificationChannel(ctx, channelInput(notify.KindNtfy, "https://ntfy.sh/t"))
	if err != nil {
		t.Fatal(err)
	}

	next, err := e.svc.RotateNotificationSecret(ctx, hook.ID)
	if err != nil || len(next) != 64 || next == hook.SigningSecret {
		t.Fatalf("RotateNotificationSecret() = %q, %v; want a new 64-digit secret", next, err)
	}
	if e.secretItems(t, hook.ID)["signing_secret"] != next {
		t.Error("the new secret is not the stored one")
	}
	if _, err := e.svc.RotateNotificationSecret(ctx, ntfy.ID); !errors.Is(err, ErrNotAWebhook) {
		t.Errorf("rotating an ntfy channel = %v, want ErrNotAWebhook", err)
	}
	if _, err := e.svc.RotateNotificationSecret(ctx, "nch_000000000000"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("rotating an unknown channel = %v, want ErrNotFound", err)
	}
}

func TestDeleteNotificationChannel(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	hook, err := e.svc.CreateNotificationChannel(ctx, channelInput(notify.KindWebhook, "https://hooks.example.com/h"))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteNotificationChannel(ctx, hook.ID); err != nil {
		t.Fatalf("DeleteNotificationChannel() = %v", err)
	}
	if got := e.nomad.VariablePaths(); len(got) != 0 {
		t.Errorf("variables left behind: %v", got)
	}
	if list, _ := e.svc.NotificationChannels(ctx); len(list) != 0 {
		t.Errorf("channels left: %v", list)
	}
	if err := e.svc.DeleteNotificationChannel(ctx, hook.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("deleting twice = %v, want ErrNotFound", err)
	}
}

func TestTestNotificationChannel(t *testing.T) {
	ctx := context.Background()

	t.Run("a webhook that accepts: sent, signed, listed", func(t *testing.T) {
		e := newEnv(t)
		rcv := newReceiver(t, http.StatusOK)
		e.svc.SetPublicURL("https://caravana.example.com/")
		hook, err := e.svc.CreateNotificationChannel(ctx, channelInput(notify.KindWebhook, rcv.URL+"/hook"))
		if err != nil {
			t.Fatal(err)
		}
		d, err := e.svc.TestNotificationChannel(ctx, hook.ID)
		if err != nil || d.Status != store.DeliverySent || d.Attempts != 1 || d.Error != "" || d.FinishedAt == nil ||
			d.EventType != "test" || !strings.HasPrefix(d.ID, "ndl_") {
			t.Fatalf("TestNotificationChannel() = %+v, %v", d, err)
		}
		if len(rcv.got) != 1 {
			t.Fatalf("receiver got %d requests, want 1", len(rcv.got))
		}
		req := rcv.got[0]
		if req.Header.Get("Caravana-Event") != "test" || req.Header.Get("Caravana-Delivery") != d.ID {
			t.Errorf("headers: %v", req.Header)
		}
		if err := notify.Verify(hook.SigningSecret, req.Header.Get(notify.SignatureHeader), []byte(rcv.bodies[0]), time.Now(), time.Minute); err != nil {
			t.Errorf("signature does not verify with the secret shown at creation: %v", err)
		}
		if !strings.Contains(rcv.bodies[0], "https://caravana.example.com/settings#notifications") {
			t.Errorf("body has no link: %s", rcv.bodies[0])
		}

		listed, err := e.svc.NotificationDeliveries(ctx, hook.ID)
		if err != nil || len(listed) != 1 || listed[0].ID != d.ID {
			t.Errorf("NotificationDeliveries() = %+v, %v", listed, err)
		}
		if strings.Contains(listed[0].Payload, hook.SigningSecret) {
			t.Error("the stored payload holds the signing secret")
		}
	})

	t.Run("a receiver that answers 500: a failed delivery with the status, not an error", func(t *testing.T) {
		e := newEnv(t)
		rcv := newReceiver(t, http.StatusInternalServerError)
		slack, err := e.svc.CreateNotificationChannel(ctx, channelInput(notify.KindSlack, rcv.URL+"/services/T0/B0/"+notifySecret))
		if err != nil {
			t.Fatal(err)
		}
		d, err := e.svc.TestNotificationChannel(ctx, slack.ID)
		if err != nil || d.Status != store.DeliveryFailed || !strings.Contains(d.Error, "500") {
			t.Fatalf("TestNotificationChannel() = %+v, %v", d, err)
		}
		if strings.Contains(d.Error, notifySecret) {
			t.Errorf("the recorded error holds the URL's secret: %s", d.Error)
		}
		if listed, _ := e.svc.NotificationDeliveries(ctx, slack.ID); len(listed) != 1 || listed[0].Status != store.DeliveryFailed {
			t.Errorf("deliveries = %+v, want the failed one", listed)
		}
	})

	t.Run("an unreachable receiver is a failed delivery", func(t *testing.T) {
		e := newEnv(t)
		rcv := newReceiver(t, 200)
		url := rcv.URL
		rcv.Close()
		ntfy, err := e.svc.CreateNotificationChannel(ctx, channelInput(notify.KindNtfy, url+"/topic"))
		if err != nil {
			t.Fatal(err)
		}
		d, err := e.svc.TestNotificationChannel(ctx, ntfy.ID)
		if err != nil || d.Status != store.DeliveryFailed || d.Error == "" {
			t.Errorf("TestNotificationChannel() = %+v, %v", d, err)
		}
	})

	t.Run("ntfy sends its token and a paused channel can be tested", func(t *testing.T) {
		e := newEnv(t)
		rcv := newReceiver(t, 200)
		in := channelInput(notify.KindNtfy, rcv.URL+"/topic")
		in.Token, in.Paused = "tk_"+notifySecret, true
		ntfy, err := e.svc.CreateNotificationChannel(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		d, err := e.svc.TestNotificationChannel(ctx, ntfy.ID)
		if err != nil || d.Status != store.DeliverySent {
			t.Fatalf("TestNotificationChannel() = %+v, %v", d, err)
		}
		if got := rcv.got[0].Header.Get("Authorization"); got != "Bearer tk_"+notifySecret {
			t.Errorf("Authorization = %q", got)
		}
	})

	t.Run("an unknown channel", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.svc.TestNotificationChannel(ctx, "nch_000000000000"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("TestNotificationChannel() = %v, want ErrNotFound", err)
		}
		if _, err := e.svc.NotificationDeliveries(ctx, "nch_000000000000"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("NotificationDeliveries() = %v, want ErrNotFound", err)
		}
	})

	t.Run("deliveries are pruned to 20", func(t *testing.T) {
		e := newEnv(t)
		rcv := newReceiver(t, 200)
		hook, err := e.svc.CreateNotificationChannel(ctx, channelInput(notify.KindWebhook, rcv.URL))
		if err != nil {
			t.Fatal(err)
		}
		for range 23 {
			if _, err := e.svc.TestNotificationChannel(ctx, hook.ID); err != nil {
				t.Fatal(err)
			}
		}
		if listed, _ := e.svc.NotificationDeliveries(ctx, hook.ID); len(listed) != 20 {
			t.Errorf("deliveries = %d, want 20", len(listed))
		}
	})
}

// SC-005: the secrets are in no SQLite file (the write-ahead log included), no
// log line, and no value the service hands back except the signing secret
// shown once at creation.
func TestNotificationSecretsStayInTheVariable(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	e := newEnv(t) // only for its fake registry, Nomad and secret store
	var logs bytes.Buffer
	svc := New(st, newDeployer(t, e.registry, e.nomad), e.secrets, slog.New(slog.NewTextHandler(&logs, nil)))

	rcv := newReceiver(t, http.StatusInternalServerError) // failing, so errors are recorded
	ntfy := channelInput(notify.KindNtfy, rcv.URL+"/topic")
	ntfy.Token = "tk_" + notifySecret
	var signing []string
	var returned strings.Builder
	for _, in := range []NotificationChannelInput{
		channelInput(notify.KindSlack, rcv.URL+"/services/T0/B0/"+notifySecret),
		channelInput(notify.KindDiscord, rcv.URL+"/api/webhooks/1/"+notifySecret),
		ntfy,
		channelInput(notify.KindWebhook, rcv.URL+"/h"),
	} {
		created, err := svc.CreateNotificationChannel(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if created.SigningSecret != "" {
			signing = append(signing, created.SigningSecret)
		}
		created.SigningSecret = "" // the one place it may appear
		d, err := svc.TestNotificationChannel(ctx, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		listed, err := svc.NotificationDeliveries(ctx, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&returned, "%+v %+v %+v", created, d, listed)
	}
	list, err := svc.NotificationChannels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(&returned, "%+v", list)
	if len(signing) != 1 {
		t.Fatalf("signing secrets = %d, want 1 (the webhook)", len(signing))
	}

	secrets := []string{notifySecret, signing[0]}
	check := func(what, text string) {
		for _, s := range secrets {
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
