# Feature Specification: Volumes panel and volume backups

**Feature Branch**: `016-volume-backups`

**Created**: 2026-10-15

**Status**: Implemented, v0.4.0

**Input**: "Feature 016: volumes panel + volume backups. A volumes panel per app (each named
volume: name, the service and path it is mounted at, where it lives on the host, its size, its
last backup), and a new backup engine `volume`: on schedule or 'back up now', stop the service's
copy, archive the volume, start the copy again (seconds of downtime at the scheduled time, said
up front); restore the same way (stop, replace the volume's contents from an archive, start). It
uses feature 010's backup destination (S3-compatible), retention (keep) and schedule, the Backups
tab, the API's back up now / list / restore, and the needs-you list for failed runs. Main test
case: Uptime Kuma 2 with its embedded MariaDB, data in /app/data on the named volume
uptime-kuma-db-data; a live copy of a running database is unsafe, which is why stop-and-archive
was chosen over copying a running volume. Postgres keeps its pg_dump engine. Open question for
the plan, not the spec: volumes are single-node single-writer host volumes; whether a backup job
can mount the volume while the app's copy is stopped, and how the copy is stopped and started
again (scale to zero, or a stop of the allocation). Out of scope: backups of volumes shared by
several services at once, incremental or deduplicated backups, cross-node moves of volumes
(Phase 3), MySQL/MariaDB logical dumps (a later engine)."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Back up an app's volume on a schedule (Priority: P1)

The owner runs Uptime Kuma 2 with its embedded MariaDB. Everything it knows (monitors, history,
settings, the database files) lives in `/app/data` on the volume `uptime-kuma-db-data`. They mark
the service's backup as `volume` with the default daily schedule and "keep 7". Every night the
service stops for a few seconds, its volume is archived to the backup destination set in feature
010, and it starts again. The Backups tab lists each backup with its time, size and how long the
service was stopped. "Back up now" takes one at once. Before they turn it on, the page says the
service stops while a backup runs.

**Why this priority**: today the data of any app that isn't Postgres has no copy off the node.
One disk failure loses it. Uptime Kuma on the maintainer's host is in that state now.

**Independent Test**: with a destination set, deploy Uptime Kuma 2 with a `volume` backup on its
service. Add a monitor. Take a backup now: it is listed with time, size and stop duration, and
the object is in the bucket. The service is healthy again afterwards and the monitor is there.
Take more than "keep" backups: the oldest are removed.

**Acceptance Scenarios**:

1. **Given** a destination and an app whose service mounts a volume, **When** the owner sets the
   service's backup to `volume` and deploys, **Then** a schedule for its backups exists and the
   Backups tab shows it, with the sentence "The service stops while a backup runs."
2. **Given** that app, **When** the owner presses "Back up now", **Then** within a few minutes a
   new backup is listed with its time, size and stop duration, the archive is in the bucket, and
   the service is running and healthy again.
3. **Given** "keep 3" and 3 backups, **When** a fourth succeeds, **Then** the oldest is deleted
   from the bucket and no longer listed.
4. **Given** a backup run fails (the bucket is unreachable, the archive can't be written),
   **When** the owner opens the app, **Then** the run shows failed with the reason, the service
   was started again, and the app is on the needs-you list.
5. **Given** an app with several services, **When** one service's volume is backed up, **Then**
   only that service stops; the others keep running.
6. **Given** a service whose backup is `volume` but that mounts no volume, **When** it deploys,
   **Then** the deploy is refused, naming the backup field and saying the service has no volume.

---

### User Story 2 - Restore a volume backup (Priority: P1)

Something went wrong with Uptime Kuma (a bad upgrade, deleted monitors). The owner picks last
night's backup on the Backups tab and restores it after typing the service's name. The service
stops, its volume's contents are replaced by the archive's, and it starts again with the data as
it was at that backup.

**Why this priority**: a backup that was never restored is a guess. Restore is what the backup is
for.

**Independent Test**: add monitor A, back up, delete A and add B, restore. The service starts
with A and without B.

**Acceptance Scenarios**:

1. **Given** a backup taken while the app had monitor A, and the app now has B instead, **When**
   that backup is restored, **Then** the service runs again with A and without B. Files that
   weren't in the archive are gone.
2. **Given** a restore is running for a service, **When** another restore or a backup of the same
   service is asked for, **Then** it is refused until the first ends.
3. **Given** a restore fails (the archive is gone or damaged), **When** the owner looks, **Then**
   the restore shows failed with the reason, the service was started again, and the volume is as
   it was before the restore began.
4. **Given** the restore form, **When** the typed name doesn't match the service, **Then**
   nothing happens.

---

### User Story 3 - See an app's volumes (Priority: P2)

On the app's page the owner sees every volume the app has: its name, the service and path it is
mounted at (and whether read-only), where it lives on the host, how big it is, and its last
backup (time and outcome), or that it isn't backed up. A volume no service mounts any more, which
the app keeps until it is deleted, shows as "not mounted".

**Why this priority**: the owner can't protect data they can't see. The panel is also where they
notice a volume that isn't backed up. It is useful on its own, but less urgent than having
backups at all.

**Independent Test**: deploy an app with two volumes, one backed up and one not. The panel lists
both with their mount, host location and size; the backed-up one shows its last backup and the
other shows "not backed up". Unmount one in a redeploy: it shows "not mounted".

**Acceptance Scenarios**:

1. **Given** an app with volumes, **When** the owner opens its volumes panel, **Then** each
   volume shows its name, service, mount path, read-only or not, host location, size, and last
   backup or "not backed up".
2. **Given** a volume no service mounts any more, **When** the owner opens the panel, **Then** it
   is listed as "not mounted" with its size, so the owner knows its data is still on the host.
3. **Given** the node can't be reached to measure a size, **When** the owner opens the panel,
   **Then** the rest of the panel shows and the size says it is unknown.
4. **Given** an API client, **When** it lists an app's volumes, **Then** it gets the same
   facts the panel shows.

---

### Edge Cases

- **A volume mounted by two services**: a `volume` backup on either service is refused, naming the
  backup field, because stopping one service would not stop the other writing to it (shared
  volumes are out of scope).
- **A service with several volumes**: one backup archives all of them; a restore puts all of them
  back.
- **A read-only mount**: it is still part of the service's backup and restore (another service may
  have written it).
- **A deploy arrives while a backup or restore has the service stopped**: the deploy waits until
  the run ends and then goes ahead. The run never leaves the service in a state that is neither the
  deploy's nor running.
- **The service doesn't become healthy after a backup restarts it**: the run is still recorded
  (succeeded if the archive is complete), and the app is on the needs-you list for the unhealthy
  service, as for any copy that fails.
- **A backup takes too long**: after a time limit (30 minutes by default) the run is failed, the
  partial archive is removed, and the service starts again.
- **Postgres services**: `postgres` stays the engine for a live dump with no downtime. A Postgres
  service may choose `volume` instead (a stopped, file-level copy); the page says which one stops
  the service.
- **No destination set**: as in feature 010, the deploy is refused naming the backup field.
- **Deleting an app**: its schedules go, its backups stay in the destination, and the delete
  confirmation says so (as in feature 010).
- **Large volumes**: the stop lasts as long as the archive takes; the Backups tab shows the last
  run's stop duration so the owner can judge the schedule.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: A service MUST be able to declare a backup with engine `volume`, a schedule (cron,
  UTC) and how many backups to keep, in the app model and in the Compose overlay, with feature
  010's defaults (daily at 03:00 UTC, keep 7).
