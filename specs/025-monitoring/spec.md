# Feature Specification: Monitoring and capacity

**Feature Branch**: `025-monitoring` (stacked on `024-logs`)

**Created**: 2026-10-25

**Status**: Implemented, v0.13.0

**Input**:
- **The maintainer, 2026-10-23:** "continue with other page": 025 follows 024 with no pause.
- **docs/plan.md, row 025 (Phase 2.6, the handoff's `MonitoringScreen`, the Platform panel on
  `DevHome`, the Capacity panel of `NowScreen` for one host):** requests, errors and slowest 5 %
  per app from Traefik's metrics, memory per app, each compared with the same hour over the last
  7 days (the shaded band) with deploy markers, and a table of apps sorted "unusual first". The
  host's own capacity: memory reserved against used, disk, "room for about 4 more apps like
  whoami", with "disk nearly full" on the needs-you list. History in a small SQLite ring buffer
  fed by Nomad's allocation and node stats and Traefik's metrics endpoint on loopback.
- **The designer's contract:** `contracts/monitoring-screen.md` (2026-10-25), with its "Rulings
  on the research" 1–9 (`research.md`, decided by the designer the same day), which override
  anything above them in the contract: memory used leaves out the page cache, on this page and on
  the Running tab; long-lived requests count as requests but not in the slowest 5 %; reserved
  follows Nomad's rule for prestart tasks; two watched filesystems; a range point sums a counter
  and takes the median of a gauge; retention stays 8 days and the band thins on 7 d; no host
  reservation in 025; `disk_nearly_full` opens at 85 % and 95 % and resolves below 83 %; text is
  never fg-4.

## How this feature is built

As for 022 to 024: the designer's contract and the research, then a lab spike for the checks
the research couldn't run, then the pure math (the metrics text, deltas, percentiles, folding,
the band, the rules, the room), the Nomad reads, the store and the sampler, the reads in the
service layer and the API, then the page on real data, the designer's review, the critique pass,
rulings and the last look.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - How each service is doing against its usual week (Priority: P1)

The owner opens Monitoring and sees, for one project › environment, four charts of the
environment's totals over the last 6 hours (or 1 hour, 24 hours, 7 days): requests per second,
the share of requests that failed with a 5xx, the slowest 5 % of requests in milliseconds, and
memory used against the summed limit. Behind each line is a shaded band: the lowest and the
highest value of the same time of day (UTC) on each of the previous 7 days. Deploy markers sit
at the time each deploy rolled out. Under the charts a table lists every service with its
requests, errors, slowest 5 %, a CPU sparkline, a memory meter against its limit and its restarts
in the range. The table is sorted "unusual first": services whose numbers broke one of the fixed
rules over the last 10 minutes come first, the furthest from usual at the top, then services
that are rolling out, then the rest by name. A fresh install says it is still learning what
usual looks like.

**Why this priority**: the Running tab shows CPU and memory per copy only while it is open, and
nothing anywhere says whether today is different from every other day; an owner can't tell a
slow hour from a normal one.

**Independent Test**: in the lab, with history seeded for 7 days and a service made slow by a
load loop, the page draws the four charts with the band and the deploy markers, the table puts
the slow service first with "820 ms · usually 180" in the warn hue, and a service without a
domain reads "—" in its HTTP columns.

**Acceptance Scenarios**:

1. **Given** an environment with running apps and at least one full day of samples, **When** the
   owner opens Monitoring, **Then** four charts show the range's points with the band behind
   each line, and the head of each chart shows the newest value and "usually N".
2. **Given** fewer than 7 previous days of samples, **Then** the band is drawn from the days that
   exist and "usually" reads "usually 180 · over 3 days".
3. **Given** less than one full day of samples, **Then** no band and no "usually" are drawn, and
   each chart's caption reads "Building the baseline: started 2026-10-25 UTC. The usual range
   appears after a full day."
4. **Given** a range of 1 h, 6 h, 24 h or 7 d, **Then** the charts have 60 one-minute, 72
   five-minute, 144 ten-minute or 168 one-hour points; a point of requests or errors is the sum
   of its rows, and a point of memory or CPU is the median of its rows.
5. **Given** a minute the sampler couldn't read a source, **Then** that point is a gap in the
   line, never a zero.
