---

description: "Task list for feature 004: webhook redeploy"
---

# Tasks: Webhook Redeploy

**Input**: Design documents from `specs/004-webhook-redeploy/`

**Prerequisites**: [plan.md](plan.md), [spec.md](spec.md), [research.md](research.md),
[data-model.md](data-model.md), [contracts/](contracts/), [quickstart.md](quickstart.md)

**Tests**: Required (constitution VIII, spec FR-015). In each story, write the tests first and
watch them fail.

**Organization**: US1 = a push redeploys (or skips when unchanged). US2 = only the right token
works, and the webhook-only listener serves nothing else. US3 = rotate, status, delete. US4 =
webhook deploys follow every feature 003 rule.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: can run in parallel (different files, no dependency on an unfinished task)
- **[Story]**: US1–US4 from spec.md

## Conventions for every task

- Every function doing I/O takes `ctx context.Context` first. No `panic` in library code.
  Errors are wrapped with context.
- **The token never leaves the create response**: not in logs, errors, other responses, or test
  failure messages (print a prefix at most).
- Tests reuse feature 003's fakes: `registrytest` (registry) and `deploytest` (Nomad), and the
  existing helpers `newEnv`/`waitFinal` (service) and `newTestEnv`/`call`/`checkSchema` (api).
- Routes and `api/openapi.yaml` stay equal after every task (the contract-sync test).
- Keep `gofmt -l .` empty after every task; feature 001's goldens must not change.

---

## Phase 1: Setup

