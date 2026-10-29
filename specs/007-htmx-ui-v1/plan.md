# Implementation Plan: HTMX UI v1

**Branch**: `007-htmx-ui-v1` | **Date**: 2026-10-04 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from `specs/007-htmx-ui-v1/spec.md`

## Summary

The Developer console for what the engine does today: an Apps page (headline sentence, apps
table, activity), an app page with Running, Deployments, Configure and Logs tabs, New app from
an image, and Settings (the 006 account page moved into the shell). Server-rendered
`html/template` pages, full-page navigation, plain forms first.

Live updates use **one server-sent events stream per page** that only says what changed. Each
region then fetches its own fragment with htmx (R1). On the Logs tab the same stream carries log
lines, which a small vanilla `console.js` renders as text. The vendored client code is htmx
2.0.11 plus its SSE extension 2.2.4. **Alpine.js was weighed and left out** (R2): our CSP needs
its restricted build, and it adds weight and swap-state problems for four small behaviors.

Change notices come from an in-process hub in `internal/service`. The deploy flow and app
changes publish to it, as does one blocking-query watcher over Nomad allocations (R5, R6). App
status combines the store's deployments with Nomad's copies (R7).

New API operations, each a twin of a console feature (constitution V):
- `GET /v1/apps`
- `POST /v1/apps` (create only)
- `DELETE /v1/apps/{id}`
- `GET /v1/deployments`
- `GET /v1/apps/{id}/copies`
- `GET /v1/apps/{id}/logs` (JSON, or an event stream with `follow=true`)

Deployments gain a `console` trigger and live rollout progress. Streams never renew the session
(R11). The VM's Nomad policy gains `read-logs`, which the gate checks.

## Technical Context

**Language/Version**: Go 1.27 (go1.27.1)

**Primary Dependencies**: standard library (`net/http`, `http.ResponseController`,
`html/template`, `embed`, `encoding/json`, `unicode/utf8`); `github.com/hashicorp/nomad/api`
(existing). It gains `Allocations().List` (blocking), `Jobs().Allocations`, `Allocations().Info`,
`AllocFS().Logs` and `Jobs().Deregister` with purge. **No new Go module.** Vendored browser files,
pinned by version and SHA-256 (R4): htmx 2.0.11 and htmx-ext-sse 2.2.4. `console.js` is ours.

**Storage**: SQLite; migration `0004_console.sql` (one index). No table changes.

**Testing**: `testing` + `httptest`, with a real store in `t.TempDir()`. The fake Nomad
(`deploytest`) gains allocations, logs and purge. Tests cover status and copy mapping tables, the
hub, stream protocol tests that read `text/event-stream`, the contract test plus a console↔API
parity test, a no-sample-data render test, `TestLab` console steps, and a `console-reads` gate
step.

**Target Platform**: linux/amd64 and linux/arm64, CGO off; macOS + Lima lab for development.
Browsers: current Chrome, Firefox, Safari (desktop and phone).

**Project Type**: single Go binary (control plane) serving an API, pages and a CLI.

**Performance Goals**:
- Status changes reach open pages within 5 s (SC-004); log lines within 3 s (SC-005).
- The Apps page costs one store read and one Nomad request, whatever the number of apps.
- Notices are coalesced to at most one per region every 500 ms.

**Constraints**:
- CSP unchanged (`default-src 'self'`): no eval, no inline script or style, no external
  requests (FR-030).
- Pages work without JavaScript (FR-027).
- One stream per page, because of the HTTP/1.1 limit of 6 connections.
- Streams don't renew sessions (FR-003).
- No log contents, tokens or env values in the request log (FR-028).
- Works at 390 px (FR-029).

