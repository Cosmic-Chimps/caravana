# Contract: the project page (`ProjectScreen`) and the sidebar's order

Written by the designer on 2026-11-03. The values come from the handoff's `DevHome.jsx`
(`ProjectScreen`, `EnvBadge`, `DeployList`, `envServices`), `Shell.jsx` (`CV_NAV`) and
`theme/caravana.css`, in `design/console/` (local, gitignored). Pages 1 to 4 of 021 still apply
(`specs/021-console-ia/contracts/`). Where this list and the handoff disagree, this list wins: it
records the decisions the console made. The console never says "ship" or "service" for an app.

## Scope

- `/projects/{project}` becomes the handoff's `ProjectScreen`: the header, the environment tabs,
  the environment's apps as a table, and a rail (Environment, Needs you, Deploys in `<env>`).
- The forms that were the page (display name, Add environment, the deletes) move to
  `/projects/{project}/settings`, so the table leads.
- The sidebar takes the handoff's order and gains its **Configure** group.
- Unchanged: `/projects` (the landing page) and `/projects/new`. No API change: `listApps`
  already carries everything a row and the panel say.

## Rulings on what Caravana can't know

1. **Branch and "when it deploys".** They live in CI, not in Caravana. The Environment panel's
   **Deploys** row says how the environment was *last* deployed, from the newest deployment of
   any app in it, in the console's one set of who-words (`deployWho`, 021 page 4), capitalized
   and followed by when: "Pushed to `main` by ana · 2 hours ago", "Promoted from `staging` ·
   3 days ago", "From the console · just now", "Through the API by `ci-github` · …", "By the
   webhook · …". With no deployment yet: "None yet".
