# Contract: API (`api/openapi.yaml` 0.8.0 → 0.9.0)

| Operation | Route | Body → Answer |
| --- | --- | --- |
| `redeployDeployment` | `POST /v1/apps/{id}/deployments/{deploymentId}/redeploy` | no body → 202 `Deployment` (pending), `Location` header like `deployApp` |
| `exportApp` | `GET /v1/apps/{id}/jobspec` | → 200 `text/plain; charset=utf-8`, the live version's HCL |
| `exportDeployment` | `GET /v1/apps/{id}/deployments/{deploymentId}/jobspec` | → 200 `text/plain; charset=utf-8`, that deployment's HCL |

The exports set `Content-Disposition: attachment; filename="<app>.nomad.hcl"`.

`Deployment` gains:

- `source` (string, optional): the deployment it was redeployed from.
- `definitionRecorded` (boolean): whether it can be redeployed (when healthy) or exported.

Errors, all JSON:

| Status | Code | When |
| --- | --- | --- |
| 404 | `not_found` | unknown app, or a deployment of another app |
| 409 | `deployment_not_healthy` | `redeployDeployment` of a deployment that isn't healthy |
| 409 | `definition_not_recorded` | redeploy or export of a deployment from before 011 |
| 409 | `no_live_deployment` | `exportApp` with no healthy deployment |
| 422 | `image_not_found` (existing) | a recorded digest is gone from its registry; `field` is `services[i].image` |
| 400 | `invalid_field` (existing) | a secret the version uses has no value (`services[i].secrets[j].value`), or the definition fails today's rules |

# Contract: console

| Route | Does |
| --- | --- |
| `POST /apps/{id}/deployments/{deploymentId}/redeploy` | `redeployDeployment`; 303 to `/apps/{id}/deployments`, or the page again at 422/409/502 with the error on that row |
| `GET /apps/{id}/jobspec` | `exportApp` (download) |
| `GET /apps/{id}/deployments/{deploymentId}/jobspec` | `exportDeployment` (download) |

The Deployments history row of a healthy deployment with a definition holds
`details.cv-confirm` "Redeploy this version". It names the images and the secrets the redeploy
removes, and says a later webhook resolves the tags again. A row with a source says "Redeploy of
<short id>". Configure gains an Export section linking `/apps/{id}/jobspec`.
