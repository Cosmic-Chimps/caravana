# Tasks: Create an app without deploying it (020)

- [X] T001 [US1][US2] Service: `app.App.Defined`, `CreateAppOnly`, `ErrAppNotDefined` in `Redeploy` /
  `RedeployVersion` (and so the webhook), `CopyApp` of an undefined app, attention test, `Defined`
  on summaries. Table tests on the fakes, including: a defined never-deployed app's first webhook
  deploys it; an undefined app's webhook is refused and records nothing; a deploy with a body
  defines it; secret values refused on create-only.
- [X] T002 API 0.17.0: `?deploy=false` on `createApp` and `createEnvironmentApp`, 409
  `app_not_defined` (deployApp empty body, redeployDeployment, webhook), `defined` on AppRecord and
  AppSummary; tests; openapi sync.
- [X] T003 Console: New app's two buttons and optional image; app page states and `?created=1`
  notice; Configure's "Define the app" form; list status; handler tests; parity test;
  `tools/ui-css.sh build`.
- [X] T004 Lab step `console-create-only` (plan.md "Lab").
- [X] T005 Docs: CHANGELOG Unreleased (Added), CLAUDE.md 020 paragraph and phase list,
  `docs/plan.md`: a 020 row (done) and build from source renumbered 021 (Aspire 022, Bella 023;
  also in CLAUDE.md and docs/vision.md where numbered).
- [ ] T006 Full check and full `TestLab` (main session).
