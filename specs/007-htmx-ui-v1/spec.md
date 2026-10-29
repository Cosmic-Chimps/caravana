# Feature Specification: HTMX UI v1

**Feature Branch**: `007-htmx-ui-v1`

**Created**: 2026-10-04

**Status**: Implemented, v0.1.0

**Input**: User description: "007 HTMX UI v1: the Developer console for what the engine does
today, built from the Claude Design handoff (DESIGN.md, PRODUCT.md). Pages: the shell (sidebar
with Apps and Settings, top bar with breadcrumbs and live dot); Apps home with a headline
sentence, an apps table and recent activity; an app page with tabs Running (live copies from
Nomad allocations), Ship (Deploy/Redeploy, deployment history with live status, the rollout in
progress), Configure (read-only app definition, webhook token create/show-once/revoke) and Logs
(stdout/stderr streamed from the app's copies); New app from an image (name, image, port, domain
→ first deploy); Settings holding today's account page (password, API tokens). Every page shows
only real data, with empty states but no placeholders. The UI calls only internal/service, and
every new read (list apps, copies, logs) also gets a /v1 API route and an openapi.yaml entry.
Single admin, signed-in session required. Out of scope: metrics charts, needs-you, capacity,
domains, secrets, backups, rollback, Operator/org/member screens." A note for the plan step, not
this spec: live updates probably over server-sent events, and Alpine.js (vendored, embedded, no
build step) is to be considered for small client-side behavior.

## Context

Features 001–006 built the engine and closed the API: an admin can deploy an image, follow the
rollout, redeploy by webhook and manage API tokens, but only through `curl`, the CLI and two bare
pages (`/login`, `/account`). Feature 007 is the first real console: the **Developer view** of
the Claude Design handoff, cut down to what the engine can back with real data today (one node,
one admin, image apps).

Who uses this:

- **The owner**: the one admin. They open the console to answer "is everything fine?", ship a
  new version, find out why a deploy failed, and read an app's logs. Sometimes on a phone.
- **API clients** (the .NET Aspire integration, CI, scripts): everything the console reads or
  does, they can do through `/v1` with an API token. The console is just another client of the
  same service layer.

Words used here (plain words in sentences, as in PRODUCT.md):

- **App**: one project, one Nomad job. It has one or more **services**.
- **Copy**: one running instance of a service (a Nomad allocation). "Copies", never
  "allocations", in sentences; Nomad IDs appear only in mono detail lines.
- **Deployment**: one deploy attempt with a version number, a trigger (API, UI, webhook) and a
  status: pending, running (rolling out), healthy, failed, reverted or superseded.
- **Deploy / Deployments**: the action is "Deploy"; the tab that lists them is "Deployments".
  The handoff's "Ship" is not used.
- **Live**: the page updates by itself while it's open, without a reload.

## Clarifications

### Session 2026-10-04

- Q: Can the console change an app's image, or only redeploy what's stored? → A: The deploy
  action lets the admin change each service's image (tag or full reference) before deploying;
  everything else in the definition stays read-only in 007.
- Q: Keep the handoff's "Ship" tab name? → A: No. The tab is "Deployments" (what it lists, and
  the API's name); "Deploy" is its action. The word "ship" isn't used in the console, and
  DESIGN.md's tab list is updated to match.
- Q: Can an app be deleted? → A: Yes. Configure has "Delete app", confirmed by typing the app's
  name: it stops and purges the app's Nomad job and removes its record, deployment history and
  webhook token. The API gets the same as `DELETE /v1/apps/{id}`.
- Q: Do live updates and log streams on an open page keep the session alive? → A: No. Only the
  admin's own actions (opening a page, pressing an action) renew the session; live updates and
  log streams flow while it's valid and stop when it expires.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - See at a glance what's running (Priority: P1)

The owner signs in and lands on the Apps page inside the console shell. A headline sentence says
how things stand right now ("3 apps healthy, 1 rollout in progress."). Below it, a table lists
every app with its status, current version, image, last deploy and its address. A side column
shows recent activity: the latest deployments across all apps, newest first. Clicking an app
opens its page, which shows the same status, its address and a one-line summary. The page
updates by itself while it's open.

**Why this priority**: it's the reason to open a console at all, and every other story starts
from this page and the shell around it.

