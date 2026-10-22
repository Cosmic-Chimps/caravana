package service

import (
	"context"
	"maps"
	"slices"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// SetBackupPolicy deploys appID with policy as the backup of service: a Deploy of
// the stored app with that one change, on the condition that the app still has
// the revision the caller read (ErrAppChanged otherwise; an empty Condition
// saves whatever is there). The API reaches the same result by deploying the
// app with the service's backup set. An unknown app is store.ErrNotFound; an
// unknown service, or one that already has a policy, is an *app.FieldError on
// "service".
//
// An app that keeps its Compose file keeps it: the overlay's backup follows
// the app, as the overlay's secrets do (editSecrets), and the file's text stays
// as it was.
func (s *Service) SetBackupPolicy(ctx context.Context, appID, service string, policy app.Backup, cond Condition, trigger app.Trigger) (store.DeploymentRecord, error) {
	rec, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return store.DeploymentRecord{}, err
	}
	a := rec.App
	i := slices.IndexFunc(a.Services, func(svc app.Service) bool { return svc.Name == service })
	if i < 0 {
		return store.DeploymentRecord{}, app.NewFieldError("service", "app %q has no service %q", appID, service)
	}
	if a.Services[i].Backup != nil {
		return store.DeploymentRecord{}, app.NewFieldError("service", "service %q already has a backup policy", service)
	}
	services := slices.Clone(a.Services)
	services[i].Backup = &policy
	a.Services = services

	var m made
	if rec.Compose != nil {
		file := withBackup(*rec.Compose, service, policy)
		m.file = &file
	}
	return s.saveAndDeploy(ctx, a, cond, trigger, m, nil)
}

// withBackup returns a copy of file whose overlay for service sets policy as
// the backup. The rest of the overlay stays. file is not changed.
func withBackup(file compose.Request, service string, policy app.Backup) compose.Request {
	file = file.WithoutSecretValues() // its own Services map and Secrets slices
	services := make(map[string]compose.Overlay, len(file.Services)+1)
	maps.Copy(services, file.Services)
	overlay := services[service]
	overlay.Backup = &policy
	services[service] = overlay
	file.Services = services
	return file
}
