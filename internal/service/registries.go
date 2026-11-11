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

// Registries returns every registry entry Caravana has credentials for,
// ordered by host and path, without the secrets.
func (s *Service) Registries(ctx context.Context) ([]store.Registry, error) {
	return s.store.ListRegistries(ctx)
}

// SaveRegistry stores the credentials for a registry entry, creating it or
// replacing the one with the same key. An entry is a host ("ghcr.io") or a host
// with a path prefix ("ghcr.io/acme"): images under the path use it, and the
// host's own entry covers the rest of that registry. A nil password keeps the
// secret stored before; it is required the first time and may not be empty.
// The answer carries the canonical entry (host lowercase, "docker.io" for
// Docker Hub).
//
// Errors are *app.FieldError naming "host", "path", "username" or "password",
// or Nomad's. When the credentials are saved but apps already deployed could
// not be told, the saved registry comes back together with an error that says
// so.
func (s *Service) SaveRegistry(ctx context.Context, entry, username string, password *string) (store.Registry, error) {
	entry, err := canonicalRegistryEntry(entry)
	if err != nil {
		return store.Registry{}, err
	}
	username = strings.TrimSpace(username)
	if username == "" {
		return store.Registry{}, app.NewFieldError("username", "is required")
	}

	var secret string
	storedUser, storedPass, stored, err := s.deployer.RegistryCredentials(ctx, entry)
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
	if err := s.deployer.SaveRegistrySecret(ctx, entry, username, secret); err != nil {
		return store.Registry{}, err
	}
	host, path := registry.SplitEntry(entry)
	now := time.Now().UTC()
	if err := s.store.SaveRegistry(ctx, store.Registry{Host: host, Path: path, Username: username, CreatedAt: now, UpdatedAt: now}); err != nil {
		return store.Registry{}, err
	}
	if changed {
		// The last test said nothing about these credentials.
		if err := s.store.ClearRegistryTest(ctx, host, path); err != nil {
			return store.Registry{}, err
		}
	}
	rec, err := s.store.GetRegistry(ctx, host, path)
	if err != nil {
		return store.Registry{}, err
	}
	private, err := s.privateRegistries(ctx)
	if err == nil {
		err = s.deployer.SyncRegistryCopies(ctx, entry, private)
	}
	if err != nil {
		return rec, fmt.Errorf("saved, but apps already deployed still have the old credentials: %w", err)
	}
	return rec, nil
}

// DeleteRegistry forgets the credentials for an entry. Apps already running
// keep running; the next deploy of a private image it covered fails until
// credentials are added again (or the host's own entry, if there is one,
// covers it). An entry with none is store.ErrNotFound.
func (s *Service) DeleteRegistry(ctx context.Context, entry string) error {
	entry, err := knownRegistryEntry(entry)
	if err != nil {
		return err
	}
	host, path := registry.SplitEntry(entry)
	if _, err := s.store.GetRegistry(ctx, host, path); err != nil {
		return err
	}
	// The secret first, so a failure leaves the registry listed and the delete
	// can be tried again.
	if err := s.deployer.DeleteRegistrySecret(ctx, entry); err != nil {
		return err
	}
	if err := s.store.DeleteRegistry(ctx, host, path); err != nil {
		return err
	}
	private, err := s.privateRegistries(ctx)
	if err == nil {
		err = s.deployer.SyncRegistryCopies(ctx, entry, private)
	}
	if err != nil {
		return fmt.Errorf("removed, but apps already deployed still have the old credentials: %w", err)
	}
	return nil
}

// TestRegistry asks the registry to accept the entry's stored credentials and
// records the outcome with the time. A refusal or an unreachable registry is an
// outcome, not an error: it comes back in the registry's LastTest with ok
// false and the reason. An entry with no credentials is store.ErrNotFound.
//
// The registry's login check says nothing about which images a login can read,
// so for an entry with a path an accepted login comes with a message saying the
// first deploy shows that.
func (s *Service) TestRegistry(ctx context.Context, entry string) (store.Registry, error) {
	entry, err := knownRegistryEntry(entry)
	if err != nil {
		return store.Registry{}, err
	}
	host, path := registry.SplitEntry(entry)
	if _, err := s.store.GetRegistry(ctx, host, path); err != nil {
		return store.Registry{}, err
	}
	outcome := store.RegistryTest{At: time.Now().UTC(), OK: true}
	if err := s.deployer.TestRegistry(ctx, entry); err != nil {
		// The registry client keeps the secret out of its errors.
		outcome.OK, outcome.Message = false, err.Error()
	} else if path != "" {
		outcome.Message = fmt.Sprintf("A registry doesn't say which images a login can read: the first deploy of an image under %s/ shows it.", entry)
	}
	if err := s.store.SaveRegistryTest(ctx, host, path, outcome); err != nil {
		return store.Registry{}, err
	}
	return s.store.GetRegistry(ctx, host, path)
}

// canonicalRegistryEntry validates an entry, a host as an image names it
// ("ghcr.io", "registry.example.com:5000", "docker.io") with an optional path
// prefix after a "/" ("ghcr.io/acme"), and returns it in the one form
// everything stores and compares: the host lowercase, with "index.docker.io"
// as "docker.io", and the path without a "/" at either end. The host is read
// the way an image reference reads it, so a name that Docker would take for a
// Docker Hub user ("acme") is refused; the path must be a valid image path.
func canonicalRegistryEntry(entry string) (string, error) {
	entry = strings.TrimSpace(entry)
	host, path, _ := strings.Cut(entry, "/")
	host, err := canonicalRegistryHost(host)
	if err != nil {
		return "", err
	}
	path = strings.Trim(path, "/")
	if path == "" {
		return host, nil
	}
	// The path is valid when it is the start of an image path.
	if ref, err := registry.Parse(host + "/" + path + "/x"); err != nil || ref.Repository != path+"/x" {
		return "", app.NewFieldError("path", "%q isn't an image path: lowercase parts separated by \"/\", like cosmic-chimps", path)
	}
	return host + "/" + path, nil
}

// knownRegistryEntry is canonicalRegistryEntry for the calls that act on an
// entry already stored: one that can't be an entry is simply not stored, so the
// answer is store.ErrNotFound rather than a field error.
func knownRegistryEntry(entry string) (string, error) {
	canonical, err := canonicalRegistryEntry(entry)
	var fe *app.FieldError
	if errors.As(err, &fe) {
		return "", fmt.Errorf("registry %q: %w", entry, store.ErrNotFound)
	}
	return canonical, err
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
