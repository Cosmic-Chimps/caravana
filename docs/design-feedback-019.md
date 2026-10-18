# Console 019: what we built, what we changed, what we need next

For the Claude Design loop. This is the second report back, after features 008 to 019 (secrets,
Compose, databases and backups, rollback and export, the installer, memory per service, Console
v2, Compose file editing, volume backups, private registries, notifications, projects and
environments). The handoff being answered is still "caravana console", Handoff 2 (2026-10-04);
the first report was `docs/design-feedback-007.md`, and nothing was written back between the
two. The built result is described in `DESIGN.md` (which records 006 and 007; the pages since
follow its rules but are not written up there yet) and in the "Where we are" notes of
`CLAUDE.md`. This file is the difference between the handoff and the build as of feature 019,
and what we need drawn for feature 020 (build from source) and the features after it.

All examples use `whoami`, `blog` and `example.com`. No sample data from the handoff is in the
repo.

How we build hasn't changed since 007, section 1: server-rendered pages, every form works
without JavaScript, live updates are server-sent notices that make a region refetch itself,
confirmations are disclosures in the page, one primary button per view, no icons ship. Two
things did change: an app's top bar holds no action any more (each tab's Deploy or Save is its
own form's button, because a top-bar Deploy that only led elsewhere read as this page's button
and lost an edit), and the stylesheet gained a few patterns (an edit form per service, a Compose
editor with a preview, a needs-you list, a usage column) that `DESIGN.md` should absorb.

## 1. Screens: handoff versus built

Built since 007, each with real data and an API twin (constitution V: nothing exists only in the
console):

