# Research: Monitoring and capacity (025)

Read-only research on the code at `024-logs` (API 0.26.0, newest migration `0018_domains.sql`), on
the vendored Nomad (`hashicorp/nomad/api@v0.0.0-20260917172403-9dcbdc5e64ec` and the matching server
and client source `nomad@v1.11.0-rc.1.0.20260917172403-9dcbdc5e64ec` in the module cache, cited as
`nomad:` paths), on Traefik's published reference for v3 metrics
(`doc.traefik.io/traefik/reference/install-configuration/observability/metrics/`, cited as "Traefik
docs") and on the lab's live Nomad 2.0.7 and Traefik 3.7.13, queried read-only on 2026-10-25 around
22:20 UTC (cited as "lab, observed"). Nothing in the lab was changed: only `GET` on Nomad's
`/v1/nodes`, `/v1/node/:id`, `/v1/client/stats`, `/v1/allocations`, `/v1/client/allocation/:id/stats`,
`/v1/agent/self`, `/v1/metrics`, `/v1/operator/scheduler/configuration`, and Traefik's
`/api/entrypoints`, `/api/overview`, `/api/version`, `/api/http/routers`, `/api/http/services`,
`/metrics` (through the lab's forward `127.0.0.1:8088` → the VM's `:8081`). The probe scripts are in
`/Users/jjchiw/.claude/jobs/42856734/tmp/research025/` (`probe1.sh` … `probe5.sh`).

A throwaway SQLite file outside the repo (`size.sql`, same folder) measured the ring buffer's size; its
numbers are marked "Mac, measured".

Inputs read: the designer's contract `specs/025-monitoring/contracts/monitoring-screen.md` (2026-10-25,
before the spec), `docs/plan.md` row 025 (and rows 5 and 13 of the comparison table), `docs/vision.md`,
the constitution (I, VI, VII), `CLAUDE.md` (014's usage, 018's notifier, 022–024), the Traefik jobs
`deploy/install/traefik.nomad.hcl` and `deploy/dev/traefik.nomad.hcl`, `deploy/install/nomad.hcl.tpl`,
`deploy/dev/nomad.hcl`, `deploy/dev/lab.yaml`, `deploy/install/install.sh` (`configure_caravana`, the
Traefik step), `deploy/install/caravana-policy.hcl`, `internal/deploy/usage.go`, `internal/deploy/copies.go`,
`internal/service/attention.go`, `internal/service/notifier.go` (`problemKey`, `diffProblems`),
`internal/service/service.go` (`Start`, `Wait`), `internal/service/domainwatch.go`,
`internal/service/schedulewatch.go`, `internal/nomadjob/domains.go` and `translate.go`
(`registration`), `internal/store/migrations/`, `go.mod`, and `specs/024-logs/research.md` (the shape,
and R1's weighing of a ring buffer).

## Findings that change the contract's wording

The contract was written before this research. Thirteen facts from the code and the lab bear on it;
each R-section carries the detail and a recommendation for the designer's ruling.

1. **Traefik's metrics are off, on hosts and in the lab.** Neither Traefik job passes any
   `--metrics.*` argument; the lab's Traefik says `"features":{"tracing":"","metrics":"","accessLog":false}`
   and `GET :8088/metrics` answers **404** (lab, observed). The Traefik job must change (installer and
   lab), which restarts Traefik once on every host (a few seconds without 80/443, as 023's file
   provider did). R1.
2. **Every router of a service already shares one Traefik service.** The lab's `lab023-web@nomad`
   service is `usedBy` five routers (`lab023-web@nomad` and four `lab023-web-<hash>@nomad`), and
   `blog-web@nomad` by three (lab, observed): Traefik's service is the Nomad registration
   `<app>-<service>` (`registration`, `internal/nomadjob/translate.go:327-345`). The contract's
   "per router, summed per service" becomes **per service, read directly**: no router labels. R1.
3. **Traefik's default buckets (0.1, 0.3, 1.2, 5 s) can't say "slowest 5%" for a fast app.**
   Interpolating inside the first bucket puts any app whose requests all take 2 ms at **95 ms**
   (p95 of a uniform 0-100 ms). Finer buckets are needed (R2), set in the same Traefik change.
4. **Long-lived requests would own the slowest 5%.** A websocket or a server-sent-events stream is
   one request that lasts minutes; Traefik labels them by `protocol` (R1). Latency must leave out
   `protocol="websocket"` and `"sse"` (they still count as requests). Unverified in the lab: R8.
5. **"Memory reserved" isn't the sum of the tasks' reservations.** Nomad counts a prestart task that
   isn't a sidecar (016's `caravana-volumes`) as the larger of it and the main tasks, not on top of
   them. Lab, observed: the naive sum over running copies is 1880 MHz / 1408 MB; Nomad's own
   `nomad.client.allocated.cpu` / `.memory` say **1730 / 1312**, exactly 3 volume apps × (50 MHz,
   32 MB) less. The sampler must apply Nomad's rule (R3).
6. **"Memory used" as the Running tab reads it includes the page cache.** `usageFromStats` takes
   `Usage` when measured (`internal/deploy/usage.go`), which on cgroups v2 is anonymous memory plus
   file cache. Lab, observed, Postgres (`db`): `Usage` 24 MB, `RSS` 3 MB, `Cache` 18 MB. A database
   fills its cache up to the ceiling and stays there, so the "above 90% of the limit" rule on `Usage`
   would mark every busy database unusual forever. R3 recommends `Usage − Cache` for the rule and the
   charts.
7. **Never watch every mount.** Nomad's `DiskStats` lists every physical partition, and the lab's
   includes `/mnt/lima-cidata` at **100.0%** (lab, observed; Lima's read-only config disk). Only the
   filesystems holding Nomad's data and Docker's root are watched, as the contract says; Nomad
   reports the first (`AllocDirStats`) but **not** Docker's root (no `driver.docker.*` attribute
   names it). R3.
8. **Caravana's Nomad token can't read the host today.** `/v1/client/stats` and `/v1/node/:id`
   need `node:read` (`nomad:client/client_stats_endpoint.go:26`, `nomad:nomad/node_endpoint.go:1206`);
   `deploy/install/caravana-policy.hcl` has none. The policy gains `node { policy = "read" }` (rerun
   the installer, as 016, 020.5 did). `agent:read` is **not** needed (R3).
9. **A range point is a sum for counters, not a median.** "Each point the median of its rows"
   fits gauges (memory, CPU); for requests and errors the exact value is the sum over the point's
   rows divided by the seconds they cover, and the slowest 5% is the quantile of the merged buckets.
   The median of five minute-counts would hide a burst. R4.
10. **The 7 d range's band can't reach 7 days back for its older points.** A point 6 days ago needs
    days −7 … −13; only 8 days are kept. With the same rule as elsewhere ("the days there are"), the
    band thins towards the left edge of the 7 d chart and is absent at its oldest day. R5.
11. **Nothing is reserved for the host itself.** `ReservedResources` is 0 MB / 0 MHz (lab, observed),
    and `nomad.hcl.tpl` sets no `client.reserved`. So "room for N more" counts memory Caravana, Nomad,
    Docker and Traefik's host need (the lab's host `Used` is 1.76 GB of 2.04 with 1.31 GB reserved).
    The sentence is still right in Nomad's terms (that is what Nomad would place); a `reserved` block
    is a scheduling change for the backlog, not 025. R3.
12. **The 10-minute rows overlap the 48 h of minute rows.** To read "the same time of day" on days −1
    and −2 from one kind of row, each 10-minute slot is folded as soon as it ends, and kept 8 days;
    the minute rows are kept 48 h beside them. The contract's row count (20 × (2,880 + 1,152)) already
    assumes this. R4.
13. **Restarts come with Nomad's limits.** A copy's `Restarts` is a lifetime counter per task and
    Nomad keeps only the last **10** events per task (`defaultMaxEvents`,
    `nomad:client/allocrunner/taskrunner/task_runner.go:52`), so the "requested" restarts of 016 are
    an undercount for a copy restarted often (as `deploy.Copy` already says). Per-minute restarts are
    deltas the sampler keeps in memory; the first minute after Caravana starts has none (R4, R6).

---

## R1. Traefik's metrics

**Decision**: turn on Traefik's Prometheus exporter on the **existing loopback entrypoint `traefik`**
(`127.0.0.1:8081` on hosts, where the dashboard already is), with finer buckets and the default
labels; read `traefik_service_requests_total` and `traefik_service_request_duration_seconds_bucket`
for the services that end in `@nomad`; parse the text format with a small parser of our own (no new
module).

### What Traefik offers (Traefik docs)

| Option | Default | For Caravana |
|---|---|---|
| `metrics.prometheus` | off | **on** |
| `metrics.prometheus.buckets` | `0.1, 0.3, 1.2, 5` | **`0.005,0.01,0.025,0.05,0.1,0.25,0.5,1,2.5,5,10`** (R2) |
| `metrics.prometheus.addEntryPointsLabels` | true | keep (cheap, unused) |
| `metrics.prometheus.addRoutersLabels` | false | **keep false** (finding 2) |
| `metrics.prometheus.addServicesLabels` | true | keep: this is what Caravana reads |
| `metrics.prometheus.entryPoint` | `traefik` | keep: the dashboard's loopback entrypoint |
| `metrics.prometheus.manualRouting` | false | keep |
| `metrics.prometheus.headerLabels` | none | none (a header label could carry anything a client sends) |
| `metrics.addInternals` | false | keep: the dashboard's own requests stay out |

