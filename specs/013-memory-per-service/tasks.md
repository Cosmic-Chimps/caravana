# Tasks: Memory per service

**Input**: [plan.md](./plan.md), [memory-per-service.spec.md](./memory-per-service.spec.md),
[research.md](./research.md), [data-model.md](./data-model.md), [contracts/](./contracts/)

Tests are required (constitution VIII).

## Phase 1: Setup

No new module or tool.

## Phase 2: Foundational (blocks every story)

Files: `internal/app/app.go`, `internal/app/validate.go`, `internal/app/validate_test.go`,
`internal/nomadjob/translate.go`, `internal/nomadjob/translate_test.go`,
`internal/nomadjob/testdata/`, `internal/config/config.go`, `internal/config/config_test.go`

**Wave 1 — independent (different files):**

- [x] **T001** [P] `Resources.MemoryMaxMB` (`memoryMaxMB`). Validation: at least 10 when set, and `memoryMB` no larger than it when both are set. Tests · internal/app/app.go, internal/app/validate.go, internal/app/validate_test.go
- [x] **T002** [P] Configuration: `CARAVANA_DEFAULT_MEMORY_MAX_MB` / `-default-memory-max-mb` (default 1024), and `CARAVANA_DEFAULT_MEMORY_MB` defaults to 256. The default ceiling must be at least the default reservation. Tests · internal/config/config.go, internal/config/config_test.go

**⟶ Wait for Wave 1 to finish, then:**

- [x] **T003** Translator: `Settings.DefaultMemoryMaxMB`, plus the reservation and ceiling rules from data-model.md on every service task (`MemoryMB`, `MemoryMaxMB`). The `names` sidecar and the backup jobs are unchanged. Table tests for the four cases, golden files with `-update`, and the HCL coverage list gains `Resources.MemoryMaxMB` · internal/nomadjob/translate.go, internal/nomadjob/translate_test.go, internal/nomadjob/hcl.go, internal/nomadjob/hcl_test.go, internal/nomadjob/testdata/

## Phase 3: User Story 1 - An app with no memory settings just runs (P1) 🎯 MVP

Files: `deploy/install/install.sh`, `deploy/dev/lab.sh`, `deploy/dev/install-test.sh`

- [ ] **T004** [US1] The installer and `lab.sh up` turn on Nomad's memory oversubscription when it's off (`operator scheduler set-config -memory-oversubscription=true`), and the installer lists `scheduler` in `changed:`. `install-test.sh` checks that it's on after the install and that a rerun leaves it alone · deploy/install/install.sh, deploy/dev/lab.sh, deploy/dev/install-test.sh

**Checkpoint**: a service with no settings reserves 256 MB and may burst to 1 GB on an installed host or in the lab.

## Phase 4: User Story 2 - An owner sets memory per service (P2)

Files: `internal/compose/fields.go`, `internal/compose/service.go`, `internal/compose/compose_test.go`,
`internal/compose/testdata/`, `internal/ui/configure.go`, `internal/ui/configure_test.go`,
`api/openapi.yaml`, `internal/api/apps_test.go`, `internal/deploy/register_test.go`,
`deploy/examples/hollo/compose.yaml`

**Wave 1 — independent (different files):**

- [x] **T005** [P] [US2] Compose: `mem_reservation` and `deploy.resources.reservations.memory` set the reservation; `mem_limit` and `deploy.resources.limits.memory` set the ceiling. The allowlist gains those paths; other reservations stay refused. Fixture and tests. Hollo's example comment is updated · internal/compose/fields.go, internal/compose/service.go, internal/compose/compose_test.go, internal/compose/testdata/, deploy/examples/hollo/compose.yaml
- [x] **T006** [P] [US2] Console: the definition's Memory row reads `256 MB reserved, up to 1024 MB`, with `default` for zero. Test · internal/ui/configure.go, internal/ui/configure_test.go
- [x] **T007** [P] [US2] API: `api/openapi.yaml` 0.10.0 with `Resources.memoryMaxMB` and `memoryMB` described as the reservation. A test that a deploy with both round-trips and that a reservation above the ceiling is 400 on `services[0].resources.memoryMB`. A test that Nomad's oversubscription warning reaches the deployment's warnings · api/openapi.yaml, internal/api/apps_test.go, internal/deploy/register_test.go

**Checkpoint**: both numbers come from JSON and Compose, show in the console, and are in the contract.

## Phase 5: User Story 3 - The upgrade runs on the real host (P3)

Files: `CLAUDE.md`, `docs/plan.md`

- [x] **T008** [US3] Docs: where we are (013), the backlog item marked done, and the release note for `v0.1.1` (what the upgrade changes on a host) · CLAUDE.md, docs/plan.md

## Phase 6: Polish

- [x] **T009** Validate against the Success Criteria: build, vet, staticcheck, `go test -race`, gofmt, tidy, both cross-compiles, UI checks, shellcheck (v0.10.0), `TestLab`, and `install-test.sh` (install, then an upgrade from a build without the setting). After merge and tag, the maintainer upgrades the real host (SC-003).

## Dependencies & Execution Order

- Phase 2: Wave 1 is T001 and T002, then T003. It blocks everything after it.
- Phase 3 (T004) and Phase 4 (T005, T006 and T007 in parallel) both need Phase 2. They don't depend on each other.
- Phase 5 (T008) comes after Phases 3 and 4, then Phase 6 (T009).
