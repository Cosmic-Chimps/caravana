package app

import (
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hashicorp/cronexpr"
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

	// secretNameRE is stricter than a plain env name: secrets reach the
	// container through an env file, whose parser accepts only these keys.
	secretNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

	// volumeNameRE has no uppercase and no "." so that "<app>_<volume>" is a
	// valid Nomad volume name, and the first "_" in it always ends the app ID.
	volumeNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

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
//  2. each service in order: name, image, replicas, resources, env, secrets,
//     ports, domains, health, then volumes
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
	if a.Source.Kind != SourceImage && a.Source.Kind != SourceCompose {
		return NewFieldError("source.kind", "%q is not supported; only %q and %q are", a.Source.Kind, SourceImage, SourceCompose)
	}
	if len(a.Services) == 0 {
		return NewFieldError("services", "an app needs at least one service")
	}

	names := map[string]bool{}
	hosts := map[string]bool{}   // across the whole app: one host, one router
	volumes := map[string]bool{} // across the whole app: one volume, one service
	for i, svc := range a.Services {
		if err := validateService(a.ID, i, svc, names, hosts); err != nil {
			return err
		}
		if err := validateVolumes(fmt.Sprintf("services[%d]", i), svc, volumes); err != nil {
			return err
		}
		if err := validateBackup(fmt.Sprintf("services[%d].backup", i), svc); err != nil {
			return err
		}
	}
	return validateRedirectPairs(a)
}

// validateBackup checks a service's backup policy. What needs the
// installation (a destination) is checked by the deploy flow.
func validateBackup(field string, svc Service) error {
	b := svc.Backup
	if b == nil {
		return nil
	}
	if b.Engine != BackupPostgres && b.Engine != BackupVolume {
		return NewFieldError(field+".engine", "%q is not supported; it is %q or %q", b.Engine, BackupPostgres, BackupVolume)
	}
	if b.Keep < 0 || b.Keep > 100 {
		return NewFieldError(field+".keep", "must be between 1 and 100 (leave it out for %d), got %d", DefaultBackupKeep, b.Keep)
	}
	if b.Schedule != "" {
		if fields := strings.Fields(b.Schedule); len(fields) != 5 || strings.ContainsFunc(b.Schedule, unicode.IsControl) {
			return NewFieldError(field+".schedule", "%q must be a five-field cron expression, e.g. %q", b.Schedule, DefaultBackupSchedule)
		}
	}
	if b.Engine == BackupVolume {
		return validateVolumeBackup(field, svc)
	}
	if len(svc.Ports) == 0 {
		return NewFieldError(field, "the backup reaches the database over its port, and the service has none")
	}
	has := slices.ContainsFunc(svc.Env, func(e EnvVar) bool { return e.Name == "POSTGRES_PASSWORD" }) ||
		slices.ContainsFunc(svc.Secrets, func(s Secret) bool { return s.Name == "POSTGRES_PASSWORD" })
	if !has {
		return NewFieldError(field, "the backup logs in with POSTGRES_PASSWORD; set it in the service's environment or secrets")
	}
	return nil
}

// NoVolumeToBackUp is the message of the refusal of a volume backup on a
// service that mounts no volume. It is a constant so that the console, which
// words the refusal for its own form, recognizes it.
const NoVolumeToBackUp = "the service has no volume to back up"

// validateVolumeBackup checks what only a volume backup needs: there must be a
// volume to archive, and the schedule must be one Caravana's own scheduler can
// run (the five-field check above lets "99 * * * *" through; Postgres backups
// leave that to Nomad, which refuses it at register time).
//
// A volume shared with another service is refused too, but earlier, by
// validateVolumes: each volume belongs to one service, so a volume backup
// never has to share its volume.
func validateVolumeBackup(field string, svc Service) error {
	if len(svc.Volumes) == 0 {
		return NewFieldError(field+".engine", "%s", NoVolumeToBackUp)
	}
	if svc.Backup.Schedule != "" {
		if _, err := cronexpr.Parse(svc.Backup.Schedule); err != nil {
			return NewFieldError(field+".schedule", "%q is not a valid cron expression: %v", svc.Backup.Schedule, err)
		}
	}
	return nil
}

