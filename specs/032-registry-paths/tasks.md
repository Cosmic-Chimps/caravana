# Tasks: Registry credentials per path

**Input**: [plan.md](./plan.md), [registry-paths.spec.md](./registry-paths.spec.md), [research.md](./research.md), [data-model.md](./data-model.md), [contracts/](./contracts/)

> **Scale note**: about 30 files in seven packages plus the gate. The layers chain (registry → nomadjob → deploy → service → api/ui), so most of the code is foundational; the story phases own the sync, the API, the console and the lab proof. Watch `SyncRegistryCopies`: a save must leave every other app's variable unwritten.

Tests are required (constitution VIII): each layer's tests are in the task that changes it.

## Phase 1: Setup

- [x] **T001** Migration rebuilding `registries` with `path` and `PRIMARY KEY (host, path)` · internal/store/migrations/0025_registry_paths.sql

## Phase 2: Foundational (blocks every story)

**Wave 1 — independent (different files):**
- [x] **T002** [P] `EntryCandidates`, `MatchEntry`, `SplitEntry`, `EntryHost` with table tests (whole parts, `acme-old`, `docker.io/library`) · internal/registry/entry.go, internal/registry/entry_test.go
- [x] **T003** [P] `Registry.Path`, `Key()`, (host, path) keys on every store method, migration test of an old row · internal/store/registries.go, internal/store/registries_test.go

**⟶ Wait for Wave 1 to finish, then:**

**Wave 2 — independent (different files):**
- [x] **T004** [P] `Credentials(ctx, host, repository)` hook and `Test(ctx, host, repository)`; test that the hook gets the repository · internal/registry/client.go, internal/registry/client_test.go
- [x] **T005** [P] `PrivateRegistry` returns the matched entry; auth `server_address` stays the host; goldens unchanged; prefix test · internal/nomadjob/translate.go, internal/nomadjob/private_test.go

**⟶ Wait for Wave 2 to finish, then:**

- [x] **T006** Deployer: `RegistryVariablePath(entry)`, candidate lookup wired as the client hook, `registry` item holds the entry, `withPullCredentials` by entry, `TestRegistry(entry)`, export header by host, `SyncRegistryCopies(ctx, entry, entries)` recomputing each copy's entry from its job image (backup and restore variables from their task image, 017's rule when the job can't be read); tests · internal/deploy/registries.go, internal/deploy/deploy.go, internal/deploy/export.go, internal/deploy/registries_test.go

**⟶ Wait for T006, then:**

- [x] **T007** Service: entries in (`canonicalRegistryEntry`, field `path`), `privateRegistries` by `Key()`, Test note for a path entry; tests · internal/service/registries.go, internal/service/export.go, internal/service/registries_test.go

## Phase 3: User Story 1 - Two accounts on one registry (P1) 🎯 MVP

Files: internal/service/registries_deploy_test.go, internal/service/registries_flows_test.go

- [x] **T008** [US1] Deploy flows: two prefixes on one host, each image's digest lookup and per-task pull login use its own entry; a wrong prefix login fails only that prefix · internal/service/registries_deploy_test.go, internal/service/registries_flows_test.go

**Checkpoint**: two accounts on one host deploy through the service.

## Phase 4: User Story 2 - The host-only entry stays the fallback (P1)

Files: internal/service/registries_hostport_test.go

- [x] **T009** [US2] Fallback cases: host entry serves other paths and `acme-old`; a prefix-only setup leaves other images anonymous; host-only entries from before behave as in 017 · internal/service/registries_hostport_test.go

**Checkpoint**: upgrade and fallback proven at the service layer.

## Phase 5: User Story 3 - Changing an entry reaches only the apps that use it (P2)

Files: internal/deploy/registries_copies_test.go

- [x] **T010** [US3] Sync tests: saving a prefix rewrites only its copies, adding a prefix moves its copies off the host entry, removing it moves them back (or empties them), other hosts and unchanged copies are not written · internal/deploy/registries_copies_test.go

**Checkpoint**: saving or removing an entry rewrites only the copies it serves.

## Phase 6: User Story 4 and the front doors (P3)

Files: internal/api/registries.go, internal/api/registries_test.go, api/openapi.yaml, internal/ui/registries.go, internal/ui/settingsnav.go, internal/ui/templates/settings.html, internal/ui/registries_test.go, internal/ui/static/app.css

**Wave 1 — independent (different files):**
- [x] **T011** [P] [US4] API `?path=` on put, delete and test; `path` in `Registry`; openapi 0.35.0; tests · internal/api/registries.go, internal/api/registries_test.go, api/openapi.yaml
- [x] **T012** [P] [US4] Console: `host/path` field and hint, `path` errors on the field, test/remove routes with hidden `host`, count per entry, Test note in the notice; tests; `tools/ui-css.sh build` if classes change · internal/ui/registries.go, internal/ui/settingsnav.go, internal/ui/templates/settings.html, internal/ui/registries_test.go

**Checkpoint**: every front door handles path entries.

## Phase 7: Polish

**Wave 1 — independent (different files):**
- [x] **T013** [P] Gate `private-registry` step: two htpasswd users, `teama/`, `teamb/`, `other/` images, wrong host login, per-prefix deploys, rotation and removal checks, new passwords in the sentinel search · cmd/caravana/gate_test.go
- [x] **T014** [P] Notes: `changelog.d/registry-paths.md` and the CLAUDE.md paragraph · changelog.d/registry-paths.md, CLAUDE.md

**⟶ Wait for Wave 1 to finish, then:**

- [x] **T015** Validate against Success Criteria: build, vet, staticcheck, gofmt, `go test ./...`, `-race` on the touched packages, linux/arm64 build, `tools/ui-css.sh check`, `go mod tidy -diff`, then the lab's `private-registry` step

## Dependencies & Execution Order

- Setup (T001) → Foundational (T002–T007) → US1, US2, US3, US4 → Polish.
- Foundational: Wave 1 (T002, T003) blocks Wave 2 (T004, T005), which blocks T006, which blocks T007.
- US1 (T008), US2 (T009) and US3 (T010) touch disjoint files and can run in any order after T007.
- US4: T011 and T012 are one wave.
- Polish: T013 and T014 are one wave; T015 runs last.