2. **Variables** ("6 shared · 14 per service"): left out. Caravana has no shared variables
   (Phase 4's `VariablesScreen`); per-app variables are on each app's Variables page.
3. **"Promote to production".** Promotion is per app, so the panel lists apps, not one button:
   for each *other* environment of the project, a row **"Promote to `<env>`"** whose value is
   the apps of the current environment that can be promoted there, one secondary sm link each
   ("`web`", to the existing preview `/apps/{id}/promote?to=<env>`). An app can be promoted when
   it passes the header's cheap test (`promotable`: defined, not stopped, newest deployment
   healthy with a recorded definition) **and** an app of the same name exists in the target
   environment (promotion needs a target; Copy to is the app header's job). A row with no app is
   not drawn; with none at all, the panel has no Promote rows. Never run the promotion plan
   (`CheckDigests`) to draw a page.
4. **"Services · N" reads "Apps · N".** One App is one Nomad job (constitution II) and the
   console calls them apps everywhere; a service is a part of an app.
5. **"Add service" reads "New app here"**, a secondary sm button with the plus icon, linking to
   New app with the current environment chosen (`/apps/new?project=…&environment=…`). The top
   bar's "New app" stays the page's one primary action. Drawn only when New app is offered.
6. **The description** beside the name: Caravana has none. The slot holds the project's name
   in mono (`cv-inline-mono`, fg-3) when it differs from the display name, else nothing.
7. **The copy marks** come from `AppStatus.Services` (wanted and healthy per service), which
   `listApps` already fills from Nomad: no per-app copies call on a list page.
8. **Backups as a workspace page** (`BackupsScreen`): waits. It is a page of its own with its own
   contract (restore points, tests, gaps), and the plan puts it in Phase 4; a bare list would be
   a third place for what the Backups tab and the needs-you list already say. Not in this pass.

## The page `/projects/{project}` (`?env=<environment>`)

`main` keeps the console's 36/64/28. The sidebar's Projects is current; the crumbs read
`<display name>` alone, as today. Live: the page opens the workspace stream (`/live`).

### Header (gap 18 to the tabs row)

- **Title row:** flex, baseline, gap 14: the h1 in **500 26px sans fg-1** (the handoff's; not
  the console's 24px mono app title: a project is a name the owner typed, not a Nomad name);
  the slot of ruling 6 in 400 15px; a spacer; then right-aligned sm buttons:
  - **Project settings:** secondary sm, `settings` icon, a link to `/projects/{project}/settings`;
  - **New app here:** ruling 5.
- **Tabs row:** `<nav aria-label="Environments">`, flex, align center, gap 8, wrap,
  border-bottom 1px line-1, padding-bottom 14. One badge per environment, in the project's
  order, each a **link** to `/projects/{project}?env=<name>`:
  - the badge (`EnvBadge`): inline-flex, 24px tall, padding 0 9px, radius 2, 500 12px mono,
    nowrap; **production** in fg-1, any other in fg-2; a 1px solid border: line-2, and for the
    current one line-3 with an ink-3 ground and `aria-current="page"`; hover: ink-2 ground;
  - then **"+ Environment"**: a secondary sm button with the plus icon, a link to
    `/projects/{project}/settings#add-environment`.
  - **Which environment:** `?env=` when it names one of the project's; else `production` when
    the project has it, else the first. A stale `?env=` (deleted meanwhile) falls back the same
    way, with no error: the tab row says where you are.
  - The row is drawn even with one environment: this is the page that grows a project.

### Main column: the environment's apps

- A `.cv-label` **"Apps · N"** (N the environment's app count, after any sort; "Apps · 0" with
  none), gap 10 to the panel.
- **The panel:** `.cv-panel` holding one row per app, sorted problems first as the Apps home
  (`sortProblemsFirst` on the row's kind). Each row is **one link** to the app (`<a>` with
  `.cv-row`): grid `minmax(0, 1.3fr) 150px 90px minmax(0, 1.3fr) 150px`, column-gap 16,
  padding 0 18px, height 48 (min-height, so a wrapped name grows it), align center, 14px fg-2;
  hover: ink-2 ground (`.cv-row`'s), the row keeps its colors.
  1. **Name:** `.cv-namecell` as the Apps home: the app's name in its environment, 500 14px mono
     fg-1, then the **kind word** (`kindOf`: compose, database, volume; none for an image app)
     in 400 12px mono fg-3, gap 10, baseline.
  2. **Status:** the quiet sm pill (`status-quiet`) with the console's words (`appStatusView`:
     healthy, rolling out, deploy failed, 1 of 2 healthy, not running, stopped, never deployed,
     not defined yet). The handoff's "running" is the console's "healthy": one set of words.
  3. **Copies:** one 7px mark per wanted copy, gap 4, from `AppStatus.Services`, service by
     service in the app's order: the healthy ones ● healthy first, then the rest with the app's
     kind: ◐ rolling while the app rolls out, ◆ warning when it is short of copies, ■ failed when
     nothing runs, ○ idle when it is stopped. With several services the groups are 8px apart and
     each group's `title` is its service's name. More than 12 marks: the text "N copies" in
     400 12px mono fg-3 instead. A never-deployed or undefined app draws "—" (fg-3). The cell has
     screen-reader words: "2 of 2 copies healthy" (or "none").
  4. **Address:** the app's first domain in 400 13px mono fg-2, ellipsized (`title` holds it);
     with more, " +2" after it in fg-3; with none, "no address" in fg-3 (the handoff's quiet
     `no domain`, in the console's word). Text, not a link: the row is the link.
  5. **Version and when:** right-aligned, 400 13px mono fg-3, nowrap: the live version (`v7`;
     while rolling out `v5 → v6` as the home's cell, the arrow aria-hidden with "rolling out
     v6" for a reader), then " · " and the newest deployment's relative time as a `<time>`
     (`whenOf`: title with the exact UTC time). Never deployed: "—".
- **Narrow:** under 720px of the panel's width (a container query, as `.cv-apps`) a row is two
  lines: name, kind and the pill on the first; marks, address and version on the second,
  the address ellipsized. Nothing scrolls sideways at 390px.
- **Empty:** the panel holds the `.cv-empty` block "No apps in `staging` yet." with one
  secondary sm button "New app here" (ruling 5's link; no button when New app isn't offered).

### Rail (the console's `.cv-split`, 400px; stacks under the column at its breakpoint)

Three sections, each a `.cv-label` and a panel, gap 10 inside, 28 between.

1. **Environment.** A `.cv-panel` with padding 18, a grid `auto minmax(0, 1fr)`, gap 12px 18px,
   baseline, 14px fg-2; the keys are the console's `.cv-label` (12px: the recorded deviation
   from the handoff's 11px). Rows, in order:
   - **Apps:** "3, all healthy" · "3, 1 needs you" (apps the needs-you list names, or whose
     status is a problem: `needsYouOf`) · "2, 1 rolling out" · "none yet". One clause after the
     count, the first that applies: needs you, rolling out, stopped ("1 stopped"), else "all
     healthy" (only when every app is healthy); otherwise the count alone.
   - **Deploys:** ruling 1.
   - **Addresses:** "2" with the domains' count, or "none".
   - **Promote to `<env>`:** ruling 3; its value is a flex row of secondary sm links, gap 8,
     wrapping.
2. **Needs you.** Only when the environment has items: the `attention-region` rows for the
   items whose project and environment are this one's (items with no app, such as a missing
   backup destination or a full disk, are the host's and stay on the home). With none the
   section is not drawn (the handoff).
3. **Deploys in `<env>`** (the environment's name in mono inside the label). The handoff's
   `DeployList`: the console's Activity rows (`activity-region`: name, the quiet pill "v7
   healthy", when; the who-words under) for the environment's newest 10 deployments, newest
   first, **without** the `Where` prefix (the label says where). The data is a store read by
   place, `ListRecentDeploymentsIn(project, environment, limit)`, not the global list cut down
   (an environment with old deploys would show none). Empty: "No deploys in `staging` yet." in
   14px fg-3, padding 18.

### Live regions

- The main column's label and panel are one region, `#project-apps`, reloaded from
  `GET /parts/projects/{project}/apps?env=<env>` on `sse:apps, sse:deployments, sse:copies,
  sse:sync` (the rows carry versions and copy marks, which deployments and copies move).
- The rail is one region, `#project-rail`, reloaded from `GET /parts/projects/{project}/rail?env=`
  on `sse:apps, sse:attention, sse:activity, sse:deployments, sse:sync`.
- The header and the tabs sit outside both. The parts answer 404 for an unknown project and
  fall back on the environment as the page does.

## `/projects/{project}/settings`

The old page's forms, in this order, each a section with a `.cv-label` heading as today:
Display name; Environments (each with its Delete and its reason, as today; the protected
production's note); **Add environment** (the section's id is `add-environment`, the tabs row's
"+ Environment" target); Delete project. Crumbs: `<display name>` (a link to the project page)
`/ Settings`; the sidebar's Projects is current; title "<display name> settings".
- The posts keep their routes. After a success: the display name → `/projects/{project}/settings?saved=1`
  (the calm notice there); Add environment → **`/projects/{project}?env=<new>`** (the new tab is
  current: the page says what was made); Delete environment → `/projects/{project}/settings`;
  Delete project → `/apps?projectDeleted=` as today. A refusal draws the settings page again
  with its status and the reason under the field, as the project page did.
- `?created=1` after New project still lands on the project page with its notice.

## The sidebar

- **Workspace:** Home, Projects, Logs, Monitoring, Schedules.
- **Configure:** Domains.
- Settings and the account row as today. Same labels, icons (`house`, `folder`, `scroll-text`,
  `activity`, `clock`, `globe`) and active rules; only the order and one new group heading
  change. The Menu panel under 900px draws the same.
- Still left out, as 021 ruled: Deploys, Previews, Variables & secrets, Backups (ruling 8), the
  organization switcher. The Developer/Operator toggle exists since 027.

## May differ (server-rendered, no JavaScript)

- The tabs are links with `?env=`; the row refreshes by region, not by state.
- Promote is per app (ruling 3) and leads to the existing preview; nothing is run on render.
- No description, no branch, no variables (rulings 1, 2, 6).
- The rail is 400px, the console's, not 420.

## Review

The designer checks at 127.0.0.1:7070, against the handoff's JSX (its render is blank):
- Default › production with whoami healthy: the row (pill, one ● mark, the address, `v0 · when`),
  the Environment panel ("1, all healthy", "From the console · …", "1"), Deploys in production;
- a project with staging and production: the tabs, `?env=` switching, the empty state, a
  never-deployed app's row ("never deployed", "—"), the Promote row once a copy exists;
- an app rolling out (◐ marks, `v1 → v2`) and a failed deploy (the pill, the Needs you section);
- the settings page: the forms, a refusal, the redirect targets;
- the sidebar's order in both the aside and the Menu panel; narrow (390px).

Then the impeccable critique (no detector), the designer's rulings, fixes, and a last look.

## Rulings on the build's departures and the live review (designer, 2026-11-03)

The build (Sonnet, reviewed by the Caravana session) was checked live on the lab at 127.0.0.1:7070
with Default › production (whoami healthy), a project with staging (a healthy 2-copy app, a
failed-before-register app, a never-deployed app) and production (promoted from staging), the
settings page, and the narrow layout (492px, the browser's floor; the row folds, nothing scrolls
sideways). Accepted or changed:

1. **The name beside the display name** is compared without regard to case, so `Default` shows
   no `default` beside it. Accepted.
2. **A project with no environment** (every one deleted) draws "This project has no environment
   yet." with a link to add one, and no rail. Accepted; the contract didn't cover it.
3. **The quiet sm pill** is written inline in the row (`cv-pill-quiet cv-pill-sm`, padding 0),
   since the shared template has no sm variant. Accepted.
4. **The rail's Needs you rows drop the place** before the app's name ("Shop Floor › staging ›"):
   the section's label already says where. The words stay the service's one wording; only its
   muted place parts are left out (`withoutPlace`). Changed in the review.
5. **Copies when Nomad can't be read** (state unknown): "—" with "unknown" for a reader, not idle
   rings, which would say "stopped". Changed in the review.
6. **The badge's hover** changes the ground only, never the text color. Changed in the review.
7. **Promote from production into staging** is offered like any other direction: promotion is
   per app and the app header offers the same; the handoff draws staging only because its sample
   has two environments.
8. **The needs-you section with nothing to draw while Nomad is unreachable** stays undrawn (the
   builder's question): the Environment panel and the table say what is known, and the home's
   notice covers the host-wide fact.

## Critique rulings (designer, 2026-11-03)

The impeccable critique (no detector) found twelve things; the rulings:

1. **The current tab wins on every channel.** The current badge is fg-1 on the ink-3 ground with
   the line-3 edge; every other badge, production included, is fg-2 on no ground. The handoff's
   "production in fg-1" loses to "this is selected": the two must not point different ways.
2. **A disabled button looks disabled.** One console-wide rule, `.cv-btn:disabled` (opacity
   0.45, `cursor: not-allowed`), as DESIGN.md's dim-text rule for disabled controls says. The
   settings page keeps its Delete buttons with their reason beside them (019's choice).
3. **Needs you leads the rail.** The rail's order is Needs you (only with items), Environment,
   Deploys in `<env>`: what needs a human is at the top (the Problems-First rule). The
   Environment panel loses its **Apps** row (it repeated the table's label and the needs-you
   list); Deploys, Addresses and the Promote rows stay.
4. **The 1452px breakpoint stays** (page 1's, console-wide): with ruling 3 the problem is the
   first thing after the table when the rail stacks.
5. **Touch targets:** on a coarse pointer the badges are 44px tall, as the nav items and crumbs
   are (the padding is the target).
6. **Narrow header and panel:** the two header buttons are one group that wraps together under
   the title; under 640px the Environment panel stacks each key over its value; a Deploys value
   keeps "· 2 minutes ago" on one line (nowrap on the time part).
7. **The marks stay beside the status.** Both marks are the handoff's (the pill's and the copies'),
   and there are no column heads (the handoff has none). In the narrow fold the copies' marks
   move to the first line, after the pill, so they are never a bullet before the address: line 1
   name, kind, pill, marks; line 2 address, version. The version cell keeps "—" for never deployed
   and the time alone for a deploy with no live version (the pill explains).
8. **An app's crumbs lead back to its environment.** The project crumb and the environment crumb
   of an app page both link to `/projects/{project}?env={environment}` (the environment crumb
   is a link now, still `Container`); the narrow back-link row's project link too.
9. **Settings page:** every section is capped at the same width (`cv-narrow`); the lede loses
   "Back to the project" (the crumb does it); the closed Delete project panel says "The project
   has to be empty first: no app is deleted for you." before the button. Kept: the field labels
   (a field needs its label), the console's settings h1, the app names as plain mono links.
10. **The header's voice:** declined (ruling 6 of the contract); the needs-you section now leads.
11. **Identical failed rows in Deploys in `<env>`:** declined for now; the row is the Activity
    row, shared with the home.
12. **Promote from production:** declined (review ruling 7).

## Last look (designer, 2026-11-03)

Checked again on the restaged instance at 1800 and 492px: the rail reads Needs you ("broken: This
deploy didn't start…", no place prefix), Environment (Deploys, Addresses, Promote to `production`),
Deploys in `staging`; the current tab is fg-1 on ink-3 with the line-3 edge and the others fg-2 on no
ground; nothing scrolls sideways; the settings page's sections share one width and its refused
Delete buttons are dimmed. Approved. The lab steps `console-projects` and `console-project-page`
pass; the critique's declined points (10 to 12) stay as recorded.
