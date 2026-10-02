// Package app holds Caravana's app model.
//
// Every input (today a prebuilt image; later a compose file or a git repo) is
// normalized into an App. Everything downstream (Nomad translation, routing,
// deploys) only sees this model. After validation, nothing reads Source: it
// records where the app came from, not how to run it.
package app

import "time"

// App is one project a user deploys. It becomes one Nomad job.
type App struct {
	// ID is a stable DNS label, e.g. "blog". It is the Nomad job ID and the
	// <app> part of every service and router name.
	ID string
	// Name is the display name and the Nomad job name. Empty means ID.
	Name string
	// Source says where the app came from. Only SourceImage is supported.
	Source Source
	// Services run in this order, one task group each. At least one.
	Services []Service
}

// SourceKind is the kind of input an App was built from.
type SourceKind string

// SourceImage is an app built from prebuilt container images, one per service.
const SourceImage SourceKind = "image"

// Source records where an App came from. The image itself lives on each
// Service.
type Source struct {
	Kind SourceKind
}

// Service is one runnable container. It becomes one Nomad task group with one
// Docker task, so each service scales on its own.
type Service struct {
	// Name is a DNS label, unique within the app.
	Name string
	// Image is the reference the user wrote, e.g. "traefik/whoami:latest".
	// It is never put in the job: the deployment's resolved digest is.
	Image string
	// Command overrides the image's default command. Empty keeps it.
	Command string
	// Args are passed to the container, with or without Command.
	Args []string
	// Env holds plain, non-secret config. Names must be unique.
	Env []EnvVar
	// Ports are the container ports the service listens on.
	Ports []Port
	// Domains are public hostnames routed over HTTPS. All of a service's
	// domains must use the same port.
	Domains []Domain
	// Replicas is how many copies run. nil means 1; a set value must be at
	// least 1.
	Replicas *int
	// Resources are the CPU and memory the service reserves. Zero fields use
	// the environment's defaults.
	Resources Resources
	// Health is the health check. Required when the service has ports, not
	// allowed when it has none. nil means no check.
	Health *HealthCheck
}

// EnvVar is one plain environment variable. Secrets come with a later feature.
type EnvVar struct {
	// Name must be non-empty and contain neither "=" nor a NUL byte.
	Name string
	// Value can be any text.
	Value string
}

// Port is a named container port. Nomad maps a dynamic host port to it.
type Port struct {
	// Name is the port label, e.g. "http": a lowercase letter, then lowercase
	// letters, digits or underscores.
	Name string
	// ContainerPort is the port inside the container, 1 to 65535.
	ContainerPort int
}

// Domain is a public hostname routed over HTTPS to one port of a service.
type Domain struct {
	// Host is a lowercase hostname, unique across the whole app.
	Host string
	// Port is the Name of a Port declared on the same service.
	Port string
}

// HealthCheck tells Nomad how to decide that a service is healthy. Deploys wait
// for it and revert automatically when it keeps failing.
type HealthCheck struct {
	// Path makes it an HTTP check on that path, which must start with "/".
	// Empty means a TCP check.
	Path string
	// Port is the Name of the port to check. Empty means the domains' port or,
	// without domains, the service's only port.
	Port string
	// Interval is the time between checks. Zero means 10s.
	Interval time.Duration
	// Timeout is how long one check may take. Zero means 2s. It must be
	// shorter than Interval.
	Timeout time.Duration
}

// Resources are what a service reserves on its node.
type Resources struct {
	// CPUMHz is the CPU share in MHz. Zero means the environment default.
	CPUMHz int
	// MemoryMB is the memory limit in MB. Zero means the environment default.
	MemoryMB int
}