| Handoff screen | Built as | Notes |
|---|---|---|
| Home (`DevHome`) | **Apps** (`/apps`) | Opens with a "Needs you" list (014), then the apps table grouped by project and environment (019), then the Activity rail. No "Shipping now" card, no Projects panel, no Platform panel (see 2.4) |
| Project / environment (`ProjectScreen`) | **Project page** (`/projects/{project}`) | Display name, environments (add, delete), delete project by typing its name. Apps stay on the home, grouped. No branch per environment, no "deploys from main": there is no git yet |
| App detail: Running / Ship / Configure / Logs | **Running / Deployments / Configure / Logs / Backups** | Five tabs now; Backups came with 010. The header gained a breadcrumb (project › environment) and an Environments line with "Copy to" and "Promote to" (019) |
| Running (`LiveMetric`) | **Running tab** | CPU and memory per copy against the reservation and the ceiling (014), read every 5 s only while the tab is open, a warning mark at 90 % of the ceiling. No node column (one node), no sparklines (no history) |
| Ship (`LedgerRow` with Revert) | **Deployments tab** | The deploy form and the history. Each entry offers "Redeploy this version" (011), "Export jobspec" (011) and "Download Compose file" (015); an entry says what it was made from (a redeploy of a version, a promotion from another environment, the webhook) |
| Deploy progress (`DeployScreen`) | A progress line inside the **deployment entry** | Still not a separate screen, and still without the four stages (see 2.3) |
| New app (`NewAppScreen`) | **New app** (`/apps/new`), two tabs | Image (as before) and Compose file (014): paste, preview, then an overlay per service (domains, health check, secrets, backup). A `place` select appears only when there is more than one environment (019). No git, no binary, no size presets, no "fits on the cluster" |
| Variables & secrets (`VariablesScreen`) | **Configure › Edit** and **Configure › Secrets** | Env is edited per service in the definition form (values shown); secrets are a separate section (names only, set or remove, each a deploy). One environment per app page, not a grid. See 2.1 and 4.1 |
| Logs (`LogsScreen`) | **Logs tab** | Copy and Stream selects, Pause, Wrap lines, as in 007. No levels, search, time range or histogram (see 2.4) |
| Domains (`DomainsScreen`) | Fields on **Configure › Edit** per service, and in the Compose overlay | Add or remove a domain and deploy. No DNS check, no certificate state, no options (see 3.3) |
| Backups (`BackupsScreen`) | **Backups tab** per app | Volumes (mount, host path, size, last backup), backed-up services with "Back up now", the backups list, "Restore" (type the app's name), the restores list (010, 016). Schedule and retention are set on Configure or in the Compose overlay. Engines: PostgreSQL dump, volume archive. No "restore into a new database", no restore test, no restore-point strip, no "not backed up on purpose" |
| Settings (Connections, Security, People, Notifications, Audit log) | **Settings** (`/settings`), one page | Password, API tokens, Backup destination (010), Registries (017), Notifications (018), top to bottom. No nav (see 2.2) |
| Settings › Registries | **Settings › Registries** section | Host, user name, write-only password or token, "Test", remove. Close to the handoff's row (name, kind, last check); no "used by n apps" |
| Settings › Notifications | **Settings › Notifications** section | Channels: signed webhook, Slack, Discord, ntfy. Add, edit, "Send a test", Pause or Resume, Remove, recent deliveries, the signing secret shown once. Each channel picks what it hears from three kinds (problems, failed deploys, successful deploys), not the handoff's event-by-channel matrix (see 2.4) |
| Login | **Login** | Unchanged since 007 |

Not built, for the same reason as in 007 (no screen shows placeholder data as if it were real,
and the engine has one node and one admin): the Operator view entirely, Placement failure,
Previews, Monitoring, Schedules, Members, the org switcher, New organization, SSO and the 2-step
code.

### 1.1 How the day-to-day flows read today

These are the flows the handoff either didn't draw or drew for a cluster. Each one is a sentence
or two about what the console does, so the next handoff can redraw it rather than guess.

- **Compose editing (009, 014, 015).** New app › Compose file takes a pasted file, shows the
  services it becomes with every warning and refusal naming its Compose path
  (`services.web.build`), then an overlay per service, then one button that creates and deploys.
  An app made this way keeps its file as typed, comments included; its Configure shows the Compose
  form first (edit, preview, deploy, refused with a 412 if the app changed since the form was
  drawn) and the definition read-only under "This app is edited through its Compose file." An app
  made from Compose before the file was kept is asked for it once ("paste it once"). A deploy from
  the definition form drops the file, with a warning.
- **Secrets (008).** Per service, `{name, value?}`: a name with no value keeps the current one.
  Values are write-only and live only in Nomad Variables; the stored app, every page, every
  download and every log hold names only. Setting or removing a secret is a deploy, and a deploy
  that drops a secret warns that its value is deleted. A failed deploy puts the previous values
  back.
- **Backups and restore (010, 016).** A service with `backup: {engine, schedule?, keep?}` is
  backed up on a cron schedule in UTC to the installation's one S3-compatible destination
  (Settings › Backup destination). PostgreSQL runs a dump in a Nomad periodic batch job; a volume
  backup stops the service, archives its volumes and starts it again, inside the app's own copy.
  Restore is all or nothing and replaces what is there (files added since the backup are
  deleted); the console asks for the app's name first. Each run has an outcome, a duration and,
  for volumes, how long the service was stopped.
- **Volumes (009, 016).** A Compose volume becomes a Nomad dynamic host volume `<app>_<volume>`;
  a service with a volume runs one copy and deploys stop-then-start. The Backups tab opens with a
  Volumes section: mount, host path, size, last backup.
- **Registries (017).** Settings › Registries holds credentials per registry host, write-only,
  with "Test". A deploy that the registry refuses fails with one action, "Check the registry's
  credentials"; a missing image without credentials says "add credentials for <host> in
  Settings › Registries". The trade-off is documented: the credentials reach the app's own
  container environment, so the console asks for a read-only token.
- **Notifications (018).** Settings › Notifications holds channels (signed webhook, Slack,
  Discord, ntfy). What the needs-you list derives is pushed as "problem opened" and "problem
  resolved" (a flapping problem sends one "opened"), and every deploy ends as "deploy succeeded"
  or "deploy failed". Deliveries are retried twice and kept as history, 20 per channel.
- **Projects and environments (019).** Apps live in a project and an environment; everything
  from before is in Default › production with its old ID. Elsewhere the ID is
  `<project>--<environment>--<name>`, so a staging `blog` is `shop--staging--blog`, and every
  Nomad-facing name still derives from it. The home groups by project and environment (as before
  while only Default › production exists); `/projects/{project}` manages a project's display name
  and environments; Default and its production can't be deleted, and a project or environment
  with apps refuses to go, naming them.
- **Copy and promote (019).** The app page's Environments line lists the project's other
  environments: "Copy to" makes the app there with domains removed and no deploy (secret values
  were never stored, so the answer lists the missing secrets and a deploy is refused until they
  have values); "Promote to" previews what changes (image, digest, what is replaced, warnings)
  and deploys the source's newest healthy deployment into the target by digest, keeping the
  target's env, secrets, domains, replicas, resources and backup. A service only in the target
  refuses the promotion. The confirm carries the preview's revision; a 412 draws the preview
  again.
- **Rollback and export (011).** Every deployment records the definition it deployed (secret
  values left out). "Redeploy this version" checks that each pinned image still exists, then
  deploys with those digests pinned, and the version's definition (and Compose file, 015) becomes
  the stored app. "Export jobspec" downloads the job as plain Nomad HCL, behind a header naming
  what the job needs (Traefik, host volumes, secret variable paths); Configure has Export too.

### 1.2 What 007's section 7 asked for that landed without a drawing

Several tasks in one copy (a `names` sidecar so services reach each other by name, a
`caravana-volumes` prestart task), storage (volumes as declared host volumes, shown on Backups),
services inside an app (each service a panel on Configure and a row on Running), and periodic
jobs (backups). Rollout control (hold a rollout, promote a canary by hand) and foreign jobs
(read-only view, adopt) are still not built; the conflict with a job Caravana didn't create is
still one sentence on the failed entry.

## 2. Deliberate changes to the handoff, and why

The 007 rule for this section still holds: these are places where the build went another way,
not places where it lags. Three are large enough that we ask for them to be drawn back (section
3); the rest are listed so the next handoff knows what it is answering.

### 2.1 Configure absorbed Domains, Variables and secrets, and Backups into one tab

The handoff's Developer nav has a "Configure" group with three screens of their own: Domains,
Variables & secrets, Backups. The build put all of it on the app's Configure tab. Each feature
from 008 on shipped its console part with it (the working rule in `docs/plan.md`), and each
landed as a section on the tab that already held the definition: Secrets (008), backup
schedule and retention (010), the per-service edit form with env and domains (014), the Compose
form first for kept-file apps (015), plus the webhook token, Export and Delete app from before.
For a one-service image app it reads fine. For the Phase 2 test app (two services, a database on
a volume, a backup), Configure is now:

| Measure | Value |
|---|---|
| Page height | about 5,500 px |
| Forms | 10 |
| Inputs | 66 |

That is a page nobody can hold in their head, and the one primary button per view rule is
strained: the Compose form's Deploy, the definition form's Save and deploy (one for every service
at once) and the Secrets form's Set and deploy are three deploy buttons on one page. We'd like
the handoff's split back, adapted to what exists (3.4).

### 2.2 Settings lost the grouped left nav and is one page

The handoff's Settings has its own left nav in groups (Connections, Security, People) with a
count or a warning mark per item. The build's Settings is the 006 page, left-aligned at the
shared width (007, 3.9), with a section added per feature: Password, API tokens, Backup
destination (010), Registries (017), Notifications (018). With one admin the People and Security
groups had nothing to hold, and the nav looked like a frame around two sections. Five sections
later the page scrolls, "Send a test" sits far from "New token", and a warning on a registry
(its last test failed) has nowhere to show except inside its own row. We'd like the nav back
(3.5).

### 2.3 The deployment entry lost the Queued, Build, Roll out, Live stages

The handoff's `DeployScreen` has four stages across the top (Queued, Build, Roll out, Live) with
a log under each. The build kept 007's entry: one head line, one sentence, one progress line
while the rollout runs, the raw error, at most one action. Without a build there were two stages
and the progress line said both ("Rolling out: 1 of 2 copies healthy."), so the stage strip
would have been two cells and two dashes. Feature 020 adds a build, and with it the stages and
their logs are needed (3.1). The entry should keep what it learned since: the source line (a
redeploy of `v6`, promoted from staging, the webhook), the warnings, the pinned digests, and the
three actions at the foot.

### 2.4 Smaller drifts

- **No "Shipping now" card on Home.** The handoff puts a rolling deploy in a card above the
  projects. 014's spec put problems first, so the home opens with the needs-you list, and a
  rollout is not a problem: it shows as a "rolling out" pill on the app's row and as the newest
  Activity entry. Whether a rollout in progress deserves the card anyway is a question for you.
- **Logs has no levels, search, time range or histogram.** Nomad gives stdout and stderr per
  copy and nothing else: no level, no retention we control. A level would have to be read out
  of each line's text, so the build shows the stream tag as a word (007, 3.11) and offers Pause
  and Wrap. The plan now gives Logs its own feature (024: levels read from the line, a search, a
  time range), so the handoff's `LogsScreen` is the drawing for it; a request log (Traefik's
  access log) waits for Phase 3.
