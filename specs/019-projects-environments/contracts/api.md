# Contract: API (`api/openapi.yaml` 0.15.0 → 0.16.0)

Additions only. Every existing operation keeps its path and meaning (FR-010).

## Projects and environments

| Operation | Route | Body | Answer |
| --- | --- | --- | --- |
| `listProjects` | `GET /v1/projects` | | 200 `[]Project` (Default first, then by creation) |
| `createProject` | `POST /v1/projects` | `{name, displayName?}` | 201 `Project` (with its `production` environment), `Location` |
| `getProject` | `GET /v1/projects/{project}` | | 200 `Project` |
| `updateProject` | `PATCH /v1/projects/{project}` | `{displayName}` | 200 `Project` |
| `deleteProject` | `DELETE /v1/projects/{project}` | | 204 |
| `createEnvironment` | `POST /v1/projects/{project}/environments` | `{name}` | 201 `Environment` |
| `deleteEnvironment` | `DELETE /v1/projects/{project}/environments/{environment}` | | 204 |
| `listEnvironmentApps` | `GET /v1/projects/{project}/environments/{environment}/apps` | | 200 `[]AppSummary` |
| `createEnvironmentApp` | `POST /v1/projects/{project}/environments/{environment}/apps` | `App` with `name` → see below | as `createApp` |
| `getEnvironmentApp` | `GET /v1/projects/{project}/environments/{environment}/apps/{name}` | | as `getApp` (with `ETag`) |

`createEnvironmentApp` takes the same `App` body as `createApp`, with the app's name in
`appName` (the `id`, when given, must equal the computed one). It answers exactly as `createApp`
does (202 with the deployment; `Location: /v1/apps/{id}`).

Schemas:
- `Project`: `{name, displayName, environments: []Environment, createdAt, updatedAt}`.
- `Environment`: `{project, name, position, apps (count)}`.
- `displayName` defaults to `name`; 1–100 printable characters.

## Where an app is

`AppRecord`, `AppSummary`, `AttentionItem`, `Deployment` (of `listRecentDeployments`) and the
webhook notification message gain `project`, `environment`, `appName`.
`listApps` takes optional `?project=` and `?environment=`.

The ID rule, documented on the `id` path parameter: the name for `default`/`production`,
otherwise `<project>--<environment>--<name>`. Any operation that creates an app
(`createApp`, the first `deployApp` or `deployCompose` of an unknown ID) places it by that rule.

## Promote and copy

| Operation | Route | Body | Answer |
| --- | --- | --- | --- |
| `previewPromotion` | `POST /v1/apps/{id}/promote/preview` | `{to}` | 200 `PromotionPreview` |
| `promoteApp` | `POST /v1/apps/{id}/promote` | `{to}`, optional `If-Match` (the target's revision) | 202 `Deployment` of the target, `Location` |
| `copyApp` | `POST /v1/apps/{id}/copy` | `{to}` | 201 `AppRecord` of the copy, plus `missingSecrets: [{service, name}]` |

`{id}` is the source; `to` is an environment name of the same project.

`PromotionPreview`: `{from, to (app IDs), fromEnvironment, toEnvironment, fromDeployment,
services: [{name, imageFrom, imageTo, digestFrom, digestTo, changed: []string}], added: [],
warnings: [], targetRevision}`. `imageFrom`/`digestFrom` are empty for an added service.

`Deployment` gains `promotedFrom: {app, environment, deployment}`, present only on a promotion;
`source` is then absent (it stays only for a redeploy of the same app's past deployment).

## Errors

| Status, code | When |
| --- | --- |
| 404 `project_not_found` | unknown project (field `project`) |
| 404 `environment_not_found` | unknown environment (field `environment`, or `id` when an ID names one, or `to`) |
| 409 `project_exists`, 409 `environment_exists` | the name is taken (field `name`) |
| 409 `not_empty` | delete with apps left; the message names them |
| 409 `protected` | deleting project `default` or its `production` environment |
| 422 `invalid_field` | a name that isn't a label or holds `--` (field `name`); `to` equal to the source's environment |
| 404 `app_not_in_environment` | promote to an environment without that app (field `to`); the message says to copy it first |
| 409 `app_exists` | copy to an environment that has the app (field `to`); also a create whose computed ID is taken |
| 409 `promotion_removes_service` | the target has a service the source lacks (field `services.<name>`) |
| 409 `deployment_not_healthy`, 409 `definition_not_recorded` | the source's newest deployment (existing codes) |
| 422 `image_not_found` etc. | as `redeployDeployment` (existing codes) |
| 412 `app_changed` | `If-Match` doesn't match the target's revision (existing) |

## Parity

| Console route | Operations |
| --- | --- |
| `GET /apps` | `listProjects`, `listApps` |
| `GET /projects/new`, `POST /projects` | `createProject` |
| `GET /projects/{project}` | `getProject`, `listEnvironmentApps` |
| `POST /projects/{project}` | `updateProject` |
| `POST /projects/{project}/delete` | `deleteProject` |
| `POST /projects/{project}/environments` | `createEnvironment` |
| `POST /projects/{project}/environments/{environment}/delete` | `deleteEnvironment` |
| `POST /apps`, `POST /apps/new/compose` (with `place`) | `createApp`/`createEnvironmentApp`, `deployCompose` |
| `GET /apps/{id}/promote?to=` | `previewPromotion` |
| `POST /apps/{id}/promote` | `promoteApp` |
| `POST /apps/{id}/copy` | `copyApp` |
