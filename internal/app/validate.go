package app

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// Patterns are compiled once, when the program starts. MustCompile panics on
// a bad pattern, which can only happen at startup with these constants.
var (
	// labelRE is a lowercase DNS label (RFC 1123): app IDs, service names
	// and each part of a hostname.
	labelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

	// portNameRE avoids "-", because Nomad exposes ports as env vars named
	// NOMAD_PORT_<label>.
	portNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

	// digestRE is an image reference pinned by digest and without a tag:
	// an optional registry host (and port), a lowercase path, @sha256: and
	// 64 lowercase hex characters.
	digestRE = regexp.MustCompile(
		`^(?:[a-zA-Z0-9.-]+(?::[0-9]+)?/)?` +
			`[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*` +
			`(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*` +
			`@sha256:[a-f0-9]{64}$`)
)

const (
	maxLabelLen = 63  // a DNS label, and Nomad's limit for service names
	maxHostLen  = 253 // a full hostname
)

// Validate checks an App and its Deployment against the model's rules and
// returns the first problem as a *FieldError, or nil.
//
// The order is fixed, so the same input always reports the same error:
//  1. id, name, source.kind, services (at least one)
//  2. each service in order: name, image, replicas, resources, env, ports,
//     domains, health
//  3. deployment: id, trigger, a digest for each service in order, then any
//     extra digest keys in sorted order
//
// Rules that depend on Nomad (minimum resources and check durations) live in
// package nomadjob, not here.
func Validate(a App, d Deployment) error {
	if err := ValidateApp(a); err != nil {
		return err
	}
	return validateDeployment(a, d)
}

// ValidateApp checks an App on its own: steps 1 and 2 of Validate's order.
// The deploy flow uses it before any image is resolved, when there is no
// deployment yet.
func ValidateApp(a App) error {
	if !labelRE.MatchString(a.ID) {
		return NewFieldError("id", "%q is not a lowercase DNS label (a-z, 0-9 and '-', at most 63 characters)", a.ID)
	}
	if strings.ContainsFunc(a.Name, unicode.IsControl) {
		return NewFieldError("name", "must not contain control characters")
	}
	if a.Source.Kind != SourceImage {
		return NewFieldError("source.kind", "%q is not supported; only %q is", a.Source.Kind, SourceImage)
	}
	if len(a.Services) == 0 {
		return NewFieldError("services", "an app needs at least one service")
	}

	names := map[string]bool{}
	hosts := map[string]bool{} // across the whole app: one host, one router
	for i, svc := range a.Services {
		if err := validateService(a.ID, i, svc, names, hosts); err != nil {
			return err
		}
	}
	return nil
}

// validateService checks one service. names and hosts collect what earlier
// services used, to catch duplicates.
func validateService(appID string, i int, svc Service, names, hosts map[string]bool) error {
	field := fmt.Sprintf("services[%d]", i)

	if !labelRE.MatchString(svc.Name) {
		return NewFieldError(field+".name", "%q is not a lowercase DNS label (a-z, 0-9 and '-', at most 63 characters)", svc.Name)
	}
	if names[svc.Name] {
		return NewFieldError(field+".name", "service %q is declared twice", svc.Name)
	}
	names[svc.Name] = true
	if n := len(appID) + 1 + len(svc.Name); n > maxLabelLen {
		return NewFieldError(field+".name", "%s-%s is %d characters; the Nomad service name allows at most %d", appID, svc.Name, n, maxLabelLen)
	}

	if svc.Image == "" {
		return NewFieldError(field+".image", "is required")
	}
	if strings.ContainsFunc(svc.Image, unicode.IsSpace) {
		return NewFieldError(field+".image", "must not contain spaces")
	}

	if svc.Replicas != nil && *svc.Replicas < 1 {
		return NewFieldError(field+".replicas", "must be at least 1, got %d (leave it out for 1)", *svc.Replicas)
	}
	if svc.Resources.CPUMHz < 0 {
		return NewFieldError(field+".resources.cpuMHz", "must not be negative")
	}
	if svc.Resources.MemoryMB < 0 {
		return NewFieldError(field+".resources.memoryMB", "must not be negative")
	}

	if err := validateEnv(field, svc.Env); err != nil {
		return err
	}
	ports, err := validatePorts(field, svc.Ports)
	if err != nil {
		return err
	}
	if err := validateDomains(field, svc.Domains, ports, hosts); err != nil {
		return err
	}
	return validateHealth(field, svc, ports)
}

