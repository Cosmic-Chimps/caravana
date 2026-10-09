# Tasks: Rollback and export

**Input**: [plan.md](./plan.md), [rollback-export.spec.md](./rollback-export.spec.md),
[research.md](./research.md), [data-model.md](./data-model.md), [contracts/](./contracts/)

**Scale note**: 15 tasks over about 22 files. Phases 3 and 4 are independent once Phase 2 is
done, so they can run side by side. The API and the console share `api.go`, `errors.go`,
`openapi.yaml` and the console's route table, so both stories meet in Phase 5, which owns those
files. Watch the HCL writer (T008): its golden files and coverage test are the guard until the
gate step parses and plans an export in the lab.

Tests are required (constitution VIII).

## Phase 1: Setup

No new module, no new tool.

## Phase 2: Foundational (blocks every story)

Files: `internal/store/migrations/0006_rollback.sql`, `internal/store/deployments.go`,
`internal/store/deployments_test.go`, `internal/service/versions.go`, `internal/service/versions_test.go`

- [x] **T001** Store: migration 0006 (`deployments.definition`, `deployments.source_deployment`, nullable TEXT, no foreign key). `DeploymentRecord.Definition *app.App` and `Source string`, written by `CreateDeployment` and read by every reader. Definitions are stored without secret values and refuse a newer schema version, like apps. Tests: round trip, NULL for old rows, updates leave both unchanged · internal/store/migrations/0006_rollback.sql, internal/store/deployments.go, internal/store/deployments_test.go

**⟶ Wait for T001, then:**

