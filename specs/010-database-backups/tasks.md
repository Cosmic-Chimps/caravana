# Tasks: Database backups

**Input**: [plan.md](./plan.md), [database-backups.spec.md](./database-backups.spec.md),
[research.md](./research.md), [data-model.md](./data-model.md), [contracts/](./contracts/)

**Scale note**: 24 tasks over about 28 files. The job shapes (Phase 2) are pure and golden;
everything Nomad-facing is behind `internal/deploy` and checked against the fake Nomad, then in
the lab by the gate step.

Tests are required (constitution VIII).

## Phase 1: Setup

No new module. The rclone image digest is pinned in Phase 2.

## Phase 2: Foundational (blocks every story)

Files: `internal/app/app.go`, `internal/app/validate.go`, `internal/app/validate_test.go`,
`internal/compose/compose.go`, `internal/compose/service.go`, `internal/compose/compose_test.go`,
`internal/nomadjob/backup.go`, `internal/nomadjob/backup_test.go`, `internal/nomadjob/testdata/`,
`internal/nomadjob/translate.go` (Settings only), `internal/config/config.go`,
`internal/store/migrations/0005_backups.sql`, `internal/store/backups.go`,
`internal/store/backups_test.go`, `internal/store/apps.go`

**Wave 1 — independent (different files):**

- [x] **T001** [P] `Service.Backup{Engine, Schedule, Keep}` with rules (engine, keep, schedule fields, ports, POSTGRES_PASSWORD present) and tests · internal/app/app.go, internal/app/validate.go, internal/app/validate_test.go
- [x] **T002** [P] Store: migration 0005 (destination, backups, restores), CRUD, rows deleted with the app, tests · internal/store/migrations/0005_backups.sql, internal/store/backups.go, internal/store/backups_test.go, internal/store/apps.go
- [x] **T003** [P] `Settings.BackupImage` (pinned rclone) and `CARAVANA_BACKUP_IMAGE` · internal/nomadjob/translate.go, internal/config/config.go

**⟶ Wait for Wave 1 to finish, then:**

**Wave 2 — independent (different files):**

- [x] **T004** [P] `nomadjob.BackupJobID`, `RestoreJobID`, `BackupJob`, `RestoreJob` (research R2, R6), golden files and tests · internal/nomadjob/backup.go, internal/nomadjob/backup_test.go, internal/nomadjob/testdata/
- [x] **T005** [P] Compose overlay `backup` · internal/compose/compose.go, internal/compose/service.go, internal/compose/compose_test.go

## Phase 3: User Stories 1 + 3 - Destination and scheduled backups (P1) 🎯 MVP

Files: `internal/deploy/backups.go`, `internal/deploy/backups_test.go`,
`internal/deploy/deploytest/nomad.go`, `internal/service/backups.go`,
`internal/service/backups_test.go`, `internal/service/deploy.go`, `internal/service/service.go`,
`internal/service/apps.go`

- [ ] **T006** [US1] Fake Nomad: `GET /v1/jobs?prefix=`, periodic force creating `<job>/periodic-<n>` children, escaped job IDs in `/v1/job/...` paths, `GET /v1/evaluation/{id}` · internal/deploy/deploytest/nomad.go
- [ ] **T007** [US1] `deploy`: SyncBackups (variables + periodic jobs, remove unwanted), SetBackupDestination (rewrite every backup variable), ForceBackup, BackupRuns (children with outcome, size, key, message from logs), DeleteAppJobs (prefix purge + variables) · internal/deploy/backups.go
- [ ] **T008** [US1] Tests for those against the fake · internal/deploy/backups_test.go
- [ ] **T009** [US3] Service: Get/SetBackupDestination (plain fields in the store, secret in `caravana/backup-destination`, rewrite variables) · internal/service/backups.go
- [ ] **T010** [US1] Deploy: refuse backups without a destination; after Register, sync backup jobs with credentials from env and secret sets; warning when the sync fails · internal/service/deploy.go
- [ ] **T011** [US1] Watcher (every 15 s): record runs, mark expired, from Start; BackUpNow refusing a running one; ListBackups · internal/service/backups.go, internal/service/service.go
- [ ] **T012** [US1] DeleteApp: delete the app's other jobs and their variables after the purge · internal/service/apps.go
- [ ] **T013** [US1] Service tests: destination round trip without the secret, deploy registers the job, refusals, back up now, a run recorded as succeeded and failed, retention, delete · internal/service/backups_test.go

## Phase 4: User Story 2 - Restore (P1)

Files: `internal/service/restore.go`, `internal/service/restore_test.go`,
`internal/deploy/restore.go`

- [ ] **T014** [US2] `deploy.Restore` (purge old, copy variable, register), `RestoreStatus` (dead → outcome, pending too long → failed) · internal/deploy/restore.go
- [ ] **T015** [US2] Service Restore (one at a time), watcher records restores · internal/service/restore.go
- [ ] **T016** [US2] Tests · internal/service/restore_test.go

## Phase 5: API and console

Files: `internal/api/backups.go`, `internal/api/backups_test.go`, `internal/api/api.go`,
`internal/api/errors.go`, `internal/api/parity_test.go`, `api/openapi.yaml`,
`internal/ui/backups.go`, `internal/ui/backups_test.go`, `internal/ui/settings.go`,
`internal/ui/app.go`, `internal/ui/configure.go`, `internal/ui/ui.go`,
`internal/ui/templates/app.html`, `internal/ui/templates/settings.html`,
`internal/ui/static/app.css`

- [ ] **T017** [US1] API: five operations, error codes `backup_running`, `no_backup_destination` · internal/api/backups.go, internal/api/api.go, internal/api/errors.go
- [ ] **T018** [US1] `api/openapi.yaml` 0.8.0 · api/openapi.yaml
- [ ] **T019** [US1] API tests (contract schemas, no secret in any answer) · internal/api/backups_test.go
- [ ] **T020** [US3] Console Settings: Backup destination section · internal/ui/settings.go, internal/ui/templates/settings.html
- [ ] **T021** [US1] Console app page: Backups tab (list, back up now, restore with typed name), delete sentence says backups stay; parity entries; tests · internal/ui/backups.go, internal/ui/app.go, internal/ui/configure.go, internal/ui/ui.go, internal/ui/templates/app.html, internal/ui/backups_test.go, internal/api/parity_test.go

## Phase 6: Gate and polish

Files: `deploy/vm/caravana-policy.hcl`, `cmd/caravana/gate_test.go`, `CLAUDE.md`, `docs/plan.md`

- [ ] **T022** Policy: variables `caravana/*` · deploy/vm/caravana-policy.hcl
- [ ] **T023** Gate step `backup-restore` (research R9) · cmd/caravana/gate_test.go
- [ ] **T024** Docs, then validate: build, vet, staticcheck, race tests, gofmt, tidy, cross-compiles, ui checks, shellcheck, TestLab · CLAUDE.md, docs/plan.md

## Dependencies & Execution Order

- Foundational (Wave 1 → Wave 2) → Phase 3 → Phase 4 → Phase 5 → Phase 6.
- Phase 3: T006 → T007 → T008; T009 → T010 → T011 → T012 → T013.
- Phase 4 after T011. Phase 5 after Phase 4. T023 after Phase 5.
