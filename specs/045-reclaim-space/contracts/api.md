# Contract: API 0.50.0 (session or full API token; deploy tokens denied by the default deny)

## Schemas

**ServerSpace**
```json
{
  "server": "one",
  "managed": true,
  "reclaimableBytes": 1500000000,
  "images": {"count": 7, "bytes": 1472000000, "unusedCount": 4, "unusedBytes": 1146000000, "measuredAt": "…"},
  "imagesUnavailable": "",            // "no agent" | "no inventory yet" when images is null
  "dockerVolumes": {"count": 154, "bytes": 91050000},   // null without an agent
  "buildCacheBytes": 0,               // null without an agent
  "orphanVolumes": [
    {"name": "shop_uploads", "app": "shop", "project": "Default", "environment": "production", "appExists": true,
     "bytes": 2300000000, "measuredAt": "…", "lastDeployedAt": "2026-10-12T…", "inUse": false, "id": "<nomad id>"}
  ],
  "lastCleanup": {Cleanup} | null,
  "openCleanup": {Cleanup} | null,
  "settings": {CleanupSettings}
}
```
`bytes` of an orphan is null until measured; `lastDeployedAt` is null when unknown; `app` is "" when the name
doesn't parse. `appExists` false reads "its app was deleted" in the console.

**Cleanup**: `id`, `server`, `trigger` (`owner`|`schedule`), `dockerVolumes`, `status` (`requested`|`running`|
`done`|`failed`|`lost`), `requestedAt`, `startedAt` (null until), `finishedAt` (null until), `imagesRemoved`,
`bytesFreed`, `volumesRemoved`, `error`.

**VolumeDeletion**: `id`, `volume`, `app`, `server`, `backUpFirst`, `status` (`archiving`|`deleting`|`done`|
`failed`), `requestedAt`, `finishedAt`, `archiveObject`, `bytes`, `error`.

**CleanupSettings**: `automatic` (bool), `keepDeployments` (1–10).

## Operations

| Operation | Route | Answer |
| --- | --- | --- |
| `getServerSpace` | `GET /v1/servers/{name}/space` | 200 `ServerSpace`, `no-store`; 404 `server_not_found` |
| `cleanServer` | `POST /v1/servers/{name}/cleanups` body `{"dockerVolumes": false}` (optional) | 202 `Cleanup`, `Location: /v1/servers/{name}/cleanups/{id}`; 404 `server_not_found`; 409 `no_agent` ("server X has no agent reporting its images: run the installer on it"), 409 `cleanup_running`, 409 `keep_list_too_long` |
| `listServerCleanups` | `GET /v1/servers/{name}/cleanups` | 200 `{"cleanups": [Cleanup]}` newest first, 20 |
| `getServerCleanup` | `GET /v1/servers/{name}/cleanups/{id}` | 200 `Cleanup`; 404 `cleanup_not_found` |
| `deleteOrphanVolume` | `POST /v1/volumes/{name}/deletions` body `{"confirm": "<name>", "backUpFirst": false}` | 200 `VolumeDeletion` (done) or 202 (archiving, `Location: /v1/volumes/{name}/deletions/{id}`); 400 `invalid_field` on `confirm`; 404 `volume_not_found`; 409 `volume_not_orphan` (field `name`, "app X declares it: delete it with the app, or drop it from the definition first"), 409 `volume_in_use`, 409 `no_backup_destination` (with `backUpFirst`), 409 `deletion_running`; 502 Nomad's refusal |
| `getVolumeDeletion` | `GET /v1/volumes/{name}/deletions/{id}` | 200 `VolumeDeletion`; 404 `deletion_not_found` |
| `getCleanupSettings` | `GET /v1/cleanup/settings` | 200 `CleanupSettings` |
| `putCleanupSettings` | `PUT /v1/cleanup/settings` body `CleanupSettings` | 200 `CleanupSettings`; 400 `invalid_field` (`keepDeployments` 1–10) |

Every answer is `Cache-Control: no-store`. `listActivity` gains the entries of both records (kind `server`).

## MCP

`get_server_space` (read; input `{"server": "one"}`): the body of `getServerSpace` as it came.

## Console routes (map to the operations above; the parity test lists none as console-only)

`GET /ops/servers/{name}/clean`, `POST /ops/servers/{name}/clean`, `GET /ops/servers/{name}/volumes/{volume}/delete`,
`POST /ops/servers/{name}/volumes/{volume}/delete`, `POST /ops/cleanup/settings`,
`GET /parts/ops/servers/{name}/space`.
