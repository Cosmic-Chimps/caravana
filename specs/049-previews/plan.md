# Plan 049: Previews

Spec: `spec.md`. Task `rg2u`. Branch: the worktree branch `alta/dreamy-glow-f7vh`. Contracts: `contracts/api.md`
(Caravana) and `contracts/action.md` (caravana-action, the sibling repository `../caravana-action`).

Numbers: 047 (Requests, API 0.52.0) and 048 (DNS providers, API 0.53.0, migration 0037) are on unmerged branches,
so this feature is 049, API **0.54.0**, migration **0038**. Expect a clash on the version line only when they merge.

## Constitution check

- I Small by default: no new module, no runtime dependency. bcrypt is already a direct module (023).
- II One model: no new field on `app.App`. A preview is an environment; its apps are ordinary apps.
- III Safe deploys: unchanged. A preview deploy is a normal deploy (digest pinned, health checked, auto-revert).
- IV Fail loudly: every refusal names its field; turning `teamOnly` on in a preview is a warning (D6).
- V API-first: every operation is in `api/openapi.yaml` 0.54.0 in the same change; the console calls the same
  service methods. caravana-action uses only the public API.
- VI Nomad-native: nothing new talks to Nomad; closing a preview is `DeleteApp` per app.
- VII Secure: the preview password's bcrypt hash lives in SQLite (a hash is not a secret, constitution VII); the
  password itself is never stored, logged or answered. Deploy tokens stay default-deny; the new rules are narrow.
- VIII Plain, tested Go: table tests for the name rule, the scope rules, the expiry and the cap; a lab gate step.

## Decisions

- **D1 A preview is an environment named `pr-<n>`.** `app.PreviewNumber(name) (int, bool)` is the one rule
  (`^pr-[1-9][0-9]{0,8}$`). `app.ValidEnvironmentName` (or the service's `CreateEnvironment`) refuses a name that
  starts with `pr-` and isn't a preview name, on `name`. No column says "preview": the name does. Environments
  created before this feature with such a name become previews (counted, expiring); none is expected.
- **D2 Expiry is computed, not stored.** expiresAt = max(environment.created_at, newest `deployments.created_at` of
  an app in it) + the project's `expireAfterDays`. One store query (`PreviewActivity(project)` returning, per preview
  environment, its created time and its newest deployment time). Changing `expireAfterDays` applies to open previews
  at once. No column to bump on deploy, so no deploy path changes for it.
- **D3 The reaper.** `service/previews.go` `runPreviewReaper`, started in `Service.Start` with the other watchers,
  every 5 minutes (`SetPreviewReapGap`; the first pass one gap after start). For each project and each expired
  preview, `ClosePreview(ctx, project, environment, trigger)`: every app in it by `DeleteApp` (sorted by ID), then
  `DeleteEnvironment`. A failure is logged with the app or environment and the pass goes on; the next pass tries
  again. A test hook `SetPreviewExpiryForTests(d time.Duration)` replaces `expireAfterDays` (the lab can't wait a
  day), as `SetScheduleLimitForTests` does.
- **D4 The limit.** `projects.preview_limit` (default 5, 0–20). `store.InsertEnvironment` gains a variant
  `InsertPreviewEnvironment(ctx, project, name, limit)` that counts the project's preview environments and inserts
  in one transaction, returning a `*PreviewLimitError{Project, Limit, Open []string}` (wrapping
  `ErrPreviewLimit`). `CreateEnvironment` uses it for a preview name, whatever the token.
