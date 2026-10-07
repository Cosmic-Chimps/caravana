// Package secretstest is an in-memory secrets.Provider for tests. It follows
// the same rules as the Nomad Variables store (who owns the kept copy, what
// Commit and Restore do), so service tests exercise the real flow without a
// Nomad. Product code never imports it.
package secretstest

import (
	"context"
	"fmt"
	"maps"
	"sync"

	"github.com/cosmic-chimps/caravana/internal/secrets"
)

// Store is an in-memory secrets.Provider. The zero value isn't usable; call
// New.
type Store struct {
	mu          sync.Mutex
	live        map[string]secrets.Sets // by app ID
	kept        map[string]kept         // by app ID
	unavailable bool
	replaces    int
}

// kept is an app's copy of the sets a deployment replaced.
type kept struct {
	owner string
	sets  secrets.Sets
}

// New returns an empty Store.
func New() *Store {
	return &Store{live: map[string]secrets.Sets{}, kept: map[string]kept{}}
}

// SetUnavailable makes every call fail with secrets.ErrUnavailable (true) or
// work again (false).
func (s *Store) SetUnavailable(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unavailable = v
}

// Live returns a copy of appID's live sets.
func (s *Store) Live(appID string) secrets.Sets {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.live[appID])
}

// SetLive changes appID's live sets, as if written outside Caravana.
func (s *Store) SetLive(appID string, sets secrets.Sets) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.live[appID] = clone(sets)
}

// Kept returns the deployment that owns appID's kept copy and the copy, or
// "" and nil when there is none.
func (s *Store) Kept(appID string) (owner string, sets secrets.Sets) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.kept[appID]
	if !ok {
		return "", nil
	}
	return k.owner, clone(k.sets)
}

// Replaces counts Replace calls that succeeded.
func (s *Store) Replaces() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.replaces
}

// Read implements secrets.Provider.
func (s *Store) Read(ctx context.Context, appID string) (secrets.Sets, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return nil, err
	}
	return clone(s.live[appID]), nil
}

// Replace implements secrets.Provider.
func (s *Store) Replace(ctx context.Context, appID, deploymentID string, sets secrets.Sets) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return err
	}
	current := s.live[appID]
	if k, ok := s.kept[appID]; ok {
		s.kept[appID] = kept{owner: deploymentID, sets: k.sets}
	} else if !current.Equal(sets) {
		s.kept[appID] = kept{owner: deploymentID, sets: clone(current)}
	}
	s.live[appID] = clone(sets)
	s.replaces++
	return nil
}

// Commit implements secrets.Provider.
func (s *Store) Commit(ctx context.Context, appID, deploymentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return err
	}
	if k, ok := s.kept[appID]; ok && k.owner == deploymentID {
		delete(s.kept, appID)
	}
	return nil
}

// Restore implements secrets.Provider.
func (s *Store) Restore(ctx context.Context, appID, deploymentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return err
	}
	if k, ok := s.kept[appID]; ok && k.owner == deploymentID {
		s.live[appID] = k.sets
		delete(s.kept, appID)
	}
	return nil
}

// Delete implements secrets.Provider.
func (s *Store) Delete(ctx context.Context, appID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return err
	}
	delete(s.live, appID)
	delete(s.kept, appID)
	return nil
}

// check fails while the store is unavailable. s.mu must be held.
func (s *Store) check() error {
	if s.unavailable {
		return fmt.Errorf("%w: the test store is switched off", secrets.ErrUnavailable)
	}
	return nil
}

// clone copies sets two levels deep, so callers never share maps with the
// store.
func clone(sets secrets.Sets) secrets.Sets {
	out := secrets.Sets{}
	for name, set := range sets {
		if len(set) > 0 {
			out[name] = maps.Clone(set)
		}
	}
	return out
}
