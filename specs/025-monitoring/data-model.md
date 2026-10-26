# Data model: Monitoring and capacity (025)

The sampler stores what each source gave, per minute, with NULL for what it didn't (migration
`0019_metrics.sql`). Everything else (points, the band, issues, room, the disk item) is derived on
read and never stored. Times in the tables are unix seconds (UTC) of a slot's start.

## Migration `0019_metrics.sql`

### `metrics_series` (one per app service ever sampled)

| Column | Type | Notes |
| --- | --- | --- |
| `id` | `INTEGER PRIMARY KEY` | |
| `app_id` | `TEXT NOT NULL` | the app's ID; its place comes from `app.PlaceOf`, so no project or environment column |
| `service` | `TEXT NOT NULL` | |
| | `UNIQUE (app_id, service)` | |

Deleted in `store.DeleteApp`'s transaction with the app; its samples go by `ON DELETE CASCADE`
(the store opens SQLite with `foreign_keys(1)`).

### `metrics_layouts` (the histogram bounds a row's counts are for)

| Column | Type | Notes |
| --- | --- | --- |
| `id` | `INTEGER PRIMARY KEY` | |
| `bounds` | `TEXT NOT NULL UNIQUE` | seconds, comma-separated, `+Inf` implied: `0.005,0.01,0.025,0.05,0.1,0.25,0.5,1,2.5,5,10` |

A row's counts are merged only with rows of the same layout; a later change of Traefik's buckets
adds a layout and never misreads an old row.

### `metrics_samples` (`STRICT, WITHOUT ROWID`)

