# Tasks: Monitoring and capacity (025)

**Input**: `specs/025-monitoring/` (spec.md, plan.md, research.md, data-model.md, contracts/api.md,
contracts/monitoring-screen.md with the designer's rulings 1–9, quickstart.md)

**Tests**: required (constitution VIII): table tests beside the pure package for the parser (on
the lab's fixture), deltas and resets, quantiles, fold, points, the band, every rule's threshold
at, just under and just over, room and the disk state; `deploytest` fakes for node stats and
reservations and a `metricstest` fake source for the service; store tests for the migration,
the tick's write, the fold and the prune; the API contract test against `api/openapi.yaml`, the
parity and scope-walk tests; a `TestConsoleLab` step and a gate step.

**Order**: the lab spike first (its answers fix the parser's fixture, the protocols left out of
latency, resets and the reserved rule), then the pure parser and percentile math, the Nomad reads,
the store and the host and lab changes, the service (sampler, reads, room, the disk item, the
Running tab's memory), the API, the console on real data, the lab steps, docs, and the review
cycle. A Sonnet agent builds each phase; Opus reviews before the next.

## Phase 0: The lab spike (no story label)

- [ ] T001 Run research R8.1–R8.9 and R8.11 against the lab (the only lab change is the one the
  feature makes: the dev Traefik job rerun with `--metrics.prometheus=true` and
  `--metrics.prometheus.buckets=0.005,0.01,0.025,0.05,0.1,0.25,0.5,1,2.5,5,10`): (1) `curl
  http://127.0.0.1:8088/metrics` answers 200 with `service="whoami-whoami@nomad"`; the exact
  `protocol` values; whether `process_start_time_seconds` is there; size and line count; save it
  as `internal/metrics/testdata/traefik.prom`; (2) a Team-only 401 and a www 301 don't move
  `traefik_service_requests_total{service="lab023-web@nomad"}`, a plain and a per-domain router's
  request do; (3) a 9:1 load of 50 ms and 400 ms (whoami `?wait=`) for 2 minutes, the p95 per minute
  from the bucket deltas against the loop's own; (4) a Traefik restart during the load: the next
  delta; (5) a stop-then-start deploy of a volume app with a domain: does its series vanish and
  come back at zero; (6) a token from `deploy/install/caravana-policy.hcl` with and without `node {
  policy = "read" }` on `Nodes().List`, `Info`, `Stats`; (7) the lifecycle-aware reserved sum (task
  lifecycles from `Jobs().Info`) against `nomad.client.allocated.cpu` / `.memory`; (8) the `db`
  Postgres under a read load: `Usage` against `Usage − Cache`; (9) `AllocDirStats` against the
  `DiskStats` `/` entry, `/mnt/lima-cidata` left out; (11) allocation stats without collection give
  NULL, not 0. Record each as "lab, observed" under research R8 and adjust plan.md where one
  differs. Clean every lab object up (the Traefik job keeps the two arguments).

## Phase 1: Foundation (no story label)

- [ ] T002 [P] New pure package `internal/metrics`: `text.go` (`ParseText` for the named families:
  the 0.0.4 format, escapes, `+Inf`/`-Inf`/`NaN`, an optional timestamp, `# HELP`/`# TYPE` skipped,
  a malformed line an error naming its number), `counters.go` (`Delta` with the reset and
  new-series rules, the 50–90 s window, `process_start_time_seconds` per T001;
  `TraefikMinute` summing requests, 5xx and the buckets of `http`/`grpc` only per service key),
  `histogram.go` (`Layout`, `Buckets`, `Merge`, the stored text form and its parser, `Quantile`
  with Prometheus' interpolation, the running maximum, `+Inf` → over, empty → none), `real.go`
  (`HTTPSource`: `Accept: text/plain;version=0.0.4`, 8 MiB limit, 5 s, 404 → `ErrOff`, dial errors →
  `ErrUnreachable`), `metricstest` (a fake source with a fixture a test changes between ticks).
  Tables: the lab's fixture first, research R2's table (2 ms, 40 ms, 180 ms, 820 ms, 3 s against the
  finer buckets), a reset, a new series, a non-monotonic scrape, a quantile in `+Inf`; a test that
  only `real.go` imports `net/http` and nothing imports `nomad`, `service` or `store`.
- [ ] T003 [P] `internal/deploy/host.go`, `reserved.go` + `deploytest`: `HostStats(ctx,
  dockerRoot)` (`Nodes().List`, `Info` cached 10 minutes, `Stats`; memory used and total, CPU used,
  total and cores, `ReservedResources`; Nomad's data by `AllocDirStats` matched to its `DiskStats`
  entry, Docker's root by the longest `Mountpoint` prefix, one row when they match, no other mount;
  403 → `ErrNodeReadDenied`); `Reservations(ctx)` (one `Allocations().List` with resources, the
  non-terminal copies, the flattening `max(Σ prestart non-sidecar, Σ main and sidecar)` as a pure
  helper with a table, task lifecycles from `Jobs().Info` cached per job ID and version, the
  service task's memory and `memory_max`, restarts, requested restarts, status, the newest
  `Terminated` event's exit code and time, job and group); the fakes for both; tests with the lab's
  numbers (1730 / 1312 against 1880 / 1408).
- [ ] T004 [P] `internal/store/migrations/0019_metrics.sql` + `internal/store/metrics.go` + tests:
  the six tables of data-model.md (`STRICT`, `WITHOUT ROWID` where it says), `EnsureSeries`,
  `EnsureLayout`, `WriteMetricsTick` (one transaction: minute rows, host, disks, an optional fold's
  rows by upsert, an optional prune, `started_at` once), `MinuteRows`, `SlotRows`, `HostRows`,
  `DiskRows`, `NewestHost`, `MetricsStartedAt`; `DeleteApp` deletes the app's series in its
  transaction; tests for a refold, the prune bounds and the cascade.
- [ ] T005 [P] Host and lab: `deploy/install/traefik.nomad.hcl` and `deploy/dev/traefik.nomad.hcl`
  gain the two arguments with a comment on the buckets; `install.sh` says "Traefik is restarted
  once for its request numbers (a few seconds without HTTP)" when the running job lacks
  `metrics.prometheus`, and writes `CARAVANA_DOCKER_ROOT` from `docker info --format
  '{{.DockerRootDir}}'` into `caravana.env`; `deploy/install/caravana-policy.hcl` gains `node {
  policy = "read" }` with its comment (feature 025: the host's memory, CPU and disks; no
  `node:write`, no `agent:read`); `internal/config` gains `DockerRoot` (`CARAVANA_DOCKER_ROOT`,
  `-docker-root`, absolute, default `/var/lib/docker`) and `TraefikMetricsURL`
  (`CARAVANA_TRAEFIK_METRICS_URL`, `-traefik-metrics-url`, default
  `http://127.0.0.1:8081/metrics`, http to a loopback host or empty) with tests;
  `deploy/dev/install-test.sh` checks the message, the policy line, the env line and a 200 from
  `127.0.0.1:8081/metrics` on the VM; shellcheck.

## Phase 2: Service (US1 the numbers, US2 the host, US3 issues, US4 the disk item)

- [ ] T006 [US1] `internal/metrics/fold.go` + tests: `Fold` (counters and `http_secs` summed, NULL
  when every row's is; buckets summed per bound within one layout, other layouts skipped; gauges
  the median; `minutes` counted) and `Points(rows, from, step, n)` (counters summed, gauges the
  median, rates over the seconds covered, gaps where no row), the four ranges' steps and the slot
  in progress read from its minute rows.
- [ ] T007 [US1] `internal/service/sampler.go` + `changes.go` + tests on the fakes: `runSampler(ctx,
  gap)` in `Start`'s `backupWatcher` group, ticks at `now.Truncate(gap) + gap + 2 s`, a 20 s budget,
  each source alone (Traefik scrape and deltas keyed `<app.ID>-<svc.Name>@nomad` for stored
  services with a domain, a collision attributed to neither and logged once; `Reservations`;
  `deploy.Usage` for the running copies of stored apps; `HostStats`), the restart deltas per copy
  (`Restarts − RequestedRestarts`, plus 1 for a newly failed copy; NULL after a start), the
  sources' last outcome, one `WriteMetricsTick` with the fold of a slot that just ended and the
  prune every 10 minutes, then `ChangeMetrics`; setters `SetMetricsSampleGap`,
  `SetTraefikMetricsURL`, `SetDockerRoot`, wired in `cmd/caravana/main.go` from config. Tests: a
  gap leaves NULLs, a reset, a missed minute only re-baselines, the first minute after a start, a
  collision, a Traefik 404 as `off`, a 403 on node reads as `denied`, `Wait` returns after cancel.
- [ ] T008 [US3] `internal/metrics/band.go`, `rules.go` + tests: `Band` (same UTC slot on days
  1–7, min, max, median, days; a 7 d point merges its six slots per day; NULLs skipped),
  `Baseline` (`building`, `partial`, `full`), `Judge` (the five rules over the last 10 minutes'
  minute rows with the band of the slot covering `now − 5 min`: sums and merged buckets for the
  HTTP rules, the median for memory, ≥ 20 requests for slower; rolling-out apps skip the HTTP
  rules; stopped apps aren't judged; only memory and restarting while `building`), `Distance`,
  `Level`, `Since` (the walk back ≤ 6 h, a no-data minute neither breaks nor ends the run), and
  `DeployNear` (15 minutes before, else the first after, "less than a minute after"). Tables at,
  just under and just over every threshold.
- [ ] T009 [US1] `internal/service/metrics.go` + tests: `crashesInHour(now, copies)` extracted from
  `copyItems` (`attention.go`) and used by both (≥ 3 needs-you, ≥ 2 monitoring); `Metrics(ctx,
  MetricsQuery)` and `AppMetrics(ctx, appID, rng, sort)`: the place check (`*PlaceError`), the apps
  and their series, one read of the range's rows and 8 days of slot rows, points, the totals and
  their band (built from the totals per day and slot), the rules per service and on the totals
  (`unusualSince`), the live copies for restarting and last exits (Nomad down leaves that rule
  unjudged), the three sorts, the issues' facts (window, domains, deploy clause from
  `ListDeploymentsIn` healthy ends), deploys in the range, baseline, sources.
- [ ] T010 [US2] `internal/metrics/room.go`, `disk.go` + `internal/service/host.go` + tests: `Room`
  (the yardstick by median summed memory reservation over running stored apps, the lower middle,
  ties alphabetical; ⌊min(free memory / X, free CPU / X)⌋; `limitedBy`; nothing running);
  `DiskState` (ok, warning, failed; the item's open rule, level and `When` per ruling 8);
  `Service.Host(ctx)` from the newest stored host and disk rows (`stale` after 5 minutes), the
  per-series reservations, the 7-day hourly history, baseline and sources; no Nomad call.
- [ ] T011 [US4] `internal/service/attention.go`, `notifier.go` + `internal/notify` + tests:
  `AttentionDiskNearlyFull` from `DiskRows` (host-level, one per watched filesystem, `Mount`,
  `Holds`, `UsedPercent`, `FreeBytes`, `Level`, `When`), not judged when the host sample is older
  than 5 minutes (the "source unknown" branch in `diffProblems`, so an outage sends no
  "resolved"), `problemKey` `disk_nearly_full|||<mount>`, `problemSubject` the mount; `notify.Event`
  gains the disk facts, Slack, Discord and ntfy a line "Disk: / is 86% full, 11 GB left", the
  webhook JSON the fields; tests: 85 opens, 86→96 sends no second "opened", 84 stays, 82 resolves,
  stale samples change nothing.
- [ ] T012 [US2] `internal/deploy/usage.go` + `internal/ui/usage_test.go`: `usageFromStats` takes
  `Usage − Cache` when both are measured, else `RSS` (ruling 1); the Running tab's memory and its
  ceiling meter follow; tests for both cases and the lab's Postgres numbers (24 / 3 / 18 → 6 MB).

## Phase 3: API 0.27.0 (US5)

- [ ] T013 [US5] `api/openapi.yaml` 0.27.0 + `internal/api/metrics.go` + tests: `getMetrics`,
  `getAppMetrics`, `getHost` per contracts/api.md (the parameters' rules and codes, `null` for gaps,
  the description texts), `AttentionItem`'s kind `disk_nearly_full` and its five properties, the
  same on `NotificationEvent`; the contract test over the new routes; `TestScopedTokenWalksEveryRoute`
  with the three routes denied; parity entries for `/monitoring`, its parts and the Apps home's
  panel added in Phase 4 (`TestConsoleParity` fails on an entry for a route not yet served, 024's
  lesson).

## Phase 4: Console (US1, US2, US3, US4, US6)

- [ ] T014 [US1] `internal/ui/style/input.css` "Page 8 (025)" + `DESIGN.md` "Monitoring (feature
  025, page 8)": the head, the filter row, the issues rows (grid `14px minmax(0, 1fr) auto`, the
  marks, the detail line, the action), the `MnChart` classes (gridlines line-1, the band polygon
  ink-3 at 0.7, the warn rect, the dashed deploy markers and their rolling labels, the series
  polyline with a non-scaling stroke, the crosshair, the axis times in fg-3), the apps table grid
  and its fold under 640px, the CPU sparkline, the `Meter` (track ink-3, fill fg-2, reserved
  hatched, warn above 90 %), the host panel, the footer, the 2×2 and one-column chart grids;
  ruling 9 (no fg-4 text) written in DESIGN.md; `tools/ui-css.sh build`.
- [ ] T015 [US1] `internal/ui/monitoring.go` + `templates/monitoring.html` + `console.js` + tests:
  `GET /monitoring` (the place bars only with more than one choice, the range bar, the sort bar,
  all links), the issues panel with the contract's fixed sentences per kind and the deploy clause,
  or its one line (and the baseline line); the four charts as inline SVG by attributes and
  classes (no `style`), `<title>`s on the band (its days) and the polyline (min, max, latest), the
  visually hidden table, the baseline caption, the 7 d caption, "No HTTP traffic: …" and the three
  source sentences; the apps table with marks, "· no domain", "—", the warn values, the
  sparkline's title, meters, restarts, the logs link with its `aria-label`, rows linking to the
  Running tab; the footer's two sentences; `/parts/monitoring/...` swapped by id on the `metrics`
  notice; the hover crosshair with script ("13:40 UTC · 07:40 local"), "now" without; the sidebar's
  Monitoring item (`activity`, `navMonitoring`) after Logs; parity entries; `TestNoShipWords` green.
- [ ] T016 [US2] `internal/ui/monitoring.go` + template + tests: the "This host" section
  (`id="host"`, `SectionHead` with "1 server", the three meters with readouts, one disk meter per
  watched filesystem labelled with its mount when they differ, the room sentence with its title and
  warn hue for "No room…", "Nothing is running yet: …", the 7-day memory chart with used and
  reserved dashed, the stale ■ line, the "token can't read the host" sentence).
- [ ] T017 [US4] `internal/ui/home.go` + `templates` + `attention.go` + tests: the Apps home's
  Platform panel above Activity ("● 1 server up" or "■ Can't read the host's numbers", the memory
  meter, the fuller disk's meter, the room sentence, linking to `/monitoring#host`) from
  `Service.Host` only; the needs-you row for `disk_nearly_full` with the contract's sentences (one
  filesystem, or both held) and **Open monitoring**; parity entry for the panel.

## Phase 5: Lab tests

- [ ] T018 `cmd/caravana/console_monitoring_test.go` + `console_lab_test.go` + `lab_test.go`: the
  step `console-monitoring` per quickstart §6 (`startLab` sets
  `CARAVANA_TRAEFIK_METRICS_URL=http://127.0.0.1:8088/metrics` and the gap to 10 s; the three
  apps; seeded history through the store; the load for 2 minutes; the issues, the order, "—", the
  SVG's band and marker, the deploy clause after a deploy in the step, the host section, the
  Platform panel, the sidebar, the links, the no-script heads, the part swap, Traefik unreachable
  by the setter).
- [ ] T019 `cmd/caravana/gate_monitoring_test.go` + `gate_test.go`: the gate step `monitoring` per
  quickstart §6 (the three reads with the gate's token, requests against the loop's count,
  non-zero host numbers, reserved equal to Nomad's allocated gauges, `node:read` with the scoped
  token, a deploy token's 403 on each, `range=2h` 422, `null` in the first minute).
- [ ] T020 The review's data and the disk fill: `cmd/caravana/seed_metrics_test.go`
  (`TestSeedMetricsForReview`, opt-in with `CARAVANA_SEED_METRICS=<db>` and `CARAVANA_SEED_DAYS`,
  rows for every stored service with a domain, `started_at` moved back); research R8.10 on
  `deploy/dev/install-test.sh`'s throwaway VM (fill past 85 %, 95 %, back to 84 %, below 83 %, the
  notifications), recorded in `research.md` as "lab, observed".

## Phase 6: Docs

- [ ] T021 `CHANGELOG.md` Unreleased (Added: the Monitoring page, the host's capacity, the Platform
  panel, `disk_nearly_full`, the three reads; Changed: the Running tab's memory leaves the page
  cache out, so its numbers drop for apps that read files; **Upgrade**: rerun the installer:
  Traefik is restarted once (a few seconds without HTTP on 80 and 443) to turn on its request
  numbers, Caravana's Nomad policy gains `node { policy = "read" }`, and `caravana.env` gains
  `CARAVANA_DOCKER_ROOT`; until then the page says to run it), `docs/plan.md` row 025 ("Built
  (specs/025-monitoring)…" with what changed from the row's text: per Traefik service, buckets
  stored and the slowest 5 % on read, memory without the cache, Nomad's prestart rule, two watched
  filesystems, no host reservation), `CLAUDE.md` "Feature 025" paragraph in 024's style.

## Phase 7: Review cycle

- [ ] T022 Checks (the CI list, `go test -race ./...`), `TestConsoleLab` (every step), the gate step
  `monitoring` and `deploy/dev/install-test.sh` in the lab; research R8.12 (a day of the ring
  buffer: rows by `res`, the file and WAL, the tick's time, Caravana's RSS) recorded; stage the
  review instances (a fresh instance, one with seeded history, the load script's unusual services
  of each kind, a service without a domain, a deploy marker, "No room…" by reservations, Traefik
  and Nomad unreachable), tell the designer; their review at 1200, 640 and 390 and without
  script; the impeccable critique (no detector); the rulings into the contract; the fixes; the last
  look.

## Dependencies

T001 → T002, T003, T004, T005 (parallel) → T006 → T007 → T008 → T009, T010, T011, T012 (parallel)
→ T013 → T014 → T015 → T016, T017 (parallel) → T018, T019, T020 (parallel) → T021 → T022.

## Parallel work

- Phase 1's four tasks touch disjoint places (`internal/metrics`, `internal/deploy`,
  `internal/store`, the installer, lab scripts and `internal/config`).
- T006 and T008 are pure and could run beside T007; T008 is listed after T007 so the sampler's rows
  are real when the rules are tried against them.
- Phase 2's T009–T012 each add their own files; T009 and T011 both touch `attention.go`
  (`crashesInHour`, the disk kind): one agent in order, or T011 after T009.
- Phase 4's T015 and T016 share `monitoring.go` and its template: one agent in order; T017 is
  separate.
- T018, T019 and T020 are independent files; one lab run at a time.
