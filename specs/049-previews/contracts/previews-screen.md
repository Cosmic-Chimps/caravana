# Contract: the Previews page (`PreviewsScreen`)

Written by the designer on 2026-11-22 (CodeAlta task `64ph`, follow-up of `rg2u` point 6). The values come from
the handoff's `PreviewsScreen.jsx` and `Shell.jsx` (`CV_NAV`) in `design/console/` (local, gitignored); the
renders are `design/console/_shots/pq-previews-open.png` and `pq-previews-setup.png`. The console's earlier
contracts still apply (021 pages 1–4, 027.5's project page). Where this list and the handoff disagree, this list
wins. The console never says "ship".

## Scope

- A new workspace page **`/previews`**, the handoff's `PreviewsScreen`: a header, two tabs (open previews, setup
  per project), the open previews of every project as one list, and each project's preview settings.
- A sidebar item **Previews** (icon `git-pull-request`), in Workspace after Projects (the handoff's order without
  its Deploys, which the console doesn't have).
- **The settings form moves here.** 049 put it on `/projects/{project}/settings`; the Previews page's setup tab
  becomes its one place. The project settings page keeps a one-line summary with a link (see "The project's
  settings page"). The post keeps its route.
- No API change: the page reads `Service.Projects` and `Service.Previews` per project, and the existing Close
  preview pages.

## Rulings on what Caravana doesn't have

1. **The pull request's title.** Caravana never learns it (the action sends the number, the ref, the actor, the
   commit and the run). The row's name is **`#142`** with, beside it, the **branch** when the newest deployment's
   origin names one. A pull request's own refs (`refs/pull/<n>/merge`, `refs/pull/<n>/head`) are not a branch: the
   `#142` already says them, so they are left out. caravana-action sends `GITHUB_REF`, which on a pull request is
   `refs/pull/<n>/merge`, so today the branch is usually absent; follow-up task: the action's preview mode sends the
   head branch.
2. **Sleep and Wake, "asleep", visits, "shares X with staging", which apps, starting data, variables per
   previews.** None exists. No Wake button, no asleep state, no visits, no shared-apps line; the setup tab has only
   what `PreviewSettings` holds. The count line leaves out "asleep ones use none".
3. **"won't start" and the Fix button ("Copy from staging").** The console's status words stand (`appStatusView`:
   deploy failed, rolling out, healthy, …), as 027.5 ruled ("one set of words"). The failure line says what the
   deployment record says, and its button is a link to the app's Deployments tab ("See the deploy"), never a fix
   that Caravana can't make.
4. **"building" / "Building image · 1m 10s of about 2m".** Caravana doesn't build: CI does. A preview whose app
   rolls out says "rolling out" and its line says which app and which version.
5. **"deleted 2 d after the PR closes".** Caravana closes a preview when the action says the pull request closed,
   or when it has had no deploy for the project's days. The line says the second, as a time: "Closes on
   `Nov 29 14:15 UTC` if nothing is deployed to it before." (`clockOf`, so localtime.js adds the local time).
6. **"More" (⋯).** No menu (no script needed for anything): its one action is **Close…**, a secondary sm link to
   the existing confirmation page.
7. **Memory.** "using 2.5 GB of memory" reads **"reserving 2.5 GB of memory"**: `PreviewApp.MemoryMB` is what
   the apps reserve, not what they use. Per row: "512 MB reserved".

## The page `/previews` (`?tab=open|setup`, `?project=` on setup)

`main` keeps the console's 36/64/28. Crumbs: **Previews** alone. Sidebar: Previews current. Title "Previews".
Live: the page opens the workspace stream (`/live`) on the open tab only.

### Header

- `.cv-sthead` with `.cv-sthead-text`: the h1 `.cv-headline` **"Previews"** and the `.cv-lede`
  "Every pull request gets its own copy of its apps, at its own address. Closed pull requests clean up after
  themselves." (the handoff's, with "PRs" written out and "the apps it changes" made true: the action copies the
  apps it is told to). No header button.
