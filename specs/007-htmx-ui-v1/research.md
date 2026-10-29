# Research: HTMX UI v1

Decisions for feature 007, each with why and what else was considered. R-numbers are cited
from the plan, the data model and the contracts. Library facts were checked against the npm
registry and the libraries' sources on 2026-10-04.

## R1. Live updates: server-sent events that say "this changed", then htmx fetches it

**Decision**: each console page opens **one** server-sent events (SSE) stream (`GET /live`,
`GET /live/apps/{id}`). The stream carries only short notices naming what changed (`apps`,
`activity`, `app`, `deployments`, `copies`, `deleted`), never page content. Each live region of
the page listens for its notice (`hx-trigger="sse:deployments"`) and fetches its own fragment
from a plain GET route (`/parts/...`, contracts/pages.md), rendered by the same template the
full page uses. When the stream (re)connects, the server first sends `sync`; every region also
listens for it, so after a reconnect every region fetches itself once and is current (FR-024)
without the server keeping per-client history or event IDs. The server coalesces notices: at
most one per name every 500 ms.

**Why**: one rendering path per region (the page and the refresh use the same template), no
ordering or duplication problems, catch-up after a drop is free, and the stream stays tiny. SSE
is plain HTTP, one direction, native in browsers (`EventSource` reconnects by itself), and
`net/http` can serve it with no new module. The open decision in `docs/plan.md` (SSE vs.
polling) closes here.

**Alternatives**: HTMX polling every 2–5 s (simpler server, but constant requests from every
open tab, and logs need a stream anyway); SSE carrying rendered HTML (`sse-swap`) (two
rendering paths per region, and a reconnect needs replay or a full reload); WebSockets
(two-way isn't needed; `gorilla/websocket` is only an indirect module today and would become a
direct one).

## R2. Client-side code: htmx 2 + its SSE extension + one small vanilla file. No Alpine.js

**Decision**: three vendored files, served from the binary:

- `htmx.min.js` **2.0.11** (htmx.org `latest`; 52 KB, 17 KB gzipped),
- `sse.min.js` **2.2.4** (htmx-ext-sse; 3 KB),
- `console.js`, ours, about 150 lines, no framework: event delegation on `data-*` attributes for
  copy-to-clipboard, the live indicator, the log view (render, follow, pause, wrap, line cap)
  and the delete confirmation's enable rule.

Tabs need no script: each tab is a link to its own URL (FR-010). Show-once values need none:
the server renders them only in the response that created them. Disclosures and confirmations
are `<details>` (R16). Alpine.js was weighed, as the maintainer asked, and is **not** used.

**Why not Alpine**:

1. Our pages send `Content-Security-Policy: default-src 'self'` (006, R12). Standard Alpine
   evaluates expressions with `new Function`, which that policy blocks. Only the CSP build
   (`@alpinejs/csp` 3.17.4) works, and it bans arrow functions, template literals and globals in
   templates. Real logic has to move into `Alpine.data()` components in a file anyway.
2. It weighs 24 KB gzipped, more than htmx itself, for four small behaviors.
3. htmx swaps throw away Alpine component state unless a morph swap is added (another
   extension), and htmx's history cache can restore DOM that Alpine had already changed.
4. Two ways of writing behavior (htmx attributes and Alpine expressions) is more for the
   maintainer to learn than one small plain file.

Revisit if a later screen needs real client-side state (for example the compose editor in 009).
The vendoring script (R4) makes adding it a one-line change.

**htmx version**: htmx 4.0 came out on 2026-08-28 but is tagged `next`, and its maintainers keep
2.x as `latest` until early 2027. 007 uses 2.0.11; moving to 4 is a later, separate change.

## R3. htmx under our Content Security Policy

**Decision**: keep the 006 policy unchanged (`default-src 'self'; frame-ancestors 'none';
form-action 'self'; base-uri 'none'`). Configure htmx with a meta tag, which needs no inline
script:

```html
<meta name="htmx-config" content='{"allowEval":false,"allowScriptTags":false,"includeIndicatorStyles":false,"historyCacheSize":0}'>
```

