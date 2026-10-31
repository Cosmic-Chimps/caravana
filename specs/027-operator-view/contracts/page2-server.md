# Contract: 027 page 2, the server page (one host)

Written by the caravana_designer session on 2026-10-29, before the spec. The values come from the
handoff's `ServerScreens.jsx` (`NodeScreen`; `ServersScreen` read and ruled out below),
`components/data/LiveMetric.jsx`, `Meter.jsx`, `components/status/AllocChip.jsx`,
`StatusPill.jsx` and `theme/caravana.css`, rendered from the prototype
(`design/console/_shots/pf-node.png`). Where this list and the handoff disagree, this list wins.
Page 1's contract (`page1-overview.md`) and the 021–025 contracts still apply: the time rule, the
No-Dim-Text Rule, 025's host numbers and units (vCPU, memory without the page cache).

## Ruling: no Servers list yet

`ServersScreen` is a list of many servers with "Add server". With one host it is one row that
only adds a click, so it isn't built. The sidebar's **Server** item goes straight to the server
page, `/ops/servers/<node name>`, and `/ops/servers` redirects (302) there. Phase 3 turns
`/ops/servers` into the handoff's list without moving this page.

## The page (`/ops/servers/<name>`)

`main`: max-width 1440, padding 30px 26px 64px, column gap 28 (the handoff's). An unknown name is
a 404 page with "No server called `x`. This caravana has one: `<name>`." and the link.

### Head

- Back link, 400 13px mono fg-3: **"← Overview"** to `/ops` (the handoff's "← Servers" has no
  list to go back to).
- Title row, flex, align center, gap 16:
  - the h1, the node's name, 500 26px mono fg-1;
  - the state pill md: Nomad's node state and eligibility in the console's words: ● "ready",
    ◐ "draining", ◆ "not taking new work" (ineligible), ■ "down" (Nomad lost its heartbeat),
    ◐ "starting" (initializing);
  - when a reboot is pending (page 3's reading), a second quiet pill ◆ **"reboot needed"** that
    links to `/ops/updates`;
  - the spec line, 400 13px mono fg-3: "4 vCPU · 7.8 GB · Ubuntu 24.04 · kernel
    6.8.0-147-generic" (page 1's line);
  - no actions on the right. The handoff's **Open shell** isn't built: a root shell on the host
    from the browser is outside what the console should hand out (a copy's terminal, 020.6, is
    as far as it goes). **Move work off** and **Take work again** (drain) aren't built: with one
    host there is nowhere to move work, and draining it would stop every app. Both come with
    Phase 3.
- The host's open needs-you items (a disk nearly full; page 3's security update and reboot), as
  023's problem banners under the head: `.cv-panel`, padding 16px 18px, the mark, the item's
  sentence 15px/1.5 fg-1, its explanation 14px fg-2 when it has one, its one action.

### The three numbers

The handoff's panel: `.cv-panel`, grid `repeat(3, minmax(0, 1fr))`, each cell padding 20, column
gap 12, 1px line-1 between cells:

- `.cv-label` title: **CPU**, **Memory**, **Disk**;
- the big value 500 24px mono (fg-1; warn hue from 85%, failed from 95% of the total, 025's
  rule) and its sub 13px fg-3, the last 10 minutes from 025's samples:
  - CPU: "1.2 vCPU" · "of 2 vCPU · 0.8 reserved";
  - Memory: "1.7 GB" · "of 1.9 GB · 1.2 GB reserved";
  - Disk: "5.8 GB" · "of 18.3 GB · `/`";
- the `Meter` without label (used solid, reserved hatched on top for CPU and memory, 025's fix),
  its percentage readout at the right.
- When the two watched disks are different filesystems, the Disk cell shows the fuller one big
  and the other as a second meter under it with its mount in the readout ("`/var/lib/docker`
  41/80G"). With the open disk item, the meter shows the item's state (025's ruling a).
- Under the panel, one line 13px fg-3: "Room for about 20 more apps like `blog` (32 MB, 0.05
  vCPU each), but memory in use is already at 87%." (025's sentence, the same function).

### Main column and aside

Grid `minmax(0, 1fr) 400px`, gap 32, `align-items: start` (the handoff's); under 1200px of
container width the aside drops below.

#### Running here (main)

- `.cv-label` **"Running here · 18"** (the count of copies Nomad has on the node, running or
  starting).
- A `.cv-panel` of rows, grid `240px minmax(0, 1fr) minmax(0, 1fr) 90px`, gap 24, padding
  14px 18px, 1px line-1 dividers, 400 13px mono fg-2; each row links to the copy's app's Running
  tab:
  - the `AllocChip` (app, then service when the app has several, then the short copy ID; the
    copy's mark and phase: starting dashed ◐, stopping dashed ○ line-through, failed);
  - **CPU**: `LiveMetric` sm: the value "0.19" with unit "vCPU" (the handoff's "cores" becomes
    025's unit), the sub "of 0.05 reserved" 12px mono fg-3, **no sparkline**: Caravana keeps
    per-service history (025), not per copy, and a service's line beside one copy of several
    would mislead;
  - **Memory**: `LiveMetric` sm: "412 MB" with the sub "of 512 MB limit", in the warn hue above
    90% of the limit; no sparkline (same reason);
  - **Up** right-aligned fg-3: "3 d 4 h" (since the copy started).
  - Values are Nomad's allocation stats now (the Running tab's source), read when the page
    renders and on the live refresh while the page is open.
  - Sorted: problems first (failed, starting, stopping), then by app and service.
  - The place prefixes the app when not Default › production ("Shop › staging › `web`"), as on
    page 1.
  - Empty: `.cv-empty` "Nothing runs here yet." with New app.

#### Server facts (aside, first)

- `.cv-label` **"Server"**, then a `.cv-panel` (padding 18) grid `auto minmax(0, 1fr)`, gap
  10px 18px, 400 13px/1.4 mono fg-2, keys fg-3. The handoff's keys, with what one host can say:
  - **pool** `default` (Nomad's node pool);
  - **address** the public address (023's: `CARAVANA_PUBLIC_URL`'s host or the entrypoint's)
    and, on a second line fg-3, the private `caravana0` address;
  - **up since** the host's boot time "2026-10-21 03:12 UTC" with the relative words after it
    ("8 d ago"), from Nomad's client stats (timestamp minus uptime); the handoff's "joined"
    isn't drawn: Nomad keeps no "created" time for a node, and joining is Phase 3;
  - **agent** "nomad 1.9.4 · docker 27.3 · traefik 3.7.1 · caravana 0.13.0" (Traefik's version
    is its registered job's image tag);
  - **os** "Ubuntu 24.04.1 LTS · kernel 6.8.0-147-generic";
  - **cpu** "AMD EPYC-Rome · 2 cores · 2,445 MHz" (Nomad's attributes); the model and the
    per-core frequency are optional: on arm Nomad gives only the cores and a total, so the row
    reads "2 cores · 4,890 MHz in total";
  - **last seen** "2 s ago" (Nomad's last heartbeat; ■ fg-1 "lost 3 min ago" when down).

#### Recent on this server (aside, second)

- `.cv-label` **"Recent on this server"**, then a `.cv-panel` (padding 6px 0) of rows, grid
  `54px 14px minmax(0, 1fr)`, gap 10, padding 9px 18px, 14px fg-2: the time 400 12px mono fg-3
  (UTC, the local time in its `title`), a 7px mark, the sentence.
- Rows, newest first, the last 8, from three sources merged (ruled 2026-10-29 on the research):
  - page 1's activity entries that concern the server: the node's own events (registered,
    heartbeat missed, back), restarts and out-of-memory needs-you entries of copies on it;
  - the host's boots, from the boot time ("The server started (up since 03:12 UTC).");
  - package runs from `/var/log/apt/history.log` (mode 644, confirmed readable; rotated monthly
    to `.gz`, the current and the previous month read): one row per Start-Date block, "OS
    packages updated by Ubuntu's automatic updates: 14 packages." when its Commandline is
    `/usr/bin/unattended-upgrade`, "OS packages updated by hand: 3 packages." otherwise, with
    ", a reboot is needed." when the block installed what `/var/run/reboot-required.pkgs` names.
  - The handoff's "Docker images pruned · 2.1 GB freed" has no source and isn't drawn.
- Empty: "Nothing recent." 13px fg-3.

### Errors

- Nomad unreachable: the console's notice, the numbers panel from the last stored sample with
  "as of 14:02 UTC" in each sub, the Running here panel "Can't read Nomad right now." with ○.
- Node down: the state pill ■ "down", the banner "Nomad lost this server's heartbeat 3 minutes
  ago. Apps on it may be down." with no action (there is nothing the console can do on one
  host); Running here shows Nomad's last known copies with their marks.

## Narrow (390px)

- The title row wraps: name and pills, then the spec line.
- The numbers panel stacks its three cells (1px line-1 between them, top instead of left).
- Running here rows fold: the chip on its own line, then CPU and Memory side by side, Up after.
- The aside follows the main column.

## May differ (server-rendered, no JavaScript)

- No live sparklines; values refresh on the live stream with script, else on reload.
- No Open shell, no drain, no list of servers.

## API (names only, for the spec)

`getHost` (pages 1 and 025) gains `node{…, address{public, private}, bootTime, lastHeartbeat,
cpuModel, traefikVersion, caravanaVersion}`; `listHostCopies` (`GET /v1/host/copies?usage=true`
→ every copy on the node with app, service, place, phase, status, cpu{used, reserved},
memory{used, limit}, startedAt), or `listCopies` across apps if the spec prefers one route;
`listActivity` (page 1) takes `server=1` for the server's entries, boots and package runs. Times RFC 3339
UTC.

## Review

The designer checks at 127.0.0.1:7070 against `pf-node.png`:
- the Server item reaching `/ops/servers/<name>`, `/ops/servers` redirecting, an unknown name;
- the head with "← Overview", the state pill, "reboot needed" when staged, the spec line, no
  actions;
- the numbers panel (memory in warn at 87%, the reserved hatch, a second disk when staged);
- Running here with a starting and a failed copy, places, the fold;
- the facts (public and private addresses, up since, the agent line), Recent with a node event, a
  boot, and a package run by Ubuntu's automatic updates and one by hand;
- Nomad unreachable; 1440, 1200, 640 and 390 widths; without script.

## Review rulings (designer, 2026-10-30, build 2d70ccd)

Checked headless at 1440, 1200, 640 and 390 (innerWidth logged, no horizontal scroll at any),
without script, `/ops/servers` (redirect) and `/ops/servers/nope` (404, the contract's sentence and
link), against `pf-node.png`.

### Verified

- The head: "← Overview", the name 500 26px mono, ● "ready", ◆ "reboot needed" linking to
  `/ops/updates`, the spec line, no actions.
- The three numbers on the newest sample with the hatched reserve. Memory 80% stays fg-1 under 85%.
- The fold at 390: chip, then CPU and Memory side by side, then Up. The numbers stack.
- The facts (pool, addresses, up since in UTC and local, agent with Traefik's tag, os, cpu on arm,
  last seen), and Recent's time column with the local time in each title.
- Ids `srv-copy-<id>` on the chips.

### Fixes

1. **Superseded failed copies fill the top of the list.** crashy shows five failed copies. Four
   have the idle ○ mark although their title says "failed", and one has ■. Each row is three bold
   "—".
   - A failed copy that Nomad replaced (it has a next copy) is history, not "here". Drop it from
     Running here. Recent already says "crashy/worker keeps restarting".
   - List the newest copy of each lineage only. When that copy is failed, it shows ■ and the
     failed chip, and its Up cell reads "failed 3 min ago" in fg-3.
   - A missing value is "—" in 400 fg-3, never the bold value style.
2. **The count reads wrong next to the rows.** "Running here · 11" sits above 16 rows. With fix 1,
   the label is "Running here · 12" counting running, starting and stopping copies, plus
   "· 1 failed" in fg-3 when failed copies are listed.
3. **The starting phase never shows.** slowstart started at 09:48:12, and its server sleeps
   120 s, yet at 09:49:43 `listCopies` already said `healthy` ("Tasks are running"). Its health
   is `{port: http}`.
   - Find out whether the check really passes during the sleep (a staging gap) or a copy is
     called healthy before Nomad's health is set (a bug in the shared copy status, which the
     Running tab would share).
   - If it's a bug, a copy whose deployment health isn't set yet is "starting" (◐, dashed chip).
   - Stage a copy that is truly starting, for example with an HTTP path check, and tell me when
     it's up.
4. **The two addresses aren't named.** "192.168.68.147" over "192.168.5.15" doesn't say which is
   which. Write "192.168.68.147 · public" and "192.168.5.15 · private" with the words in fg-3.
   Check that the second line is really the `caravana0` address: in the lab, 192.168.5.15 looks
   like Lima's user network. If `caravana0` has none, leave the line out rather than show another
   interface.
5. **Memory mixes units.** "of 1.9 GB · 736 MB reserved" becomes "of 1.9 GB · 0.7 GB reserved".
   The reserved part takes the total's unit, as page 1's meters do.
6. **The stacked aside wastes its width.** Under the 1200 container, Server and Recent each span
   the full width with short lines. When stacked and the container is 900px or more, lay them out
   side by side, two equal columns with the main column's gap, Server first. Under 900 they stack.
   The same grid areas apply, with no new pattern.

### Departures

1. The newest sample, with 025's meter thresholds (CPU warn above 90%, memory and disk from 85%,
   failed from 95%): yes. Use 025's meter function, not a copy.
2. "Waiting for the first reading of the server…" with 200 before any node is known: yes.
3. The banners reuse the needs-you row: yes. The disk clause "It holds Nomad's data and Docker's
   images." appears only when the item's `holds` says so (one filesystem). Otherwise the banner
   names its own mount. A down node's banner has no action.
4. Recent's dates: yes, HH:MM today and "22 Oct" before, with the full UTC time and local time in
   the title.
5. Caravana's version in Recent: keep it, only when the version changes, as "Caravana was
   upgraded from 0.11.2 to 0.12.0." A restart on the same version isn't listed. Today's row
   "Caravana 0.12.0-dev.023 is running." would then be gone unless the version changed.
6. Superseded by fixes 1 and 2.
7. Traefik's version without the "v": yes.
8. DESIGN.md untouched: yes.

### Still to verify (stage or point at the test)

- A boot row ("The server started (up since 02:24 UTC).") and the 10-28 kernel run with ", a
  reboot is needed." Both fall outside the last 8 here. Name the tests that assert them.
- A disk banner and a down node's banner and pill: the tests, or a staged state.
- Nomad unreachable: the panel from the last sample with "as of …", and Running here "Can't read
  Nomad right now."

## Recheck rulings (designer, 2026-10-30, build 3f243f8 on 02beb0f)

Checked at 1440, 1200, 1100 and 390, during slowstart's start (23:50:47 UTC), with no
horizontal scroll at any width.

### Verified

- Fix 1: no replaced failed copies are listed. A missing value is "—" in 400 fg-3.
- Fix 2: the label reads "Running here · 13", with no failed copy at the time. The "· 1 failed"
  form is covered by the tests.
- Fix 3: slowstart and crashy, between restarts, are ◐ with the dashed chip and "under 1 min" in
  Up, ahead of the healthy rows.
- Fix 4: "192.168.68.147 · public". The lab has no private line, which is right with no
  `caravana0`.
- Fix 5: "of 1.9 GB · 0.7 GB reserved".
- Fix 6: side by side in two equal columns at 1440 (554px each) and 1200 (434px each), stacked
  at 1100.
- The boot row "The server started (up since 14:48 UTC).", and no version row on the baseline.

### Fixes

1. The facts break inside an item at 1200 ("traefik" / "3.7.13", "08:48" / "local (9 h ago)").
   As on page 1's stats line, each item between the " · " separators stays whole (nowrap), and
   lines break only between items. The relative "(9 h ago)" belongs with the local time.
2. "last seen" read 40 s and then 45 s ago on a ready node. Confirm it comes from a fresh read of
   the node's last heartbeat, not 025's 10-minute `Nodes().Info` cache. If it's the cache, read
   the heartbeat fresh (one call per page render or live refresh). Or show "last seen" only when
   it is over the heartbeat TTL, as the down state does.

### Words

- The down banner uses "3 minutes ago", full words, as the needs-you sentences do. Short units
  ("3 min") are for dense columns (Up, Recent), never in a sentence.
- The unreachable notice, "Nomad can't be reached right now. The numbers and facts here are the
  last ones Caravana read.": yes. Each number's sub still carries "as of HH:MM UTC" (contract).

### Not page 2's, but blocking the merge

console-logs started failing 2 of 3 isolated runs at the copy-status change, after passing at the
commit before. Treat the change as the cause until shown otherwise. The page's design can be
approved without it, but 027 doesn't merge with that step flaky. A likely place: 024's
`LogCopies` and the Live follower pick copies by status.

## Critique rulings (designer, 2026-10-31, critique of 0c672ce, 27/40)

Fold these in with the two recheck fixes, the "last seen" ruling and "3 minutes ago".

### P1, a crash-looping copy looks calm: yes

- The phase mark stays truthful (■ failed, ◐ starting). The crash signal is added beside it, not
  in place of it.
- **Source: the needs-you item, one source.** When the copy's service has an open `restarting`
  or out-of-memory item (025's `crashesInHour`), the Up cell reads ◆ "restarted 4 times in 1 h"
  (or ■ "out of memory 2 times in 1 h"), in the item's level. The count comes from the same
  function. A copy's own restart count is not read separately, so the page can never disagree
  with the needs-you list or the notification.
- A failed copy adds its exit reason from Nomad's last task event: "failed 3 min ago · exit 1",
  or "· out of memory", or "· couldn't start". Use the words 022 uses for runs.

### P2, columns: yes

- Use the critique's columns: `minmax(10rem, 200px) minmax(0, 1fr) minmax(0, 1fr)
  minmax(90px, auto)`. The Up cell is nowrap, and the fold stays under 600px.
- Add a header row for screen readers only: `role="row"` with four `columnheader`s (Copy, CPU,
  Memory, Up), visually hidden. The handoff draws none.
- The fold's label follows the phase: "Up" for a running copy ("3 d 4 h"), "Starting"
  ("for 40 s"), "Failed" ("26 s ago · exit 1"). Never "Up: failed".

### P3: yes, all four

- Recent's head reads "times in UTC" in its meta, as page 1's feed does.
- The spec line keeps each " · " item whole (as recheck fix 1). "kernel 7.0.0-38-generic" never
  breaks.
- Disk's sub reads "of 18.3 GB on `/`". The second disk reads "on `/var/lib/docker`".
- vCPU values always have two decimals ("1.00", "0.31"), and "<0.01" stays. Use one formatter,
  shared with page 1's readouts and 025's, so all three pages agree.

### Minor

- A failed copy and a starting copy draw the same CPU and Memory cells: "—" in 400 fg-3 over the
  sub "of 0.05 reserved" (or "of 32 MB limit"). The reservation is a fact of the copy in every
  phase.
- "up since" with script: `localtime.js` leaves out the local date when it equals the UTC date
  ("2026-10-30 14:48 UTC · 08:48 local"). This applies console-wide. Update its checksum in
  `tools/ui-vendor.versions`, and run the console steps that read times.

### The two questions

- Room clause above 85%: no change on this page. It is 025's `Room` sentence, and 025 already
  adds ", but memory in use is already at 87%." from 85%. At 83% it is correctly absent. If the
  owner should see it sooner, that change belongs to 025's function for every page, not to this
  page.
- The crash signal comes from the needs-you item (above).

## Last look (designer, 2026-10-31, build 7637b63)

Checked at 1440, 1200, 960, 640, 390 and without script. No width scrolls sideways, and no word
breaks across lines at any width. CPU and Memory start at the same x on every row (one grid with
subgrid rows), and the four hidden columnheaders are there. Verified: the crash line ◆
"restarted 3 times in 1 h" beside a truthful phase; "times in UTC"; "of 18.3 GB on /"; "up
since … UTC · 08:46 local (10 h ago)" with the local date left out; no "last seen" on a fresh
ready node; the same "—" over the reservation sub for a starting copy.

### Last fixes, then approved

1. **Fold at 720px of the panel** (your call 1): yes. At 640 every row is four lines ("0.00" /
   "vCPU" / "of 0.05" / "reserved"). The fold at 720 is the right answer, not a narrower Up cell.
2. **A crash-looping copy sorts with the problems.** At 1440, crashy, healthy between restarts,
   sat in alphabetical order under clock, and it led the list only while it was starting.
   "Problems first" includes a row with the crash signal, so it never moves with the restart
   cycle. Order: failed, crash signal, starting, stopping, then by app and service.
3. **Capacity totals stay whole.** "of 2.00 vCPU" becomes "of 2 vCPU". Two decimals are for
   measured and reserved values ("0.22", "0.77 reserved", "0.00"). A whole number of cores is a
   count. Apply it in the shared formatter, so page 1 and 025 match.

### Calls

- Freshness from the sample's window start plus one gap (call 2): yes.
- ■ for out of memory and ◆ for restarting (call 3): yes, those are the list's own levels.

## Approved

Approved 2026-10-31 at 4344394 (designer). Measured: the fold below 720px of the panel (658 at 960),
one line per row at 1440; crashy leads with ■ "failed 31 min ago · exit 1" and ◆ "restarted 3 times in
1 h", "Running here · 11 · 1 failed"; "of 2 vCPU" on this page, page 1 and /monitoring; no horizontal
scroll at 1440, 960, 720, 640 or 390.
