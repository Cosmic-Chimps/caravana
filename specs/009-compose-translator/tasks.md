# Tasks: Compose translator

**Input**: [plan.md](./plan.md), [compose-translator.spec.md](./compose-translator.spec.md),
[research.md](./research.md), [data-model.md](./data-model.md), [contracts/](./contracts/)

**Scale note**: 30 tasks over about 30 files in seven packages. Phases 3, 4 and 5 own disjoint
files and build in parallel once Phase 2 is done. Phase 6 joins them. Golden jobs for
multi-service apps change on purpose in Phase 4 (the names sidecar).

Tests are required (constitution VIII).

## Phase 1: Setup

- [ ] **T001** Add `github.com/compose-spec/compose-go/v2` v2.16.1, tidy, and prove the no-CGO cross-compile · go.mod, go.sum

## Phase 2: Foundational (blocks every story)

Files: `internal/app/app.go`, `internal/app/validate.go`, `internal/app/validate_test.go`,
`internal/nomadjob/volume.go`, `internal/nomadjob/translate.go` (Settings only),
`internal/config/config.go`, `internal/config/config_test.go`

**Wave 1 — independent (different files):**

- [ ] **T002** [P] Add `SourceCompose`, `Volume{Name, Path, ReadOnly}`, `Service.Volumes`; accept both source kinds; volume rules (name, absolute path, one service per volume, one copy) with table tests · internal/app/app.go, internal/app/validate.go, internal/app/validate_test.go
- [ ] **T003** [P] Add `nomadjob.VolumeName(appID, volume)` = `<app>_<volume>` · internal/nomadjob/volume.go
- [ ] **T004** [P] Add `Settings.NamesImage` (must be pinned by digest), config `CARAVANA_NAMES_IMAGE` / `-names-image` defaulting to `busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e`, with tests · internal/nomadjob/translate.go (Settings and Settings.Validate only), internal/config/config.go, internal/config/config_test.go

## Phase 3: User Stories 1 + 2 - Compose file → app model, refuse and warn (P1) 🎯 MVP

Files: `internal/compose/compose.go`, `internal/compose/fields.go`, `internal/compose/service.go`,
`internal/compose/compose_test.go`, `internal/compose/testdata/`

**Goal**: `compose.Translate` turns a file plus overlay into an `app.App` and warnings, refusing
everything outside the allowlist by its Compose path.

### Tests

- [ ] **T005** [P] [US1] Fixture tests: a two-service file plus overlay gives the golden app model; ports, expose, command, env, replicas, resources, TCP default health, overlay domains/health/secrets, named volumes · internal/compose/compose_test.go, internal/compose/testdata/
- [ ] **T006** [P] [US2] Table tests: each refused attribute fails on its path (build, entrypoint, env_file, secrets, bind mount, tmpfs, privileged, profiles, include, unknown key, udp port, env without value, unset variable, overlay for an unknown service); each warned attribute warns on its path · internal/compose/compose_test.go

### Implementation

- [ ] **T007** [US1] Loading from bytes with the options of research R1, the variable lookup that refuses unset names, logrus silenced · internal/compose/compose.go
- [ ] **T008** [US2] The allowlist and the warned attributes with their messages (research R2) · internal/compose/fields.go
- [ ] **T009** [US1] One `ServiceConfig` plus overlay → `app.Service` (ports and labels, health, volumes, resources), deterministic order (services sorted by name) · internal/compose/service.go

**Checkpoint**: `go test ./internal/compose` proves the translation without Nomad.

## Phase 4: User Stories 3 + 4 (translator side) - Names sidecar, volumes in the job (P1/P2)

Files: `internal/nomadjob/translate.go` (except Settings), `internal/nomadjob/translate_test.go`,
`internal/nomadjob/testdata/`, `internal/app/apptest/fixtures.go`

- [ ] **T010** [US3] Multi-service apps: `<app>-<service>-internal` alloc-mode registration with canary tags; `names` prestart sidecar with the hosts template and script (research R3); the length rule in `Check` · internal/nomadjob/translate.go
- [ ] **T011** [US4] Volumes: group volume requests (`VolumeName`, single-node-single-writer, file-system), task mounts, `Canary: 0` for groups with a volume · internal/nomadjob/translate.go
- [ ] **T012** [US3] Golden cases: multi-service gains the sidecar; new `volumes` fixture; tests that a single-service app has no sidecar and the template skips canaries · internal/nomadjob/translate_test.go, internal/nomadjob/testdata/, internal/app/apptest/fixtures.go

**Checkpoint**: golden jobs show the sidecar, the internal registration and the volume.