func validateEnv(field string, env []EnvVar) error {
	seen := map[string]bool{}
	for j, e := range env {
		f := fmt.Sprintf("%s.env[%d].name", field, j)
		switch {
		case e.Name == "":
			return NewFieldError(f, "is required")
		case strings.ContainsAny(e.Name, "=\x00"):
			return NewFieldError(f, "%q must not contain '=' or a NUL byte", e.Name)
		case seen[e.Name]:
			return NewFieldError(f, "%q is set twice", e.Name)
		}
		seen[e.Name] = true
	}
	return nil
}

// validatePorts checks the ports and returns the set of declared names.
func validatePorts(field string, ports []Port) (map[string]bool, error) {
	names := map[string]bool{}
	for j, p := range ports {
		f := fmt.Sprintf("%s.ports[%d]", field, j)
		if !portNameRE.MatchString(p.Name) {
			return nil, NewFieldError(f+".name", "%q must start with a lowercase letter, then lowercase letters, digits or '_'", p.Name)
		}
		if names[p.Name] {
			return nil, NewFieldError(f+".name", "port %q is declared twice", p.Name)
		}
		names[p.Name] = true
		if p.ContainerPort < 1 || p.ContainerPort > 65535 {
			return nil, NewFieldError(f+".containerPort", "must be between 1 and 65535, got %d", p.ContainerPort)
		}
	}
	return names, nil
}

func validateDomains(field string, domains []Domain, ports, hosts map[string]bool) error {
	for j, dom := range domains {
		f := fmt.Sprintf("%s.domains[%d]", field, j)
		if !validHost(dom.Host) {
			return NewFieldError(f+".host", "%q is not a lowercase hostname", dom.Host)
		}
		if hosts[dom.Host] {
			return NewFieldError(f+".host", "%q is already used in this app", dom.Host)
		}
		hosts[dom.Host] = true

		if !ports[dom.Port] {
			return NewFieldError(f+".port", "%q is not a port of this service", dom.Port)
		}
		// One router per service, so every domain must go to the same port.
		if dom.Port != domains[0].Port {
			return NewFieldError(f+".port", "is %q, but domains[0] uses %q; all domains of a service must use the same port", dom.Port, domains[0].Port)
		}
	}
	return nil
}

// validHost reports whether h is a lowercase hostname. This also keeps
// backticks, spaces and "||" out of the Traefik router rule.
func validHost(h string) bool {
	if h == "" || len(h) > maxHostLen {
		return false
	}
	for label := range strings.SplitSeq(h, ".") {
		if !labelRE.MatchString(label) {
			return false
		}
	}
	return true
}

func validateHealth(field string, svc Service, ports map[string]bool) error {
	h := svc.Health
	f := field + ".health"
	switch {
	case h == nil && len(svc.Ports) > 0:
		return NewFieldError(f, "is required: the service has ports, so deploys need a health check")
	case h == nil:
		return nil
	case len(svc.Ports) == 0:
		return NewFieldError(f, "needs a port to check, but the service has none")
	}

	if h.Path != "" && !strings.HasPrefix(h.Path, "/") {
		return NewFieldError(f+".path", "%q must start with '/'", h.Path)
	}
	switch {
	case h.Port != "" && !ports[h.Port]:
		return NewFieldError(f+".port", "%q is not a port of this service", h.Port)
	case h.Port == "" && len(svc.Domains) == 0 && len(svc.Ports) > 1:
		return NewFieldError(f+".port", "is required: the service has several ports and no domains")
	}
	if h.Interval < 0 {
		return NewFieldError(f+".interval", "must not be negative")
	}
	if h.Timeout < 0 {
		return NewFieldError(f+".timeout", "must not be negative")
	}
	return nil
}

func validateDeployment(a App, d Deployment) error {
	if d.ID == "" {
		return NewFieldError("deployment.id", "is required")
	}
	if d.Trigger != TriggerManual && d.Trigger != TriggerWebhook {
		return NewFieldError("deployment.trigger", "%q is not %q or %q", d.Trigger, TriggerManual, TriggerWebhook)
	}

	services := map[string]bool{}
	for _, svc := range a.Services {
		services[svc.Name] = true
		f := "deployment.digests[" + svc.Name + "]"
		digest, ok := d.Digests[svc.Name]
		if !ok {
			return NewFieldError(f, "no resolved image for service %q", svc.Name)
		}
		if !digestRE.MatchString(digest) {
			return NewFieldError(f, "%q is not pinned by digest (repo@sha256:<64 hex>, no tag)", digest)
		}
	}
	// Sorted, so the reported key doesn't depend on map order.
	for _, name := range slices.Sorted(maps.Keys(d.Digests)) {
		if !services[name] {
			return NewFieldError("deployment.digests["+name+"]", "%q is not a service of this app", name)
		}
	}
	return nil
}
