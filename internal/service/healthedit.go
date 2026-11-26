package service

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/audit"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// SetHealthIf deploys appID with service's health check set to path, interval
// and timeout (feature 059), on the condition that the stored app still has the
// revision the caller read (ErrAppChanged otherwise), as DeployIf does. An
// empty path with no interval and no timeout is Caravana's default check, a TCP
// connection to the service's port. The check's port stays as it was. It is
// what Configure › Services saves for an app that keeps its Compose file; the
// API reaches the same result by deploying the app with the service's health
// set.
//
// An app that keeps its Compose file keeps it: the overlay's health for service
// is rewritten and the app is deployed from the edited file, whose text stays as
// typed, with no secret value in it. Its field errors then name the file's paths
// ("services.web.health.interval"); a definition's name the app's.
//
// Errors: store.ErrNotFound for an unknown app, ErrAppStopped for a stopped one,
// an *app.FieldError on "service" for a service the app doesn't have or one
// without a port (it has no health check), and every error of a deploy.
func (s *Service) SetHealthIf(ctx context.Context, appID, service, path string, interval, timeout time.Duration, cond Condition, trigger app.Trigger) (store.DeploymentRecord, error) {
	rec, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return store.DeploymentRecord{}, err
	}
	if rec.StoppedAt != nil {
		return store.DeploymentRecord{}, fmt.Errorf("app %q: %w", appID, ErrAppStopped)
	}
	i := slices.IndexFunc(rec.App.Services, func(svc app.Service) bool { return svc.Name == service })
	if i < 0 {
		return store.DeploymentRecord{}, app.NewFieldError("service", "app %q has no service %q", appID, service)
	}
	if len(rec.App.Services[i].Ports) == 0 {
		return store.DeploymentRecord{}, app.NewFieldError("service", "service %q has no port, so it has no health check", service)
	}

	if rec.Compose == nil {
		a := rec.App
		a.Services = slices.Clone(a.Services)
		health := app.HealthCheck{}
		if h := a.Services[i].Health; h != nil {
			health = *h // keeps its port
		}
		health.Path, health.Interval, health.Timeout = path, interval, timeout
		a.Services[i].Health = &health
		return recordOnly(s.saveAndDeploy(ctx, a, cond, trigger, made{action: audit.ActionUpdated}, nil))
	}

	file := withHealth(*rec.Compose, service, path, interval, timeout)
	// The kept file has no secret values: each secret is deployed without one,
	// which keeps the value the secret store has.
	a, warnings, err := compose.Translate(appID, file)
	if err != nil {
		return store.DeploymentRecord{}, err
	}
	res, err := s.saveAndDeploy(ctx, a, cond, trigger, made{file: &file, warnings: warnings, action: audit.ActionUpdated}, nil)
	return res.DeploymentRecord, inFile(err, a, file)
}

// withHealth returns a copy of file whose overlay for service has the health
// check path, interval and timeout, with the port the overlay named. All three
// empty means no check settings: the overlay keeps only its port, or has no
// health at all. A service left with an empty overlay has no entry, as
// withDomains leaves it. file is not changed, and the copy has no secret value.
func withHealth(file compose.Request, service, path string, interval, timeout time.Duration) compose.Request {
	file = file.WithoutSecretValues() // its own Services map
	services := make(map[string]compose.Overlay, len(file.Services)+1)
	maps.Copy(services, file.Services)
	overlay := services[service]

	h := compose.Health{Path: path, Interval: interval, Timeout: timeout}
	if overlay.Health != nil {
		h.Port = overlay.Health.Port
	}
	if h == (compose.Health{}) {
		overlay.Health = nil
	} else {
		overlay.Health = &h
	}
	if overlay.Empty() {
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
