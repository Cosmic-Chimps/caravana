package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/cosmic-chimps/caravana/internal/notify"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The sender (feature 018, research R3). The notifier and the deploy hook only
// queue rows in notification_deliveries; this goroutine sends them. A deploy or a
// check therefore never waits on a receiver (FR-009), and a message queued
// before a restart is sent after it.

// The shape of sending.
const (
	// dueLimit is how many due deliveries one pass looks at.
	dueLimit = 20
	// maxParallelSends is how many channels are sent to at once. A channel gets
	// one message at a time, so a slow receiver holds up only its own queue.
	maxParallelSends = 4
	// maxDeliveryAttempts is how many times a message is tried in all.
	maxDeliveryAttempts = 3
	// defaultSenderGap is how often the sender looks for due deliveries when
	// nothing wakes it: retries come due by the clock alone.
	defaultSenderGap = 5 * time.Second
)

// retryDelays is how long a message waits after its first, and after its second,
// failed attempt: 30 seconds, then 2 minutes. A third failure ends it.
var retryDelays = [maxDeliveryAttempts - 1]time.Duration{30 * time.Second, 2 * time.Minute}

// Why a queued message was not sent, as the console lists it.
const (
	reasonChannelRemoved = "channel removed"
	reasonChannelPaused  = "channel paused"
)

// deliveryQueue is the sender's bookkeeping, shared by its loop and its
// workers.
type deliveryQueue struct {
	mu    sync.Mutex
	busy  map[string]bool // channel IDs a worker is sending to, guarded by mu
	slots chan struct{}   // one token per worker, so at most maxParallelSends run
	wake  chan struct{}   // a token here makes the loop look for work at once
}

func newDeliveryQueue() *deliveryQueue {
	return &deliveryQueue{
		busy:  map[string]bool{},
		slots: make(chan struct{}, maxParallelSends),
		wake:  make(chan struct{}, 1),
	}
}

// poke wakes the sender loop, or does nothing when it is already awake or about
// to be: it never waits, so whoever queued a message goes straight on.
func (q *deliveryQueue) poke() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// runSender sends due deliveries until ctx ends: on every gap, when poked, and
// when a worker finishes (more may be waiting behind it).
//
// The workers are goroutines of their own, one per channel with something due,
// so the loop never waits for a receiver. Wait counts the loop, and the loop
// waits for its workers before it returns; they send under ctx, so they end soon
// after it does.
func (s *Service) runSender(ctx context.Context, gap time.Duration) {
	var workers sync.WaitGroup
	defer workers.Wait()
	t := time.NewTicker(gap)
	defer t.Stop()
	for {
		s.sendDue(ctx, &workers)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.queue.wake:
		}
	}
}

// sendDue is one pass of the loop: it starts a worker for each channel that has
// a delivery due and none being sent, up to maxParallelSends at once. A channel
// that has to wait is picked up by a later pass, which a finishing worker causes.
//
// The pass reads the due deliveries while holding the queue's lock, and a
// worker gives its channel back under the same lock after it has written its
// results. So a pass can never see a delivery as pending that a worker has
// already sent: either the worker is still busy and the channel is skipped, or
// it is done and its results are in the store.
func (s *Service) sendDue(ctx context.Context, workers *sync.WaitGroup) {
	q := s.queue
	q.mu.Lock()
	defer q.mu.Unlock()

	due, err := s.store.DueDeliveries(ctx, s.clock.Now(), dueLimit)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("read the queued notifications; trying again", "error", err)
		}
		return
	}
	var channels []string // in the order they first appear: the longest waiting first
	byChannel := map[string][]store.Delivery{}
	for _, d := range due {
		if _, ok := byChannel[d.ChannelID]; !ok {
			channels = append(channels, d.ChannelID)
		}
		byChannel[d.ChannelID] = append(byChannel[d.ChannelID], d)
	}

	for _, id := range channels {
		if q.busy[id] {
			continue
		}
		select {
		case q.slots <- struct{}{}:
		default:
			continue // all workers are busy; a worker finishing wakes the loop
		}
		q.busy[id] = true
		workers.Add(1)
		go func() {
			defer workers.Done()
			s.sendToChannel(ctx, id, byChannel[id])
			q.mu.Lock()
			delete(q.busy, id)
			q.mu.Unlock()
			<-q.slots
			q.poke() // more may be due behind this one
		}()
	}
}

