# Implementation Plan: Database backups

**Branch**: `010-databases-backups` | **Date**: 2026-10-07 | **Spec**: [database-backups.spec.md](./database-backups.spec.md)

**Scale note**: about 28 files in seven packages plus the policy and the gate. Three new Nomad
objects per backed-up service (a periodic job, its runs, a restore job) and two new store
tables. The job definitions are pure translator functions with golden files, as the app's job is.

## Summary

A service gains `backup: {engine: "postgres", schedule, keep}`. Every deploy of such an app
registers a Nomad periodic batch job, `<app>_<service>_backup`. Its prestart task runs `pg_dump`
with the service's own pinned image, against the service's registration. Its main task uploads
the dump with rclone (pinned by digest) to the destination and deletes the dumps beyond `keep`.
Credentials reach the job only through its own Nomad Variable, `nomad/jobs/<job>`, which
Caravana writes: the database's `POSTGRES_*` values from its env or secrets, and the
destination's `RCLONE_CONFIG_DEST_*`.

A watcher in the service layer records each run in SQLite (status, size, object key, message
from the task's output). Restore registers a one-off batch job, `<app>_<service>_restore`
(rclone download, then `pg_restore --clean --if-exists`), and records its outcome the same way.
The destination is one per installation: its plain fields live in SQLite, its secret key in the
Nomad Variable `caravana/backup-destination`. The console gets a Backups tab and a Settings
section.

## Project Structure

```text
internal/app/app.go, validate.go         # Service.Backup{Engine, Schedule, Keep}; rules
internal/compose/compose.go, service.go  # Overlay.Backup → Service.Backup
internal/nomadjob/backup.go (+ tests, testdata/)   # BackupJob, RestoreJob, job IDs; Settings.BackupImage
internal/config/config.go                # CARAVANA_BACKUP_IMAGE
internal/store/migrations/0005_backups.sql, backups.go (+ tests)
internal/deploy/backups.go (+ tests), deploytest/nomad.go   # sync jobs+variables, force, runs, restore, delete
internal/service/backups.go (+ tests), deploy.go, apps.go, service.go
internal/api/backups.go (+ tests), api.go, errors.go
internal/ui/backups.go, settings.go, templates/app.html, templates/settings.html, ui.go (+ tests)
api/openapi.yaml (0.8.0), deploy/vm/caravana-policy.hcl, cmd/caravana/gate_test.go
```

**Structure Decision**: job shapes live in `nomadjob` (pure, golden), Nomad calls in `deploy`,
records and orchestration in `service`. No S3 client in the control plane: only the jobs next
to the database talk to the bucket.

## Constitution Check

| Principle | Assessment |
| --- | --- |
| I. Small by Default | PASS. No new Go module, no new runtime service. The bucket is the user's, and optional. rclone runs only inside backup jobs, pinned by digest. |
| II. One Model, Many Inputs | PASS for the model: the policy is a field of `app.Service`, set by JSON and by the Compose overlay alike; job shapes come from pure, golden-tested functions. **Exception**: "one App is one Nomad job". See Complexity Tracking. |
| III. Safe, Reversible Deploys | PASS. The dump image is the service's own pinned digest; rclone is pinned. Backup jobs don't touch the app's rollout. Restore is a deliberate, confirmed action. |
| IV. Fail Loudly | PASS. Backups with no destination, a wrong engine, no ports or no password are refused naming `services[i].backup`. A failed run shows the tool's message. Restarts are 0, so a failure shows at once instead of retrying out of sight. |
| V. API-First | PASS. Destination, list, back up now and restore are API operations in `api/openapi.yaml`; the console calls the same service. |
| VI. Nomad-Native | PASS. Periodic batch jobs, Nomad Variables, native service discovery, all through the official client. An exported app can carry its backup job as a plain jobspec. |
| VII. Secure by Default | PASS. The secret key lives in a Nomad Variable; the job's credentials live in its own variable, readable by that job alone; nothing shows a value; task output is read for messages but never logged by Caravana. |
| VIII. Plain, Tested Go | PASS. Golden jobs, a fake Nomad that runs periodic children, store tests, a gate step that restores. |

## Complexity Tracking

| Violation | Why needed | Simpler alternative rejected because |
| --- | --- | --- |
| Backed-up services add jobs beside the app's own (`<app>_<service>_backup`, `<app>_<service>_restore`) | Nomad schedules periodic work only as a periodic batch job, a job of its own type; the app's job is a `service` job and can't hold it. The plan names backups as periodic batch jobs. | Running dumps from the control plane needs network access from Caravana to every database and the bucket, and an S3 client in Caravana. Cron inside the database container needs tools the image doesn't have. |
