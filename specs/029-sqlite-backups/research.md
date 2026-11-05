# Research: SQLite backups

## Findings

| # | Experiment | Result |
| --- | --- | --- |
| 016-L1 | A batch job asks for a volume the app's copy holds (`single-node-single-writer`) | Not placed. So no backup *job* can reach the database while the app runs. |
| 016-L4 | A task beside the service's own in its group mounts the same volume | Shares the group's claim, no conflict. |
| D1 | Docker, 2026-11-05: one container (uid 1000) inserts 20 KB rows into a WAL-mode database in a loop; a second container (root) runs `sqlite3 -bail -cmd ".timeout 60000" app.db "VACUUM INTO '/tmp/x.db'"` on the same volume | 0.1 s for 24 MB; `PRAGMA quick_check` ok; `count(*) = max(v)` (consistent); the writer never failed and went on (1184 → 1338 rows). The copy's journal mode is `delete`: no `-wal` to carry. |
| D2 | The same with the writer stopped (no `-wal`/`-shm` on disk), copy as root | SQLite creates `-wal` and `-shm` owned by the **database file's owner** (uid 1000), not root, and removes them at close. Running the copy as root can't lock the app out of its own files. |
| D3 | `sqlite3` on a path that doesn't exist | Creates an empty database file there (owned by root). The copy must check the file exists first. |
| D4 | Images | `alpine/sqlite` 3.53.4 (Alpine 3.24 plus `apk add sqlite`, runs as root, BusyBox with `sh`, `stat`, `timeout`, `gzip`, multi-arch index `sha256:8df11342…6555`). `rclone/rclone` (the pinned backup image) has BusyBox with `gzip`, `stat`, `timeout`, and runs as root. BusyBox `setpriv` can't change user (no `--reuid`), which D2 makes unnecessary. |
| D5 | `sqlite3 db "BEGIN" … "VACUUM INTO …"` | "cannot VACUUM from within a transaction": `VACUUM INTO` is its own transaction; nothing to wrap. |

The lab checks (Nomad 2.0.7) are the gate step `sqlite-backup-restore` (quickstart §3).

## R1. Where the copy is made: a sidecar in the app's own copy

**Decision**: every service with a `sqlite` backup gets two poststart sidecar tasks in its group,
both idle (`sleep` in a loop, exit 0 on SIGTERM) until Caravana runs a command in them with Nomad's
alloc exec (the `alloc-exec` capability 020.5 added; no policy change):
- `caravana-sqlite`, image `alpine/sqlite` (pinned, `CARAVANA_SQLITE_IMAGE`), mounts the
  database's volume read-write at `/caravana-volumes/<volume>` (the path `caravana-volumes` uses).
  It makes and checks copies.
- `caravana-upload`, the pinned backup image (rclone), no volume. It uploads and downloads.

