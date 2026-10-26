# Contract: 022 Schedules, the `SchedulesScreen`

Written by the caravana_designer session on 2026-10-23, before the spec; its time rule amended
the same day to the maintainer's ruling (UTC first, the viewer's local time beside it). The values come from
the handoff's `SchedulesScreen.jsx` (`SC_JOBS`, `SC_PRESETS`, `ScRunStrip`, `NewSchedulePanel`,
the row and its detail) and `theme/caravana.css`, rendered from the prototype
(`design/console/_shots/p5-schedules.png`). Where this list and the handoff disagree, this
list wins. The 021 contracts still apply: the shell (page 1), fields, selects and icons (pages
2 and 4), the Settings-style page head (page 3), the trigger words and the who-words (page 4).
The console never says "ship".

## What a schedule is (the calls the backend follows)

- **A schedule belongs to an app and one of its services.** It runs one command in a fresh copy
  made from that service's live version: the same image digest, variables, secrets, volumes
  not mounted, no ports and no traffic. There is no workspace-wide schedule: without an app
  there is no image to run in. The handoff's "Run inside" select is the app (and the service
  when the app has several), and its sentence "Uses the live version's image and variables.
  Doesn't take traffic." stays verbatim.
- **It is a Nomad periodic batch job** `<app>_<service>_<schedule>`, the shape the backups use,
  `ProhibitOverlap` from the skip setting, `TimeZone` UTC (the time rule below), and a
  `kill_timeout` plus a run limit for "stop after".
- **A run** is one Nomad dispatch: it starts, ends with an exit code, is stopped at the limit,
  is skipped (the previous run was still going), or couldn't start (no live version, the app
  stopped, Nomad refused). Caravana records every run (`schedule_runs`: started, ended, outcome,
  exit code, the limit hit, bytes of output kept) and keeps the last run's output per schedule
  (stdout and stderr, cut at 1 MiB, the oldest lines dropped) so "Logs of last run" works after
  Nomad garbage-collects the allocation. Output is never put in a notification, a needs-you
  item or a list row.
