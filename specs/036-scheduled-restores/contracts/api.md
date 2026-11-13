# API contract: scheduled restores (API 0.39.0)

## `PUT /v1/apps/{id}/backups/{backupId}/pin` — `pinBackup`

Body (`application/json`, with or without parameters): `{"label": "golden"}`.

- **200** the `Backup`, with `pin` and `pinnedAt`. A backup that held the label before loses it (one label per
  service); a backup that had another label gets this one instead.
- **400** `invalid_field`, field `label`: not 1–32 of lowercase letters, digits and `-`, starting and ending with
  a letter or digit.
- **404** `not_found`: unknown app or backup, or the backup can't be pinned (it didn't succeed, or retention
  deleted it); the message says which.
- **409** `backup_running` (a backup or restore of the service is running), `app_busy` (a deploy or a volume run
  holds the app).
- **502** Nomad couldn't take the Postgres backup job's pin list; nothing changed.

## `DELETE /v1/apps/{id}/backups/{backupId}/pin` — `unpinBackup`

- **200** the `Backup` without `pin`. Unpinning a backup that has no label is 200 too.
- **404**, **409**, **502** as above.

Both: `Cache-Control` as other reads; deploy tokens get 403 `token_scope` (default deny; the scope walk lists
both routes as denied).

## Schemas

`Backup` gains:
```yaml
pin:
  type: string
  description: The label the backup is pinned under (pinBackup). Retention never deletes a pinned backup and
    counts only unpinned ones against `keep`. Absent when not pinned.
pinnedAt: {type: string, format: date-time}
```

`Restore`:
- `status` enum adds `skipped`: a scheduled restore whose slot came while a backup or restore of the service
  was running. It is final and never on the needs-you list.
- `trigger` (required): `owner` (`restoreBackup`, the console's Restore; every restore recorded before 0.39.0)
  or `schedule` (the service's `backup.restore`).
- `pin`: for `trigger: schedule`, the label it restored from.
- `backupId`: empty for a scheduled restore refused before it found a backup (no backup pinned under the
  label); `message` says why.

`BackupPolicy` gains:
```yaml
restore:
  type: object
  additionalProperties: false
  required: [schedule, from]
  description: Restores the backup pinned under `from` on `schedule`, inside Caravana (no token). Each slot is
    the same restore as `restoreBackup` and is recorded with `trigger: schedule`; a slot while a backup or
    restore runs is recorded `skipped`; a slot with no backup pinned under `from` is a failed restore, on the
    needs-you list as `restore_failed`. A deploy whose `from` isn't pinned yet warns on
    `services[i].backup.restore.from`. A slot missed while Caravana was down runs once if it is under an hour
    old. A stopped app's restores wait.
  properties:
    schedule: {type: string, description: Five-field cron, UTC., examples: ["0 4 * * *"]}
    from: {type: string, pattern: "^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$", examples: [golden]}
```

The `restoreBackup` description gains: a restore started here is recorded with `trigger: owner`.
The `listBackups` description: pinned backups are listed like any other.
