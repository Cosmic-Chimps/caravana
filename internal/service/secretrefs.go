package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/secrets"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Secret references (feature 037). A secret with `from: {provider, path}` has
// no value in the app's definition: a deploy reads it from the provider, once
// per provider, under the app's lock and just before the sets are built, and
// writes it into the service's Nomad Variable like a typed one. Values exist
// only between the read and secrets.Provider.Replace: no error, record or log
// here carries one.

// ErrSecretNotFound is a reference whose path the provider doesn't have.
var ErrSecretNotFound = errors.New("secret not found in the provider")

// ReferenceError is a reference a deploy couldn't resolve. Field is the
// definition's field at fault: services[i].secrets[j].from.path when the
// provider doesn't have the path, services[i].secrets[j].from when the
// provider as a whole refused or couldn't be reached. Err is
// ErrSecretNotFound or a *secrets.SourceError, so errors.Is finds
// ErrSecretNotFound, secrets.ErrSourceRefused or secrets.ErrSourceUnavailable.
// It names the provider and the path, never a value or the provider's text.
type ReferenceError struct {
	Field    string
	Provider string
	Path     string // "" when the whole provider failed
	Err      error
}

func (e *ReferenceError) Error() string {
	if errors.Is(e.Err, ErrSecretNotFound) {
		return fmt.Sprintf("secret provider %q has no secret %q", e.Provider, e.Path)
	}
	return fmt.Sprintf("secret provider %q: %s", e.Provider, e.Err)
}

// Unwrap lets errors.Is and errors.As find Err.
func (e *ReferenceError) Unwrap() error { return e.Err }

// referenceAt is one reference of an app, with where it is.
type referenceAt struct {
	service, secret string
	ref             app.SecretRef
	field           string // services[i].secrets[j].from
}

// referencesOf lists a's references in definition order.
func referencesOf(a app.App) []referenceAt {
	var out []referenceAt
	for i, svc := range a.Services {
		for j, sec := range svc.Secrets {
			if sec.From != nil {
				out = append(out, referenceAt{
					service: svc.Name, secret: sec.Name, ref: *sec.From,
					field: fmt.Sprintf("services[%d].secrets[%d].from", i, j),
				})
			}
		}
	}
	return out
}

// HasSecretReferences reports whether any secret of a is a reference.
func HasSecretReferences(a app.App) bool { return len(referencesOf(a)) > 0 }

// placeOfApp is where a's ID puts it. An ID that doesn't read as a place (one
// from before projects, with a "--") is in Default's production, as the store
// has it.
func placeOfApp(a app.App) app.Place {
	if p, err := app.PlaceOf(a.ID); err == nil {
		return p
	}
	return app.Place{Project: app.DefaultProject, Environment: app.DefaultEnvironment, Name: a.ID}
}

// providerAllows reports whether p may be used by an app in place: it has no
// limits, or lists the place's project or project/environment.
func providerAllows(p store.SecretProvider, place app.Place) bool {
	if len(p.Allowed) == 0 {
		return true
	}
	return slices.Contains(p.Allowed, place.Project) || slices.Contains(p.Allowed, place.Project+"/"+place.Environment)
}

// checkReferences runs the static checks of every reference of a: its provider
// exists and may be used by a's place. Errors are *app.FieldError on
// services[i].secrets[j].from.provider. Nothing is read from a provider.
func (s *Service) checkReferences(ctx context.Context, a app.App) error {
	refs := referencesOf(a)
	if len(refs) == 0 {
		return nil
	}
	place := placeOfApp(a)
	found := map[string]store.SecretProvider{}
	for _, r := range refs {
		if _, ok := found[r.ref.Provider]; !ok {
			p, err := s.store.GetSecretProvider(ctx, r.ref.Provider)
			if errors.Is(err, store.ErrNotFound) {
				return app.NewFieldError(r.field+".provider", "no secret provider named %q", r.ref.Provider)
			}
			if err != nil {
				return err
			}
			found[r.ref.Provider] = p
		}
		if !providerAllows(found[r.ref.Provider], place) {
			return app.NewFieldError(r.field+".provider", "secret provider %q can't be used by %s/%s", r.ref.Provider, place.Project, place.Environment)
		}
	}
	return nil
}

