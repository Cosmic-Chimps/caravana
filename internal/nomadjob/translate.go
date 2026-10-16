// Package nomadjob turns an app.App into a Nomad job, using the official
// Nomad API types. Jobs are submitted as JSON; HCL is written only to export
// an app as a jobspec (HCL).
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
	"github.com/cosmic-chimps/caravana/internal/registry"
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
	// DefaultMemoryMaxMB is the memory ceiling of services that don't set
	// their own: they may use up to the larger of it and their reservation
	// while the node has memory spare. It is at least DefaultMemoryMB.
	DefaultMemoryMaxMB int
	// NamesImage is the image of the sidecar that lets the services of an app
	// reach each other by name. It must be pinned by digest. Empty means
	// DefaultNamesImage.
	NamesImage string
	// BackupImage is the image that uploads and downloads backups (rclone).
	// It must be pinned by digest. Empty means DefaultBackupImage.
	BackupImage string
}

// DefaultBackupImage is rclone 1.71, pinned by digest (an index, so it runs
// on amd64 and arm64).
const DefaultBackupImage = "rclone/rclone@sha256:3103526c506266a9ecdf064efe99bf3677d92ef6407af124d8c56b4f49cbaa51"

// backupImage is the backup tool image these settings use.
func (s Settings) backupImage() string {
	if s.BackupImage == "" {
		return DefaultBackupImage
	}
	return s.BackupImage
}

// DefaultNamesImage is busybox 1.37, pinned by digest (an index, so it runs
// on amd64 and arm64).
const DefaultNamesImage = "busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"

