# API contract: Monitoring and capacity (025), openapi 0.27.0

Every route needs a full API token or a session; deploy tokens are refused with 403
`token_scope` (default deny, no entry in `scopeRules`). Nothing here is a secret, so the answers
carry no `no-store`. Times are RFC 3339 UTC. Numbers are in base units: requests per second,
percent, milliseconds, megabytes, MHz. A gap is `null`, never 0. Every value is computed by the
same service call the console makes, so a client can match the page. All three operations read
the stored samples: Nomad or Traefik being down never fails a read; `sources` says what the
sampler couldn't read.

## Operations

| Operation | Route | Answer |
| --- | --- | --- |
| `getMetrics` | `GET /v1/metrics?project&environment&range&sort` | 200 `Metrics` for the place's apps |
| `getAppMetrics` | `GET /v1/apps/{id}/metrics?range&sort` | 200 `Metrics` for one app's services: the series are the app's totals, the deploys the app's |
| `getHost` | `GET /v1/host` | 200 `Host` |
| `listAttention` (existing) | `GET /v1/attention` | items gain the kind `disk_nearly_full` and its facts |

Parameters:

| Name | Rule |
| --- | --- |
| `project`, `environment` | required unless the installation has only Default › production; unknown → 404 `project_not_found` / `environment_not_found` (field `project` / `environment`) |
| `range` | `1h`, `6h` (default), `24h`, `7d`; other → 422 `invalid_field` (field `range`) |
| `sort` | `unusual` (default), `traffic`, `memory`; other → 422 `invalid_field` (field `sort`) |
| `{id}` | an app ID; unknown → 404 `app_not_found` |

## Schemas

```yaml
Metrics:
  required: [range, points, series, deploys, issues, services, baseline, sources]
  range: {enum: ["1h", "6h", "24h", "7d"]}
  step: {type: integer}                       # seconds per point: 60, 300, 600, 3600
  points: {type: array, items: {type: string, format: date-time}}   # each point's start
  series:                                     # the place's (or app's) totals, one entry per point
    requests: MetricSeries                    # per second
    errors: MetricSeries                      # percent of requests that were 5xx
    p95: MetricSeries                         # milliseconds, from the merged buckets
    memory: MetricSeries                      # megabytes used; no `usual` drawn by the console
    memoryLimit: {type: array, items: {type: number, nullable: true}}  # megabytes, summed
    hasHttp: {type: boolean}                  # false: no app of the place has a domain
  deploys: {type: array, items: MetricDeploy}
  issues: {type: array, items: MetricIssue}   # sorted by distance, largest first
  services: {type: array, items: ServiceMetrics}   # in the asked sort
  baseline: MetricsBaseline
  sources: MetricsSources

MetricSeries:
  points:
    type: array
    items:
      value: {type: number, nullable: true}   # null: a gap
      low: {type: number, nullable: true}     # the band; null with no earlier day or while building
      high: {type: number, nullable: true}
      usual: {type: number, nullable: true}
      days: {type: integer}                   # earlier days the band rests on (0-7)
  now: {type: number, nullable: true}         # the newest point's value
  over: {type: boolean}                       # p95 only: the newest value is beyond the highest bound ("over 10 s")
  usual: {type: number, nullable: true}       # the newest point's usual
  usualDays: {type: integer}                  # "over N days"
  unusualSince: {type: string, format: date-time, nullable: true}   # the warn rect (errors and p95)

MetricDeploy:
  appId, appName, deploymentId: {type: string}
  version: {type: integer}                    # the job version, "v24"
  endedAt: {type: string, format: date-time}  # when it ended healthy

MetricIssue:
  required: [appId, appName, service, kind, level, value, since, distance]
  appId, appName, service: {type: string}
  kind: {enum: [slower, more_errors, quieter, memory, restarting]}
  level: {enum: [warning, failed]}
  value: {type: number}                       # ms, percent, req/s, percent of limit, restarts
  usual: {type: number, nullable: true}
  limitMb: {type: number, nullable: true}     # memory
  restarts: {type: integer, nullable: true}   # memory and restarting: in the last hour
  since: {type: string, format: date-time}
  sinceCapped: {type: boolean}                # "for more than 6 hours"
  distance: {type: number}
  deployNear:                                 # absent: no deploy clause
    appId, appName, deploymentId: {type: string}
    version: {type: integer}
    endedAt: {type: string, format: date-time}
    relation: {enum: [after, before]}
    minutes: {type: integer}                  # after only
  window: {requests: integer, errors: integer, errorPercent: number, requestsPerSecond: number}
  lastExit: {copy: string, exitCode: integer, at: date-time}   # memory and restarting, when known
  domains: {type: array, items: {type: string}}               # quieter

ServiceMetrics:
  appId, appName, service: {type: string}
  state: {enum: [unusual, rolling_out, healthy, stopped]}
  level: {enum: [warning, failed], nullable: true}             # when unusual
  copies: {type: integer}
  hasDomain: {type: boolean}
  requests: {type: number, nullable: true}    # per second over the range
  errors: {type: number, nullable: true}      # percent over the range
  p95: {type: number, nullable: true}         # ms over the range
  usualP95: {type: number, nullable: true}
  cpu: {type: array, items: {type: number, nullable: true}}    # MHz per point, the sparkline
  cpuReservedMhz: {type: integer, nullable: true}
  memoryMb: {type: number, nullable: true}    # the newest minute
  memoryLimitMb: {type: number, nullable: true}
  restarts: {type: integer, nullable: true}   # in the range
  unusual: {type: boolean}
  distance: {type: number, nullable: true}
  httpShared: {type: string, nullable: true}  # the other service when Traefik's names collide

MetricsBaseline:
  since: {type: string, format: date-time, nullable: true}   # null: never sampled
  state: {enum: [building, partial, full]}
  fullDays: {type: integer}

MetricsSources:
  traefik: {state: {enum: [ok, off, unreachable, disabled]}, at: date-time|null}
  nomad: {state: {enum: [ok, denied, unreachable]}, at: date-time|null}

Host:
  required: [memory, cpu, disks, room, history, sampledAt, baseline, sources]
  memory: {used: number, reserved: number, total: number}     # megabytes
  cpu: {used: number, reserved: number, total: number, cores: integer}   # MHz
  disks:
    type: array
    items:
      mount: {type: string}
      holds: {type: array, items: {enum: [nomad, docker]}}
      usedMb, totalMb, freeMb: {type: number}
      usedPercent: {type: number}
      state: {enum: [ok, warning, failed]}
  room:
    count: {type: integer, nullable: true}    # null: nothing is running
    likeApp: {type: string, nullable: true}   # the yardstick's app ID
    likeAppName: {type: string, nullable: true}
    memoryMb: {type: number, nullable: true}
    cpuMhz: {type: number, nullable: true}
    limitedBy: {enum: [memory, cpu], nullable: true}
    freeMemoryMb: {type: number}
  history:                                    # 7 days, hourly: the host chart
    points: {type: array, items: date-time}
    memoryUsed: {type: array, items: number|null}
    memoryReserved: {type: array, items: number|null}
  sampledAt: {type: string, format: date-time, nullable: true}   # null: the host was never read
  stale: {type: boolean}                      # sampledAt older than 5 minutes
  baseline: MetricsBaseline
  sources: MetricsSources

AttentionItem (existing), gains:
  kind: enum gains disk_nearly_full           # host-level: no appId
  mount: {type: string}
  holds: {type: array, items: {enum: [nomad, docker]}}
  usedPercent: {type: number}
  freeBytes: {type: integer}
  level: {enum: [warning, failed]}            # disk_nearly_full

NotificationEvent (existing, the webhook message), gains the same five properties for a
problem about a disk.
NotificationEventType gains `problem.worsened` (designer's review, 2026-10-26): a disk that
rises from warning to failed after its `problem.opened` sends one, with `level: failed` and the
text "The disk at / is now 96% full: 410 MB left." A fall back to warning, a repeat or a further
rise sends nothing; the `problem.resolved` below 83 % is as before. `sent_problems` gains a
`level` column (in migration 0019, which has not shipped) to remember the level last sent.
```