**Scale/Scope**:
- 6 new API operations, 1 changed schema, 1 new error code, 1 migration.
- 7 pages and 5 parts, 2 stream routes.
- About 9 templates, 3 vendored or own JS files, 1 tools script, 1 Nomad policy line.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | How this plan complies | Status |
| --- | --- | --- |
| I. Small by Default | No new runtime service and no new Go module. Two vendored scripts (about 55 KB, 20 KB gzipped) are served from the binary. The Nomad watcher runs only while someone is watching (R6). | ✅ |
| II. One Model | `app.App` and `nomadjob.Translate` don't change. Only `app.Deployment`'s trigger gains `console`. New app and the image change both build an `app.App` and use the existing deploy flow. | ✅ |
| III. Safe, Reversible Deploys | Every console deploy is the normal flow: digests pinned, new job version, health-checked, auto-revert. Changing an image sends the full stored app with only that image replaced (declarative). Delete is explicit, confirmed by typing the name, and erases nothing if Nomad fails. | ✅ |
| IV. Fail Loudly | Form errors land on the field the `app.FieldError` path names. Nomad being down shows `unknown` and says so; it never shows an empty list as if nothing ran. Logs are never silently cut off: the page says when lines were dropped above 5,000. | ✅ |
| V. API-First | Every console read and action has a `/v1` twin (contracts/api.md table), enforced by a parity test. `api/openapi.yaml` changes in the same commits as the routes. Both front doors call `internal/service`. `/live` and `/parts` are presentation transports for data the API already exposes, not features. The API never redirects. | ✅ |
| VI. Nomad-Native | Nomad calls stay in `internal/deploy`, which returns plain types (`Copy`, `Progress`, `LogLine`). It uses the official client. Delete purges only the job Caravana registered. Uninstalling Caravana still leaves workloads running. | ✅ |
| VII. Secure by Default | Env values are never shown, log contents are never logged, tokens are shown once. Streams re-check the session every 30 s and never renew it. The CSP is unchanged, and htmx runs with eval off. Cross-origin protection covers every POST. The Nomad token gains only `read-logs`. No secrets go to Nomad Variables (none are created). | ✅ |
| VIII. Plain, Tested Go | Table tests and the fake Nomad cover each piece. `ctx` comes first for every I/O call, and errors are wrapped. No new interface: the hub is a concrete type, `deploy.Deployer` stays concrete. `console.js` is plain, commented and short. | ✅ |
| Tech: `html/template` + HTMX, `embed`, no SPA, no Node | Pages are templates with embedded assets. htmx is vendored, there's no build step, no client router, and navigation is full-page. | ✅ |
| Tech: dev tools pinned with the `tool` directive | Vendored JS files aren't Go tools, so they're pinned by version and SHA-256 in `tools/ui-vendor.versions`, as 006 did for Tailwind. | ⚠️ see Complexity Tracking |
| Development Workflow | 007 is the next item in `docs/plan.md`. Out-of-scope screens stay out. The UI is built from our own Claude Design handoff, with no code or copy from other PaaS projects. | ✅ |

**Post-design re-check**: still passes, with the one tracked deviation. Worth knowing:

- **Spec refinements during planning** (applied to spec.md):
  - FR-025 now also names `POST /v1/apps` (create only), the API twin of New app (R14).
  - FR-012 notes that rollout progress also shows on the API's `Deployment`.
- **Nomad ACL**: the lab uses a management token, so a missing capability only shows on the VM.
  The new gate step `console-reads` reads copies and logs with the scoped token (R10, R21).

## Project Structure

### Documentation (this feature)

```text
specs/007-htmx-ui-v1/
├── plan.md
├── research.md            # R1–R21
├── data-model.md          # migration, derived entities, notices, stream states, validation
├── quickstart.md
├── contracts/
│   ├── api.md             # additions to api/openapi.yaml (0.5.0)
│   ├── pages.md           # routes, parts, streams, page content
│   └── go-packages.md
├── checklists/requirements.md
└── tasks.md               # /speckit-tasks
```

### Source Code (repository root)

```text
api/openapi.yaml                         # 0.5.0: six operations, schemas, app_exists, console trigger

cmd/caravana/
├── main.go                              # ui.New gets the Service; BaseContext cancelled on shutdown
├── main_test.go                         # streams end on shutdown
├── gate_test.go                         # + console-reads step
└── lab_test.go                          # + console steps (form create, live notice, logs, image change, delete)

internal/app/
├── deployment.go                        # + TriggerConsole
└── validate.go                          # accepts console

internal/store/
├── migrations/0004_console.sql          # NEW: deployments_by_time index
├── apps.go                              # + InsertApp, ListApps, DeleteApp
├── deployments.go                       # + LatestDeployments, ListRecentDeployments, ListDeploymentsBefore
└── *_test.go

internal/deploy/
├── deploy.go                            # ClientConnTimeout
├── copies.go                            # NEW: Copy, Copies, AllCopies, WatchCopies
├── progress.go                          # NEW: Progress
├── logs.go                              # NEW: Logs, line splitting
├── purge.go                             # NEW: Purge
├── deploytest/nomad.go                  # + allocations (blocking), logs, purge, helpers
└── *_test.go

internal/service/
├── changes.go                           # NEW: Change, Subscribe, publish, coalescing helper
├── apps.go                              # NEW: ListApps, AppSummary, status (R7), CreateApp, DeleteApp
├── copies.go                            # NEW: Copies, Progress, Logs (merge, tail)
├── deploy.go                            # publish notices on writes; watcher cancel per app
├── sessions.go                          # + CheckSessionQuiet
└── *_test.go

internal/httpx/
├── stream.go                            # NEW: Stream, EventWriter
└── stream_test.go

internal/api/
├── api.go                               # six routes; route.quiet
├── apps.go                              # + listApps, createApp, deleteApp
├── deployments.go                       # + listRecentDeployments; progress on Deployment; console trigger
├── copies.go                            # NEW: listCopies, getLogs (JSON | event stream)
├── errors.go                            # + app_exists
├── parity_test.go                       # NEW: console action ↔ operationId table (SC-006)
└── *_test.go

internal/ui/
├── ui.go                                # routes (pages, parts, live), requirePart, asset hashes
├── apps.go, app.go, newapp.go           # NEW: handlers
├── settings.go                          # renamed from account.go; /account → /settings
├── live.go                              # NEW: /live streams (sync, notices, logs, ended)
├── view.go                              # NEW: view models, sentences, relative times
├── templates/{base,shell,parts,apps,app,newapp,settings,login,message}.html
├── style/input.css                      # + console styles (shell, tables, log view, narrow screens)
├── static/app.css                       # regenerated
├── static/js/{htmx.min.js,sse.min.js,console.js,LICENSES.md}
└── *_test.go                            # pages, parts, forms without JS, streams, no sample data

tools/
├── ui-vendor.sh                         # NEW: fetch | check
└── ui-vendor.versions                   # NEW

deploy/install/caravana-policy.hcl            # + read-logs
.github/workflows/ci.yml                 # + tools/ui-vendor.sh check
DESIGN.md                                # tabs (Deployments), narrow-screen rules, live indicator, log view
CLAUDE.md, docs/plan.md                  # where we are; the SSE decision closes the open item
```

