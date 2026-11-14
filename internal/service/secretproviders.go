package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/secrets"
	"github.com/cosmic-chimps/caravana/internal/secrets/bella"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Secret providers (feature 037): the outside places an app's secrets may
// point at ("from bella-prod"). SQLite keeps what the console shows; the
// credentials live in a Nomad Variable (package deploy), as the registries'
// and the notification channels' secrets do. Nothing here deploys or restarts
// an app.

// SourceFactory builds the secrets.Source of a stored provider from its
// credentials (deploy's SecretProviderAPIKey and SecretProviderPrivateKey
// items). A bad setting is an *app.FieldError. cmd/caravana gives the Bella
// one (BellaSource); tests give a function returning a secretstest.Source.
type SourceFactory func(p store.SecretProvider, creds map[string]string) (secrets.Source, error)

// SetSecretSources sets how a stored provider becomes a Source. Until it is
// called no provider can be saved, tested or read.
func (s *Service) SetSecretSources(f SourceFactory) { s.secretSources.Store(&f) }

// BellaSource is the SourceFactory for Bella Baxter, through Bella's own SDK
// (package bella). A refused setting is a field error naming it as the
// provider's form and API do: bella.url, bella.project, bella.environment,
// apiKey, privateKey.
func BellaSource(p store.SecretProvider, creds map[string]string) (secrets.Source, error) {
	src, err := bella.New(bella.Config{
		URL: p.URL, Project: p.Project, Environment: p.Environment,
		Auth: bella.APIKey{Key: creds[deploy.SecretProviderAPIKey], PrivateKey: creds[deploy.SecretProviderPrivateKey]},
	})
	var ce *bella.ConfigError
	switch {
	case errors.As(err, &ce):
		field := ce.Field
		if field == "url" || field == "project" || field == "environment" {
			field = "bella." + field
		}
		return nil, app.NewFieldError(field, "%s", ce.Err.Error())
	case err != nil:
		return nil, errors.New("the provider's settings could not be used")
	}
	return src, nil
}

// errNoSecretSources is what a Service without SetSecretSources says.
var errNoSecretSources = errors.New("secret providers are not set up in this installation")

// sourceOf builds p's Source.
func (s *Service) sourceOf(p store.SecretProvider, creds map[string]string) (secrets.Source, error) {
	f := s.secretSources.Load()
	if f == nil || *f == nil {
		return nil, errNoSecretSources
	}
	return (*f)(p, creds)
}

// SecretProviderInfo is a provider with the apps that reference it now.
type SecretProviderInfo struct {
	store.SecretProvider
	// UsedBy are the IDs of the stored apps with a reference to the provider,
	// in order.
	UsedBy []string
}

// ProviderInUseError is a provider that apps still reference, so it can't be
// removed. It matches ErrSecretProviderInUse.
type ProviderInUseError struct {
	Name string
	Apps []string
}

// ErrSecretProviderInUse matches every *ProviderInUseError.
var ErrSecretProviderInUse = errors.New("secret provider in use")

func (e *ProviderInUseError) Error() string {
	return fmt.Sprintf("secret provider %q is used by %s: remove those references first", e.Name, strings.Join(e.Apps, ", "))
}

// Is lets errors.Is find ErrSecretProviderInUse.
func (e *ProviderInUseError) Is(target error) bool { return target == ErrSecretProviderInUse }

// SecretProviderInput is what Save takes. APIKey and PrivateKey are nil to
// keep the stored ones; both are required the first time, and neither may be
// empty.
type SecretProviderInput struct {
	Name        string
	Kind        store.SecretProviderKind
	URL         string
	Project     string
	Environment string
	// Allowed limits the provider to places: each "project" or
	// "project/environment". Empty means every place.
	Allowed    []string
	APIKey     *string
	PrivateKey *string
}

// SecretProviders returns every provider with the apps that use it, ordered by
// name, never with a credential.
func (s *Service) SecretProviders(ctx context.Context) ([]SecretProviderInfo, error) {
	list, err := s.store.ListSecretProviders(ctx)
	if err != nil {
		return nil, err
	}
	used, err := s.providerUsers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]SecretProviderInfo, len(list))
	for i, p := range list {
		out[i] = SecretProviderInfo{SecretProvider: p, UsedBy: used[p.Name]}
	}
	return out, nil
}

