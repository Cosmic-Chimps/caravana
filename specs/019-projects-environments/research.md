# Research: Projects and Environments (019)

## R1. One Nomad namespace, not one per environment

**Decision**: every app of every project and environment runs in Nomad's `default` namespace, as
today. Environments are kept apart by job ID (R2), which also keys their secrets, volumes,
backups and routers.

**Evidence (lab, 2026-10-17, Traefik v3.7.13, Nomad 2.0.7)**: a whoami job in a second namespace
`exp019-staging`, with the Traefik read policy widened to `namespace "*"`:

| Traefik flag | `default` app | other-namespace app |
| --- | --- | --- |
| none (today) | 200 | 404 |
| `--providers.nomad.namespaces=*` | 404 | 404 (no routers at all) |
| `--providers.nomad.namespaces=default,exp019-staging` | 200 | 200 (router `expns-web@nomad-exp019-staging`) |
| same list, a namespace created afterwards | 200 | 404 until Traefik is redeployed |

So a namespace per environment means rewriting Traefik's arguments and redeploying it (a gap on
80/443 for every app, and its ACME state) each time an environment is added, plus a policy per
namespace. In exchange it would give per-namespace ACLs, which only matter with teams (Phase 4).

**Rationale**: single-node, single-admin installations get nothing from namespaces today;
Traefik can't follow new ones without a restart. Secrets are already isolated per job: a task's
workload identity reads only `nomad/jobs/<its job ID>/...` (SC-004 holds without namespaces).

**Alternatives**: namespace per environment (rejected above); namespace per project (same
Traefik problem, fewer restarts); revisit with teams in Phase 4, when moving a job between
namespaces can be a deliberate migration.

## R2. The app ID

**Decision**: an app's ID is its name when it is in project `default`, environment `production`;
otherwise `<project>--<environment>--<name>`. Project, environment and app names are lowercase
DNS labels that never contain `--`, so the two forms never collide and the parts can be read
back. The ID still passes today's rules: a DNS label, and `len(id)+1+len(service) ≤ 63`
(`app/validate.go:199`); a longer combination is refused naming the field with both lengths.

**Rationale**: every Nomad-facing and stored name already derives from the ID (job, services
`<id>-<svc>`, Traefik router `<id>-<svc>`, variables `nomad/jobs/<id>/...`, host volumes
`<id>_<vol>`, backup jobs `<id>_<svc>_backup`, backup objects `<id>/<svc>`, webhook path, console
URLs). Keeping that one key means none of those change, existing apps keep theirs byte for byte,
and the readers that split names on `_` or `/` (`splitBackupJobID`, `splitVolumeVariablePath`)
keep working since IDs hold neither. Clients compute the ID from the three names.

**Constitution VI** names the router `<app>-<service>`, with `<app>` the app ID; that stays
true, so no amendment is needed (the six defaults expected one only for the namespace route).

**Alternatives**: always prefix (`default--production--whoami`) — moves every existing app;
a random ID — clients can't compute it, and Nomad names become unreadable.

## R3. Where project, environment and name are stored

**Decision**: new tables `projects` and `environments`; three new columns on `apps`
(`project`, `environment`, `app_name`), filled for existing rows by the migration (`default`,
`production`, the ID), with `UNIQUE(project, environment, app_name)`. They are not part of the
`app.App` document.

**Rationale**: the document is what `Translate` reads and what the `revision` (ETag) hashes;
keeping placement out of it means no existing revision changes on upgrade and the translator
stays as it is (constitution II). The migration inserts the Default project and its production
environment.

## R4. Promotion

**Decision**: `Service.Promote(ctx, appID, toEnvironment)`:
1. the source's newest deployment must be healthy with a recorded definition
   (`deployment_not_healthy`, `definition_not_recorded`);
2. the target is the app with the same name in `toEnvironment` of the same project
   (`app_not_in_environment`, with "copy it first");
3. the target definition is built from the target's stored app: for each source service the
   target has, take image, command, args, ports, health and volume mounts from the source and
   keep env, secrets (names: their values stay), domains, replicas, resources and backup;
   a source-only service is added whole without domains; a target-only service refuses
   (`promotion_removes_service`, field `services.<name>`);
4. `CheckDigests` on the source's digests, then the normal deploy with those digests pinned
   (as `RedeployVersion`), so a missing secret on an added service is the existing
   `invalid_field` "secret X has no value yet";
5. the record's `source_deployment` holds the source deployment's ID (IDs are unique across
   apps), so history and the notification read "promoted from <env> (deployment …)".

A target that keeps a Compose file loses it with the existing warning ("deployed from a
definition"); the preview says so. Promoting kept files is left for later.

## R5. Copy to environment

**Decision**: `Service.CopyApp(ctx, appID, toEnvironment)` inserts the target (no deploy): the
source's stored app with domains removed (secret values are never stored), and its kept Compose
file with the overlay's domains removed. A new store path saves an app with no deployment; such
an app already shows as "not running". Webhook token, backups and volume contents are not
copied.

## R6. API shape

Additive (0.16.0):
- `listProjects`, `createProject`, `getProject`, `updateProject`, `deleteProject`
  (`/v1/projects`, `/v1/projects/{project}`);
- `createEnvironment`, `updateEnvironment`, `deleteEnvironment`
  (`/v1/projects/{project}/environments[/{environment}]`);
- `listEnvironmentApps`, `createEnvironmentApp`, `getEnvironmentApp`
  (`/v1/projects/{project}/environments/{environment}/apps[/{name}]`);
- `previewPromotion`, `promoteApp` (`/v1/apps/{id}/promote[/preview]`, body `{to}`) and
  `copyApp` (`/v1/apps/{id}/copy`, body `{to}`);
- `AppRecord`, `AppSummary`, attention items and notification messages gain `project`,
  `environment` and `appName`; `listApps` takes `?project=&environment=`.

Every other operation keeps `/v1/apps/{id}` (FR-010).

## R7. The place comes from the ID, on every create path

**Decision**: a pure `app.PlaceOf(id)` reads the place from an ID: no `--` means project
`default`, environment `production`, name = ID; otherwise exactly three labels joined by `--`,
none containing `--`, else a field error on `id`. `app.Place.ID()` is the inverse. Every path
that creates an app (`createApp`, a first `deployApp`/`deployCompose` of an unknown ID, the
console's New app, `createEnvironmentApp`, `copyApp`) places it there and refuses with 404
`environment_not_found` (field `id`) when that project or environment doesn't exist. The store
fills its columns from the same function on insert, and never changes them on an update.

**Rationale**: one rule, no new parameter on the existing operations: a client that computes
`shop--staging--web` can deploy it with today's `deployApp`, which is what Aspire and 020 need.
`environment_not_found` keeps a typo from creating an app nobody can see under a project.

**Existing IDs with `--`** (allowed until now): they stay in Default › production with
`app_name` = ID, and keep deploying (the rule applies to creation only). A later create whose
computed ID equals one of them is refused by the store's primary key, as `app_exists`.

## R8. Length

The console's New app keeps its 31-character name limit. `PlaceOf` doesn't limit lengths; the
existing `len(id)+1+len(service) ≤ 63` check in `app.Validate` does, and its message names the
ID's length, so `shop--staging--` costs 15 characters of that budget and the owner sees why.
