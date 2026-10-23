# Contract: 022 Schedules, the `SchedulesScreen`

Written by the caravana_designer session on 2026-10-23, before the spec. The values come from
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
  `ProhibitOverlap` from the skip setting, the installation's timezone (below), and a
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
- **Timezone, settled here.** The installation has one timezone, `CARAVANA_TIMEZONE`
  (`-timezone`, an IANA name, default `UTC`; the installer offers the host's zone from
  `/etc/timezone` or `timedatectl`, and writes it to the unit's environment, so rerun it to
  change it). Schedules' cron lines are read in it (Nomad's `TimeZone`), and every clock time
  the console shows (the Next and Last run cells, the stages' Live time from page 4, backups'
  run times) is drawn in it, with the zone's name in the Schedules page's lede: "Times are
  Europe/Madrid." Backups keep their stored UTC cron (the API says UTC and existing policies
  must not move): their row's words say "Every day at 03:00 UTC" as today, and their Next cell
  is drawn in the installation zone like every other time. A later change can migrate backup
  policies to the installation zone; not here.

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
  timer in a fresh copy of an app, then stop. Times are Europe/Madrid." 15px fg-2, and **New
  schedule** (primary sm, plus icon). Under it a filter row (page 4's shape, one 32px line):
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
  - **Next:** 400 13px/1.3 mono fg-2: "14:15" today, "tomorrow 02:00", "in 2 d · Sun 03:00"
    (the console's relative words, the clock in the installation zone); "paused" fg-4; "—" for
    a built-in whose app is stopped;
  - **Last run:** two 12px/1.2 mono lines: running: "started 14:00:02" in rolling over a 120×4
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
- **When**: a text field (sans) for the words, with the four presets under it as links that
  fill the field (`?preset=`, drawn as 26px chips: 400 12px mono, 1px line-2 edge, radius 3,
  the chosen one on ink-3 fg-1): "every 15 minutes", "every day at 03:00", "weekdays at
  09:00", "first of the month"; or a cron line in the same field (five fields; detected by
  shape). Under it the preview box (1px line-2, radius 4, ink-0, padding 12px 14px, 400
  13px/1.3 mono): **cron** (fg-1), **next** the next three runs ("14:15 · 14:30 · 14:45", the
  console's relative-day words), **timezone** (the installation's). It is drawn after a
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
- Head: title "`rates-sync` · yesterday 14:00" (the name mono, 30px), the sentence: the run's
  state sentence from the detail ("Stopped at the 5 min limit." / "Exited with code 1." /
  "Finished in 48 s." / "Skipped: the run before was still going." / "Couldn't start: …"),
  then "started 14:00:02 · took 5 min 0 s · `web` of `booking-api` · v24" 13px mono fg-3.
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
`schedule_failed` (and the skip `warning`); `GET /v1/health` or the settings answer gains
`timezone`. Backups appear in `listSchedules` with `builtin: true`.

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
- the timezone: an instance with `CARAVANA_TIMEZONE=Europe/Madrid` showing the lede, Next
  cells and a backup's "03:00 UTC" words side by side;
- narrow at 390px.

## Maintainer's ruling on time (2026-10-23)

"we should show UTC and localtime", with the viewer's browser time as the local one. This replaces
the contract's "Timezone, settled here" rule and the installation setting:
- No installation timezone and no `CARAVANA_TIMEZONE`. Schedules are written and stored in UTC, as
  backups are; nothing moves and nothing is migrated.
- Every clock time the console draws (next run, run times, the "Check" preview's next three runs,
  a backup's time) reads UTC first, with the viewer's local time beside it, added by a small
  vendored script from a `<time datetime="…Z">`; without JavaScript the UTC time stands alone and
  is still right. The lede says times are UTC and, when the script runs, names the viewer's zone.
- The New schedule form takes a time of day in UTC and says what that is in the viewer's time.
The designer amends the affected rows (lede, Next cell, the preview box, the run page) in place.
