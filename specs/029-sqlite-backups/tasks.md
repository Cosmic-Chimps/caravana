# Tasks: SQLite backups

Read [spec.md](spec.md), [research.md](research.md), [plan.md](plan.md) and
[contracts/api.md](contracts/api.md) first. Each phase ends with `go build ./... && go test ./...`
green for the packages it touched.

## Phase 1: Model and validation (`internal/app`)

- [x] T001 `app.BackupSQLite = "sqlite"`, `Backup.File string json:"file,omitempty"` (doc comment),
  the engine list in `Backup.Engine`'s comment. Reserved task names `SQLiteTaskName =
  "caravana-sqlite"`, `UploadTaskName = "caravana-upload"` beside `ReservedServiceName`, refused as
  service names with a message like the existing one.
- [x] T002 `func (s Service) SQLiteFile() (volume app.Volume, inVolume string, ok bool)`: the volume
  whose `Path` is the longest prefix of `Backup.File` + "/", and the path inside it (no leading
  "/"). Pure. ok false when no sqlite backup or no volume holds it.
- [x] T003 `validateBackup`: accept `sqlite`; the engine error lists three engines; `file` with
  another engine is refused on `.file`; `validateSQLiteBackup` (no volume → `.engine`, "the database
  file must be on a volume, and the service has none"; FR-001's rules on `.file`; the cron check of
  `validateVolumeBackup`). Table tests in `validate_test.go` for every rule.

## Phase 2: Job shape (`internal/nomadjob`, pure)

- [x] T004 `Settings.SQLiteImage`, `DefaultSQLiteImage =
  "alpine/sqlite@sha256:8df11342ae10bd360896a1f721d6e9e0a606520b58289e8a0398854d513b6555"`
  (comment: 3.53.4 on Alpine 3.24, an index for amd64 and arm64), `sqliteImage()`, pinned check in
  `Settings.Validate`. `internal/config`: `CARAVANA_SQLITE_IMAGE` / `-sqlite-image`, like
  `CARAVANA_NAMES_IMAGE`, with its tests; wired in `cmd/caravana/main.go` where the other images are.
- [x] T005 `sqlite.go`: `sqliteTask` and `uploadTask` (poststart, `Sidecar: true`, entrypoint
  `/bin/sh -c`, `IdleScript`, `KillTimeout` 1 s, resources from research R1, the db's volume
  mounted read-write at `VolumeRoot/<volume>` on `caravana-sqlite` only). `Translate` appends both
  after `caravana-volumes` for a sqlite-backed service. `volumeTask` mounts the database's volume
  writable (validation already refuses a read-only one; keep the guard explicit).
- [x] T006 Scripts, `const`s with doc comments, POSIX sh, `$VAR`/`$(...)` only, arguments
  positional (the exec passes `sh -c <script> sh <args…>`):
  - `IdleScript`: `trap 'exit 0' TERM INT; while :; do sleep 3600 & wait $!; done`.
  - `SQLiteCopyScript <db> <db as the service sees it> <out> <seconds>`: clear and create the work
    directory (`dirname <out>`); refuse a missing `<db>` ("there is no database file at <service
    path>") without opening it; `VACUUM INTO` under `timeout`; `PRAGMA quick_check` must print `ok`;
    print `caravana-sqlite-copy bytes=<n>`. Failure: exit 1, reason on stderr's last line, prefixed
    with the step (`copy:` / `check:`).
  - `SQLiteUploadScript <copy> <backup path> <ts> <keep> <seconds>`: read `RCLONE_CONFIG_DEST_*=…`
    lines from stdin and `export` them (nothing else); `gzip -c | rclone rcat dest:<path>/<ts>.sqlite.gz`
    with both exit codes checked (as `upload_volume`); `rclone size --json`; print `caravana-backup
    key=… bytes=…`; retention per research R4 printing `caravana-expired <ts>.sqlite.gz`, a failure
    there prints `caravana-warning …` and still exits 0; a failed upload deletes the object it
    started; the copy is always removed (`trap … EXIT`).
  - `SQLiteDownloadScript <object> <out> <seconds>`: stdin as above; clear and create the work
    directory; `rclone cat dest:<object> | gzip -dc > <out>` with both statuses checked; the file
    must start with `SQLite format 3`; print `caravana-sqlite-download bytes=<n>`.
  - `SQLiteStageScript <downloaded> <volume dir> <request> <seconds>`: `PRAGMA quick_check` = ok;
    refuse when `<volume dir>/.caravana-restore/old` exists (016's message); `mkdir -p
    <volume dir>/.caravana-restore`; `cp` to `.caravana-restore/<request>.db`; remove the download.
  - `ParseSQLiteOutput(stdout string) SQLiteOutput{Bytes, Object, Expired []string (timestamps),
    Warning}` for the three markers.
- [x] T007 `VolumeScript`: action `restore-sqlite` (`do_restore_sqlite`) per research R5 step 3,
  with `FILE` (`<volume>/<path in volume>`; refuse empty, absolute, or with a `..` component) read
  with the request. Keep every existing behaviour; add `FILE=` to the reset list in
  `handle_request`.
- [x] T008 Tests: script tests like `volumes_backup_test.go` / `volumes_restore_test.go` (run the
  scripts with `sh` and fake `rclone`/`sqlite3`/`timeout` on `PATH` where needed; the real
  `sqlite3` may be used when present, skipping otherwise) for each script and for
  `restore-sqlite` (owner and mode kept, `-wal`/`-shm`/`-journal` moved away, rollback on a failed
  rename, leftover `old/` refused, missing stage refused); `ParseSQLiteOutput` table test; a golden
  app `sqlite-backup` in `testdata` (JSON job and `*.golden.nomad.hcl`), and the HCL reflection
  test still passing.

## Phase 3: Nomad side (`internal/deploy`)

- [x] T009 `Exec` takes `stdin io.Reader` (nil for none) and passes it to Nomad's exec; update
  `RunCommand`'s and every other caller and fake (`deploytest`).
- [x] T010 `VolumeRequest.File` and the `FILE` key (in `volumeRequestKeys`, written when set).
- [x] T011 `sqliterun.go`: `SQLiteCopy(ctx, copyID, svc, request)`, `SQLiteUpload(ctx, copyID, appID,
  svc, dest, ts, keep)`, `SQLiteDownload(ctx, copyID, object, request)`, `SQLiteStage(ctx, copyID,
  svc, request)`, each building the argv from `nomadjob`'s scripts and paths, running `Exec` with a
  16 KiB limit in the right task, and returning the parsed output or an error whose message is
  `<step>: <last stderr line>` (or the exit status). `destinationLines(dest)` builds the stdin from
  `destinationItems` (only `RCLONE_CONFIG_DEST_*`), refusing a value with `\n` or `\r`. Map
  "copy not found / not running" as `RunningCopy`'s callers expect. Tests with the fake Nomad
  (`deploytest`): the argv, the task, the stdin, and that no error text holds the secret.

## Phase 4: Runs (`internal/service`)

- [x] T012 Store: migration `0022_sqlite_backups.sql` rebuilding `backups` and `restores` so
  `engine`'s CHECK allows `'sqlite'` (keep every column, default, index and the other CHECKs;
  read migrations 0005, 0008, 0021 and how 0014 rebuilt a table). A store test saves both with
  `engine = sqlite`.
- [x] T013 `sqlitebackup.go`: `hasSQLiteBackup`; `startSQLiteBackup(ctx, appID, service, trigger)`:
  claim (`claimVolumeRun`), re-check as `prepareVolumeRun` does (backed up, not stopped, destination,
  busy, `RunningCopy`), `freshVolumeRequest`, save the running record (engine `sqlite`, job ID
  `<app>_<service>_sqlite/<ts>`), then a tracked goroutine with the run's limit + grace: copy, upload,
  settle (`ExpireBackups` with `<service>-<ts>`, `Bytes`, `Object`, the warning as the message of a
  succeeded run), release, `backupsChanged`. Stopped stays nil.
- [x] T014 `startSQLiteRestore(ctx, appID, b)`: the same claim and checks; save the running restore
  (engine `sqlite`, ID `restoreRecordID`); goroutine: download, stage (a failure settles failed, the
  service never stopped), `WriteVolumeVariable(…, nil dest, 0 keep, request with File)`,
  `WaitRequestRendered` (tolerate `ErrRequestNotRendered` as `prepareVolumeRun` does),
  `RestartCopy`, `awaitVolumeResult`, `awaitStop`, `settleVolumeRestore`, clear the request, release.
- [x] T015 Routing: `backUpNow` and `Restore` send sqlite services to these; the scheduler
  (`runDueVolumeBackups`) runs sqlite backups too (log words stay generic: "scheduled backup");
  `resumeVolumeRuns`: a running sqlite backup is settled failed at once ("Caravana restarted during
  the backup; run it again"); a running sqlite restore resumes like a volume one, but fails at once
  when the `caravana-volumes` variable doesn't hold its request (it never reached the swap).
  `checkBackups`, `syncBackups`, `SetBackupDestination` need nothing (check, and say so in a
  comment where a reader would wonder).
- [x] T016 Tests with the fakes, as `volumebackup_test.go`/`volumerestore_test.go`: a backup that
  succeeds (record, bytes, object, expired), each failing step, the service never restarted, a
  restore that fails before the stop (no restart call), one that succeeds (stopped set), busy and
  stopped refusals, the scheduler picking a sqlite service, resume of both kinds, and a sentinel
  secret key absent from every record, log line and error.

## Phase 5: API and console

- [x] T017 `api/openapi.yaml` per `contracts/api.md`, version 0.32.0; the API's engine handling and
  tests (`internal/api/backups*_test.go`), the openapi sync test.
- [x] T018 Compose form: engine option `sqlite` "SQLite file (the service keeps running)" and a
  "Database file" field (`backupFile`), parsed, prefilled from a kept overlay, errors on
  `services[i].backup.file` drawn on it; "Choose none, PostgreSQL, Volume or SQLite file." Tests.
- [x] T019 Backups tab: a service with a volume and no policy keeps "Back up daily" (volume) and gains
  a small form "Back up a SQLite file instead" (a Database file field, prefilled with the first
  volume's path and "/", posting to `/apps/{id}/backups/policy` with `engine=sqlite&file=…`;
  `setBackupPolicy` accepts them, and a field error on `.file` is drawn on that field). Runs and
  restores say "SQLite" for the engine; the restore confirmation for a sqlite service says the
  service stops for a few seconds while the file is swapped; the built-in schedule's command words
  (`builtinCommand`) name the sqlite copy. Run `tools/ui-css.sh build` if a template gains classes.
  Tests.

## Phase 6: Lab, docs, finish

- [x] T020 Gate step `sqlite-backup-restore` (`cmd/caravana/gate_sqlite_test.go`) per
  [quickstart.md](quickstart.md) §3, added to `runGate` after `volume-backup-restore`.
- [x] T021 Docs: `CHANGELOG.md` (Unreleased), `docs/plan.md` (Backlog "Backups for more than
  Postgres": sqlite done in 029; the table row 9), `CLAUDE.md` "Where we are" (a short 029
  paragraph), `docs/mcp.md` only if it lists engines.
- [ ] T022 `go vet ./...`, `go tool staticcheck ./...`, `gofmt -l .`, `go test -race ./...`,
  `go mod tidy -diff`, the CGO-free cross-compile, `tools/ui-css.sh check`, `tools/ui-vendor.sh check`.