6. **Given** a service with no domain, **Then** its requests, errors and slowest 5 % read "—"
   and its name line says "· no domain"; its CPU, memory and restarts are still shown.
7. **Given** the sort bar, **Then** "unusual first", "most traffic" and "most memory" order the
   table as named, and each is a link that works without JavaScript.
8. **Given** a deploy of an app in the environment that rolled out in the range, **Then** a
   dashed marker with "`supplier-gw` v24" is drawn at that time on every chart, one label per
   deploy and "and N more" when labels crowd.
9. **Given** the page is open, **Then** it refreshes its charts and rows once a minute when the
   sampler has written a new minute.

---

### User Story 2 - The host's capacity: room for N more, and disk (Priority: P1)

The owner reads, in the "This host" section of Monitoring, how much of the host's memory and CPU
is reserved by running copies and how much is used, how full each watched disk is, and a sentence
that says how many more apps of a typical size fit: "Room for about 4 more apps like `whoami`
(256 MB, 100 MHz each)." A chart under the meters shows memory used and reserved over 7 days.

**Why this priority**: before adding an app the owner needs to know whether it fits, in the
terms Nomad uses to place it; and a full disk stops every deploy without warning.

**Independent Test**: in the lab, the host section shows the memory meter with reserved equal to
Nomad's own allocated memory, the disk meter for the filesystem holding Nomad's data and Docker's
images, and a room sentence whose count can be checked by hand from the reservations.

**Acceptance Scenarios**:

1. **Given** running apps, **Then** the cpu, mem and disk meters show used, and for cpu and mem
   the reserved part hatched under used, with readouts such as "1.2/4 vCPU", "1.9/3.8G",
   "18/40G".
