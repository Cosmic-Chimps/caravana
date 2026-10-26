# Contract: 025 Monitoring and capacity, the `MonitoringScreen` and the host's capacity

Written by the caravana_designer session on 2026-10-25, before the spec. The values come from
the handoff's `MonitoringScreen.jsx` (`MnChart`, the issues panel, the four charts, the apps
table), `DevHome.jsx`'s Platform panel, `NowScreen.jsx`'s Capacity panel and the `Meter`
component, and `theme/caravana.css`, rendered from the prototype
(`design/console/_shots/pb-monitoring.png`, `pb-now.png`, `p4-home.png` for the Platform panel).
Where this list and the handoff disagree, this list wins. The 021 to 024 contracts still apply
(shell, fields, selects, icons, page head, place bars, the time rule: UTC first, the viewer's
local time beside it; the console never says "ship"). Multi-server capacity (`CapacityScreen`,
node panels, "what fits" per server) is Phase 3.

## What 025 is (the calls the backend follows)

- **The first stored samples.** A small SQLite ring buffer (migration `metrics_samples`), fed
  once a minute by a sampler goroutine in the service:
  - **per service of each running app** (key `app/service`): requests (count in the minute),
    5xx responses (count), the slowest 5% (p95 latency in ms, from the minute's histogram
    buckets), CPU (MHz used, summed over the service's copies), memory (MB used, summed), memory
    limit (MB, the reservations' `memory_max` or reservation summed), restarts (count in the
    minute);
  - **for the host**: memory used, memory reserved, memory total; CPU used, CPU reserved, CPU
    total; disk used and total for each watched filesystem (below).
  - **Resolution and retention:** one row per minute for the last 48 hours, then folded into
    10-minute rows kept for 8 days (7 days of "same hour" plus today). Older rows are deleted by
    the sampler. At 20 services this is about 20 × (2,880 + 1,152) rows: small.
  - **Sources:** Traefik's Prometheus metrics on loopback (the research picks the entrypoint and
    the labels; requests, 5xx and latency buckets per router, summed per service, since 023
    may give a service several routers); Nomad's allocation stats (the ones the Running tab
    reads, already enabled by `publish_allocation_metrics`) for CPU and memory; Nomad's node
    stats and allocated resources for the host. A service with no domain has no HTTP numbers:
    its requests, errors and slowest 5% are "—" and only CPU, memory and restarts are kept.
  - A sampler minute that can't read a source writes no row for it (a gap, drawn as a gap,
    never as zero).
- **"Usually" and the band.** For a point at time t in the range, the band is the lowest and the
  highest value of the same metric at the same time of day (the 10-minute row covering t) on each
  of the previous 7 days; "usually" is their median. Drawn as a filled band between the two, not
  an area from zero. Times of day are UTC (the time rule).
- **Before 7 days of history:** with at least one previous full day, the band is drawn from the
  days there are and "usually" reads "usually 180 · over 3 days"; with less than one full day,
  no band and no "usually", and each chart's caption reads "Building the baseline: started
  2026-10-25 UTC. The usual range appears after a full day." Nothing is called unusual until a
  full day exists, except the absolute rules below.
- **Unusual**, per service, over the last 10 minutes (the median of its minute values):
  - **slower**: the slowest 5% is above the band's top by more than 50% and by at least 100 ms;
  - **more errors**: 5xx are above 1% of requests, at least 5 errors in the 10 minutes, and
    above the band's top;
  - **quieter**: requests are below half the band's bottom while the band's bottom is at least
    1 request a minute (traffic that stopped);
  - **memory**: above 90% of the limit (absolute; no baseline needed);
  - **restarting**: 2 or more restarts in the last hour (absolute; the needs-you list already
    has crash loops at 3, so this is the earlier warning).
  A service is unusual when any rule holds; its **distance** is the largest of value / band top
  (slower, errors), band bottom / value (quieter), and memory / 90%. The apps table's "unusual
  first" sorts unusual services by distance, largest first, then rolling-out apps, then the rest
  by name. Unusual services are the page's issues panel; they are **not** needs-you items (that
  list stays for things that need a person; an unusual week is information).
