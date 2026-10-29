# Contract: Go packages for feature 007

New and changed exported API, package by package. The HTTP API is in [api.md](api.md); pages,
parts and streams in [pages.md](pages.md); types and rules in
[data-model.md](../data-model.md); reasons in [research.md](../research.md). Nomad types never
leave `internal/deploy` (constitution VI).

## `internal/app` (changed)

```go
const TriggerConsole Trigger = "console" // a deploy started from the web console (R17)
```

`Deployment` validation accepts `manual`, `webhook` and `console`.

## `internal/store` (changed)

```go
var ErrAlreadyExists = errors.New("already exists") // exists since 006; now also for apps

func (s *Store) InsertApp(ctx context.Context, a app.App) (AppRecord, error)   // ErrAlreadyExists if the ID is stored
func (s *Store) ListApps(ctx context.Context) ([]AppRecord, error)              // by ID
func (s *Store) LatestDeployments(ctx context.Context) (map[string]DeploymentRecord, error) // app ID -> newest
func (s *Store) ListRecentDeployments(ctx context.Context, limit int) ([]DeploymentRecord, error)
func (s *Store) ListDeploymentsBefore(ctx context.Context, appID string, before time.Time, limit int) ([]DeploymentRecord, error)
func (s *Store) DeleteApp(ctx context.Context, id string) error                  // one transaction; ErrNotFound
```

Migration `0004_console.sql` (data-model).

## `internal/deploy` (changed)

```go
// Copy is one Nomad allocation as caravana shows it.
type Copy struct {
	ID, Service, Node, Description string
	Version                        uint64
	Status                         CopyStatus // starting | healthy | unhealthy | stopped | failed
	Restarts                       int
	StartedAt                      *time.Time
	Terminal                       bool
}

type CopyStatus string

// Copies lists jobID's copies (research R8). AllCopies lists every job's, by
// job ID, in one request, for the Apps page.
func (d *Deployer) Copies(ctx context.Context, jobID string) ([]Copy, error)
func (d *Deployer) AllCopies(ctx context.Context) (map[string][]Copy, error)

// WatchCopies calls changed with the job IDs whose allocations changed,
// until ctx ends. Nomad errors are retried with backoff (R6).
func (d *Deployer) WatchCopies(ctx context.Context, changed func(jobIDs []string))

type Progress struct {
	Wanted, Placed, Healthy, Unhealthy, CanariesWanted int
	Promoted                                           bool
	Description                                        string
}

// Progress reads the rollout of jobID at jobVersion; found is false when
// Nomad has no deployment for it.
func (d *Deployer) Progress(ctx context.Context, jobID string, jobVersion uint64) (p Progress, found bool, err error)

type LogStream string // "stdout" | "stderr"

type LogLine struct {
	Copy   string
	Stream LogStream
	Text   string
}

// Logs reads one copy's output: the lines in the last tailBytes, then, with
// follow, each new line until ctx ends. Lines go to out; Logs returns when
// it's done or ctx ends (R10).
func (d *Deployer) Logs(ctx context.Context, copyID, task string, stream LogStream,
	tailBytes int64, follow bool, out chan<- LogLine) error

// Purge stops and purges jobID. A job Nomad doesn't have is not an error.
func (d *Deployer) Purge(ctx context.Context, jobID string) error
```

`NewNomadClient` sets `ClientConnTimeout` to 1 s (R10). Errors keep 003's kinds
(`ErrNomadUnavailable`, `ErrNomadRejected`), so the API maps them as today.

`deploy/deploytest` gains the endpoints and helpers of research R21: `SetCopies(jobID, ...)`,
`WriteLog(copyID, stream, text)`, `Purged(jobID) bool`, and blocking `GET /v1/allocations`.

## `internal/service` (changed)

