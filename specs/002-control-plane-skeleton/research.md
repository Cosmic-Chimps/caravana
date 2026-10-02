# Research: Control Plane Skeleton and API Contract

Phase 0 output for [plan.md](plan.md). Each entry: decision, rationale, alternatives. Every
library fact below was checked on the local go1.27.1 on 2026-10-02.

## R1. SQLite driver

- **Decision**: `modernc.org/sqlite v1.60.1` through `database/sql`, driver name `"sqlite"`.
  DSN `file:<dir>/caravana.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)`.
  One open connection (`db.SetMaxOpenConns(1)`).
- **Rationale**: the constitution names it (pure Go, no CGO). A trial build ran SQLite 3.53.4
  with WAL, foreign keys and `STRICT` tables, and cross-compiled with `CGO_ENABLED=0` for
  linux/amd64 and linux/arm64 (binary about 10 MB). It adds 9 small indirect modules
  (`modernc.org/libc`, `mathutil`, `memory`, `google/uuid`, `go-humanize`, `go-isatty`,
  `go-strftime`, `bigfft`, `x/sys`). One connection serializes every query: SQLite has a single
  writer anyway, and Phase 1 traffic is a few requests a minute, so this rules out `SQLITE_BUSY`
  with no tuning.
- **Alternatives**: `mattn/go-sqlite3` (rejected: CGO); `ncruces/go-sqlite3` (WASM-based, pure
  Go, but not the one the constitution names).

## R2. Migrations

- **Decision**: our own small runner in `internal/store`. Migrations are `.sql` files embedded
  with `//go:embed migrations/*.sql`, named `NNNN_name.sql` (`0001_init.sql`). A table
  `schema_migrations(version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL)`
  records each one. At startup: read the applied versions; if the store has a version this
  binary doesn't know, fail ("store was migrated by a newer Caravana"). Then apply the missing
  ones in order, each in its own transaction together with its `schema_migrations` row.
- **Rationale**: about 60 lines of plain Go the maintainer can read in one sitting, and no new
  dependency. Forward-only matches the spec. A transaction per migration means a failure leaves
  the store at the previous version (SQLite DDL is transactional).