- **"Room for about N more apps like X"** counts **reservations**, since Nomad places by
  reservation, not use: N = the smaller of (memory total − memory reserved) / X's memory
  reservation and (CPU total − CPU reserved) / X's CPU reservation, rounded down. The
  yardstick X is the running app whose total memory reservation is the median of the running
  apps' (ties: the alphabetically first), named with its size: "Room for about 4 more apps like
  `whoami` (256 MB, 100 MHz each)." N = 0 reads "No room for another app like `whoami`: memory is
  fully reserved." in the warn hue (or "CPU", whichever ran out); with no running app: "Nothing
  is running yet: 3.6 GB of memory is free to reserve."
- **Disk nearly full**: the sampler watches the filesystem holding Nomad's data dir (copies'
  files and host volumes) and the one holding Docker's data root (images), once each when they
  are the same filesystem. ◆ warning at 85% used, ■ failed at 95%. A needs-you item
  `disk_nearly_full` (one per filesystem, on the worse state): "The disk holding
  `/var/lib/docker` is 86% full: 11 GB left." with **Open monitoring** (a link to
  `/monitoring#host`). It notifies like every item.

## Where it lives

- **`/monitoring`**, a workspace page: the sidebar's Workspace group gains **Monitoring** (icon
  `activity`) after Logs. Crumb `Monitoring`.
- **The Apps home's rail** gains the handoff's **Platform** panel (DevHome), above Activity:
  `SectionHead` "This host", a `.cv-panel` with padding 18, column gap 14: "● 1 server up" 14px
  fg-1 with a 8px healthy mark (■ "Can't read the host's numbers" when the sampler failed), the
  memory `Meter` (label "mem", reserved hatched under used, readout "1.9/3.8G"), the disk
  `Meter` for the fuller watched filesystem (readout "18/40G"), and the room sentence 14px/1.5
  fg-2. It links to `/monitoring#host`.
- **The app's page**: no new tab. The apps table rows link to the app's Running tab.

## The page (`/monitoring`)

