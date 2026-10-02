---

description: "Task list for feature 002: control plane skeleton and API contract"
---

# Tasks: Control Plane Skeleton and API Contract

**Input**: Design documents from `specs/002-control-plane-skeleton/`

**Prerequisites**: [plan.md](plan.md), [spec.md](spec.md), [research.md](research.md),
[data-model.md](data-model.md), [contracts/](contracts/), [quickstart.md](quickstart.md)

**Tests**: Required (constitution VIII). In each story, write the tests first and watch them
fail.

**Organization**: US1 = start and health (MVP). US2 = JSON conventions and errors. US3 = the
store's apps and deployments. US4 = the contract.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: can run in parallel (different files, no dependency on an unfinished task)
- **[Story]**: US1–US4 from spec.md

## Conventions for every task

- Every function doing I/O takes `ctx context.Context` first. No `panic` in library code.
- Errors are wrapped with context: `fmt.Errorf("open store %q: %w", path, err)`.
- Request bodies are decoded with `encoding/json/v2`; everything else uses `encoding/json`
  (research R4, R5).
- Tests use `t.TempDir()` for every data directory and `httptest` for HTTP. No test touches the
  real environment: config tests pass a fake `getenv`.
- Keep `gofmt -l .` empty after every task.

---

## Phase 1: Setup

- [X] T001 Add the dependencies at the repo root: `go get modernc.org/sqlite@v1.60.1
  github.com/getkin/kin-openapi@v0.149.0`. Don't run `go mod tidy` until code imports them
  (T016, T030).
- [X] T002 Create folders `api/`, `internal/config/`, `internal/api/`,
  `internal/store/migrations/`, `internal/app/apptest/`, and add `/data/` to `.gitignore` under
  "Local data" (the default data directory, research R9).

---

## Phase 2: Foundational (blocks every story)

- [X] T003 [P] Add camelCase `json` tags to `internal/app` (research R6), `omitempty` on every
  optional field: `App` → `id`, `name`, `source`, `services`; `Source` → `kind`; `Service` →
  `name`, `image`, `command`, `args`, `env`, `ports`, `domains`, `replicas`, `resources`,
  `health`; `EnvVar` → `name`, `value`; `Port` → `name`, `containerPort`; `Domain` → `host`,
  `port`; `HealthCheck` → `path`, `port`, `interval`, `timeout`; `Resources` → `cpuMHz`,
  `memoryMB`; `Deployment` (in `deployment.go`) → `id`, `trigger`, `digests`; `Warning` and
  `FieldError` (in `errors.go`) → `field`, `message`. Add a sentence to the `app.go` package doc:
  the tags are the stored document format and the API's field names; renaming one is a schema
  change.
