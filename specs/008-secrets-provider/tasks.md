# Tasks: Secrets

**Input**: [plan.md](./plan.md), [secrets-provider.spec.md](./secrets-provider.spec.md),
[research.md](./research.md), [data-model.md](./data-model.md), [contracts/](./contracts/)

Tests are required (constitution VIII). Run `go test ./...` after each phase.

## Phase 1: Setup

No setup: no new module, tool or directory outside the packages below.

## Phase 2: Foundational (blocks every story)

Files: `internal/app/app.go`, `internal/app/validate.go`, `internal/app/validate_test.go`,
`internal/secrets/secrets.go`, `internal/secrets/secrets_test.go`,
`internal/secrets/secretstest/store.go`, `internal/secrets/secretstest/store_test.go`,
`internal/nomadjob/translate.go`, `internal/nomadjob/translate_test.go`,
`internal/nomadjob/testdata/`, `internal/deploy/secrets.go`, `internal/deploy/secrets_test.go`,
`internal/deploy/deploytest/nomad.go`, `internal/service/service.go`,
`internal/service/helpers_test.go`, `cmd/caravana/main.go`, `internal/api/api.go`,
`internal/api/errors.go`

**Wave 1 — independent (different files):**

- [x] **T001** [P] Add `Secret{Name, Value *string}`, `Service.Secrets` and `App.WithoutSecretValues` · internal/app/app.go
- [x] **T002** [P] Add `internal/secrets`: `Sets`, `ErrUnavailable`, `Provider`, `Build` (keep, removal, 60 KiB limit) with table tests · internal/secrets/secrets.go, internal/secrets/secrets_test.go
- [x] **T003** [P] Add fake `/v1/var/{path}` (GET, PUT, DELETE) and `/v1/vars?prefix=` endpoints, plus an unavailable switch · internal/deploy/deploytest/nomad.go

**⟶ Wait for Wave 1 to finish, then:**

**Wave 2 — independent (different files):**

- [x] **T004** [P] Validate secrets: name pattern, uniqueness across secrets and env, UTF-8 and no NUL in values, with tests · internal/app/validate.go, internal/app/validate_test.go
- [x] **T005** [P] Add the in-memory `secretstest.Store` (Replace/Commit/Restore ownership rules, `SetUnavailable`, `Live`, `Kept`) with tests · internal/secrets/secretstest/store.go, internal/secrets/secretstest/store_test.go
- [x] **T006** [P] Add `deploy.Variables`, the Nomad Variables provider (per-service variables, kept copy at `nomad/jobs/<app>/caravana/previous`, errors as `secrets.ErrUnavailable`), tested against deploytest · internal/deploy/secrets.go, internal/deploy/secrets_test.go
- [x] **T007** [P] Emit the env template (toJSON values, `noop`, `secrets/caravana.env`) for services with secrets; add a golden case · internal/nomadjob/translate.go, internal/nomadjob/translate_test.go, internal/nomadjob/testdata/

**⟶ Wait for Wave 2 to finish, then:**

**Wave 3 — independent (different files):**

- [x] **T008** [P] `service.New` takes a `secrets.Provider`; test helpers pass a `secretstest.Store` · internal/service/service.go, internal/service/helpers_test.go
- [x] **T009** [P] Map `secrets.ErrUnavailable` to 502 `secret_store_unavailable` · internal/api/api.go, internal/api/errors.go

**⟶ Wait for Wave 3 to finish, then:**

- [x] **T010** Wire `deploy.NewVariables(nomad)` into `service.New` · cmd/caravana/main.go

## Phase 3: User Story 1 - Deploy an app with secrets through the API (P1) 🎯 MVP

Files: `internal/service/deploy.go`, `internal/service/secrets.go`,
`internal/service/secrets_test.go`, `internal/api/secrets_test.go`, `api/openapi.yaml`,
`deploy/vm/caravana-policy.hcl`, `cmd/caravana/gate_test.go`

**Goal**: a deploy with secrets reaches the containers, never shows a value, and replaces the set.

**Independent Test**: deploy with a marker secret through the API; the store holds it, the job,
responses, logs and the database don't; a deploy without it removes it.

### Tests

- [x] **T011** [P] [US1] Service tests: set, replace, remove, per-service isolation, the stored app has no values, unreachable store fails before Register, superseded deploy writes nothing · internal/service/secrets_test.go
- [x] **T012** [P] [US1] API tests: deploy with secrets, `GET` shows names only, a marker value appears in no response or log, error fields and the 502 code · internal/api/secrets_test.go

### Implementation

- [x] **T013** [US1] Add `buildSecrets` (keep check before save) and `settleSecrets` (Commit on healthy, Restore on failed or reverted, per-attempt app lock, retry until shutdown) · internal/service/secrets.go
- [x] **T014** [US1] In `Deploy` save `WithoutSecretValues` and pre-check keeps; in `deploy` Build and Replace after the latest check, Restore when Register fails; call `settleSecrets` from `finish` · internal/service/deploy.go
- [x] **T015** [P] [US1] Add `Service.secrets`, `Secret`, the error code, version 0.6.0 · api/openapi.yaml
- [x] **T016** [P] [US1] Grant `variables` on `nomad/jobs/*` (read, write, destroy, list) · deploy/vm/caravana-policy.hcl
- [x] **T017** [US1] Gate step `secrets-render`: deploy busybox with a tricky secret value, read it back over HTTPS, redeploy by webhook and read it again, delete the app · cmd/caravana/gate_test.go

