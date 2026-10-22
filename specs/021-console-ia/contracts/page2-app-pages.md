# Contract: page 2, the app's short Configure, and its Variables, Domains and Backups pages

Written by the caravana_designer session on 2026-10-22. The values come from the handoff's
`AppScreen.jsx` (the Configure tab), `VariablesScreen.jsx`, `DomainsScreen.jsx`,
`BackupsScreen.jsx`, `components/forms/Input.jsx`, `components/forms/Switch.jsx` and
`theme/caravana.css`, rendered from the prototype (renders in `design/console/_shots/p2-*.png`).
Where this list and the handoff disagree, this list wins: it records the console's decisions.
Page 1's contract (`page1-shell.md`) still applies to the shell and the app header.

## IA: five tabs stay, Variables and Domains are Configure's pages

- The app keeps its five tabs: Running, Deployments, Configure, Logs, Backups. Not seven: at
  390px the row already cuts "Backups", and the handoff's own app page has four.
- **Variables & secrets** is `GET /apps/{id}/variables` and **Domains** is `GET /apps/{id}/domains`.
  Both draw the app header with the **Configure** tab current (they are Configure's pages), the
  same crumbs as the app (`project / environment`), and no second back link: the Configure tab
  and the crumbs are the way back. Configure's Secrets panel links to the first ("Manage
  variables and secrets"), its Domains panel to the second ("Manage domains").
- **Backups** stays the tab it is, at `/apps/{id}/backups`, redrawn in the handoff's shape.
- The handoff's Variables, Domains and Backups screens are global, with project filters and
  environment columns. Per app, the filters go and the grid has one column. The cross-environment
  grid is Phase 4, as `docs/plan.md` says.

## Rules that hold on every page here

- **Every change is a deploy.** Constitution III: the definition is replaced, so each form's
  submit says so: "Save and deploy", "Set and deploy", "Remove and deploy", "Add domain and
  deploy". The handoff's "changes apply on the next ship" bar and its Discard button do not
  exist here: there is no pending state.
- **An app with a kept Compose file (015)** is edited through its file. Configure shows the
  Compose editor first and the per-service panels read-only, as today. The Variables and
  Domains pages of such an app are read-only tables with the sentence "This app is edited
  through its Compose file." and a link to Configure; setting and removing a **secret** still
  works there (`editSecrets` rewrites the overlay).
- **An undefined app (019.5)**: Configure shows "Define the app" as today; Variables and Domains
  show the title, "Define the app first." and a link to Configure; Backups shows nothing to back up.
- **A stopped app (020.5)**: every form on these pages says "The app is stopped: start it
  first." as today, and its submit is left out.
- **Secret values are never drawn**, not even their length: a secret's value cell is ten dots.
  There is no Reveal button (the handoff reveals; Caravana's values are write-only).
- **Fields** (`cv-field`): the handoff's `Input` md, so the console's fields change in one rule
  from 36px to **32px** tall (the sign-in's 42px stays): ink-2 ground, 1px line-2 edge (line-3
  and the focus ring when focused, failed-line on error), radius 4, inner padding 0 12px, text
  400 13px sans, or 400 12.5px mono when `mono`; an optional prefix or suffix 400 13px mono fg-3
  padded 12px on its side; the label above it is `.cv-label`, 7px gap; a hint or error under it
  13px/1.4 fg-3 (fg-4-free; failed for an error). A select is 38px, ink-2, line-2, radius 2,
  400 14px mono (the handoff's `mbSelect`).
- **Icons**: the page 1 mechanism (inline Lucide SVG). New ones: lock (13px, before a secret's
  name), external-link (open a domain), file-text (Edit as .env), history (Restore), play (Back
  up now), chevron-right and chevron-down (a `<details>` row's marker).
- No ghost buttons (the console has secondary and primary; the handoff's ghost actions become
  secondary). No drawers or modals: the handoff's "Add a domain" drawer and "Restore" panel are
  panels on the page, and confirmations are `<details>` as today.

## Configure (`/apps/{id}/configure`)

The handoff's Configure tab: a two-column grid of four panels. Per service, the first two; per
app, the last two. Then the app-level panels the console already has, which the handoff does
not draw, in one column.

- **Grid:** `grid-template-columns: repeat(2, minmax(0, 1fr))`, gap 16, `align-items: start`.
  Under 900px one column. Each panel is `.cv-panel` with padding 18 and a column flex: gap 16
  for the two form panels, gap 13 for Domains and Secrets. The panel's head is a `.cv-label`.
- **Several services:** the grid is drawn once per service, under a row with the service's
  name (`.cv-label` "web" as today, mono 500 14px fg-1 is also fine), Copies & size left and
  Shipping right; one `<form>` per service with a button row at its end: **Save and deploy**
  (primary sm). Domains and Secrets come once, after the last service, listing by service.
  With one service the service row is not drawn (the handoff).
- **Copies & size** (head "Copies & size"): a 4-column field grid, gap 13: **Copies** (mono,
  placeholder 1), **CPU** (mono, suffix "MHz", placeholder "default"), **Memory** (mono, suffix
  "MB", placeholder "default"), **Ceiling** (mono, suffix "MB", placeholder "default"; the memory
  ceiling of 013). No "Peak 7 d" hint: there is no history until 025.
  The handoff's inline "Needs a container registry" prerequisite is drawn when the image's host
  has no credentials in Settings › Registries **and** the last deploy failed with
  `registry_auth_failed` or a missing image: the `InlinePrereq` shape (warning mark, title
  500 14px fg-1, reason 14px/1.5 fg-2, dashed warn-line border, radius 4, padding 14px 16px)
  with one action, a link "Add credentials in Settings" (secondary sm). Otherwise nothing.
- **Shipping** (head "Shipping"): **Image** (mono, wide), **Container port** (mono, narrow),
  then **Health check**: one row of three fields, gap 13: the path (mono, prefix "GET",
  placeholder "TCP port", hint "Empty checks the TCP port."), **Every** (mono, suffix "s",
  placeholder 10), **Timeout** (mono, suffix "s", placeholder 2). Under the row one sentence,
  13px fg-3: "New copies must pass this check before they take traffic; if they don't, the
  deploy is reverted." The handoff's Auto-revert and Canary-first switches are **not drawn**:
  neither is a choice here (constitution III), and a switch that can't be flipped is a lie.
- **Advanced** (per service, only when any is set): a read-only panel after Shipping, head
  "Also set", rows `.cv-label` + value in 13px mono fg-2: Entrypoint, Command, Arguments,
  Volumes (mount → name), Backup (engine, schedule, keep). One line under it, 13px fg-3: "Edited
  through the API or a Compose file." This is where the handoff's "Advanced settings" goes.
- **Domains** (head "Domains"): one row per domain, flex, gap 13, align center: the host 400
  13px mono fg-1 (flex 1), then the service it goes to in 12px mono fg-3 ("→ web :80") when the
  app has several services. **No status pill before 023**: the console doesn't know the
  certificate's state yet, and "https" would be a claim. Then a button row: **Manage domains**
  (secondary sm, link to `/apps/{id}/domains`) and **Add domain** (secondary sm, plus icon, link
  to `/apps/{id}/domains#add`). With no domains: "No domains. Copies are reachable only inside
  the app." 13px fg-3, then the same buttons.
- **Secrets** (head "Secrets"): the names, 400 13px/1.8 mono fg-2, joined by " · "; with
  several services one line per service, the service's name first in fg-3. Under them 13px
  fg-3: "4 secrets in Nomad Variables. Values are never shown here." (the count; "No secrets."
  when none). Then **Manage variables and secrets** (secondary sm, link to `/apps/{id}/variables`).
  The set and remove forms leave Configure: they live on the Variables page.
- **After the grid**, one column, the panels the console has today, unchanged in content and
  in this order: Deploy from Compose (the button and sentence; the Compose editor itself comes
  first on the page for a kept-file app, as 015 rules), Webhook, Deploy token, Export, Delete
  app (danger button, the confirmation as today). Each is a `.cv-panel` with padding 18 and a
  `.cv-label` head. The handoff has none of these; they stay because the console needs them.
- **Environment** leaves Configure: it is a variable, so it is on the Variables page.

## Variables & secrets (`/apps/{id}/variables`)

The handoff's VariablesScreen, per app: one environment, so one value column, and no project
or service filter bar (with several services the table has a "Used by" column instead).

- **Page head**, under the tabs: a flex row, align flex-end, gap 16, wrap: a column (gap 10)
  with the title **"Variables & secrets"** in 500 18px sans fg-1 (not the handoff's 30px h1:
  the app header owns the h1) and the sentence 14px/1.5 fg-2 "One row per name. A change
  deploys the app."; a spacer; **Edit as .env** (secondary sm, file-text icon, link to
  `#env-editor`) and **Add variable** (primary sm, plus icon, link to `#add`).
- **Table** (`.cv-panel`): head 38px, ink-2, bottom 1px line-1, padding 0 18px, `.cv-label`
  cells: Name · Value · Used by (only with several services) · (empty, 44px). Columns
  `minmax(220px, 1fr) minmax(0, 1.15fr) 140px 44px` (without Used by: drop the 140px), gap 18,
  padding 0 18px, rows min-height 52, 1px line-1 dividers, hover ink-2.
  - **Name:** a 13px lock icon fg-3 before a secret's name (a 13px blank for a variable), the
    name 500 13px mono fg-1.
  - **Value:** a variable's value 400 13px mono fg-1, ellipsized, `title` with the whole; a
    secret: ten dots, 400 13px mono fg-3, letter-spacing 0.1em, no Reveal.
  - **Used by:** the service's name 400 12px/1.3 mono fg-3.
  - **Last cell:** for a secret, a **Remove** form (secondary sm) whose `<details>` confirmation
    says "Remove and deploy" as today; for a variable nothing (variables are edited as .env).
  - Sorted: secrets first, then variables, each by name.
  - Empty: "No variables or secrets yet." in the `.cv-empty` shape with the Add variable link.
- **Footnote** under the table, 13px fg-3: "Secret values live in Nomad Variables, readable only
  by this service's copies. Caravana never shows a secret again."
- **Add** (`id="add"`, a `.cv-panel`, padding 18, head "Add or change"): the console's set
  form, extended: **Service** (select, only with several services), **Name** (mono), **Value**
  (mono), a checkbox **"Keep as a secret (the value is never shown again)"**, and **Set and
  deploy** (primary sm). Unchecked it sets or replaces the variable in that service's env;
  checked it sets the secret as today. A name that is both a variable and a secret is a field
  error on Name ("already a secret"/"already a variable").
- **Edit as .env** (`id="env-editor"`, a `.cv-panel`, padding 18, head "Variables as .env",
  one per service with its name when several): today's Environment textarea exactly (ink-0,
  line-2, radius 4, 400 13px/24px mono fg-1, min-height 300, the hint "One NAME=value per line.
  Lines starting with # are ignored. This list replaces the current one."), with the handoff's
  first line as the placeholder: `# web · paste a .env file to add or update many at once`.
  **Save and deploy** (primary sm). Secrets are not in it (names only would mislead): the hint
  ends "Secrets are set above."
- Not drawn: the handoff's "shared" tags, "same as staging", "not set" warnings, the "Where
  secrets come from" rail and "Connect a source" (an external source is in the backlog), the
  reveal eye, the audit-log sentence, and the "not shipped" bar.

## Domains (`/apps/{id}/domains`)

The handoff's DomainsScreen per app, before 023: the columns the console can fill today.

- **Page head** as on Variables: title **"Domains"** 500 18px sans fg-1, sentence 14px/1.5 fg-2
  "Every address people use to reach this app. HTTPS is set up for each.", spacer, **Add domain**
  (primary sm, plus icon, link to `#add`). Under it a count line, 400 13px mono fg-3: "3
  domains" (no "all healthy" until 023 knows).
- **Table** (`.cv-panel`): head 38px ink-2 `.cv-label`: Domain · Goes to · (empty). Columns
  `minmax(0, 1.6fr) minmax(0, 1.2fr) 36px`, gap 18, padding 0 18px, rows min-height 52.
  - **Domain:** 500 14px mono fg-1, ellipsized.
  - **Goes to:** 400 13px/1.3 mono fg-2 "web" then ":80 · production" in fg-3 (the service, its
    container port, the environment).
  - **Last cell:** an icon link (external-link, 30×30, fg-3, hover ink-3 fg-1, `aria-label`
    "Open https://host", `target="_blank" rel="noopener"`).
  - **Remove:** a second line is not drawn; instead each row's last cell also holds a
    **Remove** form (secondary sm) with the `<details>` confirmation "Remove and deploy", so the
    last column is `auto`, not 36px. Sorted by host.
  - No Status, Certificate or Traffic columns, no chevron and no detail rows: all of that is
    023 (DNS, certificate, options as switches). Draw nothing rather than "—".
  - Empty: "No domains. Copies are reachable only inside the app." `.cv-empty` with Add domain.
- **Add a domain** (`id="add"`): the handoff's drawer as a `.cv-panel` on the page, padding 22,
  column gap 22, title "Add a domain" 500 18px sans fg-1:
  - **Domain** (mono input; validation as the definition's: a host name, lowercase, no scheme;
    the console's domain itself is refused, as `fix-console-domain` made it);
  - **Sends traffic to** (select, only with several services; the service's name and port:
    "web · :80");
  - **Point it at Caravana** (`.cv-label`), the sentence 14px/1.5 fg-2 "Add this record where
    you manage DNS for the domain:", then the record box: border line-2, radius 4, rows grid
    `80px minmax(0, 1fr)`, gap 12, padding 10px 14px, ink-0 ground, 1px line-1 between rows;
    key `.cv-label`, value 400 13px/1.4 mono fg-1, `word-break: break-all`. Rows: **Type** "A",
    **Name** "@" for an apex (the host has one dot or is the registered domain) or the first
    label otherwise, **Value** the host's public IP when the console knows it (the address
    `CARAVANA_PUBLIC_URL`'s host resolves to, resolved on the server with a 2 s timeout, cached
    for the process's life), else the words "this host's public IP address". No copy buttons
    (the clipboard needs script); the value is selectable. Under the box 13px fg-3: "The root of
    a domain needs an A record. A subdomain can use one too." (the edge CNAME is Phase 3).
  - No Check section (DNS, certificate, reachable): 023.
  - Footer row, gap 10, top 1px line-1, padding-top 14: a spacer, **Add domain and deploy**
    (primary sm). No Cancel (nothing to cancel on a page).
- A kept-file app: the table only, read-only, "This app is edited through its Compose file."

## Backups (`/apps/{id}/backups`)

The handoff's BackupsScreen per app, over what 010 and 016 built. Everything 016 shows stays
visible, re-homed into the handoff's rows.

- **Page head** as on Variables: title **"Backups"** 500 18px sans fg-1, sentence 14px/1.5
  fg-2 "Databases and volumes, and where their copies are." No "Back up something" button: what
  can be backed up is in the definition, and the gap rows below offer it. Under the head a line
  400 13px mono fg-3: "2 backed up · 1 not · stored in <bucket>" (the destination's bucket name;
  with no destination: "no destination").
- **No destination:** the problem banner, the handoff's Panel shape: `.cv-panel` with padding
  14px 18px, flex, gap 12: warning mark 8px, the sentence 14px fg-1 (flex 1) "No backup
  destination is set. Backups can't run until there is one.", and **Set the destination**
  (secondary sm, link to Settings › Backup destination).
- **Gap rows** (a `.cv-panel`, one row per service with something to back up and no policy):
  grid `14px minmax(0, 1fr) auto`, gap 14, align start, padding 16px 18px, 1px line-1 between
  rows: warning mark 9px (padding-top 4); a column gap 8: the sentence 15px/1.5 fg-1 with the
  name in mono, "`postgres` has no backup." or "`web · /data` has no backup.", then a meta line
  400 12px mono fg-3 "PostgreSQL · 0 copies kept" or "Volume · 210 MB"; the action **Back up
  daily** (primary sm). It posts to `POST /apps/{id}/backups/policy` with the service, which
  sets `backup: {engine, schedule: "0 3 * * *", keep: 7}` on that service through the stored
  app (`DeployIf` with the page's revision; a kept-file app rewrites its overlay as secrets do)
  and deploys: `volume` for a service with volumes, `postgres` for a service whose image's
  repository is `postgres` (or ends in `/postgres`). A service with neither gets no gap row. The
  handoff's "Safe to lose" needs a stored flag: Phase 4, not drawn.
- **Table** (`.cv-panel`), one `<details>` per backed-up service (native disclosure: the
  handoff's chevron rows, with no script). The `<summary>` is the row: head 38px ink-2
  `.cv-label` cells "What · How often · kept · Last backup · Size" over columns
  `14px 14px minmax(0, 1.3fr) minmax(0, 1.3fr) 150px 110px`, gap 18, align center, min-height
  60, padding 8px 18px, 1px line-1 dividers; an open row has an ink-2 ground and no divider.
  No Restore-test column: Caravana doesn't test restores (Phase 4).
  - cell 1: chevron-right, or chevron-down when open, 12px fg-3;
  - cell 2: a 9px mark: rolling while a run is going, failed when the newest run failed,
    healthy otherwise, idle when it never ran;
  - **What:** the service's name 500 14px mono fg-1, under it 12px mono fg-3 "PostgreSQL" or
    "Volume · /data, /cache" (the mounts);
  - **How often · kept:** the schedule in words 14px fg-1 ("Every day at 03:00", "Sundays at
    03:00", as `ScheduleText` writes it; a volume run adds "the service stops while it runs"
    13px fg-3 on a third line), under it 12px mono fg-3 "kept 7 days" (or "7 backups", as the
    policy counts);
  - **Last backup:** 12px/1.2 mono: when (fg-2; "running now" in rolling; "never" fg-3), under
    it "took 4 min 12 s" fg-3 (`durationText`); a volume run adds "stopped 8 s";
  - **Size:** right-aligned 12px/1.2 mono: the last run's size fg-2, under it "1.4 GB all" fg-3
    (the kept runs' sum).
  - The first row is `open` by default; the others closed. The detail (padding
    16px 18px 20px 82px, ink-0 ground, bottom 1px line-1, column gap 16):
    - **Restore points · last 30 days** (`.cv-label`, then the strip): a grid of 30 cells, gap
      3, height 40, align end, max-width 900: a day with a healthy run is a 26px bar in fg-4,
      failed in failed, running in rolling, no run a 4px fg-4-free gap (transparent); each bar
      with a run is a link to that run's row in the list below (`#backup-<id>`), with `title`
      "3 d ago · 12:00 · 1.9 GB" ("· failed"); under the strip "30 d ago" left and "today"
      right, 400 11px mono fg-4;
    - a sentence when the newest run failed, 13px fg-2: "The last backup failed 5 hours ago:
      <the run's error>." and nothing when it passed;
    - the volume's host path(s) for a volume engine, 12px mono fg-3 ("/var/lib/... on this
      host"), the 016 "On host" fact;
    - **Runs**: the last 10 runs, rows grid `14px 150px 110px 110px auto`, min-height 46: mark
      7px, when (12px mono fg-2), size, took, and **Restore…** (secondary sm) for a healthy run,
      opening today's `<details>` confirmation ("Type the service's name", "Restore and stop the
      service while it runs" for a volume engine), "Show older" link after 10;
    - the button row, gap 10, wrap: **Restore from the last backup** (primary sm, history icon;
      the newest healthy run's confirmation), **Back up now** (secondary sm, play icon; today's
      confirmation for a volume engine, direct for postgres). No Download, no Pick an exact time,
      no Change schedule: the first two don't exist, the third is edited through the definition.
- **Restores** (a section after the table, `.cv-label` "Restores", `.cv-panel`): today's list
  in the runs' row shape (mark, when, from which backup, took, status). Drawn only when there
  is one.
- Not drawn: the project filter bar, "Not backed up on purpose", restore tests, PITR bands,
  "Safe to lose", the More menu.

## Review

The designer checks each page live at 127.0.0.1:7070 against the prototype renders:
- Configure: whoami (one service), hollo (two), a kept-file Compose app, an undefined app, a
  stopped app, and the registry prerequisite (a deploy that failed on credentials);
- Variables: an app with variables and secrets in two services; the kept-file app read-only;
- Domains: whoami, hollo with a per-service domain, an app with none, the add form's apex and
  subdomain records;
- Backups: a Postgres service backed up (runs, a failed run), a volume service backed up, a
  volume service without a policy (gap row), no destination, and the restore confirmation;
- narrow (390px) for all four.