2. **Given** a task that runs before the main task and ends (a volume's prestart task), **Then**
   reserved counts it the way Nomad does, as the larger of it and the main tasks, not on top.
3. **Given** Nomad's data and Docker's images are on one filesystem, **Then** one disk meter is
   shown; **Given** two filesystems, **Then** two meters, each labelled with its mount in the
   readout ("/var/lib/docker 18/40G"). No other mount is ever shown.
4. **Given** running apps, **Then** the room sentence names the running app whose memory
   reservation is the median of the running apps' (ties: the alphabetically first) with its size,
   and the count is the smaller of the free memory and the free CPU divided by that app's
   reservations, rounded down.
5. **Given** the free memory or CPU is less than one such app, **Then** the sentence reads "No
   room for another app like `whoami`: memory is fully reserved." (or "CPU") in the warn hue.
6. **Given** no running app, **Then** the sentence reads "Nothing is running yet: 3.6 GB of
   memory is free to reserve."
7. **Given** the room sentence, **Then** its title reads "Nomad's count of what fits. Caravana's
   and the host's own processes aren't reserved."

---

### User Story 3 - The issues panel: what is unusual, and what happened just before (Priority: P2)

Above the charts, one row per unusual service says in a sentence what is different, by how much,
since when, and whether a deploy in the environment rolled out just before it started: "`booking-api`
is slower than usual. The slowest 5% of requests take **820 ms**, usually 180 ms. It started at
13:58 UTC. It started 2 minutes after `supplier-gw` v24 rolled out." A detail line gives the
supporting numbers, and an action opens the Logs page filtered to that service.

**Why this priority**: the table says which service is off; the sentence says what to look at
first, and the deploy clause answers the first question after a change.

**Independent Test**: in the lab with seeded history, a load loop that makes one service slow and
answer 500 to some requests produces the "slower" and "more errors" rows with their numbers and
start times; a deploy of another app a few minutes before adds the deploy clause.

**Acceptance Scenarios**:

1. **Given** a service whose slowest 5 % over the last 10 minutes is more than 50 % and at least
   100 ms above the band's top, with at least 20 requests, **Then** a "slower" row is shown.
2. **Given** a service whose 5xx share over the last 10 minutes is above 1 %, with at least 5
   errors, and above the band's top, **Then** a "more errors" row is shown.
3. **Given** a service whose requests over the last 10 minutes are below half the band's bottom
   while the bottom is at least 1 request a minute, **Then** a "quieter" row is shown with its
   domains in the detail line.
4. **Given** a service using more than 90 % of its memory limit (the median of the last 10
   minutes), **Then** a "memory" row is shown, with or without a baseline.
5. **Given** a service with 2 or more restarts nobody asked for in the last hour, **Then** a
   "restarting" row is shown with the last exit in the detail line, with or without a baseline.
6. **Given** a deploy of any app in the environment that rolled out within 15 minutes before the
   start, **Then** the sentence adds " It started 2 minutes after `supplier-gw` v24 rolled out.";
   **Given** one that rolled out after the start, **Then** " It started before `supplier-gw` v24
   rolled out."; nothing else is said about the cause.
7. **Given** errors above 10 % or memory above 98 %, **Then** the row's mark is ■ failed; else ◆
   warning.
8. **Given** no unusual service, **Then** a line reads "Everything in `Shop` / `production` is
   within its usual range." (with one place: "Every app is within its usual range."); before a
   full day of history it reads "The usual range is still being learned: started 2026-10-25 UTC."
9. **Given** an unusual service, **Then** it is not an item on the needs-you list.

---

### User Story 4 - The host on the Apps home, and "disk nearly full" (Priority: P2)

The Apps home's rail gains a "This host" panel above Activity: "● 1 server up", the memory meter
with reserved hatched under used, the meter of the fuller watched disk, and the room sentence,
linking to Monitoring's host section. When a watched disk passes 85 % the needs-you list gains an
item: "The disk holding `/var/lib/docker` is 86% full: 11 GB left." with **Open monitoring**,
and the owner is notified like for every item.

**Why this priority**: the home page is where the owner looks every day; a disk filling up should
reach them before a deploy fails for it.

**Independent Test**: the Apps home shows the panel with the same numbers as the host section;
on a throwaway VM with a small disk filled past 85 %, the item appears with its notification, turns
■ at 95 %, stays at 84 %, and resolves below 83 %.

**Acceptance Scenarios**:

1. **Given** the sampler has read the host within the last 5 minutes, **When** the owner opens
   the Apps home, **Then** the panel shows "● 1 server up", the memory meter ("1.9/3.8G"), the
   fuller disk's meter ("18/40G") and the room sentence, and links to `/monitoring#host`.
2. **Given** the host hasn't been read for more than 5 minutes, **Then** the panel shows "■ Can't
   read the host's numbers" and the last numbers it has.
3. **Given** a watched filesystem at 85 % or more, **Then** one `disk_nearly_full` item per
   filesystem appears, ◆ warning, with the sentence and **Open monitoring**; at 95 % or more it is
   ■ failed.
4. **Given** the item is open and the disk falls to 84 %, **Then** the item stays; **Given** it
   falls below 83 %, **Then** the item goes and a "resolved" notification is sent after the
   notifier's usual delay.
5. **Given** one filesystem holds both Nomad's data and Docker's images, **Then** the sentence
   names both: "The disk holding Docker's images and Nomad's data (`/`) is 86% full: 11 GB left."
6. **Given** the newest host sample is older than 5 minutes, **Then** the item is neither opened
   nor resolved until a fresh sample exists.

---

### User Story 5 - The same numbers through the API (Priority: P3)

An API client reads an environment's metrics (points, totals with their band, deploys, issues,
services), one app's, and the host's (memory, CPU, disks, room, 7-day history), computed the same
way the console computes them, with `null` for every gap.

**Why this priority**: constitution V; the MCP server and scripts read the same numbers later.

**Independent Test**: `getMetrics` for the lab's environment answers the same issues, sort and
values the page shows; `getHost` answers the host section's numbers; a deploy token is refused.

**Acceptance Scenarios**:

1. **Given** a full API token, **When** a client reads `getMetrics` with a project, an
   environment and a range, **Then** it gets the points, the four series with value, low, high
   and usual per point, the deploys, the issues sorted by distance and the services sorted
   unusual first, the baseline and the state of each source.
2. **Given** a gap, **Then** the value is `null`, never 0.
3. **Given** `getHost`, **Then** it answers memory, CPU and disks with their states, the room, the
   7-day history and when the host was sampled.
4. **Given** a deploy token, **Then** every metrics and host read is refused with 403
   `token_scope`.
5. **Given** the needs-you list through the API, **Then** `disk_nearly_full` items carry the
   mount, what it holds, the used percent, the free bytes and the level.

---

### User Story 6 - The sidebar and the way to an app (Priority: P3)

