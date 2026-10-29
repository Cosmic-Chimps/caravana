# Implementation Plan: Schedules

**Branch**: `022-schedules` (from `main` at 0.12.0) | **Date**: 2026-10-23 |
**Spec**: [spec.md](./spec.md) | **Page contract**: [contracts/schedules-screen.md](./contracts/schedules-screen.md) |
**API contract**: [contracts/api.md](./contracts/api.md)

## Summary

A schedule runs a command on a timer in a fresh copy of one service of an app, from the app's
newest healthy deployment (its pinned digest, env and secret values), with no ports and no
traffic, and the copy ends with the command. Caravana keeps the clock itself (the volume-backup
scheduler's `cronexpr` shape, research R1) and registers **one one-off Nomad batch job per run**,
so nothing is registered between runs and nothing goes stale after a deploy. Every run is a row
in `schedule_runs` with its outcome, exit code and the last 64 KiB of each output stream, captured
within seconds of the run ending and before the job is purged (R2). Stop this run and the
stop-after limit deregister the run's job (R3). A failed run is a needs-you item
(`schedule_failed`) and a `problem.opened` notification (R5); three skipped runs in a row are a
warning item. Backups are rows of the same list, built in. Times are UTC throughout, and the
console draws the viewer's local time beside each one with a small script (the maintainer's
ruling, 2026-10-23); there is no installation timezone. API 0.24.0; migration 0017.

## Technical Context

Go 1.27, no new module: `hashicorp/cronexpr` and the Nomad client are direct dependencies
already. Storage: migration `0017_schedules.sql` (tables `schedules`, `schedule_runs`). Nomad: the
scoped policy needs nothing new (`submit-job`, `read-job`, `read-logs` and `variables
nomad/jobs/*` cover register, deregister, allocations, logs and the per-run variable; R1, R7). One
new pure package, `internal/cronwords` (words ⇄ cron, next runs). Testing: golden job file, table
tests for the clock and the settle table, deploytest fakes gaining an exit code and a deregister,
service, API (contract against openapi), UI, a `TestConsoleLab` step `console-schedules` and a
gate step `schedules` (R8).

## Constitution Check

| Principle | Assessment |
| --- | --- |
| I. Small | PASS. No module, no runtime service. One static script of our own (`localtime.js`), vendored like htmx. |
| II. One Model | PASS. `app.App` and `Translate` untouched. A run's job is a second pure translator, `nomadjob.ScheduleRunJob`, golden-tested, built from the stored app and deployment. It is a helper job under II as amended in 1.1.0: the one-off batch job `<app>_run-<id>`, beside the app's job. |
| III. Safe Deploys | PASS. A run uses the newest healthy deployment's pinned digest, never a tag; it is not a deployment and records none. A rollout in progress defers the slot rather than mixing versions (R1). |
| IV. Fail Loudly | PASS. A service with volumes refuses a schedule naming `service`; an unreadable or never-firing cron line names `cron`; could-not-start, lost and the limit are recorded in words, never dropped; a skipped slot is a row. |
| V. API-First | PASS. Twelve operations in openapi 0.24.0, the console on the same `service` methods, parity entries for the new parts routes. Deploy tokens stay denied (R7). |
| VI. Nomad-Native | PASS. Official client: `Register`, `Deregister`, `Jobs().Allocations`, `AllocFS().Logs`, variables. All Nomad code in `nomadjob` and `deploy`. A run's job is exportable like any batch job but export isn't offered (it exists for seconds). |
| VII. Secure | PASS with care. The run's secrets copy lives in Nomad only while the run does, at the job's own path (R1). Output is stored (the spec requires it) but bounded, masked of the service's secret values of 6+ bytes, returned only by the run's own read with `no-store`, and never in a log, item, notification, notice or URL (R2). Running a command on a timer is denied to deploy tokens like Run a command. |
| VIII. Plain Go | PASS. Pure clock, words parser and settle table with table tests; the watcher is the stateless shape of `watchBackups`. |

## Decisions carried from research and the contract (and where they differ)