**Independent Test**: deploy two apps through the API, one healthy and one whose last deploy was
reverted; sign in; see both in the table with the right status and the right headline; start a
third deploy through the API and watch the headline and the table change without a reload.

**Acceptance Scenarios**:

1. **Given** a signed-in admin and two deployed apps, **When** they open the console,
   **Then** they see the shell (sidebar with Apps and Settings, top bar with breadcrumbs and
   the live indicator) and a table with both apps, each with status, version, image, last
   deploy time and address.
2. **Given** all apps are healthy, **When** the Apps page is open, **Then** the headline says
   so in one sentence and the only status color on the page is the healthy one.
3. **Given** the Apps page is open, **When** a deploy starts for one app (from any client),
   **Then** within 5 seconds the app's row shows the rollout in progress, the headline counts
   it, and the activity column shows the new deployment, all without a reload.
4. **Given** no apps exist yet, **When** the admin opens the console, **Then** the page says
   there are no apps and offers "New app" as the one primary action; nothing is mocked.
5. **Given** the session expired, **When** the admin opens any console page, **Then** they are
   sent to sign in and come back to the page they asked for.

---

### User Story 2 - Deploy a new version and watch it roll out (Priority: P2)

On an app's **Deployments** tab the owner sees the deployment history (version, trigger, image
digest, when, outcome) with the newest first. They open the deploy action, which shows each
service's current image. They either keep it, which deploys the stored app again and resolves
its tags anew (an image pushed under the same tag gets picked up), or change it to another tag
or reference, for example `myapp:1.4` to `myapp:1.5`. A new entry appears at the top and moves
through its states live. While it rolls out the page shows how far it got (new copies started
and healthy out of those wanted). When it ends, the entry says the outcome in a plain sentence;
a failure says what happened and what caravana did ("v6 failed its health check. Nomad rolled back to v5, which is still serving.").

**Why this priority**: deploying and knowing whether it worked is the core job of a PaaS; it's
the first action the console adds on top of reading.

**Independent Test**: on an app with a few past deploys, press deploy and watch the new entry go
from pending to running to healthy without a reload; deploy a broken image through the API and
see the reverted entry with its explanation.

**Acceptance Scenarios**:

1. **Given** an app with past deployments, **When** the admin opens Deployments, **Then** they see
   each deployment's version, trigger, resolved image digest, start time, duration and status,
   newest first.
2. **Given** the Deployments tab is open, **When** the admin presses deploy, **Then** a new deployment
   is created through the same service the API uses, it shows at the top at once, and its
   status changes live until it's final.
3. **Given** a rollout in progress, **When** the Deployments tab is open, **Then** it shows the
   progress of the rollout and updates it live.
4. **Given** a deployment ended reverted or failed, **When** the admin views it, **Then** it
   explains in a sentence what happened and what caravana or Nomad did, with the raw error in a
   mono detail line.
5. **Given** the deploy action is open, **When** the admin changes a service's image to a new
   tag and deploys, **Then** the app is saved with the new image and deployed, and the new entry
   shows the new image and its resolved digest; every other field of the app is unchanged.
6. **Given** a deploy is started on one browser tab, **When** another tab shows the same app,
   **Then** the other tab shows it too, live.
7. **Given** a deploy can't start (for example the image tag doesn't exist), **When** the admin
   deploys, **Then** the page shows why, on the image field or next to the action, keeps what
   they typed, and nothing is left looking like it's in progress.

---

### User Story 3 - Create an app from an image (Priority: P3)

From the Apps page the owner chooses "New app", fills in an app name, an image, the container
port (optional), a domain (optional) and a health check path, and deploys. They land on the new
app's Deployments tab and watch its first rollout. If the input is wrong, each problem shows next to
its field and nothing is deployed.

**Why this priority**: without it a fresh install's console is empty until someone uses the
API. It reuses the deploy flow that exists, so it's small.

**Independent Test**: on an empty install, create an app from `traefik/whoami` with port 80, a
domain and health path `/`; land on its Deployments tab, see the first deploy turn healthy and reach
the app at its domain over HTTPS. Then try a name that already exists and an invalid image and
see field errors.

**Acceptance Scenarios**:

1. **Given** the New app form, **When** the admin submits a valid name, image, port, domain
   and health path, **Then** the app is saved and its first deployment starts, and they land on
   its Deployments tab with the rollout shown live.
