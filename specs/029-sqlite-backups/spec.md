# Feature Specification: SQLite backups

**Feature Branch**: `alta/cozy-kiln-6m49` (a CodeAlta worktree branch, from `main` at 0.18.0)

**Created**: 2026-11-05

**Status**: Implemented

**Input**:
- **CodeAlta task `2026-10-20-backlog-sqlite-backup-engine-ggbm`:** "Add backup engine `sqlite`: a
  consistent copy with `sqlite3 .backup` or `VACUUM INTO`, never a raw copy of a file in use. It uses
  the same destination, retention, schedule, back up now, restore and needs-you items as the other
  engines. The spec decides how the file path is named, and how restore swaps the file while the
  service is stopped. MySQL/MariaDB and Redis come later."
- **docs/plan.md, Backlog "Backups for more than Postgres" (sqlite: Phase 2.6):** real apps keep
  data in SQLite files (Fhodor). Today they can only use the `volume` engine, which stops the
  service for every backup.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Back up a SQLite database without stopping the app (Priority: P1)

The owner runs an app that keeps its data in one SQLite file on a volume (`/data/app.db`). They set
the service's backup to the `sqlite` engine and name the file. Every night at 03:00 UTC (or on the
schedule they chose) Caravana takes a consistent copy of the database while the app keeps serving,
checks it, compresses it and uploads it to the installation's backup destination, keeping the last 7
(or the number they chose). "Back up now" does the same at once.

**Why this priority**: this is the feature: the `volume` engine already backs SQLite files up, but
stops the service each time.

**Independent Test**: an app whose only service writes rows into a WAL-mode SQLite file in a loop.
Back up now: the backup succeeds, the service's task was never restarted, rows kept being written
during the run, and the object in the destination is a gzipped SQLite file that passes
`PRAGMA quick_check`.

**Acceptance Scenarios**:

1. **Given** a service with `backup: {engine: sqlite, file: /data/app.db}` and `/data` a volume of
   the service, **When** a backup runs, **Then** the copy is made with `VACUUM INTO` (one read
   transaction: consistent, and in WAL mode it never blocks the app's writers), checked with
   `PRAGMA quick_check`, gzipped and uploaded to `<bucket>/<prefix>/<app>/<service>/<ts>.sqlite.gz`.
2. **Given** a backup running, **Then** the service's own task keeps running: no restart, no new
   job version, no Nomad deployment.
3. **Given** more succeeded backups than `keep`, **When** a backup succeeds, **Then** the oldest are
   deleted from the destination and marked expired, as for the other engines.
4. **Given** the file doesn't exist yet (the app hasn't created it), **When** a backup runs,
   **Then** it fails with "there is no database file at /data/app.db", and no empty file is created
   in the volume.
5. **Given** a backup fails (the copy, the check or the upload), **Then** the run is recorded failed
   with the step and its last line of error, nothing partial stays in the destination, and the
   needs-you list and notifications show it as for any backup.

---

### User Story 2 - Restore a SQLite backup (Priority: P1)

The owner picks a succeeded backup and restores it. Caravana downloads it and checks it while the
app still runs, then stops the service for the few seconds it takes to put the file in place, and
starts it again.

**Why this priority**: a backup that can't be restored is not a backup.

**Independent Test**: after a backup, the app writes a marker row; restore the backup: the restore
succeeds, the marker row is gone, the file is owned by the same user with the same mode as before,
and the old `-wal`/`-shm` files did not survive into the restored database.

**Acceptance Scenarios**:

1. **Given** a succeeded, unexpired sqlite backup, **When** the owner restores it, **Then** the
   backup is downloaded and decompressed into the copy, checked with `PRAGMA quick_check`, and only
   then is the service stopped.
2. **Given** the download or the check fails, **Then** the restore fails and the service was never
   stopped.
3. **Given** the service is stopped for the swap, **Then** the current database file and its
   `-wal`, `-shm` and `-journal` files are moved aside, the restored file takes the database's
   path with the old file's owner and mode (or, when there was no file, its directory's owner and
   mode 0644), the service starts again, and the old files are deleted. If any step of the swap
   fails, the old files are put back and the restore is recorded failed.
4. **Given** the restore ended, **Then** its record says how long the service was stopped, as a
   volume restore's does.

---

### User Story 3 - Choose the engine and the file in the console and through Compose (Priority: P2)