- **No Restart in the app header.** There is still no restart in the engine for a person to
  press (016 restarts a copy's tasks, but only to run a volume backup), and no Stop either.
  Both are now wanted (3.6; feature 021 in the plan).
- **Backups use UTC; the handoff's schedules use the owner's time zone.** A backup schedule is a
  five-field cron expression and the console says "every day at 03:00 UTC". Caravana has no
  profile and no time zone setting, and the server runs in UTC. The handoff's Schedules screen
  shows its times in the owner's own time zone, with the next runs as clock times. Either the
  console learns a time zone (a setting, or the browser's) or the UTC note stays; we'd like your
  view, since 3.2 adds a second kind of schedule.
- **Notifications pick by kind, not by event.** The handoff's "what goes where" is a matrix of
  nine events against every channel, with a project filter. In 018 a channel picks from three
  kinds (problems opened and resolved, failed deploys, successful deploys) and can be paused.
  One owner, few channels; the finer matrix can come when the events do (schedules, certificates,
  disk).
- **Confirmations are still disclosures, not modals or drawers.** The handoff's Add a domain,
  New schedule and Restore open as a right drawer or a modal. Everything added since 007 (Delete
  project, Restore, Remove registry, Remove channel, Promote) confirms in the page under its
  button, or on its own page (Promote has a preview page). Please keep drawing in-page flows.

## 3. What the next handoff should draw

In build order. Feature 020 is next; after it `docs/plan.md`'s Phase 2.6 takes the handoff's
Developer view one screen per feature: 021 the Configure and Settings shape (with Stop and
Restart), 022 Schedules, 023 Domains, 024 Logs, 025 Monitoring and capacity, with templates, more
channels and 2-step sign-in riding along. The last item here is what no screen has drawn. Please draw each at phone width as well: 390px, no
sideways scroll, 44px touch targets.

### 3.1 The Deploy screen with a Build stage and its log, for a single node (020)

Feature 020: a git repository plus a Dockerfile, built as a Nomad batch job, pushed to a
registry from Settings › Registries, deployed by digest. What we need:

- The four stages (Queued, Build, Roll out, Live) with a duration each, and the three logs
  (Build, Rollout, App output) under them, as `DeployScreen` has them, but for one node: no
  server names, no "placed on" line, no scheduler scores. The rollout log is what the progress
  line says today, line by line.
- Where it lives: as the deployment entry grown (the entry opens into the stages and the logs)
  or as its own page the entry links to. The entry pattern from 007 must survive either way: the
  sentence, the raw error, one action, warnings, pinned digests, the three actions at the foot.
- The source on the entry: repository, branch, commit (short hash in mono), who or what started
  it (a push, the form, the API). Autodeploy on push is the git twin of the image webhook (004)
  and ships with 020.
- A failed build as a sentence plus one action, like a failed deploy: "The build failed at step
  6 of 9 (`RUN npm run build`). Nothing was deployed; v6 is still serving." with "Open the build
  log". A failed build also reaches the owner as a notification (018) and the needs-you list.
- The prerequisite when no registry is set: a built image has to be pushed somewhere. The
  handoff's `InlinePrereq` ("Needs a container registry", with the fields inline) is the right
  shape; draw it on New app › Git repository and on the failed entry.
- New app gains a third tab, "Git repository" (repository URL, branch, Dockerfile path, the
  registry to push to), beside Image and Compose file. No size presets and no "fits on the
  cluster" yet.
- The build's own log while it runs: lines arrive as a stream, as the Logs tab does, with the
  same Pause and Wrap.

### 3.2 Schedules, for a single node

Nomad periodic batch jobs; backups already are one. The handoff's `SchedulesScreen` line "Runs a
command in a fresh copy of an app, then stops it" is exactly the model (the live version's image
and variables, no traffic). What to change for us:

- One node: no "running on" a named server, no node names anywhere.
- A schedule belongs to an app and a service (it needs their image, env and secrets), so draw
  it on the app page (a Schedules tab, or inside Deployments as "runs") as well as across apps.
- Runs, not rollouts: each run has a start, an exit code, a duration and its own log. The last
  14 runs strip and "how long each run took" are good; the log of a run is what Nomad kept.
- The limits: "if still running, skip the next run" is Nomad's own setting. "Stop after" (a
  timeout) is not; Caravana would have to stop the run itself, so draw it as optional.