The sidebar's Workspace group gains Monitoring after Logs. A row of the apps table opens the app's
Running tab, its logs link opens the Logs page filtered to that service, and an issue's action
opens the Logs page filtered to that service's errors or lines. The app's page gets no new tab.

**Why this priority**: one way in and one way on; the app's own numbers per copy are already on
its Running tab.

**Independent Test**: the sidebar lists Monitoring after Logs and marks it on `/monitoring`; a
table row opens `/apps/<id>`; "Show `booking-api` errors" opens `/logs?app=…&levels=error`.

**Acceptance Scenarios**:

1. **Given** the sidebar, **Then** Monitoring (icon `activity`) is listed after Logs and is
   active on `/monitoring`.
2. **Given** a table row, **When** it is followed, **Then** the app's Running tab opens; its
   `scroll-text` link has the label "Logs of `booking-api`" and opens the Logs page for it.

---

### Edge Cases

- A fresh install: no band, no "usually", the baseline caption on each chart and the "still being
  learned" line; only the memory and restarting rules can mark a service unusual.
- A sampler gap (Caravana stopped, a source unreadable, a slow tick): no row or a NULL column for
  the minutes missed, drawn as a gap; the next folded 10-minute row counts only the minutes it
  had, and rates divide by the seconds covered.
- Traefik's metrics are off on a host installed before 025 (its metrics address answers 404): the
  three HTTP charts and the table's HTTP columns say "Traefik's request numbers are off on this
  host. Run the installer again to turn them on."; CPU, memory and the host still work.
- Traefik is unreachable: the three HTTP charts show "Can't read Traefik's numbers right now."
  with ○ and the HTTP columns read "—"; the stored history stays drawn.
- Caravana's Nomad token can't read the host (a host whose policy predates 025): the host section
  and the Platform panel say "Caravana's Nomad token can't read the host. Run the installer
  again."; the per-service numbers still work.
- Nomad unreachable: the console's notice, the host panel's ■ line; the stored history stays
  drawn; the copy-based rule (restarting) isn't judged.
- A Caravana restart: the first minute after start has CPU and memory but no requests, errors,
  slowest 5 % or restarts (a one-minute gap); the stored history is untouched.
- Traefik restarted (a counter went back to zero): the minute's count is the new value, never
  negative and never a jump.
- The 7 d range: older hours find fewer earlier days in the 8 days kept, so the band thins to the
  left; one caption under that chart's times says so, and the band's title names how many days
  it rests on.
- A service with a domain but almost no traffic: the slowest 5 % chart draws what it has; the
  "slower" rule needs at least 20 requests in the 10 minutes.
- Websockets and server-sent-event streams: counted as requests, left out of the slowest 5 %;
  the footer says so.
- An app that rolls out: it is sorted after the unusual services and the HTTP rules don't judge
  it while it rolls out; the memory and restarting rules still do.
- A stopped app: ○ in the table, never judged.
- An app deleted: its samples go with it, and the environment's totals leave it out.
- Two services whose Traefik names collide (app `a-b` service `c` and app `a` service `b-c`): the
  HTTP numbers are attributed to neither, the collision is logged once, and both rows say why in
  the HTTP cells' title.
- A database that fills its page cache up to its ceiling: memory used leaves the cache out, so it
  isn't marked unusual for that.
- Lima's or any other mount that isn't one of the two watched filesystems at 100 %: never shown,
  never an item.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: caravana MUST sample once a minute, on wall-clock minutes, per service of each
  stored app with running copies: requests, 5xx responses and the latency histogram from
  Traefik's metrics (per Traefik service, which already sums every router of the service), CPU
  used, memory used, memory limit, CPU and memory reserved, copies and restarts nobody asked for;
  and for the host: memory used, reserved and total, CPU used, reserved and total, and the used,
  total and free space of each watched filesystem.
- **FR-002**: A source that can't be read in a minute MUST leave no value for that minute (a
  gap), never a zero; the page and the API MUST draw and answer it as a gap.
- **FR-003**: Memory used MUST be the memory a copy uses without its page cache, on this page and
  on the Running tab, so the two agree.
- **FR-004**: Requests over websockets and server-sent-event streams MUST count as requests and
  MUST be left out of the slowest 5 %; the page's footer MUST say so.
