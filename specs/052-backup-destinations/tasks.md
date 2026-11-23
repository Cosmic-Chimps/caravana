# Tasks 052: Several backup destinations and a second copy

## Phase A: model, store, destinations, resolution (existing behaviour unchanged)

- [x] T001 `internal/app`: `Backup.Destination`, `CopyTo`, `CopyKeep`, `CopyKeepOrDefault()`, the checks of D3 in
      `validateBackup`; table tests (each refusal names its field; an existing policy's JSON and revision unchanged).
- [x] T002 Migration `0042_backup_destinations.sql` (D1) and the store methods of D1 for destinations, projects,
      records' `destination`; tests: a store migrated from 0041 with one destination has `main` as the default with
      `secret_path` `caravana/backup-destination` and every backup and restore row says `main`; one with none has no
      destination; the partial unique index holds one default; `DeleteApp` removes copies.
- [x] T003 `internal/deploy`: `BackupDestination.Name`, `remoteKeys`, `BACKUP_DESTINATION`, `isDestinationKey`,
      `SyncBackups` per service, the upload script's `dest=` (goldens), `BackupRun.Destination`, `PointBackups` in
      place of `SetBackupDestination`; `deploytest` fakes; tests.
- [x] T004 `internal/service/destinations.go`: D2 (list with used-by, save, delete with refusals, the old
      `SetBackupDestination` on the default, `destinationNamed`) and secrets at `secret_path`; tests with a sentinel
      secret key in no record, log line or error.
- [x] T005 Resolution and checks (D4): `backupPlaces`, `checkBackups`' new refusals, `UpdateProject`'s
      `backupDestination` (service and store), `syncBackups` with each service's destination, the watcher recording
      `Destination`, volume and sqlite runs recording theirs; tests.
- [x] T006 Repoint (D6): after a save, a default change and a project default change; tests.

## Phase B: copies, checks, restore, attention

- [x] T007 `internal/nomadjob/backupcopy.go` and `backupcheck.go` (D8, D9): IDs, jobs, scripts; goldens
      `backup-copy-file`, `backup-copy-dir`, `backup-check-test`, `backup-check-locate`; script tests under a real `sh`
      with a fake rclone (copy file and dir, retention with pins and a bad pin list, empty copy, check test ok and each
      failed step, locate found/missing/unreachable).
- [x] T008 `internal/deploy`: `StartBackupCopy`, `BackupCopyOutcome`, `PurgeBackupCopy`, `CheckBackupPlaces`;
      `deploytest` fakes; tests (keys only in the job's variable, purged afterwards; redaction).
- [x] T009 `internal/service/backupcopies.go` (D7): queue on success for all three run kinds, the pass (settle, start,
      limits, failures), events wake; tests with the fakes, including a restart with running and pending rows.
- [x] T010 Test (D10) and restore from a place (D11) for the dump, volume and sqlite engines; `ListBackups` and
      pinning with places; tests (locate picks the copy when the primary is missing and marks it expired there;
      `from`; `backup_missing`).
- [x] T011 Attention and notifications (D12): `backup_copy_failed`, the destination in `backup_failed`,
      `ProblemParts`, `notify.Event.Destination`; the words test walks the new kind.

## Phase C: API

- [ ] T012 `internal/api`: the routes and codes of `contracts/api.md`, error mapping, `Backup.copies`,
      `Restore.destination`, the restore body, `Project.backupDestination`; `api/openapi.yaml` 0.58.0 (contract and
      parity tests, the scope walk).
- [ ] T013 A sentinel test in `api`: a destination's secret key in no answer of any route, log line or error.

## Phase D: console and docs

- [ ] T014 Settings › Backup storage (D14): list, add, edit, Test, Make default, Remove with refusals; the nav state.
- [ ] T015 Project page "Backups go to"; Backups tab: the policy line, "Where backups go" (`SetBackupPlaces`), the
      copies on each row, "Restore from"; Compose form draft carries destination and copies; the attention row.
      Sentinel test in `ui`. `tools/ui-css.sh build`.
- [ ] T016 Constitution 1.2.3 (PATCH: II names `<app>_<service>_copy-<id>` and `_backup-check-<id>`), its own commit.
- [ ] T017 `docs/backup-destinations.md`, `changelog.d/backup-destinations.md`, `CLAUDE.md`'s feature paragraph.

## Phase E: lab

- [ ] T018 Gate step `backup-copies` (D15).
- [ ] T019 Console step `console-backup-destinations` (D15).