**Checkpoint**: the API deploys apps with secrets, and the gate proves it on a real host.

## Phase 4: User Story 2 - Redeploys keep the secrets (P1)

Files: `internal/service/secrets_redeploy_test.go`

**Goal**: an empty-body redeploy and a webhook redeploy keep every value; a missing stored value fails loudly.

**Independent Test**: deploy with a secret, redeploy by webhook, check the value.

- [x] **T018** [US2] Tests: empty-body redeploy and webhook keep values; a value deleted from the store fails the redeploy naming `services[0].secrets[0].value` · internal/service/secrets_redeploy_test.go

**Checkpoint**: redeploys and webhooks are safe with secrets.

## Phase 5: User Story 3 - Manage secrets from the console (P2)

Files: `internal/service/secretedit.go`, `internal/service/secretedit_test.go`,
`internal/ui/configure.go`, `internal/ui/app.go`, `internal/ui/ui.go`,
`internal/ui/templates/app.html`, `internal/ui/static/app.css`,
`internal/ui/secrets_test.go`, `cmd/caravana/console_lab_test.go`

**Goal**: the admin lists, sets and removes secrets from Configure, each change a deploy.

**Independent Test**: set a secret from Configure, see the deploy, check the store; remove it.

### Tests

- [x] **T019** [P] [US3] Service tests for `SetSecret` and `RemoveSecret` (other secrets kept, unknown service and unknown secret refused) · internal/service/secretedit_test.go
- [x] **T020** [P] [US3] UI tests: the panel lists names and no value, the forms post and redirect, a failed set re-renders without the value, autocomplete off · internal/ui/secrets_test.go

### Implementation

- [x] **T021** [US3] Add `SetSecret` and `RemoveSecret` as Deploys of the stored app with one change · internal/service/secretedit.go
- [x] **T022** [US3] Secrets panel data, `POST /apps/{id}/secrets` and `POST /apps/{id}/secrets/remove` handlers and routes · internal/ui/configure.go, internal/ui/app.go, internal/ui/ui.go
- [x] **T023** [US3] Render the Secrets panel and forms on Configure; regenerate the stylesheet (`tools/ui-css.sh build`) · internal/ui/templates/app.html, internal/ui/static/app.css
- [x] **T024** [US3] Console lab step: set a secret from the page, wait for healthy, remove it · cmd/caravana/console_lab_test.go

**Checkpoint**: secrets are manageable without the API.

## Phase 6: User Story 4 - A failed deploy doesn't strand bad secrets (P2)

Files: `internal/service/secrets_restore_test.go`

The code is part of US1 (`settleSecrets`, the Restore on a failed Register). This phase proves it.

- [x] **T025** [US4] Tests: a failing rollout restores the previous values; a restart mid-rollout still restores; a newer deploy takes over the kept copy; a store change outside a deploy doesn't touch copies (no template re-render: the job says `noop`) · internal/service/secrets_restore_test.go

**Checkpoint**: changing a secret is reversible.

## Phase 7: User Story 5 - Deleting an app removes its secrets (P3)

Files: `internal/service/apps.go`, `internal/service/secrets_delete_test.go`

- [x] **T026** [US5] `DeleteApp` deletes the app's secrets after the purge and before the store row (a failure keeps the delete retryable) · internal/service/apps.go
- [x] **T027** [US5] Tests: delete removes live and kept secrets; a new app with the same ID has none; an unreachable store fails the delete and leaves the app · internal/service/secrets_delete_test.go

**Checkpoint**: nothing is left behind.

## Phase 8: Polish

Files: `CLAUDE.md`, `docs/plan.md`

- [x] **T028** [P] Update where we are: feature 008 on branch `008-secrets` · CLAUDE.md, docs/plan.md
- [x] **T029** Validate against the Success Criteria: `go build ./...`, `go vet ./...`, `go tool staticcheck ./...`, `go test -race ./...`, `gofmt -l .`, the linux/arm64 and amd64 cross-compiles, `tools/ui-css.sh check`, `tools/ui-vendor.sh check`, shellcheck, and `CARAVANA_LAB=1 go test ./cmd/caravana -run TestLab` when the lab is up

## Dependencies & Execution Order

- Setup (none) → Foundational → US1 → US2, US3, US4, US5 (in any order) → Polish.
- Foundational: Wave 1 (T001–T003) blocks Wave 2 (T004–T007), which blocks Wave 3 (T008–T009), which blocks T010.
- US1: the tests T011 and T012 first, then T013 → T014; T015 and T016 alongside; T017 last.
- US2, US4: one test task each, which needs US1.
- US3: T019 and T020 first, then T021 → T022 → T023 → T024.
- US5: T026 → T027.
- Polish: T028, then T029.
