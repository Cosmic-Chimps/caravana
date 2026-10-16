package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/notify"
	"github.com/cosmic-chimps/caravana/internal/store"
)

var senderT0 = time.Date(2026, 10, 17, 3, 0, 0, 0, time.UTC)

// hit is one request a recorder received.
type hit struct {
	header http.Header
	body   string
}

// recorder is an httptest receiver that records what it is sent. answer says
// what to do with the n-th request (1 is the first): the status to send, and
// whether to hold the request until the test lets go of it.
type recorder struct {
	*httptest.Server
	mu      sync.Mutex
	hits    []hit
	active  int // requests being served now
	peak    int // the most that ever were at once
	answer  func(n int) int
	hold    chan struct{} // when non-nil, requests wait for it to close
	started chan struct{} // receives a token as each request begins
}

func newRecorder(t *testing.T, answer func(n int) int) *recorder {
	t.Helper()
	r := &recorder{answer: answer, started: make(chan struct{}, 100)}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.hits = append(r.hits, hit{header: req.Header.Clone(), body: string(body)})
		n := len(r.hits)
		r.active++
		r.peak = max(r.peak, r.active)
		hold := r.hold
		r.mu.Unlock()
		r.started <- struct{}{}
		defer func() {
			r.mu.Lock()
			r.active--
			r.mu.Unlock()
		}()

		if hold != nil {
			select {
			case <-hold:
			case <-req.Context().Done():
				return
			}
		}
		status := http.StatusOK
		if r.answer != nil {
			status = r.answer(n)
		}
		w.WriteHeader(status)
		if status >= 400 {
			_, _ = io.WriteString(w, "nope")
		}
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.hits)
}

func (r *recorder) first() hit {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hits[0]
}

// always answers every request with status.
func always(status int) func(int) int { return func(int) int { return status } }

// senderEnv is an env whose own sender is parked, with a second, unstarted
// service the test drives pass by pass under a fake clock.
type senderEnv struct {
	*env
	svc2  *Service // named so it doesn't hide env.svc
	clock *fakeClock
}

func newSenderEnv(t *testing.T) *senderEnv {
	t.Helper()
	e := &senderEnv{env: newEnv(t), clock: &fakeClock{t: senderT0}}
	parkSender(e.env.svc)
	e.svc2 = New(e.store, e.env.svc.deployer, e.secrets, discardLogger())
	e.svc2.clock = e.clock
	return e
}

