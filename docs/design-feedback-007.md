# Console 007: what we built, what we changed, what we need next

For the Claude Design loop. This is a report back after the first build of the console
(feature 007, commit `9a52daf`, branch `007-htmx-ui-v1`). The handoff was "caravana console",
Handoff 2 (2026-10-04). The built result is described in `DESIGN.md`; this file is the
difference between the two, and the questions we'd like the next handoff to answer.

All examples use `whoami`, `blog` and `example.com`. No sample data from the handoff is in the
repo.

## 1. How we built it (so the next handoff fits)

The handoff said React + Vite + Tailwind + daisyUI + Lucide. We built it differently, on purpose:

- **Server-rendered pages.** Go `html/template`, one set per page, all embedded in the one binary.
  No SPA, no Node, no router. Every page is its own URL and works without JavaScript; htmx only
  improves it. So the `.jsx` files were reference for layout, copy and behavior, not code.
- **Live updates are server-sent events** carrying only notices ("deployments changed"). Each
  region then refetches its own fragment. One stream per page. The prototype's simulated metrics
  tick (CPU every 3 s) has no equivalent yet, see section 4.
- **Stylesheet:** Tailwind v4 standalone CLI + daisyUI 5 over `theme/caravana.css`, generated and
  committed (`tools/ui-css.sh`). Colors, type scale and radii are the handoff's tokens.
- **Fonts:** IBM Plex Sans and Plex Mono, self-hosted (Latin-1 subsets). **Icons: none ship.**
  Everything is text plus the status marks.
- **Script:** a small hand-written `console.js` (live indicator, copy buttons, log view, delete
  name check) plus htmx and its SSE extension, vendored and pinned by checksum. The page's
  Content-Security-Policy is `default-src 'self'`: no inline script, no inline style, no eval. So
  every style is a class; nothing is styled by attribute.

What this means for design: prefer patterns that work as plain HTML first (forms, `<details>`,
links). Anything that needs scripted state (popovers, drag, drawers, command palette) costs us
more than it did in the prototype.

## 2. Screens: handoff versus built

Built (Developer view only), each with real data and an API twin:

| Handoff screen | Built as | Notes |
|---|---|---|
| Home (`DevHome`) | **Apps** (`/apps`) | Headline sentence, apps table, Activity rail |
| App detail: Running / Ship / Configure / Logs | **Running / Deployments / Configure / Logs** | Each tab is its own URL. "Ship" was renamed, see 3.1 |
| Deploy progress (`DeployScreen`) | A progress line inside a **deployment entry** | Not a separate screen |
| New app (`NewAppScreen`) | **New app** (`/apps/new`) | One form, image only |
| Logs (`LogsScreen`) | **Logs tab** | Copy and stream choice, Pause, Wrap |
| Settings | **Settings** (`/settings`) | Password and API tokens only |
| Login | **Login** | Email and password only |

Not built yet, because the engine can't back them with real data (the product rule is "no screen
shows placeholder data as if it were real"): Operator view entirely (Overview, Servers, Capacity,
Activity, Scheduler, Node pools, Proxy, Certificates, Updates), projects and environments,
Variables and secrets, Placement failure, Domains, Previews, Monitoring, Schedules, Backups,
Members, org switcher and New organization, SSO, 2-step code.

## 3. Deliberate changes to the handoff, and why

### 3.1 The tab "Ship" is called "Deployments"
"Ship" read as a verb on a tab and nobody could say what it would do. The tab lists deployments
and holds the deploy form, so it is named for what it contains.

### 3.2 No "needs you" banner; the headline carries it
The handoff's problem banner (status mark + sentence + one button) and the `NeedsYouItem`
component are not built as a panel. The Apps page opens with one headline sentence, and the
sentence is what names the problem: "1 app needs you: whoami has a failed deploy. 1 app healthy."
Failing rows sort first. Reason: with one admin and a handful of apps, a separate panel repeated
the headline. We'd like your view on whether that holds when there are 20 apps (see 5.2).