- **FR-005**: The slowest 5 % MUST be computed from the merged latency histogram of the rows it
  covers (a minute, ten minutes, a range point, a service, the environment), never as a mean of
  stored percentiles; a value beyond the highest bound MUST read "over 10 s"; no requests MUST be a
  gap.
- **FR-006**: Reserved memory and CPU MUST follow Nomad's own rule for tasks that run before the
  main task and end (the larger of them and the main tasks, not their sum), and MUST equal Nomad's
  own count of allocated resources in the lab.
- **FR-007**: Exactly two filesystems MUST be watched: the one holding Nomad's data and the one
  holding Docker's data root (a host setting, default `/var/lib/docker`), shown once when they are
  the same; no other mount is ever shown or judged.
- **FR-008**: Samples MUST be kept as one row per minute for 48 hours and one row per 10 minutes
  for 8 days, the 10-minute row written as soon as its slot ends; older rows MUST be deleted by
  the sampler; the store MUST stay under 10 MB for 20 services.
- **FR-009**: A range point MUST sum a counter's rows (requests, errors, restarts, histogram
  counts) and take the median of a gauge's rows (memory, memory limit, CPU, copies); a point with
  no row MUST be a gap; rates MUST divide by the seconds the rows cover.
- **FR-010**: The band at a point MUST be the lowest and highest value of the same metric in the
  same UTC 10-minute slot on each of the previous 1–7 days, and "usually" their median; with less
  than one full day since sampling began, there MUST be no band, no "usually" and the baseline
  caption; with 1–6 days, "usually" MUST say "over N days"; on the 7 d range the band MUST thin
  to the left with the one caption line of ruling 6 and a title naming the days it rests on.
- **FR-011**: A service MUST be unusual when, over the last 10 minutes, any of these holds:
  slower (the slowest 5 % above the band's top by more than 50 % and by at least 100 ms, with at
  least 20 requests); more errors (5xx above 1 % of requests, at least 5 errors, and above the
  band's top); quieter (requests below half the band's bottom while the bottom is at least 1
  request a minute); memory (the median above 90 % of the limit, no baseline needed); restarting
  (2 or more restarts nobody asked for in the last hour, counted as the needs-you list counts
  them, no baseline needed). Before a full day only memory and restarting MUST be judged.
- **FR-012**: An unusual service's distance MUST be the largest of value / band top (slower,
  errors), band bottom / value (quieter, capped at 100), memory / 90 % and 1 + restarts / 10; its
  level MUST be failed when errors are above 10 % or memory above 98 %, else warning.
- **FR-013**: "Unusual first" MUST order unusual services by distance, largest first, then
  services of apps rolling out, then the rest by name; "most traffic" and "most memory" MUST order
  by requests and by memory, largest first. An app rolling out MUST NOT be judged by the HTTP
  rules; a stopped app MUST NOT be judged.
- **FR-014**: Each issue MUST name since when it holds (the earliest minute of the unbroken run,
  walking back at most 6 hours; longer reads "for more than 6 hours"), and MUST add the deploy
  clause for the newest deploy of any app in the environment that rolled out within 15 minutes
  before the start, else the first that rolled out after it; a deploy rolled out is one that
  ended healthy, at its end time.
- **FR-015**: The issues panel MUST use the contract's fixed sentences, detail lines and actions
  per kind, sorted by distance; with no issue it MUST draw the one line of the contract.
- **FR-016**: Unusual services MUST NOT be needs-you items.
- **FR-017**: The room count MUST be the smaller of (memory total − memory reserved) and (CPU
  total − CPU reserved) divided by the yardstick app's reservations, rounded down; the yardstick
  MUST be the running stored app whose memory reservation is the median (the lower middle for an
  even count; ties the alphabetically first); the three sentences of the contract MUST be used
  and the title of ruling 7 added; nothing is held back for the host's own processes in 025.
- **FR-018**: A `disk_nearly_full` item MUST exist per watched filesystem while it is at 85 % or
  more, and stay until a sample falls below 83 %; it MUST be warning below 95 % and failed at 95 %
  or more; it MUST NOT be opened or resolved while the newest host sample is older than 5 minutes;
  it MUST be notified like every item, once per filesystem (a rise from 86 % to 96 % doesn't send a
  second "opened").
- **FR-019**: The Monitoring page MUST follow the designer's contract and its rulings: the head,
  the place and range bars, the issues panel, the four charts drawn as inline SVG without inline
  styles, the apps table, the host section, the footer, the error states, the narrow layouts; text
  MUST never be fg-4.
