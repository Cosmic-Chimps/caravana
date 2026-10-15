# Tasks: Volumes panel and volume backups

**Input**: [plan.md](./plan.md), [volume-backups.spec.md](./volume-backups.spec.md),
[research.md](./research.md), [data-model.md](./data-model.md), [contracts/](./contracts/),
[quickstart.md](./quickstart.md)

**Scale note**: 24 tasks over about 35 files.

The design comes from the lab (research L1–L11, R1). Read research.md before any task: the backup
runs inside the app's own copy, as the `caravana-volumes` prestart task, and Caravana triggers it
by restarting the copy's tasks.

The three risky tasks:
- **T004**: the script must always exit 0 and never replay a request.
- **T011**: the run must always release the lock and clear the request.
- **T016**: a failed restore must leave the volumes as they were.

Shared files mean the story phases run one after another, in the order below: `api/openapi.yaml`,
`internal/api/parity_test.go`, `internal/ui/backups.go`, `internal/ui/templates/app.html`,
`internal/service/backups.go` and `cmd/caravana/gate_test.go`. Inside a phase, [P] tasks touch
different files and can run side by side.

**Tests are required** (constitution VIII):
- Each task writes its own tests next to the code: table-driven, `testing` only.
- Each task leaves these passing: `go build ./...`, `go vet ./...`, `gofmt -l .` and
  `go test ./<its packages>`.
- A task that changes templates or styles runs `tools/ui-css.sh build` and commits `app.css`.
- An API change edits `api/openapi.yaml` in the same commit.
- Nomad-facing code is tested against `deploytest` fakes; the lab and gate steps prove it for
  real.

## Phase 1: Setup

- [X] T001 Make `github.com/hashicorp/cronexpr` a direct dependency in go.mod. It is already in go.sum through the Nomad client; run `go mod tidy` and check that `go mod tidy -diff` is clean. Also add `alloc-lifecycle` and `read-fs` to the namespace capabilities in deploy/install/caravana-policy.hcl, with a comment line in the file's header comment explaining both (feature 016: restart a copy's tasks for a volume backup, and read the result file it writes; not `alloc-exec`). The commit message justifies the dependency (constitution I). Files: go.mod, go.sum, deploy/install/caravana-policy.hcl

## Phase 2: Foundational (blocks every story)

Files: `internal/app/*`, `internal/nomadjob/*`, `internal/deploy/*`, `internal/store/*`, and
`internal/service/backups.go` (engine branches only)

- [X] T002 [P] App model (research R8, data-model "App model"), in internal/app/app.go and validate.go:
  - Add `BackupVolume = "volume"` beside `BackupPostgres`, and update `Backup.Engine`'s doc comment.
  - Validate `backup.engine`. It accepts `postgres` or `volume`, else this field error: `%q is not supported; it is "postgres" or "volume"`.
  - For `volume`, refuse at `services[i].backup.engine`:
    - "the service has no volume to back up" when the service mounts no volume;
    - "volume %q is also mounted by service %q; a volume backup needs it to itself" when another service of the app mounts the same volume.
  - A service named `caravana-volumes` is refused at `services[i].name` (reserved), whatever its backup.
  - Postgres rules stay as they are.

  Table tests in internal/app/validate_test.go cover each case, and both engines with and without volumes.
- [X] T003 [P] Store (data-model "Store"):
  - Add internal/store/migrations/0008_volume_backups.sql, exactly as data-model.md says.
  - In internal/store/backups.go, `BackupRecord` and `RestoreRecord` gain `Engine string` (default `"postgres"` when empty on write), `Stopped *time.Duration` (stored as `stopped_ms`) and, on `BackupRecord`, `Volumes []VolumeArchive{Name string; Bytes int64; SizeKB int64}` (JSON in `volumes`).
  - Add internal/store/volumes.go with `SaveVolumeSize(ctx, appID, volume string, kb int64, at time.Time)` and `VolumeSizes(ctx, appID) (map[string]VolumeSize, error)`.
  - `DeleteApp` deletes `volume_sizes` rows.

  Tests:
  - round trip of the new fields;
  - old rows read with engine `postgres` and nil Stopped/Volumes;
  - sizes upsert;
  - DeleteApp clears them.