2. **Given** an app with that name already exists, **When** the admin submits, **Then** the
   form refuses with an error on the name field and the existing app is untouched.
3. **Given** invalid input (a name that isn't a DNS label, a domain without a port, an image
   that can't be resolved), **When** the admin submits, **Then** each error shows on its field,
   what they typed stays in the form, and nothing is deployed.
4. **Given** no port is given, **When** the admin submits, **Then** the app is created as a
   service without a port, domain or health check (a worker), and the form says so before
   submitting.

---

### User Story 4 - See the copies that are running (Priority: P4)

The **Running** tab lists the app's copies: which service, its status (starting, healthy,
unhealthy, stopped, failed), the version it runs, how many times it restarted and how long it
has been up, with its Nomad ID in mono. It updates live.

**Why this priority**: it answers "is it actually up?" when a status says otherwise, and it is
where the owner picks a copy to read its logs.

**Independent Test**: deploy an app with two copies; see both healthy on Running; stop one copy
from Nomad and see Running reflect the replacement live.

**Acceptance Scenarios**:

1. **Given** an app with running copies, **When** the admin opens Running, **Then** every copy
   is listed with service, status, version, restarts and uptime, and the tab label shows the
   count.
2. **Given** a rollout in progress, **When** Running is open, **Then** old and new copies are
   both shown, each with its version, and the list changes live as copies start and stop.
3. **Given** the app has no copies (never deployed, or every copy stopped), **When** Running is
   open, **Then** it says so in a sentence and, if the last deploy failed, says which.

---

### User Story 5 - Read an app's logs (Priority: P5)

The **Logs** tab shows recent output from the app's copies and keeps following new lines as
they arrive. The owner can pick all copies or one copy, stdout, stderr or both, pause and resume
following, and wrap long lines. Each line says which copy it came from.

**Why this priority**: logs are the first place to look when something is wrong, and Nomad
keeps them; until now the owner needed the Nomad CLI on the host.

**Independent Test**: deploy whoami, call it a few times, open Logs and see the request lines
arrive live; pause and see no new lines until resuming; choose one copy and see only its lines.

**Acceptance Scenarios**:

1. **Given** an app with running copies, **When** the admin opens Logs, **Then** they see the
   most recent lines (up to the last 500) from all copies, then new lines as they are written.
2. **Given** Logs is following, **When** the admin pauses, **Then** the view stops moving and
   says how many new lines arrived while paused; resuming shows them.
3. **Given** a copy and a stream are chosen, **When** lines arrive, **Then** only that copy's
   chosen stream is shown.
4. **Given** the last deploy failed and its copies have stopped, **When** the admin opens Logs,
   **Then** they can still read those stopped copies' output as long as Nomad keeps it.

---

### User Story 6 - Check an app's configuration, its webhook, or delete it (Priority: P6)

The **Configure** tab shows the app definition as it's stored: each service's image, command,
ports, domains, copies, resources, health check, and the names of its environment variables.
It's read-only in this feature. It also holds the app's webhook: whether a token is set and
when it was created, an action to create or replace it (the full webhook URL is shown once),
and an action to revoke it. At the bottom, apart from everything else, "Delete app" removes the
app for good after the admin types its name.

**Why this priority**: it's needed to wire a registry or CI to the webhook without the API, and
to check what was deployed, but the owner can do both through the API today.

**Independent Test**: on a deployed app, open Configure and compare every shown field with
`GET /v1/apps/{id}`; create a webhook token, see the URL once, call it and see a redeploy; reload
and see the token is no longer shown; revoke it and see the call refused. Delete a test app,
type its name to confirm, and see it gone from the Apps page, from Nomad and from `/v1/apps`.

**Acceptance Scenarios**:

1. **Given** a deployed app, **When** the admin opens Configure, **Then** every field of the
   stored definition is shown, and environment variable values are not shown, only their names.
2. **Given** no webhook token, **When** the admin creates one, **Then** the full webhook URL
   with the token is shown once with a copy action, and never again after leaving the page.
3. **Given** a token exists, **When** the admin replaces or revokes it, **Then** the console
   asks for confirmation, says the old token stops working at once, and does it.
