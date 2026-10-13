# Tasks: Console v2

**Input**: [plan.md](./plan.md), [console-v2.spec.md](./console-v2.spec.md),
[research.md](./research.md), [data-model.md](./data-model.md), [contracts/](./contracts/),
[quickstart.md](./quickstart.md)

**Scale note**: 28 tasks over about 35 files. The stories are independent in what they do but
share four files: `api/openapi.yaml`, `internal/api/parity_test.go`, the console's route table
(`internal/ui/ui.go`) and the generated `internal/ui/static/app.css`. Run the story phases one
after another in the order below. Inside a phase, tasks marked [P] touch different files and can
run side by side. The two risky tasks are T005 (the merge must keep every field the form doesn't
show) and T018 (secret values must never come back in a page).

Tests are required (constitution VIII). Each task writes its own tests next to the code
(table-driven, `testing` only) and leaves `go build ./...`, `go vet ./...`, `gofmt -l .` and
`go test ./<its packages>` passing. A task that changes templates or `internal/ui/style/` runs
`tools/ui-css.sh build` and commits the regenerated `app.css`. A task that adds or changes an
API route changes `api/openapi.yaml` in the same commit (the contract test checks it).

## Phase 1: Setup

No new module, no new tool, no migration.

## Phase 2: Foundational (blocks US1, US2, US3)

Files: `internal/store/apps.go`, `internal/store/apps_test.go`, `internal/service/deploy.go`,
`internal/service/compose.go`, `internal/service/deploy_test.go`, `internal/service/compose_test.go`,
`internal/api/apps.go`, `internal/api/deployments.go`, `internal/api/compose.go`,
`internal/api/api.go`, `internal/api/errors.go`, `internal/api/apps_test.go`,
`internal/api/deployments_test.go`, `internal/api/compose_test.go`, `api/openapi.yaml`

- [ ] T001 Store revision (research R1, data-model "App revision"): add `Revision string` to `store.AppRecord`, filled by every reader (`GetApp`, `ListApps`, `InsertApp`, `SaveApp`) as the first 16 lowercase hex digits of SHA-256 over the stored `document` bytes. Add `store.ErrRevisionMismatch` and `SaveAppIf(ctx, a app.App, revision string) (AppRecord, error)`: in one `BEGIN IMMEDIATE` transaction, read the row (`ErrNotFound` if none), compare revisions (`ErrRevisionMismatch`), then save as `SaveApp` does (an identical document leaves `UpdatedAt` alone). Tests: same document gives the same revision, a change gives a new one, matching save succeeds, stale save refused and nothing written, two concurrent `SaveAppIf` from the same revision → exactly one wins, missing app → `ErrNotFound` · internal/store/apps.go, internal/store/apps_test.go

**⟶ Wait for T001, then:**

- [ ] T002 Service conditions (research R3, data-model "Condition"): add `type Condition struct { IfRevision string; IfNew bool }` and `var ErrAppChanged` (wrapping `store.ErrRevisionMismatch`) in internal/service/deploy.go. Give `saveAndDeploy` a `cond Condition` parameter: both set → error; `IfNew` saves with `store.InsertApp` (taken → `fmt.Errorf("app %q: %w", id, ErrAppExists)`); `IfRevision` saves with `SaveAppIf` (mismatch → `ErrAppChanged`); zero → `SaveApp` as today. `Validate`, `checkSecrets` and `checkBackups` run before any save on every path. Add `DeployIf(ctx, a, cond, trigger)` in deploy.go and `DeployComposeIf(ctx, appID, req, cond, trigger)` in compose.go (with `compose.InFile` on its error, as `DeployCompose`). `Deploy`, `DeployCompose`, `Redeploy`, `RedeployVersion`, secret edits and `CreateApp` keep their behaviour (zero condition). Tests: each condition's success and refusal, nothing saved or registered on refusal, refusals before secret-store writes, existing deploy tests unchanged · internal/service/deploy.go, internal/service/compose.go, internal/service/deploy_test.go, internal/service/compose_test.go

**⟶ Wait for T002, then:**

