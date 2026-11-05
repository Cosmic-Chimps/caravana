# Implementation Plan: SQLite backups

**Branch**: `alta/cozy-kiln-6m49` | **Date**: 2026-11-05 | **Spec**: [spec.md](spec.md) | **Research**: [research.md](research.md)

## Summary

A third backup engine, `sqlite`, for a service that keeps its data in one SQLite file on a volume.
A backup runs inside the app's own copy while the service keeps running: Caravana runs
`VACUUM INTO` and `PRAGMA quick_check` in a new `caravana-sqlite` sidecar, then gzips and uploads the
copy from a new `caravana-upload` sidecar (rclone), with the destination's credentials on the
command's standard input. A restore downloads and checks the backup in the same two sidecars, stages
it on the database's volume, and then swaps the file through feature 016's stop-run-start
(`caravana-volumes` restarts with a `restore-sqlite` request). Schedule, retention, Back up now,
records, needs-you items and notifications are the other engines'.

## Technical Context

Go 1.27, Nomad 2.x API client, SQLite store (`modernc.org/sqlite`), Nomad alloc exec (020.5),
016's volume-run machinery (`claimVolumeRun`, `freshVolumeRequest`, `WriteVolumeVariable`,
`WaitRequestRendered`, `RestartCopy`, `awaitVolumeResult`, `awaitStop`, the scheduler and resume).
Images: `alpine/sqlite` 3.53.4 pinned by index digest (new setting `CARAVANA_SQLITE_IMAGE` /
`-sqlite-image`) and the existing pinned rclone image.

## Constitution Check

| Principle | How this plan meets it |
| --- | --- |
| I. Small by Default | No new Go module, no runtime dependency. One more pinned image, like `names` and rclone. Two idle sidecars per sqlite-backed service only (research R1, costs listed). |
| II. One Model, Many Inputs | `app.Backup` gains `file`; Compose's overlay carries it through `app.Backup`. The helper tasks are named here: **`caravana-sqlite` and `caravana-upload`, poststart sidecars of a service with a `sqlite` backup**. The constitution's list of helpers is updated (PATCH 1.1.1, its own commit). `Translate` stays pure, with golden files. |
| III. Safe, Reversible Deploys | No floating tag: the image is pinned. A restore checks the backup before it stops anything, and puts the old files back on failure. |
| IV. Fail Loudly | Every refused `file` names `services[i].backup.file`; a missing database fails the run with its path; no empty file is created (research D3). |
| V. API-First | Console and API call the same service methods; `api/openapi.yaml` 0.32.0 in the same commit. |
| VI. Nomad-Native | Sidecars and alloc exec through the official client; the export writes the sidecars (golden HCL). |
| VII. Secure by Default | The destination's secret key only travels on the exec's standard input (research R3); no new variable or file holds it; a sentinel test checks records, logs, answers and the job. |
| VIII. Plain, Tested Go | Table tests for validation, golden job and HCL, shell tests for the scripts (as `VolumeScript`'s), fakes for the service runs, a gate step in the lab. |

No Complexity Tracking entry is needed.

## Project Structure (files this feature touches)

```text
internal/app/app.go, validate.go            BackupSQLite, Backup.File, SQLiteFile(), reserved names, FR-001 checks
internal/nomadjob/sqlite.go (new)           sidecar tasks, idle/snapshot/upload/download/stage scripts, output parsing
internal/nomadjob/translate.go              Settings.SQLiteImage, add the sidecars; caravana-volumes mount writable for the db volume
internal/nomadjob/volumes.go                VolumeScript: action restore-sqlite (FILE)
internal/nomadjob/testdata/                 golden job + HCL for a sqlite-backed app
internal/config/config.go                   CARAVANA_SQLITE_IMAGE / -sqlite-image
internal/deploy/actions.go                  Exec with standard input
internal/deploy/sqliterun.go (new)          SQLiteSnapshot, SQLiteUpload, SQLiteDownload, SQLiteStage (exec wrappers), destination lines
internal/deploy/volumerun.go                VolumeRequest.File, FILE key
internal/service/sqlitebackup.go (new)      startSQLiteBackup, startSQLiteRestore, their drives and settles
internal/service/backups.go, schedule.go, resume.go   route the sqlite engine
internal/store/migrations/0022_sqlite_backups.sql     engine CHECK gains 'sqlite' (backups, restores)
internal/api, api/openapi.yaml               engine enum, BackupPolicy.file, 0.32.0
internal/ui                                  Compose form engine + file; Backups tab: SQLite offer, engine words
cmd/caravana/gate_sqlite_test.go (new)       gate step sqlite-backup-restore
docs, CHANGELOG.md, CLAUDE.md, docs/plan.md, constitution (PATCH)
```

## Decisions carried into tasks

- Paths as the sidecars see them: the database at `/caravana-volumes/<volume>/<path in volume>`;
  the work directory `/alloc/data/caravana/sqlite` (cleared by each run's first command); the stage
  `/caravana-volumes/<volume>/.caravana-restore/<request>.db`.
- Exec commands are argv arrays (`/bin/sh -c <script> sh <args…>`), never a shell line built from
  input. Scripts are `const`s in `nomadjob`, POSIX sh, `$VAR` and `$(...)` only (the idle script is
  in the job and Nomad interpolates `${...}` there; the rest follow the same rule for one style).
- Script output: the copy prints `caravana-sqlite-copy bytes=<n>`; the upload prints the existing
  `caravana-backup key=<object> bytes=<n>` and `caravana-expired <ts>.sqlite.gz` lines and
  `caravana-warning <text>` when retention failed after a good upload; the download prints
  `caravana-sqlite-download bytes=<n>`. A failure exits non-zero with its reason as the last line
  of stderr; Caravana records `<step>: <that line>` (copy, check, upload, download, stage).
- Exec output limit 16 KiB per stream; the run's context ends at the run's limit plus 016's grace.
- `deploy.Exec(ctx, copyID, task, command, stdin io.Reader, limit)`: `RunCommand` and the terminal
  pass nil.