```go
// Reads
type AppSummary struct {
	store.AppRecord
	Status         AppStatus
	LastDeployment *store.DeploymentRecord
}
type AppState string // rolling_out | healthy | degraded | not_running | unknown
type AppStatus struct {
	State                       AppState
	WantedCopies, HealthyCopies int
	LiveVersion                 *uint64
	NomadError                  string
}
type Copy = deploy.Copy         // re-exported so api and ui don't import deploy
type Progress = deploy.Progress
type LogLine struct {
	Copy, Service string
	Stream        deploy.LogStream
	Text          string
	Time          *time.Time
}
type LogQuery struct {
	Copy   string           // "" = every listed copy
	Stream deploy.LogStream // "" = both
	Lines  int              // 1–500
	Follow bool
}

func (s *Service) ListApps(ctx context.Context) ([]AppSummary, error)        // Nomad down: Status.State unknown, no error
func (s *Service) AppSummary(ctx context.Context, id string) (AppSummary, error) // store.ErrNotFound
func (s *Service) RecentDeployments(ctx context.Context, limit int) ([]store.DeploymentRecord, error)
func (s *Service) ListDeploymentsBefore(ctx context.Context, appID string, before time.Time, limit int) ([]store.DeploymentRecord, error)
func (s *Service) Copies(ctx context.Context, appID string) ([]Copy, error)
func (s *Service) Progress(ctx context.Context, rec store.DeploymentRecord) (*Progress, error) // nil unless running
func (s *Service) Logs(ctx context.Context, appID string, q LogQuery, out chan<- LogLine) error // closes nothing; returns when done or ctx ends

// Changes
var ErrAppExists = errors.New("an app with this ID already exists")

func (s *Service) CreateApp(ctx context.Context, a app.App, trigger app.Trigger) (store.DeploymentRecord, error) // ErrAppExists, *app.FieldError, *DeployError
func (s *Service) DeleteApp(ctx context.Context, id string) error // store.ErrNotFound; Nomad errors, nothing deleted

// Live notices (R5)
type ChangeKind string // apps | activity | app | deployments | copies | deleted
type Change struct {
	AppID string
	What  ChangeKind
}

// Subscribe returns a channel of notices for appID ("" = every app's) and a
// function that ends the subscription. Notices are dropped, never queued,
// when the reader is behind.
func (s *Service) Subscribe(appID string) (<-chan Change, func())
```

Deploy-flow writes (`write`, `update`, `fail`, `finish`), `Deploy`, `CreateApp` and `DeleteApp`
publish notices (data-model table). `appState` gains the cancel function of its watchers, which
`DeleteApp` calls while holding the app's lock.

`service.Auth` gains:

```go
// CheckSessionQuiet is CheckSession without renewing the session's idle time,
// for live streams (R11).
func (a *Auth) CheckSessionQuiet(ctx context.Context, sessionValue string) error
```

## `internal/httpx` (changed)

```go
// Stream prepares w for server-sent events (R12) and returns a writer for them.
func Stream(w http.ResponseWriter) (*EventWriter, error) // error: the writer can't flush

type EventWriter struct{ /* … */ }

func (e *EventWriter) Event(name string, data []byte) error // data may be nil; newlines split into data: lines
func (e *EventWriter) Comment(text string) error            // keepalive
```

The keepalive and session re-check loop is the caller's (`ui` and `api` each have one `select`
loop), so the helper stays a writer.

## `internal/api` (changed)

Six routes (api.md). `route` gains `quiet bool` (authenticate without renewing a session), set
on `GET /v1/apps/{id}/logs`; its handler decides between JSON and the event stream by `follow`.
New error code `app_exists`; `ErrAppExists` maps to it, `field: "id"`.

## `internal/ui` (changed)

```go
type Deps struct {
	Auth    *service.Auth
	Service *service.Service // new
	Proxies httpx.Proxies
	Log     *slog.Logger
	Now     func() time.Time
	Host    string // new: the host name shown in the sidebar; "" = the request's Host
}
```

Files: `ui.go` (routes, `requireSession`, `requirePart`, templates), `apps.go`, `app.go`
(tabs, deploy, webhook, delete), `newapp.go`, `settings.go` (renamed from `account.go`),
`live.go` (streams), `view.go` (what templates get: rows, sentences, relative times),
`templates/{base,shell,parts,apps,app,newapp,settings,login,message}.html`,
`static/js/{htmx.min.js,sse.min.js,console.js,LICENSES.md}`.

## `cmd/caravana` (changed)

`ui.New` gets the `Service`; every `http.Server` gets a `BaseContext` that shutdown cancels
first, so open streams end at once (R12); `runGate` gains the `console-reads` step and
`TestLab` the console steps (R21).

## Tools and deploy

- `tools/ui-vendor.sh fetch|check` and `tools/ui-vendor.versions` (R4); CI runs `check`.
- `deploy/install/caravana-policy.hcl`: `capabilities = ["submit-job", "read-job", "list-jobs",
  "read-logs"]` (R10).