// validateVolumes checks a service's volumes. used collects the names
// earlier services mount: each volume belongs to one service.
func validateVolumes(field string, svc Service, used map[string]bool) error {
	paths := map[string]bool{}
	for j, v := range svc.Volumes {
		f := fmt.Sprintf("%s.volumes[%d]", field, j)
		switch {
		case !volumeNameRE.MatchString(v.Name):
			return NewFieldError(f+".name", "%q must be lowercase letters, digits, '-' and '_', starting with a letter or digit, at most 63 characters", v.Name)
		case used[v.Name]:
			return NewFieldError(f+".name", "volume %q is already mounted by another service; each volume belongs to one service", v.Name)
		case !path.IsAbs(v.Path) || path.Clean(v.Path) != v.Path || v.Path == "/":
			return NewFieldError(f+".path", "%q must be a clean absolute path, not /", v.Path)
		case paths[v.Path]:
			return NewFieldError(f+".path", "%q is mounted twice", v.Path)
		}
		used[v.Name], paths[v.Path] = true, true
	}
	if len(svc.Volumes) > 0 && svc.Replicas != nil && *svc.Replicas > 1 {
		return NewFieldError(field+".replicas", "a service with a volume runs one copy, got %d", *svc.Replicas)
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
	if svc.Name == ReservedServiceName {
		return NewFieldError(field+".name", "%q is reserved: Caravana runs a task of that name beside every service with a volume", svc.Name)
	}
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
	if svc.Resources.MemoryMaxMB < 0 {
		return NewFieldError(field+".resources.memoryMaxMB", "must not be negative")
	}
	if r := svc.Resources; r.MemoryMB > 0 && r.MemoryMaxMB > 0 && r.MemoryMB > r.MemoryMaxMB {
		return NewFieldError(field+".resources.memoryMB", "reserves %d MB, more than the service's ceiling of %d MB (memoryMaxMB)", r.MemoryMB, r.MemoryMaxMB)
	}

	if err := validateEntrypoint(field, svc.Entrypoint); err != nil {
		return err
	}
	envNames, err := validateEnv(field, svc.Env)
	if err != nil {
		return err
	}
	if err := validateSecrets(field, svc.Secrets, envNames); err != nil {
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

// Limits of an entrypoint: generous for any real one, small enough that a
// request can't make a job body of any size.
const (
	maxEntrypointItems = 64
	maxEntrypointBytes = 4096
)

// validateEntrypoint checks a service's entrypoint list.
func validateEntrypoint(field string, entrypoint []string) error {
	if len(entrypoint) > maxEntrypointItems {
		return NewFieldError(field+".entrypoint", "has %d items; at most %d are allowed", len(entrypoint), maxEntrypointItems)
	}
	for j, item := range entrypoint {
		f := fmt.Sprintf("%s.entrypoint[%d]", field, j)
		switch {
		case item == "":
			return NewFieldError(f, "must not be empty")
		case len(item) > maxEntrypointBytes:
			return NewFieldError(f, "is %d bytes; at most %d are allowed", len(item), maxEntrypointBytes)
		case strings.ContainsRune(item, 0):
			return NewFieldError(f, "must not contain a NUL byte")
		}
	}
	return nil
}

// validateEnv checks the plain env and returns the names it sets.
func validateEnv(field string, env []EnvVar) (map[string]bool, error) {
	seen := map[string]bool{}
	for j, e := range env {
		f := fmt.Sprintf("%s.env[%d].name", field, j)
		switch {
		case e.Name == "":
			return nil, NewFieldError(f, "is required")
		case strings.ContainsAny(e.Name, "=\x00"):
			return nil, NewFieldError(f, "%q must not contain '=' or a NUL byte", e.Name)
		case seen[e.Name]:
			return nil, NewFieldError(f, "%q is set twice", e.Name)
		}
		seen[e.Name] = true
	}
	return seen, nil
}

// validateSecrets checks the secrets' names against each other and the plain
// env, and their values. Messages never include a value.
func validateSecrets(field string, secrets []Secret, envNames map[string]bool) error {
	seen := map[string]bool{}
	for j, s := range secrets {
		f := fmt.Sprintf("%s.secrets[%d]", field, j)
		switch {
		case !secretNameRE.MatchString(s.Name):
			return NewFieldError(f+".name", "%q must be letters, digits and '_', not starting with a digit", s.Name)
		case seen[s.Name]:
			return NewFieldError(f+".name", "%q is set twice", s.Name)
		case envNames[s.Name]:
			return NewFieldError(f+".name", "%q is also a plain env var of this service", s.Name)
		}
		seen[s.Name] = true
		if s.Value == nil {
			continue
		}
		if !utf8.ValidString(*s.Value) {
			return NewFieldError(f+".value", "must be UTF-8 text")
		}
		if strings.ContainsRune(*s.Value, 0) {
			return NewFieldError(f+".value", "must not contain a NUL byte")
		}
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

		if err := validateDomainOptions(f, dom); err != nil {
			return err
		}

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

// maxDomainPathLen is the longest path prefix a domain may have.
const maxDomainPathLen = 200

// validateDomainOptions checks a domain's path and redirect option. That the
// redirect's pair isn't another domain of the app needs every host, so
// validateRedirectPairs does it once all services are read.
func validateDomainOptions(field string, dom Domain) error {
	if p := dom.Path; p != "" {
		switch {
		case p[0] != '/':
			return NewFieldError(field+".path", "%q must start with /", p)
		case len(p) > maxDomainPathLen:
			return NewFieldError(field+".path", "is %d bytes; at most %d are allowed", len(p), maxDomainPathLen)
		case strings.ContainsAny(p, "` \x00") || strings.Contains(p, "||") || strings.Contains(p, "&&"):
			return NewFieldError(field+".path", "must not contain a backtick, space, NUL, || or &&")
		}
	}
	if dom.WWWRedirect && dom.RedirectPair() == "" {
		return NewFieldError(field+".wwwRedirect", "only an apex host (example.com) or a www. host can redirect its pair, and %q is neither", dom.Host)
	}
	return nil
}

// validateRedirectPairs refuses a wwwRedirect whose pair is already a domain of
// this app: the pair would be routed twice.
func validateRedirectPairs(a App) error {
	hosts := map[string]bool{}
	for _, svc := range a.Services {
		for _, dom := range svc.Domains {
			hosts[dom.Host] = true
		}
	}
	for i, svc := range a.Services {
		for j, dom := range svc.Domains {
			if pair := dom.RedirectPair(); dom.WWWRedirect && hosts[pair] {
				return NewFieldError(fmt.Sprintf("services[%d].domains[%d].wwwRedirect", i, j),
					"%q would redirect to %q, which is already a domain of this app", pair, dom.Host)
			}
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
	if d.Trigger != TriggerManual && d.Trigger != TriggerWebhook && d.Trigger != TriggerConsole {
		return NewFieldError("deployment.trigger", "%q is not %q, %q or %q", d.Trigger, TriggerManual, TriggerWebhook, TriggerConsole)
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