- [X] T004 [P] The task and the script (research R1–R4, R9, data-model "Job shape"), in internal/nomadjob/volumes.go (new) and translate.go:
  - **Names and constants**: `VolumeTaskName = "caravana-volumes"`; `VolumeVariablePath(appID, service)` = `nomad/jobs/<app>/<service>/caravana-volumes`; the result-file paths (`/alloc/data/caravana/sizes` and `/alloc/data/caravana/<request>.result`); the `VolumeScript` const.
  - **The task**: `volumeTask(appID string, svc app.Service, s Settings) *api.Task`:
    - prestart, not a sidecar; `Settings.BackupImage`;
    - entrypoint `/bin/sh -c VolumeScript`;
    - each volume mounted writable at `/caravana-volumes/<name>`;
    - one template from the variable into `local/request.env`, `change_mode noop`, `Envvars` false. Use `{{ with nomadVar ... }}{{ range .Tuples }}{{ .K }}={{ printf "%s" .V | toJSON }}` lines, as `variableTemplate` does, so values are quoted for the shell's `.`;
    - 100 MHz, 64 MB.
  - **The group**: `Translate` appends the task to every group whose service has volumes. When the service has a `volume` backup, the group's `VolumeRequest`s are `ReadOnly: false`, while the main task's mounts keep their `ReadOnly`.
  - **VolumeScript** (POSIX sh, BusyBox, `$VAR` and `$(...)` only, never `${...}`, which Nomad interpolates):
    1. Source `/local/request.env` if present.
    2. Write `<volume> <kb>` lines (`du -sk`) for every `/caravana-volumes/*` to the sizes file.
    3. If `REQUEST` is empty, or `NOT_AFTER` < now, or the request's result file exists: exit 0.
    4. Otherwise run the backup or restore of research R4 under `timeout "$LIMIT"`, if BusyBox has `timeout`. Check with `command -v timeout` and run without it otherwise.
    5. Write the result file in data-model.md's format, and always exit 0.

    Keep it readable: one function per step, comments.
  - **Pure parsing**: `ParseVolumeResult(text string) (VolumeResult, error)` and `ParseVolumeSizes(text string) map[string]int64`.

  Tests:
  - golden files in internal/nomadjob/testdata for a service with a volume and no backup, one with a `volume` backup, and one with a read-only mount and a `volume` backup;
  - existing goldens of volume apps updated (they gain the task);
  - parse tests;
  - a test that `VolumeScript` contains no `${`;
  - the HCL export reflection test passes (extend hcl.go if it reports a field), and the export header lists the variable path.

**⟶ Wait for T002–T004, then:**

- [X] T005 Deploy, Nomad side (research R2, R3, R9; data-model "Deploy"):
  - In internal/deploy/volumerun.go (new), with `context.Context` first everywhere:
    - `WriteVolumeVariable(ctx, a app.App, svc app.Service, dest *BackupDestination, keep int, req *VolumeRequest)` writes the task's variable: destination items via `destinationItems` when dest is non-nil, `KEEP`, `LIMIT`, and the request items, empty when req is nil;
    - `ClearVolumeRequest(ctx, appID, service)` keeps the rest;
    - `RunningCopy(ctx, appID, service) (copyID string, ok bool, err error)`;
    - `WaitRequestRendered(ctx, copyID, requestID)` polls `AllocFS().Cat(copy, "caravana-volumes/local/request.env")` every 500 ms until it contains the ID, up to 30 s;
    - `RestartCopy(ctx, copyID)` is `Allocations().Restart` with all tasks;
    - `VolumeResult(ctx, copyID, requestID) (nomadjob.VolumeResult, bool, error)`;
    - `VolumeSizes(ctx, copyID)`;
    - `StopDuration(ctx, copyID, mainTask string, since time.Time) (time.Duration, bool, error)`: from the main task's events, the first `Terminated` after `since` to the next `Started`.
  - `Register` (or its caller `EnsureVolumes`) writes the variable with no request for every service with volumes before registering, so the template never blocks (lab L6). It keeps an existing request's items when they aren't expired.
  - `SetBackupDestination` in internal/deploy/backups.go also rewrites the destination items of every `.../caravana-volumes` variable.
  - `SyncBackups` creates periodic jobs only for `postgres`, so a service whose engine became `volume` has its old job purged.
  - In internal/deploy/copies.go, `Copy.RequestedRestarts` counts task events of type `Restart Signaled` whose message contains "User requested all tasks to restart".
  - In internal/deploy/deploytest, fakes for alloc restart (it re-runs a configurable script result), alloc FS reads (a map of path → text per copy), and host volumes.

  Tests: each function against the fakes, and the variable never containing a secret in the job.
