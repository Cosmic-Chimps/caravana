# Implementation Plan: Control Plane Skeleton and API Contract

**Branch**: `002-control-plane-skeleton` | **Date**: 2026-10-02 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from `specs/002-control-plane-skeleton/spec.md`

## Summary

Turn `cmd/caravana` into a real server that 003 and 004 only add endpoints to. Config comes
from env vars and flags (`internal/config`), with the translator settings checked by feature
001's own rules. The HTTP layer (`internal/api`) has one endpoint, `GET /v1/health`, plus the
shared plumbing: errors returned from handlers become the standard JSON body; JSON 404/405 and
no redirects; request decoding with `encoding/json/v2` for full field paths; logging that never
touches headers, queries or bodies. A pure-Go SQLite store (`internal/store`) has embedded
forward-only migrations, a lock file, apps as versioned JSON documents and a deployments table
whose status changes are enforced. `api/openapi.yaml` starts here, and a test keeps it equal to
the route table.

## Technical Context

**Language/Version**: Go 1.27 (go1.27.1)

**Primary Dependencies**: standard library (`net/http`, `log/slog`, `flag`, `database/sql`,
`embed`, `encoding/json` and `encoding/json/v2`, `syscall`); `modernc.org/sqlite v1.60.1`
(runtime, R1); `github.com/getkin/kin-openapi v0.149.0` (tests only, R11)

**Storage**: SQLite file `<dataDir>/caravana.db` (WAL, foreign keys, STRICT tables)

**Testing**: `testing` + `net/http/httptest`; table tests; temp dirs (`t.TempDir()`) for every
store test; contract test with kin-openapi

**Target Platform**: linux/amd64 and linux/arm64, CGO off; macOS for development

**Project Type**: single Go binary (control plane)

**Performance Goals**: health within 2 s of start on the lab Mac (SC-001); a few requests per
minute in Phase 1, so one SQLite connection is enough

**Constraints**: no CGO; loopback by default; nothing secret ever logged; no redirects; every
I/O function takes `context.Context` first

**Scale/Scope**: 3 new packages, about 12 source files, 1 migration, 1 endpoint

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | How this plan complies | Status |
| --- | --- | --- |
| I. Small by Default | One binary + SQLite. New modules: `modernc.org/sqlite` (named by the constitution) and `kin-openapi` (test-only, not in the binary); both justified in the commit message. No runtime service. | ✅ |
| II. One Model | The store saves `app.App` itself as the document; nothing reads `Source`. | ✅ |
| III. Safe, Reversible Deploys | One deployment record per deploy; idempotent `SaveApp` (identical document changes nothing, R13); statuses include `reverted`. | ✅ |
| IV. Fail Loudly | Unknown body fields rejected with their full path (R4); bad config names the setting; a newer store or app document is refused, not half-read. | ✅ |
| V. API-First | `api/openapi.yaml` in the same change as the endpoint, with a sync test; charset parameters accepted; JSON errors with the right status; no redirects (R7). Field names match feature 001's paths. No UI. | ✅ |
| VI. Nomad-Native | No Nomad code here; `nomadjob` only gains an exported `Settings.Validate`. | ✅ |
| VII. Secure by Default | Loopback default plus a warning otherwise (no auth until 006); logs carry only method, path, status, duration (R10), proven by a test. | ✅ |
| VIII. Plain, Tested Go | Table tests everywhere; no panics in library code (one recover in the HTTP adapter); `ctx` first; no interfaces (the health 503 test uses a closed real store). | ✅ |
| Technology Constraints | `net/http` routing patterns, `modernc.org/sqlite`, `tool`-pinned staticcheck unchanged. | ✅ |

No violations, so Complexity Tracking is empty.

**Post-design re-check**: still passes. Two design choices worth knowing:
- Two JSON packages (R4, R5): v2 for decoding request bodies only, v1 for everything else,
  until Go decides on v2's duration format.
- JSON tags added to feature 001's model (R6), which doesn't change any translator golden file.

## Project Structure

### Documentation (this feature)

