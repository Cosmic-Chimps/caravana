package notify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// secretPath stands in for the secret part of a Slack or Discord URL.
const secretPath = "/services/T000/B000/SENTINEL_SECRET"

func slackRequest(t *testing.T, base string) *http.Request {
	t.Helper()
	req, err := Render(Channel{Kind: KindSlack, Address: base + secretPath + "?k=SENTINEL_QUERY"}, backupFailed, "ndl_1", fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestSend(t *testing.T) {
	var gotBody string
	var gotType string
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody, gotType = string(b), r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusNoContent) // Discord answers 204
	}))
	defer ok.Close()

	if err := Send(context.Background(), ok.Client(), slackRequest(t, ok.URL)); err != nil {
		t.Fatalf("Send to a receiver that accepts: %v", err)
	}
	if gotType != "application/json" || !strings.Contains(gotBody, "the backup of uptime-kuma failed") {
		t.Errorf("receiver got %q with %q", gotBody, gotType)
	}
}

func TestSendFailures(t *testing.T) {
	long := strings.Repeat("x", 500)
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    []string // pieces the error must hold
		absent  []string // pieces it must not
	}{
		{
			name:    "500 with a body",
			handler: func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", 500) },
			want:    []string{"500", "boom"},
		},
		{
			name:    "body cut at 200 bytes",
			handler: func(w http.ResponseWriter, r *http.Request) { http.Error(w, long, 400) },
			want:    []string{"400", strings.Repeat("x", 200)},
			absent:  []string{strings.Repeat("x", 201)},
		},
		{
			name: "receiver echoes the path",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "Cannot POST "+r.URL.Path+" q="+r.URL.RawQuery, 404)
			},
			want: []string{"404", "Cannot POST …"},
		},
		{
			name: "a redirect is a failure, not followed",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "https://elsewhere.example.com/", http.StatusTemporaryRedirect)
			},
			want: []string{"307"},
		},
		{
			name:    "no body",
			handler: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(502) },
			want:    []string{"502"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()

			err := Send(context.Background(), srv.Client(), slackRequest(t, srv.URL))
			if err == nil {
				t.Fatal("Send succeeded, want an error")
			}
			msg := err.Error()
			for _, w := range tc.want {
				if !strings.Contains(msg, w) {
					t.Errorf("error %q lacks %q", msg, w)
				}
			}
			for _, a := range append(tc.absent, "SENTINEL_SECRET", "SENTINEL_QUERY") {
				if strings.Contains(msg, a) {
					t.Errorf("error %q holds %q", msg, a)
				}
			}
			if !strings.Contains(msg, strings.TrimPrefix(srv.URL, "http://")) {
				t.Errorf("error %q does not name the host", msg)
			}
		})
	}
}

// TestSendSlow checks that a receiver slower than the timeout fails the attempt
// with the host named and no path in the error.
func TestSendSlow(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	start := time.Now()
	err := send(context.Background(), srv.Client(), slackRequest(t, srv.URL), 50*time.Millisecond)
	if err == nil {
		t.Fatal("send succeeded, want a timeout")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("send took %v, want about the timeout", took)
	}
	for _, a := range []string{"SENTINEL_SECRET", "SENTINEL_QUERY"} {
		if strings.Contains(err.Error(), a) {
			t.Errorf("error %q holds %q", err, a)
		}
	}
	if !strings.Contains(err.Error(), strings.TrimPrefix(srv.URL, "http://")) {
		t.Errorf("error %q does not name the host", err)
	}
}

// TestSendUnreachable checks the error of a connection that fails outright.
func TestSendUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens there any more

	err := Send(context.Background(), nil, slackRequest(t, url))
	if err == nil {
		t.Fatal("Send succeeded, want a connection error")
	}
	for _, a := range []string{"SENTINEL_SECRET", "SENTINEL_QUERY"} {
		if strings.Contains(err.Error(), a) {
			t.Errorf("error %q holds %q", err, a)
		}
	}
}

func TestSendCanceled(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Send(ctx, srv.Client(), slackRequest(t, srv.URL)); err == nil {
		t.Error("Send with a canceled context succeeded")
	}
}