- [X] T006 Service engine branches, internal/service/backups.go, so feature 010's code ignores `volume` services:
  - `checkBackups` asks a `volume` service only for a destination: no credentials, no ports.
  - `syncBackups` passes only Postgres creds.
  - `recordApp` skips `volume` services; their records are written by the run, T011.
  - `backupBusy` for a `volume` service looks only at store records with status running (backups and restores).
  - `ListBackups` returns both engines.

  Tests: a `volume` service deploys with only a destination, and gets no periodic job; the watcher doesn't touch its records; `backupBusy` reads the store.

**Checkpoint**: `go test ./...` passes. Every app with a volume gains the task at its next deploy and measures its volumes; nothing triggers a backup yet.

## Phase 3: User Story 1 - Back up an app's volume on a schedule (Priority: P1) 🎯 MVP

**Goal**: "Back up now" and the schedule stop the service, archive its volumes to the destination, start it again, and record the run with its size and stop duration.

**Independent test**: quickstart §2. Uptime Kuma 2 with a Volume backup: back up now gives a succeeded run with size and "stopped N s" under 30 s; the app is healthy with its monitor; the job version is unchanged; the archive is in the bucket; keep is applied.

Files: `internal/service/volumebackup.go`, `internal/service/schedule.go`, `internal/service/service.go`, `internal/service/resume.go`, `internal/service/attention.go`, `internal/api/backups.go`, `internal/api/errors.go`, `api/openapi.yaml`, `internal/ui/backups.go`, `internal/ui/edit.go`, `internal/ui/composeform.go`, `internal/ui/templates/app.html`, `internal/ui/templates/compose.html`, `cmd/caravana/gate_test.go`

- [ ] T007 [US1] Lab check of two open points (research R4, R12), before writing the run. In the Lima lab (`deploy/dev/lab.sh up`; `limactl shell --workdir / caravana-lab sudo lab-nomad ...`):
  1. Does the pinned backup image's BusyBox have `timeout`? Run `docker run --rm --entrypoint timeout <DefaultBackupImage> --help`.
  2. Deploy a scratch job with the T004 task shape. Does `nomad alloc status` show the prestart task's 64 MB added to the running copy's reservation?

  Record both answers under this task. If there is no `timeout`, T011 enforces the limit by restarting again with the request cleared. Clean up the scratch job, volume and variable.
- [ ] T008 [P] [US1] The script's backup path in internal/nomadjob/volumes.go (`VolumeScript`), per research R4:
  - Stream each volume: `tar -czf - -C <dir> --exclude ./.caravana-restore . | rclone rcat "dest:$BACKUP_PATH/$ts/<v>.tar.gz"`, with `ts` = the request's timestamp item `TS`.
  - Record bytes with `rclone size --json` or `rclone lsl`, and sizes from `du -sk`.
  - Apply retention over the `<ts>/` directories (`rclone lsf --dirs-only`, sorted, delete the oldest beyond `KEEP` with `rclone purge`), printing each into `expired=`.
  - On any failure, `rclone purge "dest:$BACKUP_PATH/$ts"` and status failed with the failing step's last stderr line as message.

  Run the script in tests under a real `sh` with a fake `rclone` (a shell function or PATH stub writing to a temp dir) and a temp `/caravana-volumes` (make the root a variable, `ROOT`, defaulting to `/caravana-volumes`). Cases:
  - backup succeeded with two volumes;
  - upload failure purges;
  - retention deletes the oldest;
  - expired request ignored;
  - existing result ignored;
  - exit code always 0.

  These tests skip when `sh` or `tar` is missing.
- [ ] T009 [P] [US1] Scheduler (research R5), in internal/service/schedule.go:
  - `type clock interface{ Now() time.Time }` (real by default; tests inject).
  - `dueSlot(spec string, lastStart, now time.Time) (slot time.Time, due bool, skipped bool)` is pure, via `cronexpr`. Due when a slot is in (lastStart, now] and `now - slot < 1h`; skipped when the latest slot is older than an hour.
  - `scheduleVolumeBackups(ctx)` ticks every 30 s (`Service.scheduleGap`), lists apps, and for each `volume` service with a due slot calls the same path as BackUpNow (T011) with trigger "schedule". It logs skipped slots.
  - Started from `Service.Start` beside `watchBackups`.

  Table tests for `dueSlot`: daily, hourly, after a restart within the hour, missed by more than an hour, a cron string that doesn't parse.