- [ ] T003 API conditional deploys (contracts/api.md "Conditional deploys"): `getApp` sets `ETag: "<revision>"` and adds `revision` to the `AppRecord` body. `deployApp` reads an optional `If-Match` (only with a body); `deployCompose` reads `If-Match` or `If-None-Match: *`. Parse into `service.Condition`: weak `W/"x"` accepted as `x`, an unquoted value, a list, both headers, or `If-None-Match` other than `*` → 400 `invalid_field` with `field` the header name. Call `DeployIf` / `DeployComposeIf`. Map `service.ErrAppChanged` → 412 `app_changed` (new code in errors.go, message naming the current revision) in `mapError`; `ErrAppExists` stays 409 `app_exists` field `id`. `api/openapi.yaml` 0.10.0 → 0.11.0: the headers as parameters, `revision`, the `ETag` response header, 412 responses, the `app_changed` code. Tests: each header case, 412 deploys nothing, 409 on taken ID, no headers behave as before · internal/api/apps.go, internal/api/deployments.go, internal/api/compose.go, internal/api/api.go, internal/api/errors.go, internal/api/apps_test.go, internal/api/deployments_test.go, internal/api/compose_test.go, api/openapi.yaml

**Checkpoint**: an API client can GET an app, change it and deploy it with `If-Match`, and create a Compose app with `If-None-Match: *`.

## Phase 3: User Story 1 - Change an app from its Configure tab (P1) 🎯 MVP

**Goal**: the Configure tab edits each service and saves + deploys in one action, refusing stale saves.

**Independent test**: quickstart §1. On whoami, change the image tag, add an env var and set memory from Configure → one healthy deployment carrying all three, nothing else changed; a second tab's save is refused.

Files: `internal/ui/edit.go`, `internal/ui/edit_test.go`, `internal/ui/merge.go`,
`internal/ui/merge_test.go`, `internal/ui/configure.go`,
`internal/ui/configure_test.go`, `internal/ui/app.go`, `internal/ui/ui.go`,
`internal/ui/templates/app.html`, `internal/ui/style/`, `internal/ui/static/app.css`,
`internal/api/parity_test.go`, `cmd/caravana/console_lab_test.go`

- [ ] T004 [P] [US1] Edit form parsing (research R4, data-model "Edit form"): in internal/ui/edit.go define `editForm` (per service, keyed by service name: image, env, port, domains, copies, healthPath, healthInterval, healthTimeout, cpu, memory, memoryMax, each a `formField` like newapp.go's) plus `revision`. `parseEditForm(r.PostForm, stored app.App) (editForm, bool)` reads the inputs named `svc.<name>.<field>` for each stored service; env is `NAME=value` per line (`#` and blank lines skipped; a line without `=` or a bad name → error at `.env` naming the line number); domains one host per line; numbers as whole numbers (seconds for interval/timeout, MHz, MB); empty numeric fields mean "default" (0 / nil). `formOf(stored app.App, revision string) editForm` fills the form from a stored app (the inverse; env in stored order; port shown only when the service has exactly one port; health shown only when the service has ports). Tests: round trip `formOf` → post values → `parseEditForm` gives the stored values for every `apptest.Cases()` app; each parse error lands on its field · internal/ui/edit.go, internal/ui/edit_test.go

- [ ] T005 [P] [US1] The merge and error mapping (research R4, data-model "Edit form", SC-005): in internal/ui/merge.go add the pure `applyEdit(stored app.App, f editForm) app.App`, which copies `stored` and, per service by name, replaces only `Image`, `Env`, `Ports[0].ContainerPort` (single-port services), `Domains` (each on that service's domain port name, or the only port's name), `Replicas`, `Health` (path/interval/timeout; port kept), `Resources` (CPUMHz, MemoryMB, MemoryMaxMB). It keeps `Name`, `Source`, the service list and order, `Command`, `Args`, `Secrets` (names only), `Volumes`, `Backup`, other ports, and the health check's `Port`. Add `fieldFor(path string, a app.App) (formFieldID string, ok bool)` mapping model paths (`services[i].image`, `services[i].env…`, `services[i].ports…`, `services[i].domains…`, `services[i].replicas`, `services[i].health.path|interval|timeout`, `services[i].resources.cpuMHz|memoryMB|memoryMaxMB`) to `svc.<name>.<field>`. Tests: for every `apptest.Cases()` app, `applyEdit(a, formOf(a))` equals `a` (deep equal, after `WithoutSecretValues`); a changed field changes only that field; volumes, backup, secrets, command and args survive any edit; every mapped path and an unmapped one (the test uses T004's `formOf`; until it lands, build forms by hand) · internal/ui/merge.go, internal/ui/merge_test.go

