# Contract: API (`api/openapi.yaml` 0.10.0 → 0.11.0)

All additions are optional for existing clients: no request that works today changes meaning.

## Conditional deploys

| Operation | Route | New |
| --- | --- | --- |
| `getApp` | `GET /v1/apps/{id}` | `ETag: "<revision>"` header; body `AppRecord` gains `revision` (string) |
| `deployApp` | `POST /v1/apps/{id}/deployments` | optional `If-Match: "<revision>"` (with a body; ignored without one, since an empty body redeploys the stored app) |
| `deployCompose` | `POST /v1/apps/{id}/compose` | optional `If-Match: "<revision>"`, or `If-None-Match: *` (create only) |

Errors:

| Status | Code | When |
| --- | --- | --- |
| 412 | `app_changed` (new) | `If-Match` given and the stored app's revision differs; `field` absent; message names the current revision |
| 409 | `app_exists` (existing) | `If-None-Match: *` and the ID is taken; `field` `id` |
| 404 | `not_found` (existing) | `If-Match` on an app that doesn't exist |
| 400 | `invalid_field` (existing) | both headers given, `If-Match` not a quoted value, or `If-None-Match` other than `*`; `field` names the header |

A weak tag (`W/"…"`) is treated as the same revision. One revision per `If-Match`; a list is
400.

## Copies with usage

`listCopies` `GET /v1/apps/{id}/copies` gains the query parameter `usage` (`true` | `false`,
default `false`). `Copy` gains:

- `lastRestartAt` (date-time, optional): the newest restart of any of its tasks.
- `outOfMemory` (boolean): its last stop was the kernel killing it for passing its memory
  ceiling.
- `usage` (`CopyUsage`, optional): only with `usage=true`, only for copies that are starting,
  healthy or unhealthy.

`CopyUsage`:

| Field | Type | Meaning |
| --- | --- | --- |
| `cpuMHz` | integer | CPU used now |
| `memoryMB` | integer | memory used now |
| `cpuReservedMHz` | integer | the service's CPU reservation |
| `memoryReservedMB` | integer | the service's memory reservation |
| `memoryCeilingMB` | integer | the service's memory ceiling |
| `nearCeiling` | boolean | `memoryMB` is 90 % or more of `memoryCeilingMB` |
| `error` | string | set (and the numbers absent) when Nomad couldn't give this copy's usage |

Nomad unreachable for the list itself stays 502 `nomad_unavailable`, as today.

## Attention

| Operation | Route | Answer |
| --- | --- | --- |
| `listAttention` | `GET /v1/attention` | 200 `Attention` |

`Attention`: `{ "items": [AttentionItem], "copiesUnknown": boolean }`, items newest first.

`AttentionItem`:

| Field | Type | Meaning |
| --- | --- | --- |
| `kind` | enum `deploy_failed`, `out_of_memory`, `restarting`, `unhealthy`, `backup_failed`, `restore_failed`, `no_backup_destination` | what needs a person |
| `appId` | string, optional | absent for `no_backup_destination` |
| `service` | string, optional | the service, for copy and backup kinds |
| `copies` | string[], optional | copy IDs, for copy kinds |
| `deploymentId` | string, optional | for `deploy_failed` |
| `jobVersion` | integer, optional | for `deploy_failed`, when registered |
| `at` | date-time | when it happened |
| `detail` | string, optional | the raw fact (Nomad's description, the deploy or backup error) |

`copiesUnknown` is true when Nomad couldn't be read; the store-based kinds are still listed and
the answer is still 200. Needs an API token or a session like every `/v1` route.
