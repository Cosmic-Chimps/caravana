// Package nomadjob turns an app.App into a Nomad job, using the official
// Nomad API types. Jobs are submitted as JSON, so no HCL is ever written.
//
// Translate is pure: no I/O, no clock, no randomness. The same input always
// gives the same job, which golden-file tests check byte for byte. This is
// the only package that knows the shape of a Nomad job.
package nomadjob

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// Settings are per-installation values. They come from the control plane's
// configuration, never from an app.
type Settings struct {
	// Entrypoint is the Traefik entrypoint routers bind to, e.g. "websecure".
	Entrypoint string
	// CertResolver is the Traefik certificate resolver, e.g. "letsencrypt".
	// Empty means Traefik's default certificate, as in the local lab.
	CertResolver string
	// DefaultCPUMHz is used for services that don't set their own CPU.
	DefaultCPUMHz int
	// DefaultMemoryMB is used for services that don't set their own memory.
	DefaultMemoryMB int
}

// Result is a translated job, ready to register.
type Result struct {
	Job *api.Job
	// Warnings is about input that is supported but changed in meaning.
	// Always empty in Phase 1.
	Warnings []app.Warning
}

// Update strategy: one allocation at a time, healthy by its checks, and an
// automatic revert to the last good version when a deploy fails.
const (
	minHealthyTime   = 10 * time.Second
	healthyDeadline  = 3 * time.Minute
	progressDeadline = 10 * time.Minute
)

// shutdownDelay gives Traefik time to drop a route before its container stops.
const shutdownDelay = 5 * time.Second

// Health check defaults, used when a service doesn't set its own.
const (
	defaultCheckInterval = 10 * time.Second
	defaultCheckTimeout  = 2 * time.Second
)

// Nomad's own minimums (nomad/structs MinResources, minCheckInterval and
// minCheckTimeout). Checking them here fails the deploy before registration,
// with the field named.
const (
	minCPUMHz        = 1
	minMemoryMB      = 10
	minCheckDuration = time.Second
)

// traefikNameRE is a Traefik entrypoint or certificate resolver name. These
// end up inside tag values, where a ',' would mean a list.
var traefikNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Translate builds the Nomad job for an app and one of its deployments. On
// invalid input it returns a *app.FieldError naming the field, and no job.
//
// Checks run in this order and stop at the first problem: the settings, the
// model's rules (app.Validate), then Nomad's limits with defaults applied.
func Translate(a app.App, d app.Deployment, s Settings) (Result, error) {
	if err := validateSettings(s); err != nil {
		return Result{}, err
	}
	if err := app.Validate(a, d); err != nil {
		return Result{}, err
	}
	for i, svc := range a.Services {
		if err := checkNomadLimits(i, svc, s); err != nil {
			return Result{}, err
		}
	}
	return Result{Job: job(a, d, s)}, nil
}

func validateSettings(s Settings) error {
	if !traefikNameRE.MatchString(s.Entrypoint) {
		return app.NewFieldError("settings.entrypoint", "%q must be a Traefik entrypoint name (letters, digits, '-', '_')", s.Entrypoint)
	}
	if s.CertResolver != "" && !traefikNameRE.MatchString(s.CertResolver) {
		return app.NewFieldError("settings.certResolver", "%q must be empty or a Traefik resolver name (letters, digits, '-', '_')", s.CertResolver)
	}
	if s.DefaultCPUMHz < minCPUMHz {
		return app.NewFieldError("settings.defaultCPUMHz", "must be at least %d, got %d", minCPUMHz, s.DefaultCPUMHz)
	}
	if s.DefaultMemoryMB < minMemoryMB {
		return app.NewFieldError("settings.defaultMemoryMB", "must be at least %d, got %d", minMemoryMB, s.DefaultMemoryMB)
	}
	return nil
}

// checkNomadLimits checks what Nomad would reject at registration. CPU needs
// no check: app.Validate rejects negatives and 0 means the default, which
// validateSettings already checked.
func checkNomadLimits(i int, svc app.Service, s Settings) error {
	field := fmt.Sprintf("services[%d]", i)

	if mem := svc.Resources.MemoryMB; mem != 0 && mem < minMemoryMB {
		return app.NewFieldError(field+".resources.memoryMB", "must be at least %d MB, got %d", minMemoryMB, mem)
	}

	h := svc.Health
	if h == nil {
		return nil
	}
	interval := orDefault(h.Interval, defaultCheckInterval)
	timeout := orDefault(h.Timeout, defaultCheckTimeout)
	if interval < minCheckDuration {
		return app.NewFieldError(field+".health.interval", "must be at least %v, got %v", minCheckDuration, interval)
	}
	if timeout < minCheckDuration {
		return app.NewFieldError(field+".health.timeout", "must be at least %v, got %v", minCheckDuration, timeout)
	}
	if timeout >= interval {
		return app.NewFieldError(field+".health.timeout", "is %v, but must be shorter than the interval (%v)", timeout, interval)
	}
	return nil
}

