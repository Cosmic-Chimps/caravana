# Implementation Plan: Monitoring and capacity

**Branch**: `025-monitoring` (stacked on `024-logs`) | **Date**: 2026-10-25 |
**Spec**: [spec.md](./spec.md) | **Page contract**: [contracts/monitoring-screen.md](./contracts/monitoring-screen.md) |
**API contract**: [contracts/api.md](./contracts/api.md) | **Research**: [research.md](./research.md)

## Summary

A sampler goroutine reads, once a wall-clock minute, Traefik's Prometheus metrics on loopback
(requests, 5xx and latency buckets per Traefik service `<app>-<service>@nomad`), Nomad's
allocation stats and reservations per service, and Nomad's node stats for the host, and writes
one row per service and one for the host into a SQLite ring buffer (migration `0019_metrics.sql`):
minute rows for 48 hours and 10-minute rows for 8 days, folded as each slot ends. Everything else
is computed on read by pure functions in a new package `internal/metrics`: the slowest 5 % from
merged bucket counts (Prometheus' interpolation), range points (counters summed, gauges by
median), the band (the same UTC 10-minute slot on the previous 1–7 days), the unusual rules and
their distance, the room for more apps. A new needs-you kind `disk_nearly_full` is derived from
the stored host rows. The console gains `/monitoring`, the Apps home's Platform panel and a
sidebar item; the Running tab's memory leaves the page cache out. API 0.27.0 adds `getMetrics`,
`getAppMetrics` and `getHost`. The installer turns on Traefik's metrics (Traefik restarts once),
grants the token `node:read` and writes Docker's data root. No new module.

## Technical Context

Go 1.27. **No new module**: the Prometheus text format is read by our own small parser (research
R1: `expfmt` would bring three modules for one format). Storage: migration `0019_metrics.sql`
(`metrics_series`, `metrics_layouts`, `metrics_samples`, `metrics_host`, `metrics_disks`,
`metrics_state`), 3–8 MB for 20 services. Nomad: `Allocations().List` with resources,
`Jobs().Info` once per job version (task lifecycles), `Nodes().List`, `Nodes().Info` (cached 10
minutes), `Nodes().Stats`, and 014's per-copy `Allocations().Stats`. Policy: `node { policy =
"read" }` (no `node:write`, no `agent:read`). Traefik: `--metrics.prometheus=true` and
`--metrics.prometheus.buckets=0.005,0.01,0.025,0.05,0.1,0.25,0.5,1,2.5,5,10` on the existing
loopback entrypoint `traefik` (`127.0.0.1:8081` on hosts, `127.0.0.1:8088` from the Mac in the
lab, no forward change); the installer's rerun restarts Traefik once (a few seconds without 80 and
443, as 023's file provider did) and says so. New settings: `CARAVANA_DOCKER_ROOT`
(`-docker-root`, default `/var/lib/docker`, written by the installer from `docker info`) and
`CARAVANA_TRAEFIK_METRICS_URL` (`-traefik-metrics-url`, default
`http://127.0.0.1:8081/metrics`, loopback only, empty turns the HTTP numbers off). Memory: the
previous scrape's values (~3,000 floats) and the restart counts per copy. Testing: pure table
tests (the parser on a fixture captured from the lab, deltas and resets, quantiles, fold, points,
band, rules, room, the disk item over rows), `deploytest` fakes for node stats and reservations, a
fake metrics source, store tests, service tests, the API contract test, the scope walk and
parity, UI tests, a `TestConsoleLab` step `console-monitoring` and a gate step `monitoring`; the R8
lab checks as the first task, recorded in `research.md` as "lab, observed".

## Constitution Check