// resolveReferences reads the value of every reference of a, one read per
// provider, and returns them as sets for secrets.Build. The static checks run
// again first, since the provider may have changed since the app was saved. An
// app with no reference reads nothing. Errors are *app.FieldError (a provider
// gone or no longer allowed), *ReferenceError, or the store's and Nomad's when
// the provider's credentials can't be read.
func (s *Service) resolveReferences(ctx context.Context, a app.App) (secrets.Sets, error) {
	refs := referencesOf(a)
	if len(refs) == 0 {
		return nil, nil
	}
	if err := s.checkReferences(ctx, a); err != nil {
		return nil, err
	}

	// Group by provider, in the order the definition first names them.
	var order []string
	byProvider := map[string][]referenceAt{}
	for _, r := range refs {
		if _, ok := byProvider[r.ref.Provider]; !ok {
			order = append(order, r.ref.Provider)
		}
		byProvider[r.ref.Provider] = append(byProvider[r.ref.Provider], r)
	}

	resolved := secrets.Sets{}
	for _, name := range order {
		group := byProvider[name]
		values, err := s.readProvider(ctx, name, group)
		if err != nil {
			return nil, err
		}
		for _, r := range group {
			v, ok := values[r.ref.Path]
			if !ok {
				return nil, &ReferenceError{Field: r.field + ".path", Provider: name, Path: r.ref.Path, Err: ErrSecretNotFound}
			}
			// A value goes into a Nomad Variable as JSON, as a typed one does.
			if !utf8.ValidString(v) || strings.ContainsRune(v, 0) {
				return nil, &ReferenceError{Field: r.field + ".path", Provider: name, Path: r.ref.Path,
					Err: secrets.Refused("the value is not text (UTF-8 without a NUL byte)")}
			}
			if resolved[r.service] == nil {
				resolved[r.service] = map[string]string{}
			}
			resolved[r.service][r.secret] = v
		}
	}
	return resolved, nil
}

// readProvider reads the paths of group (all one provider's) in one call.
func (s *Service) readProvider(ctx context.Context, name string, group []referenceAt) (map[string]string, error) {
	fail := func(err error) error {
		return &ReferenceError{Field: group[0].field, Provider: name, Err: err}
	}
	p, err := s.store.GetSecretProvider(ctx, name)
	if err != nil {
		return nil, err
	}
	creds, ok, err := s.deployer.SecretProviderCredentials(ctx, name)
	if err != nil {
		return nil, err // the Nomad store: secrets.ErrUnavailable
	}
	if !ok {
		return nil, fail(secrets.Refused("Caravana has no credentials for this provider: save them again in Settings"))
	}
	src, err := s.sourceOf(p, creds)
	if err != nil {
		var fe *app.FieldError
		if errors.As(err, &fe) {
			return nil, fail(secrets.Refused(fmt.Sprintf("the provider's saved settings can't be used (%s): save them again in Settings", fe.Field)))
		}
		// Today sourceOf's other errors are fixed sentences too, but a factory's
		// text is not ours to show: say it in our words.
		return nil, fail(secrets.Unavailable("Caravana couldn't set up this provider"))
	}
	paths := make([]string, 0, len(group))
	for _, r := range group {
		if !slices.Contains(paths, r.ref.Path) {
			paths = append(paths, r.ref.Path)
		}
	}
	values, err := src.Read(ctx, paths)
	if err != nil {
		var se *secrets.SourceError
		if !errors.As(err, &se) {
			se = secrets.Unavailable("Caravana couldn't read the provider")
		}
		return nil, fail(se)
	}
	return values, nil
}

// placeholderSets is the sets a reference would resolve to, with empty values:
// enough for secrets.Build to check a's other secrets before anything is read.
func placeholderSets(a app.App) secrets.Sets {
	out := secrets.Sets{}
	for _, r := range referencesOf(a) {
		if out[r.service] == nil {
			out[r.service] = map[string]string{}
		}
		out[r.service][r.secret] = ""
	}
	return out
}

// ProviderAllows reports whether p may be used by an app in place: the console
// offers only these providers when a secret is added.
func ProviderAllows(p store.SecretProvider, place app.Place) bool { return providerAllows(p, place) }