**⟶ Wait for T007–T009, then:**

- [ ] T010 [P] [US1] Store a run's request (research R2, R7), in internal/service/volumebackup.go (new):
  - `type volumeRequest struct{ ID, Action, Object, TS string; NotAfter time.Time }`.
  - `newVolumeRequest(action, object string, now time.Time, limit time.Duration)`: the ID is `<service>-<unix>`, as Postgres run IDs; `NotAfter` = now + limit + 5 min.
  - `CARAVANA_VOLUME_BACKUP_TIMEOUT` is added to internal/config, default 30m.

  Unit tests.
- [ ] T011 [US1] The run (research R1–R3, R6, R7; data-model "State of a volume run"), in internal/service/volumebackup.go:
  - `startVolumeRun(ctx, appID, service, action, object)`:
    1. Take the app's `appState.mu`.
    2. Check `backupBusy` (`ErrBackupRunning`) and the running copy (`ErrNoRunningCopy`, a new error).
    3. Write the request, `WaitRequestRendered`, save the record as running (engine `volume`, `job_id` `<app>_<service>_volumes/<ts>`), `backupsChanged`.
    4. Return the record.
    5. A goroutine (tracked in `s.watchers`) then:
       - calls `RestartCopy`;
       - polls `VolumeResult` every 2 s up to the limit + 5 min;
       - waits up to 60 s for the main task's restart and reads `StopDuration`;
       - records the outcome: status, message, object, volumes, bytes (the sum), stopped, `expired` marks on earlier records named in `expired=`;
       - always `ClearVolumeRequest`, unlocks, and calls `backupsChanged`.
  - Any error before the restart unlocks, clears the request and fails the record with the reason.
  - `BackUpNow` branches here for `volume` services.
  - Resume, in internal/service/resume.go: at `Start`, each volume record still running is settled. Read the result file if the copy still exists; else mark failed "Caravana restarted during the run". Clear the request.

  Tests with the fakes:
  - success records size, volumes and stopped;
  - a failed result records the message;
  - no result before the deadline gives failed "timed out", with the request cleared and the lock released;
  - the rendered file never shows the request gives failed;
  - a deploy started during the run waits until it ends (use a fake whose restart blocks);
  - a second BackUpNow gives `ErrBackupRunning`;
  - no running copy gives `ErrNoRunningCopy`;
  - resume settles both ways.
- [ ] T012 [P] [US1] Needs-you (research R11), internal/service/attention.go: the crash-loop count subtracts `Copy.RequestedRestarts`. A failed volume backup is listed by the existing failed-backup rule; check it with a test. Tests: three requested restarts don't make a crash loop; a real crash plus requested restarts still counts the crash.

**⟶ Wait for T010–T012, then:**

- [ ] T013 [US1] API (contracts/api.md):
  - `Backup` JSON gains `engine`, `stoppedSeconds` (float, omitted when nil) and `volumes` (`[{name, bytes, sizeBytes}]`, sizeBytes = SizeKB×1024).
  - `Restore` JSON gains `engine` and `stoppedSeconds`.
  - `BackupPolicy.engine` gains the enum value `volume`.
  - `service.ErrNoRunningCopy` → 409 `no_running_copy`, message "The service has no running copy to back up. Deploy it first.", in internal/api/errors.go.
  - api/openapi.yaml 0.12.0 → 0.13.0 for all of it.

  Tests in internal/api/backups_test.go: a volume backup's JSON, the 409, the contract test.
- [ ] T014 [P] [US1] Console (contracts/console.md):
  - The engine select on Configure's edit form (internal/ui/edit.go) and the Compose overlay (internal/ui/composeform.go) gains `<option value="volume">Volume (stops the service while it runs)</option>`.
  - A `volume` engine error maps to the backup engine input: "This service has no volume to back up." for the no-volume refusal; the shared-volume message as the service gives it.
  - On the Backups tab (internal/ui/backups.go, templates/app.html):
    - the volume service heading line;
    - "stopped N s" on volume rows;
    - "Back up now" for a volume service inside a `<details>` with the copy from contracts/console.md;
    - the volume Restore confirmation copy;
    - the no-running-copy refusal sentence.

  Tests in internal/ui/backups_test.go and edit_test.go for each string, and that the forms work without JS.