// channel adds an ntfy channel that posts to url.
func (e *senderEnv) channel(t *testing.T, name, url string) store.NotificationChannel {
	t.Helper()
	in := channelInput(notify.KindNtfy, url)
	in.Name = name
	got, err := e.env.svc.CreateNotificationChannel(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return got.NotificationChannel.NotificationChannel
}

// enqueue queues a deploy.failed message for the channel, due at the clock.
func (e *senderEnv) enqueue(t *testing.T, ch store.NotificationChannel, title string) store.Delivery {
	t.Helper()
	ev := notify.Event{Type: notify.EventDeployFailed, AppID: "hollo", Title: title, Text: "text of " + title,
		Detail: "reason", Deployment: "dep_1", Outcome: "failed", Trigger: "manual", At: senderT0}
	d, err := newDelivery(ch.ID, ev, e.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.EnqueueDelivery(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	return d
}

// pass runs one pass of the sender and waits for its workers.
func (e *senderEnv) pass(t *testing.T) {
	t.Helper()
	var workers sync.WaitGroup
	e.svc2.sendDue(context.Background(), &workers)
	workers.Wait()
}

func (e *senderEnv) delivery(t *testing.T, ch store.NotificationChannel, id string) store.Delivery {
	t.Helper()
	all, err := e.store.ListDeliveries(context.Background(), ch.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range all {
		if d.ID == id {
			return d
		}
	}
	t.Fatalf("delivery %s is not in the store", id)
	return store.Delivery{}
}

// A receiver that accepts the message ends the delivery as sent, after one
// attempt, and gets the message in its channel's own format.
func TestSenderSends(t *testing.T) {
	e := newSenderEnv(t)
	rec := newRecorder(t, nil)
	ch := e.channel(t, "phone", rec.URL+"/topic")
	d := e.enqueue(t, ch, "hollo: deploy failed")

	e.pass(t)
	got := e.delivery(t, ch, d.ID)
	if got.Status != store.DeliverySent || got.Attempts != 1 || got.Error != "" || got.FinishedAt == nil || !got.FinishedAt.Equal(senderT0) {
		t.Errorf("delivery = %+v, want sent after 1 attempt at the clock's time", got)
	}
	if rec.count() != 1 {
		t.Fatalf("receiver got %d requests, want 1", rec.count())
	}
	if h := rec.first(); h.header.Get("Title") != "hollo: deploy failed" || !strings.Contains(h.body, "Detail: reason") {
		t.Errorf("ntfy request = %v %q", h.header, h.body)
	}

	// Nothing left: another pass sends nothing more.
	e.pass(t)
	if rec.count() != 1 {
		t.Errorf("a second pass sent again: %d requests", rec.count())
	}
}

// A webhook gets its signature, made with the channel's own secret.
func TestSenderSignsWebhooks(t *testing.T) {
	e := newSenderEnv(t)
	rec := newRecorder(t, nil)
	created, err := e.env.svc.CreateNotificationChannel(context.Background(), channelInput(notify.KindWebhook, rec.URL+"/hook"))
	if err != nil {
		t.Fatal(err)
	}
	ch := created.NotificationChannel.NotificationChannel
	d := e.enqueue(t, ch, "hollo: deploy failed")

	e.pass(t)
	h := rec.first()
	if err := notify.Verify(created.SigningSecret, h.header.Get(notify.SignatureHeader), []byte(h.body), senderT0, 5*time.Minute); err != nil {
		t.Errorf("signature: %v", err)
	}
	if h.header.Get("Caravana-Delivery") != d.ID || h.header.Get("Caravana-Event") != notify.EventDeployFailed || !strings.Contains(h.body, d.ID) {
		t.Errorf("headers = %v body = %s", h.header, h.body)
	}
}

// A message that fails is tried again after 30 seconds and after 2 minutes, and
// then given up on, keeping what the receiver said.
func TestSenderRetries(t *testing.T) {
	tests := []struct {
		name         string
		answer       func(n int) int
		wantStatus   string
		wantAttempts int
	}{
		{"500 twice then ok", func(n int) int {
			if n <= 2 {
				return http.StatusInternalServerError
			}
			return http.StatusOK
		}, store.DeliverySent, 3},
		{"500 once then ok", func(n int) int {
			if n == 1 {
				return http.StatusBadGateway
			}
			return http.StatusOK
		}, store.DeliverySent, 2},
		{"always 500", always(http.StatusInternalServerError), store.DeliveryFailed, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newSenderEnv(t)
			rec := newRecorder(t, tc.answer)
			ch := e.channel(t, "phone", rec.URL+"/topic")
			d := e.enqueue(t, ch, "hollo: deploy failed")

			// First attempt, now.
			e.pass(t)
			got := e.delivery(t, ch, d.ID)
			if tc.answer(1) != http.StatusOK {
				if got.Status != store.DeliveryPending || got.Attempts != 1 || !got.NextAt.Equal(senderT0.Add(30*time.Second)) ||
					!strings.Contains(got.Error, "500") && !strings.Contains(got.Error, "502") {
					t.Fatalf("after the first failure: %+v, want pending, 1 attempt, next in 30 s, with the error", got)
				}
				// Not yet due: nothing is sent a second early.
				e.clock.Set(senderT0.Add(29 * time.Second))
				e.pass(t)
				if rec.count() != 1 {
					t.Fatalf("retried %d times before the delay", rec.count())
				}
				e.clock.Set(senderT0.Add(30 * time.Second))
				e.pass(t)
			}
			if tc.answer(2) != http.StatusOK && tc.wantAttempts == 3 {
				got = e.delivery(t, ch, d.ID)
				if got.Status != store.DeliveryPending || got.Attempts != 2 || !got.NextAt.Equal(senderT0.Add(30*time.Second+2*time.Minute)) {
					t.Fatalf("after the second failure: %+v, want pending, 2 attempts, next in 2 min", got)
				}
				e.clock.Set(senderT0.Add(30*time.Second + 2*time.Minute))
				e.pass(t)
			}

			got = e.delivery(t, ch, d.ID)
			if got.Status != tc.wantStatus || got.Attempts != tc.wantAttempts {
				t.Fatalf("delivery = %+v, want %s after %d attempts", got, tc.wantStatus, tc.wantAttempts)
			}
			if tc.wantStatus == store.DeliveryFailed && (!strings.Contains(got.Error, "500") || !strings.Contains(got.Error, "nope") || got.FinishedAt == nil) {
				t.Errorf("a failed delivery keeps the receiver's answer: %+v", got)
			}
			if tc.wantStatus == store.DeliverySent && got.Error != "" {
				t.Errorf("a delivery that got through keeps an error: %q", got.Error)
			}
			if rec.count() != tc.wantAttempts {
				t.Errorf("receiver got %d requests, want %d", rec.count(), tc.wantAttempts)
			}
			// Done: later passes leave it alone.
			e.clock.Set(senderT0.Add(time.Hour))
			e.pass(t)
			if rec.count() != tc.wantAttempts {
				t.Errorf("a finished delivery was sent again")
			}
		})
	}
}

// A paused channel is not sent to, and resuming it does not replay what was
// missed (spec scenario 5).
func TestSenderPausedChannel(t *testing.T) {
	ctx := context.Background()
	e := newSenderEnv(t)
	rec := newRecorder(t, nil)
	ch := e.channel(t, "phone", rec.URL+"/topic")
	d := e.enqueue(t, ch, "hollo: deploy failed")

	if _, err := e.env.svc.UpdateNotificationChannel(ctx, ch.ID, NotificationChannelPatch{Paused: new(true)}); err != nil {
		t.Fatal(err)
	}
	e.pass(t)
	got := e.delivery(t, ch, d.ID)
	if got.Status != store.DeliveryFailed || got.Error != "channel paused" || rec.count() != 0 {
		t.Fatalf("delivery = %+v with %d requests, want failed (channel paused) and none sent", got, rec.count())
	}

	if _, err := e.env.svc.UpdateNotificationChannel(ctx, ch.ID, NotificationChannelPatch{Paused: new(false)}); err != nil {
		t.Fatal(err)
	}
	e.pass(t)
	if rec.count() != 0 {
		t.Errorf("resuming replayed %d messages", rec.count())
	}
}

// A channel removed while its messages were being worked on is not sent to, and
// nothing breaks: its deliveries went with it.
func TestSenderRemovedChannel(t *testing.T) {
	ctx := context.Background()
	e := newSenderEnv(t)
	rec := newRecorder(t, nil)
	ch := e.channel(t, "phone", rec.URL+"/topic")
	d := e.enqueue(t, ch, "hollo: deploy failed")

	due, err := e.store.DueDeliveries(ctx, senderT0, 10) // what a pass saw before the removal
	if err != nil || len(due) != 1 {
		t.Fatalf("DueDeliveries = %v, %v", due, err)
	}
	if err := e.env.svc.DeleteNotificationChannel(ctx, ch.ID); err != nil {
		t.Fatal(err)
	}
	e.svc2.sendToChannel(ctx, ch.ID, due)
	e.pass(t)
	if rec.count() != 0 {
		t.Errorf("a removed channel was sent %d messages", rec.count())
	}
	if _, err := e.store.ListDeliveries(ctx, ch.ID, 10); err != nil {
		t.Errorf("ListDeliveries: %v", err)
	}
	_ = d
}

// Each channel keeps its last 20 finished deliveries.
func TestSenderPrunes(t *testing.T) {
	ctx := context.Background()
	e := newSenderEnv(t)
	rec := newRecorder(t, nil)
	ch := e.channel(t, "phone", rec.URL+"/topic")
	for i := 0; i < 25; i++ {
		e.clock.Set(senderT0.Add(time.Duration(i) * time.Second))
		e.enqueue(t, ch, "message")
		e.pass(t)
	}
	got, err := e.store.ListDeliveries(ctx, ch.ID, 100)
	if err != nil || len(got) != recentDeliveries {
		t.Errorf("%d deliveries kept (%v), want %d", len(got), err, recentDeliveries)
	}
	if rec.count() != 25 {
		t.Errorf("receiver got %d, want 25", rec.count())
	}
}

// A receiver that never answers holds up its own channel only; and a channel
// is sent to one message at a time, in the order they were queued.
func TestSenderSlowReceiverDoesNotDelayOthers(t *testing.T) {
	ctx := context.Background()
	e := newSenderEnv(t)
	slow := newRecorder(t, nil)
	slow.hold = make(chan struct{})
	fast := newRecorder(t, nil)
	slowCh := e.channel(t, "slow", slow.URL+"/t")
	fastCh := e.channel(t, "fast", fast.URL+"/t")
	s1 := e.enqueue(t, slowCh, "first")
	e.clock.Set(senderT0.Add(time.Millisecond)) // a later due time, so the order is the queue order
	s2 := e.enqueue(t, slowCh, "second")
	e.clock.Set(senderT0.Add(time.Second))
	f1 := e.enqueue(t, fastCh, "fast one")

	var workers sync.WaitGroup
	e.svc2.sendDue(ctx, &workers)
	<-slow.started // the slow receiver has its request and is sitting on it
	waitFor(t, "the fast channel to be sent while the slow one is stuck", func() bool {
		return e.delivery(t, fastCh, f1.ID).Status == store.DeliverySent
	})
	if got := e.delivery(t, slowCh, s1.ID); got.Status != store.DeliveryPending {
		t.Errorf("the slow delivery is %s while its receiver holds it", got.Status)
	}

	// While a worker has the slow channel, a pass leaves it alone: its second
	// message waits for the first.
	e.svc2.sendDue(ctx, &workers)
	time.Sleep(20 * time.Millisecond)
	if slow.count() != 1 {
		t.Errorf("slow receiver got %d requests while holding the first, want 1", slow.count())
	}

	close(slow.hold)
	workers.Wait()
	for _, id := range []string{s1.ID, s2.ID} {
		if got := e.delivery(t, slowCh, id); got.Status != store.DeliverySent {
			t.Errorf("slow delivery %s = %s after release", id, got.Status)
		}
	}
	if slow.peak != 1 {
		t.Errorf("slow receiver served %d requests at once, want one at a time", slow.peak)
	}
	if first, second := slow.hits[0].body, slow.hits[1].body; !strings.Contains(first, "first") || !strings.Contains(second, "second") {
		t.Errorf("order: %q then %q", first, second)
	}
}

// At most four channels are sent to at once; the rest wait for a pass after a
// worker finishes.
func TestSenderFourAtOnce(t *testing.T) {
	ctx := context.Background()
	e := newSenderEnv(t)
	var recs []*recorder
	var chans []store.NotificationChannel
	hold := make(chan struct{})
	for i := 0; i < 6; i++ {
		r := newRecorder(t, nil)
		r.hold = hold
		recs = append(recs, r)
		ch := e.channel(t, "c"+string(rune('a'+i)), r.URL+"/t")
		chans = append(chans, ch)
		e.enqueue(t, ch, "message")
	}

	var workers sync.WaitGroup
	e.svc2.sendDue(ctx, &workers)
	waitFor(t, "four receivers to be asked", func() bool {
		n := 0
		for _, r := range recs {
			n += r.count()
		}
		return n == 4
	})
	time.Sleep(20 * time.Millisecond) // a fifth would have shown by now
	n := 0
	for _, r := range recs {
		n += r.count()
	}
	if n != 4 {
		t.Fatalf("%d receivers asked at once, want 4", n)
	}

	close(hold)
	workers.Wait()
	e.pass(t) // a worker finishing wakes the loop; here the test is the loop
	for i, ch := range chans {
		all, _ := e.store.ListDeliveries(ctx, ch.ID, 10)
		if len(all) != 1 || all[0].Status != store.DeliverySent {
			t.Errorf("channel %d: %+v, want its message sent", i, all)
		}
	}
}

// What was queued before a restart is sent by the next Service on the same
// store.
func TestSenderSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	e := newSenderEnv(t)
	rec := newRecorder(t, nil)
	ch := e.channel(t, "phone", rec.URL+"/topic")
	d := e.enqueue(t, ch, "hollo: deploy failed") // the first Service's sender is parked: it never sends

	second := New(e.store, e.env.svc.deployer, e.secrets, discardLogger())
	second.clock = e.clock
	second.senderGap = time.Hour // only the first pass at start can send it
	runCtx, cancel := context.WithCancel(ctx)
	if err := second.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); second.Wait() })

	waitFor(t, "the restarted service to send the queued message", func() bool {
		return e.delivery(t, ch, d.ID).Status == store.DeliverySent
	})
	if rec.count() != 1 {
		t.Errorf("receiver got %d requests, want 1", rec.count())
	}
}