// sendToChannel sends a channel's due deliveries one after the other, in the
// order they were queued. It reads the channel and its secrets once.
//
// A channel that was removed or paused since the messages were queued is not
// sent to: they are marked failed with the reason, and a resumed channel does
// not replay them (spec scenario 5).
func (s *Service) sendToChannel(ctx context.Context, channelID string, deliveries []store.Delivery) {
	row, err := s.store.GetNotificationChannel(ctx, channelID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.giveUp(ctx, channelID, deliveries, reasonChannelRemoved)
		return
	case err != nil:
		if ctx.Err() == nil {
			s.log.Warn("read a notification channel; its messages stay queued", "channel", channelID, "error", err)
		}
		return
	case row.Paused:
		s.giveUp(ctx, channelID, deliveries, reasonChannelPaused)
		return
	}

	// A secret that can't be read fails each attempt, like a receiver that is
	// down: the message is tried again after the same delays.
	ch, secretErr := s.notifyChannel(ctx, row)
	for _, d := range deliveries {
		if ctx.Err() != nil {
			return
		}
		s.attempt(ctx, row, ch, secretErr, d)
	}
}

// attempt sends one delivery and records what came of it: sent, a retry after
// the next delay, or failed once the attempts are used up.
func (s *Service) attempt(ctx context.Context, row store.NotificationChannel, ch notify.Channel, secretErr error, d store.Delivery) {
	sendErr := secretErr
	if sendErr == nil {
		sendErr = s.render(ctx, ch, d)
	}
	if sendErr != nil && ctx.Err() != nil {
		return // shutting down: the attempt didn't count, the message is still queued
	}

	attempts := d.Attempts + 1
	// The results are written whatever ctx is doing, so a message that was
	// delivered is never sent again because Caravana was stopping.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	now := s.clock.Now().UTC()

	var err error
	finished := true
	switch {
	case sendErr == nil:
		err = s.store.FinishDelivery(wctx, d.ID, store.DeliverySent, attempts, "", now)
	case attempts >= maxDeliveryAttempts:
		err = s.store.FinishDelivery(wctx, d.ID, store.DeliveryFailed, attempts, sendErr.Error(), now)
	default:
		err = s.store.RetryDelivery(wctx, d.ID, attempts, now.Add(retryDelays[attempts-1]), sendErr.Error())
		finished = false
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) { // not found: the channel was removed meanwhile
		s.log.Error("record a notification attempt", "channel", row.ID, "delivery", d.ID, "error", err)
		return
	}
	if sendErr != nil {
		// notify's errors name the host, never the address, so this is safe to log.
		s.log.Warn("notification not delivered", "channel", row.ID, "delivery", d.ID, "attempt", attempts, "error", sendErr)
	}
	if finished {
		s.pruneDeliveries(wctx, row.ID)
	}
}

// render turns a queued delivery into a request and sends it.
func (s *Service) render(ctx context.Context, ch notify.Channel, d store.Delivery) error {
	var ev notify.Event
	if err := json.Unmarshal([]byte(d.Payload), &ev); err != nil {
		return fmt.Errorf("read the queued message: %w", err)
	}
	req, err := notify.Render(ch, ev, d.ID, s.clock.Now())
	if err != nil {
		return err
	}
	return notify.Send(ctx, s.notifyClient, req)
}

// giveUp marks queued deliveries failed without sending them.
func (s *Service) giveUp(ctx context.Context, channelID string, deliveries []store.Delivery, reason string) {
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	now := s.clock.Now().UTC()
	for _, d := range deliveries {
		err := s.store.FinishDelivery(wctx, d.ID, store.DeliveryFailed, d.Attempts, reason, now)
		if err != nil && !errors.Is(err, store.ErrNotFound) { // a removed channel took its deliveries with it
			s.log.Error("record a skipped notification", "channel", channelID, "delivery", d.ID, "error", err)
		}
	}
	s.pruneDeliveries(wctx, channelID)
}

// pruneDeliveries keeps a channel's last recentDeliveries finished deliveries.
func (s *Service) pruneDeliveries(ctx context.Context, channelID string) {
	if err := s.store.PruneDeliveries(ctx, channelID, recentDeliveries); err != nil {
		s.log.Warn("prune notification deliveries", "channel", channelID, "error", err)
	}
}
