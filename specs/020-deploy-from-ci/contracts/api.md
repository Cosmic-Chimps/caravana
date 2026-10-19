# Contract: API (`api/openapi.yaml` 0.18.0 → 0.19.0) and the action

## Deploy tokens

`createApiToken` (`POST /v1/tokens`) gains an optional `scope`:

```json
{"name": "shop-web-ci", "expiresAt": null, "scope": {"project": "shop", "app": "web"}}
```

- `project` must exist; `app` is an app name (`app.ValidName`), which need not exist yet.
- The answer and `listApiTokens` show `scope` (absent for a full token).
- A scoped token is a **deploy token**. With one, only these operations answer; anything else is
  **403 `token_scope`** ("this token can only deploy app web of project shop"), before any work:

| Operation | Allowed when |
| --- | --- |
| `getApp`, `getCompose`, `listDeployments`, `getDeployment`, `deployApp`, `deployCompose`, `previewPromotion`, `promoteApp` | the path's `{id}` has project = scope.project and name = scope.app (`app.PlaceOf`) |
| `createApp` | the body's `id` is in scope (the default project's apps: `{"project": "default", "app": "<name>"}`) |
| `createEnvironmentApp`, `getEnvironmentApp` | path project = scope.project and body/path app name = scope.app |
| `health` | always (no auth) |

  A promotion's target is the same name in the same project, so it is in scope by construction.
  A scoped token never reaches Settings, tokens, run/stop/start/restart/delete, the lists, other
  apps, the webhook-token endpoints or events.
- Console: Settings › API tokens offers "Only for one app" (project and app); an app's Configure
  page has "Create a deploy token for this app".
- Migration `0013_deploy_from_ci.sql`: `api_tokens.scope_project`, `api_tokens.scope_app` (NULL =
  full); `deployments.origin` (TEXT, JSON or NULL).

## Origin of a deploy

`deployApp`, `deployCompose`, `promoteApp` and the webhook call accept optional headers:

| Header | Value | Limit |
| --- | --- | --- |
| `Caravana-Origin-Commit` | the commit SHA | 7–64 hex |
| `Caravana-Origin-Ref` | branch or tag, e.g. `refs/heads/main` | 255, printable |
| `Caravana-Origin-Actor` | who pushed | 100, printable |
| `Caravana-Origin-Run` | the CI run's URL | 500, `https://` only |
| `Caravana-Origin-Repository` | `owner/name` | 200, printable |

A header that breaks its rule is **422 `invalid_field`** naming it; none is required. The
deployment records them as `origin {commit, ref, actor, run, repository}` (absent when none was
sent); `Deployment` gains `origin`; the history shows "from CI: `abc1234` on main by ana, run" (the
run a link); notifications add a line. The trigger stays `manual` (API) or `webhook`.

## What the action calls

1. `GET /v1/apps/{id}` → 404 (no app), or an `AppRecord` with `defined` and `revision`.
2. Deploy:
   - `image` given, app defined: take `.app`, set `services[i].image` (the one service, or the
     `service` input's), `POST /v1/apps/{id}/deployments` with it and `If-Match: <revision>`;
   - `definition` given and the app missing or undefined: `POST /v1/apps/{id}/deployments` with
     the file's App (its `image`, if the `image` input is set, replaced the same way);
   - neither, app defined: `POST /v1/apps/{id}/deployments` with an empty body (redeploy as
     stored);
   - else fail: "app X has no definition: give `definition` (or `image` with a definition file)".
   All with the origin headers from `GITHUB_SHA`, `GITHUB_REF`, `GITHUB_ACTOR`,
   `GITHUB_SERVER_URL/GITHUB_REPOSITORY/actions/runs/GITHUB_RUN_ID`, `GITHUB_REPOSITORY`.
3. 202 → `Location` → poll `GET` it every 5 s until `healthy|failed|reverted|superseded`.
4. `promote` given and healthy: `POST /v1/apps/{id}/promote` `{"to": promote}`, poll the same way.
5. 412 `app_changed` on step 2: read again and retry once.