- **D5 Deploy-token rules.** `scopeRules` changes from "find the app" to a rule that gets the request and the scope
  (`func(*http.Request, service.TokenScope) bool`); the existing entries wrap their finders unchanged (project and
  name must equal the scope's). New entries, each also in `allowedForDeployTokens`:
  - `POST /v1/projects/{project}/environments`: path project is the scope's and the body's `name` is a preview name.
  - `DELETE /v1/projects/{project}/environments/{environment}`: path project is the scope's and the environment is a
    preview name.
  - `DELETE /v1/apps/{id}`: the ID's project and name are the scope's and its environment is a preview name.

  `TestScopedTokenWalksEveryRoute` learns the new shapes: for these three, the token's own app or project in `pr-12`
  is allowed, and `staging`, another project and another app are 403. `closePreview`, `getPreviews` and
  `putPreviewSettings` stay denied.
- **D6 The preview password is Team only with one shared hash.** `projects.preview_password_hash` (bcrypt, cost 10,
  `deploy.TeamPasswordHash`). In the deploy path, after validation and before `ensureTeamPasswords`, an app whose
  environment is a preview of a project with a hash gets `teamOnly: true` on every domain (`previewTeamOnly`, pure,
  returning the first field it changed for the warning); the stored app and the export then say what runs.
  `ensureTeamPasswords` writes the project's hash as the row of such a host instead of making a password, and
  returns nothing for it. `SetPreviewSettings` with a new password rewrites the rows of every Team-only host of every
  app in the project's preview environments with the new hash and syncs the variable (`syncTeamPasswordsLocked`,
  under `teamMu`); a failure to sync puts the old hash back. `pruneTeamPasswords` is unchanged (rows go with their
  apps). `RotateTeamPassword` on a preview host of a protected project is 409 `preview_password` ("this domain uses
  project blog's preview password; change it in the project's preview settings").
- **D7 Closing a preview.** `ClosePreview` is the reaper's and `DELETE /v1/projects/{project}/previews/{environment}`
  (full tokens and the console; 204). An environment that isn't a preview is `ErrNotAPreview` (409 `not_a_preview`).
  It holds no lock of its own: each `DeleteApp` takes its app's lock, and an app created in the environment while it
  runs makes the final `DeleteEnvironment` answer `not_empty` (the next pass, or the caller, tries again).
- **D8 The list.** `Service.Previews(ctx, project) (PreviewList, error)`: settings (`limit`, `expireAfterDays`,
  `passwordSet`, `open` count) and the open previews sorted by pull request number, each with its apps: ID, name,
  status state (the summary's), addresses (domain hosts), reserved memory (sum over services of the memory
  reservation, default applied, times the wanted count), and the newest deployment (ID, status, created, trigger,
  token name, origin). `Environment` in the project JSON gains `preview: {number, expiresAt}` for preview
  environments.
- **D9 Migration 0038** `0038_previews.sql`: `ALTER TABLE projects ADD preview_limit INTEGER NOT NULL DEFAULT 5`,
  `preview_expire_days INTEGER NOT NULL DEFAULT 7`, `preview_password_hash TEXT` (NULL = none). Nothing else.
- **D10 The action.** caravana-action gains `preview: true` (contract `contracts/action.md`). It copies the source
  environment's app definition (default `staging`), puts the image in, rewrites domains from a template, drops what a
  copy must not carry (backup policy, published numbers), takes plain secret values from a `preview-secrets` input,
  deploys with the existing wait, and keeps one pull request comment. Its test is `test/run.sh` against
  `test/fake_caravana.py`, extended.
- **D11 Console, first cut.** The project page (`/projects/{project}`) gains a Previews section: the settings form
  (limit, expire after, password set/change/remove; works without JavaScript) and, on each preview environment's
  row, "preview · expires <clock>" and Close preview (a confirmation post). The handoff's Previews page goes through
  the designer cycle as its own follow-up task.
- **D12 Docs.** `docs/previews.md` (the workflow, DNS: one wildcard `*` record by hand, proxied or not; Let's
  Encrypt's 50 certificates a week per registered domain; the password; the limit and expiry; forks get no
  preview), `docs/plan.md` moves Previews out of "needs the build server", a `changelog.d/049-previews.md`, and the
  CLAUDE.md "Where we are" paragraph.

## Lab

Gate step `previews` (`cmd/caravana/gate_previews_test.go`), after `deploy-from-ci`: project `lab049` with
`staging`, a deploy token for `lab049`/`web`; staging deployed (busybox httpd or whoami, with a domain);
the token creates `pr-7` (and is refused `qa`, `pr-07`, another project); limit set to 1 makes `pr-8` 409
`preview_limit`; a preview password set; the token deploys `web` into `pr-7` with domain `pr-7-web.lab049.test`,
healthy, the warning present, the domain answers 401 without and 200 with `team:<password>` (TLS server name, as
`projects-promote` does); the password changed → the old one 401, the new one 200, with no deploy; the token deletes
its staging app → 403, its `pr-7` app → 204, the environment → 204; then a second preview `pr-9` left alone with
`SetPreviewExpiryForTests(30s)` and `SetPreviewReapGap(10s)` is closed by the reaper (app and job gone); removing
the password with a preview open is 409. When `../caravana-action` has the preview mode, the step also runs
`caravana-deploy.sh` with `preview: true` and a fake `opened` then `closed` event file (no GitHub token, so the
comment is skipped with a notice).
