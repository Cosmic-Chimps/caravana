# Contract: page 4, the Apps home and the deployment's stages

Written by the caravana_designer session on 2026-10-22. The values come from the handoff's
`DevHome.jsx` (`DevHome`, `ShippingCard`, `DeployList`), `NowScreen.jsx` (the apps table's kind
word), `DeployScreen.jsx` (the stages) and `theme/caravana.css`, rendered from the prototype
(`design/console/_shots/p4-home.png`, `p4-deploy.png`, `p4-project.png`). Where this list and
the handoff disagree, this list wins. Pages 1 to 3's contracts still apply. The console never
says "ship": the card is **Deploying now**.

## Scope

Page 4 changes the Apps home (`/apps`) and the deployment entry on the Deployments tab. It adds
no page: the handoff's `DeployScreen` is a page of its own, but its log, build tab and scheduler
details wait for the build server (Phase 3) and 024 Logs. Its **stages strip** is the part that
exists today, so it goes into the history entry.

## The Apps home (`/apps`)

The console's home keeps its shape (the headline sentence, the needs-you list, the apps grouped
by project and environment, the Activity rail) and gains four things from the handoff. Nothing
is removed.

- **Main column, in this order:** the headline and its lede; the needs-you list; **Deploying
  now** (only while a rollout is in progress); the **filter**; the groups of apps. The rail
  keeps Activity. The page's `main` keeps 36/64/28 (the handoff's DevHome is 40/64/36; the
  console's page head and gaps stay as DESIGN.md records them).

### Deploying now

The handoff's `ShippingCard`: one card per app with a rollout in progress, newest first, under
a `.cv-label` **"Deploying now"**; none when nothing is rolling out. (The handoff's "Just
shipped" twin, shown right after a rollout ends, is **not** drawn: the Activity rail and the
headline already say it.)

- A `.cv-panel` with padding 20, column flex, gap 16, and its border in **rolling-line**
  (`border-color: var(--color-rolling-line)`).