4. **Given** an app, **When** the admin chooses "Delete app", **Then** the console says what will
   be removed (its copies stop and its address stops answering; its history and webhook token
   are erased) and only deletes once the admin has typed the app's name exactly.
5. **Given** the app is deleted, **When** it's done, **Then** the admin lands on the Apps page
   without it, its Nomad job is gone, and its name can be used again for a new app.

---

### User Story 7 - Account settings inside the console (Priority: P7)

The `/account` page from feature 006 (change password, API tokens: create, list, revoke) moves
into the console shell as **Settings**, with the same behavior, reachable from the sidebar.
Signing out stays in the sidebar's account row.

**Why this priority**: it already works; this only gives it a home in the shell.

**Independent Test**: from the sidebar open Settings, create an API token (shown once), revoke
it, change the password, sign out; the old `/account` address leads to Settings.

**Acceptance Scenarios**:

1. **Given** a signed-in admin, **When** they open Settings, **Then** they can do everything
   `/account` did, inside the shell.
2. **Given** an old link to `/account`, **When** it's opened, **Then** it leads to Settings.

---

### Edge Cases

- **Nomad unreachable**: stored data (apps, deployments, configuration) still shows; Running and
  Logs say Nomad can't be reached right now instead of showing an empty list, and the app status
  says it's unknown rather than guessing.
- **Live connection drops** (server restart, network): the live indicator changes to say the
  page isn't live, the page keeps what it showed, and it reconnects and catches up by itself
  without duplicating entries.
- **Session expires while a page is open**: a tab left open still idles out after 12 hours,
  because live updates and log streams don't count as activity. At expiry they stop, the
  indicator says to sign in again, and the next action sends the admin to sign in, then back.
- **Two deploys close together**: the newest wins (feature 003); the older one shows as
  superseded, live.
- **Caravana restarts during a rollout**: the deploy resumes (feature 003) and an open page
  reconnects and keeps showing it.
- **An app with several services** (created through the API): the table and pages show the app
  once, with each service's image, copies and domains listed under it.
- **Long values** (image references, digests, domains, log lines): they truncate or wrap without
  breaking the layout, and the full value stays available.
- **Many apps or a long history**: the Apps table handles at least 100 apps; Deployments shows the
  latest 50 deployments and loads older ones on request.
- **A very chatty app**: Logs keeps at most the last 5,000 lines in view and stays responsive.
- **Deleting an app during a rollout**: the rollout stops with the app; nothing is left
  following it.
- **Another tab shows an app that was just deleted**: it says the app was deleted, live, and
  offers the Apps page; its actions are refused.
- **Delete fails halfway** (Nomad unreachable): the app stays, with its record and history, and
  the console says the job couldn't be stopped; nothing is half-removed in the store.
- **An app deleted or never existing in the URL**: a plain "no such app" page inside the shell,
  with the right not-found status.
- **Narrow screens**: every page works down to 390 px wide; the sidebar collapses into a menu
  and table rows reflow without horizontal scrolling of the page.
- **JavaScript fails to load or is off**: pages still render their current data; actions still
  work as plain form submissions; only live updates are lost.

## Requirements *(mandatory)*

### Functional Requirements

**Shell and pages**

- **FR-001**: Every console page except sign-in MUST sit inside the shell: a sidebar with the
  wordmark, the instance's host name, the navigation (Apps, Settings) and the account row with
  sign out; and a top bar with breadcrumbs (Apps › app › tab) and the live indicator.
- **FR-002**: The live indicator MUST show whether the page is currently receiving live updates
  and MUST NOT claim to be live when it isn't.
- **FR-003**: Every console page MUST require a signed-in session (feature 006); without one the
  admin is sent to sign in and returned to the page they asked for. Only the admin's own
  requests (opening a page, an action) renew the session's idle time; live updates and log
  streams MUST NOT renew it, and MUST end when the session expires or is signed out.
- **FR-004**: Pages MUST show only real data from the engine, Nomad and the store. Empty states
  say what's empty and offer the next action; no sample or placeholder data appears anywhere.
- **FR-005**: The console MUST follow DESIGN.md and PRODUCT.md (tokens, type, status as shape
  plus color, voice, sentence case, actions named by their outcome) and MUST NOT copy code, UI
  or copy from other PaaS projects.

**Apps page**

- **FR-006**: The Apps page MUST open with one headline sentence about the present state of all
  apps (counts of healthy, rolling out, and in trouble), computed from real data.
