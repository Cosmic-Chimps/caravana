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

// ServiceSettings is what Configure › Services changes of one service (feature 059): its health
// check and its copies and size. A nil Health, and a nil field of Size, leave that as it is.
type ServiceSettings struct {
	Health *HealthSettings
	Size   Size
}

// HealthSettings is a service's health check as Configure sets it. An empty Path with no Interval
// and no Timeout is Caravana's default check, a TCP connection to the service's port.
type HealthSettings struct {
	Path              string
	Interval, Timeout time.Duration
}

// Size is a service's copies and memory, in MB. Zero memory is Caravana's default.
type Size struct {
	Replicas, MemoryMB, MemoryMaxMB *int
}

// SetServiceIf deploys appID with service's health check and size set (feature 059), on the
// condition that the stored app still has the revision the caller read (ErrAppChanged otherwise),
// as DeployIf does. The check's port stays as it was. It is what Configure › Services saves for an
// app that keeps its Compose file; the API reaches the same result by deploying the app with the
// service changed.
//
// An app that keeps its Compose file keeps it. The health check goes into the overlay; the copies
// and memory that differ from the stored app are written into the file's text
// (compose.SetResources), which keeps the owner's comments and formatting. The app is deployed from
// the edited file, with no secret value in it, and its field errors name the file's paths
// ("services.web.health.interval"); a definition's name the app's.
//
// Errors: store.ErrNotFound for an unknown app, ErrAppStopped for a stopped one, an *app.FieldError
// on "service" for a service the app doesn't have or, when Health is set, one without a port (it
// has no health check), a *compose.EditError for a size the file can't take (a value behind an
// alias, or memory set back to the default, which would mean removing a key), and every error of a
// deploy.
func (s *Service) SetServiceIf(ctx context.Context, appID, service string, set ServiceSettings, cond Condition, trigger app.Trigger) (store.DeploymentRecord, error) {
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
	if set.Health != nil && len(rec.App.Services[i].Ports) == 0 {
		return store.DeploymentRecord{}, app.NewFieldError("service", "service %q has no port, so it has no health check", service)
	}

	if rec.Compose == nil {
		a := rec.App
		a.Services = slices.Clone(a.Services)
		svc := &a.Services[i]
		if h := set.Health; h != nil {
			health := app.HealthCheck{}
			if svc.Health != nil {
				health = *svc.Health // keeps its port
			}
			health.Path, health.Interval, health.Timeout = h.Path, h.Interval, h.Timeout
			svc.Health = &health
		}
		if n := set.Size.Replicas; n != nil {
			svc.Replicas = new(*n)
		}
		if n := set.Size.MemoryMB; n != nil {
			svc.Resources.MemoryMB = *n
		}
		if n := set.Size.MemoryMaxMB; n != nil {
			svc.Resources.MemoryMaxMB = *n
		}
		return recordOnly(s.saveAndDeploy(ctx, a, cond, trigger, made{action: audit.ActionUpdated}, nil))
	}

	file := rec.Compose.WithoutSecretValues()
	if h := set.Health; h != nil {
		file = withHealth(file, service, h.Path, h.Interval, h.Timeout)
	}
	size, err := sizeChange(rec.App.Services[i], set.Size)
	if err != nil {
		return store.DeploymentRecord{}, err
	}
	if size != (compose.Resources{}) {
		text, err := compose.SetResources(file.Compose, service, size)
		if err != nil {
			return store.DeploymentRecord{}, err
		}
		file.Compose = text
	}
	// The kept file has no secret values: each secret is deployed without one,
	// which keeps the value the secret store has.
	a, warnings, err := compose.Translate(appID, file)
	if err != nil {
		return store.DeploymentRecord{}, err
	}
	res, err := s.saveAndDeploy(ctx, a, cond, trigger, made{file: &file, warnings: warnings, action: audit.ActionUpdated}, nil)
	return res.DeploymentRecord, inFile(err, a, file)
}

// sizeChange is what of size differs from the stored service, as the file's resources: only those
// are written, so a value the file says its own way ("1g") is left as typed. Memory set back to
// zero, the default, would mean taking a key out of the file, which is refused.
func sizeChange(stored app.Service, size Size) (compose.Resources, error) {
	var r compose.Resources
	if n := size.Replicas; n != nil {
		count := 1
		if stored.Replicas != nil {
			count = *stored.Replicas
		}
		if *n != count {
			r.Replicas = new(*n)
		}
	}
	memory := func(n *int, current int, key string) (*int, error) {
		switch {
		case n == nil || *n == current:
			return nil, nil
		case *n == 0:
			return nil, &compose.EditError{Path: "services." + stored.Name + "." + key,
				Reason: "going back to the default means taking the key out of the file"}
		}
		return new(*n), nil
	}
	var err error
	if r.MemReservationMB, err = memory(size.MemoryMB, stored.Resources.MemoryMB, "mem_reservation"); err != nil {
		return compose.Resources{}, err
	}
	if r.MemLimitMB, err = memory(size.MemoryMaxMB, stored.Resources.MemoryMaxMB, "mem_limit"); err != nil {
		return compose.Resources{}, err
	}
	return r, nil
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
