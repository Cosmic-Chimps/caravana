# Data Model: Control Plane Skeleton and API Contract

Phase 1 output for [plan.md](plan.md). Decisions referenced as R*n* are in
[research.md](research.md).

## Configuration (`internal/config`)

```go
type Config struct {
	Listen   string            // host:port; default 127.0.0.1:7070
	DataDir  string            // default "data"
	Settings nomadjob.Settings // Entrypoint, CertResolver, DefaultCPUMHz, DefaultMemoryMB
}
```

| Rule | Error names |
| --- | --- |
| `Listen` parses as `host:port`, port 1–65535 | `CARAVANA_LISTEN / -listen` |
| `DataDir` non-empty | `CARAVANA_DATA_DIR / -data-dir` |
| Int settings parse as integers | the setting's env and flag |
| `Settings.Validate()` (feature 001's rules) | mapped from `settings.<x>` to env and flag, e.g. `settings.defaultMemoryMB` → `CARAVANA_DEFAULT_MEMORY_MB / -default-memory-mb` |

`Config.Loopback() bool` tells `main` whether to log the no-auth warning (FR-004a).

## App model JSON tags (`internal/app`, changed)

Tags are camelCase and match feature 001's error paths (R6). `omitempty` on every optional
field. Example document for the whoami app:

```json
{"id":"whoami","name":"whoami","source":{"kind":"image"},"services":[{"name":"web",
"image":"traefik/whoami:latest","ports":[{"name":"http","containerPort":80}],
"domains":[{"host":"whoami.localhost","port":"http"}],"replicas":2,"health":{"path":"/"}}]}
```

Durations (`health.interval`, `health.timeout`) are integer nanoseconds in the stored document
(R5).

## Store schema (`internal/store/migrations/0001_init.sql`)

```sql
CREATE TABLE schema_migrations (
  version    INTEGER PRIMARY KEY,
  name       TEXT NOT NULL,
  applied_at TEXT NOT NULL
) STRICT;

CREATE TABLE apps (
  id             TEXT PRIMARY KEY,
  schema_version INTEGER NOT NULL,
  document       TEXT NOT NULL,           -- the whole app.App as JSON
  created_at     TEXT NOT NULL,           -- RFC 3339, UTC, nanoseconds
  updated_at     TEXT NOT NULL
) STRICT;

CREATE TABLE deployments (
  id          TEXT PRIMARY KEY,
  app_id      TEXT NOT NULL REFERENCES apps(id),
  trigger     TEXT NOT NULL,
  digests     TEXT NOT NULL,              -- JSON object: service name -> repo@sha256:...
  status      TEXT NOT NULL CHECK (status IN
                ('pending','running','healthy','failed','reverted','superseded')),
  job_version INTEGER,                    -- NULL until Nomad registers the job
  warnings    TEXT NOT NULL DEFAULT '[]', -- JSON array of {field, message}
  error       TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
) STRICT;

CREATE INDEX deployments_by_app ON deployments (app_id, created_at DESC);
```

`schema_migrations` is created by the runner before reading it, so `0001_init.sql` holds only
`apps`, `deployments` and the index.

## Go types (`internal/store`)

```go
const appSchemaVersion = 1 // bump when the app document changes in a way old readers can't handle

type AppRecord struct {
	App       app.App
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Status string

const (
	StatusPending    Status = "pending"
	StatusRunning    Status = "running"
	StatusHealthy    Status = "healthy"
	StatusFailed     Status = "failed"
	StatusReverted   Status = "reverted"
	StatusSuperseded Status = "superseded"
)

type DeploymentRecord struct {
	Deployment app.Deployment // ID, Trigger, Digests
	AppID      string
	Status     Status
	JobVersion *uint64 // nil until known
	Warnings   []app.Warning
	Error      string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// DeploymentUpdate changes a deployment. Zero values mean "keep what's stored".
type DeploymentUpdate struct {
	Status     Status        // required
	JobVersion *uint64       // nil keeps the stored value
	Warnings   []app.Warning // nil keeps the stored value
	Error      string        // "" keeps the stored value
}
```

## Deployment status transitions

```text
            ┌──────────► failed      (broke before reaching Nomad)
            │
 pending ───┼──────────► superseded
            │
            └──► running ──┬──► running   (record the job version)
                           ├──► healthy
                           ├──► failed
                           ├──► reverted  (failed; Nomad rolled back)
                           └──► superseded
```

`healthy`, `failed`, `reverted` and `superseded` are final. Any other change, an unknown status,
or a change to a final deployment returns `ErrInvalidTransition` and writes nothing. The check
and the update run in one transaction.

## Errors (`internal/store`)

| Error | When | API maps to |
| --- | --- | --- |
| `ErrNotFound` | unknown app or deployment ID (wrapped with which one) | 404 `not_found` |
| `ErrAlreadyExists` | creating a deployment whose ID exists | 409 `conflict` (from 003) |
| `ErrInvalidTransition` | a status change outside the diagram | 409 `conflict` (from 003) |
| `ErrSchemaTooNew` | an app document's `schema_version` > `appSchemaVersion` | 500 `internal` |
| `ErrStoreTooNew` | a migration in the store this binary doesn't have | startup failure |
| `ErrLocked` | another process holds `caravana.lock` | startup failure |

All are sentinel values checked with `errors.Is`; the returned error wraps them with context.

## API error body (`internal/api`)

```go
type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}
```

Written as `{"error": {...}}`. Codes in this feature: `bad_request`, `invalid_field`,
`unknown_field`, `unsupported_media_type`, `body_too_large`, `not_found`,
`method_not_allowed`, `internal`, `unavailable`.
