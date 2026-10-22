package service

import (
	"context"
	"maps"
	"slices"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// SetSecret deploys appID with the secret name of service set to value,
// keeping every other secret: a Deploy of the stored app with that one
// change. The API reaches the same result by deploying the app with that
// secret's value and the other names without one. An unknown app is
// store.ErrNotFound; an unknown service is an *app.FieldError on "service".
func (s *Service) SetSecret(ctx context.Context, appID, service, name, value string, trigger app.Trigger) (store.DeploymentRecord, error) {
	return s.SetSecretIf(ctx, appID, service, name, value, Condition{}, trigger)
}

// SetSecretIf is SetSecret on the condition that the stored app still has the
// revision the caller read (ErrAppChanged otherwise), as DeployIf does.
func (s *Service) SetSecretIf(ctx context.Context, appID, service, name, value string, cond Condition, trigger app.Trigger) (store.DeploymentRecord, error) {
	return s.editSecrets(ctx, appID, service, cond, trigger, func(list []app.Secret) ([]app.Secret, error) {
		i := slices.IndexFunc(list, func(sec app.Secret) bool { return sec.Name == name })
		if i < 0 {
			return append(list, app.Secret{Name: name, Value: &value}), nil
		}
		list[i].Value = &value
		return list, nil
	})
}

// RemoveSecret deploys appID without the secret name of service, keeping
// every other secret. A secret the service doesn't have is an
// *app.FieldError on "name".
func (s *Service) RemoveSecret(ctx context.Context, appID, service, name string, trigger app.Trigger) (store.DeploymentRecord, error) {
	return s.RemoveSecretIf(ctx, appID, service, name, Condition{}, trigger)
}

// RemoveSecretIf is RemoveSecret with a condition on the stored app's
// revision, as SetSecretIf has.
func (s *Service) RemoveSecretIf(ctx context.Context, appID, service, name string, cond Condition, trigger app.Trigger) (store.DeploymentRecord, error) {
	return s.editSecrets(ctx, appID, service, cond, trigger, func(list []app.Secret) ([]app.Secret, error) {
		i := slices.IndexFunc(list, func(sec app.Secret) bool { return sec.Name == name })
		if i < 0 {
			return nil, app.NewFieldError("name", "service %q has no secret %q", service, name)
		}
		return slices.Delete(list, i, i+1), nil
	})
}

// editSecrets deploys the stored app with service's secrets changed by edit.
// The stored secrets have no values, so every other secret is kept.
func (s *Service) editSecrets(ctx context.Context, appID, service string, cond Condition, trigger app.Trigger,
	edit func([]app.Secret) ([]app.Secret, error)) (store.DeploymentRecord, error) {
	rec, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return store.DeploymentRecord{}, err
	}
	a := rec.App
	i := slices.IndexFunc(a.Services, func(svc app.Service) bool { return svc.Name == service })
	if i < 0 {
		return store.DeploymentRecord{}, app.NewFieldError("service", "app %q has no service %q", appID, service)
	}
	list, err := edit(slices.Clone(a.Services[i].Secrets))
	if err != nil {
		return store.DeploymentRecord{}, err
	}
	services := slices.Clone(a.Services)
	services[i].Secrets = list
	a.Services = services

	var m made
	if rec.Compose != nil {
		// The secrets are part of the kept file's overlay: change its names to
		// follow the app, so the editor and the app never disagree (the Compose
		// text itself stays as it was). Without this, Deploy would drop the file.
		file := withSecretNames(*rec.Compose, service, list)
		m.file = &file
	}
	return s.saveAndDeploy(ctx, a, cond, trigger, m, nil)
}

// withSecretNames returns a copy of file whose overlay for service lists the
// secrets in list, by name only. A service left with an empty overlay has no
// entry at all, as if the overlay had never mentioned it. file is not changed.
func withSecretNames(file compose.Request, service string, list []app.Secret) compose.Request {
	file = file.WithoutSecretValues() // its own Services map and Secrets slices
	services := make(map[string]compose.Overlay, len(file.Services)+1)
	maps.Copy(services, file.Services)

	overlay := services[service]
	overlay.Secrets = nil
	for _, sec := range list {
		overlay.Secrets = append(overlay.Secrets, app.Secret{Name: sec.Name})
	}
	if len(overlay.Domains) == 0 && overlay.Health == nil && len(overlay.Secrets) == 0 && overlay.Backup == nil {
		delete(services, service)
	} else {
		services[service] = overlay
	}
	file.Services = services
	if len(services) == 0 {
		file.Services = nil
	}
	return file
}
