# Data model: Schedules (022)

Migration `internal/store/migrations/0017_schedules.sql`. Times are stored as Caravana stores
every time (RFC 3339 with nine fraction digits, UTC, sortable as text). Nothing is added to the
app document, so no app `revision` moves.

## `schedules`

| Column | Type | Notes |
| --- | --- | --- |
| `id` | TEXT PK | 12 lowercase hex (`newID`) |
| `app_id` | TEXT NOT NULL | FK `apps(id)`; deleted with the app |
| `service` | TEXT NOT NULL | the service whose image, env and secrets a run uses |
| `name` | TEXT NOT NULL | `app.ValidName`, ≤ 31 chars; `UNIQUE(app_id, name)` |
| `command` | TEXT NOT NULL | JSON array of strings (program first), as Run a command validates |
| `cron` | TEXT NOT NULL | five fields, UTC |
| `stop_after_s` | INTEGER NOT NULL | 300, 1800 (default), 3600 or 7200 |
| `memory_mb` | INTEGER NOT NULL | 256, 512, 1024 or 2048 |
| `skip_if_running` | INTEGER NOT NULL DEFAULT 1 | 1 skip the slot, 0 allow overlap (≤ 3 at once) |
| `paused_at` | TEXT | NULL when not paused |
| `armed_at` | TEXT NOT NULL | the clock's base: set at create, resume and a cron change (FR-008) |
| `created_at`, `updated_at` | TEXT NOT NULL | |

Built-in schedules (a service's backup policy) are **not** rows: `ListSchedules` derives them
from the app's services and the backups table, with id `backup:<app_id>:<service>`.

## `schedule_runs`

| Column | Type | Notes |
| --- | --- | --- |
| `id` | TEXT PK | 12 hex; the Nomad job ID is `<app_id>_run-<id>` |
| `schedule_id` | TEXT NOT NULL | FK `schedules(id)` ON DELETE CASCADE |
| `app_id`, `service` | TEXT NOT NULL | copied, so a run reads without a join |
| `trigger` | TEXT NOT NULL | `timer` or `owner` |
| `slot_at` | TEXT | the cron instant this run answers; NULL for an owner run |
| `command` | TEXT NOT NULL | JSON, as run (an edit doesn't rewrite history) |
| `deployment_id` | TEXT | the healthy deployment whose digest ran; `job_version` INTEGER with it |
| `status` | TEXT NOT NULL | `starting`, `running`, `ok`, `failed`, `stopped_at_limit`, `stopped`, `skipped`, `could_not_start` (a CHECK) |
| `exit_code` | INTEGER | from the task's `Terminated` event, when it ran |
| `error` | TEXT | Caravana's words for the failed kinds ("exit 3", "ran out of memory", "lost while Caravana was restarting", "didn't start within 5 minutes", "3 runs already going", …) |
| `stop_reason` | TEXT | `owner`, `limit`, `app_stopped`, `schedule_removed`; written before the deregister (R3) |
| `stop_requested_at` | TEXT | |
| `started_at` | TEXT NOT NULL | the row's insert (registration) |
| `task_started_at` | TEXT | the task's own start; the limit counts from here |
| `finished_at` | TEXT | |
| `stdout`, `stderr` | TEXT | the last 64 KiB of each, secret values masked; NULL when not kept |
| `stdout_cut`, `stderr_cut` | INTEGER NOT NULL DEFAULT 0 | |
| `output_kept` | INTEGER NOT NULL DEFAULT 0 | 0 when the read failed or the run never ran |

Indexes: `(schedule_id, started_at)`, `(app_id)`, and `(status)` for the watcher's non-final scan.
`PruneScheduleRuns` keeps the newest 100 per schedule.

### State transitions

```
starting ──register ok──▶ running ──exit 0──────────────▶ ok
   │                         │──exit n / OOM / lost ──▶ failed
   │                         │──limit (intent first) ─▶ stopped_at_limit
   │                         │──owner / app stopped ──▶ stopped
   │──register refused ──────┴──driver or setup failure, not started in 5 min ──▶ could_not_start
(a slot whose run is not started because one is going, or 3 are going) ──▶ skipped
```

`skipped` rows are written directly by the clock, with `slot_at`, `finished_at = started_at` and
`error` the reason; they are never `starting`.

## Derived, not stored

- `next_run_at`: `cronwords.Next(cron, max(now, armed_at), 1)`; NULL when paused.
- `last_run`: the newest row that is not `skipped` (the strip's marks include skipped rows; the
  "Last run" cell doesn't).
- `usual_s`: the median duration of the `ok` runs among the last 14 (the detail's "usually 48 s"
  and the running bar's width).
- The skip streak: how many of the newest rows, in order, are `skipped` (capped at 3).
- The needs-you items (`schedule_failed`, `schedule_skipping`) from `LatestScheduleRuns` and the
  streak, on every read.

## `sent_problems` (no change)

Keys `schedule_failed|<app>|<service>|<schedule id>` and
`schedule_skipping|<app>|<service>|<schedule id>` use the existing text key and the fourth
segment as the subject (R5).
