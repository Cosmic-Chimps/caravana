---

description: "Task list for feature 007: HTMX UI v1"
---

# Tasks: HTMX UI v1

**Input**: Design documents from `specs/007-htmx-ui-v1/`

**Prerequisites**: [plan.md](plan.md), [spec.md](spec.md), [research.md](research.md),
[data-model.md](data-model.md), [contracts/](contracts/), [quickstart.md](quickstart.md)

**Tests**: Required (constitution VIII). In each story, write the tests first and watch them
fail.

**Organization**: US1 = see at a glance what's running (Apps page, shell, app header, live).
US2 = deploy and watch it roll out (Deployments tab). US3 = New app. US4 = Running tab (copies).
US5 = Logs tab. US6 = Configure, webhook and delete. US7 = Settings in the shell.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: can run in parallel (different files, no dependency on an unfinished task)
- **[Story]**: US1–US7 from spec.md

## Conventions for every task

- Every function doing I/O takes `ctx context.Context` first. No `panic` in library code.
  Errors are wrapped with context (`fmt.Errorf("list copies of %q: %w", appID, err)`).
- **Nomad types never leave `internal/deploy`** (constitution VI): it returns `deploy.Copy`,
  `deploy.Progress`, `deploy.LogLine`.
- **Templates obey the CSP** (research R3): no `hx-on*` attributes, no trigger filters, no
  `js:` values, no `style=` attributes, no `<style>` blocks, no `<script>` without `src`. Every
  style is a class in `internal/ui/style/input.css`; run `tools/ui-css.sh build` after changing
  templates or styles, and commit `internal/ui/static/app.css`.
- **Plain forms first**: every action is a `<form method="post">` that works without
  JavaScript; htmx only improves it (FR-027). Every `POST` goes through `s.protected`.
- **Only real data** (FR-004): tests use `whoami`, `blog` and `example.com`. Never copy sample
  data (app names, emails, domains, orgs) from the local design handoff in `design/console/`
  into tracked files.
- **Never logged** (FR-028): tokens, env values, log line text. Env values are never rendered
  either (FR-015).
- Routes and `api/openapi.yaml` stay equal after every task (the contract-sync test). Copy each
  piece from `contracts/api.md` when its story lands; bump `info.version` to `0.5.0` with the
  first one.
- Copy and sentences come from `contracts/pages.md` and `data-model.md` (PRODUCT.md voice:
  sentence case, verbs that say the outcome, no exclamation marks).
- Existing helpers: `openTestStore`, `storeWithApp` (store); `newEnv`, `startService`,
  `waitFinal`, `waitFor`, `newAuthEnvWithAdmin` (service); `newTestEnv`, `call`, `callWith`,
  `checkSchema`, `sessionCookie`, `cookieCall` (api); `newUIEnv`, `location`, `pagesToCheck`
  (ui); `newTestDeployer` (deploy); `runGate`, `labAPIToken`, `waitFor` (cmd/caravana).
- Keep `gofmt -l .` empty after every task; feature 001's goldens must not change.

---

## Phase 1: Setup

- [X] T001 Create `internal/store/migrations/0004_console.sql` exactly as in data-model.md
  (Storage), with its comment. Check `go test ./internal/store` passes (the migration tests
  count embedded files).
- [X] T002 [P] Write `tools/ui-vendor.sh` (`fetch` | `check`, `set -euo pipefail`, same style
  and cache dir `/.cache/` as `tools/ui-css.sh`) and `tools/ui-vendor.versions` (one line per
  file: name, version, npm tarball URL, path inside the tarball, SHA-256 of the extracted file).
  Pin htmx.org 2.0.11 (`dist/htmx.min.js`) and htmx-ext-sse 2.2.4 (`dist/sse.min.js`); if a
  newer 2.x patch is `latest`, use it and note it in the commit (research R2, R4). `fetch`
  writes `internal/ui/static/js/htmx.min.js` and `internal/ui/static/js/sse.min.js`; `check`
  verifies the committed files' checksums. Write `internal/ui/static/js/LICENSES.md` (both
  0BSD, with their copyright lines). Run `fetch` and commit the files.
- [X] T003 [P] Add `tools/ui-vendor.sh check` to `.github/workflows/ci.yml` next to
  `tools/ui-css.sh check`; shellcheck already covers `tools/*.sh` (run it via Docker on the
  Mac).
- [X] T004 [P] Add `TriggerConsole Trigger = "console"` in `internal/app/deployment.go`,
  accept it in `internal/app/validate.go` (update the error text to name all three), extend the
  validation table test in `internal/app/validate_test.go`, and change `Deployment.trigger`'s
  enum in `api/openapi.yaml` to `[manual, webhook, console]` with `info.version: 0.5.0`
  (research R17).

---

## Phase 2: Foundational (blocks every story)

- [X] T005 [P] `internal/httpx/stream.go`: `Stream(w)` and `EventWriter` (`Event`, `Comment`)
  per contracts/go-packages.md and research R12. It clears the write deadline through
  `http.NewResponseController`, sets `Content-Type: text/event-stream`, `Cache-Control:
  no-store`, `X-Accel-Buffering: no`, writes `retry: 3000`, splits data on newlines into
  `data:` lines, and flushes after each write. Tests in `internal/httpx/stream_test.go` with a
  real `httptest.Server` (a server `WriteTimeout` of 100 ms doesn't cut a stream that lasts
  300 ms; multi-line data; `LogRequests` in front still lets it flush).
