# API contract: Schedules (022), openapi 0.24.0

Every route needs a full API token or a session; deploy tokens are refused with 403
`token_scope` (default deny). Every time is RFC 3339 in UTC. Errors are the usual
`{"error": {"code", "message", "field"}}`.

## Operations

| Operation | Route | Answer |
| --- | --- | --- |
| `listSchedules` | `GET /v1/schedules?app=&project=` | 200 `{schedules: [Schedule]}`, built-in backup rows included (`builtin: true`), sorted failed, running, next run, paused |
| `createSchedule` | `POST /v1/schedules` body `NewSchedule` | 201 `Schedule`, `Location: /v1/schedules/{id}` |
| `getSchedule` | `GET /v1/schedules/{scheduleId}` | 200 `Schedule` |
| `updateSchedule` | `PUT /v1/schedules/{scheduleId}` body `NewSchedule` (whole) | 200 `Schedule`; a cron change re-arms the clock |
| `deleteSchedule` | `DELETE /v1/schedules/{scheduleId}` | 204; a run in progress is stopped (`stop_reason: schedule_removed`) |
| `runSchedule` | `POST /v1/schedules/{scheduleId}/run` | 202 `ScheduleRun` (`status: running` or `starting`) |
| `pauseSchedule` | `POST /v1/schedules/{scheduleId}/pause` | 200 `Schedule` |
| `resumeSchedule` | `POST /v1/schedules/{scheduleId}/resume` | 200 `Schedule` (`nextRunAt` set) |
| `listScheduleRuns` | `GET /v1/schedules/{scheduleId}/runs?limit=` | 200 `{runs: [ScheduleRun]}` newest first, 14 by default, 100 at most; a built-in's runs come from backups |
| `getScheduleRun` | `GET /v1/schedules/{scheduleId}/runs/{runId}` | 200 `ScheduleRun` |
| `stopScheduleRun` | `POST /v1/schedules/{scheduleId}/runs/{runId}/stop` | 202 `ScheduleRun` (the stop is asked; the run settles `stopped` within the grace) |
| `getScheduleRunOutput` | `GET /v1/schedules/{scheduleId}/runs/{runId}/output?stream=stdout\|stderr` | 200 `text/plain; charset=utf-8`, `Cache-Control: no-store`, headers `Caravana-Output-Cut: true\|false`; 404 `output_not_kept` |

## Schemas

```yaml
NewSchedule:
  required: [app, name, command, cron]
  properties:
    app: {type: string}                   # the app ID
    service: {type: string}               # default the app's first service
    name: {type: string, maxLength: 31}   # app.ValidName's alphabet; unique in the app
    command: {type: array, items: {type: string}, minItems: 1, maxItems: 64}  # program first, no shell implied
    cron: {type: string}                  # five fields, UTC; fires at most once a minute
    stopAfterSeconds: {type: integer, enum: [300, 1800, 3600, 7200], default: 1800}
    memoryMB: {type: integer, enum: [256, 512, 1024, 2048]}   # default: the service's reservation rounded up
    skipIfRunning: {type: boolean, default: true}             # false: overlap allowed, at most 3 at once

Schedule:
  allOf: [NewSchedule] plus
    id: {type: string}                    # 12 hex, or backup:<app>:<service> for a built-in
    words: {type: string}                 # "Every day at 03:00 UTC"
    paused: {type: boolean}
    pausedAt: {type: string, format: date-time, nullable: true}
    builtin: {type: boolean}              # a service's backup policy; read-only here
    nextRunAt: {type: string, format: date-time, nullable: true}   # null when paused
    lastRun: {$ref: ScheduleRun, nullable: true}                   # newest run that isn't skipped
    runningNow: {type: integer}           # runs in progress
    usualSeconds: {type: integer, nullable: true}                  # median of the last 14 ok runs
    project: {type: string}
    environment: {type: string}
    appName: {type: string}

ScheduleRun:
  properties:
    id: {type: string}
    scheduleId: {type: string}
    trigger: {type: string, enum: [timer, owner]}
    slotAt: {type: string, format: date-time, nullable: true}
    status: {type: string, enum: [starting, running, ok, failed, stopped_at_limit, stopped, skipped, could_not_start]}
    exitCode: {type: integer, nullable: true}
    error: {type: string}                 # Caravana's words, empty when ok; never the output
    startedAt: {type: string, format: date-time}
    taskStartedAt: {type: string, format: date-time, nullable: true}
    finishedAt: {type: string, format: date-time, nullable: true}
    durationSeconds: {type: integer, nullable: true}   # task start to finish
    command: {type: array, items: {type: string}}
    deploymentId: {type: string, nullable: true}
    jobVersion: {type: integer, nullable: true}
    outputKept: {type: boolean}
    stdoutBytes: {type: integer}
    stderrBytes: {type: integer}
    stdoutCut: {type: boolean}
    stderrCut: {type: boolean}
```

Output is never in `ScheduleRun`; it is only `getScheduleRunOutput`'s body.

## Codes

| Status | Code | When |
| --- | --- | --- |
| 404 | `schedule_not_found`, `run_not_found` | |
| 404 | `output_not_kept` | the run never ran, the read failed, or a built-in's run |
| 409 | `builtin_schedule` | update, delete, run, pause, resume or stop on a `backup:…` id |
| 409 | `schedule_exists` | `createSchedule` with a name the app already has |
| 409 | `run_going` | `runSchedule` while a run is going and `skipIfRunning` is true, or 3 runs are going |
| 409 | `run_not_running` | `stopScheduleRun` on a run that has ended |
| 409 | `app_stopped` | `runSchedule` on a stopped app (existing code) |
| 409 | `no_live_deployment` | `runSchedule` on an app without a healthy version (existing code) |
| 422 | `invalid_field` | `name`, `command`, `cron` ("fires more than once a minute", "never fires"), `service` ("the service mounts a volume; a schedule needs a service without volumes"; unknown), `stopAfterSeconds`, `memoryMB`, `app` (not defined) |
| 404 | `app_not_found` | `createSchedule` with an unknown app |

## Existing schemas that change

- `AttentionItem.kind` gains `schedule_failed` and `schedule_skipping`; the item gains
  `schedule` (name) and `scheduleId`.
- `NotificationEvent` (`problem.opened` / `problem.resolved`): `kind` may be `schedule_failed` or
  `schedule_skipping`; the event gains `schedule` and `exitCode` (nullable). No new event type.
- `AppSummary`/`AppRecord`: no change. `GET /v1/health`: no change (no timezone).

## Console routes (parity with the API, same service methods)

`GET /schedules`, `GET /parts/schedules`, `GET /schedules/new`, `POST /schedules`
(`action=check|create`), `GET /schedules/{id}/edit`, `POST /schedules/{id}` (`action=check|save`),
`POST /schedules/{id}/run|pause|resume|delete`, `GET /schedules/{id}/runs/{runId}`,
`POST /schedules/{id}/runs/{runId}/stop`. Deploy tokens never reach the console.