## Phase 5: User Stories 4 + 5 (runtime side) - Create and delete volumes (P2/P3)

Files: `internal/deploy/volumes.go`, `internal/deploy/volumes_test.go`,
`internal/deploy/register.go`, `internal/deploy/deploytest/nomad.go`,
`internal/deploy/deploytest/nomad_test.go`, `internal/service/apps.go`,
`internal/service/volumes_test.go`, `internal/ui/configure.go`, `internal/ui/configure_test.go`

- [ ] **T013** [US4] Fake Nomad: `PUT /v1/volume/host/create` (pending, then ready), `GET /v1/volume/host/{id}`, `GET /v1/volumes?type=host&prefix=`, `DELETE /v1/volume/host/{id}` (refused while the job's allocations exist) · internal/deploy/deploytest/nomad.go, internal/deploy/deploytest/nomad_test.go
- [ ] **T014** [US4] `EnsureVolumes` (create the missing ones, wait for ready) called by `Register`; `DeleteVolumes` (by prefix, retrying "in use" up to 60 s); errors as `ErrNomadUnavailable`/`ErrNomadRejected` · internal/deploy/volumes.go, internal/deploy/register.go
- [ ] **T015** [US4] Tests for both against the fake · internal/deploy/volumes_test.go
- [ ] **T016** [US5] `DeleteApp` deletes the volumes after the purge, before the secrets; tests · internal/service/apps.go, internal/service/volumes_test.go
- [ ] **T017** [US5] Configure: a Volumes row per service, source "compose", and the delete sentence names each volume and says its data is erased; tests · internal/ui/configure.go, internal/ui/configure_test.go

**Checkpoint**: volumes are created before the first deploy and gone after delete.

## Phase 6: Integration - Deploy and preview through the API (P1)

Files: `internal/service/compose.go`, `internal/service/compose_test.go`,
`internal/service/deploy.go`, `internal/api/compose.go`, `internal/api/compose_test.go`,
`internal/api/api.go`, `api/openapi.yaml`, `deploy/vm/caravana-policy.hcl`,
`cmd/caravana/gate_test.go`

- [ ] **T018** [US1] `DeployCompose` and `PreviewCompose`; `deploy` takes extra warnings (stored with the record); warning for a volume no longer mounted · internal/service/compose.go, internal/service/deploy.go
- [ ] **T019** [US1] Service tests: deploy, preview saves nothing, warnings reach the record, refused file saves nothing · internal/service/compose_test.go
- [ ] **T020** [US1] `POST /v1/apps/{id}/compose` and `/compose/preview` handlers, routes · internal/api/compose.go, internal/api/api.go
- [ ] **T021** [US1] API tests against the contract: 202, preview 200 with no secret values, error fields as Compose paths, nothing saved on refusal · internal/api/compose_test.go
- [ ] **T022** [US1] `api/openapi.yaml` 0.7.0: two operations, `ComposeRequest`, `ComposeOverlay`, `ComposePreview`, `Volume`, source kind `compose` · api/openapi.yaml
- [ ] **T023** [US4] Policy: `host-volume-create`, `host-volume-delete`, `host_volume "*_*" { policy = "write" }` · deploy/vm/caravana-policy.hcl
- [ ] **T024** [US3] Gate step `compose-app`: a two-service Compose app (`store` serves a file from its volume, `web` fetches `http://store:8080/` by name), checked by exec; redeploy; still reached by name and data kept; delete; volume gone · cmd/caravana/gate_test.go

**Checkpoint**: the Phase 2 gate shape works: a multi-service Compose app with a volume.

## Phase 7: Polish

Files: `CLAUDE.md`, `docs/plan.md`

- [ ] **T025** [P] Update where we are · CLAUDE.md, docs/plan.md
- [ ] **T026** Validate against the Success Criteria: `go build ./...`, `go vet ./...`, `go tool staticcheck ./...`, `go test -race ./...`, `gofmt -l .`, `go mod tidy -diff`, the linux/arm64 and amd64 cross-compiles, `tools/ui-css.sh check`, `tools/ui-vendor.sh check`, shellcheck, `CARAVANA_LAB=1 go test ./cmd/caravana -run TestLab`

## Dependencies & Execution Order

- Setup → Foundational → Phases 3, 4, 5 in parallel → Phase 6 → Polish.
- Foundational: Wave 1 (T002–T004) is one wave of independent files.
- Phase 3: T005, T006 then T007 → T008 → T009.
- Phase 4: T010 → T011 → T012.
- Phase 5: T013 → T014 → T015; T016 and T017 after T014.
- Phase 6: T018 → T019; T020 after T018 → T021; T022, T023 alongside; T024 last.