They share `/alloc/data` (the group's shared directory), where the copy waits between the two:
`/alloc/data/caravana/sqlite/<request>.db`.

**Rationale**: the copy must be made while the app runs (the point of the engine), on a volume only
the app's group can mount (016-L1), with a `sqlite3` binary. No pinned image has both `sqlite3` and
`rclone`, and building our own image needs a release pipeline for it. Two small official-package
images, each used as built, are the plain way.

**Alternatives considered**:
- **The `volume` engine's restart**: stops the service, which is what this engine avoids.
- **A backup job beside the app**: can't mount the volume (016-L1).
- **One sidecar with `alpine/sqlite`, and rclone's static binary copied into `/alloc/data` by
  `caravana-volumes`**: 70 MB copied on every start, and it depends on `/alloc` not being `noexec`.
- **Fetching `sqlite3` or `rclone` as a Nomad `artifact`**: a download on every start of the copy,
  so a network outage would keep the app from starting; and the checksums differ per architecture.
- **Caravana as an S3 client (presigned uploads, its own listing for retention)**: new S3 code in
  Caravana; rclone already does all of it in the other engines.

**Cost**: two idle containers per backed-up service. Reservations: `caravana-sqlite` 20 MHz /
16 MB (ceiling 256 MB), `caravana-upload` 20 MHz / 32 MB (ceiling 512 MB, rclone's multipart
buffers). The ceilings need memory oversubscription (013), as `caravana-volumes`' does.

## R2. The copy: `VACUUM INTO`, as root

**Decision**: `sqlite3 -bail -cmd ".timeout 60000" <db> "VACUUM INTO '<copy>'"`, then
`sqlite3 -bail <copy> "PRAGMA quick_check"`, which must print `ok`. The command checks first that
the database file exists (D3). It runs as root (D2).

**Rationale**: `VACUUM INTO` reads the database in one read transaction, so the copy is consistent
and never restarts, whatever the app writes meanwhile; in WAL mode it doesn't block writers (D1).
The `.backup` command uses the online backup API page by page, and a write from another process
restarts it from the beginning: on a busy database it may never finish. `VACUUM INTO` also leaves
out free pages, and its result is in rollback-journal mode, so a restore carries one file.

In rollback-journal mode the read transaction makes the app's writers wait while it runs; the
timeout on our side is 60 s for the lock, and the app's own busy timeout governs its writers.

## R3. The credentials: on the command's standard input

**Decision**: the upload and download commands read `RCLONE_CONFIG_DEST_*=<value>` lines from their
standard input and export them; Caravana sends them with the exec (`deploy.Exec` gains an input
argument). Values with a newline are refused before anything runs.

**Rationale**: the task needs no variable of its own, no template, no file with the secret key in
the copy, and Caravana knows the destination at the moment it runs the command (a destination
changed since the deploy is used at once). Nothing in the job, Nomad Variables or `/secrets`
changes. Arguments would show in the container's process list; standard input doesn't.

## R4. Names in the destination and the records

**Decision**:
- Object: `<bucket>/<prefix>/<app>/<service>/<ts>.sqlite.gz` (the same `BACKUP_PATH` as the other
  engines), `<ts>` the run's unix seconds.
- Record ID `<service>-<ts>` (as the other engines; `freshVolumeRequest` keeps it unique), job ID
  `<app>_<service>_sqlite/<ts>` (no Nomad job has it, as for `_volumes`), engine `sqlite`, bytes =
  the compressed object's size.
- Retention lists `<BACKUP_PATH>/`, keeps names matching `^[0-9]+\.sqlite\.gz$`, sorts by number
  and deletes the oldest beyond `keep`, never this run's; expired records are `<service>-<ts>`.
  Nothing else in the directory is touched (a service that changed engine keeps its old files
  until the owner deletes them, as today).

## R5. Restore: check first, swap while stopped

**Decision**:
1. In `caravana-upload`: `rclone cat dest:<object> | gzip -dc > /alloc/data/caravana/sqlite/<request>.db`
   (streamed: no `.gz` on disk), and the file must start with `SQLite format 3`.
2. In `caravana-sqlite`: `PRAGMA quick_check` must print `ok`; refuse when
   `<volume>/.caravana-restore/old` exists (an earlier restore's leftovers); copy the file to
   `<volume>/.caravana-restore/<request>.db`, on the database's own disk, and delete it from
   `/alloc/data`.
3. Write the request (`ACTION=restore-sqlite`, `FILE=<volume>/<path in the volume>`) into the
   `caravana-volumes` variable, wait until it is rendered, restart the copy's tasks. The
   `caravana-volumes` script (`VolumeScript`) then, with the service stopped:
   - reads the owner and mode of the current file (or of its directory and 0644 when there is none),
     and gives them to the staged file;
   - moves the file and its `-wal`, `-shm`, `-journal` into `.caravana-restore/old/`;
   - renames the staged file into place (same disk: a rename);
   - on any failure moves the old files back (and says so if it couldn't);
   - removes `.caravana-restore` and writes the result file.
4. Caravana reads the result, the stop's length, and records the restore (016's run code).

**Rationale**: the service is stopped only for renames, never for a download, and a backup that
can't be read never stops it (FR-004). The `caravana-volumes` variable of a sqlite service never
holds the destination: the restore's network part runs in `caravana-upload`.

**Alternatives considered**: downloading inside `caravana-volumes`, as the volume engine does:
simpler, but the service stays stopped for the download and there is no `sqlite3` there to check
the file.

## R6. Scheduling, locking, resuming

**Decision**: the volume engine's machinery, extended to sqlite services:
- Caravana's scheduler (`scheduleVolumeBackups`) runs sqlite backups on their cron slot.
- A run claims the app and holds its deploy lock until it is settled (`claimVolumeRun`), so a
  deploy waits instead of replacing the copy mid-copy, and `ErrBackupRunning` refuses a second run.
- `backupBusy` reads the store, as for volume runs.
- At start, a running sqlite backup is settled failed ("Caravana restarted during the backup; run it
  again"); a running sqlite restore is resumed like a volume restore (its result file, or failed when
  its request has expired).
- The run's limit is the volume limit (`CARAVANA_VOLUME_BACKUP_TIMEOUT`, 30 min), for the whole run.

## R7. Validation and reserved names

**Decision**: `app.Validate` checks FR-001 and refuses services named `caravana-sqlite` or
`caravana-upload` (as `caravana-volumes`). The schedule must parse with `cronexpr` (Caravana runs
it). A pure `app.Service.SQLiteFile()` returns the volume and the path inside it, for `nomadjob`,
`deploy` and `service`.

## R8. What doesn't change

No new Go module, no Nomad policy change (`alloc-exec`, `alloc-lifecycle` and `read-fs` exist), no
new Nomad Variable, no new runtime dependency. Postgres backups are untouched. The `volume` engine's
script gains one action and nothing else.