### 3.3 A failed deploy is a sentence plus one action, never an alert box
Same treatment everywhere it appears (Apps headline, row, page header, Running tab, history
entry). On the newest failed entry: a plain sentence of what happened and what Nomad did, the raw
Nomad error under it in muted mono, and one action ("Deploy again", or "Change the image" when the
image wasn't found) that links to the deploy form. Written in `DESIGN.md` as "Failed-deploy
treatment". A deploy Nomad reverted does not count as "needs you" (what runs is what was running),
but its pill reads "reverted" in the failed look.

### 3.4 One primary button per view
The handoff had Restart and Deploy side by side in the app header. We show one primary:
- Running, Configure, Logs: "Deploy" in the top bar.
- Deployments: the form's own "Deploy" button is the primary, the top bar shows none.
- Apps: "New app" in the top bar only while the list isn't empty; an empty list puts its one
  primary inside the empty panel.
There is no Restart yet (there's no restart in the engine). Known wrinkle: opening the "Replace
token" confirmation on Configure shows a second primary-weight button next to the top bar's Deploy.

### 3.5 Confirmations are disclosures in the page, not modals
Replace token, Revoke token and Delete app each open a `<details>` under their button. Delete app
sits last and apart, with a danger edge; it asks for the app's name ("Type blog to confirm") and
the button stays disabled until the name matches exactly (the server checks it again). After a
refusal the disclosure opens itself with the error under the field. Reason: works without script,
no focus trap to build, and the consequence sits next to the button.

### 3.6 Configure is a read-only receipt
The handoff's Configure was an editable form (copies, CPU, memory, image, health check, Auto-revert
and Canary switches, registry prerequisite). We show the stored definition as facts: one panel per
service, rows of a label column and mono values. Facts nobody set are one muted line ("Using
defaults: command, arguments, ..."), not a column of "none". Environment values are never shown,
only names. A one-line note says editing here comes later and points at the API. Reason: editing
needs secrets (008) and the app model's edit path first. This is the tab where the handoff and the
build differ most.

### 3.7 Tables stack by their own width, with per-cell labels
The Apps table stacks under 720px of table width and the copies table under 660px (container
queries, not screen width). Column heads go away when stacked, so each remaining fact carries its
column name in small mono above its value. Found in review: at 768px the sidebar leaves only about
470px of content, and unlabeled stacked cells ("v0", "3 minutes ago") meant nothing.

### 3.8 The sidebar leaves at 900px, not 768px
Under 900px the sidebar becomes a "Menu" disclosure in the top bar (brand, links and account row
inside). Breadcrumbs hide and gutters become 16px. The Activity rail drops below the main column
under 1452px of viewport.

### 3.9 Settings is left-aligned at the shared width
It started as a centered 720px column. Review flagged three different content widths across the
shell, so Settings now uses the same one as the other pages.

### 3.10 The delete confirmation label shows the name as typed
The uppercase section-label style was upper-casing the app's name ("TYPE BLOG TO CONFIRM") while
the match is case-sensitive. A literal name inside any uppercase label is now set in its own mono
at normal case (rule: "Mono Is Real" wins over the label style).

### 3.11 stderr is a word, not an amber color
The log tag column says "stderr" in muted text for stderr lines and stays empty for stdout. Many
programs write ordinary start-up lines to stderr, and amber made those read as faults.

### 3.12 Login has no SSO, 2-step or reset flow
Email and password, plus the host in mono and, in its own block, the command that resets the
password on the server (there is one admin and no email). The 380px centered column is as in the
handoff.

## 4. Things the handoff has that the build can't show yet (data we don't read)

- **CPU and memory per copy** (`LiveMetric`, sparklines, "of 0.25 reserved"). We read allocation
  status and restarts but not Nomad's stats. The Running table has: copy, service, status, version,
  restarts, up for. A metrics feature would add the columns and the live tick; the theme already
  defines a tick flash and a drop-in that no screen applies.
- **Node column** on copies (one node today).
- **Placement failure** (why a copy can't be placed, with fixes).
- **Rollback / Revert** on a history entry (feature 011) and "Canary first" / "Auto-revert" as
  switches (the engine always does blue/green with auto-revert; nothing is switchable yet).

## 5. What we learned that the design should absorb

### 5.1 Patterns that are new, please keep or redraw them
- **Live indicator** in the top bar: "Live" (disc), "Reconnecting…" (tilted square), "Signed out.
  Sign in again." (ring). Shape and words for each state, polite `role="status"`; at phone width
  only the mark shows. The prototype had a live clock instead.
- **Deployment entry:** version, pill, trigger/time/duration at the right; then sentence, progress
  line, raw error, at most one action, warnings, pinned digests. Newest first, "Show older".
- **Log view:** toolbar of two selects and Apply (a plain GET form), Pause with a "Paused. 12 new
  lines." counter, Wrap lines, a 5,000-line cap with "Older lines were dropped."
- **Per-app tab counts that mix meanings:** `Running 1` is a count of healthy copies,
  `Deployments v0` is the live version. Review called that inconsistent; worth deciding.
- **Empty states** say what's missing, one line on what to do, and carry the page's one primary.

### 5.2 Open design questions from the review (score 24/40, "Acceptable")
1. The Apps headline, row and header must never disagree about health. We fixed the Apps side;
   the app page header still says "healthy" when copies are healthy but the newest deploy failed.
   What should the header say?
2. A 20-app user has no filter, search or keyboard path to an app. At what size should a filter
   appear, and what is its smallest form?
3. Should an app with a failed newest deploy lead its own page with a "needs you" strip (one
   fix button), with tabs below, as the review suggested? The handoff's banner idea, applied per
   app instead of per cluster.
4. After an error in a form posted through htmx, focus should go to the first invalid field. The
   plain post does it; the htmx path doesn't yet.
5. The revoke-token confirmation is an absolutely positioned overlay inside a clipped panel; it
   fits only because of what sits below it. A flow that doesn't depend on that would be safer.

## 6. What we need from the next handoff

In build order (the plan is 008 Secrets, 009 Compose, 010 Databases and backups, 011 Rollback and
export, 012 One-command install):

1. **Variables and secrets** (008). The handoff's `VariablesScreen` shows all environments in one
   grid with overrides marked. We have one environment per app today. We need the single-app
   version: plain env versus secrets as two kinds (names visible, secret values write-only, shown
   once if generated), "declarative replace on deploy" explained in the UI, and what a rotation
   does ("redeploys through the health-checked rollout"). Plus an editable Configure to host it.
2. **Compose import** (009): where it starts (New app has only an image today), how warnings for
   "supported but changed in meaning" and the loud "can't honor this field" errors look, and how a
   multi-service app reads on Running and Configure (services are task groups; we show them as
   panels on Configure and rows by service on Running).
3. **Databases and backups** (010): `BackupsScreen` adapted to one node and a host volume,
   including restore, and a database app's Running view (pinned to a node, volume shown).
4. **Rollback** (011): a revert action on a deployment entry and an "export as Nomad job" action.
   The handoff has `LedgerRow` with Revert; we have the entry but no action yet.
5. **Narrow-screen versions** of whatever comes next: we hold 390px as a requirement (no sideways
   scroll, 44px touch targets on coarse pointers), so please draw phone width for new screens.

If you'd rather answer only some of this: 5.2 items 1 and 3 and 6.1 (secrets) matter most.

## 7. Nomad concepts nobody has designed yet

Source: a plain overview of what Nomad offers on top of jobs, tasks and allocations (job types,
lifecycles and sidecars, service discovery, update strategies and scheduling rules, persistent
storage). Read against what 007 built, the console only knows one kind of workload, a web
service, rolled out blue/green. Below is what is missing, in the order we will need it. The same
rule as everywhere: plain words in sentences, the Nomad term only in a mono detail line.

### 7.1 Needed for Phase 2 (008 to 011)

1. **Workload kinds** (Nomad: service, batch, system).
   - New app only offers "an image and an optional port"; a worker is just an app without a port.
   - Backups and Schedules are periodic jobs. A batch job has runs, not rollouts: a list of runs,
     each with an exit code, a duration and its own logs.
   - Design a "Runs" variant of the Deployments tab, and a kind choice on New app: web, worker,
     scheduled. The handoff's `BackupsScreen` and `SchedulesScreen` cover the lists, not the app
     page or the New app choice.
2. **Several tasks in one copy** (Nomad: prestart and poststart hooks, sidecars).
   - Compose `depends_on`, a migration that runs before the app starts and a log shipper beside
     the app all become ordered tasks inside one copy.
   - Running needs a "preparing" state for a copy whose pre-start task is still running.
   - Logs needs a task selector next to Copy and Stream.
   - A deployment entry needs to say "ran migrate first, then started".
3. **Rollout control** (Nomad: canaries with approval, parallelism, auto-revert).
   - The engine already auto-reverts and keeps canaries away from traffic until promoted, but
     nothing in the console lets a person hold a rollout.
   - Design the "waiting for your go" state, with promote and revert on the deployment entry and
     on Running.
   - Design the full set of deployment states: pending, running, waiting for your go, healthy,
     reverted, failed, superseded (the newest deploy wins).
   - The handoff's `DeployScreen` and its Canary first and Auto-revert switches cover part of this.
4. **Storage** (Nomad: host volumes now, CSI volumes later).
   - A Volumes view and its link to an app: which volume, which server, size and use, and
     "pinned to node-1".
   - Backup status and restore on that view.
   - A compose import state where an unsupported volume is refused with a clear error, because
     nothing may be dropped silently.
5. **Services inside an app** (Nomad: native service discovery, dynamic ports).
   - An app-level map for compose apps: which service is public (has a domain) and which is
     private, each one's internal address, port and health.
   - A Domains screen now, not later: hosts, HTTPS state and DNS instructions, since real
     certificates are issued already.
6. **Jobs Caravana didn't create.** Not in the Nomad overview, but we hit it for real: a job with
   the same name made a first deploy fail.
   - Nomad users who arrive from other tools will have these.
   - A read-only view of foreign jobs and of platform jobs (Traefik is one), and what "adopt"
     would mean for a job.
   - Today the conflict is only a sentence on the failed entry.

### 7.2 Needed for Phase 3 and 3.5

7. **Placement** (Nomad: affinity, anti-affinity, spread, constraints).
   - Plain choices on Configure: spread copies across servers, never two on one server, only on
     servers with a label. The handoff's Scheduler, Node pools and Placement screens can host
     them.
   - Jobs that run on every server (Nomad system jobs, such as a log collector or monitoring
     agent) need a row type in the Operator view.
   - Drain needs a design: what happens to each app on the server being emptied.
8. **GPU and binaries.** A GPU request on New app and Configure (count, minimum memory), a
   "no server fits" state instead of a job stuck pending, and a binary source choice on New app
   (a release URL and checksum, no image).

### 7.3 Do not design these

- A service mesh (Consul Connect, mutual TLS between apps): the project rules exclude Consul.
- Sysbatch (a batch job on every node once): no use case yet.

### 7.4 One rule for all of it

Say it in words, put the Nomad term in a mono detail line ("preparing" with `prestart` under it,
"runs on every server" with `system job` under it), and keep Nomad's own screens (export, foreign
jobs) behind a disclosure. If a screen is only useful to someone who knows Nomad, it is an
Operator screen.
