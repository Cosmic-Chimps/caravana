# Contract: 024 Logs, the `LogsScreen` (Developer mode)

Written by the caravana_designer session on 2026-10-24, before the spec. The values come from
the handoff's `LogsScreen.jsx` (`LOG_BASE`, the toolbar, the services rail, the histogram, the
stream rows, the deploy rows, the open row) and `theme/caravana.css`, rendered from the prototype
(`design/console/_shots/p9-logs.png`). Where this list and the handoff disagree, this list wins.
The 021 to 023 contracts still apply (shell, fields, selects, icons, page head, filters, the time
rule: UTC first, the viewer's local time beside it; the console never says "ship": the handoff's
"Ships show up in the stream" becomes "Deploys show up in the stream"). The Operator mode's
sources (scheduler, docker, caravana) are Phase 3 and not drawn.

## What 024 is (the calls the backend follows)

- **A read over Nomad's logs, nothing stored.** Every read goes to Nomad's log API as today
  (`getLogs`); Caravana keeps no log store. The time range, the histogram and the search are
  over what Nomad still has (its retention, the installer's `log_file`/alloc log settings), and
  the footer says so: "Kept as long as Nomad keeps them. Send them somewhere longer from Settings
  › Connections, later." (no connection exists yet; the sentence names the plan's later feature
  without a link). A ring buffer is **not** added unless the research shows the histogram across
  an environment can't be read live within 2 s for 20 copies; if it is, it holds only counts per
  minute and level (no lines), 24 h, in memory.