- **Time** (the maintainer, 2026-10-23, over research R4 and the contract's "Timezone, settled
  here"): no installation timezone, no `CARAVANA_TIMEZONE`, no `time/tzdata` import. A schedule's
  cron line is UTC, as a backup's. Every clock time on Schedules, the run's page, the Backups
  tab, the Configure panel and the deployment entry's Live stage (page 4) is a
  `<time datetime="<RFC 3339 Z>">…UTC</time>`; `localtime.js`
  (ours, in `internal/ui/static/js/`, listed in `tools/ui-vendor.versions` only as a committed file
  with its checksum) appends " · 16:15 local" in fg-3 using `Intl.DateTimeFormat` (the word
  "local", never a zone name) and adds "Your local time is shown beside them." to the lede's
  server-side "Times are UTC." Without script the UTC text stands; relative words stay
  server-side in UTC. The New schedule form's time of day is UTC ("Times of day are UTC."; the
  script appends "03:00 UTC is 05:00 local") and the preview box says the next runs in UTC, the
  script adding the three local times once after them. The API answers every time in UTC and has
  no `timezone` field. `Schedule.words` says "Every day at 03:00 UTC".
- **Job shape**: research R1's option (c). The contract's "It is a Nomad periodic batch job" (its
  line 19) was the designer's description of the backend and is superseded; the page is
  unaffected. `docs/plan.md` row 022's "as a Nomad periodic batch job" is corrected in the same
  commit as the feature's docs.
- **Output kept**: per run, the last 64 KiB of stdout and of stderr (R2), for the last 100 runs of
  a schedule, not the contract's "last run, 1 MiB": the strip's marks link to each run's page, so
  each run needs its own output; 64 KiB is Run a command's cap and the tail is what explains a
  failure. The run page's cut line reads "The first N lines were dropped; the last 64 KiB of
  each stream is kept."
- **Memory**: the contract's Memory select is taken: `memoryMB` on the schedule (the run's
  reservation; its ceiling is the larger of it and the service's `memoryMaxMB`), default the
  service's reservation rounded up to 256, 512, 1024 or 2048. The spec's "the app's memory
  settings" assumption is replaced by this.
