# Data model: Rollback and export

## Deployment (store `deployments`, two new columns)

| Column | Type | Meaning |
| --- | --- | --- |
| `definition` | TEXT, nullable | The app document as deployed (`app.App` JSON, `appSchemaVersion` 1, no secret values). NULL for deployments recorded before feature 011. |
| `source_deployment` | TEXT, nullable | For a redeploy of a past deployment, that deployment's ID. NULL otherwise. No foreign key (R1). |

`store.DeploymentRecord` gains:

- `Definition *app.App`: nil when the column is NULL.
- `Source string`: "" when NULL.

`CreateDeployment` takes both. `UpdateDeployment` never changes them, so both are fixed at
creation. Status transitions don't change.

Rules, enforced in `internal/service`:

- **Redeploy target**: same app, status `healthy`, and `Definition != nil`. Otherwise refused with
  `deployment_not_healthy` or `definition_not_recorded`.
- **Export target**: same app and `Definition != nil`. With no deployment ID, the target is the
  newest healthy deployment (`LatestHealthyDeployment`). Without one, refused with
  `no_live_deployment`.

## Jobspec export (not stored)

Produced on request: `deploy.Export(definition, record.Deployment)`, which is `nomadjob.Translate`
with today's settings, then `nomadjob.HCL(job, header)`.

The header is HCL `#` comments listing:

- the app, deployment ID and time
- the Traefik entrypoint and cert resolver, with each service's domains
- each host volume `<app>_<volume>`
- each service's secret names with their Nomad Variable path `nomad/jobs/<app>/<service>`

The file is named `<app>.nomad.hcl`.

## Warning (existing `app.Warning`, a new source)

A deploy whose definition drops secrets the stored app had warns once per secret, on
`services[i].secrets` (field of the old app), saying its value is removed. It is the same list
the console's confirmation shows, from `service.RemovedSecrets` (implemented instead of a `RedeployImpact` call: the history already holds the definitions).
