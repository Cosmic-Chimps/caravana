# Implementation Plan: Webhook Redeploy

**Branch**: `004-webhook-redeploy` | **Date**: 2026-10-02 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from `specs/004-webhook-redeploy/spec.md`

## Summary

Give each app an optional webhook token (`cvwh_` + 256 random bits, shown once, stored as a
SHA-256 hash in a new `webhook_tokens` table) and an endpoint, `POST /v1/webhooks/{id}`, that
accepts it from `?token=` or `Authorization: Bearer`, checks it in constant time, and answers
every failure with the same 401. A valid call resolves every image; if nothing changed since
the app's latest healthy deployment it answers 200 `changed: false`, otherwise it calls feature
003's `Redeploy` with trigger `webhook` and answers 202 like a deploy. Token management is three
calls on `/v1/apps/{id}/webhook` (create/rotate, status, delete). An optional webhook-only
listener (`CARAVANA_WEBHOOK_LISTEN`, off by default) serves just the webhook route, so feature
005 can expose it without exposing the unauthenticated API. No new module.

## Technical Context

**Language/Version**: Go 1.27 (go1.27.1)

**Primary Dependencies**: standard library (`crypto/rand`, `crypto/sha256`, `crypto/subtle`,
`encoding/base64`, `net/http`); feature 003's `internal/service`, `internal/deploy`,
`internal/registry`. **No new module.**

**Storage**: SQLite; migration `0002_webhook_tokens.sql` (first migration since 002)

**Testing**: `testing` + `httptest`; feature 003's `registrytest` and `deploytest` fakes; real
store in `t.TempDir()`; contract validation with kin-openapi; `TestLab` gains a webhook phase

**Target Platform**: linux/amd64 and linux/arm64, CGO off; macOS + Lima lab for development

**Project Type**: single Go binary (control plane)

**Performance Goals**: a webhook that has to deploy answers within 10 s in the lab (SC-001): one
extra `HEAD` per image for the unchanged check; an unchanged call needs no Nomad request at all

**Constraints**: the token never in logs, errors or later responses; constant-time check; same
401 for every failure; the main API stays on loopback by default

**Scale/Scope**: 1 migration, 4 endpoints, 1 listener, 1 error code; ~6 source files changed or
added

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | How this plan complies | Status |
| --- | --- | --- |
| I. Small by Default | Standard library only; no new runtime service; the second listener is in the same process. | ✅ |
| II. One Model | Nothing changes in `app.App` or the translator; the webhook redeploys the stored app. | ✅ |
| III. Safe, Reversible Deploys | Webhook deploys go through 003's `Redeploy` (digest pinning, blue/green, auto-revert, newest wins). Skipping an unchanged push creates no job version, so "every deploy is a new version" still holds for every deploy that happens. | ✅ |
| IV. Fail Loudly | Failed webhook deploys answer and record exactly like 003's. Authorization failures are deliberately uniform (401), which hides only *why* auth failed, not that it did. | ✅ |
| V. API-First | All four endpoints in `api/openapi.yaml` in the same change, the sync test covering them; logic in `internal/service` (token rules and the skip rule), so 007's UI uses the same calls. | ✅ |
| VI. Nomad-Native | No new Nomad code; the skip check reads only Caravana's own records. | ✅ |
| VII. Secure by Default | The webhook-only listener is off by default and serves one route; tokens are 256-bit, stored only as hashes (not secrets, R2), compared in constant time, never logged (the request log reads no query string or header). | ✅ |
| VIII. Plain, Tested Go | Table tests; no new interface; `ctx` first; errors wrapped. | ✅ |
| Development Workflow | Phase 1 scope (the webhook is in the Phase 1 gate); exposure over HTTPS stays in 005, auth in 006. | ✅ |

No violations, so Complexity Tracking is empty.

**Post-design re-check**: still passes. Worth knowing:
- **The token hash lives in SQLite**, not Nomad Variables. Constitution VII puts the control
  plane's *secrets* in Nomad Variables; a SHA-256 of a 256-bit token can't be used to call the
  webhook, so it isn't one (research R2).
- **Spec refinement during planning**: FR-016 and SC-001 now say how the lab proves a real
  webhook redeploy (call it while the first rollout is still running), since the lab can't move
  a Docker Hub tag.

## Project Structure

### Documentation (this feature)

```text
specs/004-webhook-redeploy/
├── plan.md
├── research.md            # R1–R10
├── data-model.md          # setting, migration, token, decision flow, schemas, error code
├── quickstart.md
├── contracts/
│   ├── openapi.yaml       # replaces api/openapi.yaml
│   └── go-packages.md
├── checklists/requirements.md
└── tasks.md               # /speckit-tasks
```

### Source Code (repository root)

```text
api/openapi.yaml                       # from contracts/openapi.yaml

cmd/caravana/
├── main.go                            # optional second server for the webhook listener
├── main_test.go                       # + both listeners start and stop
└── lab_test.go                        # + webhook phase (FR-016)

internal/config/
├── config.go                          # + WebhookListen
└── config_test.go

internal/store/
├── migrations/0002_webhook_tokens.sql # NEW
├── webhooks.go                        # NEW: Set/Get/DeleteWebhookToken
├── deployments.go                     # + LatestHealthyDeployment
├── webhooks_test.go                   # NEW
└── deployments_test.go

internal/service/
├── webhook.go                         # NEW: tokens, constant-time check, skip rule, Webhook
└── webhook_test.go                    # NEW

internal/api/
├── api.go                             # + routes, NewWebhook, unauthorized mapping
├── errors.go                          # + codeUnauthorized
├── webhooks.go                        # NEW: token handlers, webhook handler (token from query/header, body drain)
├── webhooks_test.go                   # NEW: both listeners, 401 cases, log test
└── contract_test.go                   # + new schemas

CLAUDE.md                              # where we are, new setting
```

**Structure Decision**: no new package. Token rules sit in `internal/service` beside the deploy
flow they guard; the store gets one table and one query; the API gets one file and a second
handler constructor sharing all existing plumbing.

## Implementation notes for /speckit-tasks

- **Order**: foundations (migration + store methods, config setting, error code, contract copy
  scaffolding) → US1 (create token, webhook call, skip rule, 202/200 answers, log test) → US2
  (every 401 path, dummy-hash comparison, webhook-only listener and its 404s) → US3 (rotate,
  status, delete) → US4 (webhook deploys reaching 003's outcomes: newest wins, 422, 502) →
  lab test → polish (CLAUDE.md, CI).
- **The contract grows per story**, as in 003: routes and `api/openapi.yaml` must stay equal at
  every step, or the sync test fails.
- **Go idioms to explain**: `crypto/subtle.ConstantTimeCompare` (C#:
  `CryptographicOperations.FixedTimeEquals`); `crypto/rand.Read` (C#:
  `RandomNumberGenerator.Fill`); `http.MaxBytesReader`; two `http.Server`s in one process with
  a shared shutdown.
- **Commit message note**: one migration, no new module.

## Complexity Tracking

No violations to justify.
