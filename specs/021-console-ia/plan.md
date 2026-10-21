# Implementation Plan: Console IA, the design handoff's shape

**Branch**: `021-console-ia` | **Date**: 2026-10-21 | **Spec**: [console-ia.spec.md](./console-ia.spec.md)
| **Page 1 contract**: [contracts/page1-shell.md](./contracts/page1-shell.md)

## Summary

The console is put on the handoff's shape page by page, each page its own release. Page 1 covers
the sidebar and top bar, a `/projects` page, and the app header with a copies line per service.
The only data change is per-service copy counts on `AppStatus`, which the API also exposes
(additive; openapi 0.22.0). Pages 2–4 get their contracts from the designer when each starts.

## Technical context (page 1)

- **Templates:** `internal/ui/templates/shell.html` (the sidebar, top bar and `action` slot),
  `app.html` (the app header, tabs) and `base.html`. The styles are in `internal/ui/style/`, and
  the stylesheet is generated (`tools/ui-css.sh build`, committed `static/app.css`, CI runs
  `check`).
- **Status:**
  - `service.statusOf` (`internal/service/apps.go:81`) computes `AppStatus` from the app's
    services, the latest deployment and the copies. Per-service counts are added there, as
    `Services []ServiceStatus{Name, Wanted, Healthy}` in the app's service order.
  - `internal/api/apps.go` maps it to the API's `AppStatus`, which gains `services` (an array of
    `{name, wantedCopies, healthyCopies}`, optional), so constitution V holds. Bump openapi to
    0.22.0.
- **The copies sentence:** `summaryOf` in `internal/ui/view.go:395` keeps the state switch. Only
  the healthy and degraded counts change, to a pure helper for the five shapes in the contract,
  with table tests.
- **Projects:** `/projects` (GET) lists projects with environments and app counts. The service
  already lists projects for the API (`listProjects`); reuse it.
- **Icons:** a template function `icon "house"` returns `template.HTML` from a small map of Lucide
  path data (ISC; LICENSES.md entry). There is no fetch, no font and no new Go module.
- **Active nav:** decided from the request path in the shared page data.
- **Account row:** the signed-in admin's email is already known to the session (settings shows it).
  Sign-out stays `POST /logout`.

## Constitution Check

| Principle | Assessment |
| --- | --- |
| I. Small | PASS. No module; inline SVG paths only. |
| IV. Fail loudly | Not touched. |
| V. API-First | PASS. The per-service counts the console shows are in the API too (`AppStatus.services`, openapi 0.22.0, same commit). `/projects` uses the existing service call. |
| VII. Secure | PASS. Sign-out is still a POST; no new endpoint without auth. |
| VIII. Plain Go | PASS. A pure sentence helper with table tests; handler tests for the shell on each page kind. |
| Technology (html/template + HTMX, no SPA, no Node step) | PASS. CSS-only states; works without JavaScript. |

## Testing (page 1)

- **`view.go`:** table tests for the copies sentence: one service, 1 and 2 copies; several with
  different counts; several with the same count (2 and 3 services); a short service; Nomad
  unreachable unchanged.
- **`service`:** `statusOf` fills `Services` in order, with healthy counted per service, live
  version only.
- **`api`:** `AppStatus.services` in `getApp` and `listApps`, valid against the schema (the
  contract test).
- **`ui` handler tests:**
  - every page renders the sidebar with exactly Home, Projects and Settings, and the right one
    active (`/apps`, `/projects`, `/projects/default`, `/apps/{id}`, `/apps/{id}/deployments`,
    `/settings`);
  - the account row's sign-out form posts to `/logout`;
  - "New app" is in the top bar on every page;
  - the crumbs for an app page, Settings and New app;
  - `/projects` lists projects with environment pills and app counts;
  - the app header for running (Restart, Stop…, Deploy as a link to the deploy form) and stopped
    (Start, no Deploy);
  - no `cv-where` line, no `cv-actions` row.
- **`tools/ui-css.sh check`** passes after the build.
- **Lab:** the console steps that read the app page and Settings still pass (`TestConsoleLab`).
  Update their selectors where the header moved.
- **The designer** reviews the page live, state by state; then an `impeccable` critique and polish
  pass.

## Complexity Tracking

None.
