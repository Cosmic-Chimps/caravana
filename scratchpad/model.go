// Package app holds the platform's internal app model.
//
// Every input (a single image, a compose file, a git repo + builder) is
// normalized into an App. Everything downstream (Nomad translation,
// routing, deploys) only ever sees this model.
package app

import "time"

// App is what the user sees as "one project" in the UI.
// It maps 1:1 to a Nomad job.
type App struct {
	ID       string    // stable slug, e.g. "blog" -> Nomad job ID
	Name     string    // display name
	Source   Source    // where it came from (informational + for rebuilds)
	Services []Service // one for an image app, several for compose
	Created  time.Time
}

// SourceKind mirrors the provider tabs in the UI.
type SourceKind string

const (
	SourceImage   SourceKind = "image"   // POC: start here
	SourceCompose SourceKind = "compose" // step 2
	SourceGit     SourceKind = "git"     // step 3 (needs a builder)
)

type Source struct {
	Kind SourceKind
	// SourceImage
	Image string // e.g. "ghcr.io/me/blog:main"
	// SourceGit (later)
	RepoURL string
	Branch  string
	Builder string // "dockerfile", "railpack", ...
}

// Service is one runnable container. Maps to a Nomad task group
// with a single task, so each service scales independently.
type Service struct {
	Name      string
	Image     string // tag reference as configured by the user
	Command   []string
	Env       map[string]string // plain, non-secret config
	Secrets   bool              // if true, inject Nomad Variables as env
	Ports     []Port
	Domains   []Domain // public routing via Traefik
	Volumes   []Volume
	Replicas  int
	Resources Resources
	Health    *HealthCheck
	PinToNode string // optional node name, e.g. for databases
}

type Port struct {
	Name          string // label, e.g. "http"
	ContainerPort int
}

type Domain struct {
	Host string // "blog.example.com"
	Port string // which Port.Name receives the traffic
	TLS  bool   // Let's Encrypt via Traefik
}

type Volume struct {
	HostVolume string // name of a Nomad host volume on the node
	MountPath  string
	ReadOnly   bool
}

type Resources struct {
	CPUMHz   int
	MemoryMB int
}

type HealthCheck struct {
	Path     string // HTTP path; empty means TCP check
	Port     string // Port.Name
	Interval time.Duration
	Timeout  time.Duration
}

// Deployment is one "go live" event, triggered by the UI or a webhook.
// The image tag is resolved to a digest at deploy time so Nomad
// always sees a real diff and every deploy is a revertable job version.
type Deployment struct {
	ID       string
	AppID    string
	Trigger  string            // "webhook", "manual", "rollback"
	Digests  map[string]string // service name -> "repo@sha256:..."
	Started  time.Time
	Finished *time.Time
	Status   string // "pending", "running", "healthy", "failed"
	NomadVer uint64 // Nomad job version, for revert
}