- **Head row:** flex, align center, gap 14, wrap: the app's name 500 18px mono fg-1 (a link to
  the app); its place `project / environment` 400 13px mono fg-3; the status pill md in
  rolling: **"v24 rolling out"**; a spacer; **View deployment** (secondary sm, link to the
  Deployments tab's entry `#deployment-<id>`). No Roll back button on the card: a rollout in
  progress can't be rolled back from here (Nomad reverts it itself on failure), and the entry
  offers Redeploy of the previous version once it ends.
- **Facts grid:** `grid-template-columns: auto minmax(0, 1fr)`, gap 10px 20px, align center,
  14px fg-2; the keys are the console's 12px `.cv-label`.
  - **Change**: what the deployment carries, in one line: for an origin "Pushed to `main` by
    ana · `4e0a77`" (branch and commit in mono; "by <actor>" only when known), for a console
    deploy "Deployed from the console", for the API "Deployed through the API", for a webhook
    "Deployed by the webhook", for a promotion "Promoted from staging", for a redeploy "v3
    deployed again". No "built in 41 s": there is no build.
  - **Copies**: the copies of the app's live and new versions as chips (the handoff's
    `AllocChip`: 26px tall, padding 0 9px, radius 2, ink-2 ground, 1px line-2 edge, 400 12px
    mono, a 6px mark then the app's name fg-1 then the copy's short ID fg-3; a starting copy
    has a dashed rolling-line edge and a ◐ mark; a draining copy a dashed line-2 edge, ○ mark,
    fg-3 and line-through), then the sentence "1 of 2 new copies healthy" 14px fg-3. The
    chips come from the live copies table (the Running tab's data), so with several services
    the service's name replaces the app's in the chip.
  - A failed-to-start rollout (no copies placed yet): one chip-less line "Waiting for Nomad to
    place the first copy." 14px fg-3.
- Live: the card's region refreshes on the `deployments` and `copies` notices like the table,
  and disappears when the rollout ends (the region draws nothing).

### The filter

A `<form method="get">` row between the needs-you list and the groups, flex, gap 10, wrap:

- a search field (the page 2 field, 32px, mono, width 280, `name="q"`, placeholder "Filter
  apps", `aria-label` "Filter apps by name, image or address"); matching is a case-insensitive
  substring over the app's name, its project and environment, its services' images and its
  domains;
- a state select (38px select, `name="state"`): All states (default), Needs you (failed,
  warning, unhealthy), Rolling out, Healthy, Stopped, Not deployed;
- a kind select (`name="kind"`): All kinds, Image, Compose, Database, Volume (the kinds below);
- an **Apply** button (secondary sm) and, when any filter is set, a "Clear" link to `/apps`.
- The groups keep their shape but hold only matching rows; a group with no matching apps is
  not drawn; with nothing matching, the `.cv-empty` block "No app matches." with the Clear
  link. The headline and the needs-you list are **not** filtered (they say the state of the
  whole caravana).
- Drawn only when the caravana has more than 5 apps; under that the row is left out (the
  007 review's open question 2, settled: a filter over four rows is noise).
- Live refresh keeps the filter: the region's refresh URL carries the query.

### A kind word per Apps row

The handoff's `NowScreen` apps table puts a 12px fg-3 word after the name when the app isn't a
plain image app. Rule, per app, the first that applies:
- **compose**: the app's source kind is Compose (the kept file, or `source.kind` compose);
- **database**: any service has a `postgres` backup engine, or its image repository is
  `postgres`, `mysql`, `mariadb`, `redis` or `valkey` (fixed list, in `view.go`);
- **volume**: any service has volumes (and none of the above);
- otherwise nothing (an image app shows no word, as the handoff).
Drawn after the name in the App cell: name 500 mono fg-1, then the word 400 12px mono fg-3,
gap 10, baseline-aligned. The kind select filters on the same rule.

### "by <who>" in the Activity rail

Each Activity row's second line reads **"<trigger> by <who> · <when>"** where who is known:
- an origin's actor ("Pushed to main by ana · `4e0a77`" as the handoff's DeployList: the
  branch and commit in mono);
- a promotion: "Promoted from staging";
- the console: "from the console" (one admin: the name adds nothing);
- the API: "through the API" and, **when the deployment records the token**, "through the API
  by `ci-github`" (the token's name in mono);
- the webhook: "by the webhook".
The token name is the one backend addition: `deployments.token_name` (migration, nullable),
set from the authenticated token on `deployApp`, `deployCompose`, `promoteApp` and
`redeployDeployment` (a session leaves it null), carried as `Deployment.tokenName` (API
0.23.0, optional). Worth doing: with deploy tokens per app (020) it says which CI deployed.
If it's cut, "through the API" alone stays correct.

## The deployment entry's stages

On the Deployments tab, each history entry gains the handoff's stages strip (`DeployScreen`'s
`Panel` of stages), under the entry's head line and above the progress sentence. Three stages,
not four: **Queued · Roll out · Live**. Build is drawn only when a deployment has a build
(never, until the build server), so the strip is a 3-column grid now and a 4-column one later.

- **The strip:** a `.cv-panel`, `grid-template-columns: repeat(3, minmax(0, 1fr))`; each cell
  padding 18px 20px, column flex, gap 10, border-left 1px line-1 (none on the first); the
  active cell has `box-shadow: inset 0 -2px 0 var(--color-rolling)`.
- **Cell:** the stage's name 500 15px sans with an 8px mark before it, gap 10: fg-1 when done
  or active, fg-3 when still to come; under it a 400 13px mono fg-3 line, "—" when nothing.
  Marks: done ● healthy, active ◐ rolling, to come ○ idle, failed ■.
- **What each stage says:**
  - **Queued**: done once Nomad accepted the job (the record has a job version): the time from
    `createdAt` to that ("2 s"); active while waiting ("waiting"); failed when the deploy
    failed before register (a missing image, a refused registry, validation): ■ with "didn't
    start" and the entry's error sentence stays where it is;
  - **Roll out**: active while running: "1 of 2 copies healthy" (from the Nomad deployment's
    healthy/desired, the progress line's numbers); done: the rollout's duration; failed: ■
    "failed" (the revert is said in the entry's sentence); to come: "—";
  - **Live**: done when the deployment ended healthy: the time it ended ("14:03:05", the
    console's time format); to come otherwise; a failed or reverted deployment leaves it ○ "—".
- **A superseded deployment** (one that never registered because a newer one won): Queued ○
  "superseded", the rest "—".
- The strip is drawn for every entry, closed or not: it is the entry's shape, not a live
  widget; the live region refresh updates it as it does the sentence.
- Narrow (under 660px): the three cells stack, each a row with the mark, the name and the
  value on one line.

### The entry's rollback wording (carried over from page 1)

- When Nomad reverted a deployment, the entry's sentence says "rolled back to v2 (v0 again)",
  and the app lede "v2 (v0 again) keeps running", where "v0" is the deployment whose stored
  definition and digests match the version Nomad went back to (the newest healthy deployment
  before the failed one; a match is by digests, with the definition as the tie-breaker). When
  no match is found, both stay as they read today ("v2").

## May differ (server-rendered, no JavaScript)

- The filter is a GET form with an Apply button, not live typing; no project tabs (the groups
  are the projects).
- No "Just deployed" card, no Roll back on the card, no "Stop rollout" (Nomad has no clean
  stop; it reverts on failure).
- The handoff's `DeployScreen` page, its Rollout/Build/App output tabs and scheduler details
  are not built; the stages strip lives in the entry.
- No Platform panel in the rail (025).
- Live updates refresh the card and the table as today; the strip refreshes with the entry.

## Review

The designer checks at 127.0.0.1:7070 against `p4-home.png` and `p4-deploy.png`:
- the home with a rollout in progress (the card, its chips, the headline), then after it ends
  (no card); a failed-before-register deploy (no card, the entry's Queued ■);
- the filter with 6+ apps: by name, by state Needs you, by kind compose and database, no match;
- the kind words on a compose app, a Postgres app, a volume app and an image app (none);
- the Activity rail with an origin deploy ("Pushed to main by ana"), a token deploy ("through
  the API by `ci-github`") if built, a console deploy, a promotion;
- the stages on: a healthy entry (all ●, times), a running one (◐ Roll out with "1 of 2"), a
  reverted one (■ Roll out, Live ○), a failed-before-register one (■ Queued), a superseded
  one; and the "(v0 again)" wording on the reverted app's lede and entry;
- narrow (390px) for the home and the entry.

## Rulings on the build's departures (designer, 2026-10-22)

All five accepted:

1. **Place on the card.** As the rail: drawn only outside Default › production. The home's groups already say where an app is.
2. **Stored `registered_at`.** It gives the Queued and Roll out spans and is not in the API. Deployments recorded before it show "—" for both.
3. **The "(v0 again)" match.** This rule replaces the one under "The entry's rollback wording": the repeated version is the newest earlier deployment that ended healthy with a recorded definition and pinned digests. When Nomad's revert message names a version that a healthy deployment has, that one wins. No match keeps today's wording.
4. **The reverted entry's wording.** The newest reverted entry says "rolled back to `v3` (`v1` again)", the live version, as the lede does. Older reverted entries keep the version in Nomad's message. The needs-you item uses the same wording as the lede.
5. **Strip edge cases.**
   - The strip sits after the head line, before the entry's sentence.
   - A running rollout with no progress yet reads Roll out "waiting".
   - A deployment superseded after it registered shows Queued ● and Roll out ○ "superseded".
   - Each mark and chip has screen-reader words.

**One set of trigger words everywhere** (the rail, the card, the history entry and the lede): "from the console", "through the API", "through the API by `ci-github`", "by the webhook", "promoted from staging", "deployed again".

## Review fixes (designer, 2026-10-22)

- The card's Change line says "Deployed through the API by `ci-github`" when the token is known.
- The entry's origin line is the rail's words: "Pushed to `main` by ana · `4e0a77c` · run".
- Live's time is "06:13:29 UTC", with the full date in `title`.

## Critique rulings (designer, 2026-10-22)

1. **One set of who-words.** One function serves the rail, the entry's meta line and the lede.
   - The words start lowercase everywhere except on the card and at the start of a sentence.
   - The lede: "Last deployed 26 minutes ago, promoted from `staging`." Likewise "…, pushed to `main` by ana.", "…, through the API by `ci-github`.", "…, from the console." and "…, by the webhook."
   - The rail and the entry meta use the same words, without "Last deployed".
2. **"Needs you" in the state filter** means the app has a needs-you item: the same list the headline counts (failed or reverted deploys, failed backups, crash loops, unhealthy copies, …).
3. **The card's chips carry the version.** The new version's copies come first.
   - Screen-reader words: "new, healthy", "new, starting", "serving, v5".
   - "Draining" is only for a copy that is stopping. Blue/green keeps old copies serving until promotion.
4. **The strip, re-ruled.** The full strip is drawn for:
   - a pending or running entry;
   - the newest entry;
   - any entry that failed or was reverted (its ■ cell is what explains it).

   Older healthy and superseded entries get a one-line strip in the meta's place ("● Queued under 1 s · ● Roll out 28 s · ● Live 06:28:21 UTC") and drop "took …" there.
5. **One wording for progress.**
   - The strip and the card say "N of M new copies healthy".
   - The running entry's progress line keeps the healthy count too, so all three say the same number: "2 of 2 new copies started, 1 healthy. Old copies keep serving until the new ones pass their checks."
6. Live in UTC: done with the review fixes.
7. **One 32px filter row.**
   - The 32px select, with the field's 13px mono, becomes the console's only select: Variables' Service, Domains' "Sends traffic to", Settings' Kind and Project. The 38px select goes.
   - Apply and Clear are 32px. DESIGN.md records it.
8. **The card's facts grid aligns to start.** "N of M new copies healthy" goes on its own line when the chips wrap.
9. **While a rollout runs,** the Apps row's Version cell reads `v5 → v6`, with the arrow and v6 in fg-3.
10. **The card's height shift** is accepted: the card inserts above the filter and moves it down by its height while a rollout runs.
11. **The reverted entry's raw Nomad line** is prefixed "Nomad said: …".
12. **The rail at 1440** stacks under the tables (page 1's 1452px breakpoint). No change for page 4; filed in CodeAlta.

## Last look (designer, 2026-10-22)

Approved with nothing to change. Pages 1 to 4 of 021 are approved.
