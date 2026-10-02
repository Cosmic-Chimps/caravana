---

description: "Task list for feature 003: deploy flow"
---

# Tasks: Deploy Flow

**Input**: Design documents from `specs/003-deploy-flow/`

**Prerequisites**: [plan.md](plan.md), [spec.md](spec.md), [research.md](research.md),
[data-model.md](data-model.md), [contracts/](contracts/), [quickstart.md](quickstart.md)

**Tests**: Required (constitution VIII, spec FR-023). In each story, write the tests first and
watch them fail.

**Organization**: US1 = deploy an image and see it live (MVP). US2 = redeploy safely (same
payload twice, empty-body redeploy, newer wins). US3 = a broken deploy reverts and says so.
US4 = failures are loud and leave nothing half-done (including restart). US5 = read apps and
deployment history.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: can run in parallel (different files, no dependency on an unfinished task)
- **[Story]**: US1–US5 from spec.md

## Conventions for every task

- Every function doing I/O takes `ctx context.Context` first. No `panic` in library code.
- Errors are wrapped with context: `fmt.Errorf("register job %q: %w", id, err)`.
- **JSON**: API bodies (in and out) use `encoding/json/v2` with the option set in
  `internal/api/json.go` (research R5). The store keeps `encoding/json` v1. Nothing else changes
  format.
