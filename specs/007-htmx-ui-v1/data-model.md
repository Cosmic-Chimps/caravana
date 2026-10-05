# Data Model: HTMX UI v1

What feature 007 stores, derives and sends. Reasons are in [research.md](research.md); Go
signatures in [contracts/go-packages.md](contracts/go-packages.md); JSON shapes in
[contracts/api.md](contracts/api.md).

## Storage

One migration, `internal/store/migrations/0004_console.sql`:

```sql
-- Recent deployments across all apps, for the Apps page's activity and GET /v1/deployments.
CREATE INDEX deployments_by_time ON deployments (created_at DESC);
```

No table changes:

- `deployments.trigger` is `TEXT` without a CHECK, so the new value `console` needs no
  migration (R17). `app.Deployment` validation accepts it.
- Deleting an app removes rows from `webhook_tokens`, `deployments` and `apps`, in that order,
  in one transaction (`store.DeleteApp`), because the foreign keys have no `ON DELETE CASCADE`.

Nothing else is persisted: app status, copies, rollout progress and log lines are read live from
Nomad and never stored.

## Entities

### App summary (derived, `service.AppSummary`)

| Field | Type | From |
| --- | --- | --- |
| `App` | `app.App` | store |
| `CreatedAt`, `UpdatedAt` | time | store |
| `Status` | `AppStatus` (below) | store + Nomad |
| `LastDeployment` | `*store.DeploymentRecord` | store (newest by `created_at`); nil if never deployed |

### App status (derived, `service.AppStatus`)

| Field | Type | Meaning |
| --- | --- | --- |
| `State` | `rolling_out` \| `healthy` \| `degraded` \| `not_running` \| `unknown` | R7 table |
| `WantedCopies` | int | sum of each service's replicas (nil replicas = 1) |
| `HealthyCopies` | int | copies of the live version that are healthy; 0 when `unknown` |
| `LiveVersion` | `*uint64` | job version the healthy copies run; nil if none |
| `NomadError` | string | why `unknown` (logged; shown as a mono detail line) |

Rules, in order: latest deployment `pending`/`running` → `rolling_out`; Nomad unreadable →
`unknown`; `HealthyCopies >= WantedCopies` → `healthy`; `HealthyCopies > 0` → `degraded`; else
`not_running`.

### Copy (derived, `service.Copy`, from `deploy.Copy`)

| Field | Type | Notes |
| --- | --- | --- |
| `ID` | string | Nomad allocation ID; the short form (first 8) is shown, the full in `title` |
| `Service` | string | task group = service name |
| `Node` | string | node name |
| `Version` | uint64 | job version |
| `Status` | `starting` \| `healthy` \| `unhealthy` \| `stopped` \| `failed` | R8 table |
| `Restarts` | int | sum of the task's restart count |
| `StartedAt` | `*time.Time` | first start of the task; nil while pending |
| `Description` | string | Nomad's client description, mono detail |

Listed: every copy not yet terminal, plus terminal copies of the latest job version Nomad still
keeps (R8). Ordered: service, then version descending, then start time.

### Rollout progress (derived, `service.Progress`)

| Field | Type |
| --- | --- |
| `Wanted`, `Placed`, `Healthy`, `Unhealthy` | int (summed over task groups) |
| `CanariesWanted` | int |
| `Promoted` | bool |
| `Description` | string (Nomad's status description) |

Only for a deployment whose status is `running`; nil otherwise or when Nomad has no deployment
for that version yet.

### Deployment (exists, changed)

`app.Trigger` gains `console` (`app.TriggerConsole`). Everything else as in 003. The pages
derive from it:

- **Duration**: `UpdatedAt − CreatedAt` for final statuses; "running for 40 s" while running.
- **Explanation sentence** (FR-014), from status + `Error` + the previous healthy deployment:

| Status | Sentence |
| --- | --- |
| `healthy` | "v7 is live." |
| `reverted` | "v7 failed its health checks. Nomad rolled back to v6, which is serving." |
| `failed` before registration (no job version) | "v7 didn't start: the image `x:1.5` wasn't found. v6 keeps serving." (cause from the error kind) |
| `failed` during rollout | "v7 failed during its rollout. v6 keeps serving." |
| `superseded` | "v7 was replaced by v8 before it finished." |
| `pending` / `running` | "Rolling out v7." + progress |

The raw `Error` is shown under it in mono.

### Log line (transient, `service.LogLine`)

| Field | Type | Notes |
| --- | --- | --- |
| `Copy` | string | allocation ID |
| `Service` | string | for the label |
| `Stream` | `stdout` \| `stderr` | |
| `Text` | string | one line without its newline; valid UTF-8; at most 64 KiB |
| `Time` | `*time.Time` | when caravana received it while following; nil for the tail |

Limits: the tail reads up to 64 KiB from the end of each (copy, stream) and keeps the last 500
lines overall (FR-017); the page keeps at most 5,000 lines (spec edge cases).

### Change notice (transient, `service.Change`)

| Field | Type | Notes |
| --- | --- | --- |
| `AppID` | string | "" for notices about all apps |
| `What` | `apps` \| `activity` \| `app` \| `deployments` \| `copies` \| `deleted` | which regions refresh |

Who publishes what:

| Event | Notices |
| --- | --- |
| deployment record created or updated | `deployments`, `app` for its app; `apps`, `activity` |
| app created or saved | `app`; `apps` |
| app deleted | `deleted` for its app; `apps`, `activity` |
| Nomad allocations of an app changed (R6) | `copies`, `app` for its app; `apps` |

### Webhook token, API token, session (exist)

Unchanged. `service.Auth.CheckSessionQuiet` reads a session without renewing it (R11).

## State: an open page's live stream

```text
connect ──▶ session valid? ── no ──▶ send `ended`, close
                │ yes
                ▼
           send `sync` ──▶ wait for: notice │ 20 s heartbeat │ 30 s session re-check │ disconnect
                              │                      │                  │                   │
                         coalesce 500 ms,      comment line      gone? send `ended`,    stop,
                         send `<what>`                           close                  unsubscribe
```

On the Logs tab the stream also sends `logs-reset`, then the tail as `log` events, then each new
`log` line as it comes.

## Validation rules added

- **New app form** → `app.App{ID: name, Source: image, Services: [{Name: name, Image, Ports,
  Domains, Health}]}`; field paths from `app.FieldError` map back to form fields: `id` → name,
  `services[0].image` → image, `services[0].ports[0].containerPort` → port,
  `services[0].domains[0].host` → domain, `services[0].health.path` → health path. A taken name
  is `id` with "An app called `blog` already exists."
- **Deploy form**: only `services[i].image` may change; any other field arriving in the form is
  ignored by construction (the handler copies only images onto the stored app).
- **Delete**: the typed name must equal the app ID exactly; otherwise `422` with the field
  error "Type `blog` to confirm." and nothing changes.
- **Logs query**: `copy` must be one of the app's listed copies (else `not_found`), `stream` is
  `stdout`, `stderr` or `both` (default `both`), `lines` 1–500 (default 500).