In the Compose form the engine list offers "SQLite file (the service keeps running)" with a
"Database file" field. On the Backups tab, a service with a volume and no backup policy offers,
beside "Back up daily" (the volume engine), a way to back up a SQLite file instead, with the file's
path. The runs list says "SQLite" for these runs. The API (`deployApp`, `deployCompose`'s overlay)
takes `backup.file`.

**Acceptance Scenarios**:

1. **Given** the Compose form, **When** the owner picks SQLite and names a file outside every
   volume of the service, **Then** the form is drawn again with the error on the Database file
   field, naming the volumes it may be on.
2. **Given** an API deploy with `engine: sqlite` and no `file`, **Then** 400 `invalid_field` on
   `services[i].backup.file`.

---

### Edge Cases

- The file is on a read-only volume: refused at deploy (`services[i].backup.file`), since the app
  can't write its database there and a restore couldn't either.
- The file names a `-wal`, `-shm` or `-journal` file, the volume's mount itself, or a path under
  `.caravana-restore`: refused at deploy.
- `file` set with the `postgres` or `volume` engine: refused (`services[i].backup.file`: only the
  sqlite engine names a file).
- A backup of a service with no running copy: 409 `no_running_copy`, as for the volume engine.
- A backup or restore already running for the service: 409 (`ErrBackupRunning`), as today.
- The app is stopped: refused with `app_stopped`, and the scheduler skips it, as today.
- A deploy while a sqlite backup runs: the deploy waits for the run, as it waits for a volume run.
- Caravana restarts during a backup: the run is recorded failed at the next start ("Caravana
  restarted during the backup"); the copy left in the allocation is deleted by the next run.
- Caravana restarts during a restore: as for a volume restore, the next start settles it from the
  result file, or fails it once the request has expired.
- A restore after the owner changed `file`: the backup goes to the file the service names now.
- Leftovers of an earlier restore that couldn't put the old files back
  (`.caravana-restore/old` on the database's volume): the next restore refuses, naming the
  directory, as the volume engine does.
- The database is not in WAL mode (rollback journal): the copy's read transaction makes the app's
  writers wait (up to their own busy timeout) while it runs. The docs say so.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: A service's backup policy MAY use the engine `sqlite` with `file`: the database's
  absolute path as the service's container sees it. It MUST lie inside one of the service's
  writable volumes (not the mount itself), be a clean path of letters, digits, `.`, `_`, `-`, `@`,
  `+` and `/` (at most 1024 bytes), not end in `-wal`, `-shm` or `-journal`, and not be under the
  volume's `.caravana-restore`. One file per service; more files are a later feature.
- **FR-002**: A backup MUST be a consistent copy made with `VACUUM INTO` while the service runs,
  checked with `PRAGMA quick_check`, never a copy of the file's bytes.
- **FR-003**: Backups MUST use the installation's destination, the policy's `schedule` (UTC cron,
  run by Caravana's own scheduler, as for the volume engine) and `keep`, "Back up now", the records,
  the needs-you items and the notifications of the other engines.
- **FR-004**: A restore MUST download and check the backup before the service stops, then swap the
  file while the service is stopped (through the `caravana-volumes` task, as a volume restore does),
  keeping the old file's owner and mode, removing the old `-wal`, `-shm` and `-journal`, and putting
  everything back when a step fails.
- **FR-005**: The destination's credentials MUST NOT be written anywhere new: they reach the upload
  only on the standard input of the run's command, never in a job, a variable, a file, a log, an
  argument or an answer.
- **FR-006**: The API (`BackupPolicy.file`, the `sqlite` engine in `BackupPolicy`, `Backup` and
  `Restore`) and `api/openapi.yaml` change in the same commit; the version becomes 0.32.0.
- **FR-007**: The console offers the engine and the file in the Compose form and on the Backups tab,
  and names the engine in the runs list. Forms work without JavaScript.

### Key Entities

- **Backup policy**: gains `file` (sqlite only).
- **Backup and restore records**: gain the engine value `sqlite` (migration 0022 widens the CHECK).

## Success Criteria *(mandatory)*

- **SC-001**: In the lab, a backup of a database written to several times a second succeeds with
  the service's task never restarted, and its object passes `PRAGMA quick_check`.
- **SC-002**: A restore puts the database back as it was at the backup, with the service stopped
  for the swap only (seconds, not the download).
- **SC-003**: No page, answer, record, log line, job or variable holds the destination's secret key
  because of this feature (a test searches for a sentinel).
