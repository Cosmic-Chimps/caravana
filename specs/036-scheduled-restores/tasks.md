# Tasks: Restore a pinned backup on a schedule

**Input**: [plan.md](./plan.md), [spec.md](./spec.md), [data-model.md](./data-model.md), [contracts/](./contracts/)

Tests are required (constitution VIII): each task's tests are in the task. Commit per part (A, B, C, D) with
the files of that part; every commit builds and passes `go test ./...`.

## Part A — model, store, engines (foundation)

- [x] **T001** Migration `0029_restore_schedules.sql`: `backups.pin`, `backups.pinned_at`, unique partial index;
  `restores` rebuilt with `skipped`, `trigger`, `pin` (copy every row, indexes back). Migration test of an old row
  (a restore reads trigger `owner`) · internal/store/migrations/0029_restore_schedules.sql, internal/store/*_test.go
- [x] **T002** [P] `app.RestoreSchedule`, `Backup.Restore`, `ValidLabel`, validation of both fields for every
  engine; table tests (good, missing either, bad cron, six fields, bad label, upper case) · internal/app/app.go,
  internal/app/validate.go, internal/app/validate_test.go
- [x] **T003** [P] Store: `Pin`/`PinnedAt`, `PinBackup` (moves the label, returns the previous holder),
  `UnpinBackup`, `PinnedBackup`, `LastScheduledRestore`, `RestoreRecord.Trigger`/`Pin`, `SaveBackup` leaves pins
  alone, `LatestBackupRuns` skips skipped restores, consts; tests · internal/store/backups.go, internal/store/backups_test.go
- [x] **T004** [P] Engines' retention with pins: `uploadScript` (`BACKUP_PINNED`), `VolumeScript`
  (`PINNED`, reset in `handle_request`), `SQLiteUploadScript` (6th arg). Script tests with a fake `rclone` for
  each: pinned kept and not counted, `keep` of unpinned honoured, a bad list deletes nothing and says so, the
  current run never deleted. The Postgres script gets its first script test (fake `rclone` and, on macOS, a
  `stat` that answers `-c %s`) · internal/nomadjob/backup.go, volumes.go, sqlite.go and their tests
- [x] **T005** Deploy: `SyncBackups(..., pins map[string][]string)` writes `BACKUP_PINNED`; `SetBackupPins`;
  `VolumeRequest.Pinned` → item `PINNED` (in `volumeRequestKeys`); `SQLiteUpload(..., pinned []string)`; tests
  with the deploytest fakes · internal/deploy/backups.go, volumerun.go, sqliterun.go, tests

## Part B — service

- [x] **T006** Pins: `Service.PinBackup(ctx, appID, backupID, label)`, `UnpinBackup` (D3: TryLock → `ErrAppBusy`,
  `backupBusy` → `ErrBackupRunning`, store first, then `SetBackupPins` for Postgres, rollback on its failure);
  `syncBackups` passes pins; volume and sqlite backup runs pass the pinned stamps; tests · internal/service/backuppins.go (new),
  backups.go, volumebackup.go, sqlitebackup.go, tests
- [x] **T007** Restore generalised: `restoreBackup(ctx, appID, b, trigger, label)` used by `Restore` (owner) and
  the clock; volume and sqlite restore records carry trigger and pin · internal/service/backups.go, volumebackup.go, sqlitebackup.go
- [x] **T008** The clock: `runDueRestores` in the scheduler pass, `scheduledRestore` with every outcome of the
  data-model table, `SetBackupScanGap`; tests with the fake clock: due runs once, catch-up within an hour runs the
  latest slot once, older skipped, busy → skipped record (not retried), no pin / expired / engine changed →
  failed record, stopped app → nothing, owner restores don't move the base · internal/service/restoreclock.go (new), schedule.go, service.go, tests
- [x] **T009** Deploy warning (D7) and needs-you: `restore_failed` from a refused run with its detail, a skipped
  restore never hides a failed one, a later success clears it; notifier test that a refused scheduled restore
  opens `problem.opened` · internal/service/deploy.go, attention.go, tests

## Part C — front doors

- [x] **T010** API: `pinBackup`, `unpinBackup` (routes, error mapping incl. `app_busy`, field `label`),
  `Backup.pin/pinnedAt`, `Restore.trigger/pin/skipped`, `BackupPolicy.restore`; `api/openapi.yaml` 0.39.0 with
  descriptions per contracts/api.md; scope walk lists the two routes denied; contract tests · internal/api/backups.go,
  api.go, scope tests, api/openapi.yaml, tests
- [x] **T011** Compose overlay path for `restore` (errors at `services.<svc>.backup.restore.*`), MCP's backup
  types if it decodes them (it passes the body through) · internal/compose, tests
- [x] **T012** Console per contracts/console.md: run row Pin…/Unpin, pinned runs always listed, Keep text, the
  Restore schedule line and form, recent restores' who-words, the Compose form's two fields (draft round trip),
  `parity_test.go` entries, `tools/ui-css.sh build` if classes change · internal/ui/backups.go, composeform.go,
  templates/app.html, templates/compose.html, internal/api/parity_test.go, tests
- [x] **T013** Docs: `changelog.d/scheduled-restores.md` (Added; Upgrade: nothing to rerun), CLAUDE.md feature
  paragraph, `docs/` mention where backups are described

## Part D — lab and Aspire

- [x] **T014** Lab: `startLab`'s `serviceHook` calls `SetBackupScanGap(10 * time.Second)`; `sqlite-backup-restore` gains keep-2 + pin + five backups + scheduled restore;
  `volume-backup-restore` gains pin + scheduled restore; both clear the schedule · cmd/caravana/gate_test.go,
  gate_sqlite_test.go, lab_test.go
- [x] **T015** (34602c3) Aspire (`../aspire-hosting-caravana`, branch `scheduled-restore`, not pushed):
  `WithCaravanaScheduledRestore`, overlay model, tests, README, CHANGELOG
- [x] **T016** (2026-11-13: `volume-backup-restore` passed in 359 s and `sqlite-backup-restore` in 250 s, each with its scheduled restore; two earlier sqlite runs lost an exec's output after the restore, which `main` can do too: CodeAlta task `phpv`) Run in the lab: `-run 'TestLab/^(volume|sqlite)-backup-restore$'`, then the checks of CLAUDE.md's
  Commands (build, vet, staticcheck, gofmt, race, cross-compile, ui-css check, release check)