**Structure Decision**: no new packages. The console grows inside `internal/ui`. Nomad reads go
in `internal/deploy`, the only package besides `nomadjob` that knows Nomad. Status, notices and
log merging go in `internal/service`, which the API and the pages share. The stream writer goes in
`internal/httpx`, next to the other helpers both front doors use.

## Implementation notes for /speckit-tasks

- **Order**:
  1. Foundations: `TriggerConsole`, migration and store methods, `httpx.Stream`, the hub,
     `CheckSessionQuiet`, `tools/ui-vendor.sh` and the vendored files, the shell template,
     `console.js` skeleton (live indicator).
  2. US1: list and status, `GET /v1/apps`, `GET /v1/deployments`, the Apps page, parts, `/live`,
     the Nomad watcher.
  3. US2: progress, the deploy form with image change, history, explanations.
  4. US3: `CreateApp`, `POST /v1/apps`, New app.
  5. US4: copies, `GET /v1/apps/{id}/copies`, Running.
  6. US5: logs in `deploy` and `service`, `GET /v1/apps/{id}/logs`, the Logs tab, `console.js` log
     view, `read-logs`.
  7. US6: Configure, webhook UI, `DeleteApp`, `DELETE /v1/apps/{id}`.
  8. US7: Settings, `/account` redirect.
  9. Cross-cutting: narrow screens, accessibility pass, no-JS pass, parity and no-sample-data
     tests.
  10. Lab and gate.
  11. Polish: DESIGN.md, CLAUDE.md, `docs/plan.md`.
- **The contract grows per story**, as in 003–006: the route table and `api/openapi.yaml` stay
  equal at every step, or the sync test fails.
- **Design**: before the templates, read DESIGN.md, PRODUCT.md and the local handoff screens
  listed in research R20 (`design/console/design/ui_kits/console/`). Copy no sample data (app
  names, emails, domains) into tracked files or tests: tests use `whoami`, `blog`,
  `example.com`. After the pages work, run `/impeccable critique` then `/impeccable polish` once
  on the console, then `/impeccable document` so DESIGN.md describes the built CSS.
- **Pinned versions**: the task that writes `tools/ui-vendor.versions` downloads htmx 2.0.11 and
  htmx-ext-sse 2.2.4 from the npm registry's tarballs and records their SHA-256 sums. If a newer
  2.x patch is `latest` by then, use it and say so in the commit.
- **Go idioms to explain** (C# comparisons in the summary):
  - channels and non-blocking `select` sends for the hub (C#: `Channel<T>` with a bounded drop
    mode);
  - the `for { select { … } }` loop of a stream (C#: `await Task.WhenAny`);
  - `http.ResponseController` and `Flush` (C#: `Response.Body.FlushAsync`);
  - `BaseContext` and cancelling it on shutdown (C#: `IHostApplicationLifetime.ApplicationStopping`);
  - `context.WithoutCancel` (already used);
  - type aliases (`type Copy = deploy.Copy`, C#: `using Copy = …`);
  - SQL window functions for "newest per app" (same as in SQL Server).
- **Commit message notes**: no new Go module; the vendored files and their pinning; the new
  Nomad capability `read-logs` and why; the CSP is unchanged.

## Complexity Tracking

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| htmx and its SSE extension pinned by version + SHA-256 in `tools/ui-vendor.versions`, not by the `tool` directive in `go.mod` | The constitution names HTMX for the UI. It's a browser file, not a Go program, so `go.mod` can't pin it. It's committed and embedded, `go build` never fetches it, and CI's `check` proves the committed bytes are the pinned release. | A CDN: external requests from the pages (FR-030, CSP). npm: a Node step (constitution). Copying files by hand with no checksum: nothing proves what they are. |
