package ui

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/httpx"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// fakeHub stands in for Service.Subscribe: tests publish notices by hand.
type fakeHub struct {
	mu          sync.Mutex
	subs        []chan service.Change
	unsubscribe int
}

func (h *fakeHub) Subscribe(string) (<-chan service.Change, func()) {
	ch := make(chan service.Change, 64)
	h.mu.Lock()
	h.subs = append(h.subs, ch)
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.unsubscribe++
	}
}

func (h *fakeHub) publish(what ...service.ChangeKind) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, w := range what {
		for _, ch := range h.subs {
			ch <- service.Change{What: w}
		}
	}
}

func (h *fakeHub) subscribed() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

func (h *fakeHub) unsubscribed() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.unsubscribe
}

// liveEnv is the real sign-in and logout routes plus a test-only /test-live
// route, since no console page streams yet.
type liveEnv struct {
	*uiEnv
	hub    *fakeHub
	srv    *httptest.Server
	server *server
}

// newLiveEnv serves /test-live with the kinds "apps" and "deployments". start
// is the route's optional Start hook. Timings are short; keepalive is off
// (an hour) unless the test sets it.
func newLiveEnv(t *testing.T, settings liveSettings, start func(context.Context, *httpx.EventWriter)) *liveEnv {
	t.Helper()
	e := newUIEnv(t, true, "")
	hub := &fakeHub{}
	s, err := newServer(Deps{Auth: e.Auth, Log: slog.New(slog.DiscardHandler), Now: e.Clock.Now})
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	settings.subscribe = hub.Subscribe
	if settings.coalesce == 0 {
		settings.coalesce = 200 * time.Millisecond
	}
	if settings.keepalive == 0 {
		settings.keepalive = time.Hour
	}
	if settings.recheck == 0 {
		settings.recheck = 20 * time.Millisecond
	}
	s.live = settings

	mux := http.NewServeMux()
	mux.HandleFunc("GET /test-live", func(w http.ResponseWriter, r *http.Request) {
		s.stream(w, r, liveRoute{Kinds: []service.ChangeKind{service.ChangeApps, service.ChangeDeployments}, Start: start})
	})
	mux.Handle("/", s.routes())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &liveEnv{uiEnv: e, hub: hub, srv: srv, server: s}
}

// session signs in directly and returns the session.
func (e *liveEnv) session(t *testing.T) service.Session {
	t.Helper()
	sess, err := e.Auth.SignIn(context.Background(), testEmail, testPassword, "127.0.0.1")
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	return sess
}

// liveStream is an open /test-live response read as raw event-stream lines.
type liveStream struct {
	t      *testing.T
	resp   *http.Response
	lines  chan string // "event: name" lines and ": comment" lines, as sent
	cancel context.CancelFunc
}

// open connects to /test-live with the session cookie ("" for none).
func (e *liveEnv) open(t *testing.T, cookie string) *liveStream {
	t.Helper()
	return openStream(t, e.srv.URL, "/test-live", cookie)
}

// openStream connects to a live route of the server at base with the session
// cookie ("" for none) and reads the response as raw event-stream lines.
func openStream(t *testing.T, base, path, cookie string) *liveStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: "caravana_session", Value: cookie})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("GET %s: %v", path, err)
	}
	ls := &liveStream{t: t, resp: resp, lines: make(chan string, 256), cancel: cancel}
	go func() {
		defer close(ls.lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if line := sc.Text(); strings.HasPrefix(line, "event: ") || strings.HasPrefix(line, ":") {
				ls.lines <- line
			}
		}
	}()
	t.Cleanup(func() { ls.close() })
	return ls
}

func (ls *liveStream) close() {
	ls.cancel()
	ls.resp.Body.Close()
}

// next returns the next event's name, skipping comments. "" means the stream
// closed; a stream that stays silent too long fails the test.
func (ls *liveStream) next() string {
	ls.t.Helper()
	for {
		select {
		case line, ok := <-ls.lines:
			if !ok {
				return ""
			}
			if name, found := strings.CutPrefix(line, "event: "); found {
				return name
			}
		case <-time.After(3 * time.Second):
			ls.t.Fatal("no event within 3s")
		}
	}
}

func (ls *liveStream) expect(want string) {
	ls.t.Helper()
	if got := ls.next(); got != want {
		ls.t.Fatalf("next event = %q, want %q", got, want)
	}
}

func (ls *liveStream) expectClosed() {
	ls.t.Helper()
	if got := ls.next(); got != "" {
		ls.t.Fatalf("got event %q, want the stream closed", got)
	}
}

func TestLiveSyncThenNotices(t *testing.T) {
	e := newLiveEnv(t, liveSettings{}, nil)
	ls := e.open(t, e.session(t).Value)

	if ls.resp.StatusCode != http.StatusOK || ls.resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("response = %d %q, want 200 text/event-stream", ls.resp.StatusCode, ls.resp.Header.Get("Content-Type"))
	}
	ls.expect("sync") // first, on every connect

	// A kind outside the route's filter is not forwarded; the next one is.
	e.hub.publish(service.ChangeCopies, service.ChangeDeployments)
	ls.expect("deployments")
	e.hub.publish(service.ChangeApps)
	ls.expect("apps")
}

