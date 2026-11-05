# API changes (0.31.0 → 0.32.0)

No new operation and no new error code.

## `BackupPolicy` (in `App.services[].backup` and the Compose overlay)

- `engine`: enum `[postgres, volume, sqlite]`.
- `file` (string, new): the SQLite database's absolute path as the service sees it. Required with
  `sqlite`, refused with the other engines. Rules (400 `invalid_field`, field
  `services[i].backup.file`):
  - inside one of the service's volumes, not the mount itself, and that volume is not `readOnly`;
  - a clean absolute path of `A-Z a-z 0-9 . _ - @ + /`, at most 1024 bytes;
  - not ending in `-wal`, `-shm` or `-journal`, not under the volume's `.caravana-restore`.
- With `sqlite`, the schedule must be a cron expression Caravana can run (as for `volume`), and a
  service with no volume is refused on `services[i].backup.engine`.
- Service names `caravana-sqlite` and `caravana-upload` are reserved, like `caravana-volumes`.

Description to add: "`sqlite`: a consistent copy of the database named by `file`, made with
`VACUUM INTO` while the service keeps running, checked, gzipped and uploaded as `<ts>.sqlite.gz`. A
restore downloads and checks the backup first, then stops the service for the few seconds it takes
to put the file in place. In WAL mode the copy never blocks the app's writers; with a rollback
journal they wait while it reads."

## `Backup` and `Restore`

- `engine`: enum `[postgres, volume, sqlite]`.
- `Backup.volumes` stays volume-only; `Backup.stoppedSeconds` is absent for sqlite backups (the
  service doesn't stop); `Restore.stoppedSeconds` is set for sqlite restores.

## Behaviour of the existing operations for a sqlite service

| Operation | As for |
| --- | --- |
| `backUpNow` | the volume engine: 202 with a running backup; 409 `no_running_copy`, 409 backup running, 409 `app_stopped` |
| `restoreBackup` | the volume engine: a running restore; the same 409s |
| `listBackups` | unchanged; sqlite runs have `engine: sqlite` |
