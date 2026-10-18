# Implementation Plan: Create an app without deploying it

**Branch**: `019.5-create-without-deploy` (stacked on `019-projects-environments`) | **Date**: 2026-10-18 |
**Spec**: [create-without-deploy.spec.md](./create-without-deploy.spec.md)

## Summary

A new service call saves an app without deploying it, with a definition or with none (zero
services). Redeploys of an app with no services are refused with `ErrAppNotDefined` (409
`app_not_defined`); any deploy that sends a definition works as for a new app. `createApp` and
`createEnvironmentApp` take `?deploy=false` (API 0.17.0). New app gets a second button, and app
pages explain the first deploy. No migration, no Nomad change, `nomadjob` untouched.

## Technical Context

Go 1.27, no new module; SQLite unchanged (an app document with `"services": []` is valid JSON for
the store); tests: service and API unit tests on the fakes, UI handler tests, one console lab step.

## Constitution Check

| Principle | Assessment |
| --- | --- |
| II. One Model | PASS. An app with zero services is only ever stored; `Translate` never sees one (every deploy path refuses or replaces it first). |
| III. Safe Deploys | PASS. Nothing deploys without a definition; the first deploy is a normal one. |
| IV. Fail Loudly | PASS. `app_not_defined` names what is missing; secret values on a create-only are refused naming the field, never dropped. |
| V. API-First | PASS. `?deploy=false` on both create operations, the new code, `defined` on AppRecord/AppSummary, in openapi 0.17.0 with the console in the same commits. |
| VII. Secure | PASS. The webhook still can't choose an image. |

## Design

### Service (`internal/service/apps.go`, `deploy.go`, `webhook.go`, `promote.go`)

- `CreateAppOnly(ctx, a app.App) (store.AppRecord, error)`:
  - `checkPlace(a.ID)`;
  - zero services: check the ID with `app.PlaceOf` (or `ValidName` for a bare one) and store
    `App{ID, Name, Source{Kind: image}, Services: []}`;
  - otherwise `deployer.Validate(a)`;
  - a secret with a value → `*app.FieldError` on `services[i].secrets[j].value` ("secret values
    are sent with a deploy");
  - `InsertApp(a.WithoutSecretValues(), nil)` (`ErrAppExists` as `CreateApp`); `appChanged`.
- `app.App.Defined() bool` = `len(Services) > 0` (a method in `internal/app`; no field added).
- `ErrAppNotDefined`: returned before `Validate` by `Redeploy` (so the webhook too) and by
  `RedeployVersion` when the stored app has no services. `WebhookRedeploy` returns it unchanged.
- `CopyApp` of an undefined app copies the name only (skip `Validate` when no services).
- Attention: an app with no deployments yields nothing (verify, add a test).
- Summaries/records expose `Defined`.

### API (0.17.0)

- `createApp` and `createEnvironmentApp` take `deploy` (query, boolean, default true). With
  `deploy=false`: 201 `AppRecord`, `Location: /v1/apps/{id}`, `ETag`. The body may then omit
  `services` (or send `[]`). With deploy (default) the body still needs services (existing
  error).
- 409 `app_not_defined` on `deployApp` with an empty body, `redeployDeployment`, and the webhook
  call (the webhook-only listener included).
- `AppRecord` and `AppSummary` gain `defined` (boolean).

### Console

- New app, Image tab: Image becomes optional. Two buttons, `action=deploy` ("Create and deploy",
  primary) and `action=create` ("Create without deploying", secondary). `deploy` without an image
  → 422, field `image`: "Add an image to deploy now, or create the app without deploying."
  `create` → `CreateAppOnly` with whatever was filled (name only → no services), 303 to the app's
  page with `?created=1`.
- App page (Running tab and header) for an app with no deployments:
  - defined: "Waiting for its first deploy." with Deploy now, "Create a webhook token" (link to
    Configure's webhook section), and the API call `POST /v1/apps/<id>/deployments`;
  - not defined: "No definition yet." with Configure, Deploy from Compose, and the API call with a
    body. Deploy now is not offered.
  - `?created=1` adds the notice "Created <name>. Nothing is deployed yet."
- Configure for an undefined app: a "Define the app" form with the New app fields (image,
  port, domain, health path) that saves and deploys with the revision (`DeployIf`), above the
  existing Deploy from Compose link, webhook and delete sections.
- The app list shows "Not defined yet" / "Never deployed" as the status for such apps.

### Lab

Console step `console-create-only`: name-only create (no Nomad job; webhook 409
`app_not_defined`); define it through Configure's form → healthy; create a second app with an
image and port without deploying (no job), create its webhook token, call the webhook → one
deployment, trigger `webhook`, healthy; clean up.

## Complexity Tracking

None.
