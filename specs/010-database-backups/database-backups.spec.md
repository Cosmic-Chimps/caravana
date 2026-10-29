# Feature Specification: Database backups

**Feature Branch**: `010-databases-backups`

**Created**: 2026-10-07

**Status**: Implemented, v0.1.0

**Input**: User description: "010 Databases + backups (docs/plan.md): Postgres/MySQL/Redis as
regular apps pinned to a node with a host volume; backups as Nomad periodic batch jobs to
S3-compatible storage. Phase 2 gate: a real multi-service compose app runs, with a database on a
host volume and a backup that restores. Each feature ships its console part."

## Context

After 009, a database already runs as a regular app: a Compose file with `postgres:17` and a
named volume deploys, the volume keeps its data, and the other services reach it by name. What
is missing is the part that makes it safe to keep real data there: backups, outside the node,
on a schedule, and a restore that works.

Feature 010 adds that, for PostgreSQL first. A service says it is a Postgres database and how
often to back it up. Caravana runs a dump on that schedule, stores it in an S3-compatible bucket
the admin configured once, keeps the last N, lists them, and restores any of them into the
running database on request.

Who uses this:

- **The admin**, in the console: sets the bucket once in Settings, sees each database's backups
  on its app page, takes one now, restores one.
- **API clients**: the same through `/v1`, so a script can take a backup before a migration.

Words used here:

- **Destination**: the S3-compatible bucket backups go to (AWS S3, Cloudflare R2, Backblaze B2,
  a self-hosted one), with its endpoint and credentials. One per installation.
- **Backup**: one dump of one database service at one time, stored as one object.
- **Restore**: loading a backup into the running database, replacing what it holds.

## Clarifications

### Session 2026-10-07