Rules this sets for the templates: no `hx-on:*` attributes, no trigger filters
(`click[ctrlKey]`), no `js:` values in `hx-vals`; no inline `style="..."` attributes or
`<style>` blocks (the policy blocks them; every style is a class in `app.css`); the
`.htmx-indicator` and `.htmx-request` rules live in `internal/ui/style/input.css`.
`historyCacheSize: 0` because pages are full loads (R13) and stale snapshots would show old
status.

**Why**: `allowEval:false` routes every eval path in htmx to an error event instead; with the
indicator styles off, htmx injects nothing the policy would block. A test asserts the header
is unchanged and that no template has `hx-on`, `style=` or `<script>` without `src`.

**Alternatives**: adding `'unsafe-eval'` or nonces (weakens the one header that keeps injected
markup from running).

## R4. Vendoring the scripts

**Decision**: `tools/ui-vendor.sh fetch|check` with `tools/ui-vendor.versions` (name, version,
URL, SHA-256). `fetch` downloads the pinned files from the npm registry's tarballs, verifies the
checksums and writes them to `internal/ui/static/js/`. The files are committed, like `app.css`.
`check` verifies the committed files' checksums (CI runs it). Each file keeps its license
banner; `internal/ui/static/js/LICENSES.md` lists both (htmx: 0BSD).

All static files (CSS, JS, fonts) get the content-hash cache-buster that `app.css` already has
(`?v=<hash>`), computed per file at startup.

**Why**: same pattern as `tools/ui-css.sh` (006, R13), so the maintainer has one model: pinned,
checksummed, committed, checked in CI. `go build` never downloads anything.