// SecretProvider returns one provider, or store.ErrNotFound.
func (s *Service) SecretProvider(ctx context.Context, name string) (SecretProviderInfo, error) {
	p, err := s.store.GetSecretProvider(ctx, name)
	if err != nil {
		return SecretProviderInfo{}, err
	}
	used, err := s.providerUsers(ctx)
	if err != nil {
		return SecretProviderInfo{}, err
	}
	return SecretProviderInfo{SecretProvider: p, UsedBy: used[p.Name]}, nil
}

// providerUsers maps each provider name to the IDs of the stored apps that
// reference it.
func (s *Service) providerUsers(ctx context.Context) (map[string][]string, error) {
	apps, err := s.store.ListApps(ctx) // ordered by ID
	if err != nil {
		return nil, err
	}
	used := map[string][]string{}
	for _, rec := range apps {
		seen := map[string]bool{}
		for _, svc := range rec.App.Services {
			for _, sec := range svc.Secrets {
				if sec.From != nil && !seen[sec.From.Provider] {
					seen[sec.From.Provider] = true
					used[sec.From.Provider] = append(used[sec.From.Provider], rec.App.ID)
				}
			}
		}
	}
	return used, nil
}

// SaveSecretProvider creates or replaces a provider. The credentials go to the
// Nomad Variable first, then the settings to SQLite, so a provider listed in
// the console always has them. The settings and credentials are checked by
// building the provider's Source before anything is written; errors are
// *app.FieldError on name, kind, bella.url, bella.project, bella.environment,
// allowed[i], apiKey or privateKey. created is true for a new provider. The
// last test is forgotten when the settings or the credentials changed.
func (s *Service) SaveSecretProvider(ctx context.Context, in SecretProviderInput) (info SecretProviderInfo, created bool, err error) {
	if err := app.ValidName("name", in.Name); err != nil {
		return SecretProviderInfo{}, false, err
	}
	if in.Kind != store.SecretProviderBella {
		return SecretProviderInfo{}, false, app.NewFieldError("kind", "must be %q", store.SecretProviderBella)
	}
	allowed, err := s.checkAllowedPlaces(ctx, in.Allowed)
	if err != nil {
		return SecretProviderInfo{}, false, err
	}

	existing, err := s.store.GetSecretProvider(ctx, in.Name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		created = true
	case err != nil:
		return SecretProviderInfo{}, false, err
	}
	stored, _, err := s.deployer.SecretProviderCredentials(ctx, in.Name)
	if err != nil {
		return SecretProviderInfo{}, false, err
	}
	creds := map[string]string{}
	for _, c := range []struct {
		field, item string
		given       *string
	}{
		{"apiKey", deploy.SecretProviderAPIKey, in.APIKey},
		{"privateKey", deploy.SecretProviderPrivateKey, in.PrivateKey},
	} {
		switch {
		case c.given != nil && *c.given == "":
			return SecretProviderInfo{}, false, app.NewFieldError(c.field, "is empty; leave it out to keep the one set before")
		case c.given != nil:
			creds[c.item] = *c.given
		case stored[c.item] != "":
			creds[c.item] = stored[c.item]
		default:
			return SecretProviderInfo{}, false, app.NewFieldError(c.field, "is required the first time")
		}
	}

	now := time.Now().UTC()
	p := store.SecretProvider{
		Name: in.Name, Kind: in.Kind,
		URL: strings.TrimSpace(in.URL), Project: strings.TrimSpace(in.Project), Environment: strings.TrimSpace(in.Environment),
		Allowed: allowed, CreatedAt: now, UpdatedAt: now,
	}
	// Building the source checks every setting and both keys' forms.
	if _, err := s.sourceOf(p, creds); err != nil {
		return SecretProviderInfo{}, false, err
	}

	changed := created || in.APIKey != nil || in.PrivateKey != nil ||
		existing.URL != p.URL || existing.Project != p.Project || existing.Environment != p.Environment
	if in.APIKey != nil || in.PrivateKey != nil || created {
		if err := s.deployer.SaveSecretProviderCredentials(ctx, in.Name, creds); err != nil {
			return SecretProviderInfo{}, false, err
		}
	}
	if err := s.store.SaveSecretProvider(ctx, p); err != nil {
		return SecretProviderInfo{}, false, err
	}
	if changed {
		if err := s.store.ClearSecretProviderTest(ctx, in.Name); err != nil {
			return SecretProviderInfo{}, false, err
		}
	}
	info, err = s.SecretProvider(ctx, in.Name)
	return info, created, err
}