- [x] **T002** Service: `ErrDeploymentNotHealthy`, `ErrDefinitionNotRecorded`, `ErrNoLiveDeployment`, and `version(ctx, appID, deploymentID)`, which loads a deployment of that app (`store.ErrNotFound` for another app's) and requires a definition. Tests · internal/service/versions.go, internal/service/versions_test.go

## Phase 3: User Story 1 - Go back to a version that worked (P1) 🎯 MVP

Files: `internal/registry/client.go`, `internal/registry/client_test.go`,
`internal/deploy/deploy.go`, `internal/deploy/deploy_test.go`, `internal/service/deploy.go`,
`internal/service/deploy_test.go`, `internal/service/rollback.go`, `internal/service/rollback_test.go`

### Implementation

**Wave 1 — independent (different files):**

- [x] **T003** [P] [US1] `registry.Client.Exists(ctx, ref)`: HEAD the manifest of a digest, with the same token and retry handling as `Resolve`. Missing gives `ErrNotFound`, anything else `ErrUnavailable`. Tests against `registrytest` (research R3) · internal/registry/client.go, internal/registry/client_test.go
- [x] **T004** [P] [US1] Service deploy path: `deployWarned` records the definition (without secret values) on every deployment. An option carries pinned digests and a source through `saveAndDeploy`, and skips `Resolve` when it is set. A `removedSecrets(old, next)` warning goes on every deploy (research R4). Tests: definition recorded, pinned digests used as given, removed-secret warning · internal/service/deploy.go, internal/service/deploy_test.go

**⟶ Wait for T003, then:**

- [x] **T005** [US1] `Deployer.CheckDigests(ctx, a, digests)`: `Exists` per service, returning `*ImageError` with `ErrImageNotFound` on the service's image field. Tests · internal/deploy/deploy.go, internal/deploy/deploy_test.go

**⟶ Wait for Wave 1 and T005, then:**

- [x] **T006** [US1] `Service.RedeployVersion(ctx, appID, deploymentID, trigger)`: `version`, healthy check, `CheckDigests`, then `saveAndDeploy` with the pinned digests and source. `Service.RedeployImpact` returns the images and the secrets it would remove. Tests: healthy redeploy (digests, stored definition, source), not healthy, no definition, other app, gone image, missing secret value, changed rule, newest wins against a running deploy · internal/service/rollback.go, internal/service/rollback_test.go

**Checkpoint**: a past healthy version redeploys through the service, with every refusal before anything changes.

## Phase 4: User Stories 2 + 3 - Export a version as a Nomad jobspec (P2, P3)

Files: `internal/nomadjob/hcl.go`, `internal/nomadjob/hcl_test.go`,
`internal/nomadjob/testdata/*.golden.nomad.hcl`, `internal/nomadjob/translate.go` (package doc only),
`internal/deploy/export.go`, `internal/deploy/export_test.go`, `internal/service/export.go`,
`internal/service/export_test.go`

### Implementation

- [x] **T007** [US2] `nomadjob.HCL(job, header)`: a pure writer for the fields `Translate` sets, with sorted keys, HCL2 string escaping (`${` → `$${`, `%{` → `%%{`) and heredoc template data. Golden `testdata/<case>.golden.nomad.hcl` for every translator fixture (`-update`). A coverage test fails naming any non-empty job field path the writer doesn't write. The package doc says HCL is written only for export (research R5) · internal/nomadjob/hcl.go, internal/nomadjob/hcl_test.go, internal/nomadjob/testdata/, internal/nomadjob/translate.go

**⟶ Wait for T007, then:**

- [x] **T008** [US2] `Deployer.Export(definition, deployment)`: `Translate` with today's settings, plus a header listing the deployment and time, the Traefik entrypoint and cert resolver with the domains, the host volumes, and the secret names with their variable paths (research R6). Tests: header contents, no secret value even when the definition had one · internal/deploy/export.go, internal/deploy/export_test.go

**⟶ Wait for T008, then:**

- [x] **T009** [US2] [US3] `Service.Export(ctx, appID, deploymentID)`: an empty ID means the newest healthy deployment (`ErrNoLiveDeployment` when none). Otherwise `version`. Returns the file name `<app>.nomad.hcl` and the bytes. Tests · internal/service/export.go, internal/service/export_test.go

**Checkpoint**: the live version or any recorded deployment exports as HCL through the service.

## Phase 5: API and console for all three stories

Files: `internal/api/deployments.go`, `internal/api/export.go`, `internal/api/api.go`,
`internal/api/errors.go`, `internal/api/deployments_test.go`, `internal/api/export_test.go`,
`api/openapi.yaml`, `internal/ui/app.go`, `internal/ui/configure.go`, `internal/ui/view.go`,
`internal/ui/ui.go`, `internal/ui/templates/app.html`, `internal/ui/app_test.go`,
`internal/ui/static/app.css`, `internal/api/parity_test.go`

**Wave 1 — independent (different files):**

- [x] **T010** [P] [US1] [US2] [US3] API: `redeployDeployment` (202, `Location`), `exportApp` and `exportDeployment` (`text/plain; charset=utf-8`, `Content-Disposition`), and `Deployment.source` and `definitionRecorded`. The codes `deployment_not_healthy`, `definition_not_recorded` and `no_live_deployment` are 409. `api/openapi.yaml` 0.9.0 with the routes, the HCL body and the codes. Tests: each operation, each error, no secret value in an export · internal/api/deployments.go, internal/api/export.go, internal/api/api.go, internal/api/errors.go, internal/api/deployments_test.go, internal/api/export_test.go, api/openapi.yaml
- [x] **T011** [P] [US1] [US2] [US3] Console: the history row's "Redeploy this version" `details.cv-confirm`, with images, removed secrets from `RedeployImpact` and the webhook note, posting to `/apps/{id}/deployments/{deploymentId}/redeploy` (303, or the page again with the error on the row). "Redeploy of <id>" on rows with a source. An export link on rows with a definition. An Export section in Configure. Rebuild `app.css`. Tests · internal/ui/app.go, internal/ui/configure.go, internal/ui/view.go, internal/ui/ui.go, internal/ui/templates/app.html, internal/ui/app_test.go, internal/ui/static/app.css

**⟶ Wait for Wave 1 to finish, then:**

- [x] **T012** Parity entries for the three console routes · internal/api/parity_test.go

**Checkpoint**: all three stories work from the API and the console.

## Phase 6: Polish

Files: `cmd/caravana/gate_test.go`, `cmd/caravana/console_lab_test.go`, `CLAUDE.md`, `docs/plan.md`

**Wave 1 — independent (different files):**

- [x] **T013** [P] Gate step `rollback-export`: redeploy `g.first`, wait for healthy, check Nomad runs its digest and the record names its source. Then export the live version, `Jobs().ParseHCL(…, true)`, and `Jobs().Plan(…, diff)` must report no change. A console lab step redeploys a past version through the form (research R8) · cmd/caravana/gate_test.go, cmd/caravana/console_lab_test.go
- [x] **T014** [P] Docs: where we are (011), what changed in the API and the console · CLAUDE.md, docs/plan.md

**⟶ Wait for Wave 1 to finish, then:**

- [x] **T015** Validate against the Success Criteria: build, vet, staticcheck, `go test -race`, gofmt, `go mod tidy -diff`, both cross-compiles, `tools/ui-css.sh check`, `tools/ui-vendor.sh check`, shellcheck, and `TestLab` in the lab

## Dependencies & Execution Order

- Phase 2 blocks everything: T001, then T002.
- Phase 3 (US1) and Phase 4 (US2 + US3) depend only on Phase 2 and can run side by side.
  - Phase 3: Wave 1 is T003 and T004. T005 needs T003. T006 needs T004 and T005.
  - Phase 4: T007, then T008, then T009.
- Phase 5 needs Phases 3 and 4. Its Wave 1 is T010 and T011, then T012.
- Phase 6 needs Phase 5. Its Wave 1 is T013 and T014, then T015.
