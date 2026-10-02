# Contract: Go packages for features 003 and 004

What later features build on. The HTTP contract is [openapi.yaml](openapi.yaml); types and
errors are in [data-model.md](../data-model.md).

## `internal/store`

```go
func Open(ctx context.Context, dataDir string) (*Store, error) // lock, open, migrate
func (s *Store) Close() error
func (s *Store) Ping(ctx context.Context) error

func (s *Store) SaveApp(ctx context.Context, a app.App) (AppRecord, error)
func (s *Store) GetApp(ctx context.Context, id string) (AppRecord, error)

func (s *Store) CreateDeployment(ctx context.Context, appID string, d app.Deployment) (DeploymentRecord, error)
func (s *Store) GetDeployment(ctx context.Context, id string) (DeploymentRecord, error)
func (s *Store) UpdateDeployment(ctx context.Context, id string, u DeploymentUpdate) (DeploymentRecord, error)
func (s *Store) ListDeployments(ctx context.Context, appID string) ([]DeploymentRecord, error)
```

Guarantees:
- `Open` creates `dataDir` (mode 0700) if needed, takes `caravana.lock`, opens `caravana.db`,
  and applies missing migrations. It fails with `ErrLocked`, `ErrStoreTooNew`, or a wrapped
  error naming the migration that failed.
- `SaveApp` keeps `CreatedAt` on update and leaves `UpdatedAt` alone when the document is
  identical (idempotent upserts).
- `CreateDeployment` returns `ErrNotFound` for an unknown app, `ErrAlreadyExists` for a
  duplicate ID; new records are `pending` with no job version.
- `UpdateDeployment` enforces the transitions in the data model.
- `ListDeployments` returns newest first (`created_at`, then insertion order), and
  `ErrNotFound` for an unknown app. An app with no deployments gives an empty slice.
- Every method takes `context.Context` first (constitution VIII) and is safe to call from
  several goroutines.

## `internal/api`

```go
func New(st *store.Store, log *slog.Logger) http.Handler

// For handlers added by 003 and 004:
type handlerFunc func(w http.ResponseWriter, r *http.Request) error
func decodeJSON(r *http.Request, v any) error  // 415, 413, 400 unknown_field / bad_request / invalid_field
func writeJSON(w http.ResponseWriter, status int, v any)
```

- New routes go into the package's single `routes` table (method, path, handler), which both
  builds the mux and feeds the contract-sync test.
- A handler returns an error; the adapter maps `*api.Error`, `*app.FieldError`,
  `store.ErrNotFound` and anything else (500) as described in research R8.

## `internal/nomadjob` (changed)

```go
func (s Settings) Validate() error // was the unexported validateSettings; Translate calls it
```

## `cmd/caravana`

`main` calls `run(ctx, os.Args[1:], os.Getenv, os.Stderr)` and exits with its code, so start
and stop can be tested in-process.