// checkAllowedPlaces validates and tidies the places a provider is limited to:
// each is a project, or project/environment, that exists. Duplicates collapse.
// Errors are field errors on allowed[i].
func (s *Service) checkAllowedPlaces(ctx context.Context, places []string) ([]string, error) {
	out := []string{}
	for i, raw := range places {
		field := fmt.Sprintf("allowed[%d]", i)
		place := strings.TrimSpace(raw)
		project, env, hasEnv := strings.Cut(place, "/")
		if err := app.ValidName(field, project); err != nil {
			return nil, app.NewFieldError(field, "%q must be a project or project/environment", place)
		}
		if _, err := s.store.GetProject(ctx, project); errors.Is(err, store.ErrNotFound) {
			return nil, app.NewFieldError(field, "there is no project %q", project)
		} else if err != nil {
			return nil, err
		}
		if hasEnv {
			if err := app.ValidName(field, env); err != nil {
				return nil, app.NewFieldError(field, "%q must be a project or project/environment", place)
			}
			ok, err := s.store.EnvironmentExists(ctx, project, env)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, app.NewFieldError(field, "project %q has no environment %q", project, env)
			}
		}
		if !slices.Contains(out, place) {
			out = append(out, place)
		}
	}
	return out, nil
}

// DeleteSecretProvider removes a provider and its credentials. A provider some
// stored app still references is a *ProviderInUseError naming the apps; an
// unknown one is store.ErrNotFound. Running apps are not touched either way: a
// removed provider only stops the next deploy.
func (s *Service) DeleteSecretProvider(ctx context.Context, name string) error {
	if _, err := s.store.GetSecretProvider(ctx, name); err != nil {
		return err
	}
	used, err := s.providerUsers(ctx)
	if err != nil {
		return err
	}
	if apps := used[name]; len(apps) > 0 {
		return &ProviderInUseError{Name: name, Apps: apps}
	}
	// The credentials first, so a failure leaves the provider listed and the
	// delete can be tried again.
	if err := s.deployer.DeleteSecretProviderCredentials(ctx, name); err != nil {
		return err
	}
	return s.store.DeleteSecretProvider(ctx, name)
}

// TestSecretProvider reads the provider once and records the outcome with the
// time. A refusal or an unreachable provider is an outcome, not an error: it
// comes back in LastTest with OK false and the reason in Caravana's words. An
// unknown provider is store.ErrNotFound.
func (s *Service) TestSecretProvider(ctx context.Context, name string) (SecretProviderInfo, error) {
	p, err := s.store.GetSecretProvider(ctx, name)
	if err != nil {
		return SecretProviderInfo{}, err
	}
	outcome := store.SecretProviderTest{At: time.Now().UTC()}
	creds, ok, err := s.deployer.SecretProviderCredentials(ctx, name)
	if err != nil {
		return SecretProviderInfo{}, err
	}
	if !ok {
		outcome.Message = "Caravana has no credentials for this provider: save them again"
	} else if src, err := s.sourceOf(p, creds); err != nil {
		outcome.Message = err.Error() // a field error or a fixed sentence, never a credential
	} else {
		probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		n, err := src.Probe(probeCtx)
		cancel()
		switch {
		case err == nil:
			outcome.OK, outcome.Count = true, n
			outcome.Message = fmt.Sprintf("%d secrets readable", n)
			if n == 1 {
				outcome.Message = "1 secret readable"
			}
		default:
			outcome.Message = failureWords(err)
		}
	}
	if err := s.store.SaveSecretProviderTest(ctx, name, outcome); err != nil {
		return SecretProviderInfo{}, err
	}
	return s.SecretProvider(ctx, name)
}

// failureWords is a Source error as one sentence safe to store and show: the
// Source's own reason, which is in Caravana's words, or a fixed one.
func failureWords(err error) string {
	var se *secrets.SourceError
	if errors.As(err, &se) {
		return se.Reason
	}
	return "Caravana couldn't read the provider"
}
