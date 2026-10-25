# Quickstart: Monitoring and capacity (025)

How to see the feature work end to end. Details are in [plan.md](./plan.md),
[data-model.md](./data-model.md), [contracts/api.md](./contracts/api.md) and the page contract
[contracts/monitoring-screen.md](./contracts/monitoring-screen.md) with its rulings.

## Prerequisites

- The lab up with the 025 Traefik job: `deploy/dev/lab.sh up` reruns
  `deploy/dev/traefik.nomad.hcl`, which now carries `--metrics.prometheus=true` and the bucket
  list (Traefik restarts once). `curl -s http://127.0.0.1:8088/metrics | grep -c
  traefik_service_requests_total` prints more than 0 once any app has had a request.
- Caravana running with the lab's metrics address and a short gap for a quick look:
  `CARAVANA_TRAEFIK_METRICS_URL=http://127.0.0.1:8088/metrics go run ./cmd/caravana` (the gap
  stays a minute outside tests), an admin and a full token (`admin init`, `admin create-token`),
  signed in at `http://127.0.0.1:7070/`.
- Caravana's Nomad token made from `deploy/install/caravana-policy.hcl` (as `TestLab` makes one),
  so `node:read` is what a host has.
- Three apps in one place: `whoami` (`traefik/whoami`, domain `whoami.localhost`, health path `/`),
  `slow` (the same image, domain `slow.localhost`, health path `/`), and `quiet` (busybox `sleep`,
  no domain).

## 1. A fresh install: the baseline

- `/monitoring` within the first minute: no rows yet; after a minute the charts start, with no
  band and no "usually", each caption "Building the baseline: started 2026-10-25 UTC. The usual
  range appears after a full day.", and the line "The usual range is still being learned: started
  2026-10-25 UTC." with ○.
- The table: `whoami`, `slow` and `quiet` by name; `quiet` with "—" in Requests, Errors and Slowest
  5 % and "· no domain" under its name; CPU sparklines and memory meters for all three.
- The footer: "Sampled every minute and kept for 8 days. Requests come from Traefik, memory and
  CPU from Nomad." and "Long-lived connections, such as websockets and streams, count as requests
  but not in Slowest 5%."

## 2. With history: the band and "usual"

- Seed 7 days of history for the stored apps (Caravana may keep running):
  `CARAVANA_SEED_METRICS=./data/caravana.db go test ./cmd/caravana -run TestSeedMetricsForReview -v`
  (opt-in; writes 10-minute rows shaped by a daily curve for every stored service with a domain,
  and moves `metrics_state.started_at` 8 days back).
- Reload: each chart has its band and "usually N"; the 24 h range shows the daily curve in the
  band; on 7 d the band thins to the left with the caption "The band thins to the left: older
  hours have fewer earlier days kept." in fg-3, and the band's title names its days.
- Seed only 3 days (`CARAVANA_SEED_DAYS=3`): "usually 180 · over 3 days".

## 3. The issues panel: a load script

Make `slow` slow and make it answer 500 to some requests, and call `whoami` steadily:

```sh
curl -sk -X POST -d 500 https://slow.localhost:8443/health          # /health now answers 500
while true; do
  for i in 1 2 3 4 5 6 7 8 9; do curl -sk -o /dev/null 'https://slow.localhost:8443/?wait=50ms'; done
  curl -sk -o /dev/null 'https://slow.localhost:8443/?wait=400ms'
  curl -sk -o /dev/null https://slow.localhost:8443/health
  curl -sk -o /dev/null https://whoami.localhost:8443/
done
```

- Within 2 minutes: `slow` first in the table (◆, "… ms · usually …" in the warn hue), an issues
  row "`slow` is slower than usual. The slowest 5% of requests take **N ms**, usually M ms. It
  started at HH:MM UTC." with its detail line and "Show `slow` errors" (to
  `/logs?app=…&levels=error`), and a "more errors" row with "N errors in the last 10 minutes".
  Above 10 % errors the mark is ■.
- Deploy `whoami` with a new tag, then restart the loop: the issue sentence gains " It started
  N minutes after `whoami` vN rolled out."; the charts have a dashed marker "`whoami` vN".
- Take the `whoami` call out of the loop: the seeded band's bottom is at least 1 request a
  minute, so after 10 minutes "`whoami` has much less traffic than usual: …" with "Its domains:
  `whoami.localhost`".
- Memory: deploy `quiet` with `memoryMB` 32 and `memoryMaxMB` 34 running `sh -c 'head -c 31m
  /dev/zero | tail'`: "`quiet` is using **9N%** of its 34 MB memory limit." (◆, ■ above 98 %).
- Restarting: a busybox service that exits 1 after 20 s: after the second restart, "`…` restarted
  2 times in the last hour." with the last exit; at 3 the needs-you list's `restarting` item too.
- The sort bar: "most traffic" and "most memory" reorder; each is a link.

## 4. The host