- A row (flex, align center, gap 14, wrap): the tabs as a `.cv-seg` `<nav aria-label="Previews">` of two links,
  **"open previews"** (`/previews`) and **"setup per project"** (`/previews?tab=setup`), the current one
  `cv-on` with `aria-current="true"` (`.cv-seg-item` is the handoff's `bar`: 30px, 500 13px mono, fg-3, current
  ink-3 and fg-1). On the open tab only, after it, the **count line** in 400 13px mono fg-3, nowrap (wraps under
  the tabs when narrow): **"3 open · reserving 2.5 GB of memory"**; one preview: "1 open · …"; none: the line is
  not drawn. GB with one decimal (`gbText`) from 1024 MB up, else "512 MB" (`sizeText`).
- An unknown `tab` is the open tab, with no error.

### The open tab

One `.cv-panel` holding a row per open preview of **every** project, in this order: problems first
(`sortProblemsFirst` on the row's kind: failed, then warning, then rolling), then the newest deployment first,
then project and number. The whole list is one live region (below).

**A row** (`.cv-pv`, border-top 1px line-1 between rows): a grid
`14px minmax(0, 1.6fr) 130px minmax(0, 1.3fr) minmax(0, 1fr) auto`, column-gap 18, align center, min-height 72,
padding 12px 18px; then, when there is one, the **line** (below). Not a link: it holds links.

1. **Mark:** the status mark at the handoff's 9px (`cv-mark cv-mark-lg`, the size the schedules list uses) in the
   row's kind, `aria-hidden` (the pill carries the word).
2. **What:** two lines, gap 7, `min-width: 0`.
   - Line 1, flex baseline gap 10: **`#142`** in 500 14px mono fg-2 (the handoff's fg-3 was a secondary number
     beside a title; with no title the number is the name, so it gets the next step up, never fg-4), then the
     branch (ruling 1) in 500 15px mono fg-1, ellipsized with its `title`. Without a branch, the environment name
     `pr-142` in its place.
   - Line 2 in 400 12px mono fg-3, one line, ellipsized (`title` holds it whole): the project's display name, then
     " · " and the who-words of the newest deployment of any app of the preview (`deployWho(rec, true)`, so "pushed
     by ana · 4e0a77"; without an origin "through the API by `blog-web-ci`", …), then " · " and `whenOf` the
     newest deployment's time as a `<time>`. The run address of the origin, when there is one, is a link **"run"**
     after it (as the history entry's "· run"), `rel="noopener"`. With no deployment: "no deploy yet". A ref that
     is a pull request's own (ruling 1) is left out of the words too ("pushed by ana", never "pushed to
     refs/pull/12/merge").
3. **State:** the quiet sm pill (as 027.5's rows: `cv-pill-quiet cv-pill-sm`) with the preview's word:
   - no app: idle ○ **"empty"**;
   - else the worst app by the problems-first order, with `appStatusView`'s word for it ("deploy failed",
     "rolling out", "1 of 2 healthy", "not running", "stopped", "never deployed") and its kind; every app
     healthy: healthy ● **"healthy"**.
4. **Apps:** a flex wrap gap 8 of chips, one per app (by ID, `Previews`' order): each a link to `/apps/{id}`,
   height 22, padding 0 8, radius 3, 1px line-2 border, ink-2 ground, 400 12px mono fg-1, the app's name; hover
   ink-3 ground. On a coarse pointer the chip's target is 44px (the padding grows, as the badges'). No app: "no
   apps yet" in 400 12px mono fg-3.
5. **Address and memory:** two lines, gap 7, 400 12px/1.2 mono, `min-width: 0`.
   - The first address of the first app that has one (`Addresses`, apps in order), ellipsized with its `title`,
     fg-1 when the row is healthy, else fg-2 (the handoff's fg-3 for a not-live address; fg-2 keeps it readable and
     still quieter than fg-1). More addresses: " +2" in fg-3. None: "no address" in fg-3.
   - The reserved memory of the preview's apps ("512 MB reserved", "1.2 GB reserved"; zero: "nothing reserved")
     in fg-3, then, when the project has a preview password, " · password" (the address asks for it).
6. **Actions:** flex gap 6, justify end, nowrap:
   - **Open**: ghost sm with the `external-link` icon, only when the row is healthy and has an address: a link to
     `https://<address>/`, `target="_blank" rel="noopener"`;
   - **Logs**: ghost sm with `scroll-text`, a link to `/logs?project=<project>&environment=<pr-n>`;
   - **Close…**: ghost sm (ruling 6), a link to `/projects/{project}/previews/{environment}/close?from=previews`.

**The line** (one, the first that applies), padding 0 18px 14px 50px, 13px/1.5 sans, `text-wrap: pretty`:
- a failed app: fg-1, `<app>`'s newest deployment `v5` failed: the record's error as the history entry draws it
  (the same helper; mono for the app and version), then a secondary sm link **"See the deploy"** to
  `/apps/{id}/deployments`;
- an app rolling out: fg-3, "Rolling out `web` `v3`" (the deployment's version label when it has one, else
  without), then " · started " and its `whenOf`;
- otherwise (healthy, empty, stopped, never deployed): fg-3, ruling 5's expiry sentence.

**Empty:** the panel holds `.cv-empty`: title **"No previews open."**, text "A pull request gets one when CI
deploys it with caravana-action's preview mode, and loses it when it closes.", one secondary sm button **"Set up
previews"** to `/previews?tab=setup`.

**Narrow** (under 900px of the page, the console's breakpoint): a row is three lines: mark, What and the pill on
the first; the chips and the address/memory block under What (aligned with it, the mark's column empty); the
actions last, starting under What. Nothing scrolls sideways at 390px.

### The setup tab (`?tab=setup&project=<id>`)

- Under the tabs row, gap 22, a second `.cv-seg` `<nav aria-label="Project">` with one link per project (the
  display name; `/previews?tab=setup&project=<id>`), in `Projects`' order; current `cv-on`, `aria-current`. Which
  project: `?project=` when it names one, else the first project with an open preview, else the first. A stale
  `?project=` falls back the same way.
- Then one `.cv-panel` of `.cv-setrow` rows (the handoff's `StField`: label and hint left, control right; the
  console's settings rows). The form is the 049 section's, posting to
  `/projects/{project}/previews/settings` with a hidden `from=previews`:
  1. **"Previews for pull requests"**, hint "CI opens a preview with caravana-action's preview mode. With 0
     here, it is refused." Control: the read-only state in 14px fg-2: "On · 2 open now" or "Off (at most 0)". (No
     switch: on is "at most" above 0, one field.)
  2. **"At most"** (`for` the input), hint "Another one is refused until a preview is closed. 0 turns previews off."
     Control: the number input (32px, mono, width 90, `inputmode="numeric"`, name `limit`), then "previews at once"
     in 14px fg-2, then " · about 1.2 GB if each reserves what open ones do" **only when** previews are open (their
     mean reserved memory times the limit); flex align center gap 10.
  3. **"Close after"**, hint "Counted from the preview's newest deploy, so a pull request that keeps getting pushes
     stays open." Control: the number input (width 90, name `expireAfterDays`), then "days without a deploy".
  4. **"Password"** (`for` the password input), hint, with a password set: "Every preview address asks for it, user
     `team`. A new one applies to every open preview at once."; without: "Previews are public. With a password,
     every preview address asks for it, user `team`." Control: the password input (width 320, `autocomplete=
     "new-password"`, never drawn with a value), its placeholder "Leave empty to keep it" when one is set, then
     (set only) the checkbox "Remove the password" under it.
  5. A last row with no label: the primary button **"Save preview settings"**.
  Field errors sit under their control (`field-error`), the field `aria-invalid`, as 049.
- After a save: 303 to `/previews?tab=setup&project=<id>&saved=1`, which draws the calm notice "Preview settings
  saved." above the panel (only while `saved=1`). A save with warnings is 200 with the page and the warning
  sentences in the notice (049's behaviour, here). A refusal is the setup tab drawn again with its status (422,
  409) and the reason at its field; never a redirect. An unknown project is 049's `projectGone`.
- No live region on this tab.

### Live region

The open tab's panel (with the count line) is one region `#previews-list`, reloaded from **`GET /parts/previews`**
on `sse:apps, sse:deployments, sse:copies, sse:sync`. The header and tabs sit outside it. The part answers the
region alone, the same reads as the page.

## The project's settings page

`/projects/{project}/settings`' Previews section becomes one `.cv-narrow` panel with a sentence in 14px fg-2 and a
link: "At most `5` open previews, closed after `7` days without a deploy, with a password." (or "public", or
"Previews are off: at most 0.") then a secondary sm link **"Change on the Previews page"** to
`/previews?tab=setup&project=<id>`. The environment rows keep "preview · expires" and Close preview (049).

## Close…

`/projects/{project}/previews/{environment}/close` takes an optional `from=previews` (query on the GET, a hidden
field on the POST): with it, Back and the crumbs lead to `/previews` (crumbs "Previews / Close pr-12"), and a
success redirects to `/previews?closed=pr-12&project=<id>`, which draws the calm notice "Closed `pr-12` of
<project>." above the list (only while those parameters are present). Without it, 049's behaviour stands.

## May differ (server-rendered, no JavaScript)

- The tabs and the project bar are links; the list refreshes by region, not by state.
- No Wake, no menu, no title, no sleep or sharing settings (rulings 1–6).
- The setup tab's "On" is a sentence, not a switch (the limit is the switch).

## Review

The designer checks on the lab instance against the renders:
- no preview at all (the empty state, the count line absent, the setup tab with the first project);
- a project with `staging` and two previews: one healthy with an address and a password set (Open, " · password"),
  one whose deploy failed (first, the pill "not running" (critique ruling 3), the line with the error and "See the deploy");
- a preview environment created with no app ("empty", "no apps yet", the expiry line);
- a deploy with an origin (actor, commit, run link) and one from the console;
- setup: a refused limit (25), a saved change (the notice), the password set and removed, the project settings
  summary and its link; Close… from the list and back;
- narrow (390px): the folded rows, nothing sideways; the sidebar's order in the aside and the Menu panel.

Then the impeccable critique (no detector), the designer's rulings, fixes, and a last look.

## Rulings on the build and the live review (designer, 2026-11-22)

1. **The who-words never carry the ref.** The branch is the row's name, so the meta reads "Shop · pushed by ana · 4e0a77 ·
   2 minutes ago · run", never "pushed to feature/checkout-v2". `previewWho` drops the ref from its copy of the origin
   always, not only for a pull request's own ref (which is also no branch, ruling 1 above).
2. **Without a branch, line 1 is the number alone.** `#14` is drawn as the name (500 15px mono fg-1, the branch's style),
   and the environment name is no longer shown; with a branch, `#12` stays in fg-2 14px, then the branch. The view's
   `Branch` is empty when the deploy names none.
3. **Times take the size of their text.** Inside `.cv-pvmeta` and `.cv-pvline` a `time` (the `time` template's
   `cv-data` and the `clock` template's `cv-clock`, with the local time `localtime.js` adds) inherits the surrounding
   12–13px size and keeps its mono face.
4. **The first setup hint says the 0 rule once.** "CI opens one for each pull request with caravana-action's preview
   mode. Set At most to 0 to turn previews off." The At most hint is "Another one is refused until a preview is closed."

Builder departures accepted:

- `service.PreviewApp` gained a `Summary service.AppSummary` field so the page words each app's status with
  `appStatusView`. It is Go only: the API's JSON and `openapi.yaml` are unchanged.
- A settings post without `from=previews` still redirects to the project's settings (`?saved=previews`) on success; a
  refusal always draws the Previews page's setup tab, since the form lives only there.
- A row folds by a container query on the list's panel at 900px of the panel, not by the viewport, so the six columns
  never sit cramped beside the sidebar.
- `.cv-btn-ghost` is new (no ground and no edge until hovered); the console had no ghost button.
- An expired preview's line reads "Expired <clock>; it closes at the next check." (the reaper hasn't looked yet).


## Critique rulings (designer, 2026-11-22)

The impeccable critique (no detector, 1600px and 500px, read-only) found ten things; the rulings:

1. **Columns line up from row to row.** Accepted. The actions column has a fixed width that holds Open, Logs and Close…,
   and a row without Open keeps Open's place empty, so the pill, the chips and the address sit at the same x in every row.
2. **The meta line keeps its run link and its time.** Accepted. The tracks are rebalanced so What gets the room the chips
   column didn't use (What `minmax(0, 2fr)`, chips `minmax(0, 0.8fr)`); the "run" link sits outside the ellipsized part
   and never shrinks; under the fold the meta wraps instead of cutting.
3. **The pill and the failure line.** Declined, with one correction to this contract. The pill is `appStatusView`'s word,
   the console's one set (027.5): an app whose first deploy failed is "not running", not "deploy failed" (the review list
   above said the latter in error). `v0` is the version Nomad registered and is the history's word for it. The error is
   the deployment record's, drawn as the Deployments history draws it; the console doesn't rewrite Nomad's text in one
   place only.
4. **The expiry sentence.** Accepted in part. The sentence's tail moves before the time, so the local time `localtime.js`
   appends ends it: "If nothing is deployed to it, it closes <clock>." (and "It expired <clock>; it closes at the next
   check."). The date beyond a week reads `2026-11-29` because that is the console's one clock (`clockOf`, 33 places);
   changing it is not this page's call (follow-up task).
5. **Getting to the preview.** Accepted in part. The address is the link to `https://<address>/` when the row is healthy
   (Open stays: the handoff's); on a coarse pointer Open, Logs and Close… are 44px targets. Close… stays a ghost: it
   leads to a confirmation page, not to a deletion.
6. **An empty preview is quiet.** Accepted. A preview with no app draws "no apps yet" and, in the address cell, only
   "nothing reserved"; "no address" is drawn only for a preview that has apps; " · password" is drawn only beside an
   address.
7. **Semantics.** Accepted in part. The rows are a `ul` of `li` (`aria-label="Open previews"`). No `aria-live` on the
   region: the console's regions refresh silently and the live indicator in the top bar speaks for the stream.
8. **Two marks per row.** Declined: the handoff's and this contract's (the gutter mark scans, the pill words it).
9. **A coloured edge on failed rows.** Declined: problems first and the mark do it, as on the project page and the home.
10. **The setup tab's project bar.** Accepted in part: a project with open previews reads "Shop · 3". The panel keeps the
    settings rows' full width, as Settings does.

## Last look (designer, 2026-11-22)

Checked again on the restaged instance at 1600 and 500px with pr-14 (deploy failed), pr-12 (healthy, a branch, a
password) and pr-15 (empty): the pill, the chips and the address start at the same x in every row (750, 898 and 1079
px), no meta line is cut and #12's ends "· run", the healthy address is a link, the empty preview reads "no apps yet"
and "nothing reserved", the expiry sentence ends with the time, the setup bar reads "Default" and "Shop · 3", and nothing
scrolls sideways at 500px. One fix made at the last look: folded, the meta is one wrapping line of text, so " · run"
follows the time instead of starting a line of its own. Approved.
