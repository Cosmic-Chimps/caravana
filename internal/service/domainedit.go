package service

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
)

// SetDomainsIf deploys appID with service's domains replaced by domains (with
// their options: path, wwwRedirect, teamOnly), on the condition that the
// stored app still has the revision the caller read (ErrAppChanged
// otherwise), as DeployIf does. It is what the Domains page saves.
//
// An app that keeps its Compose file is edited through the file: its
// overlay's domains for service are rewritten (as secret edits rewrite its
// secret names) and the app is deployed from the edited file, which stays
// kept, with no secret value in it. Its field errors then name the file's
// paths ("services.web.domains[1].path"); a definition's name the app's
// ("services[0].domains[1].path").
//
// The answer has the password of each domain the deploy turned Team only, the
// one time it is shown. Errors: store.ErrNotFound for an unknown app,
// ErrAppStopped for a stopped one, an *app.FieldError on "service" for a
// service the app doesn't have, and every error of a deploy.
func (s *Service) SetDomainsIf(ctx context.Context, appID, service string, domains []app.Domain, cond Condition, trigger app.Trigger) (DeployResult, error) {
	rec, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return DeployResult{}, err
	}
	if rec.StoppedAt != nil {
		return DeployResult{}, fmt.Errorf("app %q: %w", appID, ErrAppStopped)
	}
	i := slices.IndexFunc(rec.App.Services, func(svc app.Service) bool { return svc.Name == service })
	if i < 0 {
		return DeployResult{}, app.NewFieldError("service", "app %q has no service %q", appID, service)
	}
	domains = slices.Clone(domains)
	for k := range domains {
		domains[k].Host = normalizeHost(domains[k].Host)
	}

	if rec.Compose == nil {
		a := rec.App
		a.Services = slices.Clone(a.Services)
		a.Services[i].Domains = domains
		return s.saveAndDeploy(ctx, a, cond, trigger, made{}, nil)
	}

	file, err := withDomains(*rec.Compose, service, domains, rec.App.Services[i].Ports)
	if err != nil {
		return DeployResult{}, err
	}
	// The kept file has no secret values: each secret is deployed without one,
	// which keeps the value the secret store has.
	a, warnings, err := compose.Translate(appID, file)
	if err != nil {
		return DeployResult{}, err
	}
	res, err := s.saveAndDeploy(ctx, a, cond, trigger, made{file: &file, warnings: warnings}, nil)
	return res, inFile(err, a)
}

// withDomains returns a copy of file whose overlay for service has domains in
// place of its own, each with the container port its port name has in ports.
// A domain whose port is the service's only one keeps the overlay's short form
// (no port), unless the overlay named it. A port name ports doesn't have is an
// *app.FieldError on that domain's port. A service left with an empty overlay
// has no entry at all, as withSecretNames does. file is not changed, and the
// copy has no secret value.
func withDomains(file compose.Request, service string, domains []app.Domain, ports []app.Port) (compose.Request, error) {
	file = file.WithoutSecretValues() // its own Services map
	services := make(map[string]compose.Overlay, len(file.Services)+1)
	maps.Copy(services, file.Services)
	overlay := services[service]

	before := map[string]int{} // host → the port the overlay gave it
	for _, d := range overlay.Domains {
		before[d.Host] = d.Port
	}
	var out []compose.Domain
	for k, d := range domains {
		port := 0
		if d.Port != "" || len(ports) != 1 {
			j := slices.IndexFunc(ports, func(p app.Port) bool { return p.Name == d.Port })
			if j < 0 {
				return compose.Request{}, app.NewFieldError(fmt.Sprintf("domains[%d].port", k),
					"service %q has no port %q", service, d.Port)
			}
			port = ports[j].ContainerPort
		}
		if len(ports) == 1 && before[d.Host] == 0 {
			port = 0 // the service's only port, as the overlay wrote it
		}
		out = append(out, compose.Domain{Host: d.Host, Port: port, Path: d.Path, WWWRedirect: d.WWWRedirect, TeamOnly: d.TeamOnly})
	}
	overlay.Domains = out
	if len(overlay.Domains) == 0 && overlay.Health == nil && len(overlay.Secrets) == 0 && overlay.Backup == nil {
		delete(services, service)
	} else {
		services[service] = overlay
	}
	file.Services = services
	if len(services) == 0 {
		file.Services = nil
	}
	return file, nil
}
