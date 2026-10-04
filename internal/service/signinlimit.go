package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/cosmic-chimps/caravana/internal/store"
)

// The sign-in limit (research R8): per source address, 5 failures within 15
// minutes lock further sign-ins from it for 15 minutes.
const (
	signinMaxFailures = 5
	signinWindow      = 15 * time.Minute
	signinLockout     = 15 * time.Minute
	signinPruneAfter  = 24 * time.Hour
)

// limiter serializes the limit's bookkeeping and counts attempts that are
// still checking a password, which the store doesn't know about yet.
type limiter struct {
	mu       sync.Mutex
	inflight map[string]int
}

// inflightWait is what a sign-in refused because others from the same address
// are still being checked is told: those finish within a second, and then
// either the address is locked or it isn't.
const inflightWait = time.Minute

// beginAttempt is called before a password is checked. It refuses with a
// *SigninLimitError when the address is locked, or when its recorded failures
// plus the attempts already under way would pass the limit: a burst of
// simultaneous guesses can't test more than 5 passwords just because none has
// been recorded yet. Otherwise it reserves a try. recordFailure and
// clearFailures give the reservation back in the same step that writes the
// result, so no other attempt ever sees it counted twice; release gives it
// back for an attempt that ends any other way.
func (a *Auth) beginAttempt(ctx context.Context, address string) (*attempt, error) {
	a.limit.mu.Lock()
	defer a.limit.mu.Unlock()

	now := a.now().UTC()
	failures := 0
	switch rec, err := a.store.GetSigninLimit(ctx, address); {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return nil, fmt.Errorf("check sign-in limit: %w", err)
	default:
		if rec.LockedUntil != nil && now.Before(*rec.LockedUntil) {
			return nil, &SigninLimitError{RetryAfter: rec.LockedUntil.Sub(now)}
		}
		if now.Sub(rec.WindowStart) < signinWindow {
			failures = rec.Failures
		}
	}
	if a.limit.inflight == nil {
		a.limit.inflight = map[string]int{}
	}
	if failures+a.limit.inflight[address] >= signinMaxFailures {
		return nil, &SigninLimitError{RetryAfter: inflightWait}
	}
	a.limit.inflight[address]++
	return &attempt{limit: &a.limit, address: address}, nil
}

// attempt is one reserved try from an address.
type attempt struct {
	limit   *limiter
	address string
	once    sync.Once
}

// release gives the try back. Calling it again does nothing.
func (t *attempt) release() {
	t.limit.mu.Lock()
	defer t.limit.mu.Unlock()
	t.releaseLocked()
}

// releaseLocked is release for a caller that already holds limit.mu.
func (t *attempt) releaseLocked() {
	t.once.Do(func() {
		if t.limit.inflight[t.address]--; t.limit.inflight[t.address] <= 0 {
			delete(t.limit.inflight, t.address)
		}
	})
}

// recordFailure counts a failed sign-in from address and returns how many
// tries are left. The fifth failure locks the address, and then it returns a
// *SigninLimitError instead.
func (a *Auth) recordFailure(ctx context.Context, at *attempt) (triesLeft int, err error) {
	a.limit.mu.Lock()
	defer a.limit.mu.Unlock()
	defer at.releaseLocked() // runs before the unlock: deferred calls run last-in, first-out
	address := at.address

	now := a.now().UTC()
	rec := store.SigninLimitRecord{Address: address, Failures: 1, WindowStart: now}
	switch old, err := a.store.GetSigninLimit(ctx, address); {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return 0, fmt.Errorf("record failed sign-in: %w", err)
	case now.Sub(old.WindowStart) < signinWindow:
		// Still inside the window the old failures started.
		rec.Failures, rec.WindowStart = old.Failures+1, old.WindowStart
	}
	var lockErr *SigninLimitError
	if rec.Failures >= signinMaxFailures {
		until := now.Add(signinLockout)
		rec.LockedUntil = &until
		lockErr = &SigninLimitError{RetryAfter: signinLockout}
	}
	if err := a.store.PutSigninLimit(ctx, rec, now.Add(-signinPruneAfter)); err != nil {
		return 0, fmt.Errorf("record failed sign-in: %w", err)
	}
	if lockErr != nil {
		return 0, lockErr
	}
	return signinMaxFailures - rec.Failures, nil
}

// clearFailures forgets the address's failures: it signed in.
func (a *Auth) clearFailures(ctx context.Context, at *attempt) error {
	a.limit.mu.Lock()
	defer a.limit.mu.Unlock()
	defer at.releaseLocked()
	if err := a.store.DeleteSigninLimit(ctx, at.address); err != nil {
		return fmt.Errorf("clear sign-in limit: %w", err)
	}
	return nil
}