- [X] T006 [P] `internal/service/changes.go`: `ChangeKind` (`apps`, `activity`, `app`,
  `deployments`, `copies`, `deleted`), `Change`, `Subscribe(appID)` returning a channel
  (buffer 16) and an unsubscribe func, and an unexported `publish(Change)` that fans out with
  non-blocking sends (drop when full). An `""` subscriber gets every app's notices. Wire a
  `changes` field into `Service` in `internal/service/service.go`. Table tests in
  `internal/service/changes_test.go`: per-app filtering, all-apps subscriber, a full subscriber
  doesn't block others, unsubscribe closes nothing twice, `-race` clean.
- [X] T007 [P] `CheckSessionQuiet` in `internal/service/sessions.go` (CheckSession without
  `TouchSession`; share the checking code, don't copy it). Tests in
  `internal/service/sessions_test.go` with the fake clock: quiet checks never move
  `LastSeenAt`, so a session checked only quietly expires after 12 h idle (research R11).
- [X] T008 [P] Fake Nomad allocations in `internal/deploy/deploytest/nomad.go`:
  `SetCopies(jobID string, allocs ...api.AllocationListStub)` (fills IDs, indexes, job ID; bumps
  the index), `GET /v1/allocations` with blocking `index`/`wait` (answers at once when the
  index is older, else waits for a change or the wait time), `GET /v1/job/{id}/allocations`,
  `GET /v1/allocation/{id}` (a full `api.Allocation` built from the stub). Keep the existing
  four endpoints unchanged.
- [X] T009 `internal/deploy/copies.go`: `Copy`, `CopyStatus`, the mapping table of research R8
  (an unexported `copyOf(*api.AllocationListStub) Copy`), `Copies(ctx, jobID)`,
  `AllCopies(ctx)` (one `Allocations().List` call, grouped by job ID). Errors use
  `ErrNomadUnavailable` like the register path. Table tests in
  `internal/deploy/copies_test.go` for every mapping row, restarts summed over tasks,
  `StartedAt` from the task state, and Nomad down → `ErrNomadUnavailable` (depends on T008).
- [X] T010 `WatchCopies(ctx, changed func(jobIDs []string))` in `internal/deploy/copies.go`:
  the blocking-query loop of research R6, reusing `Timing` backoff and the index-goes-backwards
  rule from `watch.go`; it compares `ModifyIndex` per allocation with the previous answer and
  reports added, changed and removed allocations' job IDs (deduplicated, sorted). Tests in
  `internal/deploy/copies_test.go` against the fake: a new copy, a health change and a removed
  copy each report the job; nothing changed → no call; Nomad down then up → resumes; ctx
  cancel returns (depends on T009).
- [X] T011 Publish notices from the deploy flow in `internal/service/deploy.go`: after every
  deployment record create/update (`deploy`, `write`, `update`, `fail`, `finish`) publish
  `deployments` and `app` for the app, and `apps` and `activity` for all; after `SaveApp`
  publish `app` and `apps` (data-model, "Who publishes what"). Tests in
  `internal/service/changes_test.go`: a scripted deploy against the fake Nomad delivers
  `deployments` for pending, running and healthy to a subscriber.
- [X] T012 Copies watcher lifecycle in `internal/service/changes.go`: the first subscriber
  starts `deployer.WatchCopies` under the service's background context; it stops 30 s after the
  last subscriber leaves (make the delay a `Service` field so tests use 10 ms). Changed job IDs
  become `copies` and `app` for that app and `apps` for all. Tests: subscribe → `SetCopies` on
  the fake → a `copies` notice arrives; unsubscribe → the watcher stops (no more Nomad calls
  after the delay).
- [X] T013 UI foundations in `internal/ui/ui.go`: `Deps` gains `Service *service.Service` and
  `Host string`; content hashes (`?v=`) for every file under `static/` (generalize `cssHash`
  into a map used by a template func `asset "js/htmx.min.js"`); `parseTemplates` builds each
  page from `base.html` + `shell.html` + `parts.html` + the page; `requirePart` (like
  `requireSession`, but answers `401` with `HX-Redirect: /login?next=<HX-Current-URL path>`);
  `GET /` redirects a signed-in admin to `/apps`, and sign-in's default `next` becomes `/apps`.
  Update `cmd/caravana/main.go` to pass the Service. Keep the CSP header exactly as it is.
- [X] T014 Templates and script base: `internal/ui/templates/base.html` gains the
  `htmx-config` meta of research R3 and `<script src>` tags for `js/htmx.min.js`,
  `js/sse.min.js` and `js/console.js` (`defer`); new `internal/ui/templates/shell.html` (sidebar
  with wordmark, host in mono, Apps and Settings links with the current one marked, account row
  with the email and a Sign out form; sticky top bar with breadcrumbs, the live indicator
  `<span id="live" role="status" aria-live="polite" data-live-indicator>` and a slot for the
  page's primary action; under 768 px a `<details>` "Menu"), per contracts/pages.md (Shell) and
  DESIGN.md (Layout); new `internal/ui/templates/parts.html` with `status-mark`,
  `status-pill`, `time` (relative with exact `title`) and `empty` defines. Add the shell,
  `.htmx-indicator`/`.htmx-request` and status styles to `internal/ui/style/input.css` and run
  `tools/ui-css.sh build`.
- [X] T015 [P] `internal/ui/static/js/console.js` skeleton: plain ES2020, `"use strict"`, no
  globals beyond one IIFE, commented for a reader new to JS. Event delegation on `document`:
  the live indicator (`htmx:sseOpen` → "Live", `htmx:sseError` → "Reconnecting…",
  `htmx:sseClose` after `ended` → "Signed out. Sign in again." with a link to `/login`), and
  copy buttons (`[data-copy]` copies the text of the element its value names with
  `navigator.clipboard.writeText`, then says "Copied" for 2 s). Log view and delete-name check
  come in US5 and US6.
- [X] T016 `internal/ui/live.go`: the stream loop shared by every `/live…` route (research R1,
  R11, R12): check the session with `CheckSessionQuiet` (invalid → send `ended`, return);
  `Subscribe`; send `sync`; then `select` on notices (coalesce: at most one event per kind per
  500 ms, a timer flushes pending kinds), a 20 s keepalive comment, a 30 s session re-check
  (gone → `ended`, return) and `r.Context().Done()`. Unsubscribe on return. A `kinds` filter
  per route decides which notices become events. Tests in `internal/ui/live_test.go` that read
  the raw `text/event-stream`: `sync` first; a published notice arrives as its event; ten quick
  notices of one kind arrive as at most two events; signing out ends the stream with `ended`;
  no session → `ended` and close; the session's `LastSeenAt` doesn't move while streaming.
- [X] T017 Shutdown ends streams in `cmd/caravana/main.go`: give each `http.Server` a
  `BaseContext` from a context that `shutdown` cancels before calling `Shutdown`. Test in
  `cmd/caravana/main_test.go`: with a `/live` stream open, shutdown completes in well under the
  10 s timeout.

**Checkpoint**: streams, notices, copies from Nomad, the shell and the script base exist; no
console page yet.

---

## Phase 3: User Story 1 - See at a glance what's running (Priority: P1) 🎯 MVP

**Goal**: sign in, land on `/apps` inside the shell: headline sentence, apps table, activity;
open an app's page header; everything updates live.

**Independent Test**: deploy two apps through the API (one healthy, one reverted); sign in;
both rows and the headline are right; deploy a third through the API and watch the row,
headline and activity change within 5 s without a reload.

### Tests for User Story 1

- [X] T018 [P] [US1] Store tests in `internal/store/apps_test.go` and
  `internal/store/deployments_test.go`: `ListApps` sorted by ID (empty → empty slice),
  `LatestDeployments` returns each app's newest (ties broken by ID), `ListRecentDeployments`
  newest first across apps with a limit.
- [X] T019 [P] [US1] Service status tests in `internal/service/apps_test.go`: one table row per
  research R7 case (rolling out pending/running, healthy with and without deployment status,
  degraded counts, not running, unknown when the fake Nomad is down), `WantedCopies` with nil
  and set replicas, `LiveVersion`, multi-service apps; `ListApps` makes one Nomad request for
  many apps; Nomad down returns summaries with `unknown` and no error.
- [X] T020 [P] [US1] API tests in `internal/api/apps_test.go` and
  `internal/api/deployments_test.go`: `GET /v1/apps` (empty, two apps, statuses, schema
  `AppSummaryList` via `checkSchema`), `GET /v1/deployments` (order, `limit` bounds → 400
  `invalid_field`), both 401 without credentials.
- [X] T021 [P] [US1] UI tests in `internal/ui/apps_test.go`: `/apps` empty state ("No apps yet."
  and "New app"); populated rows show status word, `v7`, image, domains as `https://` links;
  headline sentences for all-healthy, one rolling out, one degraded, Nomad down; activity lists
  triggers in words; `/parts/apps` and `/parts/activity` render the same fragments as the page;
  parts without a session answer 401 with `HX-Redirect`; `/apps/nope` is 404 with "There's no
  app called `nope`."; `/live` streams `apps` and `activity` notices; `GET /` and sign-in land
  on `/apps`.

### Implementation for User Story 1

- [X] T022 [US1] Store methods in `internal/store/apps.go` and `internal/store/deployments.go`:
  `ListApps`, `LatestDeployments` (one query with `ROW_NUMBER() OVER (PARTITION BY app_id ORDER
  BY created_at DESC, id)`), `ListRecentDeployments` (uses the new index) — contracts/go-packages.md.
- [X] T023 [US1] `internal/service/apps.go`: `AppSummary`, `AppState`, `AppStatus`, the status
  rules of data-model.md (an unexported pure function from app + latest deployment + copies, so
  T019's table tests call it directly), `ListApps` (store + one `AllCopies`), `AppSummary(ctx,
  id)`, `RecentDeployments`. `Copy` and `Progress` type aliases. Nomad errors are logged once per
  call, never returned from `ListApps`.
- [X] T024 [US1] API: `listApps` in `internal/api/apps.go` and `listRecentDeployments` in
  `internal/api/deployments.go` (limit 1–100, default 20), routes in `internal/api/api.go`, and
  in `api/openapi.yaml` the paths `GET /v1/apps`, `GET /v1/deployments` and schemas
  `AppSummary`, `AppSummaryList`, `AppStatus` from contracts/api.md.
- [X] T025 [US1] View helpers in `internal/ui/view.go`: the headline sentence (research R7
  examples), status words and marks, trigger words ("from the console", "through the API", "by
  webhook"), version labels (`v7`), short digests and IDs with the full value for `title`, and
  move the 006 relative-time helper here. Table tests in `internal/ui/view_test.go`.
- [X] T026 [US1] Apps page: `internal/ui/apps.go` (`GET /apps`, `GET /parts/apps`,
  `GET /parts/activity`, `GET /live` with kinds `apps`/`activity`) and
  `internal/ui/templates/apps.html` per contracts/pages.md (Apps): header with headline and
  "New app" (link to `/apps/new`; until US3 lands it 404s, so hide it behind a template flag
  that US3 turns on), apps table, activity rail, empty state, the `?deleted=` notice, regions
  with `hx-get` + `hx-trigger="sse:<kind>, sse:sync"` and `hx-swap="innerHTML"`, `<body
  hx-ext="sse" sse-connect="/live" sse-close="ended">`.
- [X] T027 [US1] App page frame: `internal/ui/app.go` (`GET /apps/{id}`, `GET
  /parts/apps/{id}/header`, `GET /live/apps/{id}` with kinds `app`/`deleted`) and
  `internal/ui/templates/app.html` with the header (name, status pill, domains, summary
  sentence), breadcrumbs `Apps › id`, and the tab bar as links with `aria-current`; each tab
  appears when its story lands (US4 makes `/apps/{id}` the Running tab; until then the frame
  shows the header and the tabs built so far). A `deleted` notice swaps the header part, which
  then says "`blog` was deleted." with a link to Apps.
- [X] T028 [US1] Styles for the apps table, activity rail, app header and tab bar in
  `internal/ui/style/input.css` (DESIGN.md: rows 46–52 px with 1 px dividers, mono values,
  tabular figures, rail 400 px and below the table under 1100 px); run `tools/ui-css.sh build`.

**Checkpoint**: the MVP. The owner sees every app's real status, live.

---

## Phase 4: User Story 2 - Deploy a new version and watch it roll out (Priority: P2)

**Goal**: the Deployments tab: deploy (keep or change images), history with live status,
rollout progress, failure sentences.

**Independent Test**: on an app with past deploys, press Deploy and watch the new entry go
pending → running (with progress) → healthy without a reload; change the image and see the new
digest; deploy a broken health path through the API and read the reverted sentence.

### Tests for User Story 2

- [X] T029 [P] [US2] Fake Nomad deployments with task-group state in
  `internal/deploy/deploytest/nomad.go` (`ScriptProgress` or extend `Script` so a state can
  carry `TaskGroups` counts), and `internal/deploy/progress_test.go`: sums over task groups,
  canaries and promotion, `found == false` when Nomad has no deployment for the version.
- [X] T030 [P] [US2] Service tests in `internal/service/apps_test.go`: `Progress` is nil for
  final deployments, set while running; `ListDeploymentsBefore` pages by `created_at`.
- [X] T031 [P] [US2] API tests in `internal/api/deployments_test.go`: `progress` appears on a
  running deployment from `getDeployment` and on the newest running one in `listDeployments`,
  never on final ones; schema `Deployment` with `progress`; trigger `manual` from the API.
- [X] T032 [P] [US2] UI tests in `internal/ui/app_test.go`: `/apps/{id}/deployments` lists
  entries newest first with trigger words, digests, durations, warnings; the explanation
  sentence for each status row of data-model.md (Deployment) with the raw error under it;
  "Show older" pages with `?before=`; `POST /apps/{id}/deploy` with unchanged images calls the
  redeploy path (trigger `console`, `303` to the tab); a changed image deploys the stored app
  with only that image replaced (other fields byte-for-byte equal); an unknown image →
  `422` with the error on that image field and the typed value kept; cross-site POST → 403;
  works as a plain form post; `/parts/apps/{id}/deployments` matches the page region.

### Implementation for User Story 2

- [X] T033 [US2] `internal/deploy/progress.go`: `Progress` and `(*Deployer).Progress` per
  contracts/go-packages.md, reading `Jobs().Deployments` like `Watch`.
- [X] T034 [US2] Service: `Progress(ctx, rec)` and `ListDeploymentsBefore` in
  `internal/service/apps.go`; `store.ListDeploymentsBefore` in `internal/store/deployments.go`
  with a test in `internal/store/deployments_test.go`.
- [X] T035 [US2] API: add `progress` to `deploymentJSON` in `internal/api/deployments.go`
  (filled for running deployments by `getDeployment` and for the newest running one by
  `listDeployments`); `Progress` schema and the `Deployment.progress` property in
  `api/openapi.yaml`.
- [X] T036 [US2] View helpers in `internal/ui/view.go`: the explanation sentences of
  data-model.md (Deployment), durations ("took 42 s", "running for 40 s"), the progress line of
  research R9. Table tests in `internal/ui/view_test.go`.
- [X] T037 [US2] Deployments tab in `internal/ui/app.go` (`GET /apps/{id}/deployments`, `GET
  /parts/apps/{id}/deployments`, `POST /apps/{id}/deploy` per research R15: read the stored app,
  copy only the posted `image.<service>` values onto it, `Redeploy` when nothing changed else
  `Deploy`, both with `app.TriggerConsole`; map `*app.FieldError` paths `services[i].image` to
  the field) and a `deployments` define in `internal/ui/templates/app.html`: the deploy form
  (one image field per service, "Deploy" primary, helper text), the history region
  (`aria-live="polite"`), entries, progress, "Show older". `/live/apps/{id}` adds kinds
  `deployments` and `copies` (progress refreshes on copies). The header's primary "Deploy" links
  here.
- [X] T038 [US2] Styles for the deploy form, history entries, progress line and failure detail
  in `internal/ui/style/input.css`; run `tools/ui-css.sh build`.

**Checkpoint**: deploy from the console and watch it, live.

---

## Phase 5: User Story 3 - Create an app from an image (Priority: P3)

**Goal**: New app form → first deploy, never replacing an existing app; `POST /v1/apps`.

**Independent Test**: on an empty install create whoami with port 80, a domain and `/`; land on
its Deployments tab and see it turn healthy; a taken name and a bad image give field errors.

### Tests for User Story 3

- [X] T039 [P] [US3] Store test in `internal/store/apps_test.go`: `InsertApp` then `InsertApp`
  with the same ID → `ErrAlreadyExists`, the stored app unchanged.
- [X] T040 [P] [US3] Service tests in `internal/service/apps_test.go`: `CreateApp` deploys with
  the given trigger; an existing ID → `ErrAppExists` and no deployment record; invalid input →
  `*app.FieldError` and nothing saved; two concurrent creates of one ID → exactly one wins
  (`-race`).
- [X] T041 [P] [US3] API tests in `internal/api/apps_test.go`: `POST /v1/apps` → `202`,
  `Location`, schema `Deployment`; an existing ID → `409 app_exists` with `field: "id"`;
  validation → 400 with the field; 403 cross-site with a cookie; 401 without credentials.
- [X] T042 [P] [US3] UI tests in `internal/ui/newapp_test.go`: `/apps/new` renders the fields
  of contracts/pages.md; a valid post → `303` to `/apps/{id}/deployments` with a `console`
  deployment; a taken name → `422`, "An app called `whoami` already exists." on Name, values
  kept; field paths map to Name, Image, Container port, Domain, Health check path; no port →
  a worker app (no ports, domains or health); works as a plain post; the "New app" action is
  visible on `/apps` and its empty state.

### Implementation for User Story 3

- [X] T043 [US3] `store.InsertApp` in `internal/store/apps.go` (plain `INSERT`, map the
  primary-key violation to `ErrAlreadyExists`).
- [X] T044 [US3] `service.CreateApp` and `ErrAppExists` in `internal/service/apps.go` (validate,
  `InsertApp`, then the same internal `deploy` path `Deploy` uses; publish `app` and `apps`).
- [X] T045 [US3] API `createApp` in `internal/api/apps.go`, route `POST /v1/apps`, error code
  `app_exists` in `internal/api/errors.go` mapped from `service.ErrAppExists`, and the path and
  `409` response in `api/openapi.yaml` from contracts/api.md.
- [X] T046 [US3] `internal/ui/newapp.go` (`GET /apps/new`, `POST /apps` building the
  `app.App` of data-model.md "Validation rules added", trigger `console`) and
  `internal/ui/templates/newapp.html` (labels above fields, hints, the worker note, "Create and
  deploy", errors under fields with `role="alert"` on the first; htmx posts it with
  `hx-post`/`hx-target` on the form and `hx-swap="outerHTML"` so errors appear in place, while
  the plain post renders the whole page). Turn on the "New app" action from T026. Styles in
  `internal/ui/style/input.css`; run `tools/ui-css.sh build`.

**Checkpoint**: a fresh install can go from empty to a live app using only the console.

---

## Phase 6: User Story 4 - See the copies that are running (Priority: P4)

**Goal**: the Running tab and `GET /v1/apps/{id}/copies`, live.

**Independent Test**: deploy an app with two copies; both healthy on Running; stop one in Nomad
and watch Running show the replacement, live.

### Tests for User Story 4

- [X] T047 [P] [US4] Service tests in `internal/service/apps_test.go`: `Copies` returns
  non-terminal copies plus terminal copies of the latest job version only, ordered by service,
  version descending, start time; unknown app → `store.ErrNotFound`; Nomad down → the
  `ErrNomadUnavailable` error.
- [X] T048 [P] [US4] API tests in `internal/api/copies_test.go`: `GET /v1/apps/{id}/copies`
  (schema `CopyList`, 404 unknown app, 502 `nomad_unavailable` when the fake is down).
- [X] T049 [P] [US4] UI tests in `internal/ui/app_test.go`: `/apps/{id}` is the Running tab
  with rows (short ID with full in `title`, service, status word, version, restarts, up for);
  the tab label shows the count; empty sentence, and the failed-deploy sentence; Nomad down
  sentence; `/parts/apps/{id}/copies` matches; a `SetCopies` change produces a `copies` event
  on `/live/apps/{id}`.

### Implementation for User Story 4

- [X] T050 [US4] `service.Copies` in `internal/service/apps.go` (filter of research R8).
- [X] T051 [US4] API `listCopies` in new `internal/api/copies.go`, route, the
  `NomadUnavailable` response, `Copy` and `CopyList` schemas and the path in
  `api/openapi.yaml` from contracts/api.md.
- [X] T052 [US4] Running tab: `/apps/{id}` renders a `running` define in
  `internal/ui/templates/app.html` (region `copies`), `GET /parts/apps/{id}/copies` in
  `internal/ui/app.go`, the tab count in the header; styles in `internal/ui/style/input.css`;
  run `tools/ui-css.sh build`.

**Checkpoint**: the app page answers "is it actually up?".

---

## Phase 7: User Story 5 - Read an app's logs (Priority: P5)

**Goal**: the Logs tab (tail, follow, copy and stream choice, pause, wrap) and
`GET /v1/apps/{id}/logs`.

**Independent Test**: deploy whoami, call it a few times, open Logs and see lines arrive; pause
counts new lines; one copy and `stdout` filters; the API returns the same lines.

### Tests for User Story 5

- [X] T053 [P] [US5] Fake Nomad logs in `internal/deploy/deploytest/nomad.go`:
  `WriteLog(copyID, stream, text)` appends output; `GET /v1/client/fs/logs/{id}` answers
  Nomad's JSON frame stream (`origin=end` + `offset` tail, `follow` keeps the connection and
  sends new frames and heartbeats). Then `internal/deploy/logs_test.go`: lines split across
  frames are joined; a partial last line waits for its newline; a 64 KiB line without newline
  is cut; invalid UTF-8 is replaced; the tail respects `tailBytes`; follow delivers new lines;
  ctx cancel returns.
- [X] T054 [P] [US5] Service tests in `internal/service/logs_test.go`: `Logs` with all copies
  and both streams merges per research R10; the tail keeps the last `Lines` (≤ 500); `Copy`
  not among the app's copies → `store.ErrNotFound`; followed lines carry `Time`; ctx cancel
  ends it.
- [X] T055 [P] [US5] API tests in `internal/api/copies_test.go`: JSON `LogLines` (schema), bad
  `stream`/`lines` → 400 `invalid_field`, unknown copy → 404; `follow=true` answers
  `text/event-stream` with `log` events then new lines; following with a session cookie
  doesn't renew it (`quiet` route); a revoked token ends the stream with `ended` within the
  re-check period (make the period a server field so the test uses 50 ms).
- [X] T056 [P] [US5] UI tests in `internal/ui/logs_test.go`: `/apps/{id}/logs` renders the
  toolbar (GET form with copy and stream selects, Apply, Pause, Wrap with `aria-pressed`) and
  the server-rendered tail for no-JS readers (escaped: a line `<b>x</b>` appears as text);
  `/live/apps/{id}?logs=1` sends `logs-reset`, the tail as `log` JSON events, then new lines;
  no copies → the sentence; nothing from log lines reaches the request log.

### Implementation for User Story 5

- [X] T057 [US5] `internal/deploy/logs.go`: `LogStream`, `LogLine`, `(*Deployer).Logs` per
  contracts/go-packages.md (wraps `Allocations().Info` + `AllocFS().Logs`, cancel channel from
  ctx); `NewNomadClient` in `internal/deploy/deploy.go` sets `ClientConnTimeout` to 1 s.
- [X] T058 [US5] `internal/service/logs.go`: `LogLine`, `LogQuery`, `(*Service).Logs` (resolve
  copies with `Copies`, one goroutine per (copy, stream) feeding one channel, tail then follow,
  `Service` and `Time` filled in).
- [X] T059 [US5] API `getLogs` in `internal/api/copies.go`: validate the query, JSON without
  `follow`, an event stream with `follow=true` through `httpx.Stream` (keepalive 20 s,
  credential re-check 30 s, `ended`); `route.quiet` in `internal/api/api.go` so `authenticate`
  uses `CheckSessionQuiet` for it; path, `LogLine` and `LogLines` schemas in `api/openapi.yaml`.
- [X] T060 [US5] Logs tab: `logs` define in `internal/ui/templates/app.html` and the handler in
  `internal/ui/app.go` (`GET /apps/{id}/logs?copy=&stream=`); `/live/apps/{id}` with `logs=1`
  starts `service.Logs` and sends `logs-reset` then each line as a `log` event (JSON); the page
  has `<div sse-swap="log,logs-reset" hx-swap="none" data-log-sink>`.
- [X] T061 [US5] Log view in `internal/ui/static/js/console.js`: on `htmx:sseBeforeMessage`
  from `[data-log-sink]`, cancel the swap and handle it: `logs-reset` clears the list; `log`
  parses the JSON and appends a line element built with `textContent` only (FR-018), with the
  copy's short ID and a stderr mark; keep at most 5,000 lines (say "Older lines were dropped."
  once); follow the bottom unless paused or scrolled up; Pause/Resume buttons with the "Paused.
  12 new lines." counter; Wrap toggles a class; both set `aria-pressed`. Log view styles in
  `internal/ui/style/input.css`; run `tools/ui-css.sh build`.
- [X] T062 [US5] Add `"read-logs"` to `deploy/install/caravana-policy.hcl` and update its comment
  (research R10); `host.sh up` re-applies it (check `deploy/vm/setup.sh` applies the policy on
  every run).

**Checkpoint**: logs without the Nomad CLI.

---

## Phase 8: User Story 6 - Check configuration, the webhook, or delete the app (Priority: P6)

**Goal**: the Configure tab (read-only definition, webhook token, delete) and
`DELETE /v1/apps/{id}`.

**Independent Test**: compare Configure with `GET /v1/apps/{id}`; create a webhook token, see
it once, call it; revoke it; delete a test app by typing its name and see it gone from the
page, Nomad and `/v1/apps`.

### Tests for User Story 6

- [X] T063 [P] [US6] Fake Nomad purge in `internal/deploy/deploytest/nomad.go`
  (`DELETE /v1/job/{id}?purge=true`, `Purged(jobID) bool`, removes the job and its copies) and
  `internal/deploy/purge_test.go`: purge works; an unknown job is not an error; Nomad down →
  `ErrNomadUnavailable`.
- [X] T064 [P] [US6] Store test in `internal/store/apps_test.go`: `DeleteApp` removes the
  webhook token, deployments and app in one transaction; unknown ID → `ErrNotFound`; another
  app's rows are untouched.
- [X] T065 [P] [US6] Service tests in `internal/service/apps_test.go`: `DeleteApp` purges in
  Nomad first, then the store; Nomad down → error and everything still stored; a rollout watcher
  for the app stops; a later `Redeploy` → `store.ErrNotFound`; a `deleted` notice reaches a
  subscriber; the ID can be created again afterwards.
- [X] T066 [P] [US6] API tests in `internal/api/apps_test.go`: `DELETE /v1/apps/{id}` → 204,
  then `GET` 404; unknown → 404; Nomad down → 502 `nomad_unavailable` and the app still there;
  403 cross-site with a cookie.
- [X] T067 [P] [US6] UI tests in `internal/ui/configure_test.go`: every stored field is shown;
  env var names shown, values never present in the HTML; webhook: none → "Create token"; create
  → `200` with the full URL once, a reload doesn't show it; replace and revoke sit behind
  `<details>` and work as plain posts; delete: wrong name → `422` with "Type `blog` to
  confirm." and nothing deleted; right name → `303` to `/apps?deleted=blog`; Nomad down →
  `502` page with the sentence and the app kept; another tab's `/live/apps/blog` receives
  `deleted`.

### Implementation for User Story 6

- [X] T068 [US6] `internal/deploy/purge.go`: `(*Deployer).Purge` (`Jobs().Deregister(id, true,
  nil)`; a 404 is success).
- [X] T069 [US6] `store.DeleteApp` in `internal/store/apps.go` (one transaction, children
  first).
- [X] T070 [US6] `service.DeleteApp` in `internal/service/apps.go` and the watcher cancel in
  `internal/service/deploy.go`: `appState` keeps a cancel func for its watchers; `DeleteApp`
  takes the app's lock, cancels them, purges, deletes from the store, drops the `appState`, and
  publishes `deleted`, `apps`, `activity` (research R16).
- [X] T071 [US6] API `deleteApp` in `internal/api/apps.go`, route, and the `delete` operation
  in `api/openapi.yaml` from contracts/api.md.
- [X] T072 [US6] Configure tab: `configure` define in `internal/ui/templates/app.html`
  (definition panels per service with env names only and the API hint; webhook section with
  `<details>` confirmations and the show-once panel with a `data-copy` button; Delete app last,
  apart, behind `<details>`, with the name field and danger button) and handlers in
  `internal/ui/app.go`: `GET /apps/{id}/configure`, `POST /apps/{id}/webhook`, `POST
  /apps/{id}/webhook/revoke`, `POST /apps/{id}/delete` (contracts/pages.md).
- [X] T073 [US6] Delete-name check in `internal/ui/static/js/console.js`: an input with
  `data-confirm-name="blog"` enables its form's submit button only when the value matches
  (the button starts enabled in the HTML so the form works without JavaScript; the script
  disables it on load). Styles for Configure panels and the danger zone in
  `internal/ui/style/input.css`; run `tools/ui-css.sh build`.

**Checkpoint**: the app page is complete.

---

## Phase 9: User Story 7 - Account settings inside the console (Priority: P7)

**Goal**: `/settings` is the 006 account page inside the shell; `/account` leads there.

**Independent Test**: from the sidebar open Settings, create and revoke a token, change the
password, sign out; `/account` lands on `/settings`.

### Tests for User Story 7

- [X] T074 [P] [US7] Move the account tests in `internal/ui/ui_test.go` to `/settings` paths
  (keep every assertion), and add: `GET /account` → `301` to `/settings`; the page has the
  shell with Settings marked current; token revoke sits behind a `<details>` confirmation; the
  new token panel has a `data-copy` button.

### Implementation for User Story 7

- [X] T075 [US7] Rename `internal/ui/account.go` to `internal/ui/settings.go` and
  `internal/ui/templates/account.html` to `internal/ui/templates/settings.html` (`git mv`),
  change routes to `/settings`, `/settings/password`, `/settings/tokens`,
  `/settings/tokens/{tokenId}/revoke`, add `GET /account` → `301`, render it inside the shell
  per contracts/pages.md (Settings), and update `internal/ui/templates/login.html` or any link
  still pointing at `/account`. Run `tools/ui-css.sh build`.

**Checkpoint**: every spec story works.

---

## Phase 10: Polish & cross-cutting concerns

- [X] T076 [P] Narrow screens and accessibility pass (research R19, FR-029) over every
  template and `internal/ui/style/input.css`: the `<details>` menu under 768 px, stacked rows
  under 640 px, 16 px gutters, no horizontal scroll, 44 px touch targets on `pointer: coarse`,
  focus rings, `aria-current`, `aria-live` on the indicator and newest deployment, marks plus
  words for every status. Run `tools/ui-css.sh build`.
- [X] T077 [P] `internal/ui/sample_test.go` (`TestNoSampleData`, SC-007): render every page
  and part against an empty store and a populated one (fake Nomad copies, deployments of every
  status) and assert each app name, version, digest and domain in the HTML comes from that data;
  fail on any name from a denylist of the handoff's sample names kept in the test as hashes
  (so the names themselves never enter the repo).
- [X] T078 [P] `internal/api/parity_test.go` (`TestConsoleParity`, SC-006): a table of every
  console route that reads or changes data → its `operationId`; the test fails if a route in
  `internal/ui`'s route list is missing from the table or names an operation not in
  `api/openapi.yaml`. Export the UI's route list for the test (`ui.Routes()`), or keep the table
  in `internal/ui` and check operation IDs there.
- [X] T079 [P] Extend `TestPagesAskNothingOfOtherHosts` and `TestSecurityHeaders` in
  `internal/ui/ui_test.go` to every console page and the JS files; add a test that scans
  `internal/ui/templates/*.html` for `hx-on`, `style=`, `<style` and inline `<script>` (R3).
- [X] T080 [P] Forms-without-JavaScript test in `internal/ui/ui_test.go` (extend
  `TestAccountFormsWorkWithoutJavaScript`): New app, Deploy, webhook create/revoke, delete and
  Settings forms each succeed as plain `application/x-www-form-urlencoded` posts with only
  same-origin headers.
- [X] T081 `TestLab` console steps in `cmd/caravana/lab_test.go` (research R21): sign in through
  `POST /login`, create whoami through `POST /apps`, read `/live/apps/whoami` and get a
  `deployments` event within 5 s, wait for healthy, read a log line through
  `GET /v1/apps/whoami/logs` after calling the app, deploy a changed image through
  `POST /apps/whoami/deploy`, delete it through `POST /apps/whoami/delete` and see the Nomad
  job gone (`GET /v1/job/whoami` → 404 with the lab token).
- [X] T082 Gate step `console-reads` in `runGate` (`cmd/caravana/gate_test.go`): with the
  scoped token in place, `GET /v1/apps` lists whoami, `GET /v1/apps/whoami/copies` has a healthy
  copy, `GET /v1/apps/whoami/logs?lines=10` answers 200 (fails on a missing `read-logs`).
- [X] T083 Impeccable pass: `/impeccable critique` on the console pages, apply what fits
  DESIGN.md with `/impeccable polish`, then `/impeccable document` so `DESIGN.md` describes the
  built console: the "Deployments" tab name (clarification Q2), the shell and narrow-screen
  rules, the live indicator, the log view, confirmations as disclosures. Run
  `tools/ui-css.sh build`.
- [X] T084 [P] Docs: `CLAUDE.md` (Where we are: 007 done; new commands `tools/ui-vendor.sh`;
  local console URL `http://127.0.0.1:7070/`), `docs/plan.md` (the open decision "SSE vs. HTMX
  polling" closed: SSE, research R1), and `deploy/vm/README.md` or `host.sh` help if they list
  the policy's capabilities.
- [ ] T085 Run everything in quickstart.md §1 and §2–3 by hand in the lab (record anything that
  didn't match in this file's Notes), then `CARAVANA_LAB=1 go test ./cmd/caravana -run TestLab`;
  `host.sh prove` when a VM is available (otherwise note it as pending, as 005 did).

---

## Dependencies & Execution Order

- **Setup (T001–T004)** → **Foundational (T005–T017)** → stories.
- Inside Foundational: T008 → T009 → T010 → T012; T006 → T011 → T012; T013 → T014 → T016;
  T005, T007, T015 are independent; T016 needs T005, T006, T007; T017 needs T016.
- **US1 (P1)** needs Foundational. It builds the Apps page and the app page frame every later
  story adds a tab to, so **US2–US6 depend on US1** (T027 in particular).
- **US2, US3, US4, US5, US6** are independent of each other once US1 is done; they touch the
  same files (`internal/ui/app.go`, `internal/ui/templates/app.html`, `api/openapi.yaml`,
  `internal/ui/style/input.css`), so run them one after another unless you split those edits
  carefully. Suggested order is priority order.
- **US7** needs only Foundational (the shell); it can run any time after T014.
- **Polish (T076–T085)** after the stories it covers; T081–T082 after US3, US5 and US6.

### Parallel opportunities

- Setup: T002, T003, T004 together (T001 too).
- Foundational: T005, T006, T007, T008, T015 together.
- Each story's test tasks marked [P] run together before its implementation (different test
  files).
- Polish: T076–T080 and T084 together.

### Parallel example: User Story 1

```text
T018 store tests    T019 service status tests    T020 API tests    T021 UI tests
        ↓ (all failing)
T022 store → T023 service → T024 API      (T025 view helpers alongside)
                         ↓
               T026 Apps page → T027 app frame → T028 styles
```

## Implementation Strategy

- **MVP**: Setup + Foundational + US1. That's a working, live, read-only console: what runs,
  how healthy it is, what happened recently. Stop and check it with quickstart §2 steps 1, 4,
  11 and 12 before going on.
- **Then** US2 (deploy from the console) and US3 (create apps), which make the console usable
  on its own; then US4 and US5 (copies and logs, the debugging pair); then US6 and US7.
- After each story: `go test -race ./...`, `go vet`, staticcheck, `gofmt -l .`,
  `tools/ui-css.sh check`, `tools/ui-vendor.sh check`, and the arm64 cross-compile. Commit per
  story, with the contract and routes in the same commit.
- Explain new Go idioms in each story's summary (plan, "Go idioms to explain").

## Notes

- T085 is left open on purpose. Done: quickstart §1 (all automated checks) and §4's lab test
  (`TestLab` and `TestConsoleLab` pass, including `console-reads` with the lab's management
  token). Not done: the by-hand browser steps of §2–3 (7 stop a copy, 10 second-profile sign-out,
  11 stop the lab, 12 restart the server, JavaScript off, keyboard-only, reduced motion) and
  `host.sh prove` (no VM; as 005 did, pending). Headless Chrome covered layout at 1440/900/768/390.
- Found while building: `DELETE /v1/apps/{id}` purged a Nomad job of the same name that Caravana
  didn't create. `deploy.Purge` now takes the same `owns` check as `Register` and leaves such a
  job alone (test in `purge_test.go` and `service/apps_test.go`).
- Open decision (US3): `CreateApp` saves the app before the image resolves (R14), so a failed
  first deploy leaves the app stored and re-posting the form says the name exists. The page
  shows a notice with a link to Deployments. Options: resolve the image first (one more registry
  call, 422 without `Location`), or remove the app when the first deploy never registered.
- Not canonized in DESIGN.md: the Replace-token confirmation's primary button can make two
  primary buttons while it is open on Configure.
- The app header still says "healthy" when copies are healthy but the newest deploy failed; only
  the Apps headline, row order and link say "needs you".