// Queuing a message and poking the queue sends it at once, not at the next
// look.
func TestSenderWakes(t *testing.T) {
	ctx := context.Background()
	e := newSenderEnv(t)
	rec := newRecorder(t, nil)
	ch := e.channel(t, "phone", rec.URL+"/topic")

	svc := New(e.store, e.env.svc.deployer, e.secrets, discardLogger())
	svc.clock = e.clock
	svc.senderGap = time.Hour
	runCtx, cancel := context.WithCancel(ctx)
	if err := svc.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); svc.Wait() })

	d := e.enqueue(t, ch, "hollo: deploy failed")
	svc.queue.poke()
	waitFor(t, "the poked sender to send", func() bool { return e.delivery(t, ch, d.ID).Status == store.DeliverySent })
}

// Shutting down while a receiver sits on a request returns from Wait, and the
// message stays queued, its attempt not counted.
func TestSenderStopsWithContext(t *testing.T) {
	ctx := context.Background()
	e := newSenderEnv(t)
	rec := newRecorder(t, nil)
	rec.hold = make(chan struct{}) // never released
	ch := e.channel(t, "phone", rec.URL+"/topic")
	d := e.enqueue(t, ch, "hollo: deploy failed")

	svc := New(e.store, e.env.svc.deployer, e.secrets, discardLogger())
	svc.clock = e.clock
	runCtx, cancel := context.WithCancel(ctx)
	if err := svc.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	<-rec.started
	cancel()

	done := make(chan struct{})
	go func() { svc.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return while a send was in flight")
	}
	if got := e.delivery(t, ch, d.ID); got.Status != store.DeliveryPending || got.Attempts != 0 {
		t.Errorf("delivery = %+v, want still pending with no attempt counted", got)
	}
}