```text
specs/002-control-plane-skeleton/
├── plan.md
├── research.md            # R1–R14
├── data-model.md          # config, schema, records, transitions, errors
├── quickstart.md
├── contracts/
│   ├── openapi.yaml       # initial api/openapi.yaml
│   └── go-packages.md     # store, api, nomadjob, cmd contracts for 003/004
├── checklists/requirements.md
└── tasks.md               # /speckit-tasks
```

### Source Code (repository root)

```text
api/
└── openapi.yaml                      # from contracts/openapi.yaml

cmd/caravana/
├── main.go                           # main → run(ctx, args, getenv, stderr) → exit code
└── main_test.go                      # start in a temp dir, health, cancel, exit 0

internal/config/
├── config.go                         # Config, Load(args, getenv), Loopback()
└── config_test.go

internal/api/
├── api.go                            # New, routes table, root handler (R7), handler adapter (R8)
├── errors.go                         # Error, codes, writeError, writeJSON
├── decode.go                         # decodeJSON (R4), JSON pointer → field path
├── logging.go                        # request logging middleware, status recorder
├── health.go                         # GET /v1/health
├── api_test.go                       # 404/405/unclean paths, 500, panic, health, redaction
├── decode_test.go                    # decode table
└── contract_test.go                  # kin-openapi: validate, sync with routes, error schema

internal/store/
├── store.go                          # Open, Close, Ping, lock (R3), errors
├── migrate.go                        # embedded runner (R2)
├── migrations/0001_init.sql
├── apps.go                           # SaveApp, GetApp
├── deployments.go                    # Status, records, Create/Get/Update/List, transitions
├── migrate_test.go
├── apps_test.go
└── deployments_test.go

internal/app/
├── app.go, deployment.go, errors.go  # + camelCase json tags (R6)
└── apptest/fixtures.go               # NEW: the eight feature-001 shapes, shared by tests

internal/nomadjob/
├── translate.go                      # validateSettings → exported Settings.Validate
└── translate_test.go                 # uses apptest fixtures; goldens unchanged

.gitignore                            # + /data/
CLAUDE.md                             # + run command, where we are
```

**Structure Decision**: the packages `docs/go-guide.md` already reserves (`internal/api`,
`internal/store`), plus `internal/config` to keep `main` to wiring. `internal/service` isn't
created yet: there's no business logic until 003, and an empty layer would be a placeholder.
`apptest` holds fixtures as plain exported functions so `nomadjob` and `store` tests share the
eight shapes (SC-007) instead of copying them.

## Implementation notes for /speckit-tasks

- **Order**: setup (deps, folders, `.gitignore`) → foundations (app JSON tags + `apptest`,
  `Settings.Validate`, `api.Error`) → US1 (config, store open/migrate/lock/ping, health, main) →
  US2 (decode, 404/405/redirect guard, adapter, 500/panic, logging + redaction) → US3 (apps and
  deployments in the store) → US4 (`api/openapi.yaml`, contract test, error-schema checks) →
  polish (CLAUDE.md, CI checks).
- **US1 needs the store to open, migrate and ping**, so `store.go`, `migrate.go` and the
  migration land in US1; app and deployment methods are US3.
- **Go idioms to explain**: `//go:embed`; `database/sql` with a driver registered by a blank
  import (`_ "modernc.org/sqlite"`); `errors.Is` with sentinel errors (C#: exception types);
  `defer` for cleanup (C#: `using`/`finally`); `context.Context` cancellation and
  `signal.NotifyContext`; `httptest` (C#: `WebApplicationFactory`/`TestServer`); goroutines
  for `ListenAndServe`; handler functions that return `error`; struct tags; `t.TempDir()`.
- **Commit message for dependencies**: "Add modernc.org/sqlite (pure-Go SQLite, the store the
  constitution names; no CGO) and github.com/getkin/kin-openapi (test-only: validates
  api/openapi.yaml as OpenAPI 3.1 and keeps it in sync with the routes)."

## Complexity Tracking

No violations to justify.
