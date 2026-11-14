package service

import (
	"sync"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/secrets"
	"github.com/cosmic-chimps/caravana/internal/secrets/secretstest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// fakeSources is a SourceFactory for tests: every provider name has its own
// in-memory secretstest.Source, made on first use, and the credentials each
// build was given are kept so a test can check they came from the Nomad
// Variable. A credential that is "bad" is refused like a malformed key.
type fakeSources struct {
	mu      sync.Mutex
	sources map[string]*secretstest.Source
	creds   map[string]map[string]string // the credentials of the last build, by provider
	builds  int
}

func newFakeSources() *fakeSources {
	return &fakeSources{sources: map[string]*secretstest.Source{}, creds: map[string]map[string]string{}}
}

// source returns the fake of the provider called name.
func (f *fakeSources) source(name string) *secretstest.Source {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sources[name] == nil {
		f.sources[name] = secretstest.NewSource(nil)
	}
	return f.sources[name]
}

// lastCreds returns the credentials the last build of name was given.
func (f *fakeSources) lastCreds(name string) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.creds[name]
}

func (f *fakeSources) factory(p store.SecretProvider, creds map[string]string) (secrets.Source, error) {
	f.mu.Lock()
	f.builds++
	f.creds[p.Name] = creds
	f.mu.Unlock()
	if p.URL == "" {
		return nil, app.NewFieldError("bella.url", "must be an https:// address")
	}
	if creds[deploy.SecretProviderAPIKey] == "bad" {
		return nil, app.NewFieldError("apiKey", "must be a Bella API key")
	}
	return f.source(p.Name), nil
}