- **FR-002**: A `volume` backup MUST stop the service, archive every volume it mounts to the
  backup destination under the app's and service's names, and start the service again, whether
  the archive succeeded or not.
- **FR-003**: Only the backed-up service MUST stop; the app's other services keep running.
- **FR-004**: After a successful backup, backups beyond the number to keep MUST be deleted from
  the destination, oldest first.
- **FR-005**: Each backup MUST be listed with its time, size, status, how long the service was
  stopped, and the reason when it failed.
- **FR-006**: The owner MUST be able to take a `volume` backup now, from the console and the API.
- **FR-007**: The owner MUST be able to restore any listed `volume` backup after typing the
  service's name in the console. A restore stops the service, replaces the contents of every
  volume in the backup with the archive's (files not in it are removed), and starts the service
  again.
- **FR-008**: A failed restore MUST leave the volumes as they were before it began and start the
  service again.
- **FR-009**: Only one backup or restore per service MUST run at a time; a second is refused
  while the first runs.
- **FR-010**: A deploy MUST be refused, naming the backup field, when engine `volume` is asked
  for and the service mounts no volume, a volume it mounts is mounted by another service, or no
  destination is set.
- **FR-011**: The console MUST say, where a `volume` backup is set and before "Back up now" and
  "Restore", that the service stops while it runs, with the last run's stop duration when there is
  one.
