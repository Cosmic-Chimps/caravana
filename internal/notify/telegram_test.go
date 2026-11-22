package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const botToken = "123456789:AAH-sentinel_telegram_token_XXXXXXXX"

func TestRenderTelegramShape(t *testing.T) {
	req, err := Render(telegramChannel, backupFailed, "ndl_1", fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != http.MethodPost || req.URL.Path != "/bot"+botToken+"/sendMessage" || req.URL.Host != "api.telegram.org" {
		t.Errorf("request = %s %s", req.Method, req.URL)
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	raw, _ := io.ReadAll(req.Body)
	var body struct {
		ChatID string `json:"chat_id"`
		Text   string `json:"text"`
		Parse  string `json:"parse_mode"`
		Links  struct {
			Disabled bool `json:"is_disabled"`
		} `json:"link_preview_options"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("body %s: %v", raw, err)
	}
	if body.ChatID != "-1001234567890" || !body.Links.Disabled || body.Parse != "" {
		t.Errorf("body = %s", raw)
	}
	if !strings.HasPrefix(body.Text, backupFailed.Title+"\n") || !strings.HasSuffix(body.Text, "\n"+backupFailed.URL) {
		t.Errorf("text = %q", body.Text)
	}
}

func TestRenderTelegramRefuses(t *testing.T) {
	tests := []struct {
		name string
		ch   Channel
	}{
		{"no token", Channel{Kind: KindTelegram, Address: DefaultTelegramAPI, ChatID: "1"}},
		{"no chat", Channel{Kind: KindTelegram, Address: DefaultTelegramAPI, Secret: botToken}},
		{"no address", Channel{Kind: KindTelegram, ChatID: "1", Secret: botToken}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Render(tc.ch, backupFailed, "ndl_1", fixedNow)
			if err == nil {
				t.Fatal("Render succeeded, want an error")
			}
			if strings.Contains(err.Error(), botToken) {
				t.Errorf("error %q holds the token", err)
			}
		})
	}
}

// The bot token is in the request's path, so no failure of the Bot API may
// bring it back in an error: not when the answer echoes the path, not when it
// echoes the token alone, and not when the connection fails.
func TestDeliverTelegramErrorsHoldNoToken(t *testing.T) {
	answers := map[string]http.HandlerFunc{
		"echoes the path": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"ok":false,"description":"Not Found: `+r.URL.Path+`"}`, 404)
		},
		"echoes the token": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"ok":false,"description":"bad token `+botToken+`"}`, http.StatusUnauthorized)
		},
		"plain unauthorized": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`, http.StatusUnauthorized)
		},
	}
	for name, handler := range answers {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			defer srv.Close()
			ch := telegramChannel
			ch.Address = srv.URL
			err := Deliver(context.Background(), srv.Client(), ch, backupFailed, "ndl_1", fixedNow)
			if err == nil {
				t.Fatal("Deliver succeeded, want an error")
			}
			if strings.Contains(err.Error(), botToken) || strings.Contains(err.Error(), "sentinel_telegram") {
				t.Errorf("error %q holds the token", err)
			}
			if !strings.Contains(err.Error(), strings.TrimPrefix(srv.URL, "http://")) {
				t.Errorf("error %q does not name the host", err)
			}
		})
	}

	srv := httptest.NewServer(http.NotFoundHandler())
	ch := telegramChannel
	ch.Address = srv.URL
	srv.Close() // nothing listens there any more
	if err := Deliver(context.Background(), nil, ch, backupFailed, "ndl_1", fixedNow); err == nil || strings.Contains(err.Error(), "sentinel_telegram") {
		t.Errorf("Deliver to a closed port = %v, want an error without the token", err)
	}
}

func TestDeliverTelegram(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotPath, gotBody = r.URL.Path, string(b)
		io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	defer srv.Close()
	ch := telegramChannel
	ch.Address = srv.URL
	if err := Deliver(context.Background(), srv.Client(), ch, testMessage, "ndl_1", fixedNow); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if gotPath != "/bot"+botToken+"/sendMessage" || !strings.Contains(gotBody, `"chat_id":"-1001234567890"`) || !strings.Contains(gotBody, "Test message from Caravana") {
		t.Errorf("receiver got %s with %s", gotPath, gotBody)
	}
}