func TestLiveCoalescesQuickNotices(t *testing.T) {
	e := newLiveEnv(t, liveSettings{}, nil)
	ls := e.open(t, e.session(t).Value)
	ls.expect("sync")

	for range 10 {
		e.hub.publish(service.ChangeDeployments)
	}
	// Wait out the coalesce interval twice over, then send a marker of
	// another kind: everything before it is what the ten notices became.
	time.Sleep(3 * e.server.live.coalesce)
	e.hub.publish(service.ChangeApps)
	count := 0
	for {
		name := ls.next()
		if name == "apps" {
			break
		}
		if name != "deployments" {
			t.Fatalf("unexpected event %q", name)
		}
		count++
	}
	if count < 1 || count > 2 {
		t.Errorf("ten quick notices became %d events, want 1 or 2", count)
	}
}

func TestLiveEndsWhenSignedOut(t *testing.T) {
	e := newLiveEnv(t, liveSettings{}, nil)
	sess := e.session(t)
	ls := e.open(t, sess.Value)
	ls.expect("sync")

	if err := e.Auth.SignOut(context.Background(), sess.Value); err != nil {
		t.Fatal(err)
	}
	ls.expect("ended") // the next re-check notices
	ls.expectClosed()
	waitFor(t, "unsubscribe", func() bool { return e.hub.unsubscribed() == 1 })
}

func TestLiveWithoutSession(t *testing.T) {
	e := newLiveEnv(t, liveSettings{}, nil)
	for name, cookie := range map[string]string{"no cookie": "", "unknown session": "not-a-session-value"} {
		t.Run(name, func(t *testing.T) {
			ls := e.open(t, cookie)
			if ls.resp.StatusCode != http.StatusOK {
				t.Errorf("status = %d, want 200 (the SSE extension retries errors forever)", ls.resp.StatusCode)
			}
			ls.expect("ended")
			ls.expectClosed()
		})
	}
	if n := e.hub.subscribed(); n != 0 {
		t.Errorf("%d subscriptions without a session, want none", n)
	}
}

func TestLiveDoesNotRenewTheSession(t *testing.T) {
	e := newLiveEnv(t, liveSettings{keepalive: 20 * time.Millisecond}, nil)
	sess := e.session(t)
	sum := sha256.Sum256([]byte(sess.Value))
	id := hex.EncodeToString(sum[:])
	before, err := e.Store.GetSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}

	ls := e.open(t, sess.Value)
	ls.expect("sync")
	// Ten minutes pass, well beyond the once-a-minute touch of CheckSession,
	// and several re-checks run while the stream is open.
	e.Clock.Advance(10 * time.Minute)
	e.hub.publish(service.ChangeApps)
	ls.expect("apps")
	time.Sleep(100 * time.Millisecond)

	after, err := e.Store.GetSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !after.LastSeenAt.Equal(before.LastSeenAt) {
		t.Errorf("LastSeenAt moved from %v to %v while streaming", before.LastSeenAt, after.LastSeenAt)
	}
}

func TestLiveKeepalive(t *testing.T) {
	e := newLiveEnv(t, liveSettings{keepalive: 20 * time.Millisecond}, nil)
	ls := e.open(t, e.session(t).Value)
	deadline := time.After(3 * time.Second)
	for {
		select {
		case line := <-ls.lines:
			if strings.HasPrefix(line, ": ") {
				return
			}
		case <-deadline:
			t.Fatal("no keepalive comment within 3s")
		}
	}
}

func TestLiveUnsubscribesAndStopsStartHook(t *testing.T) {
	started := make(chan struct{})
	var mu sync.Mutex
	stopped := false
	e := newLiveEnv(t, liveSettings{}, func(ctx context.Context, ev *httpx.EventWriter) {
		close(started)
		_ = ev.Event("extra", []byte("a\nb"))
		<-ctx.Done()
		mu.Lock()
		stopped = true
		mu.Unlock()
	})
	ls := e.open(t, e.session(t).Value)
	ls.expect("sync")
	<-started
	ls.expect("extra") // the hook writes through the same stream

	ls.close()
	waitFor(t, "unsubscribe", func() bool { return e.hub.unsubscribed() == 1 })
	waitFor(t, "the start hook's context to end", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return stopped
	})
}

func TestCoalescer(t *testing.T) {
	t0 := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	gap := 500 * time.Millisecond
	c := newCoalescer(gap)

	if !c.offer("a", t0) {
		t.Fatal("first notice of a kind was held back")
	}
	if c.offer("a", t0.Add(100*time.Millisecond)) || c.offer("a", t0.Add(200*time.Millisecond)) {
		t.Fatal("notices inside the gap were not held back")
	}
	if !c.offer("b", t0.Add(200*time.Millisecond)) {
		t.Error("another kind was held back by a's gap")
	}
	if wait, ok := c.nextWait(t0.Add(200 * time.Millisecond)); !ok || wait != 300*time.Millisecond {
		t.Errorf("nextWait = %v, %v, want 300ms, true", wait, ok)
	}
	if got := c.due(t0.Add(400 * time.Millisecond)); len(got) != 0 {
		t.Errorf("due too early: %v", got)
	}
	if got := c.due(t0.Add(500 * time.Millisecond)); len(got) != 1 || got[0] != "a" {
		t.Errorf("due = %v, want [a]", got)
	}
	if _, ok := c.nextWait(t0.Add(500 * time.Millisecond)); ok {
		t.Error("nextWait reports pending work after everything was sent")
	}
	// The trailing event restarts the gap.
	if c.offer("a", t0.Add(600*time.Millisecond)) {
		t.Error("a notice right after the trailing event was not held back")
	}
}

// waitFor polls cond for up to 3 seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