- [X] T004 [P] Create `internal/app/apptest/fixtures.go` (package `apptest`, doc: "test
  fixtures shared by package tests; not used by product code"): `type Case struct{ Name string;
  App app.App; Deployment app.Deployment }` and `func Cases() []Case` returning fresh values for
  `whoami`, `minimal-web`, `multi-domain`, `worker`, `internal-service`, `custom-resources` and
  `multi-service`, with exactly the inputs now in `internal/nomadjob/translate_test.go` (move
  `whoamiApp`, `whoamiDeployment`, `digest`, `deployment`, `imageApp`, `blogWeb`, `jobsWorker`
  here as exported or unexported helpers). Also export `Whoami() Case`.
- [X] T005 Update `internal/nomadjob/translate_test.go` to build `goldenCases()` from
  `apptest.Cases()` (the `letsencrypt` case = `apptest.Whoami()` with `CertResolver:
  "letsencrypt"`) and use `apptest.Whoami()` in the other tests. Delete the moved helpers. Run
  `go test ./internal/nomadjob` **without** `-update`: every golden must still match. Depends on
  T004.
- [X] T006 [P] In `internal/nomadjob/translate.go`, rename `validateSettings(s Settings)` to the
  exported method `func (s Settings) Validate() error` with a doc comment (also used by
  `internal/config`), and call `s.Validate()` from `Translate`.
- [X] T007 [P] Create `internal/api/errors.go`: `type Error struct{ Status int `json:"-"`; Code,
  Message string; Field string `json:"field,omitempty"` }` (json `code`, `message`) with
  `Error() string`; constants for the nine codes in data-model.md; `func writeJSON(w
  http.ResponseWriter, status int, v any)` (sets `Content-Type: application/json`, writes v1
  JSON); `func writeError(w http.ResponseWriter, e *Error)` (writes `{"error": e}` with
  `e.Status`).

**Checkpoint**: `go build ./... && go test ./internal/...` pass; goldens unchanged.

---

## Phase 3: User Story 1 - An operator starts Caravana and sees it's healthy (Priority: P1) 🎯 MVP

**Goal**: `go run ./cmd/caravana` starts with defaults, migrates its store, answers health, and
stops cleanly.

**Independent Test**: `go test ./internal/config ./internal/store ./internal/api ./cmd/caravana
-run 'TestLoad|TestOpen|TestMigrate|TestLock|TestHealth|TestRun' -v`, then quickstart §2's first
block.

### Tests for User Story 1

- [X] T008 [P] [US1] Create `internal/config/config_test.go`: table `TestLoad` calling
  `Load(args, getenv)` with a map-backed `getenv`. Cases: no input → defaults from research R9;
  each env var sets its field; a flag beats the env var; and failures whose error contains both
  the env var and the flag name: `-listen nope`, `-listen 127.0.0.1:0`, `-listen
  127.0.0.1:99999`, `-data-dir ""`, `CARAVANA_DEFAULT_CPU_MHZ=abc`,
  `CARAVANA_DEFAULT_MEMORY_MB=-5`, `CARAVANA_TRAEFIK_ENTRYPOINT=a,b`,
  `CARAVANA_CERT_RESOLVER="le tls"`, an unknown flag. Table `TestLoopback`: `127.0.0.1:7070`,
  `localhost:7070`, `[::1]:7070` → true; `:7070`, `0.0.0.0:7070`, `192.168.1.10:7070`,
  `example.com:7070` → false.
- [X] T009 [P] [US1] Create `internal/store/migrate_test.go`: `TestOpen` (Open on
  `t.TempDir()/nested/dir` creates the folder, `caravana.db` and `caravana.lock`;
  `schema_migrations` holds version 1; Close; Open again works); `TestMigrate` (calling
  `migrate(ctx, db, fsys)` with the embedded migrations returns 1 applied on an empty DB, then 0;
  with an `fstest.MapFS` holding a good `0001_a.sql` and a bad `0002_b.sql`, it fails naming
  `0002_b.sql` and only version 1 is recorded); `TestOpenStoreTooNew` (insert version 999 into
  `schema_migrations`, Close, Open fails with `errors.Is(err, ErrStoreTooNew)`); `TestLock`
  (a second Open on the same dir fails with `ErrLocked`; after the first Close it succeeds);
  `TestPing` (nil when open, an error after Close).
- [X] T010 [P] [US1] Create `internal/api/api_test.go` with a helper `newTestServer(t) (url
  string, st *store.Store, logs *bytes.Buffer)` (store on `t.TempDir()`, `slog` JSON handler
  writing into the buffer, `httptest.NewServer(api.New(st, log))`, cleanup closes both), and
  `TestHealth`: GET `/v1/health` → 200, `Content-Type: application/json`, body exactly
  `{"status":"ok"}`; after `st.Close()` → 503 with code `unavailable`.
- [X] T011 [US1] Create `cmd/caravana/main_test.go`: `TestRun` picks a free port (`net.Listen`
  on `127.0.0.1:0`, read the address, close it), runs `run(ctx, []string{"-listen", addr,
  "-data-dir", dir}, fakeGetenv, &stderr)` in a goroutine, polls `/v1/health` until 200 (fail
  after 2 s, SC-001), cancels the context, and asserts exit code 0 and that `dir/caravana.db`
  exists. `TestRunBadConfig`: `CARAVANA_DEFAULT_MEMORY_MB=-5` → exit code 1 and stderr contains
  `CARAVANA_DEFAULT_MEMORY_MB`. Run the US1 tests and confirm they fail to compile or fail.

### Implementation for User Story 1

- [X] T012 [US1] Create `internal/config/config.go`: `Config{Listen, DataDir string; Settings
  nomadjob.Settings}`; `func Load(args []string, getenv func(string) string) (Config, error)`
  using `flag.NewFlagSet("caravana", flag.ContinueOnError)` whose defaults come from the env
  (research R9 table), ints parsed with `strconv.Atoi` (error names env and flag); listen
  checked with `net.SplitHostPort` and a port of 1–65535; then `Settings.Validate()`, mapping
  the `*app.FieldError` field (`settings.entrypoint`, `settings.certResolver`,
  `settings.defaultCPUMHz`, `settings.defaultMemoryMB`) to `"<ENV> / -<flag>: <message>"`.
  `func (c Config) Loopback() bool` per research R9.
- [X] T013 [US1] Create `internal/store/store.go`: package doc; sentinel errors `ErrNotFound`,
  `ErrAlreadyExists`, `ErrInvalidTransition`, `ErrSchemaTooNew`, `ErrStoreTooNew`, `ErrLocked`;
  `type Store struct{ db *sql.DB; lock *os.File; now func() time.Time }`; `func Open(ctx, dataDir
  string) (*Store, error)`: `os.MkdirAll(dataDir, 0o700)`, open `caravana.lock` and
  `syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)` (on `EWOULDBLOCK` return a wrapped
  `ErrLocked` naming the directory), `sql.Open("sqlite", dsn)` with the DSN from research R1,
  `db.SetMaxOpenConns(1)`, `PingContext`, `migrate`; on any failure, release what was opened.
  `Close()` closes the DB, unlocks and closes the lock file. `Ping(ctx)` = `db.PingContext`.
  Blank-import `_ "modernc.org/sqlite"`.
- [X] T014 [US1] Create `internal/store/migrate.go` and
  `internal/store/migrations/0001_init.sql` (the `apps` and `deployments` tables and index from
  data-model.md): `//go:embed migrations/*.sql` into `var migrationsFS embed.FS`; `func
  migrate(ctx, db *sql.DB, fsys fs.FS) (applied int, err error)` per research R2 (create
  `schema_migrations` if missing; parse `NNNN_name.sql`; refuse a stored version this binary
  doesn't have with `ErrStoreTooNew`; apply the missing ones in order, each in one transaction
  together with its `schema_migrations` row; wrap errors with the file name). `Open` calls it
  with `fs.Sub(migrationsFS, "migrations")`.
- [X] T015 [US1] Create `internal/api/api.go` and `internal/api/health.go`: `type handlerFunc
  func(w http.ResponseWriter, r *http.Request) error`; `type route struct{ method, path string;
  handle handlerFunc }`; `func (s *server) routes() []route` returning `{GET, /v1/health,
  s.health}`; `func New(st *store.Store, log *slog.Logger) http.Handler` registering
  `method+" "+path` on a `http.ServeMux` through the adapter `s.handle(h handlerFunc)
  http.HandlerFunc`, which, for now, writes a returned `*Error` as is and anything else as 500
  `internal` "internal error" (logging the real error). `health`: `st.Ping` with a 2 s timeout;
  ok → `writeJSON(w, 200, map[string]string{"status": "ok"})`; error → log it and return
  `&Error{Status: 503, Code: "unavailable", Message: "store is not reachable"}`.
- [X] T016 [US1] Replace `cmd/caravana/main.go`: `main` builds `ctx` with
  `signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)` and calls
  `os.Exit(run(ctx, os.Args[1:], os.Getenv, os.Stderr))`. `run`: JSON `slog` logger on stderr;
  `config.Load` (on error, log it and return 1); log `starting` with listen and data dir; if
  `!cfg.Loopback()`, log a warning "listening on a non-loopback address; the API has no
  authentication yet"; `store.Open` (return 1 on error); log migrations applied; `http.Server`
  with the timeouts from research R14 and `api.New`; `ListenAndServe` in a goroutine reporting
  into a channel; wait for `ctx.Done()` or a server error; `Shutdown` with a 10 s timeout; close
  the store; return 0, or 1 if the server failed or shutdown timed out. Then `go mod tidy`.
- [X] T017 [US1] Run the US1 tests (all pass), then quickstart §2's first block by hand:
  `go run ./cmd/caravana`, `curl -s localhost:7070/v1/health`, Ctrl-C (exit 0), start again (0
  migrations applied).

**Checkpoint**: MVP. The binary runs, migrates, answers health, and stops cleanly.

---

## Phase 4: User Story 2 - API clients get predictable JSON and errors (Priority: P2)

**Goal**: every error is the standard JSON body with the right status; bodies decode with full
field paths; logs never carry secrets.

**Independent Test**: `go test ./internal/api -run 'TestDecode|TestPointerToField|TestNotFound|TestMethodNotAllowed|TestNoRedirect|TestErrorMapping|TestPanic|TestRequestLog' -v`.

### Tests for User Story 2

- [X] T018 [P] [US2] Create `internal/api/decode_test.go`. `TestDecode`: a table of
  `httptest.NewRequest` bodies and `Content-Type`s decoded into `struct{ ID string
  `json:"id"`; Count int `json:"count"`; Services []struct{ Name string `json:"name"`; Ports
  []struct{ Name string `json:"name"` } `json:"ports"` } `json:"services"` }`, asserting the
  returned `*Error` (status, code, field) or nil:
  - ok: `application/json`; `application/json; charset=UTF-8`; `Application/JSON;
    Charset=utf-8`; a body of exactly 1 MiB of valid JSON (pad a string)
  - 415 `unsupported_media_type`: `text/plain`; missing; `application/json; charset=latin1`
  - 400 `unknown_field`: `{"bogus":1}` → field `bogus`;
    `{"services":[{"ports":[{"name":"http","bogus":1}]}]}` → `services[0].ports[0].bogus`;
    `{"ID":"a"}` → `ID`
  - 400 `invalid_field`: `{"count":"x"}` → field `count`
  - 400 `bad_request` (no field): empty body; `{"id":`; `{} {}`
  - 413 `body_too_large`: 1 MiB + 1 byte
  `TestPointerToField`: `/services/0/ports/0/bogus` → `services[0].ports[0].bogus`; `/id` →
  `id`; `/a~1b` → `a/b`; `/a~0b` → `a~b`; `` → ``.
- [X] T019 [P] [US2] Add to `internal/api/api_test.go`: `TestNotFound` (GET `/v1/nope` → 404
  `not_found`, JSON); `TestMethodNotAllowed` (POST `/v1/health` → 405 `method_not_allowed`,
  `Allow` contains `GET`); `TestNoRedirect` (`//v1/health`, `/v1/health/`, `/v1/../v1/health`
  sent with a raw request line, e.g. via `http.NewRequest` + `req.URL.Opaque`, or a client
  `CheckRedirect` that fails the test → 404 JSON, no `Location` header); `TestErrorMapping`
  (call `s.handle` directly with handlers returning `&app.FieldError{Field:
  "services[0].domains[1].port"}` → 400 `invalid_field` with that field;
  `fmt.Errorf("get app: %w", store.ErrNotFound)` → 404 `not_found`; `errors.New("boom")` → 500
  `internal`, the body doesn't contain `boom`, the log does); `TestPanic` (a handler that panics
  → 500 `internal`, the server keeps serving); `TestRequestLog` (GET
  `/v1/health?token=s3cr3t-q` with headers `Authorization: Bearer s3cr3t-a` and `X-Api-Token:
  s3cr3t-h`, plus POST `/v1/health` with body `{"password":"s3cr3t-b"}`: the log has one
  `request` line per request with `method`, `path` `/v1/health`, `status`, `durationMs`, and
  contains no `s3cr3t`). Run and confirm they fail.