- **FR-007**: The Apps page MUST list every app with: name, status, current version, image of
  each service, time of the last deploy, and its address (domains) as links.
- **FR-008**: The Apps page MUST show recent activity: the latest deployments across all apps
  (app, version, trigger, outcome, time), newest first.
- **FR-009**: An app's status MUST describe what's true now: rolling out (a deployment pending or
  running), healthy (the wanted copies are running and healthy), in trouble (fewer healthy
  copies than wanted, with the count), not running (no copies), or unknown (Nomad unreachable).
  The outcome of the last deployment is shown beside it when it differs (for example "v6
  reverted").

**App page**

- **FR-010**: The app page MUST show the app name, its status, its domains as links and one
  summary sentence, above four tabs: Running, Deployments, Configure, Logs. Each tab has its own
  address so it can be linked and reloaded.
- **FR-011**: Running MUST list the app's copies with service, status, version, restart count,
  uptime and Nomad ID, updated live.
- **FR-012**: Deployments MUST list the app's deployments (version, trigger, resolved digest per
  service, start time, duration, status, warnings), newest first, with statuses updated live,
  and the progress of a rollout in progress (also returned on the API's deployment).
- **FR-013**: Deployments MUST offer one deploy action that shows each service's current image and
  lets the admin keep it or change it (tag or full reference). Unchanged, it deploys the stored
  app again with its tags resolved anew (as `POST /v1/apps/{id}/deployments` with an empty
  body); changed, it deploys the stored app with only those images replaced (as the same route
  with the full app). Either way it goes through the same service call as the API and is
  recorded with a trigger that says it came from the console.
- **FR-014**: A failed or reverted deployment MUST be explained in a sentence (what happened,
  what was done, what is serving now), with the raw error in a mono detail line.
- **FR-015**: Configure MUST show the stored app definition read-only, with environment
  variable names but never their values.
- **FR-016**: Configure MUST let the admin create or replace the app's webhook token (the URL
  with the token shown once, with a copy action) and revoke it, both after a confirmation.
- **FR-017**: Logs MUST show up to the last 500 lines of the app's copies and follow new lines
  live, labeled by copy; the admin can choose all copies or one, stdout, stderr or both, pause
  and resume, and wrap lines. Copies of the last deployment that have stopped stay readable
  while Nomad keeps their logs.
- **FR-018**: Log output MUST be shown as text, never interpreted as markup.

- **FR-031**: Configure MUST offer "Delete app", set apart from the other actions. It MUST only
  proceed after the admin types the app's name exactly; it stops and purges the app's Nomad job,
  then removes the app's record, deployment history and webhook token. If the job can't be
  stopped, nothing is removed.

**New app**

- **FR-019**: The New app form MUST take an app name, an image, an optional container port, an
  optional domain and a health check path (shown only with a port), and MUST create the app and
  start its first deployment through the same service call as the deploy API.
- **FR-020**: New app MUST refuse a name that's already in use, with an error on the name field,
  and MUST never replace an existing app.
- **FR-021**: Invalid input MUST show each error on its own field (the same validation and
  field names as the API), keep what was typed, and deploy nothing.

**Settings**

- **FR-022**: Settings MUST provide everything the 006 account page did (change password; create,
  list and revoke API tokens with the token shown once) inside the shell; `/account` MUST lead
  to Settings.

**Live updates**

- **FR-023**: App status, the headline, activity, deployment status, rollout progress and copies
  MUST update on open pages within 5 seconds of the change, without a reload.
- **FR-024**: After a dropped live connection the page MUST reconnect by itself and bring its
  data up to date, with no duplicated or missing entries.

**API parity and contract**

- **FR-025**: Everything the console reads or does MUST go through the same service layer as
  the API, and every new read MUST also be available on `/v1` with an API token: list apps
  (with status), recent deployments across apps, an app's copies, and an app's logs (recent
  lines, and following new ones). Creating an app without replacing an existing one
  (`POST /v1/apps`, the twin of New app) and deleting an app (`DELETE /v1/apps/{id}`) MUST also
  be on `/v1`.
- **FR-026**: Every new or changed `/v1` route MUST be in `api/openapi.yaml` in the same change,
  with the standard JSON error body; the routes test keeps them in sync.

