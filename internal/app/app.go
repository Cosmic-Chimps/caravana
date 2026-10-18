// Package app holds Caravana's app model.
//
// Every input (today a prebuilt image; later a compose file or a git repo) is
// normalized into an App. Everything downstream (Nomad translation, routing,
// deploys) only sees this model. After validation, nothing reads Source: it
// records where the app came from, not how to run it.
//
// The json tags are both the stored document format and the API's field
// names, and match the field paths in FieldError. Renaming one is a schema
// change.
package app

import "time"

// App is one project a user deploys. It becomes one Nomad job.
type App struct {
	// ID is a stable DNS label, e.g. "blog". It is the Nomad job ID and the
	// <app> part of every service and router name.
	ID string `json:"id"`
	// Name is the display name and the Nomad job name. Empty means ID.
	Name string `json:"name,omitempty"`
	// Source says where the app came from. Only SourceImage is supported.
	Source Source `json:"source"`
	// Services run in this order, one task group each. At least one.
	Services []Service `json:"services"`
}

// Defined reports whether the app has a definition to deploy: at least one
// service. An app saved without deploying it (feature 020) may have none yet,
// and is only ever stored: nothing deploys it until a deploy brings services.
func (a App) Defined() bool { return len(a.Services) > 0 }

// SourceKind is the kind of input an App was built from.
type SourceKind string

// Source kinds. Both describe services by prebuilt images; the kind only
// records which input the app was normalized from.
const (
	// SourceImage is an app described in Caravana's own model, one image per
	// service.
	SourceImage SourceKind = "image"
	// SourceCompose is an app normalized from a Docker Compose file.
	SourceCompose SourceKind = "compose"
)

// Source records where an App came from. The image itself lives on each
// Service.
type Source struct {
	Kind SourceKind `json:"kind"`
}

// Service is one runnable container. It becomes one Nomad task group with one
// Docker task, so each service scales on its own.
type Service struct {
	// Name is a DNS label, unique within the app.
	Name string `json:"name"`
	// Image is the reference the user wrote, e.g. "traefik/whoami:latest".
	// It is never put in the job: the deployment's resolved digest is.
	Image string `json:"image"`
	// Command overrides the image's default command. Empty keeps it.
	Command string `json:"command,omitempty"`
	// Args are passed to the container, with or without Command.
	Args []string `json:"args,omitempty"`
	// Env holds plain, non-secret config. Names must be unique.
	Env []EnvVar `json:"env,omitempty"`
	// Secrets are environment variables whose values are write-only: they
	// live in the secret store, never in the stored app or the Nomad job.
	// Each deploy replaces the whole list; a name left out is removed.
	Secrets []Secret `json:"secrets,omitempty"`
	// Volumes are directories that keep their data across deploys, each
	// mounted by this service only. A service with a volume runs one copy,
	// and its deploys replace the copy instead of running two at once.
	Volumes []Volume `json:"volumes,omitempty"`
	// Backup makes Caravana back the service up on a schedule to the
	// installation's backup destination: a Postgres database, or the files of
	// its volumes. nil means no backups.
	Backup *Backup `json:"backup,omitempty"`
	// Ports are the container ports the service listens on.
	Ports []Port `json:"ports,omitempty"`
	// Domains are public hostnames routed over HTTPS. All of a service's
	// domains must use the same port.
	Domains []Domain `json:"domains,omitempty"`
	// Replicas is how many copies run. nil means 1; a set value must be at
	// least 1.
	Replicas *int `json:"replicas,omitempty"`
	// Resources are the CPU and memory the service reserves. Zero fields use
	// the environment's defaults.
	Resources Resources `json:"resources,omitzero"`
	// Health is the health check. Required when the service has ports, not
	// allowed when it has none. nil means no check.
	Health *HealthCheck `json:"health,omitempty"`
}

// EnvVar is one plain environment variable. Values that must stay hidden go
// in Service.Secrets instead.
type EnvVar struct {
	// Name must be non-empty and contain neither "=" nor a NUL byte.
	Name string `json:"name"`
	// Value can be any text.
	Value string `json:"value"`
}

// Secret is one secret environment variable of a service.
type Secret struct {
	// Name is letters, digits and "_", not starting with a digit, and unique
	// across the service's secrets and env.
	Name string `json:"name"`
	// Value is the new value: UTF-8 text without a NUL byte. nil keeps the
	// value the secret has now. It is never stored and never returned.
	Value *string `json:"value,omitempty"`
}