- **FR-012**: Failed backup and restore runs MUST appear on the needs-you list, as feature 014's
  rules already do for Postgres.
- **FR-013**: The app's page MUST show a volumes panel: per volume its name, the service and path
  it is mounted at, read-only or not, its host location, its size, and its last backup (time and
  outcome) or "not backed up"; volumes no service mounts show as "not mounted".
- **FR-014**: The API MUST list an app's volumes with the same facts as the panel, and back up
  now, list and restore `volume` backups as it does Postgres ones.
- **FR-015**: A run that exceeds its time limit MUST be failed, its partial archive removed, and
  the service started again.
- **FR-016**: A deploy arriving while a backup or restore holds a service stopped MUST wait for the
  run to end, then proceed.
- **FR-017**: Destination credentials MUST NOT appear in any page, API response, log line or job
  definition, as in feature 010.
- **FR-018**: Caravana's scoped Nomad policy MUST allow what these runs need, and the gate MUST
  have a step that backs up a volume, changes its data, restores it and finds the original data.
- **FR-019**: Every new endpoint and field MUST be in `api/openapi.yaml` in the same change.

### Key Entities

- **Volume**: belongs to an app; name, the services and paths that mount it (zero, one or more;
  backups need exactly one), read-only per mount, host location, size (measured when read), last
  backup.
- **Backup policy**: on a service; engine (`postgres` or `volume`), schedule, keep. Unchanged in
  shape from feature 010; `volume` is a new value.
- **Backup** (volume engine): app, service, the volumes it holds, time, size, status, stop
  duration, reason when failed; one archive per volume in the destination.
- **Restore** (volume engine): app, service, the backup it loads, status, stop duration, reason
  when failed.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Uptime Kuma 2 with its embedded MariaDB is backed up nightly on the maintainer's
  host, and a restore brings back a monitor deleted after the backup, with the service healthy
  afterwards.
- **SC-002**: For a volume under 100 MB, the service is stopped for under 30 seconds per backup
  and per restore.
- **SC-003**: After every backup or restore, successful or failed, the service is running again
  within one minute of the run ending, in 100 % of runs.
- **SC-004**: An owner can tell from the app's page, in under 10 seconds, which of its volumes are
  backed up and when they were last backed up.
- **SC-005**: A failed backup or restore is on the needs-you list within one minute of failing.
- **SC-006**: Restoring a backup gives back the volume's files exactly as they were at the backup:
  every file present, with the same contents, and no file added since.

## Assumptions

- Volumes are the app-owned host volumes of feature 009, on the single node; each lives on that
  node until the app is deleted. Moving volumes between nodes is Phase 3.
- A service with a volume runs one copy (feature 009), so stopping "the copy" stops the service.
- The backup destination, schedule format, retention and the Backups tab are feature 010's,
  unchanged; volume backups list beside Postgres backups.
- A short, announced stop is acceptable to the target user for a consistent copy; copying a
  running database's files is not safe, so there is no "live" volume engine.
- Archives are whole (not incremental or deduplicated) and compressed.
- How the service is stopped and started, and how the backup run reaches the volume, is decided in
  the plan, which checks it in the lab first.
- MySQL and MariaDB logical dumps, and Redis, are later engines.