- [ ] T015 [US1] Gate step, backup half, in cmd/caravana/gate_test.go: a new step `volume-backup-restore` after `backup-restore`, using its `rclone serve s3` bucket. It:
  - deploys app `vol` (busybox writing `/data/v` once at start, with a volume `data` and a `volume` backup);
  - writes "A" into the file through a console/API-free path. Use the app's command: it writes "A" when the file is missing;
  - backs up now through the API and waits until the run succeeds;
  - checks `stoppedSeconds > 0`, an object under `vol/web/<ts>/`, and the job version unchanged (read the job from Nomad).

  Leave the restore half to T017. Make sure `go vet ./cmd/caravana` passes.

**Checkpoint**: volume backups work end to end, the MVP. Release-worthy with a note that restore is next.

## Phase 4: User Story 2 - Restore a volume backup (Priority: P1)

**Goal**: restoring a volume backup stops the service, replaces its volumes with the archive's files, and starts it again; a failure changes nothing.

**Independent test**: quickstart §3. A then B, restore the A backup: A is back and B is gone; restoring a missing archive fails and the data is unchanged.

Files: `internal/nomadjob/volumes.go`, `internal/service/backups.go`, `internal/service/volumebackup.go`, `internal/ui/backups.go`, `cmd/caravana/gate_test.go`

- [ ] T016 [US2] The script's restore path in `VolumeScript`, per research R4. For each volume:
  1. Download `"$OBJECT<v>.tar.gz"` into `<dir>/.caravana-restore/<v>.tar.gz` and check it with `tar -tzf`.
  2. Only when every volume's archive checks out: per volume, move every entry but `.caravana-restore` into `.caravana-restore/old/`, then extract the archive into `<dir>`.
  3. On an extract failure: remove what was extracted (everything but `.caravana-restore`), and move the old entries back for **every** volume already swapped.
  4. Finally `rm -rf <dir>/.caravana-restore`.

  Status and message as for backups. Script tests (as in T008):
  - restore replaces files, and files not in the archive are gone;
  - a missing archive fails with the volume unchanged;
  - a corrupt archive fails with the volume unchanged;
  - a second volume's extract fails with both volumes unchanged;
  - exit 0 always.
- [ ] T017 [US2] Service and gate:
  - `Restore` in internal/service/backups.go branches to `startVolumeRun(..., "restore", backup.Object)` for `volume` backups. Only a succeeded, non-expired backup of a volume service, as today; the restore record has engine `volume`, and on completion `stopped`. The UI's restore confirmation copy comes from T014; check it renders for volume backups.
  - The gate step `volume-backup-restore` gains its second half:
    1. change the file to "B" (redeploy with an env change that makes the command overwrite it, or exec a write through a second backup-free path you document);
    2. restore the backup and wait for success;
    3. read "A" back through the app's HTTP answer (busybox httpd serving /data) or its logs;
    4. check the job version is unchanged.

  Service tests with the fakes: restore success, failure, busy refusal.

**Checkpoint**: a backup that restores, the Phase 2 gate's standard, for volumes.

## Phase 5: User Story 3 - See an app's volumes (Priority: P2)

**Goal**: a volumes panel on the Backups tab and `GET /v1/apps/{id}/volumes`.

**Independent test**: quickstart §1. Uptime Kuma's volume is listed with its mount, host path, size and backup state; a volume no longer mounted shows "not mounted".

Files: `internal/deploy/volumes.go`, `internal/service/volumes.go`, `internal/api/volumes.go`, `internal/api/api.go`, `api/openapi.yaml`, `internal/api/parity_test.go`, `internal/ui/backups.go`, `internal/ui/templates/app.html`, `cmd/caravana/console_lab_test.go`

- [ ] T018 [P] [US3] `deploy.HostVolumes(ctx, appID) (map[string]HostVolumeInfo, error)` in internal/deploy/volumes.go: name (without the app prefix) → host path (from `HostVolumes().Get`). Test with the fake.
- [ ] T019 [US3] `service.Volumes(ctx, appID) ([]VolumeView, error)` in internal/service/volumes.go (research R9, R10):
  - the stored app's volumes with their mounts, plus Nomad's volumes for the app that no service mounts (`Mounted` false);
  - sizes: read `VolumeSizes` from each running copy of a service with volumes (2 s timeout per copy, at most 4 at once), save them, then read `store.VolumeSizes`;
  - backed up, and last backup, from the store's records per service;
  - host paths from T018. When Nomad fails, leave them empty and still answer.

  Pure helper `volumeViews(app, hostVols, sizes, backups)` with table tests; one test for a failing Nomad.