- **Nomad** is only touched in `internal/deploy` (and `nomadjob`'s types). `main` builds the
  client through `deploy.NewNomadClient`.
- **No network in tests**: registries are `registrytest` servers, Nomad is a `deploytest` server,
  the store is real in `t.TempDir()`. The only exception is `cmd/caravana/lab_test.go`, which
  skips unless `CARAVANA_LAB=1`.
- Timing in tests (poll gaps, backoff, registry timeout) is shortened through unexported fields
  set by same-package tests, never with `time.Sleep` guesses. Waiting for a final status uses a
  helper that polls the store with a deadline.
- Never log, return or put in an error message the Nomad token or a registry token.
- Keep `gofmt -l .` empty after every task. Feature 001's golden files must pass without
  `-update` throughout.

---

## Phase 1: Setup

- [X] T001 Create the package folders `internal/registry/registrytest/`,
  `internal/deploy/deploytest/` and `internal/service/`, each new package with a `doc` comment
  in its first file (written by the task that creates it). No `go get`: every import is already
  in `go.mod` (plan: no new module).

---

## Phase 2: Foundational (blocks every story)

- [X] T002 [P] Split app validation in `internal/app/validate.go`: export
  `func ValidateApp(a App) error` (steps 1–2 of `Validate`'s documented order) and make
  `Validate(a, d)` call `ValidateApp` then `validateDeployment`, with the same order and
  messages. Add cases to `internal/app/validate_test.go` showing `ValidateApp` reports the same
  first error as `Validate` for every app-level case, and accepts an app with no deployment.
- [X] T003 Add `func Check(a app.App, s Settings) error` to `internal/nomadjob/translate.go`
  (`s.Validate()`, `app.ValidateApp(a)`, then `checkNomadLimits` per service) and make
  `Translate` call `Check` before `validateDeployment`'s checks via `app.Validate`. Keep it pure.
  In `internal/nomadjob/translate_test.go` add a table: every invalid case that fails
  `Translate` because of the app or settings fails `Check` with the same `*app.FieldError`.
  Goldens unchanged. (Needs T002.)
- [X] T004 [P] Store changes in `internal/store/deployments.go` (no migration, data-model "Store
  changes"): add `Digests map[string]string` to `DeploymentUpdate` (nil keeps the stored
  value); change `ListDeployments(ctx, appID, limit int)` (`limit <= 0` means all; add
  `LIMIT ?` otherwise); add `ListUnfinishedDeployments(ctx)` returning every `pending` or
  `running` deployment, oldest first. Update callers in `internal/store/deployments_test.go` and
  add table cases: digests update, limit 1/2/0, unfinished excludes the four final statuses.
- [X] T005 [P] Nomad settings in `internal/config/config.go` (research R13): `type Secret
  string` with `String()`, `GoString()` and `LogValue() slog.Value` returning `[redacted]`, and
  `Reveal() string`; `Config.NomadAddr` (`NOMAD_ADDR` / `-nomad-addr`, default
  `http://127.0.0.1:4646`, must be an `http`/`https` URL with a host, else an error naming
  `NOMAD_ADDR / -nomad-addr`); `Config.NomadToken Secret` from `NOMAD_TOKEN` only (no flag). In
  `internal/config/config_test.go`: defaults, env, flag over env, bad addresses
  (`127.0.0.1:4646` without scheme, `ftp://x`, `http://`), and a test that formats the config
  with `%v`, `%+v`, `%#v` and through `slog.JSONHandler` and finds the token 0 times.
- [X] T006 [P] API JSON option set in new `internal/api/json.go` (research R5):
  `durationMarshalers` (zero → `""`, else `Duration.String()`), `durationUnmarshalers`
  (string only, `time.ParseDuration`; a non-string fails with `must be a duration string such as
  "10s"`, a bad string with `%q is not a duration`), `json.Deterministic(true)`. Make
  `decodeJSON` in `internal/api/decode.go` use them (plus `RejectUnknownMembers`), and
  `writeJSON` in `internal/api/errors.go` encode with v2 and the same marshalers. Map a
  `*json.SemanticError` whose cause is our duration error to `invalid_field` with the pointer's
  field path and our message. Add cases to `internal/api/decode_test.go` with a struct holding a
  `time.Duration`: `"10s"`, `"1m30s"`, `10` → field path + message, `"ten"`, omitted → 0, and an
  encode case where 0 is left out. Existing health and error bodies keep the same JSON values;
  keep v1's trailing newline (`\n` after the value) so byte comparisons in existing tests still
  hold.
- [X] T007 [P] Error additions in `internal/api/errors.go` (research R14): codes
  `codeImageNotFound = "image_not_found"`, `codeJobConflict = "job_conflict"`,
  `codeRegistryUnavailable = "registry_unavailable"`, `codeNomadUnavailable =
  "nomad_unavailable"`, `codeNomadRejected = "nomad_rejected"`; add `Location string
  \`json:"-"\`` to `Error`, and make `writeError` set the `Location` header when it isn't empty.
- [X] T008 [P] Extract the schema check from `TestContractBodies` in
  `internal/api/contract_test.go` into package-level test helpers
  `checkSchema(t, doc, raw []byte, schema string)` and `loadContract(t)` (already there), so
  every API test can validate a body against `api/openapi.yaml` (FR-005). No behavior change.
- [X] T009 [P] Fake registry in new `internal/registry/registrytest/registry.go` (research R12):
  `New(t) *Server` wrapping `httptest.NewServer` (closed with `t.Cleanup`), serving
  `HEAD`/`GET /v2/<repo>/manifests/<ref>`. Methods: `AddIndex(repo, tag, digest)` and
  `AddManifest(repo, tag, digest)` (answer with the right `Content-Type` and
  `Docker-Content-Digest`, only when the request's `Accept` lists that media type);
  `OmitDigestHeader(repo, tag)` (GET body whose SHA-256 is the digest);
  `RequireToken(true)` (401 with `WWW-Authenticate: Bearer realm="<srv>/token",service="test",scope="repository:<repo>:pull"`
  until `Authorization: Bearer <issued token>`; `GET /token` issues it); `Fail(repo, tag,
  status)`; `FailToken(status)`; `Hold(repo, tag) (release func())` (blocks the manifest
  request until released); `Requests() int`; `URL() string`; `Endpoints() map[string]string`
  (both `docker.io` and `ghcr.io` → the server URL).
- [X] T010 [P] Fake Nomad in new `internal/deploy/deploytest/nomad.go` (research R12), using
  `github.com/hashicorp/nomad/api` types for bodies and the paths the client uses:
  `GET /v1/job/{id}` (404 body `job not found`), `PUT /v1/jobs` (a `JobRegisterRequest`: honor
  `EnforceIndex`/`JobModifyIndex` with Nomad's text `Enforcing job modify index %d: job exists
  with conflicting job modify index: %d`, 500; on success store the job with `Version` = previous
  + 1 and a new `JobModifyIndex`, answer a `JobRegisterResponse`), `GET /v1/job/{id}/versions`
  (a `JobVersionsResponse`, newest first) and `GET /v1/job/{id}/deployments` (`[]*api.Deployment`).
  Set `X-Nomad-Index` on every query answer. Methods: `New(t) *Nomad`;
  `Script(jobVersion uint64, states ...api.Deployment)` (each deployments request for that
  version returns the next state; the last one repeats; no script means no deployment yet);
  `AddForeignJob(job *api.Job)`; `RejectNext(status int, body string)`; `RequireToken(token)`
  (403 `Permission denied` otherwise); `Down()` / `Up()` (while down, every request gets its
  connection closed); `Registrations() int`; `Job(id) *api.Job`; `Addr() string`.

**Checkpoint**: `go test ./...` passes; no behavior change visible to users yet.

---

## Phase 3: User Story 1 - Deploy an image and see it live (Priority: P1) 🎯 MVP

**Goal**: `POST /v1/apps/{id}/deployments` with the whoami app pins the image, registers the
job, answers 202 with deployment ID, job version and warnings, and the deployment reaches
`healthy`.

**Independent Test**: service and API tests with `registrytest` + `deploytest`; then quickstart
§3 in the lab.

### Tests for User Story 1 ⚠️

- [X] T011 [P] [US1] Table tests for `Parse` and `Pinned` in
  `internal/registry/reference_test.go` (research R2, data-model "Image reference"):
  `traefik/whoami:latest`, `traefik/whoami` (→ `latest`), `nginx` (→ `library/nginx`, Name
  `nginx`), `docker.io/library/nginx:1.27`, `index.docker.io/traefik/whoami:v1`,
  `ghcr.io/acme/shop:main`, digest-only, tag+digest (digest wins, `Tag` empty, `IgnoredTag`
  set to the tag); errors for `""`,
  `Traefik/whoami`, `whoami:bad tag!`, `x@sha256:short`, `quay.io/x/y:1` and
  `localhost:5000/x:1` (unsupported registry, message says "Docker Hub and GHCR only").
- [X] T012 [P] [US1] Tests for `Client.Resolve` in `internal/registry/client_test.go` against
  `registrytest`: multi-arch tag → the index digest (FR-009); single-arch → manifest digest;
  token handshake (first request 401, token fetched, retry carries the bearer token, the token
  text appears in no returned error); digest-only reference → 0 requests; missing
  `Docker-Content-Digest` → GET and SHA-256 of the body; a header that isn't
  `sha256:<64 hex>` → `ErrUnavailable`; the result is `Name@sha256:…` for `traefik/whoami`,
  `nginx` and `ghcr.io/acme/shop`.
- [X] T013 [P] [US1] Tests for `Deployer.Validate` and `Deployer.Resolve` in
  `internal/deploy/deploy_test.go`: app rule errors and Nomad limit errors pass through as
  `*app.FieldError`; an unparseable or unsupported image is a `*app.FieldError` on
  `services[i].image`; `Resolve` returns service name → pinned image for a two-service app and
  leaves digest-only images untouched; a `traefik/whoami:v1@sha256:…` service gives one
  `app.Warning` on `services[0].image` saying the tag is ignored (constitution IV).
- [X] T014 [P] [US1] Tests for `Register` and `FindVersion` in
  `internal/deploy/register_test.go` against `deploytest`: no existing job → registered,
  returned version matches the job version whose `caravana.deployment` meta is ours; existing
  job owned per the `owns` callback → registered with the enforced modify index; the registered
  job's image is the digest, never the tag; Nomad register warnings become one `app.Warning`
  with empty field and `nomad: ` prefix; `Versions` answering 500 three times after a successful
  register → no error, `JobVersion == nil`, exactly 1 registration (research R6);
  `FindVersion` finds a version by deployment ID and reports not found otherwise.
- [X] T015 [P] [US1] Tests for `Watch` in `internal/deploy/watch_test.go` against
  `deploytest`: no deployment yet, then `running`, then `successful` → `OutcomeHealthy`; every
  keep-watching status from research R8 keeps watching; the request uses the last
  `X-Nomad-Index` as wait index.
- [X] T016 [P] [US1] Service tests in `internal/service/deploy_test.go` (helper
  `newTestService(t)` returning service, store, `*registrytest.Server`, `*deploytest.Nomad`, and
  `waitFinal(t, st, id)` polling the store with a 5 s deadline): deploying whoami with
  `traefik/whoami:latest` returns a `running` record with job version and digest, the app is
  stored, and the record ends `healthy`; the deploy's request context cancelled right after
  `Deploy` returns doesn't stop the record reaching `healthy` (research R11).
- [X] T017 [P] [US1] API tests in new `internal/api/deployments_test.go` (helper
  `newDeployTestServer(t)` wiring a real store, `registrytest`, `deploytest`, deployer, service
  and `api.New`): `POST /v1/apps/whoami/deployments` with `application/json; charset=UTF-8`
  → 202, `Location: /v1/apps/whoami/deployments/<id>`, body validates against `Deployment`
  (T008 helper) with `jobVersion`, `digests.web` and `warnings: []`; body without `id` takes the
  path's; a `traefik/whoami:v1@sha256:…` image → `warnings` has one entry on
  `services[0].image`; `GET` that location → 200 `Deployment`; unknown deployment ID → 404; a
  deployment of another app → 404.

### Implementation for User Story 1

- [X] T018 [US1] `internal/registry/reference.go`: `Reference`, `Parse`, `Pinned`, per research
  R2 (doc comment on the package: tag → digest for Docker Hub and GHCR, anonymous only).
- [X] T019 [US1] `internal/registry/client.go`: `Client{HTTP, Endpoints}`, `ErrNotFound`,
  `ErrUnavailable`, `Resolve` with the `HEAD` + `Accept` list, `Docker-Content-Digest`, GET
  fallback with SHA-256, digest format check, and the bearer-token handshake (research R3, R4).
  Real endpoints: `docker.io` → `https://registry-1.docker.io`, `ghcr.io` → `https://ghcr.io`.
  (Error statuses and retries come in T047.)
- [X] T020 [US1] `internal/deploy/deploy.go`: package doc; `Deployer`, `New(nomad, reg,
  settings)`, `NewNomadClient(addr, token)` (`api.DefaultConfig()`, then set `Address` and
  `SecretID`), error values and `ImageError` (with `Unwrap`) from contracts/go-packages.md;
  `Validate` (`nomadjob.Check`, then `registry.Parse` per service → `*app.FieldError` on
  `services[i].image`); `Resolve` returning `(digests, warnings, error)` (per service in order,
  10 s budget each; a warning for each `IgnoredTag`; failures as `*ImageError` wrapping
  `ErrImageNotFound` or `ErrRegistryUnavailable`).
- [X] T021 [US1] `internal/deploy/register.go`: `Registered`; `Register` per research R6/R7
  (`Translate`, `Jobs().Info`, ownership via `owns` and `Stop`, `RegisterOpts` with
  `EnforceIndex`, `Versions` lookup by `caravana.deployment` meta retried twice (0.5 s, 1 s),
  then `JobVersion: nil` without an error because Nomad did accept the job, Nomad warnings);
  `FindVersion`. Every call uses `WithContext(ctx)`. (Conflict and error mapping come in T048.)
- [X] T022 [US1] `internal/deploy/watch.go`: `Result`, `Outcome`, `Watch` with blocking queries
  (`WaitIndex`, `WaitTime` 5 min), picking the deployment whose `JobVersion` is ours,
  `successful` → healthy, keep-watching statuses, and an unexported minimum poll gap (1 s).
  (Superseded, rollback and backoff mapping come in T033, T039, T049.)
- [X] T023 [US1] `internal/service/service.go`: package doc (business logic shared by the API,
  the webhook (004) and the UI (007)); `Service`, `New(st, deployer, log)`, `Start(ctx)` (keeps
  the background context; resume comes in T052), `Wait()` (`sync.WaitGroup`),
  `GetDeployment(ctx, appID, deploymentID)` (`store.ErrNotFound` when it belongs to another
  app), and `DeployError{AppID, DeploymentID, Err}` with `Error` and `Unwrap`.
- [X] T024 [US1] `internal/service/deploy.go`: `Deploy(ctx, a, trigger)`: `Validate` (return
  the `*app.FieldError` as-is, nothing saved) → `SaveApp` → `CreateDeployment` with ID
  `strings.ToLower(rand.Text())` → work under `context.WithoutCancel(ctx)`, with the 25 s budget
  starting once T034's app lock is taken (research R11) → `Resolve` → `Register` (the `owns`
  callback checks `store.GetDeployment` and its `AppID`) → `UpdateDeployment` to `running` with
  the job version if known, digests, and the resolve warnings followed by `Register`'s; if that
  write fails, log it and carry on → start the watcher goroutine on the background context
  (WaitGroup). The watcher first makes sure the record is `running` with a version (retrying
  the write; `FindVersion` with backoff when the version is unknown), then records the outcome
  (`healthy`/`failed`/`reverted`/`superseded`, `error` = Nomad's description). A failure
  before Nomad accepted the job updates the record to `failed` with the error's message and
  returns a `*DeployError` (data-model lifecycle table).
- [X] T025 [US1] `internal/api/api.go`: `New(svc *service.Service, st *store.Store, log)`;
  `server` gains `svc`; routes `POST /v1/apps/{id}/deployments` and
  `GET /v1/apps/{id}/deployments/{deploymentId}`. Update `newTestServer` in
  `internal/api/api_test.go` (and `cmd/caravana/main_test.go` if needed) for the new signature.
- [X] T026 [US1] New `internal/api/deployments.go`: `deploymentJSON` (fields and omissions from
  data-model "Deployment": `jobVersion` omitted when nil, `error` omitted when empty, `digests`
  `{}` and `warnings` `[]` never null) built from `store.DeploymentRecord`; the deploy handler
  (decode `app.App`, `id` empty → path's, different → 400 `invalid_field` on `id`; call
  `svc.Deploy` with `app.TriggerManual`; 202 + `Location`; raise its own write deadline to 60 s
  with `http.NewResponseController(w).SetWriteDeadline`, research R11); the get-deployment
  handler.
- [X] T027 [US1] Wire it in `cmd/caravana/main.go` (contracts/go-packages.md "cmd/caravana"):
  `deploy.NewNomadClient(cfg.NomadAddr, cfg.NomadToken.Reveal())`, `&registry.Client{}`,
  `deploy.New`, `service.New`, `svc.Start(bgCtx)`, `api.New(svc, st, log)`; log `nomadAddr`
  at startup, never the token; on shutdown: HTTP `Shutdown` → cancel `bgCtx` → `svc.Wait()` →
  close the store. `cmd/caravana/main_test.go` must still start, answer health and stop with
  no Nomad running.
- [X] T028 [US1] Update `api/openapi.yaml` from `specs/003-deploy-flow/contracts/openapi.yaml`
  with **only** the operations served so far: `deployApp` (`POST /v1/apps/{id}/deployments`) and
  `getDeployment`, plus every component. `getApp` and `listDeployments` are added in T057, so
  `TestContractMatchesRoutes` passes at each step.

**Checkpoint**: `go test -race ./...` passes. In the lab, quickstart §3's first deploy answers
202 and reaches `healthy`, and Nomad's job shows a digest.

---

## Phase 4: User Story 2 - Redeploy safely and idempotently (Priority: P1)

**Goal**: the same payload twice is a new record and job version with the app unchanged; an
empty body redeploys the stored app; overlapping deploys of one app end with the newest live and
the others `superseded`; different apps don't wait for each other.

**Independent Test**: `go test -race ./internal/service ./internal/api ./internal/deploy`.

### Tests for User Story 2 ⚠️

- [X] T029 [P] [US2] Service tests in `internal/service/deploy_test.go`: same payload twice → 2
  records, job versions N and N+1, `GetApp` identical with the same `UpdatedAt` (SC-003);
  `Redeploy(ctx, "whoami", app.TriggerWebhook)` deploys the stored app with trigger `webhook`;
  `Redeploy` of an unknown app → `store.ErrNotFound` and no record; a moved tag (change the
  digest in `registrytest` between deploys) pins the new digest while the stored image stays the
  tag; a deploy with a missing tag, then `Redeploy` → fails the same way again with a new
  `failed` record (spec "Redeploy after a failed deploy").
- [X] T030 [P] [US2] Newer-wins tests in `internal/service/concurrency_test.go` (run with
  `-race`): A's registry answer held with `registrytest.Hold`; B for the same app started in a
  goroutine (it blocks on the app lock); release A → A ends `superseded` without registering
  (`deploytest.Registrations()` counts only B's) and B registers and is live (FR-019); with the
  budget shortened to 200 ms, A held 300 ms then released → B still ends live, not `failed`
  (the budget starts after the lock, research R11); five deploys of one app at once → exactly one not `superseded`, and the
  job Nomad holds carries the newest request's deployment ID (SC-006); app `a` held while app
  `b` deploys → `b` returns without waiting (FR-020).
- [X] T031 [P] [US2] Watch tests in `internal/deploy/watch_test.go`: `cancelled` with
  `Cancelled due to newer version of job` → `OutcomeSuperseded` with Nomad's description; the
  job's version newer than ours and no deployment for ours → `OutcomeSuperseded`.
- [X] T032 [P] [US2] API tests in `internal/api/deployments_test.go`: empty body (no
  `Content-Type`) on a stored app → 202 and a new deployment with trigger `manual`; empty body on
  an unknown app → 404 `not_found`, no record; body `{}` → 400 `invalid_field` (no services);
  a deploy superseded before registration → 202 with `status: superseded` and no `jobVersion`.

### Implementation for User Story 2

- [X] T033 [US2] `internal/deploy/watch.go`: map `cancelled` (newer job → superseded, anything
  else → failed with the description) and the newer-version-without-deployment case (research
  R8).
- [X] T034 [US2] `internal/service/deploy.go`: per-app state (`map[string]*appState` under a
  `sync.Mutex`; `appState{mu sync.Mutex; latest string}`): set `latest` when the record is
  created; hold `appState.mu` from resolution through registration; just before `Register`, if
  `latest` isn't this deployment, update it to `superseded` with "replaced by deployment <id>
  before it was registered" and return the record (research R9). Add a comment explaining why
  the check is needed (`sync.Mutex` isn't FIFO).
- [X] T035 [US2] `Redeploy(ctx, appID, trigger)` in `internal/service/deploy.go`: `GetApp`, then
  the same path as `Deploy` without saving.
- [X] T036 [US2] Optional body for deploy: in `internal/api/decode.go` add
  `decodeOptionalJSON(r, v) (present bool, err error)` (empty body → `false, nil` with no
  `Content-Type` check; otherwise exactly `decodeJSON`'s rules); in
  `internal/api/deployments.go` call `svc.Redeploy` when no body was sent.

**Checkpoint**: `go test -race ./...` passes; quickstart §3's "send the same body again" holds in
the lab.

---

## Phase 5: User Story 3 - A broken deploy reverts and says so (Priority: P2)

**Goal**: a rollout Nomad fails and rolls back ends `reverted`; one with nothing to roll back to
ends `failed`; both carry Nomad's reason.

**Independent Test**: scripted rollouts in `deploytest`; then quickstart §4 in the lab.

### Tests for User Story 3 ⚠️

- [X] T037 [P] [US3] Watch tests in `internal/deploy/watch_test.go` (table, research R8):
  `failed` + `Failed due to unhealthy allocations - rolling back to job version 3` →
  `OutcomeReverted`; `Failed due to progress deadline - rolling back to job version 3` →
  reverted; `Failed due to unhealthy allocations - no stable job version to auto revert to` →
  `OutcomeFailed`; `Failed due to progress deadline` → failed; `Deployment marked as failed` →
  failed; `cancelled` + `Cancelled because job is stopped` → failed. Each outcome's message is
  the description.
- [X] T038 [P] [US3] Service tests in `internal/service/deploy_test.go`: a healthy deploy, then
  one scripted to fail with rollback → the second record ends `reverted` with the description in
  `error`, the first stays `healthy`; an app's first deploy scripted to fail with no rollback
  target → `failed` (spec US3 scenario 2).

### Implementation for User Story 3

- [X] T039 [US3] `internal/deploy/watch.go`: description constants (`descNewerJob`,
  `descRollbackMarker = " - rolling back to job version "`) with a comment saying they come
  from `nomad/structs` and the lab test confirms them; map `failed` to reverted or failed.

**Checkpoint**: `go test ./internal/deploy ./internal/service` passes; quickstart §4 ends
`reverted` in the lab with the previous version still answering.

---

## Phase 6: User Story 4 - Failures are loud, clear and leave nothing half-done (Priority: P2)

**Goal**: every failure has the right status, code, field and `Location`; nothing that fails
before Nomad accepts the job registers anything; a lost Nomad connection never fails a record;
a restart loses no outcome.

**Independent Test**: error-case tests in `registry`, `deploy`, `service` and `api`, plus the
resume tests.

### Tests for User Story 4 ⚠️

- [X] T040 [P] [US4] Registry error tests in `internal/registry/client_test.go` (FR-011,
  research R3/R4): manifest 404 → `ErrNotFound`; 401 after a valid token → `ErrNotFound` whose
  message says "or private"; token endpoint 401 and 403 → `ErrNotFound`; 429 → `ErrUnavailable`
  saying the registry is rate-limiting, no retry; 500 → retried twice (3 requests) then
  `ErrUnavailable`; closed listener → `ErrUnavailable`; a held request beyond the (shortened)
  per-image budget → `ErrUnavailable`. Every error names the registry host and the image.
- [X] T041 [P] [US4] Register error tests in `internal/deploy/register_test.go`: a foreign
  running job (`caravana.deployment: "lab-manual"`, `owns` false) → `ErrJobConflict` naming the
  job, 0 registrations; the same job stopped → registered; enforce-index conflict (job changed
  between `Info` and register) → `ErrJobConflict`; `RejectNext(400, "task group web: ...")` →
  `ErrNomadRejected` whose message contains Nomad's text; `RequireToken` with a wrong token →
  `ErrNomadUnavailable` "Nomad refused the token" and the token value appears nowhere in the
  error; `Down()` → `ErrNomadUnavailable` naming the address.
- [X] T042 [P] [US4] Watch resilience tests in `internal/deploy/watch_test.go`: Nomad `Down()`
  while `running`, then `Up()` with `successful` → `OutcomeHealthy` (FR-015); context cancelled
  while Nomad is down → returns `ctx.Err()` promptly; backoff doubles from the (shortened)
  start and caps.
- [X] T043 [P] [US4] Service failure tests in `internal/service/deploy_test.go`: invalid app,
  unparseable image and unsupported registry → `*app.FieldError`, no app saved, no record;
  unknown tag → `*DeployError` with the deployment ID, record `failed`, app saved, 0
  registrations (SC-005); registry down → `failed`; Nomad down → `failed`; foreign job →
  `failed` with `ErrJobConflict`; Nomad rejects → `failed` with Nomad's text; two services
  where the second image is missing → no registration and the error names `services[1].image`;
  register succeeds but `Versions` keeps failing → no error, record `running` without a
  version, then `healthy` with the version filled in once `Versions` answers.
- [X] T044 [P] [US4] API error tests in `internal/api/deployments_test.go`, each body checked
  against the `Error` schema (T008): 422 `image_not_found` with `field: services[0].image` and
  `Location`; 409 `job_conflict` with `Location`; 502 `registry_unavailable` (field set), 502
  `nomad_unavailable` and 502 `nomad_rejected` (message has Nomad's text), each with `Location`;
  400 `invalid_field` for `quay.io/x/y:1` (field `services[0].image`), `"interval": 10` (field
  `services[0].health.interval`) and body `id` ≠ path (field `id`), none with `Location`; 415
  and 413 still apply to deploy bodies.
- [X] T045 [P] [US4] Secret-leak test in `internal/api/deployments_test.go` (SC-009): with
  `deploytest.RequireToken("s3cr3t-token")` and a wrong then right token, capture the server's
  log output and every response body; `s3cr3t-token` appears 0 times.
- [X] T046 [P] [US4] Resume tests in new `internal/service/resume_test.go` (research R10): a
  store with a `running` record at version 3 → `Start` watches it to `healthy`; a `pending`
  record whose ID is in a Nomad job version → `running` with that version, then `healthy`; a
  `pending` record unknown to Nomad → `failed` with "Caravana stopped before registering this
  deployment"; Nomad `Down()` at `Start` → `Start` returns at once, and the records resolve
  after `Up()`; `Wait()` returns after the context is cancelled with records left `running`.

### Implementation for User Story 4

- [X] T047 [US4] Error statuses and retries in `internal/registry/client.go` per T040: 404 /
  post-token 401/403 / token refusal → `ErrNotFound` with "image not found, or private
  (Caravana doesn't support registry credentials yet)"; 429 → `ErrUnavailable` (rate-limiting,
  no retry); network errors and 5xx → two retries (0.5 s, 1 s; unexported for tests) then
  `ErrUnavailable`; errors wrapped as `fmt.Errorf("%s: %s: %w", host, image, err)`.
- [X] T048 [US4] Conflict and error mapping in `internal/deploy/register.go` per research R6/R7:
  `errors.As(err, &api.UnexpectedResponseError{})` (a value type) for status and body; foreign
  job → `ErrJobConflict` "Nomad already runs a job %q that Caravana didn't create"; the
  enforce-index text → `ErrJobConflict` "the Nomad job changed while deploying; try again";
  403 → `ErrNomadUnavailable` "Nomad refused the token"; other 4xx/5xx on register →
  `ErrNomadRejected` with the body; connection errors → `ErrNomadUnavailable` "Nomad is
  unreachable at <addr>".
- [X] T049 [US4] Backoff in `internal/deploy/watch.go`: on any error, wait (1 s doubling to
  30 s, unexported for tests) or return `ctx.Err()`; never return an outcome because of an
  error (FR-015).
- [X] T050 [US4] Failure paths in `internal/service/deploy.go` per T043: every error after
  `CreateDeployment` and before Nomad accepts the job becomes `failed` with the client-facing
  message and a `*DeployError`; if that record update itself fails, log it and return the
  original error. Once Nomad has accepted the job, never return an error: log, and let the
  watcher bring the record up to date (research R6).
- [X] T051 [US4] Error mapping in `toAPIError` in `internal/api/api.go` (research R14):
  `*service.DeployError` sets `Location` and maps its cause; `*deploy.ImageError` with
  `ErrImageNotFound` → 422 `image_not_found` (field), with `ErrRegistryUnavailable` → 502
  `registry_unavailable` (field); `ErrJobConflict` → 409; `ErrNomadUnavailable` and
  `ErrNomadRejected` → 502 with their codes; messages are the error text (never containing a
  token, by construction in T047/T048).
- [X] T052 [US4] Resume in new `internal/service/resume.go`: `Start(ctx)` lists unfinished
  deployments and, per record, in its own goroutine (WaitGroup): `running` with a version →
  watch; `pending` → `FindVersion` (retrying with the watcher's backoff while Nomad is down) →
  `running` + version + watch, or `failed` with "Caravana stopped before registering this
  deployment". `Start` itself never blocks on Nomad.

**Checkpoint**: `go test -race ./...` passes; quickstart §5's table holds in the lab.

---

## Phase 7: User Story 5 - Read apps and their deployment history (Priority: P3)

**Goal**: `GET /v1/apps/{id}` returns the app as sent; `GET /v1/apps/{id}/deployments` lists
newest first with `limit`.

**Independent Test**: API tests against seeded data, bodies validated against the contract.

### Tests for User Story 5 ⚠️

- [X] T053 [P] [US5] API tests in new `internal/api/apps_test.go`: deploy whoami with
  `"interval": "10s"` and `"timeout": "2s"`, then `GET /v1/apps/whoami` → 200 `AppRecord` whose
  `app` equals the sent body (durations as the same strings, `id` filled in), and POSTing that
  `app` back is accepted; unknown app → 404.
- [X] T054 [P] [US5] List tests in `internal/api/deployments_test.go`: 25 seeded deployments →
  default 20 newest first; `limit=5` → 5; `limit=100` → 25; `limit=0`, `101` and `abc` → 400
  `invalid_field` with `field: limit`; an app with no deployments → `{"deployments": []}`;
  unknown app → 404. Bodies validate against `DeploymentList`.

### Implementation for User Story 5

- [X] T055 [US5] `GetApp(ctx, id)` and `ListDeployments(ctx, appID, limit)` in
  `internal/service/service.go` (thin pass-throughs to the store).
- [X] T056 [US5] New `internal/api/apps.go` (`GET /v1/apps/{id}` → `{"app", "createdAt",
  "updatedAt"}`) and the list handler in `internal/api/deployments.go` (`limit` default 20,
  1–100, else 400 on `limit`; `{"deployments": [...]}`); register both routes in
  `internal/api/api.go`.
- [X] T057 [US5] Add `getApp` and `listDeployments` to `api/openapi.yaml`, which now equals
  `specs/003-deploy-flow/contracts/openapi.yaml` exactly (`diff` prints nothing).

**Checkpoint**: `go test ./internal/api` passes, including `TestContractMatchesRoutes`.

---

## Phase 8: Polish & Cross-Cutting Concerns

- [X] T058 [P] Extend `TestContractBodies` in `internal/api/contract_test.go`: real responses for
  every new schema (`Deployment`, `DeploymentList`, `AppRecord`, and each new error response),
  plus "schemas reject wrong bodies" cases: a numeric duration in `App`, an unknown `status` in
  `Deployment`, a digest without `@sha256:`.
- [X] T059 [P] Opt-in lab test in new `cmd/caravana/lab_test.go` (research R12, quickstart §6):
  skip unless `CARAVANA_LAB=1`; run `run(...)` in-process on a free port with the lab's
  `NOMAD_ADDR`/`NOMAD_TOKEN`; purge any `whoami` job first; then: deploy whoami
  (`traefik/whoami:latest`) → 202 within 10 s, `healthy`, `https://whoami.localhost:8443`
  answers (TLS verify off), Nomad's job image is a digest; same payload again → new record,
  version +1; `containerPort: 81` → `reverted`, the URL keeps answering during the rollout, and
  afterwards the job's `caravana.deployment` meta is the earlier healthy deployment's ID;
  `traefik/whoami:does-not-exist` → 422 with no new job version. Asserting `reverted` confirms
  research R8's description strings against real Nomad.
- [X] T060 [P] Update `CLAUDE.md` "Where we are" (feature 003 on branch `003-deploy-flow`:
  registry, deploy, service, the four endpoints; next: 004) and add the lab test command from
  quickstart §6 to Commands. Update `docs/plan.md` open decision 1: settled, own registry client
  (research R1).
- [X] T061 Run every CI check from quickstart §1 (`go build`, `go vet`, `go tool staticcheck`,
  `go test -race`, `gofmt -l`, `go mod tidy -diff`, both cross-compiles) and fix anything they
  report. Then ask the maintainer to run quickstart §3–§6 in the lab (needs `lab.sh up`).
- [X] T062 Write the summary for the maintainer: what changed per package, the two spec
  refinements made during planning (FR-013a ownership, FR-021 env-only token), and the Go idioms
  introduced (`sync.Mutex` isn't FIFO, goroutines + `WaitGroup`, `context.WithoutCancel`,
  `errors.As` with a value type, custom error types with `Unwrap`, `slog.LogValuer`,
  `httptest.Server` as a fake upstream, json/v2 `WithMarshalers`, `crypto/rand.Text`), one line
  each with the C# comparison.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (T001)** → **Foundational (T002–T010)** → stories.
- **US1 (T011–T028)**: after Foundational. Builds the whole pipeline the other stories extend.
- **US2 (T029–T036)**: after US1 (extends `service/deploy.go`, `watch.go`, the deploy handler).
- **US3 (T037–T039)**: after US1. Independent of US2 except that both edit `watch.go`
  (T033 and T039): do them one after the other.
- **US4 (T040–T052)**: after US1. T049 edits `watch.go` too, after T033/T039.
- **US5 (T053–T057)**: after US1 only (reads; its own handler file). Can run alongside US2–US4.
- **Polish (T058–T062)**: last. T058 needs T057; T061 before T062.

### Within each story

Tests first and failing, then implementation, then the checkpoint. T003 needs T002. T020 needs
T018–T019. T024 needs T020–T023. T027 needs T024–T026. T028 needs T025. T052 needs T049.

### Parallel Opportunities

- Foundational: T002, T004, T005, T006, T007, T008, T009, T010 (different files); T003 after T002.
- US1 tests T011–T017 together (five packages); implementation is a chain across packages:
  registry (T018, T019) ∥ the deploy tests, then deploy (T020–T022), then service (T023, T024),
  then api (T025, T026) ∥ T028, then main (T027).
- After US1: US5 (api reads) in parallel with US2/US3/US4 (service and deploy work).
- Within US4: the tests T040–T046 are all in different files or independent cases.

## Parallel Example: User Story 1

```text
Tests at once:        T011, T012 (registry) · T013, T014, T015 (deploy) · T016 (service) · T017 (api)
Then implementation:  T018 → T019 → T020 → T021 → T022 → T023 → T024 → T025 → T026 → T027
                      T028 alongside T026
```

## Implementation Strategy

### MVP first (US1)

Phases 1–3: deploy whoami through the API, follow it to `healthy`, call it over HTTPS in the
lab. That alone proves the core of the Phase 1 gate apart from the webhook (004).

### Incremental delivery

1. US1: deploys work end to end.
2. US2: redeploys are safe, which is what 004's webhook needs.
3. US3: broken deploys are recorded truthfully (the second half of the Phase 1 gate, proven on a
   VM in 005).
4. US4: every failure is loud; restarts lose nothing.
5. US5: history and app reads for clients and the future UI.
6. Polish: contract fully checked, lab test, docs, every CI check.

## Notes

- `nomadjob.Translate`'s output must not change: run `go test ./internal/nomadjob` without
  `-update` after T003.
- `deploy` must not import `store` or `service`; `service` must not import Nomad's `api`
  package; `api` imports `deploy`/`registry` only for error values.
- The same `traefik/whoami@sha256:…` digest from `deploy/dev/whoami.json` is a good fixture
  value for `registrytest`.
- Commit when the maintainer asks. The commit message notes "no new module" (plan.md).
