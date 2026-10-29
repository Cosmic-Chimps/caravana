# Feature Specification: Schedules

**Feature Branch**: `022-schedules` (from `main` at 0.12.0)

**Created**: 2026-10-23

**Status**: Implemented, v0.13.0

**Input**:
- **The maintainer, 2026-10-23:** "what's next we should continue with the new pages? continue",
  after 021's four pages shipped as 0.12.0.
- **docs/plan.md, row 022 (Phase 2.6, the design handoff's `SchedulesScreen`):** a command on a
  timer in a fresh copy of an app: the live version's image, variables and secrets, no traffic,
  as a periodic batch job (the shape backups already use). Presets over a cron line, a
  stop-after limit, skip a run if the last one is still going, Run now, Pause and Resume, the
  last 14 runs as marks, the logs of the last run, failed runs on the needs-you list and in
  notifications. Backups appear in the list as built-in schedules. Settles the installation's
  timezone (the handoff shows local time; backups are UTC today).
- **The design handoff:** `SchedulesScreen` (the list, "New schedule", "Run once now", "Stop this
  run", the paused / running now / failed pills, "Last runs, oldest first"). The designer session
  writes the page's contract in `contracts/`, as for 021.

## How this feature is built

As agreed for pages that need a backend: the designer's contract first, then the schedules and
their runs in the service layer and the API, then the page on real data, then the designer's
review, the critique pass, the rulings and the last look.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - A command on a timer (Priority: P1)

The owner of an app opens its schedules and adds one: a name ("nightly cleanup"), the command
to run (`php artisan queue:prune`), and when ("every day at 03:00"). From then on, at that time,
Caravana starts a fresh copy of the app's live version, with the same image, variables and
secrets the app runs with, runs the command in it, and stops the copy. The run receives no
traffic. The owner sees each run's outcome as a mark in the schedule's row, when the next run
is due, and can open the last run to read what the command printed and its exit code.

**Why this priority**: this is the feature. Everything else is how it is managed or watched.

**Independent Test**: a busybox app with a schedule whose command prints a line and exits 0
"every minute". Within two minutes the row shows a successful run, the run's detail shows the
printed line and exit 0, and the app's own copies were not touched.

**Acceptance Scenarios**:

1. **Given** a healthy app, **When** the owner adds a schedule with a command and a time,
   **Then** the schedule is listed with "next run" said in the installation's timezone, and at
   that time a run starts, ends, and is recorded with its start, end, exit code and output.
2. **Given** a schedule whose run exits 0, **When** the owner opens the run, **Then** they read
   the command's output and "exit 0" and when it started and ended.
3. **Given** an app with secrets, **When** a run executes, **Then** the command sees the same
   variables and secret values the app's live copies see, and no page, record, log line or
   notification shows a secret value.
4. **Given** an app that is stopped, never deployed, or has no healthy version, **When** a
   schedule's time comes, **Then** no run starts, and the row says why ("the app is stopped").
5. **Given** an app is redeployed, **When** the next run starts, **Then** it runs the new live
   version (image, variables, secrets) without the owner touching the schedule.

---

### User Story 2 - Managing a schedule (Priority: P1)

The owner changes a schedule's name, command or time, pauses it for a while and resumes it,
runs it once right now outside its timer, stops a run that is taking too long, and removes
it. A run that is still going when the next time comes is, by default, left to finish and
that time is skipped; the owner may instead let runs overlap. A stop-after limit stops a run
that passes it and counts it as failed.

**Why this priority**: a timer the owner can't pause, test or stop is a liability, not a
feature.

**Independent Test**: on the busybox app: Run once now starts a run at once; Pause keeps the
next timer from running and the row says "paused"; Resume brings the next run back; a
schedule with a 1-minute limit and a command that sleeps 3 minutes ends as "stopped after
1 minute", counted as failed; Remove takes the row away and no run starts afterwards.

**Acceptance Scenarios**:

1. **Given** a schedule, **When** the owner presses Run once now, **Then** a run starts within
   seconds, is marked as started by the owner (not the timer), and the timer's next run is
   unchanged.
2. **Given** a running run, **When** the owner presses Stop this run, **Then** the run ends
   within a short grace period and is recorded as stopped by the owner, not as a failure.
3. **Given** a paused schedule, **When** its time comes, **Then** nothing runs; **When** the
   owner resumes it, **Then** the next time runs as usual, and runs missed while paused are not
   made up.
4. **Given** a run still going when the next time comes and "skip when the last run is still
   going" (the default), **Then** that time is skipped and the row says so; **Given** overlap
   is allowed, **Then** a second run starts beside the first.
5. **Given** a stop-after limit of N minutes, **When** a run passes it, **Then** it is stopped
   and recorded as failed with the reason "stopped after N minutes".
6. **Given** a schedule is edited, **When** the next run starts, **Then** it uses the new
   command and time; earlier runs keep the command they ran.
7. **Given** a schedule is removed, **Then** its rows are gone from the list and its runs no
   longer count on the needs-you list; a run in progress is stopped.
8. **Given** the app is deleted, **Then** its schedules and their runs go with it.

---

### User Story 3 - Failed runs reach the owner (Priority: P2)

A run that exits non-zero, is stopped by its limit, or could not start (the image is gone, the
command isn't found) shows as failed on the schedule's row, puts the app on the Apps home's
needs-you list with what failed, and goes out through the owner's notification channels the
way a failed backup does. The next successful run clears it.

**Why this priority**: the point of a timer is that nobody is watching; failures must come to
the owner.

**Independent Test**: a schedule whose command exits 3 "every minute": within two minutes the
needs-you list names the app and the schedule, a webhook channel receives a `schedule.failed`
notification with the schedule's name and exit code, and a schedule whose next run exits 0
leaves the list (and sends the resolved notification after the usual delay).

**Acceptance Scenarios**:

1. **Given** a run exits non-zero, **Then** the row shows "failed", the needs-you list has an
   item for the app naming the schedule and the exit code, and each notification channel that
   gets problems receives it once.
2. **Given** a failed run followed by a successful one, **Then** the row shows the success, the
   item leaves the needs-you list, and the channels get the resolved notification.
3. **Given** a run could not start (no image, a command not found, the app stopped at that
   moment), **Then** it is recorded as failed with the reason in Caravana's words, never as a
   run that silently didn't happen.
4. **Given** a run is stopped by the owner, **Then** it is not a failure and sends nothing.

---

### User Story 4 - Backups as built-in schedules (Priority: P2)

The schedules list also shows each service's backup (Postgres or volume) as a built-in
schedule: its time, its last runs as marks, when the next one is due, and a link to the
Backups page. The owner can't edit, pause, remove or "run once now" a backup from here
(Backups keeps that); they can see at a glance everything that runs on a timer for the app.

**Why this priority**: one place for "what runs when" is what the handoff draws; backups are
most owners' only timers today.

**Independent Test**: a volume-backup app shows its backup in the schedules list as built-in,
with the marks of the runs the Backups page lists and the same next-run time; its row has no
edit, pause or remove action.

**Acceptance Scenarios**:

1. **Given** a service with a backup policy, **Then** the schedules list has a built-in row for
   it with the same runs and next time the Backups page shows.
2. **Given** a built-in row, **Then** it offers only "Open backups"; the schedule's forms refuse
   a built-in name.

---

### User Story 5 - UTC and the viewer's local time, side by side (Priority: P2)

Every time a schedule or a backup shows ("every day at 03:00", "next run in 4 hours", a run's
start) is written in UTC, the zone backups already use, and beside it the owner reads the same
moment in their own local time, taken from their browser. The two pages, Schedules and Backups,
say times the same way, so they never disagree, and nothing the owner wrote before moves.

**Why this priority**: a timer in an unknown zone runs at the wrong time; the handoff shows
local time while backups say UTC today. The maintainer's call (2026-10-23): "we should show
UTC and localtime".

**Independent Test**: a schedule written as "every day at 03:00" runs at 03:00 UTC; the row
reads "03:00 UTC" and, in a browser set to Madrid, "05:00 your time" beside it; with scripts
off, the row reads "03:00 UTC" alone and is still right; the API answers every time in UTC.

**Acceptance Scenarios**:

1. **Given** any clock time on Schedules or Backups (a next run, a run's start or end, the
   preview of the next runs), **Then** it is drawn in UTC, named as such, and the viewer's local
   time is drawn beside it when their browser can say it.
2. **Given** a browser without JavaScript, **Then** every time still reads correctly in UTC and
   nothing else is missing from the page.
3. **Given** the New schedule form, **When** the owner types a time of day, **Then** it is taken
   as UTC, and the page says what that is in the viewer's time before they save.
4. **Given** a backup policy written before this feature, **Then** it keeps running at the same
   instant and is drawn the same way as a schedule's time.

---

### Edge Cases

- A schedule's command is the image's own program: no shell is implied. A command that needs
  one is written as `sh -c '...'`, as Run a command (020.5) does; the page says so.
- A run's output may hold a secret the command prints (as Run a command's can). It is shown on
  the run's page only, never in a notification, a needs-you item, a log line of Caravana's or
  the live stream.
- The app has several services: the owner picks which service's image and variables the run
  uses (the first service by default, as backups name theirs).
- A schedule's time never comes (an impossible cron line such as 31 February): refused when
  written, naming the field.
- A cron line that fires more often than every minute, or more than once a minute, is refused.
- Caravana restarts while a run is going: the run is found again and its outcome recorded;
  a run whose copy is gone is recorded as failed, "lost while Caravana was restarting".
- The orchestrator can't be reached: the list still draws from records, says the live state is
  unknown, and Run once now refuses with the reason.
- The orchestrator's own history of a run is gone (it keeps a limited window): the run's record still has
  its outcome and the output captured when it ended; a run whose output was never captured
  says "output not kept".
- An app's live version is rolled back after a run started: the run finishes on the version it
  started with.
- Two "Run once now" presses in a row: with skip-when-running, the second is refused with "a
  run is going"; with overlap allowed, both run.
- More than 14 runs: the marks show the newest 14; older runs are kept for the run list and
  pruned beyond a bounded history per schedule.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: An owner MUST be able to create, edit, pause, resume, remove and run once a
  schedule on an app, through the console and the API alike, with every form working without
  JavaScript.
- **FR-002**: A schedule MUST have a name (unique in its app), a command (a program and its
  arguments, no shell implied), a time (a preset or a five-field cron line), the service whose
  image and variables it uses (the app's first service by default), an optional stop-after
  limit in minutes, and whether a run is skipped while the last one is still going (default)
  or may overlap.
- **FR-003**: The time presets MUST cover at least: every hour, every day at a time of day,
  every weekday at a time of day, every week on a day at a time of day, every month on a day
  at a time of day; and a cron line for the rest. Whatever the owner picks, the schedule stores
  one cron line and the page shows it back in words.
- **FR-004**: A run MUST start a fresh copy of the app's live version (the pinned image, the
  variables and the secret values its live copies have) that receives no traffic, run the
  command in it, and stop the copy when the command ends. The app's own copies MUST NOT be
  restarted, scaled or redeployed by a run.
- **FR-005**: A run MUST record when it started and ended, how it was started (timer, owner),
  the command it ran, its exit code or its end reason (stopped by the owner, stopped after the
  limit, could not start, lost), and the command's output up to a bounded size, captured when
  it ends.
- **FR-006**: A run MUST be shown as one of: running, succeeded, failed, stopped, skipped (the
  last run was still going). The schedule's row MUST show its last 14 runs as marks oldest
  first, whether it is paused or running now, and when its next run is due.
- **FR-007**: A failed run (non-zero exit, stopped after the limit, could not start, lost)
  MUST put an item on the needs-you list for the app naming the schedule and the reason, and
  MUST go out through notification channels that receive problems, as a failed backup does; the
  next successful run MUST resolve it. A run stopped by the owner is not a failure.
- **FR-008**: Pausing a schedule MUST stop its timer without losing its runs or settings;
  resuming MUST take the next time only, not the times missed.
- **FR-009**: Stop this run MUST end a running run within a grace period and record it as
  stopped by the owner.
- **FR-010**: A schedule's time MUST be refused, naming the field, when it is not a valid
  five-field cron line, never fires, or fires more than once a minute.
- **FR-011**: A schedule MUST NOT run while its app is stopped, never deployed or without a
  healthy live version; the row MUST say why, and nothing is recorded as failed for it.
- **FR-012**: Removing a schedule MUST remove it and its runs, stopping a run in progress;
  deleting an app MUST remove its schedules and their runs.
- **FR-013**: Each service's backup policy MUST appear in the schedules list as a built-in
  schedule with its time, next run and last runs, read-only here and linking to Backups; the
  schedule forms MUST refuse a built-in name.
- **FR-014**: Every timer's time (a schedule's or a backup's time, next run, run start and end,
  the preview of the next runs) MUST be written, stored and drawn in UTC, named as such, with
  the viewer's local time drawn beside it when the browser can say it; without JavaScript the
  UTC time MUST stand alone and be correct. The New schedule form MUST take a time of day as
  UTC and say what that is in the viewer's time. There is no installation timezone setting,
  and existing backup policies keep their instant.
- **FR-015**: The API MUST offer the schedules, their runs (with output) and every action the
  console has, and the console MUST use the same service layer (constitution V). A deploy token
  scoped to an app MUST NOT manage its schedules (default deny stays).
- **FR-016**: No page, API answer, record, log line, notification or live event MUST hold a
  secret value; a run's output MUST be shown only on the run's page and in its API read.
- **FR-017**: The run history per schedule MUST be bounded (at least the last 14 marks and a
  longer list are kept; older runs are pruned), and output MUST be bounded in size, with the
  cut said.
- **FR-018**: After a Caravana restart, runs that were going MUST be found again and their
  outcome recorded; one whose copy is gone MUST be recorded as failed, "lost".

### Key Entities

- **Schedule**: belongs to one app; name, service, command, cron line (in the installation's
  zone), stop-after limit, overlap policy, paused or not, created and changed times.
- **Run**: belongs to one schedule; started by (timer, owner), start and end times, state
  (running, succeeded, failed, stopped, skipped), exit code or end reason, the command as run,
  output (bounded) and whether it was cut or not kept.
- **Built-in schedule**: a service's backup policy, drawn in the list from Backups' own records;
  not stored twice.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: An owner adds a working schedule to an app in under one minute from the app's
  page, without reading documentation: name, command, a preset, save.
- **SC-002**: A scheduled run starts within 60 seconds of its due time and its outcome shows
  on the page within 10 seconds of ending, without a reload where live updates work.
- **SC-003**: A failed run reaches the needs-you list within one minute and every subscribed
  channel within two minutes of ending; a schedule that fails every minute sends one "opened"
  notification, not one per run.
- **SC-004**: Run once now starts a run within 10 seconds; Stop this run ends it within 30.
- **SC-005**: With the zone set to a non-UTC zone, every timer time shown on Schedules, Backups
  and Settings names the zone, and a schedule "at 03:00" fires at 03:00 in that zone.
- **SC-006**: The lab test covers: a run that succeeds every minute, one that exits 3 (needs-you
  and notified), one stopped by its limit, Run once now, Pause and Resume, Stop this run, a
  built-in backup row, no secret value anywhere, and a restart of Caravana during a run.
- **SC-007**: The app's own copies see no restart during any run.

## Assumptions

- A schedule belongs to one app and runs in that app's live version; the service it uses is
  chosen when the app has several. Schedules without an app are out of scope.
- Where the list lives is the designer's contract call (`contracts/schedules-screen.md`: a
  workspace page `/schedules`, a Schedules panel on Configure, no new app tab).
- Times are UTC throughout, as backups already are; the viewer's local time is a display
  addition from their browser, never stored. There is no installation timezone setting (the
  maintainer, 2026-10-23).
- Run output is kept up to a bounded size (the same order as Run a command's 64 KiB cap) and
  the history is bounded per schedule (about 100 runs); the exact numbers are the plan's.
- A run's "fresh copy" has the app's memory settings; a schedule needing more memory than the
  service reserves is a later addition.
- "Could not start" and "lost" reasons are in Caravana's words, as Run a command's are.
- Out of scope: schedules on several nodes (Phase 3), run output kept longer than Caravana's
  bounded record, editing a backup's time from this page, the handoff's `SchedulerScreen`
  (Phase 3), schedules in a dedicated service account or user.