**Alternatives**: a CDN (external requests from the pages, refused by FR-030 and the CSP);
copying files by hand (nothing proves they're what they claim to be).

## R5. Where change notices come from: `service.Changes`

**Decision**: a small in-process hub in `internal/service` (`changes.go`). Anyone can
`Subscribe(appID)` (or all apps) and gets a channel of `Change{AppID, What}` values; slow
subscribers drop notices instead of blocking (a dropped notice is harmless: the next one, or
the coalescing timer, refreshes the region). Publishers:

- the deploy flow, after each deployment record write (`deployments`, `app`, `activity`,
  `apps`),
- `SaveApp`/`CreateApp`/`DeleteApp` (`app`, `apps`, `deleted`),
- the Nomad watcher (R6) for copies (`copies`, `app`, `apps`).

**Why**: the service already knows every store write, so notices come from the one place all
front doors share (constitution V); no polling of SQLite. A buffered channel per subscriber with
non-blocking sends is the plain Go way to fan out (C#: `Channel<T>` with `BoundedChannelFullMode
.DropWrite`).

**Alternatives**: SQLite polling (wasteful and slower); an external broker (a new runtime
dependency, constitution I).

## R6. Following Nomad: one blocking query over allocations

**Decision**: `deploy.WatchCopies(ctx, fn)` runs one loop for the whole process, started by
`Service.Start`: a blocking query on `GET /v1/allocations` (namespace `default`, `WaitIndex`, 5
minute wait), the same pattern `deploy.Watch` already uses, with the same backoff. After each
answer it compares each allocation's `ModifyIndex` with the last answer and calls `fn` with the
job IDs that changed (added, changed or gone). The service turns those into `copies` notices.
The watcher only runs while at least one page or API stream is subscribed (the hub starts it on
the first subscriber and stops it 30 s after the last one leaves).

**Why**: allocations change when copies start, stop, turn healthy or restart, and Nomad's
deployment health lives on the allocation too (`DeploymentStatus.Healthy`), so one query covers
copies, app status and rollout progress. It reuses a pattern the maintainer already knows, and
the fake Nomad (`deploytest`) only needs one more list endpoint with an index. On one node the
list is a few dozen stubs.

**Alternatives**: Nomad's event stream (`/v1/event/stream`, topics Allocation and Deployment):
precise, but newline-delimited streaming the fake Nomad would have to imitate, and it needs the
same ACL; per-app blocking queries (one goroutine per open app page).

## R7. App status: what's true now

**Decision**: `service` derives an app's status from the store and Nomad together (FR-009):

| Status | When |
| --- | --- |
| `rolling_out` | the app's latest deployment is `pending` or `running` |
| `healthy` | healthy copies of the live version ≥ wanted copies |
| `degraded` | 0 < healthy copies < wanted copies (the sentence gives the counts) |
| `not_running` | no copy running and at least one wanted |
| `unknown` | Nomad couldn't be read |

*Wanted* is the sum of the stored app's replicas per service. A copy counts as *healthy* when
Nomad runs it and, if it belongs to a deployment, Nomad marked it healthy
(`DeploymentStatus.Healthy == true`); a copy without deployment status that is running counts as
healthy (Nomad sets health for every task group in a deployment, with or without checks). The
*live version* is the job version the healthy copies run. Beside the status, the last deployment
shows when its outcome differs from "healthy" (for example "v6 reverted").

The headline sentence counts these: "3 apps healthy." / "2 apps healthy, 1 rolling out." /
"1 app needs you: `api` has 1 of 2 copies healthy." / "No apps yet." / "Nomad can't be reached;
showing what caravana last recorded."

**Why**: the store alone can say "v6 healthy" while a copy has since crashed; Nomad alone
doesn't know about failed deploys that never registered. The one list call over all allocations
(R6) serves the whole Apps page, so it costs one Nomad request, not one per app.

**Alternatives**: store-only status (lies after a crash); Nomad job summary only (no deploy
outcomes, no "rolling out" before registration).

## R8. Copies

**Decision**: `deploy.Copies(ctx, jobID)` returns plain values (no Nomad types leave `deploy`,
constitution VI): ID, service (task group), node name, job version, status, healthy (yes, no,
not yet), restarts (sum over the task's restarts), started at, and the client description.
Mapping:

| Nomad | Copy status |
| --- | --- |
| client `pending` | `starting` |
| client `running`, deployment health not set yet | `starting` |
| client `running`, healthy or no deployment status | `healthy` |
| client `running`, `Healthy == false` | `unhealthy` |
| client `complete` (desired stop) | `stopped` |
| client `failed` or `lost` | `failed` |

The Running tab and `GET /v1/apps/{id}/copies` list every non-terminal copy plus the terminal
copies of the app's latest job version that Nomad still has (so a failed deploy's crashed copies
are visible, User Story 4 scenario 3, and their logs readable, FR-017).

## R9. Rollout progress

**Decision**: `deploy.Progress(ctx, jobID, jobVersion)` reads the Nomad deployment for that
version (`Jobs().Deployments`, as `Watch` does) and sums its task groups: wanted, placed,
healthy, unhealthy, canaries wanted and whether they're promoted. The Deployments tab shows it on
the running deployment only: "2 of 2 new copies started, 1 healthy. Old copies keep serving until
the new ones pass their checks." It refreshes on `copies` notices, since health changes are
allocation changes (R6).

**Alternatives**: storing progress in SQLite on each watch tick (writes for something only open
pages need).

## R10. Logs

**Decision**: `deploy.Logs(ctx, copyID, task, stream, follow, tailBytes)` wraps
`AllocFS().Logs` (origin `end`, offset `tailBytes`, `follow` as asked). It turns Nomad's byte
frames into lines: a partial last line is held until its newline arrives (or 64 KiB, then cut);
invalid UTF-8 is replaced; each line keeps the copy ID and stream. The service merges the copies
and streams the admin chose: on open, up to 64 KiB from the end of each (copy, stream) is read,
split into lines, and the last 500 lines overall are sent (Nomad's log lines carry no time, so
the tail is ordered per copy, oldest first, copies in start order); then new lines are
interleaved as they arrive, each stamped with the time caravana received it.

- **The UI**: the Logs tab's page stream (R1) also carries `log` events (JSON:
  `{copy, stream, text, time?}`) and a `logs-reset` event before the tail. `console.js` renders
  each line with `textContent` (FR-018: never markup), keeps at most 5,000 lines, follows the
  bottom unless the admin scrolled up or paused, counts lines that arrive while paused, and
  wraps on request. The copy and stream choice are query parameters on the stream URL; changing
  them reloads the stream.
- **The API**: `GET /v1/apps/{id}/logs?copy=&stream=&lines=` returns JSON lines;
  with `follow=true` it answers `text/event-stream` with the same `log` events (contracts).
- **Nomad ACL**: reading logs needs the `read-logs` capability. The lab uses the management
  token; the VM's `deploy/install/caravana-policy.hcl` gains `read-logs` (and `host.sh up` re-applies
  it). The gate gets a step that reads copies and logs through the API with the scoped token, so
  a missing capability fails the gate, not a user.
- **Reaching the node**: the Nomad client library first tries the node's own HTTP address, then
  the server. Single node, that's the same host; `NewNomadClient` sets a short
  `ClientConnTimeout` (1 s) so a node address that isn't reachable from caravana falls back fast.

**Alternatives**: storing logs in caravana (out of scope; Nomad keeps them with its own
rotation); a log shipper (a runtime dependency).

## R11. Streams don't renew the session

**Decision** (clarification Q4): `service.Auth` gains `CheckSessionQuiet(ctx, value)`, the same
check as `CheckSession` without `TouchSession`. Page and part requests keep using
`CheckSession` (they're the admin's own actions); `/live` streams and the API's log follow use
the quiet one. A stream re-checks the session every 30 s and ends when it's gone. Ending is a
clean `200` stream that sends `event: ended` and closes, because the SSE extension retries any
error status (a 401 included) forever; the page closes its stream on `ended`
(`sse-close="ended"`) and the live indicator says "Signed out. Sign in again." with a link. A
stream request without a valid session gets the same `ended` stream at once.

The API's `authenticate` gets a per-route flag, `quiet`, set on the log-follow route. A
token-authenticated stream checks the token's revocation and expiry every 30 s the same way.

## R12. Streaming plumbing in `net/http`

**Decision**: one helper, `httpx.Stream` (`stream.go`), used by `/live` and the API's log
follow:

- clears the write deadline (`http.NewResponseController(w).SetWriteDeadline(time.Time{})`):
  the server's 30 s `WriteTimeout` would otherwise cut every stream (C#: there's no single
  equivalent; Kestrel's limits are per-request body rates),
- sets `Content-Type: text/event-stream`, `Cache-Control: no-store`,
  `X-Accel-Buffering: no`, writes `retry: 3000`, and flushes after each event,
- sends a comment line every 20 s so idle proxies (Traefik) keep the connection,
- ends when the request's context ends, the session check fails, or the server shuts down.
  `cmd/caravana` gives each `http.Server` a `BaseContext` that `shutdown` cancels before calling
  `Shutdown`, since `Shutdown` doesn't interrupt open streams and would otherwise wait the full
  10 s.

`httpx.LogRequests` already passes `Unwrap`, so `ResponseController` reaches the real writer.
The request log line for a stream is written when it ends, with its duration.

**Connection budget**: browsers allow 6 HTTP/1.1 connections per host across all tabs, and each
stream holds one. Hence exactly one stream per page (the Logs tab's log lines ride on the page
stream, R10). Behind Traefik over HTTPS browsers use HTTP/2, where the limit doesn't apply. Over
plain HTTP (local or the SSH tunnel), about five open console tabs is the practical maximum;
quickstart says so.

## R13. Pages, URLs and templates

**Decision**: plain full-page navigation (no `hx-boost`): every page and tab is its own URL and
works on reload and without JavaScript. htmx is used for three things only: refreshing regions
on notices (R1), submitting the deploy and webhook forms in place, and the New app form's
field errors in place; each of these is a normal `<form method="post">` first, so it works as a
plain submission too (FR-027).

Templates: `base.html` (document), `shell.html` (sidebar, top bar), one file per page
(`apps.html`, `app.html` with one `define` per tab, `newapp.html`, `settings.html`), and
`parts.html` with the shared pieces (status mark and pill, app row, deployment entry, copy row,
time). A region's fragment route renders the same `define` the page renders. `parseTemplates`
builds one set per page from `base` + `shell` + `parts` + the page.

`/account` and its POST routes become `/settings`; `GET /account` answers `301` to `/settings`
(FR-022). `GET /` sends a signed-in admin to `/apps`.

## R14. Creating an app without overwriting one

**Decision**: a new create-only path, `service.CreateApp(ctx, app, trigger)`: it fails with
`service.ErrAppExists` if an app with that ID is stored, otherwise it runs the normal deploy flow
(validate, resolve, save, register). The store's save gets a create-only variant
(`InsertApp`, `ErrAlreadyExists` on the primary key) so two concurrent creates can't both win.
The API gets it as **`POST /v1/apps`** (body: an `App`; `202` with the deployment like the
deploy route; `409 app_exists` with `field: "id"`), so the console's New app has an API twin
(constitution V). The existing `POST /v1/apps/{id}/deployments` stays an upsert.

**Alternatives**: `If-None-Match: *` on the deploy route (correct HTTP, but harder to discover
and to generate clients for); checking existence in the UI handler before deploying (racy, and
a UI-only rule).

## R15. Deploying with a changed image

**Decision** (clarification Q1): the deploy form lists each service's current image. The UI
handler reads the stored app, replaces only the images the admin changed, and calls
`service.Deploy` with the result. Unchanged images, or no change at all, calls
`service.Redeploy`. No new service method or API route: an API client does the same with
`GET /v1/apps/{id}` then `POST /v1/apps/{id}/deployments` with the full app, which is the
declarative model (constitution III). Field errors from validation or the registry
(`services[0].image`) come back on the image field.

## R16. Confirmations and delete

**Decision**:

- **Confirmations** (replace or revoke the webhook token, revoke an API token) are a
  `<details>` disclosure: the action's button opens a short sentence saying what will happen
  and a second button that submits. No modal and no script; keyboard and screen readers
  handle `<details>` natively.
- **Delete app** (clarification Q3): at the bottom of Configure, set apart. A disclosure lists
  what goes ("Its 2 copies stop and `blog.example.com` stops answering. Its 14 deployments and
  its webhook token are erased. This can't be undone."), a field asks for the app's name, and
  the danger button submits. `console.js` keeps the button disabled until the name matches;
  the server checks the name again (it's the real guard).
- **`service.DeleteApp(ctx, id)`**: takes the app's deploy lock (the same per-app ordering
  deploys use), cancels the app's rollout watcher, then `deploy.Purge` (Nomad deregister with
  purge; a job Nomad doesn't have counts as done). Only after Nomad succeeded, one store
  transaction deletes the webhook token, the deployments and the app. If Nomad fails, nothing
  is deleted and the error says Nomad couldn't stop the job (FR-031). Then a `deleted` notice
  goes out. Deploys that arrive for a deleted app get `not_found`. Nomad Variables are not
  touched (nothing lives there until 008, which will add their removal here).
- **API**: `DELETE /v1/apps/{id}` → `204`; `404 not_found`; `502 nomad_unavailable` or
  `502 nomad_rejected` when Nomad fails (the existing codes and status).

## R17. A "console" trigger

**Decision**: a new trigger value `console` (`app.TriggerConsole`) for deploys started from the
pages; the API keeps `manual`, webhooks `webhook`. The column has no CHECK constraint, so no
migration. `api/openapi.yaml`'s `Deployment.trigger` enum gains `console`; clients must accept
it (the API is 0.x and the Aspire client isn't generated yet).

## R18. New reads in the store

**Decision**: `store.ListApps`, `store.LatestDeployments` (each app's newest deployment, one
query with a window function), `store.ListRecentDeployments(limit)` and an index for it
(`0004_console.sql`: `CREATE INDEX deployments_by_time ON deployments (created_at DESC)`), and
`store.DeleteApp` (one transaction). The API gets `GET /v1/apps` (summaries with status) and
`GET /v1/deployments?limit=` (recent, across apps).

## R19. Narrow screens and accessibility

**Decision**: at under 768 px the sidebar becomes a top bar with the wordmark and a
`<details>` menu holding the same links; the page column keeps 16 px gutters. Table rows (apps,
deployments, copies) are CSS grid rows that, under 640 px, stack their cells in two lines (name
and status first, facts under). No horizontal page scroll; long values (`repo@sha256:…`) are
truncated with the full value in `title` and selectable. Every interactive element is a real
`<a>`, `<button>` or form control; focus styles from DESIGN.md; status is mark + word + color
(The Shape-and-Color Rule); the live indicator and new deployment entries are announced with
`aria-live="polite"`; `prefers-reduced-motion` already removes motion. DESIGN.md gets these
narrow-screen rules and the "Deployments" tab name in the polish step.

## R20. Design screens → what builds them

What each handoff screen becomes, and which part needs script. Everything not listed stays
out of 007 (spec Assumptions).

| Handoff screen | 007 page | Server templates | htmx | SSE notices | `console.js` |
| --- | --- | --- | --- | --- | --- |
| `Shell.jsx` | shell on every page | sidebar, top bar, breadcrumbs | — | live indicator state | live indicator |
| `NowScreen` / `DevHome` (trimmed) | `/apps` | headline, apps table, activity rail, empty state | region refresh | `apps`, `activity` | — |
| `AppScreen` header + tabs | `/apps/{id}…` | header, tab links | header refresh | `app`, `deleted` | — |
| `AppScreen` Running (without CPU/memory charts) | `/apps/{id}` | copies table | region refresh | `copies` | — |
| `AppScreen` Ship + `DeployScreen` | `/apps/{id}/deployments` | deploy form, history, progress, failure sentence | form submit, region refresh | `deployments`, `copies` | — |
| `AppScreen` Configure (read-only, no secrets/domains editing) | `/apps/{id}/configure` | definition, webhook, delete | form submit | `app` | copy button, delete-name check |
| `LogsScreen` (one app, no level/search) | `/apps/{id}/logs` | toolbar, empty log panel | — | `log`, `logs-reset` | render, follow, pause, wrap |
| `NewAppScreen` (image tab only) | `/apps/new` | form, field errors | form submit with errors in place | — | — |
| `SettingsScreen` (API tokens) + 006 account | `/settings` | password, tokens | — | — | copy button |
| `LoginScreen` | `/login` (006) | unchanged | — | — | — |

## R21. Testing

**Decision**:

- **Fake Nomad** (`deploytest`): adds `GET /v1/allocations` (blocking, by index),
  `GET /v1/job/{id}/allocations`, `GET /v1/allocation/{id}`, `GET /v1/client/fs/logs/{id}`
  (scripted frames, follow), `DELETE /v1/job/{id}?purge=true`, and helpers to script copies
  (`SetCopies`) and log output (`WriteLog`).
- **store**: table tests for the new queries, delete-in-one-transaction, create-only insert.
- **service**: status derivation table (every row of R7), copies mapping (R8), progress,
  log line splitting and tail (partial lines, UTF-8, 500-line cap), hub fan-out and dropping,
  `CreateApp` conflict, `DeleteApp` ordering (Nomad first, nothing removed on failure, watcher
  cancelled), quiet session check.
- **api**: every new route in the contract test; JSON and `text/event-stream` log responses;
  `app_exists`; delete; the quiet flag doesn't renew a session.
- **ui**: each page and part renders against an empty and a populated store (SC-007), with no
  sample data; forms work as plain posts; field errors map to fields; `/live` sends `sync` then
  notices on changes and `ended` on expiry; the security header and the template rules of R3;
  no external URLs (extends `TestPagesAskNothingOfOtherHosts`); `/account` redirect.
- **SC-006 parity test**: a table listing every console action and read with its `/v1`
  operation ID; it fails if a page route has no API twin in the table.
- **Lab** (`TestLab`, opt-in): sign in through `/login`, create whoami through the New app form,
  see `/live/apps/whoami` deliver `deployments` within 5 s and the deploy turn healthy, read a
  log line through the API, deploy a changed image, delete the app and see Nomad's job gone.
- **Gate** (`runGate`): a new step `console-reads` reads `/v1/apps`, the app's copies and its
  logs with the scoped Nomad token in place (catches a missing `read-logs`, R10).
- Browser behavior (`console.js`, layout at 390 px) is checked by hand with quickstart's list;
  there's no headless browser in CI and adding one would be a Node dependency.