- **Alternatives**: `pressly/goose` or `golang-migrate` (rejected: dependencies and down
  migrations we don't want).

## R3. One process per data directory

- **Decision**: `syscall.Flock(fd, LOCK_EX|LOCK_NB)` on `<dataDir>/caravana.lock`, held for the
  process lifetime and released by `Store.Close` (and by the OS if the process dies).
- **Rationale**: an advisory lock is released automatically on crash, so a stale lock never
  blocks a restart (a PID file would). It works on Linux (the target) and macOS (dev). Windows
  isn't a target.
- **Alternatives**: SQLite `locking_mode=EXCLUSIVE` (rejected: it only fails the second process
  at its first query, not at startup, and interacts badly with WAL); a PID file (stale after
  crashes).

## R4. Decoding request bodies: `encoding/json/v2`

- **Decision**: decode request bodies with `json.UnmarshalRead(body, v,
  json.RejectUnknownMembers(true))` from `encoding/json/v2`.
- **Rationale**: tested on go1.27.1 (no experiment flag). Unknown members fail with a
  `*json.SemanticError` whose `JSONPointer` is the full path, e.g.
  `/services/0/ports/0/bogus`, which converts directly to feature 001's format
  `services[0].ports[0].bogus`. Trailing data (`{} {}`) and truncated JSON fail. Names match
  case-sensitively (`"ID"` is unknown when the field is `"id"`). v1 only reports the leaf name
  (`unknown field "bogus"`).
- **Limitation**: a JSON pointer can't tell a map key from a struct field. A segment of digits
  is written as `[n]`, others as `.name`.
- **Alternatives**: v1 `Decoder.DisallowUnknownFields` (rejected: no path for nested fields).

## R5. Encoding stored documents: `encoding/json` (v1)

- **Decision**: the store marshals the app document, digests and warnings with v1
  `encoding/json`. API responses also use v1.
- **Rationale**: v2 refuses `time.Duration` without an explicit format ("no default
  representation", go.dev/issue/71631, still open), and the `format:` tag option failed in a
  trial on go1.27.1. v1 writes durations as integer nanoseconds and round-trips the model
  exactly (`*int` nil vs 2, empty vs set slices, durations), which the trial confirmed. To keep
  it simple, the rule is: **v2 only for decoding request bodies, v1 everywhere else**.
- **Alternatives**: a custom duration type in the model (rejected: changes feature 001's types
  for a storage detail).

## R6. JSON tags on the app model

- **Decision**: add camelCase `json` tags to every type in `internal/app` (`id`, `services`,
  `containerPort`, `cpuMHz`, `memoryMB`, …), `omitempty` on optional fields. The tag names are
  the same names as feature 001's error paths. `Deployment` and `Warning` get tags too.
- **Rationale**: the stored document needs stable names that don't change when a Go field is
  renamed. Using the same names as the error paths (and the future API bodies, FR-015) means
  one vocabulary. Renaming a tag later is a schema change: it bumps `schema_version`.
- **Alternatives**: a separate document struct in `store` (rejected: two copies of the model to
  keep in sync, for no gain yet).

## R7. 404, 405, redirects and the standard `ServeMux`

- **Decision**: the API's root handler does three things before the mux:
  1. If `r.URL.Path` isn't canonical (`path.Clean(p) != p`, which also catches a trailing
     slash, `//` and `..`), answer a JSON 404. The mux would otherwise redirect.
  2. Ask `mux.Handler(r)` for the pattern. An empty pattern means the mux would answer a
     plain-text 404 or 405. Run its handler into a small recorder, keep the status and the
     `Allow` header, and write the JSON error instead.
  3. Otherwise serve normally.
- **Rationale**: Go's `ServeMux` answers unknown paths and wrong methods in plain text, and
  redirects unclean paths with 301. `ServeMux.Handler` returns an empty pattern exactly for
  the not-found and method-not-allowed cases (checked in `go doc`). Recording the mux's own
  reply keeps its exact 404/405 decision and its `Allow` header, without re-implementing
  routing.
- **Alternatives**: a catch-all `/` route (rejected: it turns every wrong method into 404); a
  third-party router (rejected: constitution says `net/http` patterns).

## R8. Handlers return errors

- **Decision**: handlers have the shape `func(w http.ResponseWriter, r *http.Request) error`. One
  adapter turns a returned error into the JSON body: `*api.Error` as is, `*app.FieldError` → 400
  `invalid_field`, `store.ErrNotFound` → 404 `not_found`, anything else → 500 `internal` (the
  error is logged, the client sees "internal error"). A recovered panic also becomes a 500.
- **Rationale**: one place maps errors to statuses, so 003/004 handlers just `return err`. It's
  the Go version of a C# exception filter, but with explicit return values.

## R9. Configuration

- **Decision**: `config.Load(args []string, getenv func(string) string) (Config, error)`. A
  `flag.FlagSet` whose defaults come from the environment, so a flag wins over the env, which
  wins over the built-in default. Names:

  | Setting | Env | Flag | Default |
  | --- | --- | --- | --- |
  | Listen address | `CARAVANA_LISTEN` | `-listen` | `127.0.0.1:7070` |
  | Data directory | `CARAVANA_DATA_DIR` | `-data-dir` | `data` |
  | Traefik entrypoint | `CARAVANA_TRAEFIK_ENTRYPOINT` | `-traefik-entrypoint` | `websecure` |
  | Certificate resolver | `CARAVANA_CERT_RESOLVER` | `-cert-resolver` | `""` |
  | Default CPU (MHz) | `CARAVANA_DEFAULT_CPU_MHZ` | `-default-cpu-mhz` | `100` |
  | Default memory (MB) | `CARAVANA_DEFAULT_MEMORY_MB` | `-default-memory-mb` | `64` |

- **Rationale**: passing `args` and `getenv` makes `Load` testable without touching the real
  environment. Port 7070 avoids the lab's forwarded ports (8080, 8081, 8443, 4646). `data` is
  relative to the working directory and git-ignored (`/data/` added to `.gitignore`). The
  translator settings are checked by `nomadjob.Settings.Validate`, which this feature exports
  from 001's unexported `validateSettings`, so both use exactly the same rules (FR-005).
- **Loopback check**: `net.SplitHostPort`, then the host is loopback when it's `localhost` or
  parses to an IP with `IsLoopback()`. An empty host (`:7070`) means all interfaces, which is
  not loopback and gets the warning.
- **Alternatives**: a config file (rejected: not needed yet); a flags library (rejected: the
  standard `flag` package is enough).

## R10. Logging

- **Decision**: `log/slog` with a JSON handler on stderr. The request middleware logs exactly
  `method`, `path`, `status`, `durationMs`. Nothing else from the request is read for logging.
- **Rationale**: the safest redaction is never touching what might be secret. Headers, query
  strings and bodies are never passed to the logger, so there is nothing to redact. A test
  plants a secret in a header, the query string and the body, and checks the captured log.

## R11. Validating the contract

- **Decision**: `github.com/getkin/kin-openapi v0.149.0`, imported **only from tests**. The
  contract test loads `api/openapi.yaml`, checks `IsOpenAPI31OrLater()`, runs
  `doc.Validate(ctx)`, compares its operations with the server's route table, and validates
  error bodies from other tests against `components.schemas.Error`.
- **Rationale**: a trial loaded and validated a 3.1 document, rejected a malformed schema
  (`required: status`) and a dangling `$ref`, and listed operations for the sync test. 7
  indirect modules, none in the binary because only `_test.go` files import it. It will also
  validate request and response bodies in 003's tests (`openapi3filter`).
- **Alternatives**: `pb33f/libopenapi` (also fine, slightly larger tree); a Node linter in CI
  (rejected: a Node toolchain for one check); a hand-written YAML check (rejected: not real
  validation).

## R12. Error codes in the contract

- **Decision**: `code` is a plain string in the schema, and the known values are listed in its
  description, not as an `enum`.
- **Rationale**: generated clients often reject unknown enum values. 003 and 004 will add codes,
  and a new code mustn't break an older .NET client.

## R13. Idempotent save

- **Decision**: `SaveApp` upserts with `INSERT … ON CONFLICT(id) DO UPDATE SET … WHERE
  apps.document != excluded.document`. Saving an identical app changes nothing, not even
  `updated_at`.
- **Rationale**: constitution III: "the same payload twice changes nothing except a new
  deployment record". Marshaling is deterministic (v1 sorts map keys; struct order is fixed), so
  comparing documents as text is exact.

## R14. Server timeouts and shutdown

- **Decision**: `http.Server{ReadHeaderTimeout: 10s, ReadTimeout: 30s, WriteTimeout: 30s,
  IdleTimeout: 120s}`; `signal.NotifyContext` for SIGINT/SIGTERM; `Shutdown` with a 10 s
  context. The process exits 1 if shutdown times out.
- **Rationale**: standard protections against slow clients. The 10 s shutdown timeout is a
  constant, not a setting (FR-003 lists the only settings).
