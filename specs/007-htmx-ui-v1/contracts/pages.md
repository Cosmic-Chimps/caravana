# Contract: pages, parts and live streams

Served by `internal/ui` on the main listener, never on the webhook-only one. Not part of
`api/openapi.yaml`. Every page follows DESIGN.md and PRODUCT.md, sits in the shell (except
`/login`), and works as plain HTML forms and links without JavaScript; JavaScript adds live
updates, in-place form errors, copy buttons and the log view. Every response carries the 006
security headers, unchanged (research R3). Reasons: research R1, R11–R16, R19.

## Routes

Signed-in routes check the session with `CheckSession` (renews it), except `/live…`, which uses
`CheckSessionQuiet` (R11). Without a session, pages `303` to `/login?next=…`; parts answer
`401` with an empty body and the header `HX-Redirect: /login?next=<the page>`, so htmx sends the
browser to sign in. Every `POST` passes `http.CrossOriginProtection` first.

| Route | Does |
| --- | --- |
| `GET /` | `303` to `/apps` |
| `GET /apps` | Apps page |
| `GET /apps/new` | New app form |
| `POST /apps` | Create (`service.CreateApp`, trigger `console`). Success: `303` to `/apps/{id}/deployments`. Field errors: the form again, `422`, values kept |
| `GET /apps/{id}` | App page, Running tab |
| `GET /apps/{id}/deployments` | Deployments tab |
| `GET /apps/{id}/configure` | Configure tab |
| `GET /apps/{id}/logs?copy=&stream=` | Logs tab |
| `POST /apps/{id}/deploy` | Deploy (R15). Success: `303` to `/apps/{id}/deployments`. Errors: the tab again, `422`, image fields kept |
| `POST /apps/{id}/webhook` | Create or replace the token: the Configure tab, `200`, with the URL shown once (never a redirect, so it isn't in history) |
| `POST /apps/{id}/webhook/revoke` | Revoke; `303` to `/apps/{id}/configure` |
| `POST /apps/{id}/delete` | Delete when `confirm` equals the ID: `303` to `/apps?deleted=<id>`. Mismatch: the tab, `422`, field error. Nomad failure: the tab, `502`, the sentence |
| `GET /settings` | Settings (the 006 account page in the shell) |
| `POST /settings/password`, `POST /settings/tokens`, `POST /settings/tokens/{tokenId}/revoke` | As 006's `/account/...` |
| `GET /account` | `301` to `/settings` |
| `GET /parts/apps` | Headline + apps table (region `apps`) |
| `GET /parts/activity` | Activity rail (region `activity`) |
| `GET /parts/apps/{id}/header` | App header: status, domains, summary, tab counts (region `app`) |
| `GET /parts/apps/{id}/copies` | Running table (region `copies`) |
| `GET /parts/apps/{id}/deployments` | History with progress (region `deployments`) |
| `GET /live` | Stream for `/apps` |
| `GET /live/apps/{id}?logs=1&copy=&stream=` | Stream for an app page; `logs=1` on the Logs tab |
| `GET /static/{file}?v=<hash>` | CSS, fonts, `js/htmx.min.js`, `js/sse.min.js`, `js/console.js` |
| `POST /login`, `POST /logout`, `GET /login` | 006, unchanged; after sign-in the default `next` is `/apps` |

An unknown app ID on any app route: the shell with "There's no app called `x`." and a link to
Apps, status `404`. A part for an app deleted meanwhile answers `200` with the same sentence, so
the region shows it.

## Live streams (`text/event-stream`)

```text
retry: 3000

event: sync            ← first, on every (re)connect; every region refreshes itself
data:

event: deployments     ← a notice: the region named by the event refreshes (coalesced, 500 ms)
data:

: keepalive            ← every 20 s

event: ended           ← the session ended or was signed out; the stream closes
data:
```

- `/live` sends `apps` and `activity`.
- `/live/apps/{id}` sends `app`, `deployments`, `copies`, `deleted` for that app. With
  `logs=1` it also sends `logs-reset` (no data) and then `log` events whose `data` is one
  `LogLine` as JSON (contracts/api.md), first the tail, then new lines.
- A request without a valid session gets `ended` at once (status `200`, research R11).

Markup that uses it (attribute names are htmx's; no `hx-on`, no inline script or style):

```html
<body hx-ext="sse" sse-connect="/live/apps/blog" sse-close="ended" data-live>
  <span id="live" role="status" aria-live="polite" data-live-indicator>…</span>
  <section hx-get="/parts/apps/blog/deployments" hx-trigger="sse:deployments, sse:sync"
           hx-swap="innerHTML">…</section>
  <!-- Logs tab only: console.js takes the log events (sse-swap with hx-swap="none") -->
  <div sse-swap="log,logs-reset" hx-swap="none" data-log-sink></div>
</body>
```

`console.js` listens for `htmx:sseOpen`, `htmx:sseError` and `htmx:sseClose` on `body` to set
the indicator ("Live" / "Reconnecting…" / "Signed out. Sign in again."), and for
`htmx:sseBeforeMessage` on `[data-log-sink]` to render log lines itself with `textContent`
(FR-018) and cancel htmx's swap.

## Shell

From DESIGN.md (Layout) and `Shell.jsx`, trimmed:

- **Sidebar** (248 px): wordmark `caravana`; the host name in mono under it (where the handoff
  has the org); nav **Apps** and **Settings** with the current one marked; at the bottom the
  account row: the admin's email and "Sign out" (a `POST /logout` form). No Developer/Operator
  switch, no org/project/environment pickers.
- **Top bar** (52 px, sticky): breadcrumbs `Apps › blog › Deployments`; on the right the live
  indicator (dot + word, never a looping animation) and the page's primary action ("New app" on
  Apps, "Deploy" on an app page).
- **Under 768 px**: the sidebar collapses into the top bar as a `<details>` "Menu" (R19).

## Apps (`/apps`)

- **Header**: the headline sentence (R7), the lede "Every app on this caravana, live.", primary
  action "New app".
- **Apps table** (main column): one row per app: status mark + word (`rolling out`, `healthy`,
  `1 of 2 healthy`, `not running`, `unknown`), name (mono, link), live version (`v7`), image
  per service (mono, truncated), last deploy (relative time; its outcome when not healthy, e.g.
  "v8 reverted"), domains (links, `https://`). Multi-service apps list each service's image on
  its own line.
- **Activity rail** (400 px, below the table under 1100 px): the 20 newest deployments: app,
  version, trigger (`console`, `api`, `webhook` in words: "from the console", "through the
  API", "by webhook"), outcome mark, relative time; each links to the app's Deployments tab.
- **Empty**: "No apps yet." + "Deploy an image and it shows up here." + "New app".
- **After a delete**: a one-line notice "`blog` was deleted." above the table.

## App page (`/apps/{id}` and tabs)

- **Header** (region `app`): name (mono, 24 px), status pill, domains as links, the summary
  sentence ("2 copies running v7. Last deployed 3 minutes ago from the console."), and the
  primary action "Deploy" linking to the Deployments tab's form. Tabs (links): **Running**
  (count of copies), **Deployments** (live version, e.g. `v7`), **Configure**, **Logs**; the
  current tab has `aria-current="page"`.
- **Running** (region `copies`): rows of copy (short ID mono, full in `title`), service, status
  mark + word, version, restarts (warning color when > 0), up for (relative). Empty: "No copies
  are running." plus, if the last deploy failed, "v8 failed: see Deployments." Nomad down: "Nomad
  can't be reached right now, so copies can't be shown." No CPU/memory columns.
- **Deployments**:
  - Deploy form (top): one row per service: service name, image field prefilled with the stored
    reference (mono). Button "Deploy" (primary). Helper text: "Keep the image to deploy it again
    and pick up a new push to the same tag, or change the tag." Errors on the field.
  - History (region `deployments`): newest first, 50, then "Show older" (link with `?before=`).
    Each entry: version (mono), status mark + word, trigger in words, digest per service (short,
    full in `title`), started (relative) and duration, warnings as a list, and for failures the
    sentence (data-model) with the raw error in mono under it. A running entry shows the
    progress line (R9). The newest entry's region is `aria-live="polite"`.
- **Configure** (region `app` for the parts that change):
  - **Definition**, one panel per service: image, command and args, ports (`http → 8080`),
    domains, copies, CPU and memory (or "default"), health check (`GET /healthz every 10 s`,
    `TCP`, or "none"), environment variable names only with "Values aren't shown." No inputs: it
    says "To change these, use the API (`POST /v1/apps/{id}/deployments`)."
  - **Webhook**: "No webhook token." + "Create token"; or "Token created 3 days ago." +
    "Replace token" and "Revoke token", each behind a `<details>` confirmation ("The current
    token stops working at once."). After create or replace, a panel with the full URL in mono,
    a copy button, and "Copy it now. It won't be shown again."
  - **Delete app** (last, apart, danger outline): `<details>` "Delete app…" → what goes (R16),
    field "Type `blog` to confirm", danger button "Delete `blog`" (disabled by `console.js` until
    the name matches; the server checks again).
- **Logs**: toolbar with copy (select: "All copies" or each copy's short ID + service), stream
  (select: both, stdout, stderr), "Apply" (submits the GET form, so it works without JS), and
  buttons "Pause"/"Resume" and "Wrap lines" (`aria-pressed`). The panel is a mono list; each line
  shows the copy's short ID, then the text; stderr lines carry a mark. While paused: "Paused. 12
  new lines." Without JavaScript the panel shows the tail rendered on the server and a note that
  following needs JavaScript. No copies: "This app has no copies, so there are no logs to show."

## New app (`/apps/new`)

Fields (labels above, DESIGN.md forms): **Name** (mono; hint "Lowercase letters, digits and
dashes. It becomes the app's address in Nomad."), **Image** (mono; "e.g. `ghcr.io/you/app:1.4`"),
**Container port** (optional), **Domain** (optional; enabled hint "Needs a port. HTTPS is set up
for it."), **Health check path** (shown with a port; default `/`; empty means a TCP check).
Without a port, a note: "Without a port this runs as a worker: no address, no health check."
Button "Create and deploy". Errors under each field (`role="alert"` on the first), values kept.

## Settings (`/settings`)

The 006 account page's content inside the shell, same behavior and sentences: **Password**,
**API tokens** (list, create, revoke behind a `<details>` confirmation, new token shown once with
a copy button and the `curl` example). Headline: "Signed in as you@example.com."
