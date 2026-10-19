# Implementation Plan: Deploy from CI (caravana-action)

**Branch**: `020-deploy-from-ci` (stacked on `020.5-app-actions`) and the repository
`Cosmic-Chimps/caravana-action` (local clone `../caravana-action`) | **Date**: 2026-10-18 |
**Spec**: [deploy-from-ci.spec.md](./deploy-from-ci.spec.md)

## Summary

Two pieces, one contract ([contracts/api.md](./contracts/api.md)):

- **Caravana** (API 0.19.0): deploy tokens scoped to one app name in one project (an allowlist of
  operations checked in the API middleware, 403 `token_scope` otherwise), the origin of a deploy
  from `Caravana-Origin-*` headers recorded on the deployment and shown in history and
  notifications, and New app › "From a GitHub repository" (create without deploying, a deploy
  token, the workflow to paste).
- **caravana-action**: a composite GitHub Action (`action.yml` + `caravana-deploy.sh`, bash with
  curl and jq, both on GitHub's runners) that deploys, waits, reports and promotes, with its own
  tests (a stdlib-Python fake Caravana) and CI (shellcheck, actionlint, the tests).

## Technical Context

Caravana: Go 1.27, no new module; migration `0013_deploy_from_ci.sql`. The action: bash 5,
curl, jq; tests in bash against `test/fake_caravana.py` (Python 3 standard library only).

## Constitution Check

| Principle | Assessment |
| --- | --- |
| I. Small | PASS. Nothing new runs on the host; builds stay in CI. |
| III. Safe Deploys | PASS. The action deploys a digest or a tag Caravana pins; everything is a normal deploy. |
| IV. Fail Loudly | PASS. Bad origin headers are 422 naming them; out-of-scope is 403 naming the scope; the action fails the step with Caravana's message. |
| V. API-First | PASS. Scoped tokens and origin through the API and the console alike; openapi 0.19.0. The action uses only the public API. |
| VII. Secure | PASS. A deploy token can't leave its app; tokens are SHA-256 hashed and shown once as today; the action masks the token and passes it to curl on stdin. |
| Originality | PASS. Our own action and wording. |

## Design notes

- **Scope check** (`internal/api/scope.go`): the middleware puts the token's scope in the request
  context; each route's entry in the route table (the same one the openapi sync test reads) says
  whether a scoped token may use it and how to find the app (path `{id}`, path project+name, or the
  body's `id`/`appName`, read and restored). Default deny: a new route is out of scope unless it
  says otherwise. A test walks every route with a scoped token and expects 403 for every route
  outside the contract's table.
- **Origin** (`app.Origin` in `internal/app`, parsed and validated in `internal/api/origin.go`) is
  passed to the service with the trigger (`DeployOptions` or a field on the existing condition);
  stored as JSON; `notify.Event` gains `Origin`.
- **New app tab** `?from=github`: name, place, image (`ghcr.io/<owner>/<repo>`), port, domain,
  health path → `CreateAppOnly` with that definition (the image need not exist yet), a deploy token
  named `<app>-ci`, then a page with the token (once), the workflow (`docker/build-push-action`
  then `Cosmic-Chimps/caravana-action@v1` with `image: ghcr.io/...@${{ steps.build.outputs.digest
  }}`), and the secret to add (`CARAVANA_TOKEN`). The page is answered to the POST (200,
  `no-store`), like the token page.
- **The action** follows contracts/api.md "What the action calls". Inputs: `url`, `token`, `app`
  or `project`+`environment`+`name`, `image`, `service`, `definition`, `wait` (true), `timeout`
  (600 s), `promote`, `fail-on-superseded` (false). Outputs: `app-id`, `deployment-id`,
  `job-version`, `status`, and `promotion-id`/`promotion-status` when promoting. `::add-mask::`
  the token; curl reads it from stdin (`--config -`); `set -euo pipefail`; every error is a
  `::error::` line naming the input or Caravana's `{code}: {message}`. Job summary table.
  README with usage, inputs, outputs, examples (build and deploy; staging then promote with a
  GitHub environment), and "needs Caravana 0.9.0 or later" (the release carrying API 0.19).
- **License of the action**: MIT, so any repository can use it without AGPL questions (it is a
  client of the API, not Caravana).

## Lab

Gate step `deploy-from-ci`: create a deploy token scoped to `labci/web`; with it, run the action's
script (`$CARAVANA_ACTION_DIR/caravana-deploy.sh`, default `../caravana-action`, the step skips
with a note if it isn't there) with GitHub-like env: first run creates and deploys from a
definition file, second deploys a new image, third promotes to production; history shows origin;
the scoped token gets 403 on `/v1/apps`, `/v1/tokens` and another app.

## Complexity Tracking

None.