- **FR-020**: The Apps home MUST show the Platform panel above Activity with the host's numbers
  from the newest stored sample, without waiting on Nomad.
- **FR-021**: The page MUST refresh its parts on a `metrics` notice once a minute with script;
  without script every bar MUST be a link and the page a GET, and the head MUST show "now".
- **FR-022**: The API (0.27.0) MUST offer the environment's metrics, one app's, and the host's,
  computed by the same service calls as the console, with `null` for gaps and the state of each
  source; the needs-you items MUST gain `disk_nearly_full` and its facts. Deploy tokens MUST be
  refused.
- **FR-023**: When Traefik's metrics are off on a host (404), the page MUST say to run the
  installer again; when Traefik or Nomad can't be reached, the page MUST say so and keep the
  stored history drawn; when the Nomad token can't read the host, the page MUST say to run the
  installer again.
- **FR-024**: The installer MUST turn on Traefik's metrics on its loopback entrypoint with the
  finer latency buckets, say that Traefik restarts once, grant Caravana's Nomad token read access
  to nodes and nothing more, and write Docker's data root to Caravana's settings; the lab MUST do
  the same.
- **FR-025**: The sidebar MUST list Monitoring after Logs; the apps table's rows MUST link to the
  app's Running tab; the app's page MUST gain no tab.
- **FR-026**: Samples MUST NOT carry a URL, a header, a client address or a request body; the
  store holds counts, sizes and service names only.

### Key Entities

- **Sample**: one service's numbers for one minute or one 10-minute slot: the minutes it folds,
  the seconds its HTTP counts cover, requests, errors, the histogram counts and their bounds, CPU,
  memory, memory limit, CPU and memory reserved, copies, restarts. Any of them may be absent.
- **Host sample**: the host's memory and CPU used, reserved and total for a minute or a slot,
  and per watched filesystem its mount, what it holds, used, total and free.
- **Band**: for one point of one metric, the lowest, highest and median of the same UTC slot on
  the previous days found, and how many days that was. Derived, never stored.
- **Issue**: an unusual service: its kind, level, value, usual value, since when, distance, the
  deploy clause and the detail facts. Derived, never stored.
- **Room**: the count of further apps like the yardstick, the yardstick's name and size, and what
  ran out. Derived, never stored.
- **Baseline**: when sampling first began; a full day, some days or seven days.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: The Monitoring page for an environment with 20 services draws within 2 seconds for
  the 24 h range on the lab's VM.
- **SC-002**: A sampler tick takes under 1 second of wall time for 20 services in the lab, and a
  day of sampling grows the store by less than 2 MB.
- **SC-003**: Under a known load (9 requests of 50 ms to 1 of 400 ms), the computed slowest 5 % of
  each minute falls in the same bucket as the load loop's own measured 95th percentile.
- **SC-004**: The reserved memory and CPU shown equal Nomad's own allocated memory and CPU for the
  lab's host.
- **SC-005**: Each of the five kinds of issue the lab can make appears within 2 minutes of its
  cause, with its sentence and its start time within a minute of the cause.
- **SC-006**: The disk item opens at 85 %, turns failed at 95 %, stays at 84 % and resolves below
  83 %, with one "opened" and one "resolved" notification.
- **SC-007**: Every bar is a link and the page works without JavaScript; reloading the page's
  address shows the same view.
- **SC-008**: The designer approves the page and the Platform panel against the contract after the
  review cycle.

## Assumptions

- Traefik v3 exposes Prometheus metrics per Traefik service when turned on, and a Caravana
  service with a domain is one Traefik service named `<app>-<service>@nomad` (lab, observed).
- Nomad's allocation stats are collected (`publish_allocation_metrics`, set since 014) and its
  node stats are collected whatever the telemetry settings.
- CPU is a point sample of Nomad's last collection, not the minute's average; the sparkline's
  title says "now".
- One host today; the tables carry the node so Phase 3's servers fit without a migration.
- Memory oversubscription is on, so ceilings don't count for placement and don't count for room.
- No host reservation, no per-copy history, no access log, no alerting thresholds the owner sets,
  no network or block I/O, no queue or query-rate lines, no MCP tool (later) and no multi-server
  capacity (Phase 3).
