package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/registry"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Registry credentials (feature 017). The secret lives in a Nomad Variable
// (package deploy) and SQLite keeps what the console shows, the way the backup
// destination does (feature 010). No app is deployed or restarted by anything
// here: running apps pick the new credentials up the next time they start.

// Registries returns every registry Caravana has credentials for, ordered by
// host, without the secrets.
func (s *Service) Registries(ctx context.Context) ([]store.Registry, error) {
	return s.store.ListRegistries(ctx)
}

// SaveRegistry stores the credentials for a registry host, creating the entry
// or replacing the one with the same host. A nil password keeps the secret
// stored before; it is required the first time and may not be empty. The
// answer carries the canonical host (lowercase, "docker.io" for Docker Hub).
//
// Errors are *app.FieldError naming "host", "username" or "password", or
// Nomad's. When the credentials are saved but apps already deployed could not
// be told, the saved registry comes back together with an error that says so.
func (s *Service) SaveRegistry(ctx context.Context, host, username string, password *string) (store.Registry, error) {
	host, err := canonicalRegistryHost(host)
	if err != nil {
		return store.Registry{}, err
	}
	username = strings.TrimSpace(username)
	if username == "" {
		return store.Registry{}, app.NewFieldError("username", "is required")
	}

	var secret string
	storedUser, storedPass, stored, err := s.deployer.RegistryCredentials(ctx, host)
	if err != nil {
		return store.Registry{}, err
	}
	switch {
	case password != nil && *password == "":
		return store.Registry{}, app.NewFieldError("password", "is empty; leave it out to keep the one set before")
	case password != nil:
		secret = *password
	case !stored:
		return store.Registry{}, app.NewFieldError("password", "is required the first time")
	default:
		secret = storedPass
	}
	changed := !stored || password != nil || storedUser != username

	// The secret first: a registry listed in the console always has one.
	if err := s.deployer.SaveRegistrySecret(ctx, host, username, secret); err != nil {
		return store.Registry{}, err
	}
	now := time.Now().UTC()
	if err := s.store.SaveRegistry(ctx, store.Registry{Host: host, Username: username, CreatedAt: now, UpdatedAt: now}); err != nil {
		return store.Registry{}, err
	}
	if changed {
		// The last test said nothing about these credentials.
		if err := s.store.ClearRegistryTest(ctx, host); err != nil {
			return store.Registry{}, err
		}
	}
	rec, err := s.store.GetRegistry(ctx, host)
	if err != nil {
		return store.Registry{}, err
	}
	if err := s.deployer.SyncRegistryCopies(ctx, host); err != nil {
		return rec, fmt.Errorf("saved, but apps already deployed still have the old credentials: %w", err)
	}
	return rec, nil
}

// DeleteRegistry forgets the credentials for host. Apps already running keep
// running; the next deploy of a private image from that registry fails until
// credentials are added again. A host with none is store.ErrNotFound.
func (s *Service) DeleteRegistry(ctx context.Context, host string) error {
	host, err := knownRegistryHost(host)
	if err != nil {
		return err
	}
	if _, err := s.store.GetRegistry(ctx, host); err != nil {
		return err
	}
	// The secret first, so a failure leaves the registry listed and the delete
	// can be tried again.
	if err := s.deployer.DeleteRegistrySecret(ctx, host); err != nil {
		return err
	}
	if err := s.store.DeleteRegistry(ctx, host); err != nil {
		return err
	}
	if err := s.deployer.SyncRegistryCopies(ctx, host); err != nil {
		return fmt.Errorf("removed, but apps already deployed still have the old credentials: %w", err)
	}
	return nil
}

// TestRegistry asks the registry to accept its stored credentials and records
// the outcome with the time. A refusal or an unreachable registry is an
// outcome, not an error: it comes back in the registry's LastTest with ok
// false and the reason. A host with no credentials is store.ErrNotFound.
func (s *Service) TestRegistry(ctx context.Context, host string) (store.Registry, error) {
	host, err := knownRegistryHost(host)
	if err != nil {
		return store.Registry{}, err
	}
	if _, err := s.store.GetRegistry(ctx, host); err != nil {
		return store.Registry{}, err
	}
	outcome := store.RegistryTest{At: time.Now().UTC(), OK: true}
	if err := s.deployer.TestRegistry(ctx, host); err != nil {
		// The registry client keeps the secret out of its errors.
		outcome.OK, outcome.Message = false, err.Error()
	}
	if err := s.store.SaveRegistryTest(ctx, host, outcome); err != nil {
		return store.Registry{}, err
	}
	return s.store.GetRegistry(ctx, host)
}

// canonicalRegistryHost validates a host as an image names it ("ghcr.io",
// "registry.example.com:5000", "docker.io") and returns it in the one form
// everything stores and compares: lowercase, with "index.docker.io" as
// "docker.io". It reads the host the way an image reference does, so a name
// that Docker would take for a Docker Hub user ("acme") is refused.
func canonicalRegistryHost(host string) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", app.NewFieldError("host", "is required")
	}
	// An image's first path part is a host when it has a "." or a ":", or is
	// "localhost" (registry.Parse reads it the same way).
	if strings.ContainsAny(host, "/@ \t") || !(strings.ContainsAny(host, ".:") || strings.EqualFold(host, "localhost")) {
		return "", app.NewFieldError("host", "%q isn't a registry host: write it as in an image name, like ghcr.io or registry.example.com:5000 (docker.io for Docker Hub)", host)
	}
	ref, err := registry.Parse(host + "/x")
	if err != nil {
		return "", app.NewFieldError("host", "%q isn't a registry host: write it as in an image name, like ghcr.io or registry.example.com:5000 (docker.io for Docker Hub)", host)
	}
	return ref.Registry, nil
}

// knownRegistryHost is canonicalRegistryHost for the calls that act on a
// registry already stored: a host that can't be one is simply not stored, so
// the answer is store.ErrNotFound rather than a field error.
func knownRegistryHost(host string) (string, error) {
	canonical, err := canonicalRegistryHost(host)
	var fe *app.FieldError
	if errors.As(err, &fe) {
		return "", fmt.Errorf("registry %q: %w", host, store.ErrNotFound)
	}
	return canonical, err
}
