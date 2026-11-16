# Feature 040: a schedule that runs in the running copy (`target: copy`)

**Status**: implemented and reviewed on the worktree branch `alta/kind-chinchilla-zkvg` | **Created**: 2026-11-16 | **Size**: small
to medium (one spec file: spec, plan and tasks together, as 039)

**Input**: CodeAlta task `5f39` ("04 · Schedule a command in the running copy"), suggested by the Umbral session on
2026-11-16 and welcomed by the maintainer.

## Why

Run a command (020.5) runs a command in a running copy, on demand only. Schedules (022) run a command on a timer in
a fresh copy, and refuse a service with volumes (two writers on one volume would be unsafe). So nothing runs a
maintenance command on a timer against a service's live data: a cleanup CLI, a cache purge, a nightly
`Umbral clear-users` for the public demo.

## What

**User story (P1).** As an owner, I schedule a command to run, on a timer, inside the copy of a service that is
running now (a service with volumes too), and I see each run's exit code and why it failed, never its output.

- **FR-001** A schedule has `target`: `fresh` (the default, 022 unchanged) or `copy`. A `copy` schedule is allowed
  for any service, a service with volumes included (the point of it); `fresh` still refuses a service with volumes.
- **FR-002** A `copy` run picks the service's oldest healthy copy (Nomad's create time, then the copy ID). With
  `allCopies: true` it runs in every healthy copy of the service, one after the other, oldest first, as one run.
  `allCopies` with `fresh` is refused naming `allCopies`.
- **FR-003** The run follows 020.5's rules: Nomad alloc exec in the service's task, the program and its
  arguments (no shell unless the command is `sh -c …`), no terminal, no input, each stream cut at 64 KiB. Its
  limit is `stopAfterSeconds` from 60, 120 or 300 (0 means 60); a `copy` schedule with another limit is refused
  naming `stopAfterSeconds`. `memoryMB` doesn't apply (the copy's own memory): 0 is stored and read back, any other
  value is refused naming `memoryMB`.
- **FR-004** The output is **not kept** (020.5's research R2: it may hold anything). It is read and dropped: never
  in a row, a log line, an error, a notice, a page or an answer. `GET …/runs/{runId}/output` of a `copy` run is 404
  `output_not_kept`. What is kept: the schedule run (status, exit code, Caravana's words, the copies it ran in,
  the trigger) and, per copy, an `app_actions` row `run` with trigger `schedule`, the first word of the command,
  the copy, the service and the exit code or the reason.
- **FR-005** The clock is 022's: five-field cron in UTC, one row per slot. A `copy` schedule always skips a slot
  while its run is going (never two runs of one schedule against the same copy at once): `skipIfRunning: false`
  with `copy` is refused naming `skipIfRunning`. Run a command by hand, or a terminal, at the same time is allowed.
- **FR-006** A slot while the app is **stopped** is a `skipped` row ("the app is stopped"). A slot while the app
  is being deployed, has no live version or has no healthy copy of the service waits, as 022's (tried again until
  it is an hour old). `schedule_skipping` is not raised for a stopped app (the owner stopped it). `fresh`
  schedules keep 022's rule (a stopped app's slot waits) unchanged.
- **FR-007** Outcome: exit 0 is `ok`; another exit is `failed` "exit N" (with `allCopies`, the first copy that
  failed: "exit 3 in copy 1a2b3c4d"; the others still run); the limit is `stopped_at_limit` "stopped after 1
  minute"; a command Nomad can't start is `could_not_start` "the command couldn't start in its copy"; a copy that
  stops or vanishes during the run, or Nomad unreachable, is `failed` with 020.5's words (`execFailure`); stop by
  the owner, the app stopped and the schedule removed are `stopped` with 022's words (the exec is cancelled).
  `schedule_failed` and its notification follow from the row as for 022.
- **FR-008** A `copy` run left open by a Caravana that stopped is settled `failed` "lost while Caravana was
  restarting" by the watcher's first pass (the exec went with the process).
- **FR-009** API (0.43.0): `target` and `allCopies` on `NewSchedule` and `Schedule`; `target` and `copies` (the
  copy IDs it ran in, `[]` for `fresh`) on `ScheduleRun`. Deploy tokens are denied by the default deny, as 022.
- **FR-010** Console: the schedule form has "Run in: a new copy / the running copy" (radio) and "In every copy"
  (checkbox), the Stop after select offers 1 min, 2 min and 5 min for the running copy (one select with every
  choice; a wrong one for the target is a field error at it), Memory says it is for a new copy only, and the
  service select lists services with volumes (refused at the service for a new copy). The list and the run page
  say where it runs; the run page shows the exit code, the reason and the copies, and says "This run was in the
  running copy: its output isn't kept." The app's Recent actions say "on a schedule". Forms work without
  JavaScript.

**Success criteria.** A `target: copy` schedule on a volume service runs its command in the live copy at the
slot; the record has the first word and the exit code and no output; an exit 3 opens `schedule_failed` and
notifies; a slot while the app is stopped is skipped and recorded; a run still going at the next slot skips it; a
sentinel the command prints appears in no row, log line, page or answer.

## Plan

Decisions (D1–D9):