func job(a app.App, d app.Deployment, s Settings) *api.Job {
	name := a.Name
	if name == "" {
		name = a.ID
	}

	j := &api.Job{
		ID:          new(a.ID),
		Name:        new(name),
		Type:        new("service"),
		Datacenters: []string{"*"},
		Meta: map[string]string{
			"caravana.app": a.ID,
			// A new deployment ID on every deploy makes every deploy a new
			// job version, even when nothing else changed.
			"caravana.deployment": d.ID,
		},
		Update: &api.UpdateStrategy{
			MaxParallel:      new(1),
			HealthCheck:      new("checks"),
			MinHealthyTime:   new(minHealthyTime),
			HealthyDeadline:  new(healthyDeadline),
			ProgressDeadline: new(progressDeadline),
			AutoRevert:       new(true),
		},
	}
	for _, svc := range a.Services {
		j.TaskGroups = append(j.TaskGroups, taskGroup(a, svc, d.Digests[svc.Name], s))
	}
	return j
}

func taskGroup(a app.App, svc app.Service, image string, s Settings) *api.TaskGroup {
	count := 1
	if svc.Replicas != nil {
		count = *svc.Replicas
	}

	// Nomad picks a free host port and maps it to the container port.
	var ports []api.Port
	for _, p := range svc.Ports {
		ports = append(ports, api.Port{Label: p.Name, To: p.ContainerPort})
	}

	tg := &api.TaskGroup{
		Name:          new(svc.Name),
		Count:         new(count),
		ShutdownDelay: new(shutdownDelay),
		Networks:      []*api.NetworkResource{{Mode: "bridge", DynamicPorts: ports}},
		Tasks:         []*api.Task{task(svc, image, s)},
	}
	if reg := registration(a, svc, s); reg != nil {
		tg.Services = []*api.Service{reg}
	}
	return tg
}

// registration registers the service with Nomad's own service discovery, so
// its health check can gate deploys. With domains, Traefik's Nomad provider
// reads the tags to build the route. Without domains the registration has no
// tags and stays internal: Traefik only routes services that opt in. A service
// without ports (a worker) gets no registration.
func registration(a app.App, svc app.Service, s Settings) *api.Service {
	if len(svc.Ports) == 0 {
		return nil
	}
	name := a.ID + "-" + svc.Name
	checkPort := healthPort(svc)

	reg := &api.Service{
		Name:      name,
		PortLabel: checkPort,
		Provider:  "nomad",
	}
	if len(svc.Domains) > 0 {
		reg.PortLabel = svc.Domains[0].Port
		reg.Tags = routerTags(name, svc.Domains, s)
	}
	reg.Checks = []api.ServiceCheck{healthCheck(*svc.Health, checkPort, reg.PortLabel)}
	return reg
}

// healthPort is the port the health check uses: its own, else the domains'
// port, else the service's only port (app.Validate guarantees one of these).
func healthPort(svc app.Service) string {
	switch {
	case svc.Health.Port != "":
		return svc.Health.Port
	case len(svc.Domains) > 0:
		return svc.Domains[0].Port
	default:
		return svc.Ports[0].Name
	}
}

// routerTags builds one Traefik router for all of a service's domains, with
// TLS always on. Without a certificate resolver, Traefik serves its default
// certificate (the local lab); with one, it gets a real certificate.
func routerTags(router string, domains []app.Domain, s Settings) []string {
	hosts := make([]string, 0, len(domains))
	for _, d := range domains {
		hosts = append(hosts, fmt.Sprintf("Host(`%s`)", d.Host))
	}
	prefix := "traefik.http.routers." + router
	tags := []string{
		"traefik.enable=true",
		prefix + ".rule=" + strings.Join(hosts, " || "),
		prefix + ".entrypoints=" + s.Entrypoint,
		prefix + ".tls=true",
	}
	if s.CertResolver != "" {
		tags = append(tags, prefix+".tls.certresolver="+s.CertResolver)
	}
	return tags
}

// healthCheck builds the check on checkPort for a registration on
// servicePort. Nomad checks the registration's port unless told otherwise, so
// the check names its port only when the two differ.
func healthCheck(h app.HealthCheck, checkPort, servicePort string) api.ServiceCheck {
	check := api.ServiceCheck{
		Name:     "health",
		Type:     "tcp",
		Interval: orDefault(h.Interval, defaultCheckInterval),
		Timeout:  orDefault(h.Timeout, defaultCheckTimeout),
	}
	if h.Path != "" {
		check.Type = "http"
		check.Path = h.Path
	}
	if checkPort != servicePort {
		check.PortLabel = checkPort
	}
	return check
}

func task(svc app.Service, image string, s Settings) *api.Task {
	config := map[string]any{"image": image}
	if svc.Command != "" {
		config["command"] = svc.Command
	}
	if len(svc.Args) > 0 {
		config["args"] = svc.Args
	}

	var env map[string]string // nil when there are none, so the job has no Env
	if len(svc.Env) > 0 {
		env = make(map[string]string, len(svc.Env))
		for _, e := range svc.Env {
			env[e.Name] = e.Value
		}
	}

	return &api.Task{
		Name:   svc.Name,
		Driver: "docker",
		Config: config,
		Env:    env,
		Resources: &api.Resources{
			CPU:      new(orDefault(svc.Resources.CPUMHz, s.DefaultCPUMHz)),
			MemoryMB: new(orDefault(svc.Resources.MemoryMB, s.DefaultMemoryMB)),
		},
	}
}

// orDefault returns v, or def when v is the zero value.
func orDefault[T comparable](v, def T) T {
	var zero T
	if v == zero {
		return def
	}
	return v
}
