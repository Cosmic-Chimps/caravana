# Tasks: Deploy from CI (020)

## Caravana (this repository)
- [X] T001 Migration 0013, store: token scope columns, `deployments.origin`; tests.
- [X] T002 Service/auth: scoped tokens (create, list, check returns the scope), `app.Origin` and its
  recording on deploys, promote and the webhook; notify event origin; tests.
- [X] T003 API 0.19.0: `scope` on createApiToken/listApiTokens, the scope middleware with the
  allowlist and a default-deny test over every route, origin headers (422 on bad ones),
  `Deployment.origin`; openapi sync.
- [X] T004 Console: Settings › API tokens "Only for one app", Configure's "Create a deploy token for
  this app", the history's origin line, New app › "From a GitHub repository" (create, token, the
  workflow page); handler tests; parity; `tools/ui-css.sh build`.
- [X] T005 Lab: gate step `deploy-from-ci` (plan.md "Lab").
- [X] T006 Docs: CHANGELOG Unreleased, CLAUDE.md 020 paragraph, docs/plan.md 020 row Done.
- [X] T007 Full check and full TestLab (main session). Passed 2026-10-18 (1589 s, 41 steps; `deploy-from-ci` 48 s, the real script).

## caravana-action (`../caravana-action`)
- [X] A001 `action.yml`, `caravana-deploy.sh`, `LICENSE` (MIT), `.gitignore`.
- [X] A002 `test/fake_caravana.py` and `test/run.sh`: every path of contracts/api.md "What the
  action calls", the failures, the masking (the token never in output), outputs.
- [X] A003 `.github/workflows/ci.yml` (shellcheck, actionlint, the tests) and `README.md`.
- [X] A004 Run it against the lab's Caravana (main session, with T005).

caravana-action commits (local, `../caravana-action`, main): f0754c1, 883cda4, b45b214, 05a2d87
(promote without deploying first), 78a8abb (If-Match quoted: found by running the real script
against the API). Its tests: 1000 checks, shellcheck and actionlint clean.