### Implementation for User Story 2

- [X] T020 [US2] Create `internal/api/decode.go`: `const maxBodyBytes = 1 << 20`; `func
  decodeJSON(r *http.Request, v any) error`: `mime.ParseMediaType` on `Content-Type`; type must
  be `application/json` and a `charset`, if present, `utf-8` (case-insensitive), else 415; read
  at most `maxBodyBytes+1` with `io.LimitReader` (more → 413; zero bytes → 400 "request body is
  required"); `json.Unmarshal` from `encoding/json/v2` with `json.RejectUnknownMembers(true)`.
  Map errors: `*json.SemanticError` with `errors.Is(err, json.ErrUnknownName)` → 400
  `unknown_field` with `pointerToField(se.JSONPointer)`; other `*json.SemanticError` → 400
  `invalid_field` with its field; anything else → 400 `bad_request` "malformed JSON".
  `func pointerToField(p jsontext.Pointer) string` per research R4 (`~1` → `/`, `~0` → `~`,
  digit segments as `[n]`).
- [X] T021 [US2] In `internal/api/api.go`: make the adapter map `*app.FieldError` → 400
  `invalid_field` (field and message kept) and `errors.Is(err, store.ErrNotFound)` → 404
  `not_found`, and recover panics into 500 `internal` (log the panic value). Wrap the mux in a
  root handler (research R7): non-canonical path (`path.Clean(p) != p`) → 404 JSON; else
  `h, pattern := mux.Handler(r)`; if `pattern == ""`, run `h` into an
  `httptest.NewRecorder()`-style recorder (write a tiny unexported one, not `httptest`, in
  product code), copy its `Allow` header, and write 404 `not_found` or 405 `method_not_allowed`
  JSON; otherwise `mux.ServeHTTP`.
- [X] T022 [US2] Create `internal/api/logging.go`: a `statusRecorder` wrapping
  `http.ResponseWriter` (records the status, default 200); middleware logging one `request` line
  with `method`, `path` (`r.URL.Path`), `status`, `durationMs`, and nothing else. `New` wraps the
  root handler with it, so 404s and 405s are logged too. Run all US1 and US2 tests: pass.

**Checkpoint**: US1 and US2 pass.

---

## Phase 5: User Story 3 - The store records apps and deployments (Priority: P3)

**Goal**: the store functions 003 needs, with idempotent saves and enforced status changes.

**Independent Test**: `go test ./internal/store -run 'TestApps|TestDeployments|TestTransitions' -v`.

### Tests for User Story 3

- [X] T023 [P] [US3] Create `internal/store/apps_test.go`, with a helper `openTestStore(t)`
  that opens on `t.TempDir()` and sets `s.now` to a fake clock advancing 1 s per call.
  `TestAppsRoundTrip`: for every `apptest.Cases()`, `SaveApp` then `GetApp` returns an `App`
  that `reflect.DeepEqual`s the input (SC-007). `TestAppsSave`: first save has `CreatedAt ==
  UpdatedAt`; saving the identical app again leaves `UpdatedAt` unchanged; saving a changed app
  (another replica count) changes `UpdatedAt`, keeps `CreatedAt`, and there's still one row
  (`SELECT count(*) FROM apps`); empty ID fails. `TestAppsErrors`: `GetApp("nope")` →
  `ErrNotFound`; after `UPDATE apps SET schema_version = 2`, `GetApp` → `ErrSchemaTooNew`.
- [X] T024 [P] [US3] Create `internal/store/deployments_test.go`. `TestDeployments`: create for
  an unknown app → `ErrNotFound`; create for a saved app → `pending`, nil `JobVersion`, no
  warnings, empty error, same digests and trigger; duplicate ID → `ErrAlreadyExists`; three
  creates → `ListDeployments` newest first (also with a clock that returns the same time every
  call: insertion order decides); `GetDeployment`/`UpdateDeployment("nope")` → `ErrNotFound`;
  `ListDeployments` for an unknown app → `ErrNotFound`, for an app with none → length 0;
  `running` → `running` with `JobVersion` 3 stores it; `running` → `reverted` with `Error:
  "health check failed"` reads both back; warnings set on update read back; `UpdatedAt` changes
  on every update. `TestTransitions`: a table over every (from, to) pair of the six statuses
  plus `"bogus"`, expecting success exactly for the pairs in data-model.md's diagram; failures
  are `ErrInvalidTransition` and the stored record is unchanged. Run and confirm they fail.

### Implementation for User Story 3

- [X] T025 [US3] Create `internal/store/apps.go`: `const appSchemaVersion = 1`; `AppRecord`;
  `SaveApp` (empty ID → error; v1 `json.Marshal`; `INSERT INTO apps … ON CONFLICT(id) DO UPDATE
  SET schema_version = excluded.schema_version, document = excluded.document, updated_at =
  excluded.updated_at WHERE apps.document != excluded.document`, research R13; then return
  `GetApp`); `GetApp` (`sql.ErrNoRows` → wrapped `ErrNotFound`; `schema_version >
  appSchemaVersion` → wrapped `ErrSchemaTooNew` naming the app and version; unmarshal). Times as
  `time.RFC3339Nano` in UTC, from `s.now()` (default `time.Now`).
- [X] T026 [US3] Create `internal/store/deployments.go`: `Status` and its constants,
  `DeploymentRecord`, `DeploymentUpdate` (data-model.md); `var allowedNext =
  map[Status][]Status{pending: {running, failed, superseded}, running: {running, healthy, failed,
  reverted, superseded}}`; `CreateDeployment` (app must exist → `ErrNotFound`; primary-key
  conflict → `ErrAlreadyExists`; status `pending`; digests and `[]` warnings as JSON);
  `GetDeployment`; `UpdateDeployment` in one transaction: read the row (`ErrNotFound`), check
  `u.Status` is in `allowedNext[current]` (else `ErrInvalidTransition` naming both), apply
  `JobVersion`/`Warnings`/`Error` only when set, set `updated_at`, commit, return the new
  record; `ListDeployments` (app must exist; `ORDER BY created_at DESC, rowid DESC`). Run all
  tests: pass.

**Checkpoint**: US1–US3 pass.

---

## Phase 6: User Story 4 - The contract matches the server (Priority: P4)

**Goal**: `api/openapi.yaml` is valid OpenAPI 3.1, equals the route table, and describes the
bodies the server really sends.

**Independent Test**: `go test ./internal/api -run 'TestContract' -v`.

- [X] T027 [US4] Create `api/openapi.yaml` from
  `specs/002-control-plane-skeleton/contracts/openapi.yaml` (drop the two leading comment
  lines). From now on `api/openapi.yaml` is the source of truth.
- [X] T028 [US4] Create `internal/api/contract_test.go`: load `../../api/openapi.yaml` with
  `openapi3.NewLoader().LoadFromFile`; `TestContractIsValid` (`doc.IsOpenAPI31OrLater()` and
  `doc.Validate(ctx)` is nil); `TestContractMatchesRoutes` (build the set of `"METHOD path"` from
  `doc.Paths.Map()` operations and from `(&server{}).routes()`, then `diffRoutes(contract,
  router) []string` must be empty; failures list each missing pair and which side lacks it);
  `TestDiffRoutes` (a unit table proving `diffRoutes` reports a route missing from either
  side); `TestContractBodies` (against a `newTestServer`: the health 200 body validates against
  `components.schemas.Health`; the bodies of a 404, a 405, an unclean path and a 503 validate
  against `components.schemas.Error`, using `schemaRef.Value.VisitJSON` on the decoded JSON).
  Then `go mod tidy` (kin-openapi stays as a test dependency). Run: pass.

**Checkpoint**: all four stories pass.

---

## Phase 7: Polish & Cross-Cutting Concerns

- [X] T029 [P] Update `CLAUDE.md`: in Commands, add `go run ./cmd/caravana   # 127.0.0.1:7070,
  data in ./data` and `curl -s localhost:7070/v1/health`; in "Where we are", say feature 002 is
  implemented on its branch (server, config, store, `api/openapi.yaml`) and next is feature 003.
- [X] T030 Run every CI check from quickstart §3 at the repo root (`gofmt -l .`, `go mod tidy
  -diff`, `go vet ./...`, `go tool staticcheck ./...`, `go test -race ./...`, both CGO-free
  cross-compiles) and fix anything reported. Then quickstart §2 in full: the curl checks (404,
  405, `//v1/health`), a second process on the same data directory failing, bad config
  failing, and `-listen :7070` logging the warning.
- [X] T031 Write the maintainer summary in the final report (not a file): what was built, the
  Go idioms from plan.md "Implementation notes" with one line each, and the dependency commit
  message from plan.md (constitution I). Commit only when the maintainer asks.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (T001–T002)** → **Foundational (T003–T007)** → stories.
- **US1 (T008–T017)**: after Foundational. Builds the store's open/migrate/lock and the API
  skeleton that the other stories extend.
- **US2 (T018–T022)**: after US1 (extends `api.go` and `api_test.go`).
- **US3 (T023–T026)**: after US1 (needs `store.Open`). Independent of US2: can run alongside it.
- **US4 (T027–T028)**: after US2 (validates the 404/405 bodies US2 produces).
- **Polish (T029–T031)**: last; T030 before T031.

### Within each story

Tests first and failing, then implementation, then the checkpoint run. T005 needs T004. T016
needs T012–T015.

### Parallel Opportunities

- T003, T004, T006, T007 (different files); T005 after T004.
- T008, T009, T010 (three packages).
- US2 and US3 as a whole, once US1 is done (`internal/api` vs `internal/store`).
- T018 with T019 (different files); T023 with T024.

## Parallel Example: after US1

```text
Stream A (internal/api):   T018, T019 → T020 → T021 → T022
Stream B (internal/store): T023, T024 → T025 → T026
```

## Implementation Strategy

### MVP first (US1)

Phases 1–3: a binary that starts, migrates and answers health. Check by hand with quickstart §2.

### Incremental delivery

1. US1: the server runs.
2. US2: every later endpoint gets correct JSON and errors for free.
3. US3: the store is ready for 003.
4. US4: the contract is locked to the routes.
5. Polish: docs, every CI check, summary.

## Notes

- Never change a feature-001 golden file in this feature: T005 must pass without `-update`.
- `internal/api` imports `internal/store` for `ErrNotFound` and `*store.Store`; `store` must
  never import `api`.
- Commit when the maintainer asks; the dependency commit carries the justification from plan.md.