**⟶ Wait for T004 and T005, then:**

- [ ] T006 [US1] Configure becomes a form (contracts/console.md, research R4, R12): `GET /apps/{id}/configure` renders one panel per service with the inputs from `formOf` (labels as DESIGN.md: "Image", "Environment", "Container port", "Domains", "Copies", "Health check", "CPU (MHz)", "Memory reserved (MB)", "Memory ceiling (MB)", defaults shown as placeholders, each service's memory input with `id="svc-<name>-memory"` for the needs-you link), the hidden `revision`, and one "Save and deploy" button. A Compose-made app shows above the form: "Made from a Compose file. Deploying a file again replaces these edits." Secrets, webhook, export and delete sections stay below, unchanged. New route `POST /apps/{id}/configure` (`s.protected(s.requireSession(…))`, `MaxBytesReader` 256 KiB): parse, `applyEdit` on the stored app, `svc.DeployIf(ctx, a, service.Condition{IfRevision: revision}, app.TriggerManual)`. Success → 303 `/apps/{id}/deployments#<deployment>`. Parse or `*app.FieldError` → 422 with every typed value, the message at `fieldFor`'s input (`aria-invalid`, `aria-describedby`, the first error focused), unmapped paths in the form notice naming the path. `ErrAppChanged` → 409 with the form as typed, a panel "This app changed since you opened the form." listing the fields whose stored value differs from what the form was drawn from, and the fresh revision in the hidden input. `*service.DeployError` (e.g. image not found) → the same page with the message at the image field of that service when the error names it, else the notice. Nomad unavailable → the form, notice "Nomad can't be reached; nothing was deployed." Register the route in `routeTable`. Rebuild app.css. Tests (httptest + deploytest): render, successful save (303, stored app changed, volume/backup/secrets kept), 422 with values kept, 409 stale panel, no-JS post works, htmx-boosted post works · internal/ui/configure.go, internal/ui/configure_test.go, internal/ui/app.go, internal/ui/ui.go, internal/ui/templates/app.html, internal/ui/style/, internal/ui/static/app.css

**⟶ Wait for T006, then:**

- [ ] T007 [P] [US1] Parity: `"POST /apps/{id}/configure": {ops: []string{"getApp", "deployApp"}}` · internal/api/parity_test.go
- [ ] T008 [P] [US1] Lab step in `TestConsoleLab` (research R13): through the console's HTTP forms, edit a lab app's env and memory, follow the deployment to healthy, check Nomad's job has the new env and memory and the stored app kept its volume; a second post with the old revision gets 409 and registers nothing · cmd/caravana/console_lab_test.go

**Checkpoint**: US1 works end to end; this alone is shippable.

## Phase 4: User Story 5 - A "needs you" list on the Apps home (P2)

**Goal**: the home opens with what only the owner can decide, each with one action, gone when resolved.

**Independent test**: quickstart §5. A deploy with a bad health path is reverted → an item within 30 s linking to the deployment; a healthy deploy → the item is gone; `GET /v1/attention` lists the same.

Files: `internal/deploy/copies.go`, `internal/deploy/copies_test.go`,
`internal/deploy/deploytest/nomad.go`, `internal/store/backups.go`,
`internal/store/backups_test.go`, `internal/service/attention.go`,
`internal/service/attention_test.go`, `internal/service/changes.go`, `internal/service/backups.go`,
`internal/service/apps.go`, `internal/api/attention.go`, `internal/api/attention_test.go`,
`internal/api/copies.go`, `internal/api/copies_test.go`, `internal/api/api.go`, `api/openapi.yaml`,
`internal/ui/attention.go`, `internal/ui/attention_test.go`, `internal/ui/apps.go`,
`internal/ui/view.go`, `internal/ui/app.go`, `internal/ui/ui.go`, `internal/ui/templates/apps.html`,
`internal/ui/templates/parts.html`, `internal/ui/templates/app.html`, `internal/ui/style/`,
`internal/ui/static/app.css`, `internal/api/parity_test.go`, `cmd/caravana/console_lab_test.go`

**Wave 1 — independent (different files):**

- [ ] T009 [P] [US5] Copy facts (research R8, data-model "Copy"): `deploy.Copy` gains `LastRestart *time.Time` (newest non-zero `TaskState.LastRestart` over the stub's tasks) and `OutOfMemory bool` (the task named like the service, `a.TaskGroup`: its newest event of type `api.TaskTerminated` has `Details["oom_killed"] == "true"`), both set in `copyOf`. deploytest: stubs given to `SetCopies` keep their `TaskStates` (check, add if dropped). Tests in `TestCopyOf`: restarts with times, an OOM terminated event, a non-OOM one, the `names` sidecar's events ignored · internal/deploy/copies.go, internal/deploy/copies_test.go, internal/deploy/deploytest/nomad.go
- [ ] T010 [P] [US5] `store.LatestBackupRuns(ctx) ([]BackupRecord, []RestoreRecord, error)`: per (app, service), the newest backup and the newest restore by `started_at` (ties by id), one query each (`ROW_NUMBER() OVER (PARTITION BY app_id, service ORDER BY started_at DESC, id DESC) = 1`), expired backups included. Tests: several apps and services, ties, none · internal/store/backups.go, internal/store/backups_test.go

**⟶ Wait for Wave 1, then:**

- [ ] T011 [US5] Service attention (research R9, R10, data-model "Attention item"): in internal/service/attention.go define `AttentionKind` (7 constants), `AttentionItem`, `Attention`, the pure `attentionOf(apps []store.AppRecord, latest map[string]store.DeploymentRecord, copies map[string][]Copy, copiesErr error, backups []store.BackupRecord, restores []store.RestoreRecord, hasDestination bool) Attention` following the data-model rule table (failed or reverted latest deploy; out of memory; ≥ 3 restarts — `const restartingAt = 3`; unhealthy while no rollout runs; failed newest backup/restore; backups with no destination, once), with copy items of one kind per service merged, copy items of a failed deployment's job version suppressed, sorted by `When` newest first, ties by app then service; `copiesErr != nil` → `CopiesUnknown` and no copy items. `Service.Attention(ctx)` does the reads (`ListApps` store read, `LatestDeployments`, `deployer.AllCopies`, `LatestBackupRuns`, `GetBackupDestination`; a Nomad error is logged, not returned). Add `ChangeAttention ChangeKind = "attention"` and publish it (AppID "") from `deploymentChanged`, `copiesChanged`, `backupsChanged` and `DeleteApp`. Tests: a table for `attentionOf` covering every row of the rule table, merging, suppression, ordering, resolution (a later healthy deploy, a later good backup), `CopiesUnknown`; `Attention` with deploytest; the new notice published · internal/service/attention.go, internal/service/attention_test.go, internal/service/changes.go, internal/service/backups.go, internal/service/apps.go

**⟶ Wait for T011, then (Wave 2, different files):**

- [ ] T012 [P] [US5] API (contracts/api.md "Attention", "Copies with usage" first two fields): `GET /v1/attention` → `listAttention` with `{items, copiesUnknown}` (fields `kind`, `appId`, `service`, `copies`, `deploymentId`, `jobVersion`, `at`, `detail`, empty ones omitted; `items` always an array). `copyJSON` gains `lastRestartAt` and `outOfMemory`. Register the route behind auth; `api/openapi.yaml` gets the path, `Attention`, `AttentionItem` and the two `Copy` fields. Tests: each kind serialised, empty list, Nomad down → 200 `copiesUnknown: true`, 401 without a token · internal/api/attention.go, internal/api/attention_test.go, internal/api/copies.go, internal/api/copies_test.go, internal/api/api.go, api/openapi.yaml
- [ ] T013 [P] [US5] Console (contracts/console.md, research R12): `internal/ui/attention.go` turns each item into one `sentence` (view.go's type) plus a "when" and one action link, per kind: deploy_failed "<app>'s deploy v<N> failed|was reverted: <detail>." → "View deployment" `/apps/<app>/deployments#<id>`; out_of_memory "<n> cop(y|ies) of <app>/<service> ran out of memory." → "Raise memory" `/apps/<app>/configure#svc-<service>-memory`; restarting "… keeps restarting." → "View logs" `/apps/<app>/logs?copy=<id>`; unhealthy "… is failing its health check." → "View logs"; backup_failed / restore_failed "The latest backup|restore of <app>/<service> failed." → "Open backups" `/apps/<app>/backups`; no_backup_destination "Backups are set up, but no backup destination is." → "Set destination" `/settings#backup-destination` (add that `id` in settings.html if missing). The Apps page draws the region under the headline (status mark per DESIGN.md, newest first; empty: "Nothing needs you."; `CopiesUnknown`: "Copies couldn't be checked: Nomad can't be reached."), the headline's count uses the list. New route `GET /parts/attention` (`s.requirePart`), `/live` adds `service.ChangeAttention`, the region refetches on `sse:attention`. The Running tab shows "ran out of memory" for `OutOfMemory` copies. Rebuild app.css. Tests: each kind's sentence and link, empty, unknown, region in the page and the part, live kinds include attention · internal/ui/attention.go, internal/ui/attention_test.go, internal/ui/apps.go, internal/ui/view.go, internal/ui/app.go, internal/ui/ui.go, internal/ui/templates/apps.html, internal/ui/templates/parts.html, internal/ui/templates/app.html, internal/ui/templates/settings.html, internal/ui/style/, internal/ui/static/app.css

**⟶ Wait for Wave 2, then:**

- [ ] T014 [P] [US5] Parity: `"GET /parts/attention": {ops: ["listAttention"]}`; `"GET /apps"` and `"GET /live"` add `listAttention` · internal/api/parity_test.go
- [ ] T015 [P] [US5] Lab step (research R8, R13): deploy a lab app with a health path that 404s, wait for reverted, `GET /v1/attention` has a `deploy_failed` item and `/parts/attention` shows it; deploy it fixed, the item is gone. Then a busybox service with `memoryMB: 16, memoryMaxMB: 24` running `sh -c 'head -c 64m /dev/zero | tail'`: within 60 s its copy has `outOfMemory: true` and an `out_of_memory` item exists (if Nomad's list stub has no events, fall back as research R8 says and note it in research.md) · cmd/caravana/console_lab_test.go

**Checkpoint**: US5 works in the API and the console.

## Phase 5: User Story 2 - Create an app from a pasted Compose file (P2)

**Goal**: New app › Compose file: preview, fill the overlay, create and deploy.

**Independent test**: quickstart §2. Paste the Hollo file, fill domain, health and secrets, create → healthy, and the stored app equals what the API builds from the same file and overlay.

Files: `internal/ui/composeform.go`, `internal/ui/composeform_test.go`, `internal/ui/newapp.go`,
`internal/ui/newapp_test.go`, `internal/ui/ui.go`, `internal/ui/templates/newapp.html`,
`internal/ui/templates/compose.html`, `internal/ui/style/`, `internal/ui/static/app.css`,
`internal/api/parity_test.go`, `cmd/caravana/console_lab_test.go`

- [ ] T016 [US2] Compose draft (research R11, data-model "Compose draft"): in internal/ui/composeform.go define `composeDraft` (name, compose text, revision, per-service overlay fields `svc.<name>.domains|healthPath|healthInterval|healthTimeout|backupEngine|backupSchedule|backupKeep`, secret inputs `svc.<name>.secret.<NAME>`) with `parseComposeDraft(form) (composeDraft, bool)` and `(d composeDraft) request() compose.Request` (empty secret input → `app.Secret{Name}`, the secret names to send are the union of the preview's names and the inputs posted). `draftFrom(a app.App)` prefills the overlay from an app (domains as hosts, health, secret names, backup) for US3. The struct that feeds templates never holds secret values: `parseComposeDraft` keeps them in a separate map used only by `request()`. Tests: round trip with `deploy/examples/hollo/overlay.example.json`'s values gives the same `compose.Request` as decoding that JSON; empty secret means keep; secret values absent from the template data · internal/ui/composeform.go, internal/ui/composeform_test.go

**⟶ Wait for T016, then:**

- [ ] T017 [US2] New app tabs and routes (contracts/console.md): `/apps/new` shows two tab links, "Image" (today's form, unchanged) and "Compose file" (`?from=compose`: name and a monospace textarea for the file, button "Preview"). `POST /apps/new/compose/preview`: `MaxBytesReader` 1 MiB + 64 KiB (over → error at the file field "The file is larger than 1 MiB."), name checked as New app checks it plus "taken" via `GetApp`, then `svc.PreviewCompose`. Render (template compose.html): the file still in the textarea, a services table (name, image, ports, volumes, memory reserved/ceiling, copies), warnings and refusals each with its Compose path (refusals as `*app.FieldError` from the preview: field is the Compose path), and when nothing is refused the overlay fields per service (domains and health for services with ports; a password input per secret name, always empty; backup engine/schedule/keep) and a "Create and deploy" button posting to `POST /apps/new/compose`, which calls `svc.DeployComposeIf(ctx, name, draft.request(), service.Condition{IfNew: true}, app.TriggerManual)`: success → 303 to the deployment; `ErrAppExists` → 409 with "taken" at the name; `*app.FieldError` → 422 with the message at the overlay field it names or in the refusal list for Compose paths; any failure re-renders with every secret input empty and the notice "Secret values aren't kept; type them again." Register both routes. Rebuild app.css. Tests: preview of a valid file, of a refused one (no create button), name taken at preview and at create, create success (stored app equals `PreviewCompose`'s), a failing create's response body contains none of the posted secret values, oversize file · internal/ui/newapp.go, internal/ui/newapp_test.go, internal/ui/composeform.go, internal/ui/composeform_test.go, internal/ui/ui.go, internal/ui/templates/newapp.html, internal/ui/templates/compose.html, internal/ui/style/, internal/ui/static/app.css

**⟶ Wait for T017, then:**

- [ ] T018 [P] [US2] Parity: `"POST /apps/new/compose/preview": {ops: ["previewCompose"]}`, `"POST /apps/new/compose": {ops: ["deployCompose"]}` · internal/api/parity_test.go
- [ ] T019 [P] [US2] Lab step: create the compose example (`internal/compose/testdata` web-db or the lab's own) through the New app › Compose forms, follow the deployment to healthy · cmd/caravana/console_lab_test.go

**Checkpoint**: Compose apps can be created from the console.

## Phase 6: User Story 3 - Redeploy an existing Compose app from a pasted file (P3)

**Goal**: Configure › Deploy from Compose with the overlay prefilled and added/removed services named.

**Independent test**: quickstart §3. Paste Hollo with one newer image → preview shows the change and no added/removed service; deploy → only that service is replaced, secrets kept.

Files: `internal/ui/composeform.go`, `internal/ui/composeform_test.go`, `internal/ui/configure.go`,
`internal/ui/ui.go`, `internal/ui/templates/compose.html`, `internal/ui/templates/app.html`,
`internal/ui/static/app.css`, `internal/api/parity_test.go`

- [ ] T020 [US3] Deploy from Compose (contracts/console.md, research R11): Configure gets a "Deploy from Compose" link to `GET /apps/{id}/compose` (the compose.html form under the Configure tab, with the app's revision hidden and the overlay from `draftFrom(stored)`, secret hints "Empty keeps the current value."). `POST /apps/{id}/compose/preview` previews as T017 and also lists services the file adds and removes against the stored app ("Removes service worker: its copies stop."). `POST /apps/{id}/compose` calls `DeployComposeIf` with `Condition{IfRevision: revision}`: success → 303; `ErrAppChanged` → 409 with the form and "This app changed since you opened the form." and a fresh revision; errors as T017. Register the routes. Rebuild app.css. Tests: prefill from a stored app with secrets (names, no values), added/removed services, stale refusal, empty secret keeps its value (secretstest), success · internal/ui/composeform.go, internal/ui/composeform_test.go, internal/ui/configure.go, internal/ui/ui.go, internal/ui/templates/compose.html, internal/ui/templates/app.html, internal/ui/static/app.css

**⟶ Wait for T020, then:**

- [ ] T021 [US3] Parity: `"GET /apps/{id}/compose": {ops: ["getApp"]}`, `"POST /apps/{id}/compose/preview": {ops: ["getApp", "previewCompose"]}`, `"POST /apps/{id}/compose": {ops: ["deployCompose"]}` · internal/api/parity_test.go

**Checkpoint**: the Compose round trip works without curl.

## Phase 7: User Story 4 - See each copy's CPU and memory (P3)

**Goal**: the Running tab shows live usage per copy against reservation and ceiling, warning at 90 %.

**Independent test**: quickstart §4. A copy near its ceiling shows the warning within 10 s; `listCopies?usage=true` gives the same numbers.

Files: `internal/deploy/usage.go`, `internal/deploy/usage_test.go`,
`internal/deploy/deploytest/nomad.go`, `internal/service/usage.go`, `internal/service/usage_test.go`,
`internal/api/copies.go`, `internal/api/copies_test.go`, `api/openapi.yaml`, `internal/ui/app.go`,
`internal/ui/live.go`, `internal/ui/view.go`, `internal/ui/app_test.go`,
`internal/ui/templates/app.html`, `internal/ui/templates/parts.html`, `internal/ui/style/`,
`internal/ui/static/app.css`, `internal/api/parity_test.go`, `cmd/caravana/console_lab_test.go`,
`cmd/caravana/gate_test.go`

- [ ] T022 [US4] `deploy.Deployer.Usage(ctx, copies []Copy) map[string]CopyUsage` (research R6): for each copy that isn't terminal and has started, `Allocations().Stats(&api.Allocation{ID: c.ID}, …)` with a 2 s timeout, at most 4 at once (a buffered channel as semaphore + `sync.WaitGroup`); read the task named `c.Service` from `Tasks`: CPU `TotalTicks` rounded to MHz, memory `Usage` if `"Usage"` is in `Measured`, else `RSS`, bytes → MB rounded. `deploy.CopyUsage{CPUMHz, MemoryMB int; Err error}`; a failed call sets `Err` (wrapped with `d.callError`). deploytest: `SetStats(allocID, api.AllocResourceUsage)` and `GET /v1/client/allocation/{id}/stats` (404 when unset). Tests: cgroups v1 and v2 shapes, sidecar task ignored, one failing copy doesn't affect others, terminal copies skipped, concurrency bounded · internal/deploy/usage.go, internal/deploy/usage_test.go, internal/deploy/deploytest/nomad.go

**⟶ Wait for T022, then:**

- [ ] T023 [US4] Service usage (data-model "Copy usage", research R7): `service.CopyUsage` (CPUMHz, MemoryMB, CPUReservedMHz, MemoryReservedMB, MemoryCeilingMB, NearCeiling, Error string) and `Service.CopiesWithUsage(ctx, appID) ([]Copy, map[string]CopyUsage, error)`: `Copies`, then `deployer.Usage`, then reservations from the stored app's service with the same defaults `Translate` applies (add `Deployer.Settings() nomadjob.Settings` or a small `deploy` helper `Reserved(svc app.Service) (cpu, mem, memMax int)` so the default rule lives in one place — reuse what nomadjob uses for `memory_max`). `NearCeiling = MemoryMB*10 >= MemoryCeilingMB*9`. Error text: "Nomad didn't answer for this copy." Tests: defaults applied, explicit values, near-ceiling boundary (89 %, 90 %), an errored copy · internal/service/usage.go, internal/service/usage_test.go

**⟶ Wait for T023, then (different files):**

- [ ] T024 [P] [US4] API: `listCopies` reads `usage` (`true`/`false`, else 400 `invalid_field` field `usage`); with true it calls `CopiesWithUsage` and adds `usage` (`cpuMHz`, `memoryMB`, `cpuReservedMHz`, `memoryReservedMB`, `memoryCeilingMB`, `nearCeiling`, or only `error`) to copies that have it. `api/openapi.yaml`: the parameter and `CopyUsage`. Tests: without the parameter unchanged, with it numbers and an error entry, bad value · internal/api/copies.go, internal/api/copies_test.go, api/openapi.yaml
- [ ] T025 [P] [US4] Console: the Running tab's copies table gains "CPU" ("<n> MHz" over "of <r> MHz reserved") and "Memory" ("<n> MB" over "of <c> MB ceiling, <r> reserved", a thin meter of n/c, warning mark and "close to its ceiling" when `NearCeiling`), or the error sentence / "—" for copies without usage; the page and `/parts/apps/{id}/copies` use `CopiesWithUsage`. The live route `/live/apps/{id}` with `?tab=running` gets a `Start` func that sends an event `usage` every 5 s until ctx ends (`const usageEvery = 5 * time.Second`, settable in `liveSettings` for tests); the copies region's `hx-trigger` adds `sse:usage`. Other tabs don't pass `tab=running`, so nothing is read. Rebuild app.css. Tests: columns with numbers, warning, error, no usage events without `tab=running`, events with it (short interval) · internal/ui/app.go, internal/ui/live.go, internal/ui/view.go, internal/ui/app_test.go, internal/ui/templates/app.html, internal/ui/templates/parts.html, internal/ui/style/, internal/ui/static/app.css

**⟶ Wait for T024 and T025, then:**

- [ ] T026 [US4] Parity, lab and gate: parity `"GET /apps/{id}"`, `"GET /parts/apps/{id}/copies"` and `"GET /live/apps/{id}"` keep `listCopies` (now with usage; update comments). Lab step: `listCopies?usage=true` on a running lab app has non-zero memory and the app's reservation. Gate step `console-v2-reads` in `runGate` after `console-reads`: with the scoped token, `GET /v1/apps/<app>/copies?usage=true` has `usage` without `error` for a running copy, and `GET /v1/attention` answers 200 · internal/api/parity_test.go, cmd/caravana/console_lab_test.go, cmd/caravana/gate_test.go

**Checkpoint**: all five stories work from the API and the console.

## Phase 8: Polish & cross-cutting

- [ ] T027 [P] Docs: CLAUDE.md "Where we are" gets feature 014 (what changed in the console and the API, 0.11.0, the new codes and routes); docs/plan.md marks 012.5 done as feature 014 and renumbers Phase 2.5's items (Aspire 015, Bella Baxter 016); DESIGN.md gets the edit form, needs-you list and usage cells if `/impeccable document` is run · CLAUDE.md, docs/plan.md
- [ ] T028 Validate against the Success Criteria: `go build ./...`, `go vet ./...`, `go tool staticcheck ./...`, `go test -race ./...`, `gofmt -l .`, `go mod tidy -diff`, `CGO_ENABLED=0 GOOS=linux GOARCH=amd64|arm64 go build ./...`, `tools/ui-css.sh check`, `tools/ui-vendor.sh check`, shellcheck (via Docker), `CARAVANA_LAB=1 go test ./cmd/caravana -run TestLab -v -timeout 25m`; then walk quickstart.md §1–§5 in the browser with and without JavaScript · specs/014-console-v2/quickstart.md

## Dependencies & Execution Order

- **Phase 2** blocks US1, US2 and US3: T001 → T002 → T003. US5 and US4 don't need it and could
  start first, but share files with US1 (see the scale note), so the default is story order.
- **US1 (Phase 3)**: T004 ∥ T005 → T006 → T007 ∥ T008.
- **US5 (Phase 4)**: T009 ∥ T010 → T011 → T012 ∥ T013 → T014 ∥ T015.
- **US2 (Phase 5)**: T016 → T017 → T018 ∥ T019.
- **US3 (Phase 6)**: needs US2's T016/T017 (the draft and template). T020 → T021.
- **US4 (Phase 7)**: T022 → T023 → T024 ∥ T025 → T026.
- **Polish**: T027 any time after the stories; T028 last.

Story order: US1 → US5 → US2 → US3 → US4 (priority, then the plan's delivery order).

## Parallel examples

```text
US1:  T004 (ui/edit.go parsing)     ∥ T005 (ui/merge.go merge and path map)
US5:  T009 (deploy/copies.go)       ∥ T010 (store/backups.go)
      T012 (internal/api)           ∥ T013 (internal/ui)
US4:  T024 (internal/api)           ∥ T025 (internal/ui)
```

## Implementation strategy

- **MVP**: Phase 2 + US1 (T001–T008). The roughest daily task (editing an app) leaves curl
  behind; release it as v0.2.0-rc if wanted.
- **Then** US5 (the owner hears about failures), US2 + US3 (Compose without scripts), US4
  (usage). Each ends at a checkpoint where `go test ./...` and `TestConsoleLab` pass.
- **Delegation**: each phase goes to one Sonnet agent with this file, the spec, plan, research,
  data model and contracts; Opus reviews the diff, runs the tests and checks the task's
  acceptance before the next phase starts.