- **Concurrency**: when overlap is allowed, at most 3 runs of one schedule at once; a fourth slot
  is skipped with the reason "3 runs already going" (R1's cap).
- **Outcomes**: the contract's `outcome` values are the API's closed set:
  `running | ok | failed | stopped_at_limit | stopped | skipped | could_not_start`, with `error`
  in Caravana's words for the failed kinds. Research R3's table maps onto it: exit 0 → `ok`; exit
  n, out of memory, lost, stopped outside Caravana → `failed` with the reason; the limit →
  `stopped_at_limit`; the owner, or the app being stopped → `stopped`; driver or setup failure,
  not started within 5 minutes, no live version → `could_not_start`.
- **Built-in rows**: a service's backup appears in `listSchedules` as a schedule with
  `builtin: true`, id `backup:<app>:<service>`, its cron and words (UTC), next run from the
  cron, and its last runs from the backups table (outcome `ok|failed|running` only). Mutating
  routes answer 409 `builtin_schedule`; its runs link to the Backups tab, and
  `getScheduleRunOutput` is 404 `output_not_kept` for them.
- **Which app can have one**: a defined app with a healthy newest deployment at run time; the form
  lists stopped apps disabled. A service with volumes refuses (422 `invalid_field` on `service`,
  "the service mounts a volume; a schedule needs a service without volumes").

## Design

### `internal/cronwords` (new, pure)

- `Parse(text string) (cron string, err error)`: a five-field cron line by shape, else the
  contract's words: "every N minutes" (N divides 60), "every N hours" (N divides 24), "every
  hour", "every day at HH:MM", "weekdays at HH:MM", "<weekday>s at HH:MM", "first of the month
  [at HH:MM]"; anything else is an error whose text is the contract's "Couldn't read that…".
- `Words(cron string) string`: the inverse for those shapes, "On `<cron>`" as the last resort;
  always ending " UTC" for a time of day.
- `Validate(cron string) error`: exactly five fields, `cronexpr.Parse` ok, `Next(now)` within a
  year, and the gap between consecutive runs at least 60 s (FR-010).
- `Next(cron string, after time.Time, n int) []time.Time` in UTC.
- Used by the service (validation, words, next run) and the console (the form and the preview).

### `nomadjob` (`internal/nomadjob/schedule.go`, pure, golden `testdata/schedule-run.golden.json`)

- `ScheduleRunJobID(appID, runID) = appID + "_run-" + runID`; `ScheduleRunVariablePath(jobID) =
  BackupVariablePath(jobID)`.
- `ScheduleRunJob(appID string, svc app.Service, image string, command []string, memoryMB int,
  s Settings, private map[string]bool, names bool) *api.Job`: `batchJob` + `batchGroup`, group
  and task named after the service, docker `image` = the pinned digest, `entrypoint = command[:1]`,
  `args = command[1:]`, `Env` = the service's non-secret env, `Templates: [variableTemplate(jobID)]`
  (secrets and registry credentials as env), resources from `memoryMB`, `KillTimeout` 10 s,
  `Logs{MaxFiles 2, MaxFileSizeMB 1}`, `Meta` `caravana.app/schedule/run`, no `Services`, no
  ports, the `names` sidecar when the app shares names; `withPullAuth` as backups. A reflection
  test keeps `HCL` honest is not needed (no export).

### `deploy` (`internal/deploy/schedules.go`)

- `StartScheduleRun(ctx, job *api.Job, secrets map[string]string) error`: write the run's
  variable (the secret set plus registry credentials, `withPullCredentials`), then `Register`.
- `ScheduleRunState(ctx, jobID) (RunState, error)`: `Jobs().Info` (404 → `ErrRunGone`), then
  `Jobs().Allocations(jobID, true)`: the newest allocation's `ClientStatus`, the service task's
  `StartedAt`, its newest `Terminated` event's `exit_code` and `oom_killed`, a `Driver Failure` /
  `Setup Failure` event's presence, `DesiredStatus`.
- `ScheduleRunOutput(ctx, allocID, task string) (stdout, stderr []byte, cutOut, cutErr bool, err
  error)`: `Logs` tail 64 KiB per stream; `Logs` gains the "cut" return (R2).
- `StopScheduleRun(ctx, jobID)`: `Deregister(purge=false)`. `PurgeScheduleRun(ctx, jobID)`:
  `Deregister(purge=true)` then delete the variable (`purgeJob` + `DeleteVariable`).
- `StopApp` also deregisters the app's in-flight run jobs (the service passes their IDs).
- deploytest: `FinishRun(jobID, exitCode)`, `FailRunStart(jobID)`, scripted `Logs` per job,
  `Deregister` recorded, `RunState` answered from the fake's job table.

### `store` (`internal/store/schedules.go`, migration `0017_schedules.sql`)

- Tables per [data-model.md](./data-model.md). `DeleteApp` deletes both; `DeleteSchedule` deletes
  the runs. `PruneScheduleRuns(scheduleID, keep=100)` after each settle.
- `LatestScheduleRuns(ctx)`: the newest ended run per schedule that is not `skipped` and not
  `stopped` (R5), and `SkipStreak(scheduleID)` (the newest runs while `skipped`, capped at 3).

### `service` (`internal/service/schedules.go`, `schedulewatch.go`)

- CRUD: `CreateSchedule`, `UpdateSchedule`, `DeleteSchedule`, `PauseSchedule`,
  `ResumeSchedule`, `GetSchedule`, `ListSchedules(filter)` (stored rows plus the built-in backup
  rows, sorted as the contract says, each with `NextRunAt` from `cronwords.Next` and `LastRun`),
  `ListScheduleRuns`, `GetScheduleRun`, `GetScheduleRunOutput`. Validation: name `app.ValidName`
  and unique in the app; the command as `RunCommand` validates it (1–64 items, 4 KiB, no NUL);
  cron via `cronwords.Validate`; `stopAfterSeconds` in {300, 1800, 3600, 7200}; `memoryMB` in
  {256, 512, 1024, 2048}; the service exists and has no volumes.
- `RunScheduleNow(ctx, id, trigger)`: refuses `ErrAppStopped`, `ErrScheduleBuiltin`,
  `ErrRunGoing` when `skipIfRunning` and a run is going (409 `run_going`), `ErrNoLiveDeployment`
  (409); else `startRun(trigger owner, slot nil)`.
- `startRun`: insert the row `starting` (write-ahead, R6), read the newest healthy deployment's
  digest for the service and the service's current secret set, build the job, `StartScheduleRun`,
  flip to `running`; on a refused register: `could_not_start` with the reason in Caravana's words.
- `StopScheduleRun(ctx, runID)`: write `stop_reason = owner` and `stop_requested_at`, then
  `StopScheduleRun`; the watcher settles it as `stopped`.
- **Clock** (`runScheduleClock`, every `scheduleScanGap` = 10 s, settable for the lab): for each
  unpaused stored schedule, `dueSlot` generalised (base `max(armed_at, newest slot_at)`, catch-up
  1 h, UTC): a due slot starts a run (trigger `timer`, `slot_at` set) unless: the app is stopped,
  undefined, has no healthy deployment, or its newest deployment is pending or running (the slot
  is retried next pass, nothing recorded, as the volume scheduler does); or `skipIfRunning` and a
  run is going → a `skipped` row for the slot; or 3 runs are going → `skipped`, "3 runs already
  going". `armed_at` is set at create, resume and when the cron changes (FR-008).
- **Watcher** (`watchScheduleRuns`, every 5 s, the `watchBackups` shape): for each `starting` or
  `running` row, `ScheduleRunState`; terminal → capture output (masked), settle by the outcome
  table, purge the job, publish `ChangeSchedules` and `ChangeAttention`; running → check the
  limit (from the task's own start) and the 5-minute start deadline, writing the intent then
  deregistering; `ErrRunGone` → `failed`, "lost while Caravana was restarting". The same pass is
  the restart recovery (FR-018). Order in `Start`: watcher first, then the clock.
- **Masking**: `maskSecrets(out []byte, values []string) []byte` replaces each secret value of 6+
  bytes with `[secret <NAME>]` before storing (R2).
- **Attention**: `AttentionScheduleFailed` from `LatestScheduleRuns` (the newest ended run is
  `failed`, `stopped_at_limit` or `could_not_start` and the schedule exists; key
  `schedule_failed|app|service|scheduleID`), `Detail` the state sentence of the contract's table;
  `AttentionScheduleSkipping` (warning) when the skip streak is 3. The notifier words them
  (`problemEvent`, `problemPath` → `/schedules#schedule-<id>`); `notify.Event` gains `schedule`
  and `exitCode`. Neither is a copy kind.
- Live: `ChangeSchedules` notice (names only) on start, settle and skip; the app's stream
  forwards nothing new (the Configure panel is static).
- `Service.StopApp` passes the app's running run job IDs to `deploy.StopApp`; those runs settle
  as `stopped` ("the app was stopped").

### API 0.24.0 (`internal/api/schedules.go`)

Per [contracts/api.md](./contracts/api.md): `listSchedules`, `createSchedule`, `getSchedule`,
`updateSchedule`, `deleteSchedule`, `runSchedule`, `pauseSchedule`, `resumeSchedule`,
`listScheduleRuns`, `getScheduleRun`, `stopScheduleRun`, `getScheduleRunOutput`; schemas
`Schedule`, `NewSchedule`, `ScheduleRun`; codes `builtin_schedule`, `run_going`, `run_not_running`,
`output_not_kept`, `schedule_exists`; `AttentionItem.kind` gains `schedule_failed` and
`schedule_skipping` with `schedule`/`scheduleId`; `NotificationEvent` gains `schedule` and
`exitCode`. Deploy tokens: denied by default (R7); the contract test walks the new routes.

### Console (`internal/ui/schedules.go`, `schedulesform.go`, `templates/schedules.html`)

Per the page contract with the time ruling applied: `/schedules` (the list, the detail per
`<details>`, the filter, the count line, the footer), `/schedules/new` and `/schedules/{id}/edit`
(the form with the presets as links, the Check submit and the preview box in UTC),
`/schedules/{id}/runs/{runId}` (the run's page, `no-store`), the posts (`run`, `stop`, `pause`,
`resume`, `delete`), the sidebar's Schedules item (`clock` icon), the Configure "Schedules"
panel, the live region `/parts/schedules` on the `schedules` notice, the needs-you wording, the
Backups tab's times as `<time>` elements too. `localtime.js` and the `<time>` helper in
`view.go`. Styles in `style/input.css` ("Page 5" section), `tools/ui-css.sh build`.

### Lab

- `TestConsoleLab` step `console-schedules` (`console_schedules_test.go`): a busybox app with
  `labResources`; a schedule created through the form from words, Run now (ok, the run's page
  shows the sentinel, no sentinel in any list, item, delivery, log line or notice), a schedule
  that exits 3 (`run now`; the needs-you item and the `problem.opened` delivery with
  `kind: schedule_failed`; then a fixed run resolves it), a 5-minute limit shortened for the lab
  through `Service` setters with `sleep 180` (stopped at the limit), Pause and Resume, Stop this
  run, a secret value echoed and absent from the SQLite file, a built-in volume-backup row, Delete.
  The one clock-bound check: an "every minute" schedule seen to run on its own within 70 s.
- Gate step `schedules` (`gate_schedules_test.go`): the same through the API with the gate's
  token, a deploy token refused on every schedule route, and `RestartCaravana` during a
  `sleep 20` run (the run ends `ok`, not lost). `host.sh prove` once before the release.

## Complexity Tracking

None. The one new package (`cronwords`) is pure and has two callers.
