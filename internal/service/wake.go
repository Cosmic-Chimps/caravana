package service

import "sync"

// The keys of the wake hub: one per thing a watcher waits for. A rollout has
// its own key per app.
const (
	copiesKey  = "copies"  // some copy changed (the Logs followers relist)
	runsKey    = "runs"    // a schedule run's job or copy changed
	backupsKey = "backups" // a backup or restore job or copy changed
)

func rolloutKey(appID string) string { return "rollout/" + appID }

// wakeHub lets any number of goroutines wait for a key to be woken, without
// one goroutine's wait using up the wake of another. The zero value is ready to
// use.
//
// The pattern is a closed channel: channel(key) hands out the key's current
// channel, wake(key) closes it and forgets it, so every goroutine that holds it
// sees the wake once, and the next channel(key) starts a fresh one. A waiter
// takes its channel BEFORE it reads what it wants to know about, so a wake that
// comes during the read isn't lost. (C# comparison: a TaskCompletionSource that
// is replaced after each SetResult.)
type wakeHub struct {
	mu    sync.Mutex
	chans map[string]chan struct{}
}

// channel returns the key's current channel: it is closed when the key is woken.
func (h *wakeHub) channel(key string) <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.chans == nil {
		h.chans = map[string]chan struct{}{}
	}
	ch, ok := h.chans[key]
	if !ok {
		ch = make(chan struct{})
		h.chans[key] = ch
	}
	return ch
}

// wake releases everyone waiting on key. A key nobody waits on costs nothing.
func (h *wakeHub) wake(key string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ch, ok := h.chans[key]; ok {
		close(ch)
		delete(h.chans, key)
	}
}

// wakeAll wakes every key that has a waiter.
func (h *wakeHub) wakeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for key, ch := range h.chans {
		close(ch)
		delete(h.chans, key)
	}
}
