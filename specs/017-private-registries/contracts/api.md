# Contract: API (`api/openapi.yaml` 0.13.0 → 0.14.0)

Additions only. One message changes wording: "missing or private" now points at Settings ›
Registries.

## Registries

| Operation | Route | Body | Answer |
| --- | --- | --- | --- |
| `listRegistries` | `GET /v1/registries` | none | 200 `[]Registry` |
| `putRegistry` | `PUT /v1/registries/{host}` | `{"username": "...", "password": "..."}`; `password` required when creating, optional when updating | 200 `Registry` |
| `deleteRegistry` | `DELETE /v1/registries/{host}` | none | 204 |
| `testRegistry` | `POST /v1/registries/{host}/test` | none | 200 `Registry` with `lastTest` updated |

```json
{
  "host": "ghcr.io",
  "username": "acme-bot",
  "createdAt": "2026-10-16T10:00:00Z",
  "updatedAt": "2026-10-16T10:00:00Z",
  "lastTest": {"at": "2026-10-16T10:01:00Z", "ok": false, "message": "the registry refused the stored credentials (401)"}
}
```

- `password` never appears in an answer.
- `lastTest` is absent until the first test.
- `{host}` is the host as images name it, port included: `ghcr.io`, `registry.example.com:5000`,
  `docker.io` for Docker Hub. It is lowercased, and `index.docker.io` is stored as `docker.io`.

Errors:

| Status | Code | When |
| --- | --- | --- |
| 422 | `invalid_field` | `host` isn't a registry host; `username` empty; `password` missing on create or present but empty |
| 404 | `not_found` | `deleteRegistry` or `testRegistry` for a host with no credentials |

## Deploys

| Status | Code | When |
| --- | --- | --- |
| 422 | `registry_auth_failed` (new) | the registry refused the stored credentials. `field` is the image's field (`services[i].image`); the message names the registry and the image |
| 422 | `image_not_found` (existing) | unchanged; without stored credentials its message ends "add credentials for <host> in Settings › Registries" |

`createApp`, `deployApp`, `deployCompose`, `redeployDeployment` and the webhook can all answer
`registry_auth_failed`, in the same places they answer `image_not_found` today.

## Parity

| Console route | Operations |
| --- | --- |
| `GET /settings` | `listTokens`, `listRegistries` |
| `POST /settings/registries` | `putRegistry` |
| `POST /settings/registries/{host}/test` | `testRegistry` |
| `POST /settings/registries/{host}/remove` | `deleteRegistry` |