Series per service (Traefik docs): `traefik_service_requests_total{code,method,protocol,service}`
(counter), `traefik_service_request_duration_seconds{code,method,protocol,service}` (histogram:
`_bucket{le}`, `_sum`, `_count`), `traefik_service_retries_total{service}`,
`traefik_service_server_up{service,url}` (only for services with a Traefik health check; Caravana's
use Nomad's), and the bytes counters. Routers have the same with `router` and `service`.

Caravana reads:

- **requests** = Σ over `code, method, protocol` of the delta of `traefik_service_requests_total`;
- **errors_5xx** = the same over `code` starting with `5` (Traefik's own 502/504 for a dead or slow
  copy count: they are the app's failures as a visitor sees them);
- **buckets** = Σ over `code, method` of the delta of each `_bucket{le}`, **only for `protocol` `http`
  and `grpc`** (finding 4: `websocket` and `sse` last as long as the connection). Unverified: the
  exact protocol values on 3.7.13 (R8). Observed (R8.1, lab): `http`, `sse` and `grpc`, never
  `websocket` (those requests aren't recorded at all), and for `grpc` the `code` label is the gRPC
  status, so errors count only `http` and `sse`.

### The service label

- Traefik's service for a Caravana service is the Nomad registration name plus the provider:
  `<app ID>-<service>@nomad` (lab, observed: `whoami-whoami@nomad`, `hollo-web@nomad`; an app in a
  project would be `shop--production--web-web@nomad`). Per-domain routers (023,
  `DomainRouterName`, `internal/nomadjob/domains.go`) name the same service, so their requests are
  already in it.
- The sampler maps a label back with a table built from the stored apps: for each app and each
  service **with a domain** (no domain, no registration tags, no Traefik service: "—" in the
  contract), key `<app.ID>-<svc.Name>@nomad`. Anything else is ignored: `caravana@file` (the console
  route, `traefik.nomad.hcl`), `api@internal`, jobs Caravana didn't make.
- A latent ambiguity, not 025's to fix: app `a-b` service `c` and app `a` service `b-c` are both
  `a-b-c`, which today already merges their copies in one Traefik load balancer. When two stored
  pairs give the same key the sampler attributes the series to neither and logs it once. Worth a
  backlog item (a deploy-time refusal).
- **Which requests reach the service metrics** (lab, observed, R8.2: confirmed): a Team-only domain's 401 (basic
  auth on the router) and a www pair's 301 (redirect middleware on the router) are answered before
  the service, so they should count only on the router and the entrypoint. That is the right meaning
  ("requests the app saw"); the domain page (023) is where redirects and the password show.

### How Caravana reaches it

- **Hosts**: `http://127.0.0.1:8081/metrics`. Traefik runs with `network_mode = "host"` and the
  `traefik` entrypoint is `127.0.0.1:8081` (`deploy/install/traefik.nomad.hcl`), so it is loopback
  only, like Caravana's own listener and Nomad. Nothing new is opened; the dashboard on the same
  port is already unauthenticated on loopback (`--api.insecure=true`), and metrics add only service
  names, status codes and counts (no URL, no header, no host).
- **Lab**: the `traefik` entrypoint is `:8081` in the VM, forwarded to the Mac's `127.0.0.1:8088`
  (`deploy/dev/lab.yaml`), so the lab needs **no forward change**: `http://127.0.0.1:8088/metrics`.
- A setting `CARAVANA_TRAEFIK_METRICS_URL` (`-traefik-metrics-url`), default
  `http://127.0.0.1:8081/metrics`, loopback (or `http://` to a loopback address) only, as 026.5 checks
  `CARAVANA_URL`; `startLab` sets the lab's. Empty turns the HTTP numbers off (the page then says
  "Can't read Traefik's numbers").
- Alternative: a dedicated entrypoint `metrics` on `127.0.0.1:8082` with
  `--metrics.prometheus.entryPoint=metrics`. It survives a later decision to close the dashboard,
  but costs one more argument, one more port, and a Lima forward (an edit of `lab.yaml` needs the
  VM recreated or `limactl edit` and a restart). Not now; it is a one-line move when the dashboard
  changes.

### What changes in the Traefik jobs

Both `deploy/install/traefik.nomad.hcl` and `deploy/dev/traefik.nomad.hcl` gain, in `args`:

```
"--metrics.prometheus=true",
"--metrics.prometheus.buckets=0.005,0.01,0.025,0.05,0.1,0.25,0.5,1,2.5,5,10",
```

(entryPoint, services labels and routers labels at their defaults; a comment says why the buckets.)
The installer already reruns the Traefik job on every run (`nomad_cli job run -detach … traefik.nomad.hcl`,
`install.sh:382`); the changed args restart the system job's allocation once. The installer should say
so, as it did for 023 ("Traefik is restarted once …"). The lab: `lab.sh up` or a manual
`job run` of the dev job. Traefik's own resources (128 MB reserved, the lab's copy uses 15 MB,
lab, observed) are ample.

### Parsing

The text exposition format Traefik serves (version 0.0.4) is lines of `# HELP`, `# TYPE`, and
`name{label="value",…} value [timestamp]`, label values escaping `\\`, `\"` and `\n`, values that may
be `+Inf`, `-Inf` or `NaN`.

| Option | For | Against |
|---|---|---|
| **Our own parser** (recommended): ~120 lines in a pure package, reading only the two metric names Caravana uses (a prefix check before any label parsing), table-tested with a fixture captured from the lab (R8) | no module; pure; ignores what it doesn't need; the format has been stable for a decade | we own its edge cases (escapes, `+Inf`), all small and tested |
| `github.com/prometheus/common/expfmt` | the reference parser | `go.sum` has **no** `prometheus` or `protobuf` module today (Mac, observed: `grep` finds none); `expfmt` pulls `prometheus/client_model` and `google.golang.org/protobuf` for its types: three new modules for one text format, against constitution I's "justify every module" |

Send `Accept: text/plain;version=0.0.4` so Traefik never answers OpenMetrics or protobuf. Go's client
asks for gzip and inflates it on its own. Bound the read (`io.LimitReader`, 8 MiB) and the call
(5 s).

**Rationale**: the entrypoint and the port exist; the change is two arguments and a Traefik restart;
the parser is small enough that a module isn't worth it.

**Alternatives**: Traefik's access log, counted by Caravana (rejected in `traefik.nomad.hcl`'s own
comment: webhook tokens travel in query strings; and parsing a log is costlier); the plan's comparison
row 13 keeps the access log for Phase 3. Router labels (rejected: finding 2; they multiply series by
routers per service for no gain).

---

## R2. Percentiles from histograms

**Decision**: per service per minute, store the **bucket deltas** (not a p95), and compute any p95
(a service's minute, its 10 minutes, a range point, the environment's total) at read time from the
merged buckets with Prometheus' `histogram_quantile` interpolation. Finer buckets, as in R1.

### The computation

1. **Delta per series**: for each `_bucket{service,code,method,protocol,le}` series, `now − previous`.
   A **reset** (Traefik restarted, or the series was dropped and came back) shows as `now < previous`:
   then the delta is `now` (Prometheus' `rate()` rule: the counter restarted at zero). A series that
   wasn't in the previous successful scrape counts from zero (its whole value is new since that
   scrape, because the previous scrape was a minute ago). The one undetectable case (a reset followed
   by more requests than before within one minute) is Prometheus' own blind spot; if Traefik exposes
   `process_start_time_seconds` (a lab check, R8), a change in it marks every series reset.
2. **Merge**: sum the deltas per `le` over `code`, `method` and the kept protocols, giving one
   cumulative histogram per service per minute: counts `c(le_1) ≤ … ≤ c(+Inf)`. A non-monotonic
   result (Traefik's buckets aren't read atomically) is made monotonic by taking the running maximum,
   as Prometheus does.
