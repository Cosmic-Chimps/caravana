# Implementation Plan: Network and disk I/O per server

**Spec**: [spec.md](./spec.md) · **Tasks**: [tasks.md](./tasks.md)

## Summary

The node agent (031) reads `/proc/net/dev`, `/proc/diskstats` and `/proc/self/mountinfo` and adds an optional `io` block
of cumulative counters to every sync. The console keeps each server's previous sample in memory, makes rates from two
samples 20 to 120 seconds apart, and the 025 sampler writes each server's fresh rates into its host row (migration
**0044**, five nullable columns). `getHost` gains `io` (API **0.61.0**), the server page a **Network and disk** section,
and Monitoring's host section two charts. No new Go module, no new runtime dependency, no policy change, no new route,
constitution unchanged.

## Decisions

- **D1 The agent reads, the console divides.** The agent runs once every 30 seconds and keeps nothing; counters since
  boot are the only honest thing it can send. The rate math is pure and tested on the console (`service/hostio.go`).
- **D2 Files are read under the agent's root** (`a.path`, like `/proc/meminfo` already is), so tests use a temporary
  folder with fixtures in `internal/nodeagent/testdata/proc/`. The parsers (`parseNetDev`, `parseDiskstats`,
  `parseMountinfo`) are pure functions of text.
- **D3 Which interfaces.** The `dev` of the default route (`State.PublicInterface` when the state has it, else
  `ip -4 route show default`, the way `init-first` reads it) and `caravana-mesh` when present. Never `lo`, `veth*`,
  `docker*`, `cni*`, `br-*`, `nomad` or `caravana0`, even if a default route names one. At most 4.
- **D4 Which devices.** The device of the mount that holds `/opt/nomad/data` and the one that holds Docker's root
  (`docker info --format {{.DockerRootDir}}`, `/var/lib/docker` when it doesn't answer a path), found by the longest
  mount point in `/proc/self/mountinfo` and its `major:minor` in `/proc/diskstats`: one entry when it is one device,
  with `holds` saying `nomad` and/or `docker`. Loop, RAM and zram devices are skipped, and so is a mount that has no
  line in `diskstats` (btrfs, overlay). At most 4.
- **D5 Bytes on the wire.** The agent sends bytes (`sectors × 512`) rather than sectors, so a reader of the API
  or the stored report needs no unit lore; `busyMs` is the kernel's `io_ticks`. A counter that doesn't fit a JSON
  number the console reads (above 2^63) leaves its entry out.
- **D6 Validation never fails a sync.** `NodeIO.UnmarshalJSON` marks a block that doesn't decode `Invalid` instead of
  failing the whole report; `checkIO` (names ≤ 64 printable bytes, ≤ 4 interfaces and devices, kinds and holds from a
  fixed set, counters non-negative, no duplicate names, the block's time within 10 minutes of the console's) says why
  a block can't be used and the sync drops it with one log line (the reason, never the agent's text). The io block is
  not stored in the server's report.
- **D7 Rates.** `ioRates(prev, cur)`: only when the samples are 20 to 120 s apart by the agent's own clock; a counter that
  went down is a reset for that interface or device alone; bytes per second rounded; busy % = Δbusy_ms / Δt, capped at
  100. Headline numbers: network is the **default-route interface's** (deviation from "summed over the reported
  interfaces": the mesh's bytes leave on that interface again, so summing counted them twice; the mesh is listed
  beside it); disk read and write are summed over the devices, busy is the busiest device's.
- **D8 State is in memory.** `hostIOState` keeps per server index: the last sync time, whether it had a usable block,
  the previous block and the newest rates. Fresh for 3 minutes. After Caravana restarts the first rates come with the
  second report. A server's removal forgets it.
- **D9 Reasons.** `no_agent` (no row: an unmanaged node), `agent_too_old` (the agent syncs, no usable block),
  `stale` (no sync for 3 minutes, with `since`), `waiting` (an addition to the brief's three: one sample so far, a gap
  too long, a reset, or Caravana just started). A number that isn't known is `null` in the API, NULL in the database,
  "—" on the pages.
- **D10 Storage.** Migration `0044_host_io.sql` adds `net_rx_bps`, `net_tx_bps`, `disk_read_bps`, `disk_write_bps`,
  `disk_busy_pct` (INTEGER, NULL) to `metrics_host`. The sampler writes a node's fresh rates by mapping the node to
  its server row (`serverNode.Index`, set by `buildServerDirectory`); `foldHosts` takes the median of each column like
  the others; retention is the host rows'.
- **D11 Reads.** `Host` has `IO HostIO`; `HostIOSeries(server, range)` reads the host rows (minute rows for 1 h and 6 h,
  slot rows plus the open slot folded for 24 h and 7 d) into points of the range's step, the median of each column.
- **D12 API.** `getHost.io` (schema `HostIO`): `reason`, `since`, `at`, `network{rxBytesPerSec, txBytesPerSec,
  interfaces[]}`, `disk{readBytesPerSec, writeBytesPerSec, busyPercent, devices[]}`. No new route, so the access tables
  (054) need no new rule: `GET /v1/host` is already ruled.
- **D13 Console.** Server page: a `srv-io` section in the body, rows built by `ioSectionOf` (values in B/s, KB/s, MB/s
  by the console's byte formatter, names in mono, "—" rows with one sentence for a reason). Monitoring: two charts under
  the memory chart with the existing plot helpers (`ioChartOf`), the line of the reason when there is none. No new CSS: the
  section reuses the Space section's classes and the charts the memory chart's. Both work without JavaScript.

## Constitution Check

- **I Small**: no module, no service, no runtime dependency; reads three files the kernel keeps. ✓
- **II One model**: no change to `app.App` or the translator. ✓
- **IV Fail loudly**: an unknown number is a dash with its reason, never a zero; a dropped block is logged. ✓
- **V API-first**: `api/openapi.yaml` 0.61.0 changes in the same commit; the console reads `service.Host` and
  `service.HostIOSeries` only. ✓
- **VI Nomad-native**: untouched; the agent is not Nomad. ✓
- **VII Secure by default**: the agent takes nothing from the console for this; counters are not secrets; log lines hold
  reasons and counts, never the agent's text. ✓
- **VIII Plain Go**: table tests for every parser and the rate math; fixtures in `testdata/`; errors returned. ✓
- Governance: the feature is in Phase 3's cluster work (per-server monitoring) and was asked for by the maintainer.

## Tests

Agent parsers on real-format fixtures (odd spacing, counters near 2^64, an escaped mount point, a line without a colon),
report building with a fake root; `checkIO`, `ioRates` (reset, gap, too close, cap, sums), `hostIOState` (waiting, known,
stale, too old, forget); the sampler writing values and NULLs; the fold; `HostIOSeries`; the store column round trip and
migration over a store from 0043; `getHost.io` against the contract through a real agent join; the server page and
Monitoring for known data and each reason. `deploy/dev/install-test.sh` has `check_host_io` (not run in this session).
The lab's node is unmanaged, so a `TestConsoleLab` step could only check the `no_agent` reason, which the UI tests cover.
