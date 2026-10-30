# Contract: 027 page 1, the Developer/Operator toggle and the Operator Overview (one host)

Written by the caravana_designer session on 2026-10-29, before the spec. The values come from the
handoff's `Shell.jsx` (`CV_NAV`, the `radiogroup` toggle, `isActive`), `NowScreen.jsx` (the
headline, the Cluster section, the apps table, Needs you, Live activity, Capacity),
`components/data/NodePanel.jsx`, `EventRow.jsx`, `Meter.jsx`, `Sparkline.jsx`,
`components/status/AllocChip.jsx` and `theme/caravana.css`, rendered from the prototype
(`design/console/_shots/pe-ops-now.png` in Operator mode, `pb-now.png`). Where this list and
the handoff disagree, this list wins. The 021–025 contracts still apply: the shell, fields,
selects, icons, page head, the time rule (UTC first, the viewer's local time beside it), the
No-Dim-Text Rule (text never fg-4), and the console never says "ship". 025's host numbers
(memory used without the page cache, reserved by Nomad's rule, the two watched disks, the room
sentence) are the numbers here: one source, the same words.

## What page 1 is

- **Two views, one shell.** The handoff's Developer/Operator toggle comes to the sidebar. The
  Developer view is everything built so far. The Operator view is about the host Caravana runs
  on: today one server. Multi-server screens (Servers as a list of many, Capacity across
  servers, Scheduler, Node pools, adding a server, moving work) stay in Phase 3, and nothing in
  this feature draws them.
- **The mode is the URL.** Operator pages live under `/ops` (`/ops` is the Overview,
  `/ops/servers/<name>` page 2, `/ops/updates` page 3). There is no stored preference to get wrong:
  being on an `/ops` page *is* Operator mode. Pages both views share (Settings, an app's pages,
  New app) draw the sidebar in the mode the owner was last in, remembered in a cookie
  `caravana_mode=dev|ops` that every page of a mode sets (server-side, `SameSite=Lax`, not a
  security value). Sign-in and `/` land on the Developer home as today.

## The toggle (sidebar)

The handoff's `radiogroup`, under the brand block (page 1 of 021 removed it; it comes back):

- A `<nav aria-label="View">` holding two links styled as the segmented control: grid
  `1fr 1fr`, gap 2, padding 2, radius 4, ink-1 ground, 1px line-1 edge. Each link 30px tall,
  radius 3, 500 13px sans, centred: the current one ink-3 fg-1 with `aria-current="page"`, the
  other transparent fg-3 (hover ink-2 fg-2). **Developer** links to `/apps`, **Operator** to
  `/ops`. Plain links: no script needed, no `role="radio"` (they navigate, they don't select).
- The top block regains its 12px inner gap between the brand and the toggle
  (`padding 14px 14px 12px`).
- Under 900px the toggle is the first row of the Menu panel, the same shape.

## The Operator sidebar

The handoff's `CV_NAV.ops`, reduced to entries that have a page (021's rule: an entry to "not
designed yet" is worse than no entry):

- Group **Server** (the handoff's "Cluster": one host isn't a cluster): **Overview** (icon
  `layout-grid`, `/ops`), **Server** (icon `server`, `/ops/servers/<the host's node name>`, page 2;
  `/ops/servers` redirects to the one server until Phase 3 makes it a list).
- Group **Platform**: **Updates** (icon `download`, `/ops/updates`, page 3). The handoff's Node
  pools and Certificates aren't drawn here: node pools are Phase 3, and certificates already
  live in Settings › Certificates (023).
- Later contracts add Activity, Logs and Proxy & routing to these groups.
- Item shapes, the active bar and Settings at the foot are page 1 of 021's, unchanged.
- **Active rule** (the handoff's `isActive`): Overview on `/ops`; Server on `/ops/servers*`;
  Updates on `/ops/updates*`; on an app's pages while in Operator mode, Overview.
- Updates carries 021 page 3's nav count/mark rule: ■ when a reboot is needed, ◆ when
  security updates are waiting, else the count of available updates ("1" for a newer caravana),
  else nothing (page 3 defines each).

## Crumbs and head

- Top-bar crumbs: `Overview`, `Server`, `Updates` (one crumb, the page's name; no organization
  level). The top bar keeps New app (021 page 1: on every page).

## The Overview (`/ops`)

`main`: max-width 1440, padding 36px 26px 62px, column gap 36 (the handoff's).

### Headline

- The h1 (headline type, 30px fg-1, `text-wrap: pretty`): **the same sentence the Developer
  home draws** (one function), so the two homes never disagree: "11 apps healthy, 1 rollout in
  progress. `metabase` needs you." / "… Nothing needs you." in fg-3.
- Under it the stats line, 400 13px mono fg-3, items 23px apart: **"1 server · 13 apps · 18
  copies · cpu 38% · mem 63% · 3 open items"**. cpu and mem are the last 10 minutes' used ÷
  total from 025's host samples (memory without the page cache). "open items" is the needs-you
  count. With no host sample yet: "1 server · 13 apps · 18 copies · waiting for the first
  reading".

### Main column and aside

Grid `minmax(0, 1fr) 448px`, gap 26 (the handoff's), `align-items: start`. Under 1200px of
container width the aside drops below the main column (024's container rule).

#### Server (main column, first)

- `SectionHead` **"Server"** with meta the 025 room sentence ("Room for about 20 more apps like
  `blog` (32 MB, 0.05 vCPU each), but memory in use is already at 87%."), no right action (the
  handoff's "Add node" is Phase 3).
- One `NodePanel` for the host, full width of the main column (the handoff's three-up grid has
  one panel; spanning keeps the chips readable). The panel links to the server page:
  - `.cv-panel` look: ink-1, 1px line-1 (line-2 on hover), radius 6, padding 16, column gap 13;
  - header: the node's 8px mark (● ready, ◐ draining, ◆ ineligible, ■ down; Nomad's states in
    the handoff's `NODE_STATE` map), the host name 500 14px mono fg-1 (Nomad's node name, which
    the installer sets to the hostname), the node pool chip when Nomad reports one (`default`:
    400 12px mono fg-3, 3px 7px, 1px line-2, radius 2), a spacer, then "18 copies" 400 12px
    mono fg-3 (or the state pill sm when not ready);
  - the spec line, 400 12px mono fg-3: **"4 vCPU · 7.8 GB · Ubuntu 24.04 · kernel
    6.8.0-147-generic"** (from Nomad's node attributes). The handoff's provider type and region
    ("cx32", "hel1") aren't drawn: Caravana can't know them without the provider's API;
  - the three meters (`Meter`: label width 40, 6px bar, reserved hatched for cpu and mem, the
    readouts "1.2/2 vCPU", "1.7/1.9G", "5.8/18.3G"); colour by 025's rule (fg-2 below 85%, warn
    from 85%, failed from 95%; for disk, the open item's state while one is open);
  - the copies as `AllocChip`s (wrap, gap 5): one chip per running copy, "app" fg-1 then the
    copy's short ID fg-3, the copy's mark (starting dashed ◐, draining dashed ○ line-through,
    failed with the failed ground), grouped by app in the table's order; each chip links to its
    app's Running tab. More than 40 copies: the first 40 and "and 12 more copies" 12px fg-3.

#### Apps (main column, second)

- `SectionHead` **"Apps"** with meta "13 running" (the count of apps with a running copy).
- The handoff's table in a `.cv-panel`: head 36px ink-2, 500 12px mono fg-3 uppercase
  (`.cv-label`), columns `224px 134px 104px 124px 90px minmax(0, 1fr) 180px`, padding 0 16px,
  rows 40px, 1px line-1 dividers, 400 13px mono fg-2; one row per app, all places (the Operator
  view is the host's, not one environment's: the app's place, when not Default › production,
  prefixes its name in fg-3, "Shop › staging › `web`"):
  - **App**: name 500 fg-1, then the kind word 12px fg-3 (page 4's rule);
  - **State**: the quiet sm pill of the app's status (page 1's words: "healthy", "rolling out",
    "deploy failed", "not running", "stopped");
  - **Copies**: one 7px mark per copy, gap 3;
  - **CPU 1h**: an 84×16 sparkline of the last hour from 025's samples (fg-2 line, warn when
    the app is unusual by 025's rules; the handoff's healthy blue isn't used: colour is status);
  - **Mem**: memory used now (025's "used"), "810 MB" / "1.4 GB";
  - **Reach**: the first domain, ellipsized (fg-2), "no address" fg-3;
  - **Last change**: page 4's who-words compact, "v31 · 3 d ago" fg-3.
  - Sorted problems first (failed, warning, rolling, healthy, stopped), then by name. Each row
    links to the app.
- The handoff's "Reach" header collides with "Change" at narrow table widths (the render shows
  "REACƗST"): the Reach column has a minimum of 140px, and the table folds below 1000px of its
  own width (the home's fold: name and state, then label/value pairs).

#### Needs you (aside, first)

- `SectionHead` **"Needs you"** with meta the count ("3"), or "nothing".
- The **same list and rows** as the Developer home (one template), each with its one action
  link as built. The handoff's one-click fixes ("Use 6 GB", "Raise to 2 GB", "Prune old
  images") and "Why?" aren't in this feature: every action in the console stays an explicit page
  or a confirmed form. With nothing open: the `.cv-empty` line "Nothing needs you."

#### Live activity (aside, second)

(Ruled 2026-10-29 on the research: Caravana's own records, not Nomad's event stream.)

- `SectionHead` **"Live activity"** with the right-hand note "from caravana's records" 400 12px
  mono fg-3 (the handoff's "nomad event stream" isn't what we read).
- A `.cv-panel` of the last 8 entries as `EventRow`s: grid `64px 92px minmax(0, 1fr)`, gap 13,
  padding 10px 16px, 1px line-1 between rows: the time 400 12px mono fg-3 tabular ("14:02:14",
  UTC, with the script's local time in its `title`), the kind 400 12px mono fg-3 in caravana's
  words, then a 6px mark and the sentence 14px/1.4 fg-2.
- **Source**: the records caravana already keeps, merged newest first:
  - **deploy**: a deployment started and ended, in page 4's who-words ("`supplier-gw` v24 is
    live, pushed to `main` by ana.", "`booking-api` v57 failed its health checks. v56 keeps
    running.");
  - **restart** / **stop** / **start** / **command**: app actions (020.5), never their output;
  - **backup** / **restore**: runs and their outcome (016);
  - **schedule**: a schedule run that failed or was stopped at its limit (022; ok runs aren't
    listed, they would drown the panel);
  - **needs you**: an item opened or resolved ("`metabase` keeps restarting: out of memory.",
    "Resolved: `hel` disk below 83%.");
  - **server**: the node's own events from Nomad's node record (`Events[]`: registered, drained,
    heartbeat missed, back), and a boot (page 2).
- Records survive a restart, so there is no "since caravana started" line. Nomad's event stream
  isn't read (megabytes of JSON per hour, mostly evaluations and sidecars, lost when Nomad
  restarts). Per-copy health lines ("`c81e` passed its health check") aren't shown; crashes and
  out-of-memory kills arrive as needs-you entries.
- New entries arrive on the console's existing live notices (deployments, actions, backups,
  schedules, attention); the panel swaps by row id, and a new row gets the fresh tint once (the
  One Tick Rule). Without script the panel is the page's snapshot.
- The full feed is the Activity page, a later contract; this panel has no "See all" link until
  that page exists.

#### Capacity (aside, third)

- `SectionHead` **"Capacity"**, then 025's host meters and room sentence in a `.cv-panel`
  (padding 16, gap 10), the **same template** as Monitoring's "This host" without the 7-day
  chart. The handoff's sentence "Memory runs out first. With 10 GB unreserved you can add about
  4 more apps like traveler-web … before you need another node." becomes 025's room sentence:
  one host can't "need another node" in this phase, so the sentence doesn't say it.

### Errors and empty states

- Nomad unreachable: the console's notice at the top of `main` ("Nomad can't be reached right
  now, so the server's state isn't shown."), the NodePanel replaced by that sentence with ○, the
  activity panel "Can't read Nomad's events right now." with ○; the apps table and Needs you
  still draw from Caravana's records.
- No apps yet: the Server panel draws (meters, no chips), the Apps section is the `.cv-empty`
  "No apps yet." with New app.

## Narrow (390px)

- The toggle stays two equal halves in the Menu panel.
- The headline wraps; the stats line wraps between items, never inside one.
- The NodePanel's meters stay full width; the chips wrap.
- The apps table folds (above); the aside sections follow the main column.

## May differ (server-rendered, no JavaScript)

- The toggle is two links, not a radio group.
- Live activity is a snapshot without script; no fresh tint.
- No one-click fixes, no "Why?", no "Add node", no provider type or region.
- The org switcher stays out (021 page 1).

## What one host can't draw (left out, said here so nobody draws a placeholder)

- Several NodePanels, "Add node", node pools as a choice, placement across servers.
- "before you need another node" in the capacity sentence.
- Evaluations that only matter across nodes (the handoff's "placed 2 of 2 copies across hel-2,
  fsn-1" reads "placed 2 copies" on one host).

## API (names only, for the spec)

`getHost` (025) grows: `node{name, state, pool, os, kernel, nomadVersion, dockerVersion, cpu{
cores, mhz}, memoryMB, uptimeSeconds}`; `listActivity` (`GET /v1/activity?limit=8` →
`entries[]{time, kind, status, message, app?, place?}`, caravana's records and the node's events,
newest first);
`listApps` already has what the table needs plus 025's per-app samples (`getMetrics`'
`services[]`). The `caravana_mode` cookie is UI only. Times RFC 3339 UTC.

## Review

The designer checks at 127.0.0.1:7070 against `pe-ops-now.png`:
- the toggle on both views, the cookie carrying the mode onto Settings and an app's pages, the
  Operator sidebar with Overview active;
- `/ops` with the lab's apps: the shared headline, the stats line, the Server panel (spec line,
  meters at 87% memory in warn, chips by app with a starting and a failed copy), the apps table
  across places with sparklines and the fold, Needs you as on the home, Live activity during a
  deploy, a restart, a backup and a failed schedule run (fresh rows), still filled after a
  caravana restart, Capacity;
- Nomad unreachable; no apps; 1440, 1200, 640 and 390 widths; the page without script.

## Review rulings (designer, 2026-10-30)

Checked headlessly at 1440 against the lab instance; the toggle and the mode verified as contracted.

Fixes:
1. **Stacked order.** At 1440 the container is under 1200, so the aside drops: when stacked the order is Needs you,
   Server, Apps, Live activity (grid areas; side by side unchanged).
2. **No Capacity section** on one host: its meters and room sentence repeat the Server panel and its meta. The Server
   panel's meters take the split readouts ("1.5G used · 0.7G reserved of 1.9G", "0.1 used · 0.72 reserved of 2
   vCPU"). Capacity returns with Phase 3's many servers.
3. **The apps table stays inside its panel:** text cells `min-width: 0` and wrap in their column; at 1440 the
   table's scrollWidth equals its panel's width.
4. **Activity sentences are sentence parts**, drawn with the console's `sentence` template: program, app, service,
   schedule and version names in mono everywhere (replaces the first-word rule).
5. **A rolling-out deploy entry is ◐**, not ○ (○ is stopped).
6. **One event, one row:** a needs-you "opened" entry only for kinds with no record of their own (disk, unhealthy,
   restarting, out of memory, DNS, certificates, OS updates, no backup destination); deploy, schedule and backup
   failures already have their row. "Resolved:" entries are always listed.
7. **A needs-you entry uses the needs-you list's own sentence**, not the notification's short title.

Departures ruled: the table folds under 860px of its own width (yes); Needs you's empty state is the home's paragraph
(yes); no copies count when Nomad is unreadable (yes); the activity note when Nomad is unreachable reads "Can't read
the server's own events right now." (yes); local time in the title, not the cell (yes); a terminal session is kind
`command` and "Resolved: " precedes the sentence (yes); `rebootRequired` waits for page 3 and bad activity queries are
422 `invalid_field` (yes).

## Recheck and critique rulings (designer, 2026-10-30)

The recheck at f0fd007 measured no overflow at 1440, 1100 and 1700 and confirmed the six fixes. The critique
(28/40, no P0, two P1) is saved in `.impeccable/critique/`. Ruled, to do in one pass:

1. **The fold:** each folded value is one element (the cell has two items, label and value), and the pairs sit as
   label-over-value cells in `repeat(auto-fill, minmax(9rem, 1fr))` under the name and state line (about 80px per app at
   800px).
2. **Column widths:** Last change 160px nowrap; State 136px nowrap; the fold moves to under 920px of the table's own
   width.
3. **Unusual numbers by more than colour:** State stays the run state. A 7px ◆ warn mark before the CPU sparkline; mark
   and line are one link to the app on `/monitoring`, with 025's issue sentence plus ", open monitoring" as its
   aria-label and title. The amber line and the mark come only from 025's `Judge` issues for the app; no issue, an fg-2
   line and no mark.
4. **One CPU and memory window:** the stats line uses the same newest sample the meters show (no 10-minute mean).
5. **A stopped app:** 025's ruling exactly, "—" and "CPU while the app ran; it is stopped now"; never a "now" value.
6. **Focus:** the server panel and the apps table are separate regions swapped by id; row links and chips have stable
   ids (`ops-app-<app id>`, `ops-copy-<copy id>`); no new script.
7. **Activity under 420px of the panel's own width:** time and kind share a line above the sentence.
8. **The feed's place label** is the full one, as the table and needs-you have it ("Shop › staging › `web`", place in
   fg-3, name in mono); Default › production stays the bare name.

Minor: the note reads "from caravana's records · times in UTC"; the needs-you action cell gets a min-width that fits the
longest label ("View deployment"), button right-aligned, in the shared template (the home too); the DISK readout goes
under its bar like MEM and CPU. Kept as is: "5 apps need you" beside "Needs you 6" (apps against items), the copy chips,
stacked at 1440, no sort or filter.