// Volume is a named directory of the app on one node, mounted into one
// service. It is created on the first deploy that names it and deleted with
// the app.
type Volume struct {
	// Name is lowercase letters, digits, "-" and "_", starting with a letter
	// or digit, and unique across the app.
	Name string `json:"name"`
	// Path is where the service sees it: an absolute path, not "/".
	Path string `json:"path"`
	// ReadOnly mounts it read-only.
	ReadOnly bool `json:"readOnly,omitempty"`
}

// ReservedServiceName is the name of the task Caravana adds beside every
// service that has a volume (feature 016). A service can't take it, so the
// task can't be confused with a service's own.
const ReservedServiceName = "caravana-volumes"

// Backup engines.
const (
	// BackupPostgres is pg_dump and pg_restore, with the credentials the
	// official postgres image reads (POSTGRES_USER, POSTGRES_PASSWORD,
	// POSTGRES_DB) from the service's env or secrets.
	BackupPostgres = "postgres"
	// BackupVolume archives the service's volumes with the service stopped, so
	// it works for any app that keeps its data in a volume (feature 016).
	BackupVolume = "volume"
)

// Backup is a service's backup policy.
type Backup struct {
	// Engine is how the service is backed up: BackupPostgres, for a database
	// Caravana reaches over its port, or BackupVolume, for the files of the
	// service's volumes.
	Engine string `json:"engine"`
	// Schedule is a five-field cron expression, in UTC. Empty means every day
	// at 03:00 (DefaultBackupSchedule).
	Schedule string `json:"schedule,omitempty"`
	// Keep is how many backups stay in the destination, 1 to 100. Zero means
	// DefaultBackupKeep.
	Keep int `json:"keep,omitempty"`
}

// Defaults of a backup policy.
const (
	DefaultBackupSchedule = "0 3 * * *"
	DefaultBackupKeep     = 7
)

// ScheduleOrDefault is the cron expression the backup runs on.
func (b Backup) ScheduleOrDefault() string {
	if b.Schedule == "" {
		return DefaultBackupSchedule
	}
	return b.Schedule
}

// KeepOrDefault is how many backups stay.
func (b Backup) KeepOrDefault() int {
	if b.Keep == 0 {
		return DefaultBackupKeep
	}
	return b.Keep
}

// WithoutSecretValues returns a copy of a whose secrets have names only, the
// form Caravana stores and shows. Deployed again, it keeps every value.
func (a App) WithoutSecretValues() App {
	services := make([]Service, len(a.Services))
	for i, svc := range a.Services {
		if len(svc.Secrets) > 0 {
			names := make([]Secret, len(svc.Secrets))
			for j, s := range svc.Secrets {
				names[j] = Secret{Name: s.Name}
			}
			svc.Secrets = names
		}
		services[i] = svc
	}
	a.Services = services
	return a
}

// Port is a named container port. Nomad maps a dynamic host port to it.
type Port struct {
	// Name is the port label, e.g. "http": a lowercase letter, then lowercase
	// letters, digits or underscores.
	Name string `json:"name"`
	// ContainerPort is the port inside the container, 1 to 65535.
	ContainerPort int `json:"containerPort"`
}

// Domain is a public hostname routed over HTTPS to one port of a service.
type Domain struct {
	// Host is a lowercase hostname, unique across the whole app.
	Host string `json:"host"`
	// Port is the Name of a Port declared on the same service.
	Port string `json:"port"`
}

// HealthCheck tells Nomad how to decide that a service is healthy. Deploys wait
// for it and revert automatically when it keeps failing.
type HealthCheck struct {
	// Path makes it an HTTP check on that path, which must start with "/".
	// Empty means a TCP check.
	Path string `json:"path,omitempty"`
	// Port is the Name of the port to check. Empty means the domains' port or,
	// without domains, the service's only port.
	Port string `json:"port,omitempty"`
	// Interval is the time between checks. Zero means 10s.
	Interval time.Duration `json:"interval,omitempty"`
	// Timeout is how long one check may take. Zero means 2s. It must be
	// shorter than Interval.
	Timeout time.Duration `json:"timeout,omitempty"`
}

// Resources are what a service reserves on its node.
type Resources struct {
	// CPUMHz is the CPU share in MHz. Zero means the environment default.
	CPUMHz int `json:"cpuMHz,omitempty"`
	// MemoryMB is the memory reserved for the service, in MB: what the
	// scheduler counts when it places copies. Zero means the environment
	// default.
	MemoryMB int `json:"memoryMB,omitempty"`
	// MemoryMaxMB is the most memory the service may use while the node has
	// it spare, in MB; past it the copy is killed and restarted. Zero means
	// the larger of the reservation and the environment default.
	MemoryMaxMB int `json:"memoryMaxMB,omitempty"`
}
