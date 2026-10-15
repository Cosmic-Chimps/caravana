# Data model: Volumes panel and volume backups

The decisions behind each part are in [research.md](./research.md).

## App model (`internal/app`)

- `BackupVolume = "volume"`, beside `BackupPostgres`. `Backup{Engine, Schedule, Keep}` is otherwise
  unchanged, with the same defaults (daily at 03:00 UTC, keep 7).
- Validation (`validate.go`, `backup.engine`):

| Engine | Refused when (field) |
| --- | --- |
| `postgres` | as in feature 010 (no ports, no password, ...) |
| `volume` | the service mounts no volume (`services[i].backup.engine`: "the service has no volume"); a volume it mounts is also mounted by another service (`services[i].backup.engine`, naming the volume and the other service) |
| any | no destination (service check, as feature 010) |

- The Compose overlay's `backup` takes `engine: volume` unchanged (`compose.Overlay.Backup`).

## Job shape (`internal/nomadjob`, pure)

Every service with volumes gets, in its task group:

| Field | Value |
| --- | --- |
| Task name | `caravana-volumes`. It is reserved: a service can't be called that (validation). |
| Lifecycle | `prestart`, not a sidecar |
| Image | `Settings.BackupImage` (pinned rclone, BusyBox) |
| Mounts | each of the service's volumes at `/caravana-volumes/<volume>`, writable |
| Template | `nomadVar "nomad/jobs/<app>/<service>/caravana-volumes"` → `local/request.env`, `change_mode = noop`, no `env` |
| Script | `VolumeScript` const: sizes, then the request if any (research R4) |
| Resources | 100 MHz, 64 MB |

A service with a `volume` backup asks for its volumes writable at group level. Its main task keeps
`read_only` on its own mounts. Translation is pure, as before, and golden files cover a service
with a volume and no backup, one with a `volume` backup, and one with a read-only mount.

## The task's variable

`nomad/jobs/<app>/<service>/caravana-volumes`, written by `deploy` before every register of a
service with volumes:

| Item | When |
| --- | --- |
| `RCLONE_CONFIG_DEST_*`, `BACKUP_PATH` | the service has a `volume` backup (from feature 010's `destinationItems`) |
| `KEEP` | the same |
| `REQUEST`, `ACTION` (`backup` \| `restore`), `OBJECT`, `NOT_AFTER` | while a run is requested; empty otherwise |
| `LIMIT` | the run's time limit in seconds |

Changing the destination (`SetBackupDestination`) rewrites these items too, keeping the request
items.

## Result files (in the copy's `/alloc/data/caravana/`)

`sizes`:

```text
<volume> <kilobytes>
```

`<request>.result`:

```text
status=succeeded|failed
message=<why it failed>
object=<BACKUP_PATH>/<ts>/
volume.<name>.bytes=<archive bytes>
volume.<name>.size=<data kilobytes>
expired=<ts> <ts> ...
```

## Store (migration `0008_volume_backups.sql`)

```sql
ALTER TABLE backups  ADD COLUMN engine     TEXT NOT NULL DEFAULT 'postgres'
                     CHECK (engine IN ('postgres', 'volume'));
ALTER TABLE backups  ADD COLUMN stopped_ms INTEGER;          -- volume runs: how long the service was stopped
ALTER TABLE backups  ADD COLUMN volumes    TEXT;             -- volume runs: JSON [{name, bytes, sizeKB}]
ALTER TABLE restores ADD COLUMN engine     TEXT NOT NULL DEFAULT 'postgres'
                     CHECK (engine IN ('postgres', 'volume'));
ALTER TABLE restores ADD COLUMN stopped_ms INTEGER;

CREATE TABLE volume_sizes (
  app_id      TEXT NOT NULL REFERENCES apps (id),
  volume      TEXT NOT NULL,
  kilobytes   INTEGER NOT NULL,
  measured_at TEXT NOT NULL,
  PRIMARY KEY (app_id, volume)
) STRICT;
```

`DeleteApp` also deletes the app's `volume_sizes` rows.

- `BackupRecord` and `RestoreRecord` gain `Engine`, `Stopped *time.Duration`, and (backups only)
  `Volumes []VolumeArchive{Name, Bytes, SizeKB}`.
- A volume run's ID is `<service>-<unix seconds>`, as for Postgres. Its `job_id` is
  `<app>_<service>_volumes/<ts>`, and its `object` is the `<ts>/` prefix.

## Service

- **`VolumeRun`** (internal): app, service, request ID, action, object, copy ID, `NOT_AFTER`.
- **`BackUpNow`** and **`Restore`** branch on the service's engine. For `volume`:
  1. lock the app;
  2. check it isn't busy, and find the running copy (`ErrNoRunningCopy` if none);
  3. write the request and wait for the rendered file;
  4. record the run as running and return it;
  5. a goroutine restarts the copy's tasks, waits for the result file and the main task's
     restart, records the outcome and the stop duration, clears the request, and unlocks.
- **`Volumes(ctx, appID) ([]VolumeView, error)`**: the definition's volumes, plus Nomad's host
  volumes for the app; sizes refreshed from running copies (then `volume_sizes`); the last backup
  per service.
- **Scheduler**: started in `Service.Start`. It ticks every 30 s and runs due `volume` backups
  (research R5).
- **Resume** (`Start`): volume runs still marked running are settled (research R7).

## Deploy (`internal/deploy`)

| Function | What |
| --- | --- |
| `WriteVolumeRequest(ctx, appID, service, req)` / `ClearVolumeRequest` | the variable's request items |
| `WaitRequestRendered(ctx, copyID, requestID)` | `AllocFS().Cat("caravana-volumes/local/request.env")` until it holds the ID |
| `RestartCopy(ctx, copyID)` | `Allocations().Restart(..., AllTasks: true)` |
| `VolumeResult(ctx, copyID, requestID)` | reads and parses `<request>.result`; `ok = false` until it exists |
| `VolumeSizes(ctx, copyID)` | reads `sizes` |
| `StopDuration(ctx, copyID, task, since)` | from the main task's events |
| `HostVolumes(ctx, appID)` | name → host path, for the panel |
| `Copy.RequestedRestarts` | restarts with the event "User requested all tasks to restart" |

## State of a volume run

```text
requested ──(file shows request)──▶ restarting ──(result file)──▶ succeeded | failed
    │                                    │
    └─(30 s, file never showed it)──▶ failed ("the copy didn't pick up the request")
                                         └─(limit + 5 min, no result)──▶ failed ("timed out")
```

Every path ends with the request cleared and the lock released.