3. **Quantile** (Prometheus' `histogram_quantile`): rank `r = 0.95 × c(+Inf)`; the first bucket with
   `c(le_i) ≥ r`; linear interpolation `le_{i-1} + (le_i − le_{i-1}) × (r − c(le_{i-1})) / (c(le_i) −
   c(le_{i-1}))`, with `le_0 = 0`. If the rank falls in `+Inf`, the answer is the highest finite bound
   (10 s with the finer buckets): "over 10 s". No requests: no value (a gap, not zero).
4. **Merging across services** (the environment's Slowest 5% chart, a multi-service app, a 10-minute
   row, a range point): add the bucket counts per `le`, then step 3. This is the contract's ruling
   ("merged buckets, not a mean of p95s") and it needs every row to carry the buckets, hence the
   storage decision.
5. **Too few requests**: a quantile over 5 requests is "the slowest one". The rules (R5) judge
   "slower" only with at least **20 requests** in the 10 minutes; the charts draw any value.

### What the buckets can express

The interpolation assumes requests spread evenly inside a bucket, so the answer is only as good as the
bucket around it.

| p95 truly | Default buckets (0.1, 0.3, 1.2, 5) | Finer (0.005 … 10) |
|---|---|---|
| 2 ms (whoami) | ≈ 95 ms (somewhere in 0-100 ms) | ≈ 4.75 ms (0-5 ms) |
| 40 ms | ≈ 95 ms | 25-50 ms |
| 180 ms | 100-300 ms | 100-250 ms |
| 820 ms | 300 ms-1.2 s | 500 ms-1 s |
| 3 s | 1.2-5 s | 2.5-5 s |

With the defaults every app under 100 ms reads "95 ms" and "slower than usual" can never fire below
it. The finer set (Prometheus client's own defaults plus 10 s) keeps each bucket within 2.5× of its
neighbour from 5 ms to 10 s, so the contract's "above the band's top by 50% and by 100 ms" is
judgeable from about 200 ms up and sensible below.

### Cost of finer buckets

- Series: each `(service, code, method, protocol)` combination is `buckets + 3` series (`_bucket` per
  bound plus `+Inf`, `_sum`, `_count`): 15 instead of 7. A service sees perhaps 5-10 combinations
  (200/304/404 GET, 200 POST, a 500): ~150 series per service, ~3,000 for 20 services, plus the
  entrypoint histograms (2 entrypoints × a few combinations). At ~150-200 bytes per line that is
  **~0.5 MB per scrape**, parsed in a few milliseconds (estimate; R8 measures the lab's).
- Traefik's memory: a few hundred bytes per series, about 1 MB more (estimate).
- Caravana's memory: the previous scrape's values for the series it reads, a map of ~3,000 floats
  (~300 KB with keys).
- Storage: 12 counts per row instead of one number (R4).

**Rationale**: storing buckets makes every grouping exact (a 10-minute slot, a 1-hour point, an
environment); storing a p95 per row would make all of them a mean of percentiles, which the contract
rules out.

**Alternatives**: a p95 per row plus a separate merged-buckets table per environment (two writes, a
second shape, and the environment rows go stale when an app is deleted); Traefik's `_sum / _count`
mean (cheap, but a mean hides the slow tail the chart is for).

---

## R3. Memory and CPU per service and the host

**Decision**: per minute, one `Allocations().List` with `resources=true` (every copy, its tasks'
reservations and states), 014's `deploy.Usage` for the running copies of stored apps, and for each
node `Nodes().Info` (cached 10 minutes) and `Nodes().Stats`. The policy gains `node { policy = "read" }`.
Docker's root comes from a setting (default `/var/lib/docker`) the installer fills.

### Per service

- **CPU** (MHz) and **memory** per copy from `deploy.Usage` (`internal/deploy/usage.go`: per copy
  `Allocations().Stats`, the service's own task, 4 at a time, 2 s each). Lab, observed: one stats call
  **13 ms**; for 20 copies about 5 rounds of 4, well under a second.
  - **CPU** is `TotalTicks`, the docker driver's percent turned into MHz
    (`nomad:drivers/docker/util/stats_posix.go:79`). It is a **point sample** of Nomad's last
    collection (`CollectionInterval: "1s"`, lab, observed), not the minute's average: Nomad's API
    exposes no cumulative CPU time. A once-a-minute sparkline of samples is what the contract asks
    for; its `title` should say "now" (the contract's "0.4 of 1 core reserved, now" already does).
  - **Memory**: **`Usage − Cache`** when both are measured, else `RSS` (finding 6). cgroups v2 maps
    `Cache` to the `file` counter (`stats_posix.go:37-41`), which is reclaimable page cache. Caveat:
    Postgres' shared buffers are shared memory, which cgroups v2 also counts as `file`, so they are
    under-counted; an out-of-memory kill is still its own needs-you item (`out_of_memory`). Lab,
    observed:

    | Copy | Usage | RSS | Cache | Usage − Cache |
    |---|---|---|---|---|
    | `db` / postgres | 24 MB | 3 MB | 18 MB | 6 MB |
    | `nginx` / redis | 6 MB | 4 MB | 0 | 6 MB |
    | `traefik` | 15 MB | 14 MB | 0 | 15 MB |

    The Running tab (014) shows `Usage`; the monitoring page showing a different number for the same
    copy would confuse. Recommended: 025 changes `usageFromStats` to `Usage − Cache` for both (one
    line, a test), and the spec says so.
  - **Memory limit**: per copy, the service task's `memory_max` when set, else its reservation, from
    `AllocatedResources.Tasks[<service>].Memory` (`MemoryMaxMB`, `MemoryMB`; lab, observed, e.g.
    whoami 32/64). Summed over the service's running copies. The `names` sidecar and `caravana-volumes`
    are not the service.
  - **Copies**: the count of running copies (the table's "2 copies").

### The host

`Nodes().Stats(nodeID)` (`/v1/client/stats`; lab, observed, abridged):

```json
"Memory": {"Total": 2044227584, "Available": 176529408, "Used": 1761398784, "Free": 38780928},
"CPUTicksConsumed": 484.84848485627975,
"DiskStats": [
  {"Device": "/dev/vda1", "Mountpoint": "/", "Size": 19682557952, "Used": 6233407488,
   "Available": 13432373248, "UsedPercent": 31.696720164225063},
  {"Device": "/dev/vda13", "Mountpoint": "/boot", …},
  {"Device": "/dev/vda15", "Mountpoint": "/boot/efi", …},
  {"Device": "/dev/vdb", "Mountpoint": "/mnt/lima-cidata", "Size": 19922944, "Used": 19922944,
   "Available": 0, "UsedPercent": 100.0}],
"AllocDirStats": {"Device": "", "Mountpoint": "", "Size": 19682557952, "Used": 6233407488,
                  "Available": 13432373248, "UsedPercent": 31.696720164225063}
```

- **Memory used**: `Used` (gopsutil's Linux value: total less free, buffers and cache;
  `nomad:client/hoststats/host.go:168-181`). **Total**: `NodeResources.Memory.MemoryMB` (1949, lab,
  observed; the same 2,044,227,584 bytes). **Reserved**: Nomad's own `AllocatedResources` sum over
  the node's non-terminal copies with its lifecycle rule (finding 5), plus `ReservedResources`
  (0 in the lab).
- **CPU used**: `CPUTicksConsumed` (MHz over all cores, `host.go:308-338`; a point sample as above).
  **Total**: `NodeResources.Cpu.CpuShares` (4000 in the lab, set by `cpu_total_compute`). **Reserved**:
  as memory.
- **The lifecycle rule** (finding 5): per copy, `max(Σ prestart non-sidecar tasks, Σ main and sidecar
  tasks)` for CPU and memory, as Nomad's `Comparable()` flattening does; then sum over copies. The
  task's lifecycle is on the job (`Allocations().Info` or the job), not the list stub; the sampler
  can read each app's job once per deploy (it changes only with a deploy) or recognise
  Caravana's own prestart task by name (`caravana-volumes` is the only non-sidecar prestart task
  `Translate` writes; `names` is a sidecar). A test against Nomad's numbers in the lab (R8) pins it.
  An alternative that needs no rule: Nomad's `/v1/metrics` gauges `nomad.client.allocated.cpu` /
  `.memory` (lab, observed: 1730 / 1312) and `nomad.client.unallocated.*` (2270 / 637). That endpoint
  has no ACL check (`nomad:command/agent/metrics_endpoint.go:22-38`) but its names are Nomad's
  telemetry, not its API, and it answers for the agent it is asked, not for a node by ID. Kept as the
  lab's cross-check, not the source.
- **Disk**, the two watched filesystems (finding 7):
  - **Nomad's data** (copies' directories and logs, dynamic host volumes, which the `mkdir` plugin puts
    under `<data_dir>/host_volumes/`: lab, observed, `/opt/nomad/data/host_volumes/<uuid>`):
    `AllocDirStats`, which Nomad measures on its alloc dir (`host.go:151-157`). It carries no mount
    name; the mount is the `DiskStats` entry with the same `Size` (and `Used` within 1%, both read
    in the same collection): `/` in the lab. No path setting and no `agent:read` needed.
  - **Docker's root** (images, writable layers): Nomad doesn't report it (the docker fingerprint
    sets version, bridge IP, runtimes, OS type; `nomad:drivers/docker/fingerprint.go:96-184`; lab,
    observed: no root attribute). A setting `CARAVANA_DOCKER_ROOT` (default `/var/lib/docker`); the
    installer writes `docker info --format '{{.DockerRootDir}}'`. Its filesystem is the `DiskStats`
    entry with the **longest `Mountpoint` that is a prefix of the path**. Phase 3: move it to a node
    meta key written at node bootstrap (readable with `node:read`), so each node says its own.
  - The same filesystem twice is shown once, labelled with its mount (the contract's rule).
  - "Used %" is `UsedPercent` (gopsutil: used / (used + available), what `df` prints, so it reaches
    100% when non-root writes fail); "GB left" is `Available`.

### "Room for about N more apps like X"

The contract's ruling stands: N = ⌊min((memory total − memory reserved) / X's memory,
(CPU total − CPU reserved) / X's CPU)⌋, by reservation. Details from the code and the lab:

- **X's size** is the app's flattened reservation over its running copies (all services, all
  replicas, its `names` sidecars; its `caravana-volumes` task folded in by the rule). Only stored
  Caravana apps that are running are candidates (not Traefik, not backup or schedule runs, not jobs
  Caravana didn't make); everything running counts in "reserved".
- **Lab, computed by hand from the observed allocations** (which of the lab's jobs are stored apps
  was not checked): app reservations are 32 MB for ten apps, 96 MB for `hollo` and `db`, 544 MB for
  `nginx`; the median is 32 MB, the alphabetically first at the median `blog` (32 MB, 100 MHz);
  N = min(⌊(1949 − 1312) / 32⌋, ⌊(4000 − 1730) / 100⌋) = min(19, 22) = **19**, limited by memory.
- Memory oversubscription is on (`MemoryOversubscriptionEnabled: true`, lab, observed): ceilings
  (`memory_max`) don't count for placement, so they don't count here either.
- Finding 11: nothing is held back for the host's own processes; the sentence is Nomad's view.

### Policy

`node { policy = "read" }` in `deploy/install/caravana-policy.hcl`, with a comment (feature 025: the
host's memory, CPU and disks, and each node's resources). It lets the token read node records
(attributes, resources, drivers, host volume paths) and every node's client stats; it doesn't allow
draining, eligibility or writes (`node:write`). Not `agent:read`: `/v1/agent/self` would give
`DataDir` but also the agent's whole configuration and `agent/monitor` (Nomad's own log stream), and
`AllocDirStats` makes it unnecessary.

**Rationale**: everything comes from Nomad's documented API through the client Caravana already uses
(constitution VI); one capability more, read-only; the per-copy calls already exist and are fast.

**Alternatives**: Caravana's own `statfs` of the paths (it runs on the host today, but not on Phase 3's
other nodes, and under `ProtectSystem=strict`); Nomad's `/v1/metrics` for everything (above);
turning on `publish_node_metrics` (only puts the same numbers in the telemetry sink).

---

## R4. The sampler and the ring buffer

**Decision**: one goroutine, `runSampler`, started in `Service.Start` in the group `Wait` waits for
(it starts no watchers, like the notifier), ticking at each wall-clock minute. Migration
`0019_metrics.sql` with a series table and one samples table holding both resolutions, plus host and
disk tables. Gaps are missing rows or NULL columns, never zeros. Pure math in a new pure package
(`internal/metrics`: the text parser, deltas with resets, quantiles, folding, the band, the rules,
room).

### The loop

- Ticks at `now.Truncate(time.Minute) + 1 min + 2 s` (wall clock, so rows line up with minutes and
  across restarts); the first tick is the first whole minute after start (like the notifier, nothing
  is read while Nomad may still be starting).
- Each tick, with a 20 s budget (`context.WithTimeout`): Traefik's scrape, `Allocations().List`
  (`resources=true`), `deploy.Usage`, node `Stats` (and `Info` every 10 minutes). Each source
  independently: one that fails leaves its columns NULL.
- In memory between ticks, nothing that must survive: the previous Traefik values per series, the
  previous restart counts per copy, the time of the previous successful scrape. A restart of Caravana
  loses them, so its first minute has CPU and memory but no requests, errors, slowest 5% or restarts
  (a one-minute gap in those, drawn as a gap; the stored history is untouched).
- A Traefik delta is stored only when the previous successful scrape was 50-90 s before; after a
  missed minute the next scrape only re-baselines (one more minute of gap rather than a two-minute
  count drawn as one).
- One transaction per tick: the minute's rows; when a 10-minute slot has just ended, its fold; every
  10 minutes, the prune. Then `publish(Change{Kind: ChangeMetrics})` so the page swaps its parts
  (`internal/service/changes.go`).
- `Service.Wait`: `Start` adds the sampler with `s.backupWatcher.Add(1)` / `Done()` as it does the
  notifier and the domain watcher (`service.go:303-320`). Another agent is reworking `Wait` now; the
  sampler belongs wherever the notifier ends up, since both are timer loops that start nothing.
- Settings for the lab: `SetMetricsSampleGap` (default a minute), as `SetScheduleScanGap`; the lab
  shortens it to 10 s so a step sees rows.

### Tables (migration `0019_metrics.sql`)

```sql
CREATE TABLE metrics_series (           -- one per app service ever sampled
  id      INTEGER PRIMARY KEY,
  app_id  TEXT NOT NULL,
  service TEXT NOT NULL,
  UNIQUE (app_id, service)
) STRICT;

CREATE TABLE metrics_layouts (          -- the bucket bounds a row's counts are for
  id     INTEGER PRIMARY KEY,
  bounds TEXT NOT NULL UNIQUE           -- "0.005,0.01,…,10" (seconds; +Inf implied)
) STRICT;

CREATE TABLE metrics_samples (
  series       INTEGER NOT NULL REFERENCES metrics_series (id) ON DELETE CASCADE,
  res          INTEGER NOT NULL CHECK (res IN (60, 600)),
  ts           INTEGER NOT NULL,        -- unix seconds, the slot's start (UTC)
  minutes      INTEGER NOT NULL,        -- minute rows folded in (1 for res 60)
  http_secs    INTEGER,                 -- seconds the HTTP counts cover; NULL: no HTTP numbers
  requests     INTEGER,
  errors       INTEGER,                 -- 5xx
  layout       INTEGER REFERENCES metrics_layouts (id),
  buckets      TEXT,                    -- per-bucket counts (not cumulative), trailing zeros cut; NULL when no request
  cpu_mhz      INTEGER,                 -- NULL: Nomad not read
  memory_mb    INTEGER,
  memory_limit_mb INTEGER,
  copies       INTEGER,
  restarts     INTEGER,                 -- NULL: no baseline (first minute after start)
  PRIMARY KEY (series, res, ts)
) STRICT, WITHOUT ROWID;

CREATE TABLE metrics_host (
  node     TEXT NOT NULL,               -- Nomad node ID (one today; Phase 3 ready)
  res      INTEGER NOT NULL CHECK (res IN (60, 600)),
  ts       INTEGER NOT NULL,
  memory_used_mb INTEGER, memory_reserved_mb INTEGER, memory_total_mb INTEGER,
  cpu_used_mhz   INTEGER, cpu_reserved_mhz   INTEGER, cpu_total_mhz   INTEGER,
  PRIMARY KEY (node, res, ts)
) STRICT, WITHOUT ROWID;

CREATE TABLE metrics_disks (
  node     TEXT NOT NULL,
  res      INTEGER NOT NULL CHECK (res IN (60, 600)),
  ts       INTEGER NOT NULL,
  mount    TEXT NOT NULL,               -- "/", "/var/lib/docker", …
  holds    TEXT NOT NULL,               -- "nomad", "docker" or "nomad,docker"
  used_mb  INTEGER NOT NULL,
  total_mb INTEGER NOT NULL,
  free_mb  INTEGER NOT NULL,
  PRIMARY KEY (node, res, ts, mount)
) STRICT, WITHOUT ROWID;

CREATE TABLE metrics_state (            -- when sampling first began: "Building the baseline: started …"
  id         INTEGER PRIMARY KEY CHECK (id = 1),
  started_at TEXT NOT NULL
) STRICT;
```

- Integer `ts` and `WITHOUT ROWID` keep a row small and the primary key the only index; the app's
  place comes from its ID (`app.PlaceOf`), so no project or environment column.
- Deleting an app deletes its series (and with `ON DELETE CASCADE` its samples) in the same
  transaction as the app, as 018 does for `sent_problems`. The environment's totals then leave the
  deleted app's history out; accepted. `internal/service/**` is being edited by another agent now;
  the hook goes in `store.DeleteApp`.
- `metrics_state.started_at` is written by the first tick ever and never moved: "a full day" is
  `now − started_at ≥ 24 h`. A sampler stopped for days still has the day; the band then simply finds
  fewer days with rows (R5).

### Folding and pruning

- **Fold** the slot `[t, t+10 min)` at the first tick after it ends: per series, `minutes` = count of
  minute rows; `requests`, `errors`, `restarts`, `http_secs` summed (NULLs skipped, the column NULL
  when every row's is); `buckets` summed per bound (rows of another layout skipped); `cpu_mhz`,
  `memory_mb`, `memory_limit_mb`, `copies` the **median** of the rows. Host and disks the same way.
  `INSERT … ON CONFLICT DO UPDATE`, so a refold after a restart is harmless.
- **Rates** always divide by what was covered: req/s = Σrequests / Σhttp_secs; a 10-minute row with 3
  minutes of data has `http_secs` 180, not 600 (finding 9).
- **Range points** (the contract's 60 × 1 min, 72 × 5 min, 144 × 10 min, 168 × 1 h): 1 h and 6 h from
  minute rows, 24 h and 7 d from 10-minute rows; counters and buckets summed per point, gauges the
  median of the point's rows (the contract), a point with no row a gap.
- **Prune** every 10 minutes: `DELETE … WHERE res = 60 AND ts < now − 48 h` and `res = 600 AND
  ts < now − 8 d`, about 200 and 20 rows per run. SQLite reuses the freed pages, so the file stops
  growing at its steady size without `VACUUM`.

### Size and cost

- **Rows**: per series 2,880 minute rows + 1,152 ten-minute rows = 4,032; 20 services → **80,640**
  (the contract's figure), host 4,032, disks ≤ 8,064.
- **Bytes** (Mac, measured, SQLite 3.51, the samples table above, 20 series, then `VACUUM`):
  **7.8 MB** with a 12-number cumulative bucket text in every row (~50 bytes); **2.8 MB** with no
  buckets. Real rows sit between: idle services have NULL buckets and per-bucket (not cumulative)
  counts with trailing zeros cut are short. So **3-8 MB** for 20 services, under 1 MB for the host.
- **Per tick** (lab, observed): Traefik scrape (one local GET, ~0.5 MB, estimate), one allocation list
  (24 allocations, 112 KB with resources), N stats calls at ~13 ms (4 at a time), one client stats call
  (12 ms); one transaction of ~25 rows. Well under a second of wall time and a few ms of CPU per minute
  on a 2 GB VM; memory: the previous scrape's ~3,000 values.
- **Writes**: ~30,000 rows a day plus folds, in WAL; the same order as 022's runs at a busy schedule.

**Rationale**: storing what each source gave, per minute, with NULL for what it didn't, is the
simplest shape that keeps gaps honest and lets every later computation (fold, point, band, rule) be
a pure function over rows.

**Alternatives**: computing the 10-minute rows only when minute rows age out (the band would read two
kinds of rows for the last two days); an in-memory ring (lost on every restart, and the baseline
needs 7 days); keeping p95 per row (R2).

---

## R5. The band and "unusual"

**Decision**: compute on read, never stored: the band and "usually" per point from the 10-minute rows
of the same UTC slot of day on the previous 1-7 days; the rules over the last 10 minutes from minute
rows; the deploy clause from the stored deployments. All pure functions in `internal/metrics`, fed by
one store read per page.

### The band

- For a point at time t and a metric m: slot `s = ⌊(t mod 86400) / 600⌋`; for each day d in 1..7,
  the value of m in the 10-minute row at `day(t) − d`, slot s (requests and errors as rates and
  percent of that row; the slowest 5% from that row's buckets; memory the row's median). Rows with a
  NULL for m are skipped.
- `low = min`, `high = max`, `usual = median` of the values found; with **no** value, no band at
  that point. "over N days" is the count of values for the newest point (`usually 180 · over 3 days`).
- Before a full day (`now − metrics_state.started_at < 24 h`): no band, no "usually" anywhere, the
  baseline caption; only the absolute rules (memory, restarting) may fire.
- The environment's band is built from the environment's totals per day and slot (requests summed,
  errors as a share of the summed requests, buckets merged, memory summed), not from the services'
  bands.
- Points of a range coarser than 10 minutes (7 d's hour) use the six slots' merged values for each
  previous day. Finding 10: on 7 d, older points find fewer previous days.
- Cost: a 24 h page for 20 services reads 20 × 1,152 ten-minute rows (8 days) once, plus the
  minute rows of the range; grouping in Go, milliseconds.

### The rules (contract ruling 1), over the last 10 minutes

The window is the ten newest **minute** rows. The band is the slot covering the window's middle
(now − 5 min). Per service:

| Rule | Value over the window | Holds when | Distance |
|---|---|---|---|
| slower | p95 of the **merged buckets** of the 10 rows (not the median of minute p95s: a minute with 3 requests would swing it), with ≥ 20 requests | `value > 1.5 × high` and `value − high ≥ 100 ms` | value / high |
| more errors | Σerrors / Σrequests | `> 1%`, Σerrors ≥ 5, and `> high` | value / high |
| quieter | Σrequests / Σhttp_secs × 60 (req/min) | `value < low / 2` and `low ≥ 1 req/min` | low / value (∞ capped at 100 when value is 0) |
| memory | the median of the 10 rows' memory_mb / memory_limit_mb | `> 90%` (no band needed) | value / 0.9 |
| restarting | from the live copies, not the rows (R6) | ≥ 2 in the last hour | not ranked by distance: 1 + restarts / 10 |

- The contract's "the median of its minute values" becomes "the window's sums and merged buckets"
  for slower, errors and quieter (finding 9's reasoning); memory keeps the median.
- **Failed** (■): errors above 10% or memory above 98% (the contract).
- **A rolling-out app** (its newest deployment `pending` or `running`) is listed after the unusual
  ones and isn't judged by the HTTP rules while it rolls out (canaries take requests only after
  promotion; the first minutes after a deploy are the slowest). The deploy clause covers the minutes
  after.
- **"It started at 13:58 UTC"**: walking back from now over minute rows (at most 6 hours), the
  earliest minute of the unbroken run in which that minute (or, for slower, the 5 minutes ending
  there, to have enough requests) still breaks the same threshold against its own slot's band; a
  minute with no data neither breaks nor ends the run; a run longer than 6 hours reads "for more than
  6 hours".

### The deploy clause

- Candidates: the stored deployments of every app in the environment (`store.ListAppsIn`, then the
  deployments) that ended **healthy**, with their end time (`UpdatedAt` at `healthy`, the moment the
  new version took all the traffic; blue/green promotes the canaries then).
- The newest one ending within 15 minutes **before** the start: "It started 2 minutes after
  `supplier-gw` v24 rolled out." (minutes rounded down, "less than a minute after" for 0). Else the
  first one ending **after** the start and before now: "It started before `supplier-gw` v24 rolled
  out." Else nothing. `v24` is the job version (`JobVersion`), as 021's history writes it.
- Failed or reverted deploys aren't "rolled out"; they are already a needs-you item.
- The chart's deploy markers are the same deployments in the range, one label per deployment and
  "and N more" when they crowd (024's rule).

**Rationale**: the rules are a few comparisons over rows already read for the charts; storing
"unusual" would need a writer and an expiry for something a read derives in milliseconds. The page and
the API compute it the same way because both call the service.

**Alternatives**: a standard-deviation band (needs more days than a week holds for a stable estimate,
and min..max is what the contract draws); judging per minute (noisy at low traffic).

---

## R6. `disk_nearly_full` and restarts

### The needs-you item

**Decision**: a new kind `disk_nearly_full`, host-level (`AppID` empty, like `no_backup_destination`),
one per watched filesystem, derived on every `Attention` read from the **stored** newest host sample,
with a small hysteresis from the stored history.

- Facts on the item: `Mount` (new field), `Holds` (`nomad`, `docker`), `UsedPercent`, `FreeBytes`,
  `Level` (`warning` at ≥ 85%, `failed` at ≥ 95%), `When` (the first minute of the current run above
  85%, from the rows), `Detail` empty.
- **Hysteresis**: the item holds while the newest sample is ≥ 85%, or the item's filesystem was ≥ 85%
  in any sample of the last hour and the newest is still ≥ 83%. Stateless (read from the rows), so a
  disk hovering at 85.0% doesn't blink on and off the list. 018's `resolveAfter` (5 min) still
  debounces the notification.
- **Unknown**: when the newest host sample is older than 5 minutes (the sampler or Nomad down), the
  disk kind isn't judged, like the copy kinds while Nomad can't be read: `diffProblems` gains the
  same "not opened, not resolved" branch (`isCopyKind` widens to an "is this kind's source known"
  check), so an outage never sends "resolved".
- **Key** (`problemKey`, `internal/service/notifier.go:317`): `disk_nearly_full|||<mount>`;
  `problemSubject` returns `Mount` when set. One problem per filesystem: going from 86% to 96% doesn't
  send a second "opened". Alternative for the designer: the level in the key, so 95% is announced
  too; it then needs a rule that the 85% problem isn't "resolved" while the 95% one is open, or the
  owner gets "resolved" while the disk is fuller. Recommended for 025: one key, and the item (and its
  notification on open) says the level it had.
- **Wording** (the contract's): "The disk holding `/var/lib/docker` is 86% full: 11 GB left." When
  one filesystem holds both, name what it holds: "The disk holding Docker's images and Nomad's data
  (`/`) is 86% full: 11 GB left." Notification title "This host: the disk at / is 86% full". The
  action **Open monitoring** → `/monitoring#host`. A resolved notification: "The disk at / is below
  85% again: 14 GB free."
- **Order** among the items: by `When`, as every item.
- A test drives `attentionOf` with rows (no Nomad), and the lab's check fills a file on a small
  filesystem (R8).

### Restarts

**Decision**: one counting rule, shared: extract the per-service count `copyItems` already makes
(`internal/service/attention.go:265-320`: restarts nobody asked for, `Restarts − RequestedRestarts`,
of copies that restarted within the last hour, plus one per copy failed within the hour) into a pure
`crashesInHour(now, copies)` used by the needs-you list (≥ 3: `restarting`) and by the monitoring rule
(≥ 2: the "restarting" issue). They can't disagree, and the monitoring issue is the earlier warning
the contract wants.

- Per-minute stored `restarts` (the table's "Restarts in the range" and the charts): per copy, the
  delta of `Restarts − RequestedRestarts` since the previous tick (the sampler's memory), plus 1 for a
  copy that became `failed` since; summed per service. NULL in the first minute after a start.
- The memory issue's detail ("copy e4d1 · exit 137 last at 13:56 UTC") comes from the service task's
  newest `Terminated` event (`Details["exit_code"]`, its time), which the list stub carries
  (`TaskStates`, lab, observed: `files-vol`'s `web` events `Received, Task Setup, Started, Restart
  Signaled, Terminated, Restarting, Started`). The 10-event cap (finding 13) means a copy restarted
  more than a few times has lost its older events; the newest is always there.
- Unusual services are **not** needs-you items (the contract): `restarting` stays the needs-you kind
  at 3, as today.

---

## R7. The API (0.27.0)

**Decision**: three read operations, all additive; deploy tokens denied by the default deny (no entry
in `scopeRules`, `internal/api/scope.go`; `TestScopedTokenWalksEveryRoute` covers them); `Cache-Control:
no-store` is not needed (no secret). Times RFC 3339 UTC; numbers in base units (requests per second,
percent, milliseconds, megabytes, MHz); `null` for a gap, never 0.

```yaml
/v1/metrics:
  get:
    operationId: getMetrics
    summary: How an environment's apps are doing, against their usual week.
    parameters:
      - {name: project, in: query, schema: {type: string}}      # both may be left out with one environment
      - {name: environment, in: query, schema: {type: string}}
      - {name: range, in: query, schema: {type: string, enum: ["1h", "6h", "24h", "7d"], default: "6h"}}
    responses:
      "200": {content: {application/json: {schema: {$ref: "#/components/schemas/Metrics"}}}}
      "404": project_not_found, environment_not_found
/v1/apps/{id}/metrics:
  get:
    operationId: getAppMetrics      # the same shape, the app's services only
/v1/host:
  get:
    operationId: getHost
    summary: This host's memory, CPU and disks, and the room left by reservation.
```

Schemas (abridged):

```yaml
Metrics:
  required: [range, points, series, deploys, issues, services, baseline]
  properties:
    range: {type: string, enum: ["1h", "6h", "24h", "7d"]}
    points: {type: array, items: {type: string, format: date-time}}   # each point's start
    series:                       # the environment's totals, one entry per point
      properties:
        requests: {$ref: "#/components/schemas/MetricSeries"}  # per second
        errors:   {$ref: …}       # percent of requests that were 5xx
        p95:      {$ref: …}       # milliseconds, from the merged buckets
        memory:   {$ref: …}       # megabytes used; `limit` beside it
    deploys: {type: array, items: {appId, appName, version, endedAt}}
    issues:  {type: array, items: MetricIssue}     # sorted by distance
    services: {type: array, items: ServiceMetrics} # "unusual first"
    baseline: {since: date-time, fullDays: integer}  # "Building the baseline" when fullDays is 0
    sources: {traefik: {ok: boolean}, nomad: {ok: boolean}}   # the page's "Can't read …" notices
MetricSeries:
  properties:
    points: {type: array, items: {value: number|null, low: number|null, high: number|null, usual: number|null}}
    now: number|null               # the latest point's value
    usualDays: integer             # "over N days"
    unusualSince: date-time|null   # the warn rect on Errors and Slowest 5%
MetricIssue:
  required: [appId, service, kind, level, value, since]
  properties:
    kind: {enum: [slower, more_errors, quieter, memory, restarting]}
    level: {enum: [warning, failed]}
    value: number, usual: number|null, limit: number|null, restarts: integer|null
    since: date-time
    deployNear: {appId, appName, version, endedAt, relation: {enum: [after, before]}, minutes: integer}
    lastExit: {copy, exitCode, at}            # memory and restarting
    distance: number
ServiceMetrics:
  properties:
    appId, appName, service, copies: integer, hasDomain: boolean, state: {enum: [unusual, rolling_out, healthy, stopped]}
    requests: number|null, errors: number|null, p95: number|null, usualP95: number|null
    cpu: {type: array, items: number|null}     # the sparkline, per point
    cpuReservedMhz: integer, memoryMb: number|null, memoryLimitMb: number|null, restarts: integer|null
    unusual: boolean, distance: number|null
Host:
  required: [memory, cpu, disks, room, sampledAt, baselineSince]
  properties:
    memory: {used, reserved, total}            # megabytes
    cpu: {used, reserved, total}               # MHz
    disks: {type: array, items: {mount, holds: [nomad|docker], used, total, free, usedPercent, state: {enum: [ok, warning, failed]}}}
    room: {count: integer|null, likeApp: string|null, memoryMB, cpuMHz, limitedBy: {enum: [memory, cpu]}|null, freeMemoryMB}
    history: {points: [date-time], memoryUsed: [number|null], memoryReserved: [number|null]}   # 7 days, hourly: the host chart
    sampledAt: date-time|null       # null: the sampler has never read the host
    baselineSince: date-time|null
```

- The attention item's schema gains `disk_nearly_full` in its `kind` enum, and `mount`,
  `usedPercent`, `freeBytes`, `level` (`AttentionItem` and the webhook notification component, 018).
- The live hub gains `metrics` (once a minute); the API's change stream forwards it like the others.
- No write operation: nothing in 025 is configured through the API (the buckets and the URL are host
  settings).

---

## R8. Lab checks to run (after the Traefik and policy changes, before the sampler)

Run on the lab on 2026-10-25 (T001), except 10, 12 and 13, which wait for the throwaway VM, the
sampler and the console. The only change to the lab's configuration was the feature's own: the
Traefik job re-registered by `deploy/dev/lab.sh up` with the two arguments. Each of items 1–9 and 11
ends with "Lab, observed". The throwaway objects were an app `spike025` (a whoami job registered
through Nomad's API with the router shapes 023 writes: plain, Team-only basic auth, a www redirect
pair, a per-domain router with a path) and two ACL policies and tokens `spike025-with` and
`spike025-without`; all were removed. The lab was short of memory the whole time (1.7–1.84 GB of
1.95 GB used, 34 MB free and 100–150 MB available), so the kernel's own out-of-memory killer took
Traefik down four times that day (`dmesg`: "dockerd invoked oom-killer … global_oom …
task=traefik"), one of them during check 4, and killed the Postgres backend of check 8's sort (anon-rss 82
MB); Traefik itself uses 17 MB of its 128. That is the lab's condition, not the feature's, but the
numbers of checks 3 and 4 carry it.

1. **Traefik's metrics on**: rerun the dev Traefik job with the two arguments; `curl
   http://127.0.0.1:8088/metrics` answers 200 with `traefik_service_requests_total{…,service="whoami-whoami@nomad"}`
   (check the exact label value and the `protocol` values: `http`, `websocket`, `sse`, `grpc`?);
   capture the whole answer as the parser's fixture (with the buckets set) and note its size and line
   count; does it carry `process_start_time_seconds`?
   - **Lab, observed**: after `lab.sh up`, `curl http://127.0.0.1:8088/metrics` answers 200. Before any
     request it is 131 lines, 8,820 bytes: `go_*`, `process_*`, `traefik_config_*` and
     `traefik_open_connections`, no `traefik_service_*` (a service's series appear with its first
     request). After one request to `whoami.localhost`:
     `traefik_service_requests_total{code="200",method="GET",protocol="http",service="whoami-whoami@nomad"} 1`,
     with `_request_duration_seconds_{bucket,sum,count}`, `_requests_bytes_total`,
     `_responses_bytes_total` and `_requests_tls_total{service=…,tls_cipher=…,tls_version="1.3"}`.
     The buckets are the eleven bounds and `+Inf` (`le="0.005"` … `le="10"`, `le="+Inf"`).
     **`protocol` values seen: `http`, `sse`** (`Accept: text/event-stream`) **and `grpc`** (a POST
     with `Content-Type: application/grpc` to whoami, which answered HTTP 200; the series says
     `code="2"`, the gRPC status, not an HTTP code). **`websocket` never appeared**: a `101` to
     whoami's `/echo`, with curl and with a raw client that sent a text frame and a close frame and
     closed cleanly, left no series 12 s later. `process_start_time_seconds` is there
     (`1.79296926318e+09`) **but is not a stable start time**: one process read 1792969410.57,
     406.57 and 405.57 on three scrapes within two minutes; the next 764.09, 763.09, 761.09 and,
     over the following half hour, 752.09, 749.09 and 746.09 (Nomad's own "Started" event said
     766): it drifts back about 1 s a minute. A restart moved it by 359 s.
     After 46 requests to five services: **446 lines, 47,152 bytes** (108 bucket lines), saved
     untrimmed (under 200 KB) as `internal/metrics/testdata/traefik.prom`; its services are
     `blog-web`, `hollo-web`, `lab023-web`, `spike025-web` and `whoami-whoami`, all `@nomad`, with
     codes 200, 503 and the grpc 2, and protocols `http`, `sse`, `grpc`. Decisions changed (plan):
     errors count only `http` and `sse`; `process_start_time_seconds` marks a reset only when it
     moves by more than 10 s; websocket traffic is not in the numbers.

2. **Which requests count on the service**: one Team-only domain (023's `team.lab023.localhost`)
   without a password (401) and a www pair (301): neither should move
   `traefik_service_requests_total{service="lab023-web@nomad"}`; a plain request should. A
   per-domain router's request should (finding 2).
   - **Lab, observed**: 023's `lab023-web` Team-only router was disabled in the lab (its auth
     variable is gone: `middleware "lab023-web-13ab8f6e-auth@file" does not exist`, answers 404), so
     the check ran on `spike025`, with the same shapes. `traefik_service_requests_total{service="spike025-web@nomad"}`
     started at 1; Team-only without a password (401) left it at 1; the www pair's 301 left it at 1;
     Team-only with the password (200) made 2; the plain domain 3; the per-domain router with a path
     4; the apex of the www pair 5; a host no router knows (Traefik's own 404) left it at 5.
     `traefik_entrypoint_requests_total{entrypoint="websecure"}` carried the 401 (1), the 301 (1)
     and the 404s (2) beside the 200s (5). So a middleware's answer and an unrouted request never
     reach the service's numbers (the "requests the app saw" meaning), and every per-domain router
     feeds the one service (finding 2 holds). The redirect regex sees the Host with its port, so the
     lab's `https://www…:8443/` is served, not redirected: the test sent the Host without the port.

3. **The bucket math against a known load**: a service with a path that sleeps a known time (e.g. 50
   ms and 400 ms in a 9:1 mix, `curl` in a loop for 2 minutes); the computed p95 per minute from the
   deltas should fall in the 250-500 ms bucket; compare with the client-side p95 of the loop.
   - **Lab, observed**: two runs of a sequential client, one request per round trip, nine
     `?wait=50ms` to one `?wait=400ms`, scraping once a minute and applying the plan's rules to
     the bucket deltas (running maximum, Prometheus interpolation):

     | Run, minute | Requests (client) | p50 buckets / client | p95 buckets / client |
     |---|---|---|---|
     | 1, minute 1 | 252 (240) | 82 / 83 ms | 856 / 1328 ms |
     | 1, minute 3 (spans the restart of item 4) | 92 (88) | 82 / 61 ms | 540 / 661 ms |
     | 1, minute 4 | 295 (293) | 83 / 84 ms | 594 / 764 ms |
     | 1, minute 5 | 382 (382) | 81 / 70 ms | 485 / 561 ms |
     | 2, minute 1 | 270 (265) | 85 / 88 ms | 625 / 880 ms |
     | 2, minute 2 | 319 (324) | 82 / 81 ms | 480 / 752 ms |

     The totals agree with the client's (the scrape and the loop don't start on the same second),
     the p50s within a few ms (but 82 against 61 in the minute with the restart), and **the
     bucket p95 is 14–36% below the client's p95 in every minute** (Traefik doesn't time the
     client's own work) and in the same or the next lower bucket. **It fell in the 250–500 ms
     bucket in 2 of the 6 minutes, not all**: the VM was starved (`CPUTicksConsumed` 3,818–3,961
     of 4,000 MHz while the load ran, about 450 idle; the "50 ms" requests had a p50 of 82 ms, and
     23% of all requests took over 100 ms where the mix has only its slow tenth above it), so the
     slow tenth reached into the 0.5–1 s bucket. The interpolation itself is covered by the unit tables, not by this run.
     No decision changes: a p95 reads as "within a bucket", the finer buckets do their job (a p95
     of 480–856 ms reads as that, not as the old default's 1.2 s).

4. **Resets**: restart Traefik (`job restart` or the job rerun) during the load; the next minute's
   delta must be the new value, not negative and not a jump.
   - **Lab, observed**: Traefik went down during run 1, twice: the VM's out-of-memory killer
     (the scrape at 122 s was refused) and my `alloc restart` at 150 s (exit 137, restarted by Nomad
     within 2 s: "Restarting Task restarting in 0s"; after the kernel's kill Nomad waited 15 s);
     the clients saw 123 failed requests in the minutes around them. A restarted Traefik has no `traefik_service_*` series until a service's first request.
     The first successful scrape after it: the `+Inf` bucket sum went from 259 to 92, so the reset
     rule's delta is 92, the requests since the restart: not negative, not a jump; the two minutes
     after it gave 295 and 382, within 2 of the client's 293 and 382. `process_start_time_seconds`
     moved by 359 s. That scrape came about 120 s after the last good one (a scrape in between
     had been refused), over the plan's 90 s window, so the sampler would only re-baseline and
     store no HTTP numbers for that minute, as planned.

5. **A series that vanishes**: a stop-then-start deploy of a volume app with a domain: does its
   service's series disappear from `/metrics` and come back at zero?
   - **Lab, observed**: stopping `spike025` (Deregister, no purge: the same disappearance of the
     registration as a volume app's stop-then-start) left its series in `/metrics` 12 s later
     (933 requests, 18 lines) and **gone 32 s later** (0 lines); a request in between answered 404.
     After the start the first 200 came 6 s later, and the series came back **at 1**, then 2: from
     zero, as a new series. Other services' series were not touched. A "now less than previous"
     (1 against 933) would also give a delta of 1, so the plan's two rules agree; the sampler should
     still drop a series' memory when it is absent from a scrape.

6. **The scoped token reads the host**: a token from `deploy/install/caravana-policy.hcl` with
   `node { policy = "read" }` (as `TestLab` makes one): `Nodes().List`, `Info`, `Stats` answer; without
   the line, 403 (finding 8).
   - **Lab, observed**: a token from `deploy/install/caravana-policy.hcl` as it stands, and one with
     `node { policy = "read" }` appended:

     | Call (`/v1/…`) | Without | With |
     |---|---|---|
     | `Nodes().List` `nodes` | 403 `Permission denied` | 200, 1 node |
     | `Nodes().Info` `node/<id>` | 403 | 200 (`MemoryMB` 1949, `CpuShares` 4000) |
     | `Nodes().Stats` `client/stats?node_id=` | 403 | 200 (`Memory.Used` 1,826,328,576) |
     | `Allocations().List` `allocations?resources=true` | 200 | 200 |
     | `Jobs().Info` `job/whoami` | 200 | 200 |

     Finding 8 stands; the reservations need no node capability.

7. **Reserved by Nomad's rule**: the sampler's lifecycle-aware sum equals
   `nomad.client.allocated.cpu` / `.memory` in `/v1/metrics` (lab, observed today: 1730 / 1312 against
   a naive 1880 / 1408).
   - **Lab, observed** (18 non-terminal copies on 2026-10-25): the lifecycle-aware sum (per copy
     `max(Σ prestart non-sidecar, Σ main and sidecar)`, task lifecycles from the job's versions)
     was **1780 MHz and 1344 MB**; Nomad's `nomad.client.allocated.cpu` was **1780** and
     `allocated.memory` **1344**, exact, against a naive sum of 1930 and 1440. The prestart tasks
     that matter were `caravana-volumes` on `db`, `files-vol` and `nobackup` (50/32 against a
     service's 100/96, 100/32, 100/32); `names` sidecars sit with the main tasks. The unallocated
     gauges were 2220 and 605 (4000 − 1780, 1949 − 1344), and `allocated.max_memory` was 4576:
     the gauge for memory leaves `memory_max` out, as the room's yardstick does. The node's
     `ReservedResources` is 0 / 0.

8. **Memory under cache**: the `db` Postgres under a read load (e.g. a `pgbench -S` run in the copy,
   or a large `SELECT`): `Usage` climbs towards the 192 MB ceiling while `Usage − Cache` stays near
   the working set; no OOM.
   - **Lab, observed** (`db`, `postgres` task, 96 MB reserved, ceiling 192): at rest Usage 17.3 MB,
     RSS 4.1, Cache 11.6, **Usage − Cache 5.6**. Reading 286 MB of the image's files through the
     container twice moved Cache to 16.8 and 21.8 and Usage to 23.6 and 28.4, while **Usage −
     Cache stayed 6.8 and 6.6**. The data directory is 39 MB. A sort in `work_mem` (`set work_mem='80MB'`, 900,000 rows) took Usage to
     86.0 MB, RSS 66.4, Usage − Cache 69.9: anonymous memory moves both, as it should. **Not
     reproduced**: Usage climbing towards the 192 MB ceiling, because the page cache this cgroup was
     charged for was only about 10 MB (the image's files were charged to whoever read them first),
     and the backend of that sort was then killed by the VM's out-of-memory killer ("connection to
     server was lost"; `dmesg` shows a `postgres` of 82 MB anonymous memory killed by the global
     killer, 34 MB free in the VM; the container didn't restart, its restart count was 1 from an
     earlier kill that day). The check shows what `Usage − Cache` is for (cache doesn't
     count), not the behaviour at the ceiling.

9. **Disk fields**: `AllocDirStats` matches the `/` entry (today: same `Size`, same `Used`); on a host
   whose `/opt/nomad/data` or `/var/lib/docker` is a separate mount, the match and the longest-prefix
   rule pick it; `/mnt/lima-cidata` (100%) is never watched.
   - **Lab, observed**: `AllocDirStats` is `Size` 19,682,557,952, `Used` 6,235,652,096,
     `UsedPercent` 31.71 (and an `InodesUsedPercent` 7.27); the `DiskStats` entry `/dev/vda1` on `/`
     has the same `Size` and the same `Used` (one collection), and it is the only entry that
     matches. `/boot` (23.6%), `/boot/efi` (6.7%) and `/mnt/lima-cidata` (`Size` 19,922,944,
     `Available` 0, 100%) don't match and aren't watched. The lab has one filesystem for
     `/opt/nomad/data` (dynamic host volumes at `/opt/nomad/data/host_volumes/<uuid>`) and
     `/var/lib/docker`, so **the separate-mount match and the longest-prefix rule are not
     exercised here** (the unit table covers them). The node's attributes have no Docker root
     (`driver.docker.version` 29.1.3, `.bridge_ip`, `.os_type`, `.runtimes` only), as R3 said.

10. **Disk nearly full**: filling the lab's 20 GB `/` is slow and risky for the lab, and a loop
    filesystem mounted elsewhere isn't a watched one. Use a throwaway VM (`deploy/dev/install-test.sh`'s
    Lima VM) with a small disk, filled past 85% and 95% with `fallocate`, then freed: the item, its
    level, the notification on open and its "resolved" after `resolveAfter`; the hysteresis at 84%.
    The rest is a table test of `attentionOf` over stored rows.
   - **Lab, observed** (2026-10-26, T020): a throwaway Lima VM `caravana-disk-test` (Ubuntu 24.04,
     2 CPUs, 2 GiB, `--disk 10`: `/` is 8.7 GB, 2.8 GB used after the install), installed from a dev
     release of `025-monitoring` the way `deploy/dev/install-test.sh` does (`--acme none`), then
     `CARAVANA_NOTIFY_RESOLVE_AFTER=10s` added to `caravana.env` and Caravana restarted; a webhook
     channel (`problems`) to a receiver inside the VM on `127.0.0.1:9099`. The sampler kept its
     minute. Docker's root was `/var/lib/docker`, on `/` with Nomad's data: one watched filesystem,
     `holds` `["nomad","docker"]`, 31.3% before the fill. Each stage is one `fallocate` file sized to
     the percent asked (used / (used + available), as `df` and Caravana compute it); the times are
     from the file's write.
     - **87%**: the next tick's `getHost` read 87.0%, 1,149 MB free, state `warning`; the needs-you
       item `disk_nearly_full` came 56 s after the write at level `warning`, "The disk at / is 87%
       full: 1.1 GB left." (`freeBytes` 1,204,813,824), and the webhook's `problem.opened` 5 s after
       it: title "Disk nearly full: / is 87% full", that text, `level` warning, `mount` "/", `holds`,
       `usedPercent` 86.999…, `freeBytes`, `url` `https://caravana.test/monitoring#host`.
     - **96%**: 56 s later the item was `failed`, "The disk at / is 96% full: 353 MB left."; `getHost`
       state `failed`. **No second message** over the next 70 s (two notifier checks) and none after:
       the item's key carries no level, so the rise to ■ isn't notified.
     - **84%**: after 100 s the item **stayed open**, at `warning`, "… 84% full: 1.4 GB left." (the
       hysteresis), while `getHost`'s disk state for the same sample read **`ok`** (the meter's
       state follows the 85% threshold alone, as `DiskState`'s comment says). The meter and the
       needs-you row disagree for that band: worth the designer's look in T022.
     - **81%**: the item was gone 11 s after the write (the next tick), and `problem.resolved` came
       at once after `resolveAfter`: title "Resolved: Disk nearly full: / is 87% full" (the percent
       of when it opened), text "It is no longer on the needs-you list.", the same `url`.
     Two messages in all, one `opened` and one `resolved`. The VM was deleted after. "The disk at
     / …" is the API's `detail` and the messages' text (the notify goldens' wording); the console's
     needs-you row draws its own sentence ("The disk holding Nomad's data and Docker's (/) …",
     `internal/ui/attention.go`), not looked at here (no browser on the VM). A Docker root on its
     own filesystem, two watched mounts, isn't exercised (the unit tables cover it, as in item 9).
11. **Allocation stats with `publish_allocation_metrics`**: already on in both configs
    (`PublishAllocationMetrics: true`, lab, observed); a check that a node without it gives
    `ErrUsageNotCollected` rows as NULL memory and CPU, not 0.
   - **Lab, observed**: not run as written: turning `publish_allocation_metrics` off needs a Nomad
     restart on the lab that holds the 18 review apps. What the API does give for a copy with no
     collected stats (a `complete` and a `failed` Traefik allocation): `Timestamp` 0 and **one entry
     per task** with a zeroed `ResourceUsage` and `Measured` null, not an empty `Tasks` map.
     014's `usageFromStats` only says "not collected" for `len(Tasks) == 0 && Timestamp == 0`, so
     it would read that answer as 0 MHz and 0 MB, not NULL. Whether a running copy on a node without
     collection answers the same way is unverified. Plan changed: `Timestamp` 0, or no measured
     memory kind, reads NULL (see "Memory used").

12. **The ring buffer after a day**: run Caravana in the lab with `SetMetricsSampleGap` at a minute
    for 24 h against ~15 services: row counts by `res`, the file's growth (`caravana.db` size, the WAL),
    the sampler's time per tick (log it at debug), and Caravana's RSS before and after.
13. **Where steps go**: a console step `console-monitoring` in `TestConsoleLab` (a service with a slow
    path and a 500 path driven by a loop; with seeded history rows for the band; the issues panel, the
    table, the host panel; Traefik unreachable by pointing the URL at a closed port) and a gate step
    `monitoring` (the API reads with the gate's token, a deploy token's 403, the host's numbers
    non-zero, the scoped token's `node:read`), as 022-024 did.

---

## Summary of decisions

| Question | Decision |
|---|---|
| R1 Traefik | `--metrics.prometheus=true` and finer buckets on the existing loopback entrypoint `traefik` (`127.0.0.1:8081`; lab `127.0.0.1:8088`, no forward change); service labels only (every per-domain router shares the service, lab, observed); read `traefik_service_requests_total` and `_request_duration_seconds_bucket` for `<app>-<service>@nomad`; our own ~120-line text parser, **no new module** (expfmt would bring three); a Traefik restart on each host when the installer reruns |
| R2 percentiles | Store per-minute bucket deltas, not p95s; Prometheus' interpolation at read time over merged buckets (environment, 10-minute, range points); resets as Prometheus; buckets `0.005 … 10` s (defaults would read any fast app as 95 ms); latency leaves out websocket and SSE |
| R3 Nomad | 014's per-copy stats (13 ms each), memory as `Usage − Cache` (and the Running tab with it); host from `Nodes().Stats` and `Info`; reserved by Nomad's lifecycle rule (1730/1312, not 1880/1408); disks: `AllocDirStats` (Nomad's data) and `CARAVANA_DOCKER_ROOT` (default `/var/lib/docker`) by longest mount prefix, never every mount; policy gains `node { policy = "read" }`; room by reservation (lab: 19 more like `blog`) |
| R4 store | Migration `0019_metrics.sql`: `metrics_series`, `metrics_layouts`, `metrics_samples` (res 60 and 600, overlapping, `WITHOUT ROWID`), `metrics_host`, `metrics_disks`, `metrics_state`; NULL for a source not read; fold each 10-minute slot when it ends, prune every 10 minutes; 3-8 MB for 20 services (Mac, measured: 7.8 MB with buckets in every row, 2.8 MB without); one sampler goroutine on wall-clock minutes in `Wait`'s group |
| R5 band | On read: min..max and median of the same UTC 10-minute slot on the previous 1-7 days; window sums and merged buckets for the rules (≥ 20 requests for "slower"); start found by walking back ≤ 6 h; deploy clause from healthy deployments' end within 15 min |
| R6 needs-you | `disk_nearly_full`, host-level, one per filesystem, key `disk_nearly_full|||<mount>`, 85%/95% with a stored-history hysteresis (83%), not judged when the newest host sample is over 5 minutes old; restarts from one shared `crashesInHour` (3 for needs-you, 2 for monitoring) |
| R7 API | `getMetrics`, `getAppMetrics`, `getHost` (0.27.0), `null` for gaps, `sources` flags for the notices, `disk_nearly_full` in the attention kinds; deploy tokens denied by default |
| R8 lab | Metrics on and the fixture, which requests count on a service, bucket math under a known load, resets, vanishing series, the scoped token's `node:read`, reserved against Nomad's gauges, memory under cache, disk fields and fill, a day of the ring buffer |

Installer and host changes the feature needs: two Traefik arguments (install and dev jobs), `node {
policy = "read" }` in `caravana-policy.hcl`, `CARAVANA_DOCKER_ROOT` written to `caravana.env` (and
optionally `CARAVANA_TRAEFIK_METRICS_URL`, which defaults to the right address). Nothing in
`nomad.hcl.tpl`: host stats are collected whether or not `publish_node_metrics` is on
(`nomad:client/client.go:3235-3256`), and `publish_allocation_metrics` is already set. No new Go module.
