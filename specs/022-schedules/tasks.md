# Tasks: Schedules (022)

**Input**: `specs/022-schedules/` (spec.md, plan.md, research.md, data-model.md,
contracts/api.md, contracts/schedules-screen.md, quickstart.md)

**Tests**: required (constitution VIII): table tests beside the code, golden JSON for the run
job, sentinel searches for output and secret values, the API contract test against
`api/openapi.yaml`, a `TestConsoleLab` step and a gate step.

**Order**: as the plan says, the backend and the API first (Phases 1–2 and the backend halves of
US1–US4), then the console on real data (the console tasks of each story, US5), then the lab
steps, docs and the review cycle. A Sonnet agent builds each phase; Opus reviews before the next.

## Phase 1: Foundation (no story label; everything below needs it)

- [X] T001 [P] `internal/cronwords/cronwords.go` + `cronwords_test.go`: `Parse(text) (cron, error)`
  (five-field cron by shape; the contract's words: "every N minutes/hours", "every hour", "every
  day at HH:MM", "weekdays at HH:MM", "<weekday>s at HH:MM", "first of the month [at HH:MM]"; the
  error text is the contract's "Couldn't read that. Try "every 2 hours" or paste a cron line."),
  `Words(cron) string` (the inverse, ending " UTC" for a time of day, "On `<cron>`" last resort),
  `Validate(cron) error` (five fields, parses, fires within a year, at least 60 s apart; errors
  name what's wrong), `Next(cron, after, n) []time.Time` in UTC. Table tests for each shape, the
  refusals ("31 2 * * *", six fields, "* * * * * *", "*/30 * * * * " seconds) and round-trips.
- [ ] T002 [P] `internal/store/migrations/0017_schedules.sql` and `internal/store/schedules.go`
  (+ `schedules_test.go`): tables per data-model.md (CHECK on status, UNIQUE(app_id, name),
  indexes); `InsertSchedule`, `UpdateSchedule`, `GetSchedule`, `ListSchedules(appID "" for all)`,
  `DeleteSchedule` (cascades runs), `SetSchedulePaused`, `ArmSchedule(id, at)`; `InsertRun`
  (`starting` or `skipped`), `SetRunRunning`, `SetRunStopIntent(id, reason, at)`, `SettleRun`
  (status, exit code, error, task_started_at, finished_at, output and flags, output_kept),
  `GetRun`, `ListRuns(scheduleID, limit)`, `OpenRuns(scheduleID)` and `AllOpenRuns()`,
  `LatestScheduleRuns()` (newest ended run per schedule that isn't skipped or stopped),
  `SkipStreak(scheduleID)` (cap 3), `PruneRuns(scheduleID, keep)`; `DeleteApp` deletes both
  tables. Tests: round-trips, the cascade, the prune, the latest-run query with skipped and
  stopped rows in between.
- [X] T003 [P] `internal/nomadjob/schedule.go` + `schedule_test.go` +
  `testdata/schedule-run.golden.json`: `ScheduleRunJobID`, `ScheduleRunVariablePath`,
  `ScheduleRunJob(appID, svc, image, command, memoryMB, s, private, names)` per plan.md (batch
  job and group, task named after the service, `entrypoint`/`args`, env, `variableTemplate`,
  resources from memoryMB with the ceiling max(memoryMB, svc.MemoryMaxMB), KillTimeout 10 s,
  Logs 2×1 MB, Meta, no Services, the `names` sidecar when asked, `withPullAuth`). Golden test
  for a one-service and a multi-service app; a test that the job has no Services and no ports.
- [ ] T004 `internal/deploy/schedules.go` + `schedules_test.go`: `StartScheduleRun(ctx, job,
  secrets map[string]string)` (write the variable with the secret set and `withPullCredentials`,
  then Register; a refused register returns a typed error whose message is in Caravana's words),
  `ScheduleRunState(ctx, jobID) (RunState, error)` (`ErrRunGone` on 404; the newest allocation's
  ClientStatus, DesiredStatus, the service task's StartedAt, its newest Terminated event's
  exit_code and oom_killed, a Driver/Setup Failure event), `ScheduleRunOutput(ctx, allocID,
  task) (stdout, stderr []byte, cutOut, cutErr bool, err)` over `Logs` (which gains a "cut"
  return in `logs.go`, test in `logs_test.go`), `StopScheduleRun` (Deregister purge=false),
  `PurgeScheduleRun` (purge=true, then DeleteVariable). `StopApp` takes run job IDs beside the
  backup job IDs (`deploy/actions.go`). deploytest (`deploytest/nomad.go`): run jobs in the fake's
  job table, `FinishRun(jobID, exitCode)`, `FailRunStart(jobID)`, `OOMRun(jobID)`, scripted logs
  per job, Deregister recorded with purge, `RunState` answered from the table.

## Phase 2: Service core (no story label; US1–US4 build on it)

- [ ] T005 `internal/service/schedules.go` + `schedules_test.go`: types `Schedule`, `ScheduleRun`
  (API shapes of contracts/api.md), errors `ErrScheduleNotFound`, `ErrRunNotFound`,
  `ErrScheduleExists`, `ErrScheduleBuiltin`, `ErrRunGoing`, `ErrRunNotRunning`,
  `ErrOutputNotKept`; validation (name, command as `RunCommand` validates, `cronwords.Validate`,
  stopAfterSeconds and memoryMB enums, service exists and has no volumes → `*app.FieldError` on
  `service`; app defined); `CreateSchedule`, `UpdateSchedule` (a cron change re-arms),
  `DeleteSchedule` (stop intent `schedule_removed` and deregister of open runs), `PauseSchedule`,
  `ResumeSchedule` (re-arms), `GetSchedule`, `ListSchedules(filter{app, project})` (stored rows
  plus built-in backup rows `backup:<app>:<service>` from the apps' policies and the backups
  table, each with NextRunAt, LastRun, RunningNow, UsualSeconds, Words; sorted failed, running,
  next run, paused), `ListScheduleRuns`, `GetScheduleRun`, `GetScheduleRunOutput(stream)`; the
  `schedules` live notice. Table tests for validation and the list's sort and built-ins.
- [ ] T006 `internal/service/schedulerun.go` + tests: `startRun(ctx, sched, trigger, slot)`:
  write-ahead `starting` row, the newest healthy deployment's digest for the service
  (`runningImage`; `ErrNoLiveDeployment` → the run is `could_not_start` "the app has no live
  version" when timer-started, a 409 when owner-started), the service's current secret set,
  the job, `StartScheduleRun`, flip to `running`; `RunScheduleNow` (refusals: stopped app,
  built-in, `ErrRunGoing` when skipIfRunning and a run is open or 3 are open); `StopScheduleRun`
  (intent `owner`, then deregister; `ErrRunNotRunning`); `maskSecrets(out, values)` (values of
  6+ bytes → `[secret NAME]`, longest first). Tests with deploytest: the variable written then
  removed, the row states, masking.
- [ ] T007 `internal/service/scheduleclock.go` + tests: `runScheduleClock(ctx, gap)` every
  `scheduleScanGap` (10 s; `SetScheduleScanGap` for the lab): per unpaused schedule the due slot
  (`dueSlot` generalised in `schedule.go` with base `max(armed_at, newest slot_at)`, catch-up 1 h,
  UTC); skip conditions → a `skipped` row with the reason; deferral (no record) while the app's
  newest deployment is pending/running, the app stopped, undefined or without a healthy
  deployment; otherwise `startRun(timer, slot)`. Table tests with a fake clock: a due slot runs
  once, a paused schedule doesn't, resume takes the next slot only, an edit re-arms, the catch-up
  window, skip-if-running, the 3-run cap, deferral.
- [ ] T008 `internal/service/schedulewatch.go` + tests: `watchScheduleRuns(ctx, gap)` every 5 s
  over `AllOpenRuns`: `ScheduleRunState`; terminal → capture output (`ScheduleRunOutput`, masked),
  settle by the outcome table of plan.md (ok / failed "exit n" / "ran out of memory" /
  `stopped_at_limit` / `stopped` from the intent / `could_not_start` for driver or setup failure
  / "stopped outside Caravana"), purge the job, `PruneRuns`, publish `ChangeSchedules` and
  `ChangeAttention`; running → the limit from `task_started_at` (`SetScheduleLimitForTests`
  setter) and the 5-minute start deadline (setter): write the intent then deregister; `ErrRunGone`
  → failed "lost while Caravana was restarting". Started in `Start` before the clock. Table
  tests for every row of the outcome table with deploytest, the limit, the deadline, lost.

## Phase 3: US1, a command on a timer (P1)

Goal: a schedule runs a command in a fresh copy at its time and records the run with output.
Independent test: quickstart §1.

- [ ] T009 [US1] `internal/api/schedules.go` + `schedules_test.go` + `api/openapi.yaml` 0.24.0:
  `listSchedules`, `createSchedule`, `getSchedule`, `updateSchedule`, `deleteSchedule`,
  `listScheduleRuns`, `getScheduleRun`, `getScheduleRunOutput` (text/plain, `Cache-Control:
  no-store`, `Caravana-Output-Cut`), schemas `NewSchedule`, `Schedule`, `ScheduleRun`, codes
  `schedule_not_found`, `run_not_found`, `output_not_kept`, `schedule_exists`, `builtin_schedule`,
  the `invalid_field` fields; `protectedRoutes` entries; the contract test; the scope walk expects
  403 for every new route (no `allowedForDeployTokens` change); a test that no list, run or
  attention answer holds a run's output.
- [ ] T010 [US1] Console list and run page: `internal/ui/schedules.go`, `templates/schedules.html`,
  `schedulesnav` in `shell.go`/`shell.html` (Workspace › Schedules, `clock` icon in `icons.go`,
  active on `/schedules*`), routes `GET /schedules`, `GET /parts/schedules`,
  `GET /schedules/{id}/runs/{runId}` per contracts/schedules-screen.md "The list", "The row's
  detail" (the state sentence table, the bars, the facts grid; the buttons of US2 come in T012),
  "The run's page" (`no-store`, the output panel, the cut line "The first N lines were dropped;
  the last 64 KiB of each stream is kept."), the empty state, the footer, the count line, the
  filter (project select only with more than one project, app select, Apply, Clear), the live
  region on the `schedules` notice with stable ids; `ui/parity_test.go` entries. Handler tests
  per state (never run, ok, failed, running, paused, skipped) and a sentinel test that the
  output appears only on the run's page.
- [ ] T011 [US1] Styles: `internal/ui/style/input.css` "Page 5 (022)" section per the contract's
  sizes (the table columns, the row, the detail grid, the bars, the strip, the preview box, the
  chips, narrow rules under 900px), `tools/ui-css.sh build`, and the DESIGN.md Schedules section.

## Phase 4: US2, managing a schedule (P1)

Goal: create, edit, pause, resume, run now, stop a run, delete, from the console and the API.
Independent test: quickstart §3.

- [ ] T012 [US2] API: `runSchedule` (202), `pauseSchedule`, `resumeSchedule`, `stopScheduleRun`
  (202), codes `run_going`, `run_not_running`, `app_stopped`, `no_live_deployment` on these
  routes, in `internal/api/schedules.go` and `api/openapi.yaml`; tests including a built-in's
  409s and the deploy-token 403s.
- [ ] T013 [US2] Console form: `internal/ui/schedulesform.go` + template section: `GET
  /schedules/new`, `POST /schedules` (`action=check|create`), `GET /schedules/{id}/edit`,
  `POST /schedules/{id}` (`action=check|save`) per the contract's "New schedule" (Name with the
  default from the command, Run inside: app select with stopped apps disabled and the service
  select, without script listing `app › service` pairs, services with volumes disabled, and the
  hint "a schedule needs a service without volumes" when every service of the picked app has
  volumes; Command with the `splitCommand` hint; When with the four preset links, the Check
  submit and the preview box in UTC with its "Couldn't read that…" line; Limits: Stop after,
  Memory with the service's rounded default, the skip checkbox checked by default; Create/Save
  and Cancel; a refused submit redraws with the field error; create lands on
  `/schedules?created=<id>` with the row open). Handler tests: each preset, a cron line,
  unreadable words, each refusal, edit prefilled.
- [ ] T014 [US2] Console posts: `POST /schedules/{id}/run|pause|resume|delete` and
  `POST /schedules/{id}/runs/{runId}/stop` in `internal/ui/schedules.go`, the detail's buttons
  (Run now primary when the last run failed, Stop this run and Delete… behind `<details>`
  confirmations, Pause/Resume, Edit, Logs of last run; none of Pause/Edit/Delete on a built-in,
  "Change in Backups" instead), refusals redrawn with the sentence (409 status), the Configure
  panel "Schedules" in `configure.go`/`app.html` (lines `name · words`, "No schedules.", Manage
  and New links). Handler tests.

## Phase 5: US3, failed runs reach the owner (P2)

Goal: a failed run is a needs-you item and a notification; the next success clears it.
Independent test: quickstart §2.

- [ ] T015 [US3] `internal/service/attention.go`: kinds `AttentionScheduleFailed` and
  `AttentionScheduleSkipping` from `LatestScheduleRuns` and `SkipStreak`, `Schedule`/`ScheduleID`
  on `AttentionItem`, `Detail` from the contract's sentence table ("Stopped at the 5 min limit.",
  "Exited with code 3.", "Couldn't start: …", "`<name>` has been skipped 3 times: each run takes
  longer than the time between runs."), never output; not copy kinds. `notifier.go`:
  `problemKey` with the schedule ID in the fourth slot, `problemEvent` wording, `problemPath` →
  `/schedules#schedule-<id>`; `notify.Event` gains `Schedule` and `ExitCode` (`internal/notify`),
  rendered in the webhook JSON and the Slack/Discord/ntfy text; `api/openapi.yaml`
  `AttentionItem.kind` and fields, `NotificationEvent` fields; `internal/api/attention.go`.
  Tests: `attentionOf` tables, `diffProblems` with the new kinds (one opened per failing
  schedule, resolved after a success, a removed schedule resolved), the render of each channel
  kind, no output in any delivery payload.
- [ ] T016 [US3] Console: the needs-you row wording for both kinds in `internal/ui/attention.go`
  (linking to the schedule), the home's headline counting them, the Apps row's trouble words;
  handler tests.

## Phase 6: US4, backups as built-in schedules (P2)

Goal: each backup policy is a read-only row of the list with its runs and next time.
Independent test: quickstart §5.

- [ ] T017 [US4] Service and API: built-in rows in `ListSchedules` (T005 placed them; this task
  tests them end to end): `builtin: true`, words from the policy's cron, next from `cronwords.Next`,
  last runs from the backups table mapped to `ok|failed|running`, `getScheduleRunOutput` 404
  `output_not_kept`, the 409 `builtin_schedule` on every mutation; a volume and a Postgres
  policy in the tests.
- [ ] T018 [US4] Console: the built-in row and detail ("`<service> backup`", "pg_dump → `backups`"
  / "volumes → `backups`", the "Made by Backups…" sentence, Change in Backups, the run marks
  linking to the Backups tab), the Backups tab's times as `<time>` elements (T019's helper), the
  form refusing a built-in name. Handler tests.

## Phase 7: US5, UTC and the viewer's local time (P2)

Goal: every clock time reads UTC, with the viewer's local time beside it when scripts run.
Independent test: quickstart §6.

- [ ] T019 [US5] `internal/ui/view.go`: a `clock` template helper drawing `<time
  datetime="<RFC 3339 Z>" class="cv-clock">14:15 UTC</time>` (and the relative-day words the
  contract lists); applied on Schedules (Next, Last run, the strip titles, the detail, the
  preview box's three next runs), the run's page (title and "started …"), the Backups tab's run
  and next times, and the deployment entry's Live stage (`stages.go`). Tests that each page's
  clock times are `<time>` elements with a `Z` datetime and the " UTC" text.
- [ ] T020 [US5] `internal/ui/static/js/localtime.js` (ours: on load and after each htmx swap,
  for every `time.cv-clock` append `<span class="cv-clock-local"> · 16:15 local</span>` from
  `Intl.DateTimeFormat` in the browser's zone, the same shape as the UTC text; the lede's
  "Your local time is shown beside them."; the form's "03:00 UTC is 05:00 local" hint; the
  preview's three local times once after the three), listed in `tools/ui-vendor.versions` as a
  committed file with its checksum and checked by `tools/ui-vendor.sh check`, loaded from the
  shell with the console's CSP (`'self'`, no inline); `.cv-clock-local` fg-3 in `input.css`;
  `static/js/LICENSES.md` unchanged (ours). A test that the shell loads it and that every page
  renders correctly with its text alone (the no-script case is the server's output already).

## Phase 8: Lab, docs and the review cycle (no story label)

- [ ] T021 `cmd/caravana/console_schedules_test.go` and the `console-schedules` entry in
  `console_lab_test.go` after `console-backup-daily`: a busybox app with `labResources`; through
  the forms: create from words, Run now (ok; the run page shows the sentinel; no sentinel in
  any list, item, delivery, log line, notice or the SQLite file), a schedule exiting 3 (item and
  `problem.opened` with `kind: schedule_failed`, then resolved after a fixed run), the limit
  shortened by the setters with `sleep 180` (`stopped_at_limit`), Stop this run, Pause and
  Resume, a secret value echoed and masked, a built-in volume-backup row, Delete; the one
  clock-bound check ("every minute" runs on its own within 70 s). `startLab` sets the scan gap
  to 5 s.
- [ ] T022 `cmd/caravana/gate_schedules_test.go` and the `schedules` step in `gate_test.go` after
  `app-actions`: the same through the API with the gate's token, every schedule route 403 for a
  deploy token, and `RestartCaravana` (new on `gateTarget` in `lab_test.go`: cancel `runServer`'s
  context, run it again on the same data dir, wait healthy) during a `sleep 20` run that then
  ends `ok`.
- [ ] T023 [P] Docs: `CHANGELOG.md` Unreleased (Added: schedules, the API, the needs-you kinds,
  UTC with local time; Changed: Backups' times drawn as clock elements), `docs/plan.md` row 022
  ("one batch job per run on Caravana's clock" instead of "a Nomad periodic batch job", and the
  time ruling), the CLAUDE.md "Where we are" paragraph for 022, `api/openapi.yaml` info text.
- [ ] T024 Checks (the CI list, `go test -race` on ui/service/store), `TestConsoleLab`, the
  review instance staged with the contract's review list (a failed, a running, a paused, a
  never-run, a built-in Postgres and a built-in volume schedule; the form states; the run pages;
  the needs-you item and a delivery; the Configure panel; the clock times with and without
  script; 390px), the designer's review, the impeccable critique agent (no detector), the
  designer's rulings recorded in the contract, fixes, the last look.

## Dependencies

- Phase 1 (T001–T004, parallel) → Phase 2 (T005 → T006 → T007, T008) → US1 (T009 → T010, T011)
  → US2 (T012, T013, T014) → US3 (T015 → T016) → US4 (T017 → T018) → US5 (T019 → T020) → Phase 8.
- US3, US4 and US5 depend on US1 and US2's code but not on each other; T023 can start any time
  after Phase 2.

## Parallel examples

- Phase 1: T001, T002, T003 at once (different packages); T004 after T003 (it builds the job).
- US1: T011 (styles) beside T010.
- Phase 8: T021 and T022 beside T023.

## MVP

US1 with its API (T001–T011): a schedule created through the API or the console runs at its
time, and its run's page shows the output. Everything after is management, alerting, the
built-ins and the local time.