| Column | Type | Notes |
| --- | --- | --- |
| `series` | `INTEGER NOT NULL REFERENCES metrics_series (id) ON DELETE CASCADE` | |
| `res` | `INTEGER NOT NULL CHECK (res IN (60, 600))` | a minute row or a 10-minute row |
| `ts` | `INTEGER NOT NULL` | the slot's start |
| `minutes` | `INTEGER NOT NULL` | minute rows folded in (1 for `res` 60) |
| `http_secs` | `INTEGER` | seconds the HTTP counts cover; NULL: no HTTP numbers (no domain, Traefik not read, first minute) |
| `requests` | `INTEGER` | |
| `errors` | `INTEGER` | 5xx |
| `layout` | `INTEGER REFERENCES metrics_layouts (id)` | |
| `buckets` | `TEXT` | per-bucket counts (not cumulative), `+Inf` last, trailing zeros cut; NULL when no request was timed |
| `cpu_mhz` | `INTEGER` | summed over the running copies; NULL: Nomad not read |
| `cpu_reserved_mhz` | `INTEGER` | the flattened reservation summed over the running copies (added to research R4's table) |
| `memory_mb` | `INTEGER` | `Usage − Cache` (else `RSS`) summed |
| `memory_limit_mb` | `INTEGER` | `memory_max`, else the reservation, of the service task, summed |
| `memory_reserved_mb` | `INTEGER` | flattened, summed (added to research R4's table) |
| `copies` | `INTEGER` | running copies |
| `restarts` | `INTEGER` | restarts nobody asked for plus newly failed copies since the previous tick; NULL in the first minute after a start |
| | `PRIMARY KEY (series, res, ts)` | the only index |

### `metrics_host` (`STRICT, WITHOUT ROWID`)

| Column | Type | Notes |
| --- | --- | --- |
| `node` | `TEXT NOT NULL` | Nomad node ID (one today; Phase 3 ready) |
| `res`, `ts` | as above | |
| `memory_used_mb`, `memory_reserved_mb`, `memory_total_mb` | `INTEGER` | used from `Stats().Memory.Used`; reserved by Nomad's rule plus `ReservedResources`; total from `NodeResources` |
| `cpu_used_mhz`, `cpu_reserved_mhz`, `cpu_total_mhz` | `INTEGER` | used is `CPUTicksConsumed` |
| `cpu_cores` | `INTEGER` | for the vCPU readout (added to research R4's table) |
| | `PRIMARY KEY (node, res, ts)` | |

### `metrics_disks` (`STRICT, WITHOUT ROWID`)

| Column | Type | Notes |
| --- | --- | --- |
| `node`, `res`, `ts` | as above | |
| `mount` | `TEXT NOT NULL` | `/`, `/var/lib/docker`, … |
| `holds` | `TEXT NOT NULL` | `nomad`, `docker` or `nomad,docker` |
| `used_mb`, `total_mb`, `free_mb` | `INTEGER NOT NULL` | free is `Available` (what a non-root writer gets); used % is `used / (used + free)`, Nomad's `UsedPercent`, what `df` prints |
| | `PRIMARY KEY (node, res, ts, mount)` | |

### `metrics_state`

| Column | Type | Notes |
| --- | --- | --- |
| `id` | `INTEGER PRIMARY KEY CHECK (id = 1)` | one row |
| `started_at` | `TEXT NOT NULL` | written by the first tick ever, never moved: "Building the baseline: started …" |

### Retention

| Rows | Kept | Written | Pruned |
| --- | --- | --- | --- |
| `res` 60 | 48 hours | every tick | every 10 minutes, `ts < now − 48 h` |
| `res` 600 | 8 days | at the first tick after the slot ends (`INSERT … ON CONFLICT DO UPDATE`) | every 10 minutes, `ts < now − 8 d` |

Size: 4,032 rows per series; 3–8 MB for 20 services, under 1 MB for the host and disks (research
R4, Mac, measured).

## Derived, not stored

### Point (one per range step)

| Field | Rule |
| --- | --- |
| `T` | the point's start; 1 h: 60 × 1 min, 6 h: 72 × 5 min (minute rows); 24 h: 144 × 10 min, 7 d: 168 × 1 h (10-minute rows, the slot in progress from its minute rows) |
| requests | Σrequests / Σhttp_secs (per second) |
| errors | Σerrors / Σrequests × 100 |
| p95 | `Quantile(Merge(buckets), 0.95)` in ms |
| memory, memory limit, CPU, copies | median of the point's rows |
| restarts | Σrestarts |
| gap | no row, or every row's column NULL |

### Band (per point, per metric)

| Field | Rule |
| --- | --- |
| `Low`, `High`, `Usual` | min, max, median of the metric in the same UTC 10-minute slot on each of the previous 1–7 days (a 7 d point merges its six slots per day) |
| `Days` | how many earlier days had a value |
| absent | `building` baseline, or no earlier day had a value |

### Baseline

| State | When | Effect |
| --- | --- | --- |
| `building` | `now − started_at < 24 h` | no band, no "usually", the baseline caption; only memory and restarting judged |
| `partial` | 1–6 full days | "usually 180 · over 3 days" |
| `full` | 7 full days or more | "usually 180" |

### Issue (an unusual service)

| Field | Notes |
| --- | --- |
| `Kind` | `slower`, `more_errors`, `quieter`, `memory`, `restarting` |
| `Level` | `failed` when errors `> 10 %` or memory `> 98 %`; else `warning` |
| `Value`, `Usual`, `Limit`, `Restarts` | the kind's numbers |
| `Since` | the earliest minute of the unbroken run (≤ 6 hours back; `SinceCapped` beyond) |
| `Distance` | value / high, low / value (100 when 0), memory / 0.9, 1 + restarts / 10 |
| `DeployNear` | the deploy clause: app, version, end time, `after` or `before`, minutes |
| `Window` | requests, errors, error percent, req/s of the 10 minutes (the detail lines) |
| `LastExit` | copy, exit code, time (memory and restarting) |
| `Domains` | the service's domains (quieter's detail line) |

### Room

| Field | Notes |
| --- | --- |
| `Count` | ⌊min(free memory / X.memory, free CPU / X.cpu)⌋; nil when nothing runs |
| `LikeApp`, `MemoryMB`, `CPUMHz` | the yardstick X: the median running app by summed memory reservation (lower middle, ties alphabetical) |
| `LimitedBy` | `memory` or `cpu` |
| `FreeMemoryMB` | for "Nothing is running yet: 3.6 GB of memory is free to reserve." |

### Disk state and `disk_nearly_full`

| Field | Rule |
| --- | --- |
| `State` (per watched filesystem) | `ok` below 85 %, `warning` 85–95 %, `failed` ≥ 95 % (the meter and `getHost`) |
| item open | newest ≥ 85 %, or newest ≥ 83 % and no sample below 83 % since the newest ≥ 85 % one |
| item level | `failed` when newest ≥ 95 %, else `warning` |
| item `When` | the first ≥ 85 % sample of the current run |
| not judged | the newest host sample is older than 5 minutes |
| key | `disk_nearly_full|||<mount>` |

### Sources (the sampler's last outcome, in memory)

| Source | States |
| --- | --- |
| Traefik | `ok`, `off` (404: metrics not turned on), `unreachable`, `disabled` (the URL setting is empty) |
| Nomad | `ok`, `denied` (403 on node reads: the policy predates 025), `unreachable` |

## Where the API's shapes come from

| API field | Source |
| --- | --- |
| `Metrics.points`, `series.*.points[].value` | Points over `metrics_samples` of the place's series |
| `series.*.points[].low/high/usual/days` | Band over `res` 600 rows of the 8 days |
| `series.*.unusualSince` | the rules on the totals, `Since` |
| `deploys` | `store.ListDeploymentsIn` (024), status `healthy`, `UpdatedAt` in the range |
| `issues`, `services` | the rules per series, live copies for restarting, the sort |
| `baseline` | `metrics_state.started_at` and now |
| `sources` | the sampler's last outcome |
| `Host.memory/cpu/disks` | the newest `metrics_host` and `metrics_disks` rows |
| `Host.room` | the newest rows' totals and reservations, and per series `cpu_reserved_mhz` and `memory_reserved_mb` summed per running app |
| `Host.history` | `metrics_host` `res` 600 rows of 7 days, hourly points (median) |
| `AttentionItem` (`disk_nearly_full`) | `metrics_disks` rows of the kept history |
