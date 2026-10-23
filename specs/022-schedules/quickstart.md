# Quickstart: Schedules (022)

How to see the feature work end to end. Values below are examples; the lab's tests do the same
through `TestConsoleLab` (`console-schedules`) and the gate (`schedules`).

## Prerequisites

- The lab up (`deploy/dev/lab.sh up`, `eval "$(deploy/dev/lab.sh env)"`), Caravana running
  (`go run ./cmd/caravana`), the admin created, a full API token in `$TOKEN`.
- A small app to run in: a busybox service with `labResources` (16/32 MB), no volumes, deployed
  and healthy, say `clock` with service `web` (`sh -c 'exec httpd -f -p 8080'` with a health port).

## 1. A schedule that works

```bash
curl -s -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' localhost:7070/v1/schedules \
  -d '{"app":"clock","name":"hello","command":["sh","-c","echo hello from $(hostname); exit 0"],"cron":"* * * * *"}'
```

Expected: 201 with `words: "Every minute"`, `nextRunAt` within the next minute (UTC). Within
about 70 s a run appears: `GET /v1/schedules/{id}/runs` has one `ok` run with `exitCode: 0`;
`GET …/runs/{runId}/output` returns `hello from …`. On `/schedules` the row shows ● and "Last
run … took 1 s"; the mark links to the run's page, which shows the output. The app's own copy
was not restarted (`/apps/clock` Running tab, no new copy ID).

## 2. Run now, and a failure that reaches you

```bash
curl -s -X POST -H "Authorization: Bearer $TOKEN" localhost:7070/v1/schedules/$ID/run
```

202 at once. Then a schedule that fails:

```bash
… -d '{"app":"clock","name":"boom","command":["sh","-c","echo boom; exit 3"],"cron":"0 3 * * *"}'
curl -s -X POST … /v1/schedules/$BOOM/run
```

Within 10 s the run is `failed`, `exitCode: 3`, `error: "exit 3"`. `/apps` shows the needs-you
item "clock: boom failed (exit 3)", `GET /v1/attention` has `kind: schedule_failed` with
`schedule: "boom"`, and a notification channel subscribed to problems gets one `problem.opened`
with `kind: "schedule_failed"` and `exitCode: 3` (not one per run: run it again, nothing new is
sent). A fixed `boom` (`exit 0`, Run now) clears the item and sends `problem.resolved` after
`CARAVANA_NOTIFY_RESOLVE_AFTER`.

## 3. The limit, Stop this run, Pause

- `{"name":"slow","command":["sleep","180"],"cron":"0 4 * * *","stopAfterSeconds":300}` then Run
  now: after 5 minutes (the lab shortens it) the run is `stopped_at_limit`, "Stopped at the 5 min
  limit." in the detail, a needs-you item.
- Run `slow` again and `POST …/runs/{runId}/stop`: within 30 s it is `stopped`, no item, nothing
  sent.
- `POST …/pause`: `paused: true`, `nextRunAt: null`, the row says "paused", its slot doesn't run;
  `POST …/resume`: `nextRunAt` is the next slot, the missed ones are not made up.
- With `skipIfRunning` (default) and a run going, Run now is 409 `run_going`, and a timer slot
  becomes a `skipped` row (◆ in the strip). Three in a row: a warning item "has been skipped 3
  times".

## 4. Secrets stay secret

Give `clock` a secret `TOKEN=s3cret-sentinel-022` and a schedule
`["sh","-c","echo $TOKEN"]`. Run now. The run's output reads `[secret TOKEN]`; the SQLite file,
the logs, the deliveries, the attention items and every list answer contain no
`s3cret-sentinel-022`. The run's variable `nomad/jobs/clock_run-<id>` is gone after the run.

## 5. Built-in backups and the Configure panel

An app with a backup policy (`db`, Postgres): `GET /v1/schedules` lists `backup:db:postgres`
with `builtin: true`, `words: "Every day at 03:00 UTC"`, its last runs from Backups; `PUT`,
`DELETE`, `run`, `pause` answer 409 `builtin_schedule`. On `/schedules` the row has "Change in
Backups" and no Pause/Edit/Delete. `/apps/clock/configure` has the Schedules panel listing
`hello · Every minute` with Manage and New links.

## 6. Times

Every clock time on `/schedules`, a run's page and `/apps/db/backups` reads "03:00 UTC"; with
JavaScript on, " · 05:00 local" follows it (a Madrid browser in summer) and the lede gains "Your
local time is shown beside them."; with scripts off, the UTC text alone. The API has no
timezone field.

## 7. A restart during a run

Start `sleep 20` with Run now, restart Caravana (the gate's `RestartCaravana`), wait: the same run
ends `ok`, not "lost". Stop Nomad's job by hand during a run instead: the run ends `failed`,
"stopped outside Caravana".

## Checks before calling it done

`go build ./...`, `go vet ./...`, `go tool staticcheck ./...`, `gofmt -l .`, the arm64 CGO-free
build, `go mod tidy -diff`, `tools/ui-css.sh check`, `tools/ui-vendor.sh check`,
`go test ./...`, `go test -race` on ui/service/store, `CARAVANA_LAB=1 go test ./cmd/caravana -run
TestConsoleLab`, and `host.sh prove` once before the release.