- `/monitoring#host`: the cpu, mem and disk meters with reserved hatched, readouts "x/4 vCPU",
  "x/3.8G", "x/20G" (the lab's `/` holds both Nomad's data and Docker's root, so one disk meter);
  `/mnt/lima-cidata` (100 %) is not there.
- The room sentence: "Room for about N more apps like `…` (32 MB, 100 MHz each)." with the title
  "Nomad's count of what fits. Caravana's and the host's own processes aren't reserved."; check N
  by hand against `nomad.client.unallocated.memory` and `.cpu` on the VM's `/v1/metrics`.
- The 7-day memory chart: used and reserved (dashed).
- The Apps home: the "This host" panel above Activity with "● 1 server up", the same numbers,
  linking to `/monitoring#host`.

## 5. Errors and the API

- Traefik off: point `CARAVANA_TRAEFIK_METRICS_URL` at `http://127.0.0.1:8088/nothing` (404):
  "Traefik's request numbers are off on this host. Run the installer again to turn them on."; at
  a closed port: "Can't read Traefik's numbers right now." with ○; the stored history stays drawn.
- A token without `node:read`: the host section and the Platform panel say "Caravana's Nomad
  token can't read the host. Run the installer again."; after 5 minutes the panel's "■ Can't read
  the host's numbers".
- `GET /v1/metrics?project=default&environment=production&range=24h` with the token: the points,
  the series with `low`/`high`/`usual`/`days`, the issues sorted by distance, `null` in the gaps;
  `GET /v1/apps/slow/metrics`; `GET /v1/host`; a deploy token on each → 403 `token_scope`;
  `range=2h` → 422.
- Without JavaScript (curl the page): every bar is a link, the heads show "now", no crosshair.

## 6. The lab steps

- **The spike first** (research R8.1–R8.9 and R8.11, task T001): the dev Traefik job with the two
  arguments; `/metrics` answers 200 with `service="whoami-whoami@nomad"`; the exact `protocol`
  values (`http`, `websocket`, `sse`, `grpc`?) and whether `process_start_time_seconds` is there;
  the answer saved as `internal/metrics/testdata/traefik.prom` with its size and line count; a
  Team-only 401 and a www 301 don't move the service's counter, a plain and a per-domain
  router's request do; the 9:1 load of 50 ms and 400 ms for 2 minutes, the computed p95 per minute
  against the loop's own; a Traefik restart during the load (the next delta is the new value); a
  stop-then-start deploy of a volume app with a domain (does its series vanish and come back at
  zero?); the scoped token on `Nodes().List`, `Info`, `Stats` with and without `node { policy =
  "read" }`; the lifecycle-aware reserved sum against `nomad.client.allocated.cpu` / `.memory`;
  the `db` Postgres under a read load (`Usage` climbs, `Usage − Cache` stays near the working
  set); `AllocDirStats` against the `/` entry; a node without `publish_allocation_metrics` gives
  NULL, not 0. Recorded in `research.md` as "lab, observed" before the sampler is written.
- **The disk fill** (R8.10): `deploy/dev/install-test.sh`'s throwaway Lima VM with a small disk,
  filled with `fallocate` past 85 % (the item, ◆, its "opened" notification to an ntfy or webhook
  channel), past 95 % (■, no second "opened"), freed to 84 % (stays), below 83 % (resolved after
  `resolveAfter`).
- **`TestConsoleLab` step `console-monitoring`** (`cmd/caravana/console_monitoring_test.go`):
  `startLab` sets the gap to 10 s; the `slow`, `whoami` and `quiet` apps of the prerequisites;
  seeded history rows through the store for the band; the load script's calls made from the test
  for 2 minutes; the page's issues panel (slower, more errors, with the deploy clause after a
  deploy during the step), the table's order and "—" for `quiet`, the four charts' SVG with a band
  polygon and a marker, the host section and the Platform panel, the sidebar item, the range and
  sort links, the no-script heads, the `metrics` part swap, Traefik unreachable (the URL pointed
  at a closed port through the service's setter); Nomad unreachable through the fakes in a unit
  test (the lab's Nomad isn't stopped).
- **The gate step `monitoring`** (`cmd/caravana/gate_monitoring_test.go`): with the gate's token,
  `getMetrics`, `getAppMetrics` and `getHost` against a gate app called in a loop: requests
  non-zero and matching the loop's count within a minute's rows, the host's memory, CPU and disk
  non-zero, reserved equal to Nomad's allocated gauges, the scoped token reading the host
  (`node:read`), a deploy token's 403 on each, `range=2h` 422, `null` (not 0) in the first
  minute's HTTP values; on a real host through `host.sh prove`, the same against the public
  console domain.
- **A day of the ring buffer** (R8.12, during the review): Caravana in the lab at a minute's gap for
  24 h against ~15 services: rows by `res`, `caravana.db` and WAL growth, the tick's time (logged
  at debug), Caravana's RSS before and after.

## Checks before calling it done

`go build ./...`, `go vet ./...`, `go tool staticcheck ./...`, `gofmt -l .` empty, the two
cross-compiles, `go mod tidy -diff` (no new module), `tools/ui-css.sh check`, `tools/ui-vendor.sh
check`, shellcheck (the installer and the lab scripts changed), `go test -race ./...`,
`TestConsoleLab` (every step) and the gate step `monitoring` in the lab, and
`deploy/dev/install-test.sh` (the Traefik restart message, the policy line, `CARAVANA_DOCKER_ROOT`).