- **D1 Store.** Migration `0031_schedule_target.sql`: `schedules.target TEXT NOT NULL DEFAULT 'fresh' CHECK
  (target IN ('fresh','copy'))`, `schedules.all_copies INTEGER NOT NULL DEFAULT 0`, `schedule_runs.target TEXT NOT
  NULL DEFAULT 'fresh' CHECK (…)` (a run keeps the target it ran with, since the schedule can be edited while it
  goes) and `schedule_runs.copies TEXT` (JSON array of copy IDs, NULL for fresh). No change to `app_actions`: its
  `trigger` has no CHECK; `schedule` is a new value. `ScheduleRecord.Target/AllCopies`,
  `ScheduleRunRecord.Target/Copies`.
- **D2 One row per slot.** `allCopies` is one run row over several copies, run one after the other: 022's
  overlap, skip streak, usual duration and `schedule_failed` stay one rule. Each copy gets its own `app_actions`
  row.
- **D3 Execution off the clock.** `startRun` branches on the target. A copy run: `runBlocker` (as 022), then pick
  the copies (`deployer.Copies`, the service's, `Status == healthy`, not `Terminal`, not `Stopping`, sorted by
  `CreatedAt` then ID; none is a blocker `ErrNoRunningCopy`, which waits like `ErrNoLiveDeployment`), then
  `claimRun` (row `starting` with `target` and `copies`; it stays in `s.starting` until settled, so the watcher
  leaves it alone), then the row is `running` and a goroutine runs the execs and settles it. Its context is
  `context.WithoutCancel` + a cancel func kept in a map `s.copyRuns[runID]`; Stop, stopAppRuns and
  DeleteSchedule write the stop intent first and then cancel it. `deployer.Exec`'s timeout is the run's limit
  (`runLimit`, so the lab's shortcut works). The output in `ExecResult` is dropped at once.
- **D4 Watcher.** `followScheduleRuns`: an open run with target `copy` not in `s.starting` was left by an ended
  process: settle `failed` "lost while Caravana was restarting" (no Nomad call, nothing to purge).
- **D5 Stopped app.** In `runDueSchedules`, a `copy` schedule whose app is stopped writes a skipped row for the
  slot (reason "the app is stopped") through `claimRun`'s skipped path, instead of waiting. `scheduleItems` gives
  no `schedule_skipping` for an app that is stopped.
- **D6 Checks.** `checkSchedule`: target (`""` → fresh), the volumes rule only for fresh, `allCopies` only for
  copy, `stopAfterSeconds` from {60, 120, 300} (0 → 60) for copy, `memoryMB` 0 for copy, `skipIfRunning` forced
  true and an explicit false refused for copy.
- **D7 Trigger.** `app.TriggerSchedule = "schedule"` for the `app_actions` rows; `triggerWords` and the activity
  feed say "on a schedule". Not a deploy trigger (`Deployment.Trigger` validation unchanged).
- **D8 Output.** `GetScheduleRunOutput` already answers `ErrOutputNotKept` for `output_kept = 0`; copy runs never
  set it. `RunCommand` is unchanged; the copy run calls `deployer.Exec` itself (shared helpers `programOf`,
  `execFailure`, `commandOutputLimit`).
- **D9 Lab.** Gate step `schedule-in-copy` (`gate_schedule_copy_test.go`): a busybox service with a volume; a
  `copy` schedule every minute whose command prints a sentinel, appends to a file on the volume and exits 3; its
  slot runs in the live copy (the file grew; the row has `copies`, exit 3, `failed`, no output; the output read is
  404); `schedule_failed` in `/v1/attention`; an `app_actions` row with trigger `schedule`; a second schedule
  every minute running `sleep 100` with a 120 s limit: its next slot while the first run goes is a skipped row;
  stop the app, a slot is a skipped row "the app is stopped"; the
  sentinel in no answer, page, log line or the SQLite file. Console step `console-schedule-in-copy`: the form with
  "the running copy" creates one, the run page says the output isn't kept.

Constitution: II (no new helper task or job: a copy run uses the service's own task), IV (every refused
combination names its field), V (API and console share `service`; openapi in the same change), VII (output never
kept or logged). No new module.

## Tasks

- [x] T1 Store: migration 0031, records, scans, tests.
- [x] T2 Service: checks (D6), copy runs (D3), stop/delete/app-stop cancel, watcher (D4), stopped-app skip (D5),
  trigger words (D7), table tests with deploytest's exec fake, including a sentinel search of rows, log and
  answers.
- [x] T3 API: fields, openapi 0.43.0, tests (deploy token still 403 on the schedule routes).
- [x] T4 Console: form, list, run page, Recent actions words; tests; `tools/ui-css.sh build` if styles change.
- [x] T5 Lab: gate step `schedule-in-copy`, console step `console-schedule-in-copy`; run both alone in the lab. Both passed
  on 2026-11-16: the gate step three times in a row (161 s, 175 s, 195 s) and the console step (27 s, run with
  `console-sign-in`). The lab stops every run at 40 s, so the gate step lifts that shortcut while it runs
  (`gateTarget.SetScheduleLimit`) to let `sleep 100` outlast a slot. One earlier gate run saw purge's first run end `ok`
  with exit code 0 where the command exits 3, and did not repeat in the next four; Nomad's exec reports the code
  (`allocations_exec.go` returns the frame's), so it looks like Docker's exec-inspect race, the same one Run a command has.
- [x] T6 Docs: `changelog.d/schedule-in-copy.md`, CLAUDE.md paragraph.
- [x] T7 CI checks: build, vet, staticcheck, gofmt, cross-compile, `go mod tidy -diff`, release check, `go test
  ./...` (all passed 2026-11-16; the schedule, store, api and ui tests also under `-race`).