An example of `getHost`:

```json
{
  "memory": {"used": 1946, "reserved": 1312, "total": 3891},
  "cpu": {"used": 1210, "reserved": 1730, "total": 4000, "cores": 4},
  "disks": [{"mount": "/", "holds": ["nomad", "docker"], "usedMb": 18432, "totalMb": 40960,
             "freeMb": 22528, "usedPercent": 45.0, "state": "ok"}],
  "room": {"count": 4, "likeApp": "whoami", "likeAppName": "whoami", "memoryMb": 256,
           "cpuMhz": 100, "limitedBy": "memory", "freeMemoryMb": 2579},
  "history": {"points": ["2026-10-18T23:00:00Z", "…"], "memoryUsed": [1890, null, "…"],
              "memoryReserved": [1312, null, "…"]},
  "sampledAt": "2026-10-25T23:04:02Z", "stale": false,
  "baseline": {"since": "2026-10-25T21:31:02Z", "state": "building", "fullDays": 0},
  "sources": {"traefik": {"state": "ok", "at": "2026-10-25T23:04:02Z"},
              "nomad": {"state": "ok", "at": "2026-10-25T23:04:02Z"}}
}
```

## Codes

| Code | Status | When |
| --- | --- | --- |
| `project_not_found`, `environment_not_found` | 404 | the place doesn't exist |
| `app_not_found` | 404 | `getAppMetrics` for an unknown app |
| `invalid_field` | 422 | `range`, `sort` |
| `token_scope` | 403 | a deploy token on any of these routes |

There is no 502: the reads answer from the store, and `sources` carries what couldn't be read.

## Live

The console's live hub gains the notice `metrics`, published after each tick's write (once a
minute). It carries no number; the page refetches its parts.

## Description texts (in `getMetrics`)

- The slowest 5 % is Prometheus' `histogram_quantile(0.95)` over the merged counts of Traefik's
  buckets `0.005 … 10` s; websocket and server-sent-event requests count as requests but not in
  it; a value beyond 10 s sets `over`.
- A point sums the rows of a counter and takes the median of a gauge's rows.
- The band is the minimum, maximum and median of the same UTC 10-minute slot on each of the
  previous 1–7 days; `days` says how many were found.
- The rules and thresholds of "unusual", and the distance, as in the spec's FR-011 and FR-012.
- Memory used is Nomad's `Usage − Cache` (the page cache left out), also on `listCopies`'
  `usage`.