Answered with the recommended option (unattended run, the maintainer's standing preference).

- Q: Which engines in 010? → A: PostgreSQL only. It is the gate's database. MySQL/MariaDB has the
  same dump-and-load shape and comes next; Redis keeps its data as files and needs a different
  restore (stop, replace, start).
- Q: Where is the list of backups kept: read from the bucket, or recorded by Caravana? → A:
  Recorded by Caravana from Nomad's record of each backup run, so the control plane never needs
  its own S3 client or network path to the bucket; the jobs that run next to the database are
  the only ones talking to it.
- Q: Does deleting an app delete its backups? → A: No. Backups exist to survive mistakes,
  deleting the app included. The delete confirmation says they stay.
- Q: One destination per installation or per app? → A: Per installation for 010.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Back up a database on a schedule (Priority: P1)

The admin sets the destination once. An app's `db` service (`postgres:17`, with a volume) is
marked as a Postgres database with a daily schedule and "keep 7". Every day a dump of its
database is stored in the bucket under the app's and the service's names. The app page lists
the backups with their time and size; after the eighth, the oldest is gone. "Back up now" takes
one at once.

**Why this priority**: data without a backup off the node is one disk failure from gone.

**Independent Test**: configure a destination, deploy an app with a backed-up Postgres, take a
backup now, see it listed and in the bucket; take more than "keep" of them and see the oldest
removed.

**Acceptance Scenarios**:

1. **Given** a destination and an app whose `db` service is a Postgres database with backups,
   **When** it deploys, **Then** a schedule for its backups exists and the app page shows it.
2. **Given** that app, **When** the admin clicks "Back up now", **Then** within a minute a new
   backup is listed with its time and size, and the object is in the bucket.
3. **Given** keep is 3 and 3 backups exist, **When** a fourth succeeds, **Then** the oldest is
   deleted from the bucket and no longer listed.
4. **Given** a backup run fails (wrong password, bucket unreachable), **When** the admin opens
   the app, **Then** the failure shows with what the dump or the upload said.
5. **Given** no destination is set, **When** an app asks for backups, **Then** the deploy fails
   naming the backup field and saying to set a destination first.

---

### User Story 2 - Restore a backup (Priority: P1)

The admin picks a backup on the app page and restores it, after typing the service's name to
confirm. The running database's contents are replaced by the backup's. The restore shows as
running, then done or failed with the tool's message.

**Why this priority**: a backup that was never restored is a guess; the Phase 2 gate is "a
backup that restores".

**Independent Test**: write a row, back up, change the row, restore, read the original row back.

**Acceptance Scenarios**:

1. **Given** a backup taken while a table held row A, and the table now holds row B, **When**
   that backup is restored, **Then** the table holds row A again.
2. **Given** a restore is running for a service, **When** another restore is asked for the same
   service, **Then** it is refused until the first ends.
3. **Given** a restore fails (the object is gone, the database refused), **When** the admin looks,
   **Then** the restore shows failed with the message, and the database is as the tool left it.
4. **Given** the restore form, **When** the typed name doesn't match, **Then** nothing happens.

---

### User Story 3 - Set the destination (Priority: P1)

In Settings, the admin enters an S3 endpoint, region, bucket, an optional prefix, an access key
ID and a secret access key. The secret is write-only like every secret. Changing the
destination applies to every backed-up database from its next backup on.

**Why this priority**: nothing above works without it.

**Independent Test**: set a destination, read it back without the secret, change it, check the
next backup goes to the new bucket.

**Acceptance Scenarios**:

1. **Given** no destination, **When** the admin saves one, **Then** reading it back shows every
   field but the secret key.
2. **Given** a destination, **When** the admin saves it again without the secret key, **Then**
   the secret key stays as it was.
3. **Given** a destination and backed-up databases, **When** it changes, **Then** their next
   backups go to the new destination without redeploying the apps.

### Edge Cases

- **A database that isn't Postgres** (MySQL, MariaDB, Redis): refused with a message naming the
  engine field; only `postgres` is supported in 010.
- **A backed-up service without ports**: refused, because the backup reaches it over its port.
- **No password for the dump**: the service's `POSTGRES_PASSWORD` (environment or secret) is
  what the dump uses; without one, the deploy is refused naming the backup field.
- **An app deleted**: its backup schedule and restore jobs go with it; its backups stay in the
  bucket (they are the point of a backup), and the delete confirmation says so.
- **Backups removed from a service**: its schedule goes; its backups stay in the bucket and are
  no longer listed by Caravana.
- **A backup taken while Caravana was down**: it is listed once Caravana is back, if Nomad still
  remembers the run.
- **The schedule's time zone**: UTC.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The admin MUST be able to set, read (without the secret key) and change one backup
  destination: endpoint, region, bucket, optional prefix, access key ID, secret access key.
- **FR-002**: A service MUST be able to declare it is a Postgres database with a backup
  schedule (cron, UTC) and how many backups to keep, in the app model and in the Compose overlay.
- **FR-003**: For each such service, Caravana MUST run a dump on its schedule and store it in the
  destination under the app and service names, one object per backup.
- **FR-004**: After a successful backup, backups beyond the number to keep MUST be deleted from
  the destination, oldest first.
- **FR-005**: Caravana MUST list each service's backups with their time, size and status, and
  failed runs with the tool's message.
- **FR-006**: The admin MUST be able to take a backup now.
- **FR-007**: The admin MUST be able to restore any listed backup into the running database,
  after typing the service's name in the console; one restore per service at a time.
- **FR-008**: A restore MUST report running, then succeeded or failed with the tool's message.
- **FR-009**: Destination credentials and database passwords MUST NOT appear in any API
  response, page, log line or Nomad job.
- **FR-010**: A deploy asking for backups MUST be refused, naming the field, when no destination
  is set, the engine isn't `postgres`, the service has no ports, or there is no password.
- **FR-011**: Deleting an app MUST remove its backup schedules and restore jobs and keep its
  backups in the destination; the delete confirmation MUST say so.
- **FR-012**: Changing the destination MUST apply to every schedule from its next run, without a
  deploy.
- **FR-013**: The console MUST show the destination in Settings and, on the app page of a
  backed-up service, its backups with "Back up now" and "Restore".
- **FR-014**: Caravana's scoped Nomad policy MUST allow what backups need, and the gate MUST have
  a step that writes data, backs it up, changes it, restores, and reads the original back.
- **FR-015**: Every new endpoint and field MUST be in `api/openapi.yaml` in the same change.

### Key Entities

- **Backup destination**: endpoint, region, bucket, prefix, access key ID (shown), secret key
  (write-only). One per installation.
- **Backup policy**: on a service: engine (`postgres`), schedule, keep.
- **Backup**: app, service, time, size, status (succeeded, failed), message; its object in the
  destination.
- **Restore**: app, service, the backup it loads, status (running, succeeded, failed), message.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A row written before a backup is read back after a restore, in 100% of gate runs.
- **SC-002**: "Back up now" lists the new backup within 60 seconds for a small database.
- **SC-003**: With keep N, the destination never holds more than N backups of a service once a
  backup run ends.
- **SC-004**: A marker password and secret key are found in zero responses, pages, log lines and
  Nomad jobs across set-destination, backup and restore.
- **SC-005**: The gate passes in the lab and on a real host with the scoped policy.

## Assumptions

- **PostgreSQL only in 010.** It is the gate's database and the most common one. MySQL/MariaDB
  (dump-based, same shape) and Redis (file-based, needs a stop) are a follow-up, and the engine
  field leaves room for them.
- **Logical dumps** (`pg_dump` custom format of the database in `POSTGRES_DB`, default the user),
  restored with `pg_restore --clean --if-exists`. They are portable across nodes and minor
  versions; the dump uses the service's own image, so the tools match the server.
- **Credentials from the service**: `POSTGRES_USER` (default `postgres`), `POSTGRES_PASSWORD`,
  `POSTGRES_DB`, from its environment or its secrets, the variables the official image reads.
- **One destination per installation**, not per app. Per-app destinations can come later.
- **Restore replaces the running database's contents** while it runs; clients connected at the
  time may see errors. There is no "restore into a new database" in 010.
- **Backups outlive the app** by design; deleting them is the bucket owner's choice.
- **"Pinned to a node with a host volume"** is already true since 009: a volume lives on one
  node and Nomad places the service there.

## Verbatim Constraints

- Contract file: `api/openapi.yaml`
- Engine value: `postgres`