// pinnedImageRE is an image reference pinned by digest, without a tag.
var pinnedImageRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:-]*@sha256:[a-f0-9]{64}$`)

// namesImage is the sidecar image these settings use.
func (s Settings) namesImage() string {
	if s.NamesImage == "" {
		return DefaultNamesImage
	}
	return s.NamesImage
}

// Result is a translated job, ready to register.
type Result struct {
	Job *api.Job
	// Warnings is about input that is supported but changed in meaning.
	// Always empty in Phase 1.
	Warnings []app.Warning
}

// Update strategy: blue/green with canaries. Each deploy starts a full set of
// new allocations (one canary per replica) next to the running ones. Traefik
// doesn't route to canaries (see canaryTags), so the old version keeps
// serving every request until the new one is healthy by its checks; then
// Nomad promotes it automatically and stops the old allocations. A deploy
// that never gets healthy is reverted to the last good version, and the old
// allocations never stopped serving.
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
// private is the set of registry hosts ("ghcr.io", "registry.example.com:5000",
// "docker.io") Caravana holds credentials for, true for each. A service whose
// image comes from one of them pulls with those credentials (see pullAuth).
// It is an argument, not a Setting: it belongs to the moment of the deploy,
// not to the installation's configuration. nil means none, and gives the job
// every service had before private registries existed.
//
// Checks run in this order and stop at the first problem: the settings, the
// model's rules (app.ValidateApp), Nomad's limits with defaults applied, then
// the deployment's rules (app.Validate).
func Translate(a app.App, d app.Deployment, s Settings, private map[string]bool) (Result, error) {
	if err := Check(a, s); err != nil {
		return Result{}, err
	}
	if err := app.Validate(a, d); err != nil {
		return Result{}, err
	}
	return Result{Job: job(a, d, s, private)}, nil
}

// Check runs every rule Translate runs that doesn't need a deployment: the
// settings, the app's own rules and Nomad's limits. The deploy flow calls it
// before saving an app, so input Nomad would refuse never gets that far. Like
// Translate, it is pure.
func Check(a app.App, s Settings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if err := app.ValidateApp(a); err != nil {
		return err
	}
	for i, svc := range a.Services {
		if err := checkNomadLimits(i, svc, s); err != nil {
			return err
		}
	}
	return checkNames(a)
}

// Validate checks the settings with the same rules Translate applies, and
// returns a *app.FieldError naming the setting (e.g. "settings.entrypoint").
// The control plane's config uses it at startup.
func (s Settings) Validate() error {
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
	if s.DefaultMemoryMaxMB < s.DefaultMemoryMB {
		return app.NewFieldError("settings.defaultMemoryMaxMB", "must be at least the default reservation (%d MB), got %d", s.DefaultMemoryMB, s.DefaultMemoryMaxMB)
	}
	if s.NamesImage != "" && !pinnedImageRE.MatchString(s.NamesImage) {
		return app.NewFieldError("settings.namesImage", "%q must be an image pinned by digest (repo@sha256:...), without a tag", s.NamesImage)
	}
	if s.BackupImage != "" && !pinnedImageRE.MatchString(s.BackupImage) {
		return app.NewFieldError("settings.backupImage", "%q must be an image pinned by digest (repo@sha256:...), without a tag", s.BackupImage)
	}
	return nil
}

// checkNomadLimits checks what Nomad would reject at registration. CPU needs
// no check: app.Validate rejects negatives and 0 means the default, which
// Settings.Validate already checked.
func checkNomadLimits(i int, svc app.Service, s Settings) error {
	field := fmt.Sprintf("services[%d]", i)

	if mem := svc.Resources.MemoryMB; mem != 0 && mem < minMemoryMB {
		return app.NewFieldError(field+".resources.memoryMB", "must be at least %d MB, got %d", minMemoryMB, mem)
	}
	if mem := svc.Resources.MemoryMaxMB; mem != 0 && mem < minMemoryMB {
		return app.NewFieldError(field+".resources.memoryMaxMB", "must be at least %d MB, got %d", minMemoryMB, mem)
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

func job(a app.App, d app.Deployment, s Settings, private map[string]bool) *api.Job {
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
			AutoPromote:      new(true),
		},
	}
	for _, svc := range a.Services {
		j.TaskGroups = append(j.TaskGroups, taskGroup(a, svc, d.Digests[svc.Name], s, private))
	}
	return j
}

func taskGroup(a app.App, svc app.Service, image string, s Settings, private map[string]bool) *api.TaskGroup {
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
		Name:  new(svc.Name),
		Count: new(count),
		// One canary per replica: the whole new version runs beside the old
		// one before any old allocation stops. Nomad merges this with the
		// job's update block.
		Update:        &api.UpdateStrategy{Canary: new(count)},
		ShutdownDelay: new(shutdownDelay),
		Networks:      []*api.NetworkResource{{Mode: "bridge", DynamicPorts: ports}},
		// The service's own task is always the first.
		Tasks: []*api.Task{task(a.ID, svc, image, s, private)},
	}
	if len(svc.Volumes) > 0 {
		tg.Volumes = volumeRequests(a.ID, svc)
		// A canary can't be placed on a single-writer volume the old copy
		// still holds, so these groups stop the old copy, then start the new
		// one. Still health-checked and reverted like any other deploy. A
		// group without canaries can't auto-promote (Nomad refuses the job),
		// so this also turns off the job's AutoPromote for this group.
		tg.Update = &api.UpdateStrategy{Canary: new(0), AutoPromote: new(false)}
	}
	if reg := registration(a, svc, s); reg != nil {
		tg.Services = []*api.Service{reg}
	}
	if sharesNames(a) {
		if len(svc.Ports) > 0 {
			tg.Services = append(tg.Services, internalRegistration(a.ID, svc))
		}
		tg.Tasks = append(tg.Tasks, namesTask(a, s))
	}
	if len(svc.Volumes) > 0 {
		tg.Tasks = append(tg.Tasks, volumeTask(a.ID, svc, s))
	}
	return tg
}

// volumeRequests asks Nomad for each of a service's volumes, keyed by the
// volume's name, which the task's mounts refer to. Each is the app's dynamic
// host volume, held by one allocation at a time.
//
// The request is writable, even for a volume the service mounts read-only,
// when the service has a volume backup: the caravana-volumes task has to be
// able to restore into it. The service's own mount keeps its read_only, so
// the service sees no change.
func volumeRequests(appID string, svc app.Service) map[string]*api.VolumeRequest {
	reqs := make(map[string]*api.VolumeRequest, len(svc.Volumes))
	for _, v := range svc.Volumes {
		reqs[v.Name] = &api.VolumeRequest{
			Name:           v.Name,
			Type:           "host",
			Source:         VolumeName(appID, v.Name),
			ReadOnly:       v.ReadOnly && !hasVolumeBackup(svc),
			AccessMode:     "single-node-single-writer",
			AttachmentMode: "file-system",
		}
	}
	return reqs
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
		reg.CanaryTags = canaryTags
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

// canaryTags replace a service's tags while its allocation is an unpromoted
// canary, so Traefik doesn't send it traffic before it is healthy. Nomad uses
// the normal tags when CanaryTags is empty, so this must not be empty.
//
// It deliberately has no traefik.enable tag: Caravana's Traefik runs with
// exposedByDefault=false, so a registration without traefik.enable=true is
// never routed. A "traefik.enable=false" here would sit next to the live
// instances' "traefik.enable=true" in Nomad's merged service tags, and the lab
// showed Traefik then drops the whole router for about a second whenever the
// canaries change.
var canaryTags = []string{canaryTag}

// canaryTag marks a registration of an unpromoted canary.
const canaryTag = "caravana.canary=true"

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

// SecretsPath is the Nomad Variable that holds a service's secrets. It is the
// path Nomad lets that service's tasks read without any ACL policy
// (nomad/jobs/<job>/<group>), so a service sees its own secrets and no other.
func SecretsPath(appID, service string) string {
	return "nomad/jobs/" + appID + "/" + service
}

// secretsTemplate renders a service's secrets into the env file Nomad reads
// for the task. Each value is JSON-encoded: Nomad's env-file parser accepts
// exactly JSON's escapes in double quotes, so any UTF-8 value arrives byte
// for byte (research R2). "noop" means a changed variable never touches a
// running copy: new values reach copies only through a deploy's rollout.
func secretsTemplate(appID, service string) *api.Template {
	tmpl := `{{ with nomadVar "` + SecretsPath(appID, service) + `" }}` +
		`{{ range .Tuples }}{{ .K }}={{ printf "%s" .V | toJSON }}` + "\n" + `{{ end }}{{ end }}`
	return &api.Template{
		EmbeddedTmpl: new(tmpl),
		DestPath:     new("secrets/caravana.env"),
		Envvars:      new(true),
		ChangeMode:   new("noop"),
	}
}

// Pulling a private image (feature 017, research R1). The Docker driver reads
// its registry login from the task's config, and Nomad fills ${...} in that
// config from the task's environment before it pulls. So the job holds only
// placeholders; a template renders the two values from a Nomad Variable into
// the task's environment, where only that task can read them.
const (
	// RegistryUsernameEnv and RegistryPasswordEnv are the environment names the
	// placeholders and the variable's items use. The password reaches the
	// service's own container under that name, which the console says.
	RegistryUsernameEnv = "CARAVANA_REGISTRY_USERNAME"
	RegistryPasswordEnv = "CARAVANA_REGISTRY_PASSWORD"
)

// pullAuth is the docker driver's auth block for an image, and true, when the
// image comes from a registry in private. The JSON job format takes a block as
// a list holding one object. The values are placeholders, never credentials.
func pullAuth(image string, private map[string]bool) (auth []map[string]any, ok bool) {
	host, ok := PrivateRegistry(image, private)
	if !ok {
		return nil, false
	}
	return []map[string]any{{
		"username":       "${" + RegistryUsernameEnv + "}",
		"password":       "${" + RegistryPasswordEnv + "}",
		"server_address": host,
	}}, true
}

// PrivateRegistry returns the canonical registry host of image, and true, when
// that host is in private.
func PrivateRegistry(image string, private map[string]bool) (host string, ok bool) {
	if len(private) == 0 {
		return "", false
	}
	ref, err := registry.Parse(image)
	if err != nil || !private[ref.Registry] {
		return "", false
	}
	return ref.Registry, true
}

// PullCredentialsPath is the Nomad Variable that holds the credentials a
// service's task pulls its image with. It is the task's own path
// (nomad/jobs/<job>/<group>/<task>), which Nomad lets that task read without
// any ACL policy, and no other service's.
func PullCredentialsPath(appID, service string) string {
	return SecretsPath(appID, service) + "/" + service
}

// pullCredentialsTemplate renders the two credential items into the task's
// environment. Both lines are always written, each JSON-quoted like the
// secrets (see secretsTemplate): a missing item renders as "", so the
// placeholders always resolve and a pull without credentials fails with the
// registry's own error. It loops over the variable's tuples, as the other
// templates do, instead of naming a map key. "noop": a changed variable never
// touches a running copy, so new credentials reach copies when they start.
func pullCredentialsTemplate(appID, service string) *api.Template {
	tmpl := `{{ with nomadVar "` + PullCredentialsPath(appID, service) + `" }}` +
		`{{ $user := "" }}{{ $pass := "" }}` +
		`{{ range .Tuples }}` +
		`{{ if eq .K "` + RegistryUsernameEnv + `" }}{{ $user = printf "%s" .V }}{{ end }}` +
		`{{ if eq .K "` + RegistryPasswordEnv + `" }}{{ $pass = printf "%s" .V }}{{ end }}` +
		`{{ end }}` +
		RegistryUsernameEnv + `={{ $user | toJSON }}` + "\n" +
		RegistryPasswordEnv + `={{ $pass | toJSON }}` + "\n" +
		`{{ end }}`
	return &api.Template{
		EmbeddedTmpl: new(tmpl),
		DestPath:     new("secrets/registry.env"),
		Envvars:      new(true),
		ChangeMode:   new("noop"),
	}
}

func task(appID string, svc app.Service, image string, s Settings, private map[string]bool) *api.Task {
	config := map[string]any{"image": image}
	auth, isPrivate := pullAuth(image, private)
	if isPrivate {
		config["auth"] = auth
	}
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

	t := &api.Task{
		Name:      svc.Name,
		Driver:    "docker",
		Config:    config,
		Env:       env,
		Resources: taskResources(svc.Resources, s),
	}
	// The job names the variable, never a value: values stay in the store.
	if len(svc.Secrets) > 0 {
		t.Templates = []*api.Template{secretsTemplate(appID, svc.Name)}
	}
	if isPrivate {
		t.Templates = append(t.Templates, pullCredentialsTemplate(appID, svc.Name))
	}
	for _, v := range svc.Volumes {
		t.VolumeMounts = append(t.VolumeMounts, &api.VolumeMount{
			Volume:      new(v.Name),
			Destination: new(v.Path),
			ReadOnly:    new(v.ReadOnly),
		})
	}
	return t
}

// orDefault returns v, or def when v is the zero value.
func orDefault[T comparable](v, def T) T {
	var zero T
	if v == zero {
		return def
	}
	return v
}

// taskResources is what a service's task reserves, and the memory ceiling
// it may burst to while the node has memory spare (Nomad's memory_max; the
// cluster must allow memory oversubscription, or Nomad ignores it and says
// so when the job is registered).
//
//   - reservation: the service's own, else the default, lowered to the
//     service's ceiling when only the ceiling is set
//   - ceiling: the service's own, else the larger of the reservation and the
//     default ceiling
func taskResources(r app.Resources, s Settings) *api.Resources {
	cpu, reserve, ceiling := Reserved(r, s)
	return &api.Resources{
		CPU:         new(cpu),
		MemoryMB:    new(reserve),
		MemoryMaxMB: new(ceiling),
	}
}

// Reserved is what a service's task reserves, with the defaults applied: its
// CPU in MHz, its memory in MB, and its memory ceiling in MB. The job is built
// from it and the console shows it next to the use, so both say the same.
func Reserved(r app.Resources, s Settings) (cpuMHz, memoryMB, memoryMaxMB int) {
	reserve := r.MemoryMB
	if reserve == 0 {
		reserve = s.DefaultMemoryMB
		if r.MemoryMaxMB > 0 && r.MemoryMaxMB < reserve {
			reserve = r.MemoryMaxMB
		}
	}
	ceiling := r.MemoryMaxMB
	if ceiling == 0 {
		ceiling = max(reserve, s.DefaultMemoryMaxMB)
	}
	return orDefault(r.CPUMHz, s.DefaultCPUMHz), reserve, ceiling
}
