# Research: Rollback and export

## R1. What a deployment records

**Decision**: two new nullable columns on `deployments` (migration `0006_rollback.sql`):
`definition`, the app document as deployed (the same JSON as `apps.document`, without secret
values), and `source_deployment`, the ID a redeploy came from. Every deploy writes `definition`
when it creates its record (`deployWarned` already holds the app). `DeploymentRecord` gains
`Definition *app.App` (nil for older rows) and `Source string`; `app.Deployment` doesn't change,
because it is the translator's input.

**Rationale**: the digests are already per deployment; only the definition is missing. Storing
it with the record makes "what ran then" a single row. A definition is a few KB, and the history
already lives as long as the app.

**Alternatives considered**: Nomad's job versions (`Jobs().Revert`). Nomad keeps only a few
versions, the job has lost the app model (secrets as names, backup policy, volumes to create),
and a revert isn't a new Caravana deployment. A separate `definitions` table keyed by hash:
saves space nobody is short of, and adds a join.

No foreign key on `source_deployment`: the store deletes an app's deployments in one statement,
and a self-reference could trip `foreign_keys`. The service checks the source belongs to the app.

The definition uses the app document format (`appSchemaVersion` 1). A future format change
migrates both columns in the same migration, so no version column is added.

## R2. Redeploying a past deployment

**Decision**: `Service.RedeployVersion(ctx, appID, deploymentID, trigger)`. It loads the
source, refuses unless it belongs to the app, is `healthy` and has a definition, checks every
digest still exists in its registry, and then runs `saveAndDeploy` with an option carrying the
pinned digests and the source ID. `deployWarned` skips `Resolve` when digests are given.
Everything else is the normal deploy: `Validate`, `checkSecrets` (a secret with no current value
fails naming `services[i].secrets[j].value`), `checkBackups`, `SaveApp` (the stored definition
becomes the source's), per-app ordering, newest wins, the watcher, resume after restart.

**Rationale**: one deploy path, so every rule that applies to a deploy applies to a rollback.
`nomadjob.Translate` already rejects digests that don't match the services, so a bad pin can't
slip through. Resume reads only Nomad and the record, so a rollback resumes like any deploy.

**Alternatives considered**: a new trigger value `rollback`. The trigger says who started the
deploy (API, console, webhook); the source says what was deployed. Keeping them apart keeps the
existing trigger rules and words.

## R3. A digest that is gone

**Decision**: `registry.Client.Exists(ctx, ref)` sends `HEAD /v2/<repo>/manifests/<digest>`
with the same token and retry handling as `Resolve`. `Deployer.CheckDigests(ctx, a, digests)`
calls it per service and returns the existing `*ImageError` with `ErrImageNotFound`, which the
API already answers as 422 `image_not_found`. It runs before anything is saved or recorded.

**Rationale**: `Resolve` returns a digest reference without asking the registry, so without
this check a deleted image would only fail later in Nomad, after the definition was saved.

## R4. Secrets a rollback removes

**Decision**: a pure helper `removedSecrets(old, next app.App) []app.Warning` in
`internal/service`, used by `saveAndDeploy` for every deploy, beside `unmountedVolumes`. The
console calls the same pure `service.RemovedSecrets(current, definition)` (as built; first planned as a `RedeployImpact` call) for
the confirmation before anything is posted.

**Rationale**: any deploy that drops a secret deletes its value (Constitution III), so every
deploy says so, not only a rollback. One rule is simpler than a rollback-only one.

## R5. HCL

**Decision**: a small hand-written writer, `nomadjob.HCL(job *api.Job, header string)
([]byte, error)`, typed against the `api.Job` fields `Translate` sets, with sorted map keys.
Strings are quoted with `\\`, `\"`, `\n`, `\t` and `\uXXXX` escapes, and with `${` → `$${` and
`%{` → `%%{`, because HCL2 reads those as templates. The env template's `data` is a heredoc
(only `${` and `%{` escaped there) when it ends in a newline, otherwise a quoted string.

Two tests keep it honest. Golden files `testdata/<case>.golden.nomad.hcl` for every translator
fixture. A coverage test marshals each fixture's job to JSON, lists every non-empty field path,
and fails naming any path the writer doesn't cover, so a field `Translate` gains later can't be
dropped from exports without a red test. In the lab, the gate parses the export through Nomad's
`/v1/jobs/parse` and plans it against the live job.

**Rationale**: the subset is small (one job, one group per service, one docker task plus the
optional `names` sidecar). A writer for it is about 250 lines of plain Go and needs no module.

**Alternatives considered**: `hclwrite` with `go-cty`. It handles quoting, but adds two direct
modules and their dependencies (Constitution I), and it still needs code that knows every block
and attribute name. Rendering Nomad's stored job (`Jobs().Info`): it has every default filled
in, which makes the file long and hides what Caravana chose.

## R6. What is exported

**Decision**: `Translate(definition, deployment, current settings)`, rendered with a header
comment. The header names the deployment and its time, then what the job needs from the cluster:
Traefik with its Nomad provider (entrypoint and cert resolver from the settings, and the
domains), each host volume `<app>_<volume>`, and each service's secret names with their Nomad
Variable path `nomad/jobs/<app>/<service>`. Nothing reads Nomad to export.

**Rationale**: the same pure function made the job Nomad runs, so the same input gives the same
job. Exports need no Nomad access, so the policy doesn't change.

`// simplified:` the settings (entrypoint, cert resolver, names image) are today's, not those
of the deployment. They change only when the operator reconfigures Caravana. If that matters
later, store the settings with the deployment too.

## R7. API and console

**Decision**:

- `POST /v1/apps/{id}/deployments/{deploymentId}/redeploy` answers 202 with the new deployment,
  like `deployApp`.
- `GET /v1/apps/{id}/jobspec` exports the live version.
- `GET /v1/apps/{id}/deployments/{deploymentId}/jobspec` exports a past one.
- The two exports answer `text/plain; charset=utf-8` with
  `Content-Disposition: attachment; filename="<app>.nomad.hcl"`. Errors stay JSON.
- `Deployment` gains `source` and `definitionRecorded`.

In the console, every healthy history row with a definition gets an inline confirmation
(`details.cv-confirm`, as for secrets and webhooks). It lists the images, the secrets it
removes, and the webhook note, and posts to `/apps/{id}/deployments/{deploymentId}/redeploy`.
Rows with a definition get an export link. Configure gets an Export section with the live
version's link. A row redeployed from another says so.

**Rationale**: it follows the existing patterns. Redeploying a past version isn't destructive the
way delete is, so it needs no typed confirmation. `text/plain` opens in any browser and saves
with the right name.

## R8. Gate

**Decision**: a final gate step, `rollback-export`:

1. Redeploy `g.first`, the first healthy whoami deployment, which has a different digest from
   the live one. Wait for healthy.
2. Check that Nomad runs `g.first`'s digest and that the record names its source.
3. Export the live version, parse it with `Jobs().ParseHCL(…, true)`, and plan it against the
   job with `Jobs().Plan(…, diff=true)`. The diff must be `None`.

A console lab step redeploys a past version through the console form.

**Rationale**: the plan is the real test of FR-013 and SC-003, and the lab already has Nomad.
The step runs last, so it doesn't change what earlier steps expect.
