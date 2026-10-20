package mcpserver

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestErrorMapping: what the agent reads for each way the API can refuse.
func TestErrorMapping(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"refusal with a field", 422, `{"error":{"code":"invalid_field","message":"no such tag","field":"services[0].image"}}`,
			"Caravana refused: invalid_field: no such tag (field services[0].image) (HTTP 422)"},
		{"refusal without a field", 409, `{"error":{"code":"app_stopped","message":"The app is stopped: start it first."}}`,
			"Caravana refused: app_stopped: The app is stopped: start it first. (HTTP 409)"},
		{"unauthorized", 401, `{"error":{"code":"unauthorized","message":"authentication required"}}`,
			"Caravana refused the token (401 unauthorized): check CARAVANA_TOKEN, or make a new token in Settings › API tokens"},
		{"unauthorized without a body", 401, ``,
			"Caravana refused the token (401 unauthorized): check CARAVANA_TOKEN, or make a new token in Settings › API tokens"},
		{"token scope", 403, `{"error":{"code":"token_scope","message":"this token can only deploy app web of project shop"}}`,
			"Caravana refused: token_scope: this token can only deploy app web of project shop (HTTP 403)"},
		{"no such app", 404, `{"error":{"code":"not_found","message":"no app web"}}`,
			"Caravana refused: not_found: no app web (HTTP 404)"},
		{"route an older Caravana lacks, no JSON", 404, ``,
			"this Caravana doesn't offer getApp; it is older than this binary"},
		{"route an older Caravana lacks, its own JSON", 404, `{"error":{"code":"not_found","message":"no such endpoint"}}`,
			"this Caravana doesn't offer getApp; it is older than this binary"},
		{"proxy page", 502, `<html>Bad gateway</html>`,
			"Caravana answered HTTP 502 to getApp without an error message"},
		{"server error with a JSON error", 500, `{"error":{"code":"internal","message":"something broke"}}`,
			"Caravana refused: internal: something broke (HTTP 500)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeAPI(t)
			f.reply("GET /v1/apps/web", tt.status, tt.body)
			cs := f.session(t, false, nil, fastTuning, nil)

			if got := mustFail(t, cs, "get_app", map[string]any{"app": "web"}); got != tt.want {
				t.Errorf("text = %q\nwant   %q", got, tt.want)
			}
		})
	}
}

// TestRedirectIsNotFollowed: a 3xx is an error naming the status, and the
// address it points to is never called with the token.
func TestRedirectIsNotFollowed(t *testing.T) {
	var elsewhereHits atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhereHits.Add(1)
	}))
	t.Cleanup(elsewhere.Close)

	for _, status := range []int{301, 302, 307, 308} {
		f := newFakeAPI(t)
		f.on("GET /v1/apps/web", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, elsewhere.URL+"/steal", status)
		})
		cs := f.session(t, false, nil, fastTuning, nil)

		got := mustFail(t, cs, "get_app", map[string]any{"app": "web"})
		if !strings.Contains(got, fmt.Sprintf("redirect (HTTP %d)", status)) {
			t.Errorf("status %d: text = %q", status, got)
		}
		if !strings.Contains(got, "not followed") {
			t.Errorf("status %d: text doesn't say the redirect was not followed: %q", status, got)
		}
	}
	if n := elsewhereHits.Load(); n != 0 {
		t.Errorf("the redirect target was called %d times", n)
	}
}

// TestUnreachable: a server that isn't there is said plainly, with the address
// and never the token.
func TestUnreachable(t *testing.T) {
	f := newFakeAPI(t)
	cs := f.session(t, false, nil, fastTuning, nil)
	f.srv.Close()

	got := mustFail(t, cs, "list_apps", nil)
	if !strings.HasPrefix(got, "couldn't reach Caravana at "+f.srv.URL+": ") {
		t.Errorf("text = %q", got)
	}
	if strings.Contains(got, sentinelToken) {
		t.Errorf("text holds the token: %q", got)
	}
}

// TestCallTimeout: a call that gets no answer in time is an error, not a hang.
func TestCallTimeout(t *testing.T) {
	f := newFakeAPI(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	f.on("GET /v1/apps", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	cs := f.session(t, false, nil, tuning{poll: time.Millisecond, waitUnit: time.Millisecond, callTimeout: 50 * time.Millisecond}, nil)

	got := mustFail(t, cs, "list_apps", nil)
	if !strings.HasPrefix(got, "couldn't reach Caravana at ") || !strings.Contains(got, "no answer within 50ms") {
		t.Errorf("text = %q", got)
	}
}

// TestAnswerWithoutABody: a 204 reads as a confirmation, not as an empty tool
// result.
func TestAnswerWithoutABody(t *testing.T) {
	f := newFakeAPI(t)
	f.on("POST /v1/apps/web/stop", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	cs := f.session(t, true, nil, fastTuning, nil)

	if got := mustCall(t, cs, "stop_app", map[string]any{"app": "web"}); got != "done (HTTP 204, no body)" {
		t.Errorf("text = %q", got)
	}
}
