# Data Model: Database backups

## app.Backup (new) on app.Service.Backup

| Field | JSON | Rules |
| --- | --- | --- |
| `Engine` | `engine` | `postgres` |
| `Schedule` | `schedule`, omitted when empty | five-field cron, UTC; empty means `0 3 * * *` |
| `Keep` | `keep`, omitted when 0 | 0 means 7; otherwise 1 to 100 |

The service must have ports and `POSTGRES_PASSWORD` in its env or secrets
(`services[i].backup`). Compose: `services.<name>.backup` in the overlay.

## Destination (store `backup_destination`, one row; secret in Nomad Variables)

`endpoint` (URL), `region`, `bucket`, `prefix` (optional), `accessKeyId`, `secretAccessKey`
(write-only, `caravana/backup-destination`), `updatedAt`.

## Backup record (store `backups`)

| Column | Meaning |
| --- | --- |
| `app_id`, `id` | key; `id` = `<service>-<unix seconds>` |
| `service`, `job_id` | the run's Nomad job |
| `status` | `running`, `succeeded`, `failed` |
| `object`, `bytes` | from upload's output, on success |
| `message` | the failure's last line |
| `started_at`, `finished_at`, `expired` | expired: deleted by retention |

## Restore record (store `restores`)

`id` (random), `app_id`, `service`, `backup_id`, `status` (`running`, `succeeded`,
`failed`), `message`, `created_at`, `finished_at`.

Rows of an app go with the app.