| Principle | Assessment |
| --- | --- |
| I. Small | PASS. No module, no runtime dependency (Traefik already runs; its exporter is an option of it). One goroutine wakes once a minute for well under a second; the ring buffer is 3–8 MB for 20 services and stops growing (pruned every 10 minutes, SQLite reuses the pages). |
| II. One Model | PASS. Nothing touches `app.App` or `Translate`; the sampler reads what Nomad and Traefik say about the stored apps. |
| III. Safe Deploys | PASS. Read-only for apps. The installer's Traefik change is a static job argument, applied by the installer's existing rerun of the Traefik job. |
| IV. Fail Loudly | PASS. A source not read is NULL, drawn as a gap, never 0; "—" and "no domain" for a service without HTTP numbers; Traefik's metrics off, Traefik unreachable, Nomad unreachable and the token without `node:read` each have their own sentence; a Traefik name collision attributes to neither, is logged and named in the cells' title; the 7 d band says it thins. |
| V. API-First | PASS. `getMetrics`, `getAppMetrics`, `getHost` and the attention kind in openapi 0.27.0 in the same commit as the routes; the console calls the same service methods; deploy tokens denied by the default deny, covered by the scope walk. |
| VI. Nomad-Native | PASS. Nomad through the official client only, inside `deploy` (node stats, allocation list, job lifecycles, the prestart rule); the service sees samples, not Nomad types. Traefik's routing is untouched (still service tags); its metrics are a read of its own loopback endpoint. |
| VII. Secure | PASS. `node:read` only (it allows no drain, no eligibility, no write); the metrics endpoint stays on loopback beside the dashboard; no header labels, router labels or URLs in the series; the store holds counts, sizes and service names; nothing secret is sampled. |
| VIII. Plain Go | PASS. Pure functions with tables for every rule and threshold; the Traefik source and the node reads are small seams with fakes as second implementations; the sampler is one plain loop with a timer. |

## Decisions carried (and where the research and the contract differed)

- **The sampler** (R4): `runSampler(ctx, gap)` started in `Service.Start` in the `backupWatcher`
  group (`Add(1)` / `Done()`, as the notifier and the domain watcher; `Wait` already waits for
  every goroutine `Start` launches, 91e6ba9). It ticks at `now.Truncate(gap) + gap + 2 s` (wall
  clock, so rows line up across restarts); the first tick is the first whole minute after start.
  Each tick has a 20 s budget; each source is read independently, and one that fails leaves its
  columns NULL. One transaction per tick: the minute's rows, the fold of a slot that just ended,
  and every 10 minutes the prune; then `publish(Change{What: ChangeMetrics})`.
  `SetMetricsSampleGap` (default a minute) lets the lab use 10 s.
- **In memory between ticks** (R4): the previous Traefik value per series, the previous restart
  counts per copy and the time of the previous successful scrape; and each source's last outcome
  (`ok`, `off`, `unreachable`, `denied`, `disabled`) for the page's notices. A restart loses them:
  its first minute has CPU and memory but no HTTP numbers or restarts.
- **Traefik** (R1): read `traefik_service_requests_total` and
  `traefik_service_request_duration_seconds_bucket` for services ending in `@nomad`, keyed back
  to `(app, service)` by `<app.ID>-<svc.Name>@nomad` for every stored service with a domain;
  anything else is ignored. Per Traefik service, not per router: every per-domain router of 023
  already names the service (lab, observed), so the contract's "per router, summed per service"
  becomes "per service, read directly", with router labels left off. A key two stored services
  share is attributed to neither and logged once. `Accept: text/plain;version=0.0.4`, 8 MiB read
  limit, 5 s per call. A 404 is `off` (an old host: "run the installer again"); a refused
  connection or timeout is `unreachable`.
- **Counters and resets** (R2): the delta per series is `now − previous`; `now < previous` is a
  reset and the delta is `now`; a series absent from the previous scrape counts from zero. A
  delta is stored only when the previous successful scrape was 50–90 s before; after a missed
  minute the next scrape only re-baselines. `process_start_time_seconds` is in the answer but is
  not a stable process start: it drifts back 1–4 s between scrapes of one process (lab, observed,
  R8.1/R8.4), so it marks every series reset only when it moves by more than 10 s (a restart moved
  it by 359 s); the counter rule stays the base, and the 10 s check only catches a restart that
  leaves every counter above its old value.