- The time zone question from 2.4 has to be settled here, since a person types "every day at
  03:00" and expects their own clock.
- The backups row with "Made by Backups. Change the time in the database's backup settings." is
  right and should stay.

### 3.3 Domains, for a single node

Today a domain is a field on a service. We want the handoff's Domains screen, adapted:

- The DNS instruction is one A record to the host's address (and an AAAA record if it has one).
  There is no edge, no CNAME to follow, no three proxy addresses. For `blog.example.com`: Type
  A, Name `blog`, Value the host's IP. For the apex: Type A, Name `@`.
- The status column is what we want to read and show: "DNS points elsewhere" (with what it
  points at now), "getting certificate", "live · HTTPS", "certificate expiring". Traefik issues
  and renews with Let's Encrypt; the check is a DNS lookup plus the certificate's state.
- "Goes to" is `<app> :<port> · <environment>`, and the row links to the app.
- The options (Force HTTPS, Team only, www redirect) and the "Change path or port" action are
  the subject of 4.2: the first three can be drawn now; path routing waits.
- Where it lives: a Domains page across apps (the handoff's) and the per-service field on
  Configure, which stays as the place a domain is added with a deploy.

### 3.4 Configure split back to the handoff's information architecture

Take the 5,500 px page apart along the handoff's lines, keeping what each section learned:

- **Configure** keeps the definition per service: image, command and arguments, container port,
  copies, CPU, memory (reservation and ceiling, 013), health check. Plus the kept-Compose-file
  mode: the Compose form first, the definition read-only, "This app is edited through its
  Compose file.", and the "paste it once" state for an app made before the file was kept.
- **Domains** moves out (3.3).
- **Variables and secrets** moves out, for one app and one environment: plain env (values
  shown) and secrets (names only, write-only values, "no value means keep the current one"), the
  rule that each deploy replaces the whole list, and what setting a secret does (a deploy through
  the health-checked rollout). See 4.1 for the sources panel.
- **Backups** already has its own tab; the schedule and retention fields should move there from
  Configure, next to the runs they produce.
- A home for the rest: the webhook token (replace, revoke), Export, and Delete app last and
  apart. A "Danger zone" at the foot of Configure, as the handoff's Organization settings has,
  would do.
- Across apps or per app: the handoff's Configure group (Domains, Variables & secrets, Backups)
  sits in the sidebar, across every app. We have both views now (Backups per app, Registries
  across). Please say which goes where, and what the sidebar holds under 900px when it becomes
  the Menu disclosure.

### 3.5 The Settings nav

The handoff's grouped left nav, filled with what exists and what 020 adds:

- Account: Password. Security: API tokens, and 2-step sign-in for the admin (Phase 2.6).
  Connections: Backup destination, Registries, Notifications, and with 020 the git side (a deploy key or access token per repository, or one
  provider connection; please draw both shapes and we'll pick the one 020's research settles).
- The count or warning mark per item (a registry whose last test failed, a channel whose last
  delivery failed, a token that never expires) is wanted; today those warnings sit inside rows.
- Not coming yet, so leave them out of the nav rather than greyed: SSH keys, Single sign-on,
  Audit log, Members, DNS providers, Organization.
- Under 900px: a select, tabs, or the Menu disclosure again. Your call.

### 3.6 Five things the handoff is silent on

1. **Stop and Start as actions.** The handoff has Restart only. Stop is Nomad's `job stop`
   without purge: the copies go, the definition, volumes, secrets and history stay, and Start
   deploys the stored app again. Draw the stopped app: the ○ idle mark exists; the page header,
   the row on the home (a stopped app is not a problem for the needs-you list), the Running tab
   ("Stopped by you 2 hours ago."), and where Stop, Start and Restart sit given one primary
   button per view and no action in the top bar.
2. **Templates, or one-click apps.** `deploy/examples/` holds a Compose file, an overlay and a
   runbook per app (two today). "Start from an example" on New app: pick one, see the preview,
   fill in the domains and the secrets, create. A template is a Compose file plus an overlay, so
   it is the Compose tab with the file already there. Draw the list and the filled form.
3. **A shell, or "Run a command", into a copy.** Nomad can exec into a copy. A terminal needs
   scripted UI; the no-JavaScript first step is a form: pick the copy, type a command, see the
   captured output under it. Draw that, and the terminal as the later state.
4. **Exposing a TCP port.** A database reached from outside (`blog`'s Postgres on 5432 from the
   owner's laptop). It is a firewall rule on the host beside Traefik, which means a hole in the
   "only 22, 80 and 443 open" promise. The console must say so and ask for the source address
   range. Draw it as a field on the service with its warning, and where it shows on Running.
5. **A light theme.** The handoff is dark only. The status colors are ordered by lightness
   (failed 0.68 < healthy 0.76 < rolling 0.84 < warning 0.90) on a dark ground; on a light ground
   that order and the contrast have to be rebuilt, and the ink ramp inverted. Draw the four
   status marks, a panel, a pill, a button and a log line on light, following the system's
   `prefers-color-scheme` with a setting to force one.

## 4. Two tensions to settle

### 4.1 The Variables screen lists seven secret providers; the constitution names one

`VariablesScreen` offers Vault or OpenBao, Infisical, AWS Secrets Manager, AWS Parameter Store,
Doppler, Azure Key Vault and 1Password in "Connect a secret source". Constitution VII says
secrets live in Nomad Variables, external providers are optional, Caravana must work fully
without any, and a cold start must never wait on one. `docs/integrations.md` names Bella Baxter
as the first external provider (Phase 2.75 in `docs/plan.md`), authenticating with the copy's
workload identity so no key is stored, with a webhook on secret change that calls our deploy
webhook so the app is redeployed through the normal rollout.

So please draw the single-app Variables and secrets screen around "built in, plus Bella Baxter":

- The "Where secrets come from" panel with two rows: Caravana (built in, Nomad Variables, n
  values) and one Bella Baxter source (address, "copies sign in with their workload identity",
  which projects may use it). "Connect a source" has one shape, not a grid of seven.
- A referenced value shows its path (`bella · blog/stripe → live_key`), never the value, as the
  handoff already does for its `vault ·` rows.
- Rotation as a sentence: "When this value changes in Bella Baxter, blog is redeployed with it
  through the health-checked rollout."
- What happens when the source is unreachable (constitution VII): running apps keep their last
  rendered env; a new deploy fails with a clear error. Draw that failed entry.
- The other six providers are not in scope. If a later phase adds one, it will take the same
  row shape.

The "Edit as .env" view and "Copy from production" are welcome; the per-environment grid is
not, since an app page shows one environment and promotion (019) carries the target's own
values.

### 4.2 The routing extras are Traefik middlewares; path and header routing is not

The handoff's Domains options (Force HTTPS, Team only, Add www redirect) and the Proxy screen's
defaults (rate limit, timeout, upload size, slow start) are all Traefik middlewares attached to
the one router a service has, `<app>-<service>`, configured through service tags. Force HTTPS is
already true everywhere (HTTP redirects to HTTPS since 005). "Team only" is a forward-auth
middleware that asks the console for a session. A www redirect, a rate limit, a request timeout,
a body size limit and a slow start are each a tag. None of them needs a constitution amendment,
so draw them where the handoff has them.

Routing one domain by path or by header to different services ("Change path or port" in the
Domains row, the webfinger-and-inbox case in `docs/plan.md`'s backlog "Routing by path and
header") is different: it needs more than one router per service, and constitution VI names the
router `<app>-<service>`. That waits for an amendment and for Phase 3 or later. Please draw the
options now and leave "Change path or port" out, or draw it as a later state that says so.

## 5. Open questions from 007 that are still open

Checked against what 014 (Console v2) changed.

1. **The Apps headline, row and header disagree about health.** Still open. 014 put the
   needs-you list on the Apps home, so a failed newest deploy is said there, once, with its
   action. But the app page header's pill still comes from the running copies (healthy, n of m
   healthy, rolling out, not running), so an app with healthy copies and a failed newest deploy
   reads "healthy" in its own header while the home lists it under needs you. What should the
   header say?
2. **A 20-app user has no filter, search or keyboard path.** Still open. 019 grouped the home
   by project and environment, which helps a 20-app owner find a thing; `docs/plan.md` ranks a
   filter as cheap. At what size should it appear, and what is its smallest form?
3. **Should an app with a failed newest deploy lead its page with a needs-you strip?** Partly
   answered: 014 made the list per installation (on the home, derived from state, never
   dismissed, one action per item), not per app. The app page still leads with its header and
   tabs; the failed entry on Deployments carries the sentence and the action. Still open whether
   the app page should repeat its own item at the top.
4. **Focus to the first invalid field after an htmx post.** Still open: the script only focuses
   the delete-name field. Mitigated: the forms added by 014 and 015 post as plain HTML and the
   server autofocuses the invalid field on the page it draws back, so the gap bites only where a
   form posts through htmx.
5. **The revoke-token confirmation is an absolutely positioned overlay inside a clipped panel.**
   Still there, and now the odd one out: every row added since (registries, notification
   channels) opens its confirmation in the page under the row. Two patterns coexist; please pick
   one, and if it is the in-page one we'll move the token row to it.

If you'd rather answer only some of this: 3.1 (the Deploy screen with a Build stage), 3.4 (the
Configure split) and 4.1 (secrets around Bella Baxter) matter most, in that order.
