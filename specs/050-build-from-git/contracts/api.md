# API contract 050 (openapi 0.56.0)

Errors are the usual `{"error": {"code", "message", "field"}}`. Every new route needs a full API token or a signed-in
session; deploy tokens are refused by the default deny (403 `token_scope`), except that `deployApp` of their own app
keeps working when it builds.

## Model

`Service.build` (schema `ServiceBuild`, optional):

```json
{"repository": "https://github.com/acme/web.git", "branch": "main", "path": ".", "dockerfile": "Dockerfile",
 "access": "public", "hostKey": ""}
```

`access`: `public` (default), `deploy-key`, `token`. `hostKey`: `"<type> <base64>"`, required for an SSH repository on a
host other than github.com and gitlab.com. With `build`, the service's `image` is the repository builds push to, with
no tag or digest. The defaults are written into the stored app (`branch` `main`, `path` `.`, `dockerfile`
`Dockerfile`, `access` `public`).

`Deployment.build` (schema `BuildSummary`, present only on a deployment that built):

```json
{"id": "3f9a0c1d2e4b", "service": "web", "status": "succeeded", "repository": "https://github.com/acme/web.git",
 "branch": "main", "commit": "4e0a77c…", "author": "Ana", "subject": "Retry supplier timeouts",
 "image": "ghcr.io/acme/web:git-4e0a77c1b2d3", "digest": "sha256:…", "error": "",
 "queuedAt": "…", "startedAt": "…", "endedAt": "…"}
```

`status`: `queued`, `running`, `succeeded`, `failed`, `cancelled`. Times are RFC 3339, absent until known.

## Deploys

`deployApp` (with a body or empty), `deployCompose`… : for an app with a built service and no pinned version, the
answer is 202 with the deployment in status `pending` and its `build` `queued`, and `Location`. `redeployDeployment`
and `promoteApp` never build. New refusals, before anything is saved or recorded:

| Status | Code | Field |
| --- | --- | --- |
| 400 | `invalid_field` | `services[i].build.*`, `services[i].image`, `services[i].volumes[j].name` (spec "Refusals") |
| 409 | `git_access_missing` | `services[i].build.access` |
| 422 | `git_host_key_unknown` | `services[i].build.hostKey` |
| 422 | `build_registry_missing` | `services[i].image` |

A failed build makes the deployment `failed` with `error` "build failed: <reason>" followed by the last log lines.

## Build

`GET /v1/apps/{id}/deployments/{deploymentId}/build` → 200 `Build` (`BuildSummary` plus `log` string and `logCut`
boolean, true when only the last 64 KiB were kept; `log` is empty once pruned), `Cache-Control: no-store`.
404 `not_found` for the app or deployment, 404 `build_not_found` when the deployment didn't build.

## Git access

- `GET /v1/apps/{id}/services/{service}/git-access` → 200 `{"kind": "none"|"deploy-key"|"token", "publicKey": "ssh-ed25519
  AAAA… caravana web/web", "fingerprint": "SHA256:…", "username": "x-access-token", "createdAt": "…"}` (fields absent when
  they don't apply). 404 `not_found` for an unknown app or service.
- `POST /v1/apps/{id}/services/{service}/git-access/deploy-key` → 201 the same object (a new key; the old access is
  replaced). 400 `invalid_field` on `service` when the service isn't built.
- `PUT /v1/apps/{id}/services/{service}/git-access/token` body `{"username"?: "…", "token": "…"}` → 200 the object.
  400 `invalid_field` on `token`/`username`.
- `DELETE /v1/apps/{id}/services/{service}/git-access` → 204; 404 `git_access_not_found` when there is none.
- 502 `secret_store_unavailable` when Nomad's variables can't be read or written.

No answer ever holds the private key or the token.

## Settings

`GET /v1/builds/settings` → `{"memoryMB": 2048, "cpuMHz": 2000, "timeoutMinutes": 30, "concurrency": 1, "cacheMB": 5120}`;
`PUT` the same (every field required) → 200; 400 `invalid_field` naming the field out of range.

## Webhook

`POST /v1/webhooks/{id}` also takes the token from `X-Gitlab-Token`. For an app with a built service it reads the push
body (D6). `WebhookResult` gains `skipped` (string, optional): why nothing was built ("a push to refs/heads/dev, not
main", "the branch was deleted", "a ping"). A skipped call answers 200 with `changed: false` and the app's newest
deployment, or 204 when the app has none.
