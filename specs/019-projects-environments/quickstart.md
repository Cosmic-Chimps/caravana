# Quickstart: Projects and Environments (019)

Validation scenarios; see [contracts/api.md](./contracts/api.md) for the shapes.

## Unit and store level

```bash
go test ./internal/app -run 'TestPlace' -v           # PlaceOf / ID round trip, refusals
go test ./internal/store -run 'Projects|Environments|Place|Migrat' -v   # 0011 on an existing DB
go test ./internal/service -run 'Project|Environment|Promot|Copy' -v
go test ./internal/api ./internal/ui
```

The upgrade test opens a store at migration 0010 holding two apps (one with a webhook token, a
backup row and a deployment), applies 0011, and finds both in default/production with unchanged
IDs, revisions, tokens and history.

## Lab (`TestLab`)

1. Gate step `projects-promote` (API only, `deploy/dev` lab):
   - create project `lab019` and environment `staging`;
   - deploy `lab019--staging--web` with `deployApp` (whoami `v1.10`, `FOO=staging`, domain
     `lab019-staging.localhost`) — the ID places it;
   - `copyApp` to `production`: undeployed, no domains; give it `FOO=production`, a secret and
     domain `lab019.localhost`, deploy;
   - deploy staging again with whoami `v1.11`, then `previewPromotion` (image change listed) and
     `promoteApp`: production's live digest equals staging's, `FOO` is still `production`, its
     domain answers 200 over HTTPS and shows `v1.11`, the deployment has `promotedFrom`;
   - a staging deploy with an extra service is promoted: added; then production gains a service
     staging lacks: 409 `promotion_removes_service`;
   - `deleteProject` is 409 `not_empty`; delete the apps, the environment, the project.
2. Console step `console-projects`: New project form, add environment, New app in it, Copy to
   environment, Promote (preview, confirm), the breadcrumb and the grouped home; clean up.

## On the maintainer's host (after the release)

Upgrade: the home looks as before; `GET /v1/projects` lists Default with production holding
every app; no Nomad job has a new version (`caravana-nomad job history` unchanged); an old
webhook URL still redeploys.
