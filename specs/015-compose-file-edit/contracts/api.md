# Contract: API (`api/openapi.yaml` 0.11.0 → 0.12.0)

Additions only: no request that works today changes meaning. One behavior is newly documented:
`deployApp` with a body on an app with a kept file drops the file and warns.

## Read the kept file

| Operation | Route | Answer |
| --- | --- | --- |
| `getCompose` | `GET /v1/apps/{id}/compose` | 200 `ComposeRequest` + `ETag: "<revision>"` |
| `getDeploymentCompose` | `GET /v1/apps/{id}/deployments/{deploymentId}/compose` | 200 `ComposeRequest` |

`ComposeRequest` is the existing request schema of `deployCompose`. In these answers every
`services.<name>.secrets[]` has `name` only, never `value`. Sending the answer back to
`deployCompose` unchanged keeps every secret's current value, and with
`If-Match: "<revision>"` it is a safe edit.

Errors:

| Status | Code | When |
| --- | --- | --- |
| 404 | `not_found` | no such app, or no such deployment of this app |
| 404 | `compose_not_kept` | the app (or deployment) has no kept file. `message`: "This app's Compose file wasn't kept." (or "This deployment wasn't made from a Compose file.") |

Both need an API token or a session, like every `/v1` read.

## New fields

| Schema | Field | Type | Meaning |
| --- | --- | --- | --- |
| `AppRecord` (`getApp`) | `composeKept` | boolean | the app has a kept file; `getCompose` answers 200 |
| `AppSummary` (`listApps`) | `composeKept` | boolean | same |
| `Deployment` (every read, and deploy answers) | `composeKept` | boolean | this deployment has a file; `getDeploymentCompose` answers 200 |

`AppRecord.revision` and the `ETag` of `getApp` now change when the kept file changes, including
a comment-only edit. For apps without a kept file they are unchanged.

## Behavior documented on existing operations

- `deployCompose`: the request, secret values removed, becomes the app's kept file and the new
  deployment's file.
- `deployApp` **with a body** on an app with a kept file: the deploy proceeds, the kept file is
  dropped, and the deployment's `warnings` include
  `{"message": "This app's Compose file was dropped: it was deployed from a definition, not a file."}`.
- `deployApp` without a body, and `POST /v1/webhooks/{id}`: the kept file is unchanged, and the
  new deployment records it.
- `redeployDeployment`: the version's file (or none, with the warning above) becomes the kept file.
- `deleteApp`: the kept file and every deployment's file are deleted with the app.

## Parity (`internal/api/parity_test.go`)

| Console route | Operations |
| --- | --- |
| `GET /apps/{id}/configure` | `getApp`, `getWebhook`, `getCompose` |
| `GET /apps/{id}/compose` | `getApp`, `getCompose` |
| `GET /apps/{id}/deployments/{deploymentId}/compose` (new) | `getDeploymentCompose` |