- **Page head** (page 3's shape): "Monitoring" 30px, "How your apps are doing compared with their
  usual week. The shaded band is the same hour over the last 7 days." 15px fg-2.
- **Filter row** (flex, gap 14, wrap): the project and environment bars (024's `seg`, only when
  more than one choice); a spacer; the range bar **1 h · 6 h · 24 h · 7 d** (default 6 h). The
  ranges map to points: 1 h = 60 one-minute points, 6 h = 72 five-minute points, 24 h = 144
  ten-minute points, 7 d = 168 one-hour points (each point the median of its rows).
- **Issues panel** (the handoff's): one row per unusual service, sorted by distance; grid
  `14px minmax(0, 1fr) auto`, gap 14, padding 16px 18px, 1px line-1 between rows: a 9px mark
  (◆ warning; ■ failed when errors are above 10% or memory above 98%); the sentence 15px/1.5 fg-1
  with the numbers in 500 weight; under it a 12px/1.4 mono fg-3 detail line; the action
  (secondary sm, `scroll-text` icon) to the Logs page filtered. The sentences, from a fixed table:
  - slower: "`booking-api` is slower than usual. The slowest 5% of requests take **820 ms**,
    usually 180 ms. It started at 13:58 UTC." plus, when a deploy of any app in the environment
    falls within 15 minutes before the start: " It started 2 minutes after `supplier-gw` v24
    rolled out." or after the start: " It started before `supplier-gw` v24 rolled out." (facts
    only: no "so the deploy isn't the cause"); detail "2.4% of requests return 5xx · 96 req/s";
    action "Show `booking-api` errors" (`/logs?app=…&levels=error`);
  - more errors: "`booking-api` returns more errors than usual: **2.4%** of requests, usually
    0.3%. It started at 13:58 UTC." + the deploy clause; detail "41 errors in the last 10
    minutes"; same action;
  - quieter: "`booking-api` has much less traffic than usual: **3 req/s**, usually 96. It
    started at 13:58 UTC."; detail "Its domains: `api.example.com`"; action "Show `booking-api`
    logs";
  - memory: "`metabase` is using **98%** of its 1 GB memory limit." + " It restarted 3 times in
    the last hour." when true; detail "copy e4d1 · exit 137 last at 13:56 UTC"; action "Show
    `metabase` logs";
  - restarting: "`metabase` restarted 2 times in the last hour." detail the last exit; action
    "Show `metabase` logs".
  With no unusual service: a line (no panel), 14px fg-2, ● 9px: "Everything in `Shop` /
  `production` is within its usual range." (with one place: "Every app is within its usual
  range."); before a full day of history: "The usual range is still being learned: started
  2026-10-25 UTC." with ○.
- **Charts** (`section`, grid `repeat(4, minmax(0, 1fr))`, gap 14): four `.cv-panel`s, the
  handoff's `MnChart`, the environment's totals: **Requests** (req/s, summed), **Errors** (% 5xx
  of all requests), **Slowest 5%** (ms, the p95 over all requests: the sampler keeps the merged
  buckets per environment per minute for this, not a mean of p95s), **Memory** ("GB of 5.4 limit",
  summed).
  - Head: `.cv-label` title, flex 1, and "now" 12px mono fg-3 (with script, the hovered point's
    time); the value 500 26px mono fg-1 (warn hue when the chart's latest 10 minutes are unusual
    by the rule above, for Errors and Slowest 5%) with its unit 13px mono fg-3, and "usually 120"
    12px mono fg-3 at the right (no "usually" for Memory: its band is drawn, its value is
    against the limit).
  - The plot: inline SVG (024's histogram is the precedent; the CSP forbids style attributes),
    height 120, margin 14px 16px 0: three 1px line-1 gridlines at 25/50/75%; the band as a
    polygon between the 7-day min and max, ink-3 at 0.7; a warn 8% rect from the unusual start
    to now on Errors and Slowest 5%; deploy markers as 1px dashed rolling lines with their label
    11px mono rolling ("`supplier-gw` v24"; one label per deployment, "and N more" when they
    crowd, as 024); the series as a 1.5px fg-1 polyline (non-scaling stroke); gaps break the
    line. Under it 11px mono fg-4: the start time, the middle time, "now" (UTC).
  - With script: a hover crosshair (1px fg-3) and the head shows that point's time and value
    ("13:40 UTC · 07:40 local"); without script the head shows "now" and each point has no
    readout (the `<title>` of the polyline names the range's min, max and latest).
  - No data at all for a chart (no service with a domain): the panel shows "No HTTP traffic:
    no app in `Shop` / `production` has a domain." 13px fg-3 instead of the plot (Requests,
    Errors and Slowest 5%).
- **Apps table** (`SectionHead` "Apps" with meta "5 in `Shop` / `production`" and the sort bar
  **unusual first · most traffic · most memory** at its right, 024's `seg`, links): `.cv-panel`,
  head 38px ink-2 `.cv-label`, columns `14px minmax(0, 1.3fr) 110px 100px 150px 130px
  minmax(0, 1.2fr) 80px 36px`, gap 16, rows min-height 52, padding 0 18px, 1px line-1:
  - a 9px mark: ◆ unusual (■ by the failed rule above), ◐ rolling out, ● healthy, ○ stopped;
  - **App**: the app's name 500 14px mono fg-1 (with several services, one row per service:
    "`hollo` / `web`"), under it 12px mono fg-3 "2 copies" (and "· no domain" for a service
    without HTTP numbers);
  - **Requests** "96/s", **Errors** "2.4%" (warn hue when unusual), **Slowest 5%** "820 ms ·
    usually 180" (the "usually" part fg-3, only when unusual) right-aligned 13px mono; "—" fg-4
    for a service with no domain;
  - **CPU**: a 120×24 inline SVG sparkline of the range (1.25px fg-2), `title` "0.4 of 1 core
    reserved, now";
  - **Memory**: the `Meter`'s bar (max-width 140, 6px, radius 1, ink-3 track, fg-2 fill, warn
    above 90%) and "0.7 / 1.0 GB" 12px mono fg-3 (warn above 90%);
  - **Restarts** in the range, right-aligned 13px mono, warn hue when non-zero;
  - a `scroll-text` icon link to the service's logs (`aria-label` "Logs of booking-api").
  - Rows link to the app's Running tab.
- **This host** (`id="host"`, `SectionHead` "This host" with meta "1 server"): the `NowScreen`
  Capacity panel for one host: a `.cv-panel` with padding 18, gap 14: the three `Meter`s (cpu,
  mem, disk; label width 40; reserved hatched for cpu and mem; readouts "1.2/4 vCPU", "1.9/3.8G",
  "18/40G"); one disk meter per watched filesystem when they differ, labelled with its mount in
  the readout ("/var/lib/docker 18/40G"); the room sentence; a 7-day memory chart (the
  `MnChart` shape, used and reserved as two lines, reserved dashed) under them.
- **Footer** 13px fg-3: "Sampled every minute and kept for 8 days. Requests come from Traefik,
  memory and CPU from Nomad."
- **Errors**: Traefik's metrics unreachable: the three HTTP charts show "Can't read Traefik's
  numbers right now." with ○ and the table's HTTP columns "—"; Nomad unreachable: the console's
  notice and the host panel's ■ line; both keep the stored history drawn.
- **Live**: the page refreshes on a `metrics` notice once a minute (one swap per chart and per
  table row, by id; nothing typed sits in the page).

## Narrow

- Under 1200px the charts are a 2×2 grid; under 640px one column.
- The issues rows stack their action under the text under 640px (023's banner rule).
- The apps table folds like the home's: mark, name and copies on the first line, then
  "Requests · Errors · Slowest 5%" as label/value pairs, the memory meter full width, Restarts and
  the logs link on the last line.
- The host panel's meters stay full width.

## May differ (server-rendered, no JavaScript)

- No hover readout without script; the head shows "now".
- The range, place and sort bars are links; the page is a GET.
- Live refresh needs script; without it, a reload.
- The handoff's queue and query-rate sub-lines ("14 queued", "1.2k queries/s") are not drawn:
  caravana has no source for them.

## API (names only, for the spec)

`getMetrics` (`GET /v1/metrics?project&environment&range` → `{range, points[]{t}, series{
requests, errors, p95, memory}[]{value, low, high, usual}, deploys[], issues[]{service, kind,
value, usual, since, deployNear?}, services[]{app, service, requests, errors, p95, cpu[],
memory, memoryLimit, restarts, unusual, distance}}`), `getAppMetrics` (`GET
/v1/apps/{id}/metrics?range`, the same per service), `getHost` (`GET /v1/host` → `{memory{used,
reserved, total}, cpu{…}, disks[]{mount, used, total, state}, room{count, likeApp, memoryMB,
cpuMHz, limitedBy}, sampledAt, baselineSince}`); attention kind `disk_nearly_full`. Times RFC
3339 UTC. API minor version after 024's.

## Review

The designer checks at 127.0.0.1:7070 against `pb-monitoring.png` and `pb-now.png`:
- `/monitoring` on a fresh instance (the baseline sentence, no band) and on one with seeded
  history (the band, "usually", "over N days");
- an unusual service of each kind the lab can make (slower: a service with a slow path called in
  a loop; errors: a path that answers 500; memory: a service near its ceiling; restarting), with
  their issue sentences, the deploy clause, the sort, the warn values;
- a service without a domain ("—", "no domain"); deploy markers on the charts;
- the host panel on `/monitoring` and the Platform panel on `/apps`, the room sentence with a
  yardstick, "No room…" when reservations are full;
- disk nearly full (a lab filesystem filled past 85%, or the table test), the needs-you item and
  its notification;
- Traefik's metrics unreachable; Nomad unreachable;
- each range; 1200, 640 and 390 widths; the page without script.

## Rulings on the research (designer, 2026-10-25)

1. **Memory used excludes the page cache** (Nomad's Usage − Cache), on Monitoring and on the Running
   tab, so the two pages agree. The CHANGELOG says so: the Running tab's numbers drop for apps that
   read files.
2. **Long-lived requests** (websockets, server-sent events) count as requests but are left out of
   the latency numbers. The page's footer adds: "Long-lived connections, such as websockets and
   streams, count as requests but not in Slowest 5%."
3. **Reserved** follows Nomad's rule for prestart tasks (a sidecar that ends before the main task
   isn't added on top), not the naive sum of the tasks' reservations.
4. **Two watched filesystems**: Nomad's data dir and Docker's root, from the setting
   `CARAVANA_DOCKER_ROOT` (default `/var/lib/docker`); never every mount.
5. **Range points**: a counter's point is the sum of its rows (requests per slot), a gauge's
   (memory) the median of its rows. This corrects "each point the median of its rows" above.
6. **Retention stays 8 days** and the band thins on the 7 d range: one caption line under that
   chart's times only, "The band thins to the left: older hours have fewer earlier days kept.", and
   the band's `title` names how many days it rests on.
7. **No host reservation in 025** (backlog). The room sentence keeps its wording and gains a
   `title`: "Nomad's count of what fits. Caravana's and the host's own processes aren't reserved."
8. **`disk_nearly_full`** opens at 85% (◆) / 95% (■) and resolves below 83%; a host sample older
   than 5 minutes isn't judged.
9. **Text is never fg-4** (designer, 2026-10-25): the 7 d caption and every chart's axis times
   (start, middle, "now") are fg-3; fg-4 stays for non-text marks only. Deploy labels stay rolling,
   gridlines line-1. (024's histogram label row was moved to fg-3 on its own branch for the same
   reason.)

## Review fixes (designer, 2026-10-26)

Applied on branch `025-monitoring`, one commit each, in this order:

1. **The apps table's HTTP numbers are the last 10 minutes.** Requests, Errors and Slowest 5% in the
   table come from the same 10-minute window as the issues and the chart heads (`metrics.WindowOf`:
   requests per second over the window, 5xx as a share of the window's requests, the p95 of its merged
   buckets), so a row never disagrees with its own issue; only the CPU sparkline keeps the range, and a
   number is in the warn hue only when its rule holds (the API's `requests`, `errors` and `p95` say the
   same).
2. **The host meters draw reserved, and memory warns from 85%.** The hatched line runs from 0 to the
   reserved fraction of the total (`x2`, checked in the page's markup for memory and CPU); the host's
   memory meter takes the warn fill and readout with a ◆ mark from 85% and a ■ mark from 95%, as the disk
   does, and is fg-2 below 85% (CPU keeps 90%). Memory used is Nomad's host `Memory.Used`, total less free,
   buffers and cache, so the page cache is left out as for the apps (ruling 1).
3. **The Apps home's app names never break mid-word.** The name keeps to one line (an ellipsis when it alone
   is wider than its cell) and the kind word drops under it when the two don't fit side by side.
4. **The disk meter shows the needs-you item's state while it is open.** `Service.Host` runs
   `metrics.DiskRun` over the same history as the attention kind, so a disk opened at 86% reads ◆ warning at
   84% until it resolves below 83%, and ■ failed from 95%, in the host section and the Platform panel.
5. **One wording for the disk item everywhere.** "The disk at `/var/lib/docker` is 87% full: 1.1 GB left."
   in the console (the mount in code style), the API's `detail` and every notification
   (`notify.DiskSentence`); the percent is cut down, not rounded, and the space left is `notify.Size`'s.
   The console's "The disk holding …" sentence is gone; what is on the disk stays the API's `holds`.
6. **A disk that rises from warning to failed sends one message.** `sent_problems` remembers the level last
   sent (a `level` column added to the unreleased migration 0019); a present item rising to failed from the
   sent warning sends one `problem.worsened` ("The disk at / is now 96% full: 410 MB left."), a fall or a
   repeat sends nothing, and the resolve below 83% is the usual `problem.resolved`.

### Rulings

- **The seven departures, accepted as built:** (1) when Traefik's numbers are off or disabled, the three HTTP
  charts show a ○ sentence in the plot's place without history, and keep the plot with a caption with history;
  (2) the CPU sparkline scales from zero to the larger of the reservation and the observed maximum; (3) an
  issue whose run reaches the 6-hour walk limit says "It started more than 6 hours ago."; (4) the 7 d chart's
  axis and gap titles name the weekday; (5) the Platform panel says "Waiting for the first reading of the host"
  with ○ before the first sample, and refreshes on the `metrics` notice; (6) no disk item before the first
  sample; (7) Nomad problems are a notice at the top of the live region plus a ■ line in the host section.
- **The three disk answers:** (a) while the needs-you item is open, the meter shows its state (◆ until it
  resolves below 83%, ■ at 95% or more), so the two never disagree; (b) one wording everywhere, console, API
  `detail` and notifications: "The disk at `/var/lib/docker` is 87% full: 1.1 GB left."; (c) a rise from ◆ to ■
  sends one message, `problem.worsened` ("…is now 96% full: 410 MB left."); a fall sends nothing; the resolve
  below 83% sends the usual resolved message. "No second opened" was about flapping at the threshold.
- Where this section and the contract above disagree, this section wins. The contract's "The disk holding
  `/var/lib/docker` is 86% full: 11 GB left." (needs-you list) reads "The disk at `/var/lib/docker` is 86%
  full: 11 GB left." from fix 5.

### Critique rulings (designer, 2026-10-26)

From the impeccable critique of the built page (`.impeccable/critique/2026-10-26T02-57-08Z__internal-ui-templates-monitoring-html.md`),
applied on branch `025-monitoring`, one commit per group, in this order. Where this section and the contract
above disagree, this section wins.

1. **A row's mark is the app's status first.** ■ not running or deploy failed (the same states `/apps` shows ■),
   ○ stopped (or never deployed), ◐ rolling out, ◆ some copies short; ◆ "unusual" only means "unusual against its
   week" and only for a serving service (■ "failing" when its issue is failed). **Amendment: a service that isn't
   serving gets no "quieter" issue at all** (its cause is on the needs-you list; the row's ■ is enough); the other
   kinds still apply, and the quieter sentence gains the deploy clause like the others. A place with nothing running
   reads "Nothing is running in `Shop` / `staging`: `web` is stopped." with ○ in place of the calm line.
2. **Chart heads are the last 10 minutes at every range**, labelled "last 10 min" (not "now"; the API's `now` is the
   same window). **Amendment to the contract's "now" heads.** A number is in the warn hue only when a rule judged
   it; the table's warn hues follow the issues.
3. **The host meters.** The reserved mark is drawn above the fill (it was hidden when use exceeds the
   reservation). The readout is split: "1.7G used · 1.3G reserved of 1.9G", CPU likewise in vCPU ("1.2 used · 0.8
   reserved of 2 vCPU"). When memory in use is at 85% or more, the room sentence adds ", but memory in use is
   already at 89%." (its `title` unchanged). vCPU is the one CPU unit on the page (the sparkline's title, the table,
   the meters, the room sentence: "(32 MB, 0.1 vCPU each)").
4. **Scale, floor and edges.** An 11px fg-3 label of the top gridline's value on each chart ("4 req/s", "5%",
   "800 ms", "1.2G"); the Errors chart scaled to at least 0-5%; the band's low and high edges drawn as 1px line-2
   polylines (`.cv-mnband-edge`) over the polygon.
5. **The live swap keeps focus.** The sort, range and place bars sit outside the swapped region; the notice swaps the
   issues, each chart and each table row by id, out of band (as 023's Domains rows), never the whole region, so
   keyboard focus and an open hover survive; the source notices sit outside the live region (no re-announcement each
   minute). The CPU cell prints its figure beside the sparkline ("0.4/0.5 vCPU") and, with the memory cell, sits
   above the row link's overlay.
6. **The smaller rulings.** HTTP charts mark only the deploys of apps with a domain (the Memory chart marks all);
   deploys within 5 minutes of each other become one marker labelled "10 deploys" (its `title` lists them); the 7 d
   caption is said once, under the chart grid; the hover's "usually" follows the hovered point; logs links carry the
   service (`/logs?app=<id>&service=<app>/<service>`); "—" alone (no "—%", "—MB"); rates and percents with one
   decimal; restarts warn from 2 (the rule's threshold); the ARIA table's cells (the logs link inside a
   `role="cell"`, header and cell counts equal).

## Last look (designer, 2026-10-26): Approved

1. **Deploy labels never collide.** Markers closer than 4% of the plot (12 of its 300 units) become one marker with one label ("6 deploys") whose `title` lists every deploy in it.
2. **A stopped app has no "now".** Its row reads "—" in requests, errors, slowest 5%, the CPU figure and the memory readout; only Restarts and the CPU sparkline (history) stay.
3. **CPU under 0.01 vCPU.** A used value above 0 and under 0.01 is written "<0.01" (so "<0.01/0.05 vCPU"), in the readout, the sparkline title and the host meter; exactly 0 stays "0".

025 is done, and with it Phase 2.6's screens.
