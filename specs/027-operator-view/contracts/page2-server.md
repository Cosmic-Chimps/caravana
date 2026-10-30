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
    ("8 d ago"); the handoff's "joined" (when a server joined the cluster) is Phase 3;
  - **agent** "nomad 1.9.4 · docker 27.3 · traefik 3.7.1 · caravana 0.13.0";
  - **os** "Ubuntu 24.04.1 LTS · kernel 6.8.0-147-generic";
  - **cpu** "AMD EPYC-Rome · 2 cores · 2,445 MHz" (Nomad's attributes);
  - **last seen** "2 s ago" (Nomad's last heartbeat; ■ fg-1 "lost 3 min ago" when down).

#### Recent on this server (aside, second)

- `.cv-label` **"Recent on this server"**, then a `.cv-panel` (padding 6px 0) of rows, grid
  `54px 14px minmax(0, 1fr)`, gap 10, padding 9px 18px, 14px fg-2: the time 400 12px mono fg-3
  (UTC, the local time in its `title`), a 7px mark, the sentence.
- Rows, newest first, the last 8, from three sources merged:
  - page 1's translated Nomad events for this node and its copies ("`supplier-gw` `c81e`
    started.", "`metabase` `e4d1` restarted: out of memory.");
  - the host's boots, from the boot time ("The server started (up since 03:12 UTC).");
  - package changes, **only if** the research confirms `/var/log/apt/history.log` (or
    `/var/log/dpkg.log`) is readable by Caravana's service user: "OS packages updated: 14
    packages, no reboot needed." / "…, a reboot is needed." (page 3 owns the details).
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
`listHostEvents` (page 1) takes `node=` and includes boots and package changes. Times RFC 3339
UTC.

## Review

The designer checks at 127.0.0.1:7070 against `pf-node.png`:
- the Server item reaching `/ops/servers/<name>`, `/ops/servers` redirecting, an unknown name;
- the head with "← Overview", the state pill, "reboot needed" when staged, the spec line, no
  actions;
- the numbers panel (memory in warn at 87%, the reserved hatch, a second disk when staged);
- Running here with a starting and a failed copy, places, the fold;
- the facts (public and private addresses, up since, the agent line), Recent with Nomad events,
  a boot and (if readable) a package change;
- Nomad unreachable; 1440, 1200, 640 and 390 widths; without script.