- **"Across an environment"** is the filter's scope: the page reads one project › environment at
  a time (the handoff's two segmented bars), then its apps and services. "All services" means
  every service of every app in that environment; the rail lists them grouped by app. A single
  caravana with only Default › production shows no project or environment bar (page 1's rule:
  a chooser with one choice isn't drawn).
- **Levels are read from the line**, in this order: a JSON line (the line starts with `{` and
  parses) takes its `level` / `lvl` / `severity` field (case-insensitive values `error`, `err`,
  `fatal`, `panic` → error; `warn`, `warning` → warn; `info` → info; `debug`, `trace` → debug)
  and its `msg` / `message` as the text (the raw JSON stays behind "Raw" in the open row);
  otherwise the first word or bracketed word of the line (`ERROR`, `[warn]`, `WARN:`, `level=info`,
  `E0124` glog-style) names the level; a line on stderr with no level word is `warn`; anything
  else is `info`. The level never comes from stdout vs stderr alone except that stderr fallback.
  The rule is one pure function with a table test (`levelOf`), and the API answers it per line.
- **ANSI sequences are stripped** (SGR colors, cursor moves, OSC): the page never renders color
  from a line (the console's own hues mean status). The rule is one pure function (`stripANSI`),
  applied before the level is read.
- **Repeated lines are folded**: consecutive lines from the same copy with the same text (after
  ANSI stripping, the timestamp a log library prefixes removed by the same `levelOf` pass) become
  one row with "×N" (the handoff's chip); the row's time is the newest. Folding is per page read,
  not stored.
- **Deploys are rows in the stream**: each deployment of an app in the environment whose
  `createdAt` (started) or end (`updatedAt` of a finished one) falls in the range is a deploy row
  at that time: "`supplier-gw` v24 started rolling out · pushed to `main` by ana · `4e0a77`" (the
  who-words of page 4) and "`supplier-gw` v24 is live · rollout finished" / "v24 failed its
  health checks · rolled back to v23". They are also the histogram's markers.
- **"Lines around this"** is a read of that copy's log around a line: the page `?copy=<id>&
  around=<time>` shows 50 lines before and 50 after the line's time, unfiltered (every level,
  that copy only), with the line highlighted (page 1's stable-id rule: `#line-<copy>-<n>`).
- **Download** delivers the current filter as a text file (`text/plain`, one line per row, the
  time in UTC RFC 3339, the service and copy, the level, the text; folded rows unfolded; deploy
  rows included as `# deploy …` comment lines), at most the range's lines Nomad gives, named
  `caravana-logs-<project>-<environment>-<from>-<to>.txt`.
- **Live follow** stays the console's: the stream is a live region refreshed on the `logs`
  notice while "Live" is on; without JavaScript the page is a snapshot with "Following needs
  JavaScript. Reload the page to see newer lines.", as today's tab says.

## Where it lives

- **`/logs`**, a workspace page: the sidebar's Workspace group gains **Logs** (icon
  `scroll-text`) after Domains. Crumb `Logs`. Active on `/logs*`.
- **The app's Logs tab** (`/apps/{id}/logs`) stays and becomes the same page filtered to the app
  (one template): its rail lists the app's services only, its head's sentence ends with "All
  logs" linking to `/logs?project=…&environment=…`, and its deploy rows are the app's. The tab's
  Copy and Stream selects go; copy is a rail entry (below) and stream is in the open row.
- **`/logs?copy=<id>&around=<time>`** is the "lines around this" view, the same page with a
  notice line "Around 14:03:39 UTC in copy `9a4d` of `supplier-gw`. Every level, this copy only."
  and a "Back to the stream" link.

## The page (`/logs`)

- **Page head** (page 3's shape, `main` padding 30px 26px 48px, gap 18, the handoff's tighter
  values): "Logs" 30px, "Output from every copy of every service, newest first. Deploys show up
  in the stream." 15px fg-2; at the right the **range** segmented bar (the handoff's `seg`:
  2px padding, radius 4, ink-1 ground, 1px line-1; items 30px, 0 12px, radius 3, 500 13px mono,
  active ink-3 fg-1, else fg-3; a `<form method="get">` of links carrying the other filters):
  **15 min · 1 h · 24 h · 7 d** (default 15 min), then **Live** / **Paused** (a toggle button,
  page 1's `aria-pressed` shape: primary when live with the `pause` icon, secondary when paused
  with `play`; a plain post without script that flips `?live=0|1`).
- **Filter row** (flex, gap 10, wrap): the **project** bar and the **environment** bar (the
  handoff's `seg`, only when more than one choice each); a 1px line-2 divider 22px tall; the
  **search field** (flex 1, min-width 280, 36px, ink-1 ground, 1px line-2, radius 4, a 14px
  `search` icon fg-3, the input 400 14px mono fg-1, placeholder "Search text, a request id, a
  status code…"; when set, "N lines" 12px mono fg-3 at its right end); a hidden submit so Enter
  applies; then the **level toggles**: four 30px chips, 0 10px, radius 4, 500 12px mono, gap 6:
  on = ink-2 ground, 1px line-3, the level's hue (error failed, warn warn, info fg-2, debug fg-3);
  off = transparent, 1px line-1, fg-4; each with its count in fg-3 for error and warn ("error 2",
  "warn 8"); defaults error, warn, info on, debug off; each chip is a link that flips its level in
  the query. Everything is one GET form: `?project&environment&app&service&copy&q&range&levels&live`.
- **Two columns**: grid `220px minmax(0, 1fr)`, gap 20, align start.
  - **Services rail** (`<nav aria-label="Services">`, sticky top 72): `.cv-label` "Services"
    (padding 0 10px 8px), then items 32px, 0 10px, radius 4, gap 10, 400 13px mono: "all
    services" first (an 8px ring, 1px line-3), then per app a group head (the app's name as a
    `.cv-label` row when the environment has more than one app) and its services, each with an
    8px square of the service's hue (below) and its copy count 11px fg-4 at the right; the
    chosen one on ink-2 fg-1, others fg-2. Each item is a link carrying the filters. Under a
    chosen service its copies are listed indented (the copy's short ID, 12px mono), the chosen
    copy on ink-2.
  - **Service hue**: the handoff's six-hue palette (`oklch(0.78 0.08 250)`, `(0.78 0.09 300)`,
    `(0.80 0.07 200)`, `(0.80 0.08 150)`, `(0.80 0.08 80)`, `(0.78 0.08 20)`) picked by a stable
    hash of `<app>/<service>`; it colors the service's name in the rail and the rows. This is
    the one place the console colors by identity, not status: the hues are low-chroma and never
    the status hues' exact values; record the exception in DESIGN.md.
  - **Histogram** (`.cv-panel`, padding 14px 16px 10px): 30 bars of equal time (the range / 30),
    flex 1 each, gap 3, height 48, align end: a bar's height = count / the busiest bar × 44
    (min 2px when non-zero), ink-3; error lines stacked on top in failed (their share of the
    bar); deploy markers as 1px dashed rolling vertical lines at their time, `title` "v24 rolling
    out" / "v24 live"; under it a 11px mono fg-4 row: the range's start time ("13:49 UTC"), the
    deploy's name under its marker in rolling ("┆ supplier-gw v24"), "now" at the right. With
    no lines: the panel shows the empty bars (all 2px line-2 stubs) and "no lines in this range".
    The histogram is drawn from the same read as the stream (the page reads the range once).
  - **Stream** (ink-0 ground, 1px line-1, radius 4):
    - a head line when live: 8px 16px, 400 12px mono rolling, ◐ 7px, "streaming from 8 copies";
      when paused: "paused · N new lines since" in fg-3 with a "Show" link (the lines arrive on
      Live);
    - each line a `<details>`-free row (grid `104px 48px 190px minmax(0, 1fr) auto`, gap 12,
      align baseline, padding 5px 16px, 400 13px/20px mono): the time 12px fg-4 as `<time>`
      "14:03:41.208" (UTC; the script appends nothing on rows, the head carries the zone once:
      "times in UTC" in the stream head); the level 12px in its hue; the service in its hue then
      the copy's short ID 12px fg-4; the text fg-1 (debug fg-3) on one line, ellipsized; the
      "×N" chip (500 11px mono fg-3, 3px 6px, radius 3, 1px line-2) when folded; an error row
      has a 7% failed tint, a fresh row (arrived live) a 5% fg-1 tint that fades in 600 ms (the
      One Tick Rule); the row is a link to `?…&open=<copy>-<n>#line-…` that opens it;
    - the **open row**: the text wraps (`pre-wrap`), and under it a strip (8px 16px 10px 180px,
      ink-1, 1px line-1 top and bottom, flex, gap 8): **Lines around this** (secondary sm,
      `list` icon, link), **Only `supplier-gw`** (secondary sm, `filter` icon, link that sets the
      service filter), **Copy link** stays hidden without script (the row's URL is the link; a
      `select-all` 12px mono of it is drawn for the no-script case), **Raw** for a JSON line (a
      `<details>` with the line as received, `pre`); and the error row's note when known: "First
      seen 09:12 UTC today, 3 times since." (the count of equal folded texts in the range) in 13px
      fg-3;
    - a **deploy row**: flex, gap 12, 8px 16px, a rolling 8% tint, 1px rolling-line top and
      bottom, 500 13px/1.4 mono rolling: the time 96px fg-3 400, the `rocket` icon 13px, the
      sentence (above); it is a link to the deployment's entry;
    - "Older lines were dropped." as today when the read hit Nomad's tail, and "Show older"
      (a link to `?…&before=<time>`) when more exists;
    - empty: 24px padding, 14px fg-3: "No lines match. Try a wider time range or turn on more
      levels." / with no filter: "No output yet. New lines appear here."
  - **Footer** 13px fg-3: the retention sentence above, and **Download** (secondary sm,
    `download` icon, a link to `/logs/download?<the same query>`).
- **Errors**: Nomad unreachable: the stream shows the console's notice "Nomad can't be reached
  right now, so logs can't be read." (page 1's idle mark) and the histogram its empty bars; a
  copy that is gone: its lines stay, its rail entry reads "(gone)".

## Narrow (under 900px, checked at 390px)

- The head stacks (title, sentence, then the range bar and Live on one line).
- The filter row wraps: bars, then the search field full width, then the level chips.
- The rail becomes a horizontal scrolling row of chips above the stream (the Settings nav's
  shape from page 3), the chosen one first; the copies' sub-list is a second row when a service
  is chosen.
- The histogram keeps its height; the labels keep their ends.
- A row folds to two lines: time · level · service copy, then the text (wrapping, no ellipsis);
  the "×N" chip after the text; the open strip's buttons wrap.

## May differ (server-rendered, no JavaScript)

- The filters are a GET form applied by Enter or Apply; the segmented bars and chips are links.
- Live follow needs JavaScript; without it the page is a snapshot with the sentence.
- The fresh-row tint and the paused "N new lines" count need JavaScript; without it, nothing.
- Copy link is the row's URL in selectable text.
- The histogram's hover values are `title`s.

## API (names only, for the spec)

`getLogs` grows (0.26.0): `GET /v1/logs?project&environment&app&service&copy&q&from&to&levels&
before&around&lines` answering `LogLine{time, app, service, copy, stream, level, text, raw?,
repeat}` newest first, plus `deploys[]` rows for the range and `histogram{from, to, buckets[]{
from, count, errors}}` and `copies` (the rail's counts); the app's `GET /v1/apps/{id}/logs` keeps
its shape and gains `level` and `repeat` per line; `downloadLogs` (`GET /v1/logs/download`,
`text/plain`); `listLogDeploys` is not separate (the rows come with the read). Times RFC 3339
UTC. `levelOf` and `stripANSI` are documented in the API's description so clients can match
the console.

## Review

The designer checks at 127.0.0.1:7070 against `p9-logs.png`:
- `/logs` with two apps of several services, error and warn lines (a copy that prints
  `ERROR …`, `[warn] …`, a JSON line with `"level":"error"`, ANSI-colored lines), a folded
  repeat, a deploy row and its marker (deploy an app during the review), the counts on the
  chips, the rail with copy counts and hues;
- each range; search for a word and a status code; levels off and on; "Only <service>", a
  chosen copy; "Lines around this" with the highlighted line and the notice; the open row's
  Raw on a JSON line; the error note;
- Live on (a line arriving with its tint) and Paused with "N new lines"; the no-script snapshot;
- Download with the filter; "Show older"; Nomad unreachable (stop the lab's Nomad briefly, or
  the test's fake); a gone copy;
- the app's Logs tab as the same page filtered, with "All logs";
- narrow at 390px.
