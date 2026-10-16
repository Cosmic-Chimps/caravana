# Contract: API (`api/openapi.yaml` 0.12.0 → 0.13.0)

Additions only. No request that works today changes meaning.

## Volumes

`GET /v1/apps/{id}/volumes` (`listVolumes`) → 200 `[]AppVolume` (schema `AppVolume`: `Volume` already names the definition's volume)

```json
[{
  "name": "uptime-kuma-db-data",
  "mounted": true,
  "mounts": [{"service": "uptime-kuma", "path": "/app/data", "readOnly": false}],
  "hostPath": "/opt/nomad/data/host_volumes/3f1c…",
  "size": {"bytes": 327155712, "measuredAt": "2026-10-16T03:00:41Z"},
  "backedUp": true,
  "lastBackup": {"id": "uptime-kuma-1792119600", "status": "succeeded", "startedAt": "…"}
}]
```

- `mounted` is false, and `mounts` empty, for a volume Nomad has for the app that no service mounts
  any more.
- `size` is absent when the volume has never been measured.
- `hostPath` is absent when Nomad couldn't be read; the rest still answers (spec US3-3).
- Errors: 404 `not_found`.

## Backups

- `BackupPolicy.engine` (the app model and the Compose overlay): `postgres` | `volume`.
- `Backup` gains:
  - `engine` (`postgres` | `volume`);
  - `stoppedSeconds` (number, volume runs only);
  - `volumes` (`[{name, bytes, sizeBytes}]`, volume runs only).
- `Restore` gains `engine` and `stoppedSeconds`.
- `backUpNow` (`POST /v1/apps/{id}/backups`) and `restoreBackup` work for `volume` backups
  unchanged: 202 with the run, recorded `running`.
- A deploy whose service has a `volume` backup is refused with 422 `invalid_field` and field
  `services[i].backup.engine` when:
  - the service mounts no volume;
  - it shares a volume with another service of the app;
  - it is named `caravana-volumes`.

New error:

| Status | Code | When |
| --- | --- | --- |
| 409 | `no_running_copy` | back up now, or restore, for a `volume` backup when the service has no running copy |

The existing `backup_running` (409) also covers "a restore of this service is running" and the
reverse.

## Parity

| Console route | Operations |
| --- | --- |
| `GET /apps/{id}/backups` | `listBackups`, `listVolumes` |
