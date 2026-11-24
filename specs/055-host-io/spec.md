# Feature Specification: Network and disk I/O per server

**Feature Branch**: `055-host-io` · **Created**: 2026-11-24 · **Plan**: [plan.md](./plan.md) · **Tasks**: [tasks.md](./tasks.md)

## Why

Monitoring (025) and the server pages (027, 031) show CPU, memory and disk *space* per server from Nomad. Nomad's host
stats have nothing about the network or about how hard the disk works, which is what the owner of a small VM most lacks
when a site is slow ("is the box out of bandwidth, or is the disk the bottleneck?"). Every managed server already runs
Caravana's root node agent, which syncs a report every 30 seconds. The maintainer decided (2026-11-24) that the agent
reads `/proc` and reports the counters through that sync: no new dependency, no new login. A separate monitoring add-on
(Beszel) was considered and put in the backlog.

## User stories

### US1 (P1): see a server's network and disk throughput now
The owner opens **Operator › Servers › (server)** and sees, in a **Network and disk** section, the bytes per second in
and out of the server's public network interface, the bytes per second read from and written to the disk that holds
Nomad's data and Docker, and how busy that disk is, with the interface and device names. Numbers are averages over the
last half minute and are labelled in B/s, KB/s or MB/s.

**Acceptance**: with an agent that reports, the section shows the five numbers within about a minute of the agent's
second report; the interface of the default route and the device holding Nomad's data are named; the mesh interface
(`caravana-mesh`) is listed beside them and is not added to the network total.

### US2 (P1): know why there are no numbers
A server whose numbers can't be known says why in one sentence and shows a dash, never a zero: a node Caravana doesn't
manage (the lab's) *needs Caravana's agent on the server*; an older agent *is older, run the installer again*; an agent
that stopped reporting says *no report for N minutes*; the first minutes after a start say it is *waiting for the
agent's next report*.

### US3 (P2): see them over time
**Monitoring › This host** draws two small charts over the page's range (1 hour, 6 hours, 24 hours, 7 days): network
(in solid, out dashed) and disk (read solid, write dashed), with a hidden table for screen readers. A minute without
a reading is a gap.

### US4 (P2): read them through the API
`GET /v1/host` (and `?server=`) has an `io` object with the same numbers and the reason when there are none.

## Requirements

- **FR-001** The agent reports cumulative counters read from `/proc/net/dev` and `/proc/diskstats` in an optional
  block of its report; the report is unchanged for an old console, and an old agent's report is accepted.
- **FR-002** Only the default-route interface, `caravana-mesh`, and the block devices that hold Nomad's data folder
  and Docker's root are reported; never the interfaces that come and go with apps.
- **FR-003** A block the console can't use is dropped with a log line and never fails the sync.
- **FR-004** Rates are made from two reports 20 to 120 seconds apart; a counter that went down makes no rate for that
  pair; a number that isn't known is null/NULL/"—", never 0.
- **FR-005** The sampler stores each server's fresh rates in its host row; the 10-minute fold takes the median.
- **FR-006** The API is 0.61.0: `getHost.io`. No new route.
- **FR-007** No secret, command, path or address is taken from the console by the agent for this; nothing is logged
  but reasons.

## Out of scope

Alerts or needs-you items for I/O; per-container I/O; IOPS and latency; a monitoring add-on (Beszel); history of a second
device beyond the sums; a threshold setting.
