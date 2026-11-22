# Contract: the API of previews (0.54.0)

Every answer and error is the usual JSON. "Preview name" is `pr-<n>` (`^pr-[1-9][0-9]{0,8}$`).

## Changed

### `createEnvironment` — `POST /v1/projects/{project}/environments`

Body unchanged (`{"name": "pr-12"}`). New refusals:

- a name starting `pr-` that isn't a preview name: 422 `invalid_field`, field `name`, "names starting with pr- are
  kept for previews: pr-<pull request number>, such as pr-12";
- a preview name beyond the project's limit: 409 `preview_limit`, field `name`, the message naming the open ones
  (with limit 0: "previews are off for project blog (its limit is 0)").

A deploy token may call it for its own project with a preview name (else 403 `token_scope`).

### `deleteEnvironment` — `DELETE /v1/projects/{project}/environments/{environment}`

Unchanged for full tokens. A deploy token may call it for a preview environment of its own project. Not empty:
409 `not_empty` as before.

### `deleteApp` — `DELETE /v1/apps/{id}`

Unchanged for full tokens. A deploy token may delete its own app only when the ID's environment is a preview name.

### `Environment` schema

Gains `preview` (object, only on preview environments): `number` (integer, the pull request number) and `expiresAt`
(date-time, D2).

### Deploys into a preview of a protected project

`deployApp`, `deployCompose`, `createApp`, `createEnvironmentApp` (with deploy), `redeployDeployment`, `promoteApp`
into a preview, the webhook: each domain gets `teamOnly: true`; when any changed, one warning on the first changed
field (`services[0].domains[0].teamOnly`): "domains of preview environments are Team only with project blog's
preview password". No `teamPasswords` entry for these hosts.

### `rotateTeamPassword`

On a domain whose password is its project's preview password: 409 `preview_password`, field `host`.

## New

### `getPreviews` — `GET /v1/projects/{project}/previews`

200:

```json
{
  "project": "blog",
  "settings": {"limit": 5, "expireAfterDays": 7, "passwordSet": true},
  "previews": [
    {
      "environment": "pr-12",
      "number": 12,
      "createdAt": "2026-11-22T10:00:00Z",
      "expiresAt": "2026-11-29T12:31:07Z",
      "apps": [
        {
          "id": "blog--pr-12--web",
          "name": "web",
          "state": "healthy",
          "addresses": ["pr-12-blog.lacalabaza.xyz"],
          "memoryMB": 64,
          "lastDeployment": {
            "id": "…", "status": "healthy", "createdAt": "…", "trigger": "api",
            "tokenName": "blog-web-ci",
            "origin": {"commit": "abc1234…", "ref": "refs/pull/12/merge", "actor": "ana", "run": "https://…"}
          }
        }
      ]
    }
  ]
}
```

`previews` is sorted by number; `apps` by ID; `lastDeployment` is absent for an app never deployed and `origin`
absent when the deployment has none. 404 `project_not_found`. Deploy tokens: 403.

### `putPreviewSettings` — `PUT /v1/projects/{project}/previews/settings`

Body, every field optional (absent keeps):

```json
{"limit": 5, "expireAfterDays": 7, "password": "…", "removePassword": false}
```

- `limit` 0–20, `expireAfterDays` 1–30: else 422 `invalid_field` naming it.
- `password` 12–128 characters, printable (no control characters): else 422 on `password`. Sets or changes it;
  open previews use a changed one at once. `password` with `removePassword: true`: 422 on `removePassword`.
- `removePassword: true` while previews are open: 409 `previews_open` on `removePassword`, naming them.
- 200: the `settings` object (as in `getPreviews`) plus `warnings` (a list of `{field, message}`), with "open
  previews pr-3, pr-4 get the password at their next deploy" when a password was set the first time with previews
  open. The password is never in an answer.

Deploy tokens: 403.

### `closePreview` — `DELETE /v1/projects/{project}/previews/{environment}`

Deletes every app of the preview (as `deleteApp`), then the environment. 204. 404 `project_not_found` /
`environment_not_found`; 409 `not_a_preview` (field `environment`) for an environment that isn't a preview; 409
`not_empty` when an app appeared meanwhile; an app that can't be deleted answers as `deleteApp` would (502 when
Nomad refuses) and the preview stays with what is left. Deploy tokens: 403.

## Codes

`preview_limit`, `previews_open`, `not_a_preview`, `preview_password`.
