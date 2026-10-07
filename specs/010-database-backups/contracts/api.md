# Contract: API (`api/openapi.yaml` 0.7.0 → 0.8.0)

| Operation | Route | Body → Answer |
| --- | --- | --- |
| `getBackupDestination` | `GET /v1/backups/destination` | → 200 `BackupDestination` (no secret), or 404 when none |
| `setBackupDestination` | `PUT /v1/backups/destination` | `BackupDestinationRequest` → 200 `BackupDestination` |
| `listBackups` | `GET /v1/apps/{id}/backups` | → 200 `{backups: [Backup], restores: [Restore]}`, newest first |
| `createBackup` | `POST /v1/apps/{id}/backups` | `{service}` → 202 `Backup` (running) |
| `restoreBackup` | `POST /v1/apps/{id}/backups/{backupId}/restore` | → 202 `Restore` (running) |

`Service.backup`: `{engine: "postgres", schedule?, keep?}`. `ComposeOverlay.backup`: the same.

Errors: `invalid_field` on `services[i].backup` (or `services.<name>.backup` from Compose);
409 `backup_running` when a backup or restore of that service is running; 409
`no_backup_destination` from `createBackup`/`restoreBackup` when none is set; `not_found` for an
unknown backup or a service without backups.
