package service

import (
	"context"
	"slices"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// SetSecret deploys appID with the secret name of service set to value,
// keeping every other secret: a Deploy of the stored app with that one
// change. The API reaches the same result by deploying the app with that
// secret's value and the other names without one. An unknown app is
// store.ErrNotFound; an unknown service is an *app.FieldError on "service".
func (s *Service) SetSecret(ctx context.Context, appID, service, name, value string, trigger app.Trigger) (store.DeploymentRecord, error) {
	return s.editSecrets(ctx, appID, service, trigger, func(list []app.Secret) ([]app.Secret, error) {
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
	return s.editSecrets(ctx, appID, service, trigger, func(list []app.Secret) ([]app.Secret, error) {
		i := slices.IndexFunc(list, func(sec app.Secret) bool { return sec.Name == name })
		if i < 0 {
			return nil, app.NewFieldError("name", "service %q has no secret %q", service, name)
		}
		return slices.Delete(list, i, i+1), nil
	})
}

// editSecrets deploys the stored app with service's secrets changed by edit.
// The stored secrets have no values, so every other secret is kept.
func (s *Service) editSecrets(ctx context.Context, appID, service string, trigger app.Trigger,
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
	return s.Deploy(ctx, a, trigger)
}