- [X] T001 Create `internal/store/migrations/0002_webhook_tokens.sql` exactly as in
  data-model.md ("Store: migration"), with its comment. Check `go test ./internal/store` still
  passes: the migration test must now count 2 migrations on a new store (update the expected
  count in `internal/store/migrate_test.go` if it's hard-coded).

---

## Phase 2: Foundational (blocks every story)

- [X] T002 [P] Store methods in new `internal/store/webhooks.go`: `WebhookTokenRecord`,
  `SetWebhookToken` (upsert in one statement, new `created_at`; `ErrNotFound` wrapped with the
  app ID for an unknown app), `GetWebhookToken` (`ErrNotFound` when none), `DeleteWebhookToken`
  (nil when none; `ErrNotFound` for an unknown app). Table tests in new
  `internal/store/webhooks_test.go`: set/get round trip, replace changes hash and `CreatedAt`,
  delete then get is `ErrNotFound`, delete twice is nil, unknown app for all three.
- [X] T003 [P] `LatestHealthyDeployment(ctx, appID)` in `internal/store/deployments.go`: newest
  `healthy` record by `created_at DESC, rowid DESC`, `ErrNotFound` when none. Cases in
  `internal/store/deployments_test.go`: none; one healthy among failed/reverted/superseded; the
  newest of two healthy.
- [X] T004 [P] Webhook listener setting in `internal/config/config.go` (research R7):
  `Config.WebhookListen` from `CARAVANA_WEBHOOK_LISTEN` / `-webhook-listen`, default "" (off);
  when set it must pass `checkListen` and differ from `Listen`, else an error naming
  `CARAVANA_WEBHOOK_LISTEN / -webhook-listen`. Cases in `internal/config/config_test.go`:
  default off, env, flag, invalid, equal to `Listen`; the defaults struct gains the field.
- [X] T005 [P] Add `codeUnauthorized = "unauthorized"` to `internal/api/errors.go`.
- [X] T006 [P] Token helpers in new `internal/service/webhook.go` (research R1, R2):
  `newWebhookToken() (string, error)` (`cvwh_` + base64url-no-padding of 32 bytes from
  `crypto/rand`), `hashToken(token string) string` (hex SHA-256), and
  `tokenMatches(storedHash, presented string) bool` using `crypto/subtle.ConstantTimeCompare` on
  the decoded 32-byte hashes, with a package-level dummy hash used when there is no stored one.
  Table tests in new `internal/service/webhook_test.go`: format (`^cvwh_[A-Za-z0-9_-]{43}$`),
  1000 tokens all different, hash is 64 hex characters and not the token, matches only the
  same token, an empty or malformed presented value never matches, the dummy never matches.

**Checkpoint**: `go test ./...` passes; nothing visible to users yet.

---

## Phase 3: User Story 1 - A push redeploys the app (Priority: P1) 🎯 MVP

**Goal**: create a token, call the webhook, and get 202 with a new deployment (trigger
`webhook`) when an image changed, or 200 `changed: false` when nothing did.

**Independent Test**: service and API tests with the fakes; quickstart §3 in the lab.

### Tests for User Story 1 ⚠️

- [X] T007 [P] [US1] Service tests in `internal/service/webhook_test.go` (use `newEnv`; deploy
  whoami and `waitFinal` it first): `CreateWebhookToken` returns a token matching the format,
  `Path` = `/v1/webhooks/whoami?token=<token>`, and the stored hash is `hashToken(token)`; for
  an unknown app it's `store.ErrNotFound`. `Webhook` with the valid token and an unchanged
  registry → `Changed: false`, the deployment is the latest healthy one, 0 new records, 0 new
  Nomad registrations. After `registrytest.AddIndex` moves `latest` → `Changed: true`, a new
  record with trigger `webhook` pinning the new digest, which reaches `healthy`. An app whose
  only deployment is still `running` (no healthy one) → deploys. A stored app saved again after
  its healthy deployment (`SaveApp` with a changed field) → deploys even with equal digests.
- [X] T008 [P] [US1] API tests in new `internal/api/webhooks_test.go` (use `newTestEnv`,
  `deployOK`, `call`, `checkSchema`): `POST /v1/apps/whoami/webhook` → 201, body validates
  `WebhookToken`; `POST /v1/webhooks/whoami?token=…` → 200 `WebhookResult` with `changed:
  false`; the same with `Authorization: Bearer …` and with `authorization: bearer …`; after
  moving the tag → 202 `WebhookResult` with `changed: true` and a `Location`; a JSON body, a
  `text/plain` body and no body all behave the same; a body over 1 MiB → 413 `body_too_large`
  and no deployment. Token-leak test (SC-004): plant the token in the query, the header and the
  body of webhook calls, then check it appears 0 times in the captured logs and in every
  response body except the create response.

### Implementation for User Story 1

- [X] T009 [US1] In `internal/service/webhook.go`: `ErrUnauthorized`, `WebhookToken`,
  `WebhookResult`, `CreateWebhookToken` (generate, `SetWebhookToken(hashToken(t))`, return token,
  path and `CreatedAt`), and `Webhook(ctx, appID, token)`: load the stored hash (missing app or
  token → compare against the dummy, then `ErrUnauthorized`); `tokenMatches` or
  `ErrUnauthorized`; then the skip rule (research R6): `GetApp`, `LatestHealthyDeployment`,
  `deployer.Resolve`; skip only when a healthy one exists, `app.UpdatedAt` isn't after its
  `CreatedAt`, and every digest is equal; a resolution error falls through. Otherwise
  `Redeploy(ctx, appID, app.TriggerWebhook)` and `Changed: true`. Return `Redeploy`'s errors
  as they are.
- [X] T010 [US1] New `internal/api/webhooks.go`: `createWebhookToken` (201 `{token, path,
  createdAt}`) and `callWebhook` (research R4, R5): raise the write deadline to 60 s; read the
  token from `?token=` and `Authorization: Bearer` (scheme case-insensitive; both present and
  different → treat as invalid); call `svc.Webhook`; only after it accepted the token is the
  body read, limited by `http.MaxBytesReader` to `maxBodyBytes` and drained (over the limit →
  413 before deploying, so drain before calling the deploy part: split `Webhook` into the
  auth check and the rest if needed, keeping both in the service); answer 202 + `Location`
  when changed, else 200, with `{changed, deployment}`. Register both routes in
  `internal/api/api.go`, and map `service.ErrUnauthorized` in `mapError` to 401
  `unauthorized` "missing or invalid webhook token" with `WWW-Authenticate: Bearer` (set the
  header on the response, e.g. through a field on `Error` written by `writeError`).
- [X] T011 [US1] Update `api/openapi.yaml` from `specs/004-webhook-redeploy/contracts/openapi.yaml`
  with only the operations served so far: `createWebhookToken` (the `post` of
  `/v1/apps/{id}/webhook`) and `callWebhook`, plus every component. `getWebhook` and
  `deleteWebhookToken` come in T021.

**Checkpoint**: `go test -race ./...` passes; quickstart §3's first two calls work in the lab.

---

## Phase 4: User Story 2 - Only the right token deploys, and only its app (Priority: P1)

**Goal**: every authorization failure is the same 401 with no deploy; the webhook-only
listener serves nothing but the webhook.

**Independent Test**: API and `main` tests for every 401 case and every other route on the
webhook listener.

### Tests for User Story 2 ⚠️

- [X] T012 [P] [US2] Service tests in `internal/service/webhook_test.go`: no token, a wrong
  token, a well-formed token of another app, a malformed token (wrong prefix, wrong length,
  non-base64), the stored hash itself (SC-005), an app without a token and an unknown app all
  return `ErrUnauthorized` (and nothing else), with 0 new deployment records and 0 Nomad
  registrations.
- [X] T013 [P] [US2] API tests in `internal/api/webhooks_test.go`: each US2 case answers 401
  with `WWW-Authenticate: Bearer` and byte-identical bodies (SC-002); query and header both
  present and different → 401, equal → works. Webhook-only handler (`NewWebhook` on its own
  `httptest.Server`): `GET /v1/health`, `POST /v1/apps/whoami/deployments`, `GET
  /v1/apps/whoami`, `POST /v1/apps/whoami/webhook` → 404 standard body; `GET
  /v1/webhooks/whoami` → 405; a valid webhook call → the same answer as on the main handler
  (SC-007).
- [X] T014 [P] [US2] In `cmd/caravana/main_test.go`: start `run` with `-webhook-listen` on a
  second free address; health answers 200 on the main address and 404 on the webhook one;
  cancel → exit 0 and both stop. `-webhook-listen` equal to `-listen` → exit 1, stderr names
  `CARAVANA_WEBHOOK_LISTEN`.

### Implementation for User Story 2

- [X] T015 [US2] Make sure `Webhook` in `internal/service/webhook.go` does the same work on every
  failure path (hash the presented value and compare with the stored hash or the dummy) before
  returning `ErrUnauthorized`, and never says why in the error.
- [X] T016 [US2] `NewWebhook(svc, log) http.Handler` in `internal/api/api.go`: a server with the
  same `logRequests`, `jsonOnly` and `handle` adapter as `New`, but a route table with only
  `POST /v1/webhooks/{id}`. Refactor `New` so both share one builder from a route list.
- [X] T017 [US2] In `cmd/caravana/main.go`: when `cfg.WebhookListen` is set, build a second
  `http.Server` (same timeouts) with `api.NewWebhook(svc, log)`, start it next to the main one,
  log `webhookListen`, treat either server failing as fatal, and shut both down with the same
  timeout on SIGINT/SIGTERM.

**Checkpoint**: `go test -race ./...` passes.

---

## Phase 5: User Story 3 - Rotate or turn off a token (Priority: P2)

**Goal**: creating again rotates; status shows whether a token exists; delete turns it off.

**Independent Test**: service and API tests around rotation and deletion.

### Tests for User Story 3 ⚠️

- [X] T018 [P] [US3] Service tests in `internal/service/webhook_test.go`: create, create again →
  the first token is `ErrUnauthorized` from then on (SC-003) and the second works; `WebhookStatus`
  is `Enabled: false` before, `true` with the newest `CreatedAt` after; `DeleteWebhookToken` →
  the token is `ErrUnauthorized` and the status `Enabled: false`; deleting when there's none is
  nil; all three on an unknown app → `store.ErrNotFound`; deploying through `Deploy` still
  works after delete.
- [X] T019 [P] [US3] API tests in `internal/api/webhooks_test.go`: `GET /v1/apps/whoami/webhook`
  validates `WebhookStatus` and never contains `cvwh_`; `DELETE` → 204 with no body, then the
  webhook answers 401; rotate via `POST` → old 401, new works; unknown app → 404 for all
  three.

### Implementation for User Story 3

- [X] T020 [US3] `WebhookStatus` type, `WebhookStatus(ctx, appID)` and
  `DeleteWebhookToken(ctx, appID)` in `internal/service/webhook.go` (unknown app →
  `store.ErrNotFound`, checked with `GetApp`).
- [X] T021 [US3] Handlers `getWebhook` (200 `{enabled, createdAt?}`) and `deleteWebhookToken`
  (204) in `internal/api/webhooks.go`, routes in `internal/api/api.go`, and `api/openapi.yaml`
  now equal to `specs/004-webhook-redeploy/contracts/openapi.yaml` (`diff` prints nothing).

**Checkpoint**: `go test ./internal/api` passes, including `TestContractMatchesRoutes`.

---

## Phase 6: User Story 4 - Webhook deploys behave like any other deploy (Priority: P2)

**Goal**: a webhook deploy reaches each of feature 003's outcomes, recorded with trigger
`webhook`.

**Independent Test**: service and API tests driving the webhook into 003's paths.

### Tests for User Story 4 ⚠️

- [X] T022 [P] [US4] Service tests in `internal/service/webhook_test.go`: with `latest` moved and
  the registry held (`registrytest.Hold`), two `Webhook` calls in a row → the later one is
  live and the earlier ends `superseded` (FR-019 through the webhook); a stored app whose tag
  doesn't exist → `*DeployError` matching `deploy.ErrImageNotFound`, record `failed` with
  trigger `webhook` (the pre-check failure falls through, research R6); registry 502 and Nomad
  down → `failed` records with trigger `webhook`.
- [X] T023 [P] [US4] API tests in `internal/api/webhooks_test.go`: webhook → 422
  `image_not_found` with `field` and `Location`; registry down → 502 `registry_unavailable`;
  a foreign Nomad job → 409 `job_conflict`; each body validates `Error`.

### Implementation for User Story 4

- [X] T024 [US4] Fix whatever T022/T023 reveal in `internal/service/webhook.go` or
  `internal/api/webhooks.go`; by design `Webhook` returns `Redeploy`'s errors unchanged and the
  existing `toAPIError` maps them (including `Location`), so no new mapping is expected.

**Checkpoint**: `go test -race ./...` passes.

---

## Phase 7: Polish & Cross-Cutting Concerns

- [X] T025 [P] Extend `TestContractBodies` in `internal/api/contract_test.go`: real responses for
  `WebhookToken`, `WebhookStatus`, `WebhookResult` (200 and 202) and the 401 `Error`, plus
  "reject" cases: a token without the `cvwh_` prefix, `WebhookResult` without `deployment`.
- [X] T026 [P] Webhook phase in `cmd/caravana/lab_test.go` (FR-016, research R9): run `run` with
  `-webhook-listen` on a second free address; right after the first API deploy's 202, create a
  token and call the webhook on the webhook listener → 202 `changed: true` within 10 s; its
  deployment reaches `healthy` and the API's first one ends `superseded`; call it again → 200
  `changed: false` and Nomad's job version doesn't move; `GET /v1/health` on the webhook
  listener → 404. Then continue with feature 003's existing steps.
- [X] T027 [P] Update `CLAUDE.md` ("Where we are": feature 004 on branch
  `004-webhook-redeploy`, tokens and the webhook-only listener; next: 005) and add
  `CARAVANA_WEBHOOK_LISTEN` to the run command example.
- [X] T028 Run every CI check from quickstart §1 and fix anything they report; run the lab test
  (quickstart §4) when the lab is up.
- [X] T029 Write the summary for the maintainer: what changed per package, the FR-016/SC-001
  refinement made during planning, and the Go idioms introduced
  (`crypto/subtle.ConstantTimeCompare`, `crypto/rand`, `http.MaxBytesReader`, two
  `http.Server`s with one shutdown), one line each with the C# comparison.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (T001)** → **Foundational (T002–T006)**; T002 needs T001.
- **US1 (T007–T011)**: after Foundational.
- **US2 (T012–T017)**: after US1 (extends `webhook.go`, `webhooks.go`, `api.go`).
- **US3 (T018–T021)**: after US1; independent of US2 except shared files (`webhook.go`,
  `webhooks.go`, `api.go`): do the edits one story at a time.
- **US4 (T022–T024)**: after US1.
- **Polish (T025–T029)**: last; T025 needs T021; T028 before T029.

### Within each story

Tests first and failing, then implementation, then the checkpoint. T010 needs T009; T011 needs
T010; T016 before T017; T021 needs T020.

### Parallel Opportunities

- Foundational: T002 (after T001), T003, T004, T005, T006 in five files.
- Each story's test tasks are in different packages and can be written together (e.g. T007 and
  T008; T012, T013 and T014).
- After US1: US4's tests (T022, T023) alongside US2/US3 work, since they need no new code.
- Polish: T025, T026, T027 together.

## Parallel Example: User Story 1

```text
Tests at once:       T007 (service) · T008 (api)
Then:                T009 → T010 → T011
```

## Implementation Strategy

### MVP first (US1)

Phases 1–3: create a token and redeploy by webhook on the main API. That's the webhook half of
the Phase 1 gate, minus public exposure (005).

### Incremental delivery

1. US1: the webhook works, and skips unchanged pushes.
2. US2: it's safe to expose: uniform 401s, the webhook-only listener.
3. US3: tokens can be rotated and turned off.
4. US4: proof that webhook deploys keep every 003 guarantee.
5. Polish: contract bodies, lab test, docs, CI.

## Notes

- One migration (`0002`), no new module: say so in the commit message.
- The webhook's `Location` points at the main API, which the webhook-only listener doesn't
  serve; that's intended (research R4).
- Commit when the maintainer asks.
