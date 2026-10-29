# Implementation Plan: Deploy Flow

**Branch**: `003-deploy-flow` | **Date**: 2026-10-02 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from `specs/003-deploy-flow/spec.md`

## Summary

Make Caravana deploy. `POST /v1/apps/{id}/deployments` validates the app (feature 001's rules,
Nomad's limits and every image reference), saves it, creates a `pending` deployment record,
pins every image to a digest with our own small registry client (`internal/registry`: Docker
Hub and GHCR, anonymous token handshake, index digest for multi-arch), checks that any
existing Nomad job with that ID is Caravana's, registers the translated job with an enforced
modify index, and answers 202 with the deployment ID, job version and warnings. A background
watcher in `internal/deploy` follows the Nomad deployment with blocking queries and records
`healthy`, `failed`, `reverted` or `superseded`. `internal/service` holds the flow: a per-app
lock with a "latest request wins" check, deploys that outlive their request, and resuming
unfinished deployments on startup. Three read endpoints, five new error codes, duration strings
in API bodies (v2 marshalers, no model change), `NOMAD_ADDR` and `NOMAD_TOKEN` settings. No new
module, no migration, no golden-file change.

## Technical Context

**Language/Version**: Go 1.27 (go1.27.1)

**Primary Dependencies**: standard library (`net/http`, `crypto/sha256`, `crypto/rand`,
`encoding/json/v2`, `sync`, `context`, `log/slog`); `github.com/hashicorp/nomad/api` (already
required by feature 001, now used for I/O as well); `modernc.org/sqlite` and
`github.com/getkin/kin-openapi` (tests) unchanged. **No new module** (R1).

**Storage**: the feature 002 SQLite store; new methods, no schema change

**Testing**: `testing` + `httptest`; fake registry (`registrytest`) and fake Nomad
(`deploytest`) as real HTTP servers, no interfaces (R12); real store in `t.TempDir()`; contract
validation with kin-openapi; opt-in lab test with `CARAVANA_LAB=1`

**Target Platform**: linux/amd64 and linux/arm64, CGO off; macOS + Lima lab for development

**Project Type**: single Go binary (control plane)

**Performance Goals**: deploy response within 10 s in the lab (SC-001): one `HEAD` (plus a
token request) per image, three Nomad calls; deploys of different apps run in parallel

**Constraints**: the request's work finishes inside the server's 30 s write timeout (25 s
budget, R11); the token is never logged; no floating tags reach Nomad; Nomad unreachable never
fails a running rollout's record

**Scale/Scope**: 3 new packages (`registry`, `deploy`, `service`) + 2 test-helper packages,
4 endpoints (1 existing), 5 error codes, ~15 source files

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | How this plan complies | Status |
| --- | --- | --- |
| I. Small by Default | No new module: our own registry client instead of go-containerregistry's large tree (R1); the Nomad client was already required. No runtime service. | ✅ |
| II. One Model | The request body is `app.App`; nothing reads `Source` after validation. `nomadjob.Translate` stays pure; `Check` is pure too, and golden files don't change. | ✅ |
| III. Safe, Reversible Deploys | Every image is pinned to `repo@sha256:…` on every deploy (R3); every deploy is a new job version (deployment ID in meta); `AutoRevert` comes from the translator and `reverted` is recorded (R8); env is fully replaced because the whole job is re-registered; same payload twice = new record + new version, stored app unchanged (`SaveApp`'s no-op on identical documents). | ✅ |
| IV. Fail Loudly | Every failure has a code, status and field (R14); unsupported registries and bad references fail validation, never silently pass; a foreign Nomad job is a 409, never taken over (R7); a lost Nomad connection is never recorded as a failure (R8). | ✅ |
| V. API-First | The API calls only `internal/service` for apps and deployments; `api/openapi.yaml` changes in the same commit, with the route-sync test and body validation (FR-005); charset parameters still accepted; deploy responses carry deployment ID, job version and warnings; 202 + `Location` for following. Two documented cases answer 202 without `jobVersion`: a deploy a newer request replaced before it reached Nomad (R9), and one Nomad accepted whose version couldn't be read back yet (R6). Both return the deployment ID, and the version shows on the record as soon as it's known. | ✅ |
| VI. Nomad-Native | Official `nomad/api` client, jobs as JSON; `Provider: "nomad"` unchanged; Nomad types only in `deploy` and `nomadjob` (`main` uses `deploy.NewNomadClient`). Export and uninstall untouched. | ✅ |
| VII. Secure by Default | `NOMAD_TOKEN` env only, held in a type that prints `[redacted]` (R13); registry tokens never cached or logged (R4); request logging unchanged (method, path, status, duration). Phase 1 has no secrets in app env (008). | ✅ |
| VIII. Plain, Tested Go | Table tests; fakes as `httptest` servers so no interface is added (R12); errors wrapped with context; `ctx` first everywhere; no panics; `go test -race` covers the concurrency rules. | ✅ |
| Technology Constraints | `net/http` patterns, `encoding/json/v2` at the API boundary (R5), SQLite unchanged, Nomad 2.x, Traefik via tags only. | ✅ |
| Development Workflow | Phase 1 scope only (webhook, HTTPS on a VM, UI, secrets are out). Implementations are ours (no PaaS code copied). | ✅ |

No violations, so Complexity Tracking is empty. The 202 answer without `jobVersion` (R6, R9) is recorded in the V row and in research R6; Principle V as amended in 1.1.0 says it: the answer carries the deployment ID and its `Location`, and the version appears on the record.

**Post-design re-check**: still passes. Three design points worth knowing:
- **Spec refinements made during planning**: FR-013a now defines "Caravana's job" as one
  whose `caravana.deployment` meta names a deployment in this store (the lab's hand-written
  job carries `caravana.*` meta too, R7); FR-021 makes the token env-only (a flag would show it
  in `ps`).
- **Nomad description strings** decide `reverted` vs `failed` vs `superseded` (R8): Nomad has
  no structured field for it. They're constants with fake-Nomad tests, and the lab test
  confirms them against real Nomad.
- **JSON rule from feature 002 changes** to "v2 at the API boundary, v1 in the store" (R5).
  Health and error bodies stay byte-for-byte the same.

## Project Structure

### Documentation (this feature)

```text
specs/003-deploy-flow/
├── plan.md
├── research.md            # R1–R14
├── data-model.md          # settings, references, lifecycle, store changes, resources, errors
├── quickstart.md
├── contracts/
│   ├── openapi.yaml       # replaces api/openapi.yaml
│   └── go-packages.md     # registry, deploy, service, store, api, config contracts
├── checklists/requirements.md
└── tasks.md               # /speckit-tasks
```

### Source Code (repository root)

```text
api/
└── openapi.yaml                      # from contracts/openapi.yaml

cmd/caravana/
├── main.go                           # + Nomad client, registry, deployer, service, Start/Wait
├── main_test.go                      # still starts with no Nomad running
└── lab_test.go                       # NEW: CARAVANA_LAB=1 end-to-end against the lab

internal/config/
├── config.go                         # + NomadAddr, NomadToken (Secret)
└── config_test.go

internal/app/
├── validate.go                       # + ValidateApp; Validate = ValidateApp + deployment rules
└── validate_test.go

internal/nomadjob/
├── translate.go                      # + Check (settings, ValidateApp, Nomad limits)
└── translate_test.go                 # goldens unchanged

internal/registry/                    # NEW
├── reference.go                      # Reference, Parse, Pinned (R2)
├── client.go                         # Client, Resolve, HEAD/GET, token handshake (R3, R4)
├── reference_test.go
├── client_test.go
└── registrytest/registry.go          # fake registry server

internal/deploy/                      # NEW
├── deploy.go                         # Deployer, New, NewNomadClient, Validate, Resolve, errors
├── register.go                       # ownership check, enforced register, version lookup (R6, R7)
├── watch.go                          # Watch, status mapping, backoff (R8)
├── deploy_test.go
├── register_test.go
├── watch_test.go
└── deploytest/nomad.go               # fake Nomad server

internal/service/                     # NEW
├── service.go                        # Service, New, Start, Wait, reads
├── deploy.go                         # Deploy, Redeploy, per-app lock, latest-wins (R9, R11)
├── resume.go                         # unfinished deployments on startup (R10)
├── deploy_test.go
└── resume_test.go

internal/store/
├── deployments.go                    # + Digests in updates, limit, ListUnfinishedDeployments
└── deployments_test.go

internal/api/
├── api.go                            # New(svc, st, log); routes; error mapping for new codes
├── errors.go                         # + 5 codes; writeError with Location
├── json.go                           # NEW: the v2 option set (R5), decode + encode
├── decode.go                         # uses json.go; optional body for deploy
├── apps.go                           # NEW: GET /v1/apps/{id}
├── deployments.go                    # NEW: POST/GET deployments, GET one
├── api_test.go, decode_test.go
├── deployments_test.go               # NEW: every status and code, Location, limit
└── contract_test.go                  # + body checks for the new schemas

CLAUDE.md                             # where we are, new commands (lab test)
docs/plan.md                          # open decision 1 settled (own registry client)
```

**Structure Decision**: the packages `docs/go-guide.md` already reserves (`internal/registry`,
`internal/deploy`, `internal/service`) and the flow CLAUDE.md describes: API → service → deploy
(registry, translate, register, watch) → store records the result. Test fakes live in
`registrytest` and `deploytest` sub-packages (like `apptest`), so `deploy`, `service` and `api`
tests share them.

## Implementation notes for /speckit-tasks

- **Order**: foundations (`app.ValidateApp`, `nomadjob.Check`, store changes, config + `Secret`,
  API v2 option set and new codes, `registrytest`, `deploytest`) → US1 (registry parse +
  resolve, deploy Validate/Resolve/Register/Watch, service Deploy, POST endpoint, GET
  deployment, main wiring) → US2 (Redeploy and empty body, per-app lock + latest wins,
  idempotency tests) → US3 (watch mapping for rollback, first-deploy failure) → US4 (error
  mapping, `Location`, job conflict, Nomad down/rejects) → US5 (GET app, list + limit) →
  restart/resume → contract + lab test → polish (CLAUDE.md, docs/plan.md, CI checks).
- **US1 already needs a working watcher**, because its acceptance is "reaches healthy"; US3 adds
  the rollback and no-rollback branches with their scripted states.
- **Tests that prove the hard rules**: `-race` service tests where request A's registry answer
  is held (`registrytest.Hold`) while B deploys (B live, A `superseded`, A never registered);
  a fake Nomad that goes down mid-watch (record stays `running`, then resolves when it's back);
  a store restart with `pending` and `running` records (R10).
- **Go idioms to explain in summaries**: `sync.Mutex` and why it isn't FIFO; goroutines plus a
  `sync.WaitGroup` for watchers (C#: `Task`s you await on shutdown); `context.WithoutCancel`;
  `errors.As` with a value type (`api.UnexpectedResponseError`); custom `error` types with
  `Unwrap`; `slog.LogValuer` for redaction; `httptest.Server` as a fake upstream; json/v2
  `WithMarshalers`; `crypto/rand.Text`.
- **Commit message note**: no new module; the Nomad API client moves from types-only use
  (translator) to I/O (`internal/deploy`), as the constitution requires.

## Complexity Tracking

No violations to justify.