**Safety and quality**

- **FR-027**: State-changing actions (deploy, create app, delete app, webhook token, API token,
  password) MUST be protected against cross-site requests as in feature 006, and MUST work as
  plain form submissions when scripts don't run.
- **FR-028**: Tokens, environment values and log contents MUST NOT be written to the server's
  request log.
- **FR-029**: Every page MUST work with the keyboard alone, show status in shape and text as
  well as color, honor reduced motion, and work from 390 px wide up.
- **FR-030**: The console MUST NOT need anything but the caravana binary to serve: its scripts,
  styles and fonts are served by caravana itself, and no page loads anything from a third-party
  host.

### Key Entities

- **App summary**: an app as the list shows it: name, services with their images and domains,
  current version, derived status, last deployment (version, outcome, time).
- **Copy**: one running or recently stopped instance of a service: service, status, version,
  restarts, start time, Nomad ID.
- **Deployment** (exists): version, trigger (now also "console"), digests, status, warnings,
  error, times. Rollout progress (copies wanted, started, healthy) is read live, not stored.
- **Log line**: copy, stream (stdout or stderr), text, and time when known.
- **Webhook token** (exists), **API token** and **session** (exist, feature 006).

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: From sign-in, the owner can tell whether every app is healthy in under 5 seconds,
  without scrolling, on a laptop screen with up to 10 apps.
- **SC-002**: The owner can redeploy an app and see its outcome in at most 2 clicks from the
  Apps page, without a reload and without any other tool.
- **SC-003**: On a fresh install, the owner can go from an empty console to a healthy app
  reachable at its domain over HTTPS using only the console, in under 3 minutes plus the image's
  pull time.
- **SC-004**: Status changes (deploy started, rollout progress, final outcome, a copy stopping)
  appear on open pages within 5 seconds, every time, in the lab end-to-end test.
- **SC-005**: A new log line written by an app appears in the Logs tab within 3 seconds.
- **SC-006**: Every action and read the console offers can also be done with an API token
  through `/v1`, checked by a test that walks the console's actions against the contract.
- **SC-007**: No console page shows a value that isn't from the engine, Nomad or the store:
  checked by rendering every page against an empty install and a populated one.
- **SC-008**: Every page is usable at 390 px wide and with the keyboard alone.
- **SC-009**: The lab end-to-end test and the VM gate keep passing.

## Assumptions

- **`createApp` saves before it resolves the image.** A failed first deploy leaves a stored app
  that never ran. Feature 019.5 made that a normal state ("never deployed"), so the client
  retries with `deployApp` and nothing is removed (#27).

- **One admin, one node, image apps** (Phase 2 scope). No orgs, projects or environments: the
  breadcrumbs start at Apps, and the sidebar shows the instance's host name where the design has
  the org name.
- **Editing an app in the console is limited to its images in 007**, through the deploy action
  (see Clarifications). Configure is read-only; changing ports, domains, copies, resources or the
  health check is done through the API. Editable
  environment and secrets arrive with 008, compose apps with 009.
- **The deploy action redeploys the stored definition** and re-resolves tags, which is how a
  new image under the same tag (for example `:latest`) is picked up. Deploying an older version
  is rollback, feature 011.
- **Restart, scale, metrics charts (CPU and memory), needs-you, capacity, domains
  and certificates pages, variables, backups, previews, and the Operator view** are out of
  scope, as are SSO, 2-step and members.
- **Logs come from what Nomad keeps** for each copy (its own rotation limits); caravana stores
  no logs. Log level filtering, search and download are not in 007.
- **A copy's CPU and memory figures** are not shown; the Running tab shows state, not metrics.
- **Environment variable values are hidden** even though the model calls them plain config,
  because before 008 people may put secrets there.
- **The New app form makes a single-service app** named after the app. Multi-service apps come
  from the API or, later, compose.
- **Desktop first**: the handoff is desktop-only; narrow screens get a working, simpler layout
  (collapsed sidebar, stacked rows), not a separate design.
- **Live updates and client-side behavior** are chosen in the plan (server-sent events with the
  htmx extension are the likely transport; Alpine.js is to be weighed for tabs, copy, show-once
  reveal and log follow/pause), within the constitution: `html/template` + HTMX, assets embedded,
  no SPA and no Node build step.
