# Contract: Go packages for feature 003 (and what 004 builds on)

The HTTP contract is [openapi.yaml](openapi.yaml) (it replaces `api/openapi.yaml`). Types and
statuses are in [data-model.md](../data-model.md); the reasons are in
[research.md](../research.md). Signatures are the intended shape; names may shift slightly
during implementation, behavior may not.

Dependency direction (no cycles):

```text
cmd/caravana ─► api ─► service ─► deploy ─► nomadjob ─► app
                          │          └────► registry
                          └────► store ─► app
```

`api` also imports `deploy` and `registry` only to check their error values. Only `deploy` and
`nomadjob` import `github.com/hashicorp/nomad/api` (constitution VI).

## `internal/registry` (new)

```go
type Reference struct {
	Name       string // as written, without tag or digest: "traefik/whoami"
	Registry   string // "docker.io" or "ghcr.io"
	Repository string // "traefik/whoami", "library/nginx", "acme/shop"
	Tag        string // "" when Digest is set
	Digest     string // "sha256:<64 hex>" or ""
	IgnoredTag string // set when both a tag and a digest were given; becomes a warning
}

func Parse(image string) (Reference, error) // pure; R2
func (r Reference) Pinned(digest string) string // r.Name + "@" + digest

var (
	ErrNotFound    = errors.New("image not found")       // 404, or 401/403 after the handshake
	ErrUnavailable = errors.New("registry unavailable")  // network, 5xx, 429, bad digest
)

type Client struct {
	HTTP      *http.Client      // nil: a client with sane timeouts
	Endpoints map[string]string // registry -> base URL; nil: the real ones. Tests point it at httptest.
}

// Resolve returns the pinned image ("<name>@sha256:…"). A reference that already has a
// digest returns it without any request. Errors wrap ErrNotFound or ErrUnavailable.
func (c *Client) Resolve(ctx context.Context, ref Reference) (string, error)
```

`internal/registry/registrytest` (new, test helper): `New(t) *Server` with
`AddIndex(repo, tag, digest)`, `AddManifest(repo, tag, digest)`, `Fail(repo, tag, status)`,
`Hold(repo, tag) (release func())`, `RequireToken(bool)`, and `Endpoints() map[string]string`.

## `internal/deploy` (new)

```go
type Deployer struct { /* nomad client, registry client, translator settings, poll timing */ }

func NewNomadClient(addr, token string) (*api.Client, error) // main passes cfg.NomadToken.Reveal()
func New(nomad *api.Client, reg *registry.Client, s nomadjob.Settings) *Deployer

// Validate is everything decidable from the request alone: app rules, Nomad limits
// (nomadjob.Check) and every image reference. Errors are *app.FieldError. Pure.
func (d *Deployer) Validate(a app.App) error

// Resolve pins every service's image, in service order, with a warning for each ignored
// tag. A failure is an *ImageError naming services[i].image and wrapping ErrImageNotFound
// or ErrRegistryUnavailable.
func (d *Deployer) Resolve(ctx context.Context, a app.App) (map[string]string, []app.Warning, error)

// Register translates and registers the job (R6). owns reports whether a deployment ID
// is one the store has for this app (R7). It returns the new job version.
func (d *Deployer) Register(ctx context.Context, a app.App, dep app.Deployment,
	owns func(ctx context.Context, deploymentID string) (bool, error)) (Registered, error)

type Registered struct {
	JobVersion *uint64 // nil: Nomad accepted the job but its version isn't known yet
	Warnings   []app.Warning
}

// FindVersion looks for the job version carrying deploymentID (restart, R10).
func (d *Deployer) FindVersion(ctx context.Context, appID, deploymentID string) (uint64, bool, error)

// Watch follows the Nomad deployment for jobVersion until it ends (R8). It retries
// while Nomad is unreachable and returns only an outcome or ctx's error.
func (d *Deployer) Watch(ctx context.Context, appID string, jobVersion uint64) (Outcome, error)

type Result string // OutcomeHealthy, OutcomeFailed, OutcomeReverted, OutcomeSuperseded
type Outcome struct {
	Result  Result
	Message string // Nomad's description
}

type ImageError struct {
	Field, Image string
	Err          error
}

var (
	ErrImageNotFound       = errors.New("image not found")
	ErrRegistryUnavailable = errors.New("registry unavailable")
	ErrJobConflict         = errors.New("job conflict")
	ErrNomadUnavailable    = errors.New("nomad unavailable")
	ErrNomadRejected       = errors.New("nomad rejected the job")
)
```