- [ ] T020 [US3] API and console:
  - `listVolumes` (`GET /v1/apps/{id}/volumes`) in internal/api/volumes.go, registered in api.go, in api/openapi.yaml (`Volume` schema per contracts/api.md);
  - parity: `GET /apps/{id}/backups` gains `listVolumes`;
  - the console's Volumes section at the top of the Backups tab, drawn only when the app has a volume, with the columns and copy of contracts/console.md, and the "not backed up" hint linking to Configure.

  Tests: the API answer, the section's rows (mounted, not mounted, not measured yet, not backed up), and the parity test.
- [ ] T021 [US3] Console lab step `console-volumes` in cmd/caravana/console_lab_test.go: deploy a Compose app with a volume and a `volume` backup through the console, open Backups, check the Volumes row has a size, press "Back up now" (the form inside the `<details>`), wait for success, and check the row shows "stopped". Then delete the app. `go vet ./cmd/caravana` must pass.

**Checkpoint**: all three stories work in the API and the console.

## Phase 6: Polish & cross-cutting

- [ ] T022 [P] Docs:
  - CHANGELOG.md "Unreleased" gains Added (volume backups, restore, volumes panel, API 0.13.0) and Upgrade: rerun the installer for the new policy capabilities, then redeploy each app with a volume once so it gains the `caravana-volumes` task. Its panel says "not measured yet" until then. If CHANGELOG.md doesn't exist on this branch (branch `release-changelog` not merged yet), create the file with only that section and say so in the commit.
  - CLAUDE.md "Where we are" gains a feature 016 paragraph in the 015 style (task, request variable, restart, result file, scheduler, lock, migration 0008, policy capabilities, API 0.13.0, gate and lab steps).
  - docs/plan.md marks 016 done.
- [ ] T023 Full check (CLAUDE.md "Commands"): `go mod tidy -diff`, `go build ./...`, `go vet ./...`, `go tool staticcheck ./...`, `gofmt -l .`, `go test -race ./...`, the linux amd64 and arm64 CGO-free builds, `tools/ui-css.sh check`, `tools/ui-vendor.sh check`, and shellcheck on deploy/install via Docker. Run them all and fix what fails.
- [ ] T024 Lab validation: `CARAVANA_LAB=1 go test ./cmd/caravana -run TestConsoleLab -v`, then `CARAVANA_LAB=1 go test ./cmd/caravana -run TestLab -v -timeout 25m` (the lab applies caravana-policy.hcl, so the new capabilities are exercised), then quickstart §1–§4 by hand with Uptime Kuma 2. Record results here, as 015's T020 did.

## Dependencies & Execution Order

- **Setup**: T001 first.
- **Foundation**: (T002 ∥ T003 ∥ T004), then T005, then T006.
- **US1**: (T007 ∥ T008 ∥ T009), then (T010 ∥ T012), then T011, then (T013 ∥ T014), then T015. T011 needs T007's answers and T008's script.
- **US2**: needs T011. T016, then T017.
- **US3**: needs only the foundation (T005 for sizes). T018, then T019, then T020, then T021. It could run beside US1, but it shares app.html, openapi.yaml and parity_test.go, so by default it runs after US2.
- **Polish**: T022 any time after the stories; T023, then T024 last.

Story order: US1, then US2, then US3.

## Parallel examples

```text
Foundation: T002 (internal/app)   ∥ T003 (internal/store)        ∥ T004 (internal/nomadjob)
US1:        T007 (lab check)      ∥ T008 (script backup path)    ∥ T009 (scheduler)
            T010 (request)        ∥ T012 (attention)
            T013 (internal/api)   ∥ T014 (internal/ui)
```

## Implementation strategy

- **MVP**: Setup, Foundation and US1 (T001–T015). Uptime Kuma gets nightly off-node copies,
  which is the gap on the host today.
- **Then** US2 (restore, without which a backup is a guess) and US3 (the panel). Release as
  v0.4.0 after US2 at the latest; a backup without a restore shouldn't ship alone for long.
- **Delegation**: each phase goes to one Sonnet agent with this file, the spec, plan, research,
  data model and contracts. Opus reviews the diff, runs the tests (and for US1 and US2, the gate
  step in the lab) before the next phase starts.
