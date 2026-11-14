package secretstest

import (
	"context"
	"maps"
	"sync"

	"github.com/cosmic-chimps/caravana/internal/secrets"
)

// Source is an in-memory secrets.Source for tests: the values of one outside
// provider, with switches that make it refuse or go down. The zero value
// isn't usable; call NewSource.
type Source struct {
	mu      sync.Mutex
	values  map[string]string
	down    bool
	refused bool
	reads   int
	probes  int
}

// NewSource returns a Source holding values (copied).
func NewSource(values map[string]string) *Source {
	return &Source{values: maps.Clone(values)}
}

// Set makes path have value.
func (s *Source) Set(path, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = map[string]string{}
	}
	s.values[path] = value
}

// Delete removes path.
func (s *Source) Delete(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, path)
}

// SetDown makes every call fail with secrets.ErrSourceUnavailable (true) or
// work again (false).
func (s *Source) SetDown(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.down = v
}

// SetRefused makes every call fail with secrets.ErrSourceRefused (true) or
// work again (false).
func (s *Source) SetRefused(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refused = v
}

// Reads counts Read calls, failed ones included: a deploy reads each
// provider once.
func (s *Source) Reads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

// Probes counts Probe calls.
func (s *Source) Probes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.probes
}

// Read implements secrets.Source: the paths it has, the others absent.
func (s *Source) Read(ctx context.Context, paths []string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	if err := s.check(); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(paths))
	for _, p := range paths {
		if v, ok := s.values[p]; ok {
			out[p] = v
		}
	}
	return out, nil
}

// Probe implements secrets.Source: how many secrets it holds.
func (s *Source) Probe(ctx context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.probes++
	if err := s.check(); err != nil {
		return 0, err
	}
	return len(s.values), nil
}

// check fails while the source is switched down or refusing. s.mu must be held.
func (s *Source) check() error {
	switch {
	case s.down:
		return secrets.Unavailable("the test provider is switched off")
	case s.refused:
		return secrets.Refused("the test provider refuses Caravana")
	}
	return nil
}