`internal/deploy/deploytest` (new, test helper): `New(t) *Nomad`, an `httptest` server for the
four Nomad endpoints in R12, with `Script(jobVersion, states ...api.Deployment)`,
`AddForeignJob(job)`, `Down()` / `Up()`, `Jobs()`, and `Addr()`.

## `internal/nomadjob` (changed)

```go
// Check runs every rule Translate runs that doesn't need a deployment: the settings,
// app.ValidateApp and Nomad's limits. Pure.
func Check(a app.App, s Settings) error
```

`Translate` calls `Check` and then the deployment rules; its output and golden files don't
change.

## `internal/app` (changed)

```go
func ValidateApp(a App) error // Validate without the deployment part
```

`Validate(a, d)` becomes `ValidateApp(a)` followed by the deployment checks, with the same order
and messages.

## `internal/store` (changed)

```go
type DeploymentUpdate struct {
	Status     Status
	JobVersion *uint64
	Digests    map[string]string // NEW: nil keeps the stored value
	Warnings   []app.Warning
	Error      string
}

func (s *Store) ListDeployments(ctx context.Context, appID string, limit int) ([]DeploymentRecord, error) // limit <= 0: all
func (s *Store) ListUnfinishedDeployments(ctx context.Context) ([]DeploymentRecord, error)             // NEW
```

## `internal/service` (new)

```go
type Service struct { /* store, deployer, logger, per-app state, background context, WaitGroup */ }

func New(st *store.Store, d *deploy.Deployer, log *slog.Logger) *Service

// Start resumes unfinished deployments in the background (R10). ctx lives as long as
// the process; cancelling it stops every watcher.
func (s *Service) Start(ctx context.Context) error
func (s *Service) Wait() // blocks until every watcher has returned

// Deploy validates, saves and deploys a. Redeploy deploys the stored app. Both return
// once Nomad accepted the job (or the deploy was superseded before registering).
func (s *Service) Deploy(ctx context.Context, a app.App, t app.Trigger) (store.DeploymentRecord, error)
func (s *Service) Redeploy(ctx context.Context, appID string, t app.Trigger) (store.DeploymentRecord, error)

func (s *Service) GetApp(ctx context.Context, id string) (store.AppRecord, error)
func (s *Service) ListDeployments(ctx context.Context, appID string, limit int) ([]store.DeploymentRecord, error)
func (s *Service) GetDeployment(ctx context.Context, appID, deploymentID string) (store.DeploymentRecord, error)

// DeployError is returned once a deployment record exists, so the API can set Location.
type DeployError struct {
	AppID, DeploymentID string
	Err                 error
}
```

Guarantees:
- Validation errors are `*app.FieldError` and nothing is saved or recorded.
- `GetDeployment` returns `store.ErrNotFound` when the deployment belongs to another app.
- Feature 004 calls `Redeploy(ctx, appID, app.TriggerWebhook)`.

## `internal/api` (changed)

```go
func New(svc *service.Service, st *store.Store, log *slog.Logger) http.Handler
```

`st` stays for the health check only; every app and deployment endpoint goes through `svc`
(constitution V). Request and response bodies use the v2 option set in R5.

## `internal/config` (changed)

```go
type Secret string // String, GoString and LogValue return "[redacted]"; Reveal returns the value

type Config struct {
	Listen     string
	DataDir    string
	Settings   nomadjob.Settings
	NomadAddr  string // NEW
	NomadToken Secret // NEW, env only
}
```

## `cmd/caravana` (changed)

Wiring order: config → store → Nomad client → registry client → deployer → service →
`svc.Start(bgCtx)` → HTTP server. On shutdown: HTTP `Shutdown` → cancel `bgCtx` →
`svc.Wait()` → close the store.