- **Buckets stored, p95 on read** (R2): per service per minute the per-bucket (not cumulative)
  counts, trailing zeros cut, with a layout id naming the bounds; any p95 (a minute, a window, a
  point, a service, the environment) merges the counts and applies Prometheus'
  `histogram_quantile`: rank `0.95 × total`, linear interpolation inside the bucket from its lower
  bound, a cumulative series made monotonic by a running maximum, a rank in `+Inf` reads "over 10
  s", no requests is a gap. Rows of another layout are skipped in a merge. The contract's "the
  sampler keeps the merged buckets per environment per minute" is met by merging the services'
  stored counts on read: no environment table, so a deleted app leaves the totals.
- **Long-lived requests** (ruling 2, R1): the bucket deltas sum only `protocol` `http` and `grpc`;
  `sse` (and `websocket`, below) count in requests but never in latency. The protocol values on
  Traefik 3.7.13 (R8.1, lab, observed) are `http`, `sse` and `grpc`; **`websocket` never appeared**
  in `traefik_service_*` after an upgraded connection ended cleanly, so websocket traffic is not
  in the numbers at all (the footer's sentence says so). **Errors (5xx) are counted only for
  `http` and `sse`**: for `grpc` the `code` label is the gRPC status (observed `code="2"`), so a
  `5` there is NOT_FOUND, not a server error; grpc requests still count in requests and latency.
  The footer adds the ruling's sentence.
- **Memory used** (ruling 1, R3): `Usage − Cache` when both are measured, else `RSS`, in
  `usageFromStats` (`internal/deploy/usage.go`), so the Running tab and Monitoring agree. The
  Running tab's numbers drop for apps that read files; the CHANGELOG says so. A copy whose stats
  answer has `Timestamp` 0 or no measured memory kind reads **NULL, not 0**: Nomad answers that way
  for a copy that isn't running (lab, observed, R8.11: one zeroed entry per task, `Measured` null,
  so 014's `len(Tasks) == 0` test would have read it as 0 MB), and `usageFromStats` gains the
  check beside the `Usage − Cache` change.
- **Memory limit** (R3): per copy the service task's `memory_max` when set, else its reservation,
  from `AllocatedResources.Tasks[<service>]`, summed over the service's running copies.
- **Reserved** (ruling 3, R3): per copy, `max(Σ prestart non-sidecar tasks, Σ main and sidecar
  tasks)` for CPU and memory, Nomad's flattening; summed over copies. The lifecycle of each task
  comes from the job (`Jobs().Info`), cached per job ID and version, so jobs Caravana didn't make
  are counted right too. The host's reserved is the sum over every non-terminal copy on the node
  plus the node's `ReservedResources` (0 today). R8.7 checks it against Nomad's
  `nomad.client.allocated.*` gauges: on 2026-10-25 the rule gave 1780 MHz / 1344 MB over 18 copies
  and the gauges said exactly 1780 / 1344, against a naive 1930 / 1440 (lab, observed, R8.7). The
  gauge `allocated.memory` leaves `memory_max` out, as the room's yardstick does.
- **Reservations per service stored** (an addition to R4's table): `metrics_samples` gains
  `cpu_reserved_mhz` and `memory_reserved_mb` (the service's flattened reservation over its
  running copies, its `names` sidecar and `caravana-volumes` task included), and `metrics_host`
  gains `cpu_cores`. The room's yardstick, the CPU sparkline's "0.4 of 1 core reserved" and the
  vCPU readout then come from the newest stored rows, so the Apps home never waits on Nomad.
- **CPU** (R3): `TotalTicks` per copy and `CPUTicksConsumed` for the host, point samples of
  Nomad's last collection; the sparkline's title says "now". vCPU = MHz ÷ (total MHz ÷ cores).
- **Two watched filesystems** (ruling 4, R3): Nomad's data is `AllocDirStats`, its mount the
  `DiskStats` entry with the same `Size` (and `Used` within 1 %); Docker's root is the
  `DiskStats` entry with the longest `Mountpoint` that is a prefix of `CARAVANA_DOCKER_ROOT`.
  The same mount twice is one row with `holds` `nomad,docker`. Used % is `UsedPercent`; "left" is
  `Available`. No other mount is read into a row.
- **Retention and folding** (R4): minute rows (`res` 60) for 48 h, 10-minute rows (`res` 600) for
  8 days, overlapping; the slot `[t, t+10 min)` is folded at the first tick after it ends, in Go
  over its minute rows (`metrics.Fold`): counters and `http_secs` summed (NULL when every row's
  is), buckets summed per bound within one layout, gauges the median; `INSERT … ON CONFLICT DO
  UPDATE`, so a refold after a restart is harmless. Prune every 10 minutes. A gap is a missing
  row or a NULL column.
- **Range points** (ruling 5, R4): 1 h = 60 × 1 min and 6 h = 72 × 5 min from minute rows; 24 h =
  144 × 10 min and 7 d = 168 × 1 h from 10-minute rows; the slot still in progress (not yet
  folded) is read from its minute rows. Counters and buckets summed per point, gauges the median
  of the point's rows, rates divided by the seconds covered; a point with no row is a gap. This
  corrects the contract's "each point the median of its rows".
- **The band** (R5, ruling 6): per point and metric, slot `⌊(t mod 86400) / 600⌋`; for d in 1..7
  the 10-minute row of that slot on `day(t) − d` (requests and errors as that row's rate and
  share, the slowest 5 % from its buckets, memory its median); NULLs skipped; `low` min, `high`
  max, `usual` median, `days` the count. A 7 d hour point merges its six slots per earlier day.
  The environment's band is built from the environment's totals per day and slot, not from the
  services' bands. "over N days" is the newest point's `days`. On 7 d older points find fewer
  earlier days in the 8 kept: the band thins to the left; one caption under that chart's times,
  "The band thins to the left: older hours have fewer earlier days kept.", and the band's `title`
  names the days it rests on.
- **Baseline states** (contract, R4): `metrics_state.started_at` is written by the first tick ever
  and never moved. `building` while `now − started_at < 24 h` (no band, no "usually", the
  baseline caption, the "still being learned" line, only the absolute rules); `partial` with 1–6
  full days ("usually 180 · over 3 days"); `full` from 7.
- **The rules** (contract, R5): over the last 10 minutes' minute rows, with the band of the slot
  covering `now − 5 min`. slower: the p95 of the window's merged buckets with ≥ 20 requests, `>
  1.5 × high` and `− high ≥ 100 ms`; more errors: Σerrors / Σrequests `> 1 %`, Σerrors ≥ 5, and `>
  high`; quieter: Σrequests / Σhttp_secs × 60 `< low / 2` with `low ≥ 1 req/min`; memory: the
  median of memory / limit `> 90 %`; restarting: ≥ 2 from the live copies. The contract's "the
  median of its minute values" becomes the window's sums and merged buckets for the three HTTP
  rules, by ruling 5's reasoning (a median of minute counts hides a burst, a median of minute p95s
  swings on a minute with three requests); memory keeps the median. Distance: value / high,
  low / value (100 when the value is 0), memory / 0.9, 1 + restarts / 10. Level failed when errors
  `> 10 %` or memory `> 98 %`. Rolling-out apps (newest deployment `pending` or `running`) skip the
  HTTP rules; stopped apps aren't judged. The environment's charts use the same rules on the
  totals for the warn hue and the warn rect.
- **Since when** (R5): walking back from now over minute rows, at most 6 hours, the earliest minute
  of the unbroken run in which that minute (for slower, the 5 minutes ending there) still breaks
  the same threshold against its own slot's band; a minute with no data neither breaks nor ends
  the run; longer than 6 hours reads "for more than 6 hours".
- **The deploy clause** (R5): deployments of the place's apps that ended `healthy`, at their
  `UpdatedAt` (024's `store.ListDeploymentsIn`): the newest ending within 15 minutes before the
  start ("It started 2 minutes after …", "less than a minute after" for 0), else the first ending
  after the start and before now ("It started before …"), else nothing. `v24` is the job version.
  The charts' deploy markers are the same healthy ends in the range, one label per deployment and
  "and N more" when they crowd (024's rule).
- **Restarts** (R6): one counting rule, `crashesInHour(now, copies)` extracted from
  `copyItems` (`internal/service/attention.go`): restarts nobody asked for of copies that
  restarted within the hour, plus one per copy failed within it; ≥ 3 is the needs-you
  `restarting`, ≥ 2 the monitoring issue. Stored per minute: the delta of `Restarts −
  RequestedRestarts` per copy since the previous tick, plus 1 for a copy newly `failed`; NULL in
  the first minute after a start. The issue's last exit is the service task's newest `Terminated`
  event (exit code, time).
- **Room** (contract, ruling 7, R3): N = ⌊min((memory total − reserved) / X.memory, (CPU total −
  reserved) / X.cpu)⌋; X the running stored app whose summed memory reservation is the median of
  the running apps' (the lower middle for an even count; ties the alphabetically first); `limitedBy`
  the smaller; the three sentences of the contract; the `title` of ruling 7. No host reservation
  (backlog). Ceilings don't count (oversubscription is on).
- **`disk_nearly_full`** (ruling 8, R6): host-level (`AppID` empty), one per watched filesystem,
  derived on every `Attention` read from the stored host rows. It holds while the newest sample is
  ≥ 85 %, or while the newest is ≥ 83 % and no sample since the newest ≥ 85 % one fell below 83 %
  (within the kept history). Level `failed` when the newest is ≥ 95 %, else `warning`. `When` is
  the first ≥ 85 % sample of the current run. When the newest host sample is older than 5 minutes,
  the kind isn't judged: `diffProblems` gains the "source unknown" branch the copy kinds have, so
  an outage never sends "resolved". Key `disk_nearly_full|||<mount>` (one problem per
  filesystem; the item and its "opened" say the level it had). The research's one-hour window for
  the hysteresis gives way to ruling 8's plain "resolves below 83 %".
- **Text is never fg-4** (ruling 9): chart axis times, the 7 d caption and the table's "—" are
  fg-3 (the contract drew the "—" in fg-4; the ruling's "text is never fg-4" covers it, to be
  confirmed at the review); fg-4 stays for non-text marks only. Deploy labels stay rolling,
  gridlines line-1.
- **Not in 025**: a host reservation, per-copy history, Traefik's access log, owner-set
  thresholds, network and block I/O, the queue and query-rate lines, an MCP tool, multi-server
  capacity (Phase 3: Docker's root as a node meta key, a host row per node).

## Design

### `internal/metrics` (new, pure but `real.go`)
- `text.go`: `ParseText(r io.Reader, names ...string) ([]Sample, error)`: the 0.0.4 exposition
  format, only the named families (a prefix check before any label parsing), label escapes `\\`,
  `\"`, `\n`, values `+Inf`, `-Inf`, `NaN`, an optional timestamp ignored; table tests and the
  lab's fixture (`testdata/traefik.prom`, captured in T001).
- `counters.go`: `Delta(prev, now map[SeriesKey]float64) map[SeriesKey]float64` with the reset
  and new-series rules; `TraefikMinute(deltas, keyOf func(service string) (Key, bool))` summing
  requests, 5xx (`http` and `sse` only) and (for `http`/`grpc` only) buckets per service.
- `histogram.go`: `Layout` (bounds), `Buckets` (per-bucket counts), `Merge`, `Quantile(b, q)`
  (Prometheus' interpolation, running maximum, `+Inf` → `Over`, empty → no value), the text form
  stored in a row and its parser.
- `fold.go`: `Fold(rows []Row) Row` and `Points(rows, from, step, n) []Point` (counters summed,
  gauges median, gaps).
- `band.go`: `Band(history []Row, at time.Time, metric Metric) Range{Low, High, Usual, Days}`,
  `Baseline(startedAt, now)`.
- `rules.go`: `Judge(window []Row, band Bands, copies CopyFacts, rollingOut, stopped bool, state
  BaselineState) []Issue`, `Since(rows, band…)`, `Distance`, `Level`, `DeployNear(start, now,
  deploys)`; tables for every threshold at, just under and just over.
- `room.go`: `Room(host HostTotals, apps []AppReservation) RoomResult`.
- `disk.go`: `DiskState(rows []DiskRow, now) (open bool, level, when)` for ruling 8.
- `real.go`: `HTTPSource{URL, Client}` with `Scrape(ctx) (io.ReadCloser, error)` mapping 404 to
  `ErrOff` and dial errors to `ErrUnreachable`; `metricstest` has a fake source serving a fixture
  that a test can change between ticks.
- A test that the package imports nothing of `net/http` outside `real.go`, nor `nomad`,
  `service` or `store`.

### `internal/deploy`
- `host.go`: `HostStats(ctx, dockerRoot string) ([]NodeStats, error)`: `Nodes().List`, `Info`
  cached 10 minutes (total memory, CPU shares, cores, `ReservedResources`), `Stats` (memory used,
  `CPUTicksConsumed`, `DiskStats`, `AllocDirStats`), the two watched filesystems by the rules
  above. A 403 is `ErrNodeReadDenied` (the token predates 025), distinct from `ErrNomadUnavailable`.
- `reserved.go`: `Reservations(ctx) ([]CopyReservation, error)`: one `Allocations().List` with
  `resources=true`, non-terminal copies, the flattened CPU and memory per copy by the lifecycle
  rule (task lifecycles from `Jobs().Info`, cached per job ID and version), the service task's
  memory and `memory_max`, restarts, requested restarts, status, the newest `Terminated` event's
  exit code and time; the job and group, so the service maps it to stored apps and counts the
  rest only in the host's reserved. The flattening is a pure helper with a table test.
- `usage.go`: `usageFromStats` takes `Usage − Cache` when both are measured, else `RSS`.
- `deploytest`: node stats and info, allocation stubs with resources and lifecycles, a 403 for
  node reads.

### `internal/store`
- `migrations/0019_metrics.sql`: the tables of [data-model.md](./data-model.md).
- `metrics.go`: `EnsureSeries(ctx, app, service) (int64, error)`, `EnsureLayout(ctx, bounds)`,
  `WriteMetricsTick(ctx, Tick)` (one transaction: minute rows, host, disks, an optional fold, an
  optional prune, `started_at` on the first tick), `MinuteRows(ctx, series, from, to)`,
  `SlotRows(ctx, series, from, to)`, `HostRows`, `DiskRows`, `NewestHost`, `MetricsStartedAt`.
  `DeleteApp` deletes the app's series in its transaction (samples by `ON DELETE CASCADE`).

### `internal/service`
- `sampler.go`: `runSampler`, the tick (Traefik scrape and deltas, `Reservations`, `deploy.Usage`
  for the running copies of stored apps, `HostStats`), the mapping to series, the restart deltas,
  the tick's write, `ChangeMetrics`; setters `SetMetricsSampleGap`, `SetTraefikMetricsURL`,
  `SetDockerRoot` wired from `cmd/caravana/main.go`.
- `metrics.go`: `Metrics(ctx, MetricsQuery{Project, Environment, Range, Sort})` and
  `AppMetrics(ctx, appID, rng, sort)`: the place check (`*PlaceError`), the apps, one read of the
  range's rows and 8 days of slot rows, points, band, totals, rules (with the live copies for
  restarting, one `ListCopies`-like read; Nomad down leaves that rule unjudged), sort, issues with
  sentences' facts, deploys, baseline, sources.
- `host.go`: `Host(ctx) (HostView, error)` from the newest stored host and disk rows (stale after
  5 minutes), the per-series reservations for the room, the 7-day hourly history.
- `attention.go`: `crashesInHour` extracted; `AttentionDiskNearlyFull` with `Mount`, `Holds`,
  `UsedPercent`, `FreeBytes`, `Level` on the item, from `store.DiskRows`.
- `notifier.go`: `problemKey` and `problemSubject` for the mount; the "source unknown" branch for
  the disk kind when the host sample is stale.
- `changes.go`: `ChangeMetrics ChangeKind = "metrics"`.
- `internal/notify`: `Event` gains the disk facts; Slack, Discord and ntfy get a line "Disk: / is
  86% full, 11 GB left"; the webhook JSON carries them.

### API 0.27.0 (`internal/api/metrics.go`)
Per [contracts/api.md](./contracts/api.md): `getMetrics`, `getAppMetrics`, `getHost`; the
`AttentionItem` kind enum and its new properties, and the webhook notification component's;
deploy tokens denied by the default deny (the scope walk lists the three routes as denied);
parity entries for `/monitoring`, its parts and the Apps home's panel (added with the console, as
024 learned).

### Console (`internal/ui/monitoring.go`, `templates/monitoring.html`, `home.go`, `shell.go`)
Per the page contract and its rulings: `/monitoring` (head, the place bars only with more than
one choice, the range bar, the issues panel or its line, four `MnChart` panels as inline SVG by
attributes and classes with a `<title>` per polyline and band and a visually hidden table, the
apps table with the sort bar, marks and meters and the CPU sparkline, the host section with
`id="host"`, three meters and the 7-day memory chart, the footer with ruling 2's sentence, the
error states); `/parts/monitoring/...` swapped on the `metrics` notice by id (one swap per chart
and per table row); the hover crosshair in `console.js` (script only; the head shows "now"
without it); the Apps home's Platform panel above Activity; the sidebar's Monitoring item
(`activity`) after Logs; the Running tab's memory as the service now gives it. Styles "Page 8
(025)" in `internal/ui/style/input.css`, `tools/ui-css.sh build`; DESIGN.md: the `Meter` with its
hatched reserved part, the `MnChart` (band, series, gridlines, deploy markers, warn rect), the
sparkline, ruling 9. The narrow layouts at 1200, 640 and 390 per the contract.

### Installer and lab
- `deploy/install/traefik.nomad.hcl` and `deploy/dev/traefik.nomad.hcl`: the two arguments with a
  comment on the buckets; `install.sh` says "Traefik is restarted once for its request numbers (a
  few seconds without HTTP)" when the running job lacks `metrics.prometheus`.
- `deploy/install/caravana-policy.hcl`: `node { policy = "read" }` with a comment (feature 025).
- `install.sh`: `CARAVANA_DOCKER_ROOT=$(docker info --format '{{.DockerRootDir}}')` in
  `caravana.env`.
- `internal/config`: `DockerRoot` (absolute path) and `TraefikMetricsURL` (http, loopback host
  only, or empty), with flags and tests.
- Lab: `lab.sh up` reruns the dev Traefik job (it does today); `startLab` sets
  `CARAVANA_TRAEFIK_METRICS_URL=http://127.0.0.1:8088/metrics` and the sample gap to 10 s;
  `TestLab`'s token from `caravana-policy.hcl` gains `node:read` with the file.
  `deploy/dev/install-test.sh` checks the policy line, the env line and a 200 from
  `127.0.0.1:8081/metrics` on the VM.

## Complexity Tracking

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| (none) | | `prometheus/common/expfmt` was the one alternative that would add weight (three modules); rejected in research R1 for a ~120-line parser of our own. |
