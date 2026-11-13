# Feature Specification: Restore a pinned backup on a schedule

**Feature Branch**: `alta/simple-ithaca-nr74` (worktree branch, from CodeAlta task `sgnc`)
**Created**: 2026-11-13
**Status**: Implementing
**Input**: CodeAlta task "03 · Restore a pinned backup on a schedule (e.g. reset a demo nightly)",
requested by the maintainer on 2026-11-11 for the public Umbral demo (Umbral's work item fcp3).

## Why

Caravana backs up on a schedule but restores only by hand (the console's Restore, or
`POST /v1/apps/{id}/backups/{backupId}/restore` with a full API token). A public demo that is reset every
night to a configured state needs today a timer on the host holding a full API token, and a golden backup
kept out of the retention window by hand. This feature makes both part of Caravana: a backup can be pinned
under a label, which retention never deletes, and a service can declare a restore schedule that names a label.

## User Scenarios & Testing

### User Story 1 - Pin a backup under a label (P1)

The owner configures the demo once, backs it up, and pins that backup as `golden` on the Backups tab (or with
`PUT /v1/apps/{id}/backups/{backupId}/pin`). Retention never deletes it, neither its record nor its object in
the destination, and `keep` counts only the unpinned backups.

**Acceptance**:
1. Given a service with `keep: 2` and a backup pinned as `golden`, when five more backups run, then the pinned
   backup's record is not expired and its object is still in the destination, and exactly two unpinned
   backups are kept. This holds for each engine (`postgres`, `volume`, `sqlite`).
2. Given `golden` is on backup A, when the owner pins backup B as `golden`, then B carries the label and A
   carries none (a label is unique per service). A backup carries at most one label.
3. Given a pinned backup, when the owner unpins it, then it is an ordinary backup again and the next backup's
   retention may delete it.
4. A failed or expired backup can't be pinned (404 with the reason). A pin while a backup or restore of the
   service is running is refused (409 `backup_running`), and so is a pin while the app's deploy lock is
   held (409 `app_busy`), so retention never runs with a pin list older than the store's.

### User Story 2 - Restore a label on a schedule (P1)

The owner declares, with the app, `backup: {engine, schedule?, keep?, restore: {schedule: "0 4 * * *", from:
"golden"}}`. At each slot Caravana restores the backup pinned as `golden` into the service, exactly as the
manual restore of that engine does, and records it as a restore with trigger `schedule`.

**Acceptance**:
1. Given a volume service with a file added after the pin, when the slot passes, then the file is gone and the
   golden configuration is back; the restore record has trigger `schedule`, the backup restored, and how long
   it took.
2. The same for a `sqlite` service (stage while running, swap on restart) and a `postgres` service
   (`pg_restore --clean`).
3. Given Caravana was down across one or more slots and comes back within an hour of the latest, the latest
   slot runs once, not once per slot. Later than an hour, nothing runs until the next slot (as scheduled
   volume backups).
4. Given a backup or restore of the service is going at the slot, the slot is recorded as a `skipped`
   restore with the reason, and not retried.
5. Given the app is stopped, its scheduled restores wait: nothing runs and nothing is recorded.

### User Story 3 - A refused or failed run reaches the owner (P1)

**Acceptance**:
1. Given `restore.from: golden` and no backup of the service pinned as `golden`, the deploy is accepted with a
   warning on `services[i].backup.restore.from`, and each due slot is recorded as a failed restore whose message
   says "no backup of <service> is pinned as golden"; the needs-you list shows `restore_failed` and the
   owner's channels get `problem.opened`.
2. Given a restore that fails partway, the service runs on its previous data (the engines already put
   everything back), the restore is recorded failed, and the same item and notification follow.
3. A later successful restore of the service resolves the item.

### User Story 4 - Declare it from every front door (P2)

The restore schedule is a field of the app's backup policy, so it travels with a definition (`deployApp`),
the Compose overlay (`deployCompose`), the console (the Compose form's backup fields, and a per-service form on
the Backups tab) and the Aspire package (`WithCaravanaScheduledRestore(caravana, from: "golden", schedule)`).

**Acceptance**:
1. `restore.schedule` must be a five-field cron in UTC and `restore.from` a label; anything else is a field
   error naming `services[i].backup.restore.schedule` or `.from` (and `services.<svc>.backup.restore...` in a
   Compose overlay).
2. The console's Compose form shows and keeps a service's restore schedule: a deploy from the form never drops it.
3. No token is needed: the clock runs inside Caravana.

### Edge Cases

- The pinned backup was made by another engine than the service's current one: the run is refused and recorded
  ("the backup pinned as golden was made by the volume engine; the service now backs up with sqlite").
- The pinned backup was deleted from the destination outside Caravana: the engine's restore fails as a manual
  one does, and the record says so.
- The service has no running copy (volume, sqlite) or no healthy deployment (postgres) at the slot: recorded failed.
- A copy of the app (`CopyApp`) keeps the policy but has no pins: its runs are refused and recorded, as above.
- Unpinning a label a schedule names is allowed; the next run is a recorded refusal.
- Deleting the app deletes its pins with its backup rows.

## Requirements

- **FR-001** A succeeded, unexpired backup can be pinned under a label `^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`.
  A label is unique per (app, service); pinning moves it. A backup has at most one label. Unpin removes it.
- **FR-002** Retention of every engine never deletes a pinned backup's object and counts only unpinned backups
  against `keep`. The pins reach each engine's retention before it runs (Postgres: the backup job's variable;
  volume: the run's request; sqlite: the upload command's arguments). A pin list that isn't digits and spaces
  deletes nothing.
- **FR-003** `Backup.restore` is `{schedule, from}`, both required, validated as above.
- **FR-004** Caravana's clock (the volume-backup scheduler's pass) runs due restore slots with `dueSlot`, from the
  newest scheduled restore of the service (any outcome), else from when the app was last saved.
- **FR-005** A due slot always leaves one restore record: running (then succeeded or failed), failed (refused:
  no pin, expired pin, engine mismatch, no copy, no destination, Nomad's error) or skipped (busy).
- **FR-006** A scheduled run is the manual restore of the engine, with trigger `schedule` and the label recorded.
- **FR-007** `restore_failed` considers the newest restore of a service that isn't skipped; it is notified as today.
- **FR-008** A deploy whose policy names a label not pinned on the service warns on `services[i].backup.restore.from`.
- **FR-009** API: `pinBackup`, `unpinBackup`, `Backup.pin`/`pinnedAt`, `Restore.trigger`/`pin`, restore status
  `skipped`, `BackupPolicy.restore`; deploy tokens denied by the default deny.
- **FR-010** Console: pin and unpin on a run, pins shown on the service row, the restore schedule shown and set
  on the row, the Compose form's restore fields, restores show their trigger.
- **FR-011** No secret, token or backup content in any record, log, message or page (as today).

## Success Criteria

- **SC-001** The task's "done when" holds in the lab: `volume-backup-restore` and `sqlite-backup-restore` each
  pin a backup, run a scheduled restore at a slot a minute or two ahead, and see the file added after the pin
  gone and the record's trigger `schedule`; the sqlite step also shows `keep: 2` and five more backups leave the
  pinned one (row and object).
- **SC-002** Unit tests cover each engine's retention with pins (the scripts run with a fake rclone), the clock's
  outcomes (due, skipped, refused, stopped, catch-up), and the needs-you rule with skipped restores.

## Not in this feature

Restoring into another app or environment; restoring on an event (deploy, webhook); several labels on one backup.
