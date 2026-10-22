package ui

import (
	"regexp"
	"slices"
	"strconv"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// applyEdit is the stored app with what the form sends laid over it. It
// is pure: stored is not changed, and the result is a new app. Per service, by
// name, it replaces only the fields the form offers, as a whole: image, the
// container port of a one-port service, copies, the health check's path,
// interval and timeout, and the resources. Everything else stays as stored:
// the app's name and source, the service list and its order, environment
// variables and domains (they have their own pages), command, args, entrypoint,
// secrets (names; the values live in the secret store), volumes, backup, the
// other ports and the health check's port (FR-003). A service the form has no
// section for is left alone.
func applyEdit(stored app.App, f editForm) app.App {
	out := stored
	out.Services = slices.Clone(stored.Services)
	for i, svc := range out.Services {
		s, ok := f.service(svc.Name)
		if !ok {
			continue
		}
		e := s.Edit
		svc.Image = e.Image
		if s.ShowPort && len(svc.Ports) == 1 {
			svc.Ports = slices.Clone(svc.Ports)
			svc.Ports[0].ContainerPort = e.Port
		}
		svc.Replicas = e.Replicas
		if s.ShowHealth {
			health := app.HealthCheck{}
			if svc.Health != nil {
				health = *svc.Health // keeps its port
			}
			health.Path, health.Interval, health.Timeout = e.HealthPath, e.HealthInterval, e.HealthTimeout
			svc.Health = &health
		}
		svc.Resources = app.Resources{CPUMHz: e.CPUMHz, MemoryMB: e.MemoryMB, MemoryMaxMB: e.MemoryMaxMB}
		out.Services[i] = svc
	}
	return out
}

// pathRE splits the model's field paths the form can place:
// services[<i>].<rest>.
var pathRE = regexp.MustCompile(`^services\[(\d+)\]\.(.+)$`)

// fieldFor is the input an error about a model field path belongs to, as its
// posted name (svc.<service>.<field>), for the app a is deployed as. ok is
// false for a path the form has no input for (a volume, a secret, a backup, an
// environment variable or a domain, a service's own name, the port of a health
// check, ...), and for a path of a service whose section doesn't show that
// input (the port of a service with several ports): the caller then names the
// path in the form's notice.
func fieldFor(path string, a app.App) (name string, ok bool) {
	m := pathRE.FindStringSubmatch(path)
	if m == nil {
		return "", false
	}
	i, err := strconv.Atoi(m[1])
	if err != nil || i >= len(a.Services) {
		return "", false
	}
	svc := a.Services[i]
	s := newEditService(svc)

	field := ""
	switch rest := m[2]; {
	case rest == "image":
		field = "image"
	case hasPathPrefix(rest, "ports") && s.ShowPort:
		field = "port"
	case rest == "replicas":
		field = "copies"
	case (rest == "health" || rest == "health.path") && s.ShowHealth:
		field = "healthPath"
	case rest == "health.interval" && s.ShowHealth:
		field = "healthInterval"
	case rest == "health.timeout" && s.ShowHealth:
		field = "healthTimeout"
	case rest == "resources.cpuMHz":
		field = "cpu"
	case rest == "resources.memoryMB":
		field = "memory"
	case rest == "resources.memoryMaxMB":
		field = "memoryMax"
	default:
		return "", false
	}
	return inputName(svc.Name, field), true
}

// hasPathPrefix reports whether path is name, or goes inside it
// (name[0].value, name.x), but not a longer word that merely starts with it.
func hasPathPrefix(path, name string) bool {
	if len(path) < len(name) || path[:len(name)] != name {
		return false
	}
	return len(path) == len(name) || path[len(name)] == '[' || path[len(name)] == '.'
}