- **Backups are built-in schedules.** Every service with a backup policy is a row in the same
  list, named `<service> backup`, with the engine as its command words ("pg_dump → `backups`",
  "volumes → `backups`"), no Pause, no Edit, no Delete (the sentence "Made by Backups. Change
  the time or how many are kept in the app's Backups tab." and a link). Their runs are the
  backups table's runs.
- **A failed run** (exit code ≠ 0, stopped at the limit, couldn't start) is a needs-you item
  (`AttentionKind` `schedule_failed`, one per schedule, on the newest run) and so a
  `problem.opened` notification, with the same sentence as the row's detail. A skipped run is
  not a failure; three skips in a row are a `warning` item ("`rates-sync` has been skipped 3
  times: each run takes longer than the time between runs.").
- **Time, by the maintainer's ruling (2026-10-23): UTC and the viewer's local time.** There is no
  installation timezone and no `CARAVANA_TIMEZONE`. Schedules are written, stored and run in UTC
  (Nomad's `TimeZone` "UTC"), as backups are; nothing moves and nothing migrates. Every clock
  time the console draws (a schedule's Next, a run's start and end, the Check preview's next
  three runs, a backup's time, the stages' Live time from page 4) is a `<time datetime="…Z">`
  whose text reads the UTC time first, named: "14:15 UTC". A small vendored script
  (`tools/ui-vendor.sh`'s list, no fetch at run time) adds the viewer's local time after it in
  fg-3: "14:15 UTC · 16:15 local" (the browser's zone; the word "local", not the zone's name).
  Without JavaScript the UTC time stands alone and is right. Relative words ("tomorrow",
  "in 2 d", "yesterday") are computed in UTC on the server, as today.

## Where it lives

- **`/schedules`**, a workspace page: the sidebar's Workspace group gains **Schedules** (icon
  `clock`) as its third item, after Projects, now that the page exists. Crumb: `Schedules`.
  Active on `/schedules*`.
- **Per app:** no new tab (the row already names the app). The app's **Configure** page gains
  a panel "Schedules" after Variables & secrets in the two-column grid: the app's schedules as
  "`rates-sync` · every 15 minutes" lines (12px mono fg-3 after the name), "No schedules."
  when none, and **Manage schedules** (secondary sm, link to `/schedules?app=<id>`) and **New
  schedule** (secondary sm, plus icon, link to `/schedules/new?app=<id>`).
- **`/schedules/new`** and **`/schedules/{id}/edit`** are pages, not the handoff's drawer.
- **`/schedules/{id}/runs/{runId}`** is the run's page (the logs).

## The list (`/schedules`)

- **Page head** (page 3's shape): title "Schedules" 30px, the sentence "Commands that run on a
  timer in a fresh copy of an app, then stop. Times are UTC; your local time is shown beside
  them." 15px fg-2 (the second sentence only when the script is present: the server draws
  "Times are UTC." and the script appends the rest), and **New schedule** (primary sm, plus icon). Under it a filter row (page 4's shape, one 32px line):
  a project select (only when more than one project), an app select (`name="app"`, "All
  apps"), Apply and Clear; then the count line 400 13px mono fg-3: "7 schedules · 1 failing ·
  next run 14:15" ("none failing"; "next run" is the earliest Next among unpaused rows, "no
  runs scheduled" when all are paused or none exist).
- **Table** (`.cv-panel`): head 38px ink-2 `.cv-label` "Schedule · Runs · Next · Last run ·
  Last 14 runs" over columns `14px 14px minmax(0, 1.3fr) minmax(0, 1.2fr) 150px 160px 200px`
  (no 36px menu column: the handoff's "…" becomes the detail's buttons), gap 18, align center,
  min-height 62, padding 8px 18px, 1px line-1 between rows. Each row is a native `<details>`
  (page 2's backups rows): the `<summary>` is the row, an open row has an ink-2 ground.
  - cell 1: chevron-right / chevron-down 12px fg-3;
  - cell 2: a 9px mark: ◐ rolling while a run is going, ○ idle when paused, else the last run's
    mark (● healthy, ■ failed, ◆ warning for a skipped run, ○ for never run);
  - **Schedule:** the name 500 14px mono fg-1, then a quiet sm pill when there is something to
    say: "running now" (rolling), "paused" (idle), "last run failed" (failed); under it 12px
    mono fg-3 ellipsized "`booking-api` · `node scripts/sync-rates.js`" (app, service when the
    app has several, the command);
  - **Runs:** the schedule in words 14px fg-1 ("Every 15 minutes", "Every day at 03:00",
    "Weekdays at 09:00", "Sundays at 03:00", "First of the month at 06:00"; page 2's
    `ScheduleText` grows to these, "On `<cron>`" only as the last resort), paused in fg-3; under
    it the cron line 400 12px mono fg-4;
  - **Next:** 400 13px/1.3 mono: "14:15 UTC" today, "tomorrow 02:00 UTC", "in 2 d · Sun
    03:00 UTC" (the console's relative words) in fg-2, then the script's "· 16:15 local" in
    fg-3 on the same line (wrapping to a second line under 900px); "paused" fg-4; "—" for a
    built-in whose app is stopped;
  - **Last run:** two 12px/1.2 mono lines: running: "started 14:00:02 UTC" in rolling over a 120×4
    bar (ink-3 track, rolling fill, width = elapsed / usual duration, capped at 100%; no usual
    duration yet: no bar); otherwise the when (failed hue for a failed run, fg-2 otherwise;
    "never" fg-3) over "took 5 min 0 s" fg-3 (`durationText`); a stopped-at-limit run reads
    "stopped at 5 min";
  - **Last 14 runs:** the strip, 7px marks with 4px gaps, oldest first (● ok, ■ failed, ◆
    skipped, ○ stopped by the owner), `aria-label` "Last runs, oldest first", each mark a link
    to that run's page with its `title` ("yesterday 14:00 · failed · 5 min 0 s"); empty cells
    for fewer than 14 runs are not drawn.
  - Sorted: failed first, then running, then the rest by next run, paused last. The project
    and app filters apply; the count line reflects the filter.
  - Empty: `.cv-empty` "No schedules yet. A schedule runs a command in a fresh copy of an app
    on a timer, then stops." with New schedule.
- **Footer** under the table, 13px fg-3, two sentences 18px apart: "Last 14 runs read oldest
  to newest." and "Failed runs are sent to your channels. Change them in Settings › Notifications."
  (only when a channel exists; the handoff's "#vee-dev" names a channel we don't have).

### The row's detail

The handoff's open row: a grid `minmax(0, 1.2fr) minmax(0, 1fr)`, gap 28, padding
16px 18px 20px 82px, ink-0 ground, bottom 1px line-1.

- **Left column** (gap 14):
  - the state sentence 14px/1.5 fg-1, `text-wrap: pretty`, one of: a failed run's cause with
    its count: "Stopped at the 5 min limit. 3 of the last 4 runs failed the same way." /
    "Exited with code 1. 3 of the last 4 runs failed the same way." / "Couldn't start: the app
    has no live version." (the plain cause from a fixed table: the limit, an exit code, no live
    version, the app is stopped, Nomad refused, the image can't be pulled); a running run:
    "Running for 1 min 20 s. Usually takes 48 s."; a paused schedule in fg-2: "Paused 3 days
    ago." (one admin: no "by lea", no quote); a skip note in fg-2: "Skipped once, 9 weeks ago,
    because the run before was still going."; a built-in: the "Made by Backups…" sentence;
  - **How long each run took** (`.cv-label`): the bars, flex, align end, gap 4, height 56,
    one per run of the last 14, flex 1 with max-width 22, radius 1: height = duration / the
    longest of the 14 × 56 (min 4px), fg-3 for ok, failed for a failed run, warn for a
    stopped-at-limit run, a 2px line-2 stub for a skipped run; `title` per bar ("yesterday
    14:00 · 3 min 40 s"); under it 400 12px mono fg-3 "usually 48 s · stops at 5 min" (the
    median of the ok runs; "no runs yet" when none).
- **Right column** (gap 12): the facts grid `130px minmax(0, 1fr)`, gap 8px 14px, 400
  13px/1.4 mono fg-2, keys fg-3: **command** (`word-break: break-all`), **runs in** ("`web` of
  `booking-api`"), **stops after** ("5 min"), **if still running** ("skip the next run" /
  "start another"), **memory** ("512 MB"), **project** ("booking / production"); then the
  button row, gap 8, wrap, padding-top 4:
  - running: **Stop this run** (secondary sm, `square` icon; a `<details>` confirmation "The
    run is stopped and counted as stopped by you.", danger **Stop the run**);
  - otherwise **Run now** (primary sm when the last run failed, secondary otherwise; `play`
    icon; a plain post; refused with a sentence when a run is going or the app is stopped);
  - **Logs of last run** (secondary sm, `scroll-text` icon, a link to the last run's page;
    left out when there is no run);
  - **Pause** / **Resume** (secondary sm, `pause` / `play` icons, plain posts; not on a
    built-in);
  - **Edit** (secondary sm, `pencil`, link to `/schedules/{id}/edit`; not on a built-in; a
    built-in shows **Change in Backups** instead, a link to the app's Backups tab);
  - **Delete…** (secondary sm; a `<details>` confirmation "Its runs' history goes with it.",
    danger **Delete schedule**; not on a built-in).
- Live: the list is one live region refreshed on a `schedules` notice (a run starts, ends or
  is skipped), as the home's table; an open `<details>` keeps its state across a refresh by
  `id` (page 1's stable-ids rule).

## New schedule (`/schedules/new`) and Edit (`/schedules/{id}/edit`)

A page with page 3's setting rows (label and hint on a 220px column, the control beside),
titled "New schedule" / "Edit `rates-sync`" (30px) with the sentence "Runs a command in a fresh
copy of an app, then stops it." One `<form method="post">`, `max-width 1180`.

- **Name** (mono, `app.ValidName`'s alphabet, at most 31 characters; default from the command's
  last path segment without its extension, as the handoff does, filled server-side on a
  refused submit only). Unique per app.
- **Run inside**: the app select (defined apps, stopped ones listed but disabled with
  "(stopped)"), and a service select drawn only when the picked app has several (without
  script: the service select lists every service of every app as "`app` › `service`"; the
  page re-draws with the app's services after a refused submit or a "Check" submit). The hint
  "Uses the live version's image and variables. Doesn't take traffic." Prefilled from `?app=`.
- **Command** (mono textarea, 2 rows; page 020.5's `splitCommand` rules: quotes and
  backslashes, no pipes or variables, "use `sh -c '…'`"; the hint says so).
- **When**: a text field (sans) for the words, in UTC (its hint: "Times of day are UTC."; the
  script appends what the typed time is in the viewer's local time, "03:00 UTC is 05:00 local"),
  with the four presets under it as links that
  fill the field (`?preset=`, drawn as 26px chips: 400 12px mono, 1px line-2 edge, radius 3,
  the chosen one on ink-3 fg-1): "every 15 minutes", "every day at 03:00", "weekdays at
  09:00", "first of the month"; or a cron line in the same field (five fields; detected by
  shape). Under it the preview box (1px line-2, radius 4, ink-0, padding 12px 14px, 400
  13px/1.3 mono): **cron** (fg-1), **next** the next three runs ("14:15 · 14:30 · 14:45 UTC",
  the console's relative-day words, each a `<time>` so the script adds the local times once
  after the three: "· 16:15 · 16:30 · 16:45 local"), **timezone** ("UTC"). It is drawn after a
  **Check** submit (secondary sm, `name="action" value="check"`, redraws the form with the
  preview) and on every refused submit; text it can't read gets the warning line "Couldn't
  read that. Try "every 2 hours" or paste a cron line." in the box. The words it reads: "every
  N minutes/hours", "every day at HH:MM", "every hour", "weekdays at HH:MM", "<weekday>s at
  HH:MM", "first of the month [at HH:MM]"; anything else must be cron.
- **Limits** (`.cv-label` "Limits"): a 3-column grid, gap 12: **Stop after** (32px select: 5
  min, 30 min, 1 h, 2 h; default 30 min), **Memory** (select: 256 MB, 512 MB, 1 GB, 2 GB;
  default the service's reservation rounded up to one of them), and no "Retry on failure": it
  isn't in the plan, and a retry hides a failure; then the checkbox **"Skip a run if the last
  one is still going"** (checked by default; unchecked means "start another").
- Footer row: **Create schedule** / **Save schedule** (primary sm) and a Cancel link to
  `/schedules`. No "Run once now" on this page: it needs a schedule to exist; the row offers
  Run now right after creation, and the answer lands on `/schedules?created=<id>` with that
  row open.
- A refused submit redraws the page with the field error under its field (page 2's rule).

## The run's page (`/schedules/{id}/runs/{runId}`)

- Crumbs `Schedules / rates-sync` (the schedule a link to `/schedules#schedule-<id>`); the app
  header is not drawn (it is not an app page).
- Head: title "`rates-sync` · yesterday 14:00 UTC" (the name mono, 30px; a `<time>`, so the
  script adds "· 16:00 local" in fg-3), the sentence: the run's
  state sentence from the detail ("Stopped at the 5 min limit." / "Exited with code 1." /
  "Finished in 48 s." / "Skipped: the run before was still going." / "Couldn't start: …"),
  then "started 14:00:02 UTC · took 5 min 0 s · `web` of `booking-api` · v24" 13px mono fg-3.
- **Output**: a `.cv-panel` with the log lines as the Logs tab draws them (copy ID column
  dropped: one copy), stdout and stderr interleaved by time, stderr marked as on Logs; 13px
  mono; "No output." when empty; a first line in fg-3 when cut: "The first N lines were
  dropped; 1 MiB is kept." No live follow: a run's page is a record, and a running run's page
  says "Still running: reload for more." under the head.
- A built-in backup's runs link to the app's Backups tab instead (its output isn't kept the
  same way).

## Narrow (under 900px, checked at 390px)

- The page head stacks (page 3's rule); the filter wraps to the field, the selects, Apply.
- The table's row folds like the home's: the mark, the name and its pill on the first line;
  the app · command line; then "Runs", "Next", "Last run" as label/value pairs; the strip on
  its own line, wrapping at 14 marks. The detail stacks its two columns; the bars keep their
  height 56.
- The form's setting rows stack (label over control); the preset chips wrap.

## May differ (server-rendered, no JavaScript)

- No drawer, no "…" menu, no live progress bar movement (the bar is drawn at refresh), no
  typing-time preview: the Check submit draws it.
- The service select without script lists `app › service` pairs.
- The run's page doesn't follow output; reload.
- Confirmations are `<details>`; Run now, Pause and Resume are plain posts.

## API (for the spec; names only)

`listSchedules`, `createSchedule`, `getSchedule`, `updateSchedule`, `deleteSchedule`,
`runSchedule`, `stopScheduleRun`, `pauseSchedule`, `resumeSchedule`, `listScheduleRuns`,
`getScheduleRunOutput` (`text/plain`); `Schedule{id, app, service, name, command[],
cron, words, timezone, stopAfterSeconds, memoryMB, skipIfRunning, paused, builtin,
nextRunAt, lastRun}`, `ScheduleRun{id, startedAt, endedAt, outcome: ok|failed|stopped_at_limit|
stopped|skipped|could_not_start, exitCode, error, durationSeconds, outputBytes}`; attention kind
`schedule_failed` (and the skip `warning`). All times in the API are RFC 3339 UTC, as
today. Backups appear in `listSchedules` with `builtin: true`.

## Review

The designer checks at 127.0.0.1:7070 against `p5-schedules.png`:
- the list with a failed schedule (open by default when it is the first), a running one (the
  bar), a paused one, a never-run one, a built-in Postgres backup and a built-in volume backup,
  with the sidebar item active and the count line;
- the detail of each, the bars with a failed, a stopped-at-limit and a skipped run, and the
  buttons per state (built-in without Pause/Edit/Delete);
- New schedule: each preset, a cron line, unreadable words, a stopped app disabled, a refused
  submit, the service select with hollo; Edit prefilled;
- Run now, Stop this run, Pause and Resume, Delete; the run's page with output, a cut output
  and a skipped run;
- the needs-you item and a notification for a failed run; the Configure panel;
- time: every clock time reads "… UTC" and, in Chrome, "· … local" after it; the same pages
  without script (headless with JavaScript off) show the UTC times alone and nothing broken;
  the form's hint and the preview's next three runs;
- narrow at 390px.

## Review (designer, 2026-10-23): follows the contract, four fixes

1. **The UTC · local pair never wraps mid-phrase.** In Next and Last run the script puts the local
   time on its own line, no separator, fg-3 ("Mon 03:00 local" under "in 2 d · Mon 09:00 UTC");
   the UTC text never breaks before "UTC" (`white-space: nowrap` on the `<time>`). Next is 170px.
   The count line stays one line as it is.
2. **The run page's title** is the name and the UTC time only ("boom · 02:57 UTC"); the local time
   stays in the meta line.
3. **The bars panel's words:** "no completed runs · stops at 30 min" when runs exist but none
   finished; "no runs yet" only when there are none.
4. **No horizontal scroll at any width from 390 up:** at 1440px with JavaScript off the table's
   fixed columns overflowed. The strip column is `minmax(120px, 200px)`; the fixed 150/160 columns
   and nowrap mono text are checked at 1200 and 1440.

## Rulings on the build's departures (designer, 2026-10-23)

1. **Could-not-start wording.** The record keeps Nomad's short reason. The detail and the run page
   say "Couldn't start: no `sh` in the image." (the executable wasn't found; the program is the
   command's first word), "Couldn't start: the image couldn't be pulled.", else "Couldn't start:
   Nomad couldn't start its copy." The needs-you item and the notification stay "Couldn't start."
2. The run page shows stdout, then stderr marked, not interleaved by time.
3. The cut line has no count: "The first lines of stdout were dropped; the last 64 KiB of each
   stream is kept."
4. Posts, create and save land on `/schedules?open=<id>#schedule-<id>`.
5. The preview's next three runs are compact, with one "UTC" after the three.
6. Superseded by fix 2.
7. A preset link draws the preview at once.
8. The sentences: "The last run finished in 48 s." (no colon), "It hasn't run yet.",
   "Skipped N times, the last … ago, because …".
9. Next wrapping: fix 1.
10. Built-in rows' runs always read trigger `timer` (the backups table keeps no trigger): later,
    filed in CodeAlta.
11. An owner's stop neither opens nor clears a failure item (research R5).

## Critique rulings (designer, 2026-10-23)

The critique (design-review pass, no detector) found twelve things; four were the review's fixes
1–3 and ruling 8 (whose sentence becomes the critique's "Last run finished in 8 s.", no "The").
The rest:

4. **The When hint** is derived from the first of the preview's next three runs (else the next
   occurrence of the time of day), with "today" appended when that date is today, so a DST
   change never shows two local times for one schedule.
5. **The detail's project fact** uses the project's display name and is omitted in
   Default › production, as the rail does.
6. **A built-in row has one button, "Change in Backups"**; no "Logs of last run" (the Backups
   tab shows the runs).
8. **The form's text controls are 560px** (Name, Command, When); the selects stay 360; Limits is
   two columns of 274.
9. **Run now is primary only in the row the page opened by itself** (the first failed),
   secondary in rows the owner opens. A paused schedule keeps Run now.
10. **Bars are ember for every failed kind** (the limit told in the bar's title and the caption,
    "stopped at 5 min"); amber only for the skipped stub. This overrides the body's "warn for a
    stopped-at-limit run": one status, one color.
11. **Configure's Schedules panel** has a 7px mark before each name (the row's mark), failing
    lines first, and its note reads "6 schedules · 2 failing", linking to `/schedules?app=<id>`.
12. **Narrow:** the facts' keys are 130px; the row's command line is `nowrap` with an ellipsis;
    the stacked Next has no leading dot (fix 1's block form).
- **Edit's "Run inside"** is text ("`web` of `clock`") when it can't change. The preview's
  timezone row stays.

## Last look (designer, 2026-10-24)

Approved with nothing to change. 022 is done.
