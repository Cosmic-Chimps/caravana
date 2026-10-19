# Implementation Plan: App actions and Run a command

**Branch**: `020.5-app-actions` (stacked on `019.5-create-without-deploy`) | **Date**: 2026-10-18 |
**Spec**: [app-actions.spec.md](./app-actions.spec.md)

## Summary

Four actions per app, from the console and the API: Run a command in a running copy (Nomad alloc
exec, output shown once and never stored), Stop (deregister without purge, app and its backup
periodic jobs), Start (register the stopped job again with `Stop=false`), Restart (copy by copy,
waiting for each). A stopped app refuses every deploy (409 `app_stopped`) and its scheduled
backups skip. Every action is recorded in `app_actions` (migration 0012) without any output.
API 0.18.0. Research R1–R6 hold the decisions.

## Technical Context

Go 1.27, no new module (the Nomad client's `Allocations().Exec` is in the module already). Storage:
migration `0012_app_actions.sql` (research R6). Nomad: the scoped policy gains `alloc-exec`.
Testing: deploytest fakes gain exec/stop/start/restart; service, API and UI tests; a gate step and
a console step in the lab.

## Constitution Check

| Principle | Assessment |
| --- | --- |
| I. Small | PASS. No module, no service. |
| II. One Model | PASS. `app.App` and `Translate` untouched; Start re-registers Nomad's own stored job. |
| III. Safe Deploys | PASS. Start runs the same job definition and digests (no tag resolution). Stop and Restart aren't deploys and record no deployment. A stopped app refuses deploys loudly. |
| IV. Fail Loudly | PASS. `app_stopped`, `app_busy`, `copy_not_running`, timeouts and truncation all said; nothing dropped silently. |
| V. API-First | PASS. Five operations in openapi 0.18.0 with the console in the same commits; parity entries. |
| VI. Nomad-Native | PASS. Official client: `Deregister`, `Info`+`Register`, `RestartAllTasks`, `Exec`. Nomad code stays in `deploy`. |
| VII. Secure | PASS with care: `alloc-exec` lets Caravana's token run commands in app copies, which is the feature; it's reachable only by the signed-in admin or an API token. Output never logged or stored (R2), `Cache-Control: no-store`, sentinel tests. |
| VIII. Plain Go | PASS. Pure shell-words parser and capped writer, table tests. |

## Design

### deploy (`internal/deploy/actions.go`)
- `StopApp(ctx, appID, backupJobIDs []string)`: `Deregister(purge=false)` for the app's job and each
  backup periodic job that exists. `StartApp(ctx, appID, backupJobIDs)`: `Info`, `Stop=false`,
  `Register`, for each.
- `RestartCopy(ctx, copyID)`: `RestartAllTasks` (already used by volume runs; reuse).
- `Exec(ctx, copyID, task string, command []string, limit int) (ExecResult, error)`:
  `ExecResult{ExitCode *int, Stdout, Stderr []byte, StdoutTruncated, StderrTruncated, TimedOut bool}`;
  the caller's context carries the 60 s timeout. `ErrCopyNotRunning`.
- deploytest gains these with scripted results.

### store (`internal/store/actions.go`, migration 0012)
- `SetAppStopped(ctx, appID, at *time.Time)`; `AppRecord.StoppedAt *time.Time`.
- `InsertAction`, `FinishAction`, `ListActions(appID, limit)`, `FailRunningActions(msg)` at start;
  actions deleted with the app.

### service (`internal/service/actions.go`)
- `RunCommand(ctx, appID, copyID string, command []string, trigger) (CommandResult, error)`:
  validates the command (1–64 args, ≤ 4 KiB each, no NUL, first word non-empty), finds the copy
  among the app's copies (404 otherwise; `copy_not_running` unless running), the task = the copy's
  service, 60 s timeout, 64 KiB per stream; records the action (program = first word only; exit
  code; `failed` with a short error when it couldn't start or timed out — never output); publishes
  "actions changed" (no content).
- `StopApp`, `StartApp(ctx, appID, trigger)`: `TryLock` the app's lock (409 `ErrAppBusy`), require
  a deployed app (`ErrNoLiveDeployment` otherwise); Stop of a stopped app is 409 `app_stopped` and
  Start of a running one 409 `app_not_stopped`; backup job IDs from the app's
  services with a postgres backup, record the action, `SetAppStopped`, publish "app changed".
- `RestartApp(ctx, appID, trigger)`: refuses when stopped or busy; records the action `running`
  and returns; a goroutine (tracked like other watchers, the lock held) restarts each running copy
  of the live version in turn and waits up to 2 min for it to be running (and healthy when the
  service has a health check), then finishes the action `succeeded`, or `failed` naming the copy.
- `ErrAppStopped` checked in `saveAndDeploy`, `Redeploy`, `RedeployVersion`, `Promote` (target),
  `BackUpNow`, `Restore`; the volume-backup scheduler skips stopped apps.
- `statusOf`: a stopped app's state is `stopped`; Attention ignores the copy kinds of stopped apps.

### API 0.18.0
- `runCommand` `POST /v1/apps/{id}/copies/{copyId}/run` `{command: [..]}` → 200
  `{exitCode?, stdout, stderr, stdoutTruncated, stderrTruncated, timedOut}`, `Cache-Control:
  no-store`.
- `stopApp`, `startApp` `POST /v1/apps/{id}/stop|start` → 200 `AppSummary`.
- `restartApp` `POST /v1/apps/{id}/restart` → 202 `AppAction`.
- `listAppActions` `GET /v1/apps/{id}/actions` → 200 `[AppAction]` (last 20).
- `AppStatus.state` gains `stopped`; `AppRecord`/`AppSummary` gain `stoppedAt`.
- Codes: 409 `app_stopped`, 409 `app_not_stopped`, 409 `app_busy`, 409 `copy_not_running`,
  422 `invalid_field` (`command`).

### Console
- App header actions (for a deployed app): Restart (POST), Stop (GET `/apps/{id}/stop` confirm page
  with what stops and what waits → POST), Start for a stopped app (POST). Notices after each.
- Running tab: per running copy, a "Run a command" form (one line; help text on quoting and
  `sh -c`); `POST /apps/{id}/copies/{copyId}/run` answers 200 with the Running tab plus a result
  panel (exit code, stdout, stderr in `<pre>`, truncated/timed-out notes, "This output is shown
  once; it isn't kept."), `Cache-Control: no-store`. The tab lists the last 10 actions.
- Stopped app: ○ stopped pill in lists and header; the deploy form, Configure save, secrets and
  backups actions say the app is stopped and offer Start.
- Pure `splitCommand(line) ([]string, error)` (quotes, backslash) with table tests.

### Lab
- The scoped policy `deploy/install/caravana-policy.hcl` gains `alloc-exec` (TestLab applies it, so a
  missing capability fails in the lab).
- Gate step `app-actions`: deploy a two-copy whoami with small reservations; run `echo
  <sentinel>` in a copy (stdout has it, exit 0; `listAppActions` has `echo` without it; the
  sentinel is in no later API answer); `sh -c 'exit 3'` → 3; restart while polling the domain (no
  failed request, action succeeded, no needs-you item); stop (domain 404 within 30 s, webhook and
  deploy 409 `app_stopped`, state stopped); start (domain 200, same job definition and digests as
  before the stop); delete.
- Console step `console-app-actions`: the run form with a sentinel (shown on the answer, absent
  from a reload), Stop confirm page and Stop, Start, Restart notice.

## Complexity Tracking

None.
