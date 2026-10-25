# API contract: Logs (024), openapi 0.26.0

Every route needs a full API token or a session; deploy tokens are refused with 403
`token_scope` (default deny). Every answer is `Cache-Control: no-store` (log text may hold
anything). Times are RFC 3339 UTC. `text` is the line with its control sequences removed
(`stripANSI`) and the level read as the console reads it (`levelOf`); both rules are written out
in `listLogs`' description so a client can match the page.

## Operations

| Operation | Route | Answer |
| --- | --- | --- |
| `listLogs` | `GET /v1/logs?project&environment&app&service&copy&stream&levels&q&from&to&range&before&lines` | 200 `LogRead`, lines newest first |
| `getLogsAround` | `GET /v1/logs/around?copy=&at=<stream>.<file>.<offset>&lines=50` | 200 `LogRead` with `anchor` (every level, that copy only, both streams); 404 `not_found` (the copy is gone), 404 `log_rotated` (the file was purged) |
| `downloadLogs` | `GET /v1/logs/download?<listLogs' query>` | 200 `text/plain; charset=utf-8`, `Content-Disposition: attachment; filename="caravana-logs-<project>-<environment>-<from>-<to>.txt"`, oldest first; 409 `download_busy` |
| `getLogs` (existing) | `GET /v1/apps/{id}/logs?service&copy&stream&lines&follow` | unchanged order (oldest first); each `LogLine` gains `id`, `level`, `timeSource`; `text` is stripped; `time` is set on tail lines read from the line |

Parameters of `listLogs`:

| Name | Rule |
| --- | --- |
| `project`, `environment` | required unless the installation has only Default › production; unknown → 404 `project_not_found` / `environment_not_found` (field `project` / `environment`) |
| `app` | an app id of that place; unknown → 404 `app_not_found` |
| `service` | `<app>/<service>`; unknown → 422 `invalid_field` |
| `copy` | an allocation id or its first 8; unknown → 404 `not_found` |
| `stream` | `stdout`, `stderr`, `both` (default) |
| `levels` | comma list of `error`, `warn`, `info`, `debug`; default `error,warn,info`; other → 422 |
| `q` | ≤ 200 bytes, case-insensitive substring; longer → 422 |
| `from`, `to` | RFC 3339; `to` defaults to now; `from` after `to` → 422; or `range` in `15m`, `1h`, `24h`, `7d` (default `15m`); both given → 422 |
| `before` | RFC 3339, "Show older": the read starts from this time |
| `lines` | 1–1000 rows after folding, default 500 |

## Schemas

```yaml
LogEntry:
  id: {type: string}                     # <copy>.<stream>.<file>.<offset>
  time: {type: string, format: date-time}
  timeSource: {enum: [line, estimated, received]}
  app, service, copy: {type: string}     # copy is the short id
  stream: {enum: [stdout, stderr]}
  level: {enum: [error, warn, info, debug]}
  text: {type: string}                   # control sequences removed
  raw: {type: string}                    # only when the line was JSON
  repeat: {type: integer}                # N-1 for a folded row of N

LogDeploy:
  deploymentId, app: {type: string}
  service: {type: string}                # absent for the whole app
  at: {type: string, format: date-time}
  kind: {enum: [started, live, failed, reverted, superseded]}
  jobVersion: {type: integer}
  revertedTo: {type: integer}            # for reverted
  who: {type: string}                    # page 4's words: "pushed to main by ana", "from the console", "through the API by ci-token", "by the webhook", "promoted from staging"

LogBin:
  from: {type: string, format: date-time}
  count, errors, estimated: {type: integer}
  read: {type: boolean}                  # false: wholly before the coverage, not zero

LogRead:
  lines: {type: array, items: LogEntry}
  deploys: {type: array, items: LogDeploy}
  histogram: {from, to: date-time, bucketSeconds: integer, buckets: [LogBin]}
  coverage: {from: date-time, complete: boolean}
  copies: {type: array, items: {id, short, app, service: string, gone: boolean, lines: integer}}
  dropped: {type: boolean}               # a stream's oldest line read is the oldest Nomad keeps
  anchor: {type: string}                 # getLogsAround only: the id of the line asked for

LogLine (existing, per app):            # gains
  id: {type: string}
  level: {enum: [error, warn, info, debug]}
  timeSource: {enum: [line, estimated, received]}
```

The download's body:

```
# newest last
# caravana logs shop/production 2026-10-25T00:18:21Z to 2026-10-25T00:33:21Z, read from 2026-10-25T00:21:04Z (service web, levels error,warn,info)
2026-10-25T00:21:04.118Z	shop--production--web/web	9a4d13ff	stdout	info	Starting up on port 80
2026-10-25T00:21:05.000Z~	shop--production--web/web	9a4d13ff	stderr	error	    at com.example.Foo.bar(Foo.java:42)
# deploy 2026-10-25T00:30:12Z web v24 started rolling out · pushed to main by ana · 4e0a77
# cut: the budget ended at 2026-10-25T00:33:00Z; newer lines of copy 9a4d13ff weren't included
```

## Codes

| Code | Status | When |
| --- | --- | --- |
| `project_not_found`, `environment_not_found` | 404 | the place doesn't exist |
| `app_not_found`, `not_found` | 404 | the app or copy isn't there |
| `log_rotated` | 404 | the file a line id names was purged by Nomad |
| `invalid_field` | 422 | `service`, `levels`, `q`, `from`, `to`, `range`, `before`, `lines`, `at` |
| `download_busy` | 409 | a download is already running |
| `nomad_unavailable` | 502 | as today |
| `token_scope` | 403 | a deploy token on any of these routes |
