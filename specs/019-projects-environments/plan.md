# Implementation Plan: Projects and Environments

**Branch**: `019-projects-environments` (stacked on `018-notifications`) | **Date**: 2026-10-17 |
**Spec**: [projects-environments.spec.md](./projects-environments.spec.md)

**Scale note**: about 35 files in five packages (`app`, `store`, `service`, `api`, `ui`), one
migration, no Nomad change and no change to `nomadjob`. The risk is moving what runs: research
R1–R3 keep every Nomad-facing name keyed by the app ID as it is today, so an upgrade changes no
job, variable, volume, backup path or router, and no stored revision.

## Summary

Projects hold ordered environments; each app sits in one environment. The place is read from the
app ID (`<project>--<environment>--<name>`, or the bare name for Default › production), so
existing apps stay byte for byte where they are and clients compute IDs. One Nomad namespace,
as today (the lab showed Traefik can't follow new namespaces without a restart). Promotion
deploys a target with the source's healthy digests and service shape, keeping the target's env,
secrets, domains, memory, copies and backups; copy creates an undeployed app in another
environment. API 0.16.0 adds project, environment, promote and copy operations; the console
groups the home by project and environment and adds project pages, a place select on New app,
a breadcrumb, and Promote/Copy on each app.

## Technical Context

**Language/Version**: Go 1.27, no CGO

**Primary Dependencies**: none new

**Storage**: SQLite migration `0011_projects.sql` (data-model.md); nothing in Nomad Variables

**Testing**: pure `PlaceOf` table tests; store tests including an upgrade from 0010 with data;
service tests on the deploytest/registrytest fakes (promotion pins digests, keeps env/secrets/
domains, refuses as listed); API and console handler tests; lab steps `projects-promote` (gate)
and `console-projects`.

**Target Platform**: single-node Linux host

**Project Type**: web service

**Performance Goals**: listing grouped apps costs one extra query; promote is a normal deploy.

**Constraints**: zero change to running jobs on upgrade; every existing path and webhook URL
keeps working; secrets never cross environments (they are per job ID already).

**Scale/Scope**: a handful of projects, 2–4 environments each.

## Constitution Check

| Principle | Assessment |
| --- | --- |
| I. Small by Default | PASS. No module, no service; two small tables. |
| II. One Model, Many Inputs | PASS. `app.App` and `Translate` unchanged; the place lives beside the document (R3). Promotion builds an `app.App` like every other input. |
| III. Safe, Reversible Deploys | PASS. A promotion is a normal health-checked deploy with digests pinned (no floating tag), refused before anything changes when an image is gone; target env and secrets stay declarative (the target's own, sent whole). |
| IV. Fail Loudly | PASS. Every refusal names its field (`to`, `services.<name>`, `id`, `name`); a dropped Compose file on promotion is a warning in the preview and the deploy result. |
| V. API-First | PASS. 13 operations in `api/openapi.yaml` 0.16.0 in the same commits; parity table in contracts/api.md; existing paths unchanged. |
| VI. Nomad-Native | PASS. One namespace; router name stays `<app>-<service>` with the app ID, so no amendment (R2). Export unchanged. |
| VII. Secure by Default | PASS. Copies never carry secret values, webhook tokens or backups; a promotion never moves secret values between environments (an added service with a secret the target lacks is refused). |
| VIII. Plain, Tested Go | PASS. Pure place parsing; table tests; lab steps. |

No violations. Re-checked after design: no change.

## Project Structure

```text
internal/app/place.go (+_test)                 Place, PlaceOf, ID, ValidName
internal/store/migrations/0011_projects.sql
internal/store/projects.go (+_test)            projects, environments, ErrNotEmpty
internal/store/apps.go                         place columns, ListAppsIn, AppRecord.Place
internal/store/deployments.go                  SourceApp
internal/service/projects.go (+_test)          CRUD, checkPlace (every create path)
internal/service/promote.go (+_test)           PreviewPromotion, Promote, CopyApp
internal/service/{apps,deploy,compose,attention,notifier}.go   place on creates, items, events
internal/notify/notify.go                      Event.Project/Environment/AppName/PromotedFrom
internal/api/projects.go, promote.go (+_test)  the new operations; place fields on existing ones
api/openapi.yaml                               0.16.0
internal/ui/projects.go, promote.go (+_test)   pages; templates projects.html, promote.html
internal/ui/{apps,newapp,composepage,app}.go + templates   grouping, place select, breadcrumb
cmd/caravana/gate_test.go, console_lab_test.go projects-promote, console-projects
```

## Design notes

- **Create paths** (R7): `Service.checkPlace(ctx, id)` runs before `InsertApp` in `CreateApp`,
  and before the save in `saveAndDeploy` when the app doesn't exist yet (Deploy, DeployIf,
  Compose), and in `CopyApp`. It refuses a `--` ID that `PlaceOf` rejects (422 `invalid_field`,
  `id`) and a missing project or environment (404 `environment_not_found`/`project_not_found`).
  Existing apps skip it, so a grandfathered `a--b` app keeps deploying.
- **Deletes**: `DeleteApp` is unchanged; `DeleteEnvironment`/`DeleteProject` are refused while
  apps remain and for `default`/`production` (409 `protected`).
- **Promotion** (R4): build the target from its stored app plus the source deployment's recorded
  definition; `Changed` per service compares command, args, ports, health and volumes; then
  `CheckDigests(source digests)` and `saveAndDeploy` with `pinned{digests, source: depID}` and
  the caller's `Condition` (If-Match of the target). The pinned digests map is keyed as the
  source deployment recorded it; services kept from the target that the source also has use the
  source's digest (all target services exist in the source, by the refusal rule).
- **Copy** (R5): `InsertApp(target, file)` with domains (and overlay domains) removed; answer the
  secrets that have no value in the target (all of them: a new app has none).
- **Events**: `notifyDeploy` sets `PromotedFrom` (source environment) when `SourceApp` differs;
  titles keep the app ID (it already names project and environment).
- **Console grouping** reads `ListProjects` once and `AppSummaries` once, groups in Go.

## Complexity Tracking

None.
