# Page contract: Servers and Add a server

Designer's contract for 031, written from the handoff's `ServerScreens.jsx` (`ServersScreen`,
`AddServerScreen`; `NodeScreen` is 027's page 2, approved) in
`design/console/design/ui_kits/console/` of the main checkout. The method is 021's and 027's: must-match
values here, a live review, an impeccable critique (critique only), a last look. Every rule of
`DESIGN.md` holds: token-only colour, no dim text (never fg-4 on words), 7px status marks, 32px fields
and selects, and the console never says "ship". Mode: Operator (the `/ops` shell, nav item active).

Words: the console says **server**, never node, host or machine (the page's own lede says "machine" once,
for the thing before it is a server). Nomad's scheduler role is "runs the scheduler".

## Ruling 1: the nav item becomes "Servers"

`shell.html`'s Operator item "Server" (`/ops/servers`) reads **Servers** and is the list from now on,
with one server too: a list of one is the honest first state of a page whose button is "Add server".
`aria-current` on the list, a server page and Add a server.

## Ruling 2: one way to add, so no tabs

The handoff has two tabs, "Run a command" and "Give Caravana SSH access". SSH access is not built (it
needs the organization's SSH key, Settings › SSH keys, later). A tab control with one tab is noise: the
page has no tabs, and nothing on it mentions SSH.

## Ruling 3: the name and pool come first

In the handoff the Name and Pool fields sit under the command and the checks start by themselves. Here
the token belongs to a name and a pool, so the page has two states:

1. **Before** (`GET /ops/servers/new`): the lede, the form (Name, Pool, the button **Make the join
   command**), and to the right the Checks panel with its four lines idle and the line "Make the command
   to start."
2. **After** (`POST /ops/servers`, answered 200 with the page, `Cache-Control: no-store`): the command
   in place of the form's button, the fields shown as facts (not inputs), and the Checks panel live.

Reloading the "after" page can't show the token again (it is shown once). `GET
/ops/servers/joins/{id}` is the same page without the command: "The command was shown once. If you lost
it, cancel this one and make a new command." with **Cancel this join** (a form, post).

## Servers (`/ops/servers`)

Page width as the handoff: max 1440, padding 36/26/64, sections 28 apart.

**Head.** `h1` "Servers" (`cv-headline` type), then on the same baseline a mono 13px fg-3 line and, at
the right end, the primary small button **Add server** with the plus icon, a link to
`/ops/servers/new`. The line, by state:

- one server: `1 server · 2 GB · 2 vCPU`
- several: `3 servers · 6 GB · 6 vCPU` (the sums of the ready ones; the vCPU formatter is 027's)
- a named pool other than `default` in use: `3 in default, 1 in build · …`

**The cluster sentence**, under the head, one line of 14px fg-2 with a 7px mark:

| State | Mark | Sentence |
| --- | --- | --- |
| three or more, three schedulers running | healthy | "The cluster survives the loss of one server." |
| three or more, promotion under way | rolling | "Three servers: two more are starting to run the scheduler." |
| two servers | idle | "With one more server the cluster survives the loss of one." |
| one server | idle | none: no sentence |
| a scheduler server down | warning | "One of the three servers that run the scheduler is down. The cluster works, and can't lose another." |

Under it, only when `canAdd` is false, a `cv-notice` with the reason (`cannotAddReason`) and Add server
disabled-looking but still a link (the Add page says the same).

**The list**, one `cv-panel`, a row per server, the handoff's grid: `200px 120px repeat(3,
minmax(0,1fr)) 100px`, gap 24, padding 16/18, a 1px line-1 rule between rows. The whole row is a link to
the server's page (`cv-row` hover).

1. Name (mono 500 15px fg-1) over the spec line (mono 400 12px fg-3): `2 vCPU · 2 GB · arm64`. Under the
   name, as small quiet marks in a row when true: `console` (Caravana runs here), `ingress` (public
   traffic enters here), `scheduler` (runs Nomad's scheduler). 11px mono fg-3 with a 1px line-2 border,
   radius 2, padding 1/5; they are facts, not statuses, so no colour.
2. State, a quiet small status pill: `ready` (healthy), `down` (failed), `not taking work` (idle),
   `disk filling` (warning; the disk state of 025), `joining` (rolling), `waiting` (idle).
3. to 5. Meters `cpu`, `mem`, `disk` as page 2's `srv-meter` at the list's size: used solid, reserved
   hatched, the readout at the right (`0.4 / 2 GB`), label width 36. A server with no numbers yet shows
   "—" in each, never an empty meter.
6. `4 copies`, right aligned, mono 13px fg-3 (`1 copy`).

A **waiting** row has no meters: columns 3 to 5 are one cell, 14px fg-2: "Waiting for the server to call
in · the command works for 43 more minutes" and a quiet **Cancel** button (a form). An expired one
isn't listed. A **joining** row's cell is its first open check ("Can't reach fsn-1 yet") and the row
links to `/ops/servers/joins/{id}`. A server Nomad knows and Caravana doesn't manage (`managed: false`)
is a normal row with the state as Nomad says it and, under the spec line, "Not set up for a cluster
yet."

**Down**: a down server's row keeps its last numbers greyed (fg-3 readouts, no fill) and its copies
count reads `0 copies`.

Live: the region `#srv-list` refetches `/parts/ops/servers` on the notices `servers`, `metrics` and
`copies` of the Operator stream (`/live?watch=ops`). Without JavaScript the page is whole and current at
load.

Narrow (390px): each row stacks: name and marks, state, then the three meters full width, then copies.

## Add a server (`/ops/servers/new`, then the answer of `POST /ops/servers`)

Page width max 1240, padding 36/26/64, gap 28. Back link `← Servers` (mono 13px fg-3). `h1` "Add a
server". Lede, 15px fg-2, max 760:

> Any machine with Ubuntu 24.04 and 2 GB of memory or more. Caravana installs what it needs, and the
> server starts taking work once its checks pass.

Two columns, `minmax(0,1fr) 400px`, gap 36, top aligned; one column under 900px with Checks below.

**Left, before.** A form, gap 20:

- **Name** (mono, 32px, optional, placeholder `fsn-2`), hint 13px fg-3: "Lowercase letters, digits and
  dashes. Leave it empty to use the machine's own name."
- **Pool** (mono, 32px, value `default`), hint: "Apps run on servers in `default`. A server in another
  pool takes no apps."
- The primary button **Make the join command**.
- A refusal (`server_exists`, a bad name) is drawn at its field, 422/409, the form filled as typed.
- When servers can't be added yet (`cluster_not_ready`): no form; a `cv-notice` with the service's
  sentence and, under it, the installer line to run on this server (mono, the one Updates shows for a
  rerun). With 63 servers: "This cluster has 63 servers, the most it can have."

**Left, after.** Gap 20:

- 15px fg-1: "Run this on the new server as root:"
- The command block: panel ink-0, 1px line-2, radius-2, padding 16/18, mono 13px/22px fg-1,
  `white-space: pre-wrap`, the command broken after the pipe (`… | sudo bash -s -- \` then the options
  indented two spaces). A ghost small **Copy** button top right (10px in) that `console.js` wires
  (`data-copy`); without script it isn't drawn and the text is selectable.
- 13px fg-3: "The token works once and for one hour."
- For a console on Traefik's own certificate (`--insecure` in the command), a `cv-notice` warning: "This
  console has no trusted certificate, so the new server can't check who it is talking to. Use this on a
  network you trust."
- For a dev build, `commandNote` as a `cv-notice`.
- Facts, two columns (`auto minmax(0,1fr)`, mono 13px): `name` (or "the machine's own name"), `pool`.
- A ghost small **Cancel this join** (a form, post, to `/ops/servers/joins/{id}/cancel`, then 303 to the
  list).

**Right: Checks.** Label `Checks` (`cv-label`), then a `cv-panel`, padding 18, gap 14:

- While nobody called in: a first line, 14px fg-2, with a rolling 8px mark: "Waiting for the server to
  call in…"
- The four lines, 14px, each with an 8px mark: done is healthy and fg-1, the one being worked on is
  rolling and fg-2, the ones after it idle and fg-3. Their words are the API's `detail`, with these when
  not yet known: "Connected to the cluster", "Docker running", "Reaches the other servers on the private
  network", "Ready to take work".
- When the token expired unused: the first line becomes, with a warning mark, "The command expired.
  Make a new one." and a link to `/ops/servers/new`.
- When all four pass: a rule (1px line-1), then 14px/1.5 fg-2 a sentence, and the primary small button
  **Go to <name>**. The sentence: "<name> adds 2 GB." and, when it is the third server: "With three
  servers the cluster survives the loss of one: <a> and <b> are starting to run the scheduler too."

The handoff's "search-indexer v9 was waiting for room and will start there now" needs Placement's
knowledge of what waits; not here.

Live: the panel is the region `#join-checks`, refetched from `/parts/ops/servers/joins/{id}` on the
`servers` notice, and every 5 s by `ops.js` while a check is open (the agent's report arrives by a call,
which is a notice too; the timer is the fallback). Without JavaScript: `<noscript>` a meta refresh of
`/ops/servers/joins/{id}` every 10 s while a check is open.

## The server page (`/ops/servers/<name>`), changes to 027's page 2

- The back link reads `← Servers` and goes to `/ops/servers`.
- It works for every server. The numbers, "Running here" and the facts come from that server.
- The spec line gains the role marks as words: `runs the console`, `public traffic enters here`, `runs
  the scheduler`.
- Facts (the aside's "Server" panel) gain: `pool`, `address` (the public one), `private address`
  (10.98.0.N), `joined` (relative, exact in `title`), `agent` (`caravana 0.18.0 · nomad 2.0.7 · docker
  27.5.1`), `last seen` (relative), `certificate` (`renews by Feb 8`), for a managed server.
- A server that isn't the one Caravana runs on has no OS block, no reboot pill and no "Recent on this
  server" from the host's files; in their place one line, 14px fg-2: "Updates and restarts are read on
  <first> only." with a link to Updates. Never an empty panel.
- A **down** server: the state pill `down` (failed), a banner: "<name> is down since 14:02 UTC. Its
  copies were replaced on other servers; what has a volume here waits for it." and, in the head, the
  quiet button **Remove server…**, a link to the confirmation page `/ops/servers/<name>/remove` (what
  goes: its place in the cluster and its token; what stays: nothing on the machine is touched; the
  primary button **Remove <name>**, Cancel). A running server has no Remove; the page doesn't offer what
  it would refuse.
- An agent older than the console: under the facts, 13px fg-3: "This server's agent is 0.17.0; the
  console is 0.18.0. Run the newer installer on it."

## Elsewhere, once there is more than one server

- **Overview (`/ops`).** The Server section's label reads "Servers" with a link "All servers" at the
  right of the section head, and the `ops-node` panel repeats, one per server, in index order (the
  handoff's `NowScreen` draws a panel per node). The headline counts servers ("3 servers, 12 copies…");
  with one server nothing changes.
- **Running tab.** Each copy's row gains the server's name, mono 12px fg-3, after the copy's ID, a link
  to the server's page. With one server it isn't drawn.
- **Monitoring › Host.** The section is per server: a small select (32px) "Server" above it when there
  is more than one; the room sentence stays for the cluster ("room for about 9 more apps like whoami",
  the sum).
- **Backups › Volumes.** A column "server" when there is more than one.
- **Updates.** Under the OS section's title: "Read on <first>, where Caravana runs."
- **Needs you.** `server_down` and `server_certificate_expiring` in `ProblemParts`' words, the server's
  name in mono, linking to its page.

## May differ

Server-rendered with forms: no optimistic states, the Copy button only with script, the list's hover
without script.

## Review

### Live review (designer, 2026-11-10, build 6c22fcb, staged: six servers, a joining one, a waiting join, an unmanaged node)

Verified at 1440 and at 500 wide (no sideways overflow): the nav word; the list's grid, role marks,
pills, meters with the hatched reserved part and "—" for no numbers; the waiting row with Cancel; the
joining row with its open check; "Not set up for a cluster yet."; Add a server before and after (the
command block, "The token works once and for one hour.", the facts, Cancel this join, the Checks panel
in its three mark states); the down server's pill, banner and Remove server…; the per-server panels on
the Overview; the facts of a managed server.

Rulings:

1. **The cluster sentence was mine and wrong for more than three servers** ("Three servers: two more
   are starting…" with five). It reads, while promotion is under way: "Two more servers are starting to
   run the scheduler. Once three do, the cluster survives the loss of one." ("One more server is…" for
   one). The other four states stay.
2. **A down server must not hold up a join.** The network check asks for the ready servers that are up
   (fixed in the service; api.md §3). The staged joining row named a down server among those it can't
   reach.
3. **A joining server needs a way out.** `/ops/servers/joins/{id}` of a server that called in and isn't
   ready shows, under the facts: "<name> called in <relative time>. If it never gets ready, remove it
   and make a new command." and the quiet button **Remove this server** (the cancel post; the service
   refuses when Nomad has a running node for it, and the page draws that refusal). While it waits for
   the call, the contract's "The command was shown once…" line stays.
4. **The room sentence is the cluster's**, so it doesn't belong on one server's page once there is more
   than one server: it stays on the Overview and on Monitoring. And it must be true: the staged Overview
   said "Nothing is running yet" beside five running copies. Find out whether that is the staging or the
   sum, and fix the one that is wrong.
5. **A down server's numbers are old.** Its page shows no meters panel: one line in its place, "<name>'s
   numbers were last read at 17:54 UTC." (the `cv-srvnums-line` form). The list's greyed readouts stay.
6. **"Updates and restarts are read on <first> only."** goes under the facts panel, in the same column,
   not alone in the other column.
7. **Overview: the server panels are a grid**, as the handoff's `NowScreen` has them: three columns
   (`repeat(3, minmax(0,1fr))`, gap 13) at 1200 and wider, two from 800, one below. With one server the
   panel is full width as today.
8. **"This server needs a reboot"** and the security-updates item name the server once there is more
   than one: "<first> needs a reboot: …". One source, `ProblemParts`.
9. The list's head line counts every server Nomad or Caravana knows that isn't waiting or joining, and
   sums the ready ones. An unmanaged node has no spec line, so its memory and vCPU aren't in the sums
   either: the numbers under a name and the sum in the head must agree.

### Critique rulings (designer, 2026-11-10, impeccable critique of build ba2852b: 29/40)

The critique found no overflow at 1440, 900, 500 or a real 390, and contrast of 5.59:1 or better on
every text in both themes. Its findings, ruled:

**Yes, wrong for the data shown (P1)**

1. **The Overview's stats line and the room sentence mix one server with the cluster.** With several
   servers, `cpu` and `mem` in the stats line, and "memory in use is already at N%" in the room
   sentence, are the cluster's: the sums of used over the sums of totals of the ready servers. With one
   server nothing changes.
2. **"The command works for 105 more minutes"** for a fresh join. Find the cause. If the staged console
   runs the pages on a frozen clock and the service on the real one, fix the staging (one clock); if the
   product can show more than 60, fix it and test it.
3. **A down server's "last seen" is a clock time** ("last seen 18:12 UTC", the exact time in `title`),
   never "just now" beside "down since 18:12 UTC".
4. **"Recent on this server" is this server's.** Items about another server (its disk, it being down)
   don't appear on this one's page.
5. **One vCPU number per server.** hel-1's spec line and facts said 4 vCPU while its meter and the list
   said 2. Find which source is wrong (the staging's node facts, or two reads that can differ) and make
   the page say one.

**Yes (P2)**

6. "<name> called in 46 minutes ago." is one phrase in the sentence's own type; only the name is mono.
7. **Remove this server** (a joining server) goes through the same confirmation page as **Remove
   server…**. Both take a server out of the cluster; one confirmation.
8. The confirmation says what removal means for data, without "sync": "Apps with a volume on <name>
   can't start on another server: their data is on <name>'s disk, and removing it here deletes nothing
   there." and "The other servers stop talking to it within a minute."
9. The join page of a server that called in is titled **"<name> is joining"** (breadcrumb, `h1`), with
   no lede. An open `network` check has a second line, 13px fg-3: "Check that UDP port 51820 is open
   between the servers."
10. The link in "Updates and restarts are read on <first> only." is underlined, as inline links are.
11. Each Cancel in the list has `aria-label="Cancel the join of <name>"` (or "…of the server that hasn't
    called in" for a join without a name).
12. A server whose disk is filling says so on its own page: a banner in `ProblemParts`' words ("The disk
    at / is 89% full: 2.0 GB left.").
13. The Overview's panel of a down server has no meters: the one line of ruling 5.
14. At 900 wide a server's name never breaks ("fsn-2" stays whole: the name doesn't wrap, the pool tag
    wraps under it), and "All servers" stays on the section head's line.
15. **Meters line up down the list.** The readout has a fixed width (the widest form, `16.6 / 18.6 GB`
    with its warning mark), so every track in a column has the same length and 31% looks the same in
    every row.
17. The command block's first line keeps clear of Copy (right padding the width of the button).
19. "Make the command to start." has no mark: it isn't a check.
20. A server Nomad knows and Caravana doesn't manage has the idle pill **not set up**, not "ready".
22. (in part) A down server's page doesn't show "Updates and restarts are read on <first> only."; the
    confirmation's button **Remove <name>** has the console's danger style, as Delete app has.
23. The Overview's server panels in a row have one height (`align-items: stretch`).

**No**

- 16 (the placeholder names an existing server): it is an example of the form, not a suggestion.
- 18 (the token sentence): the contract's words stay.
- 21 ("G" on the Overview's panels, "GB" on the server page): the panel is 027's, approved with "G".
- 22 (28px buttons at 390; the nav group "Server" over the item "Servers"): the console's small button is
  28px everywhere, and the group label is 027's.

**The three questions:** an unmanaged server's pill is "not set up" (20); yes, one confirmation (7);
the Overview's percentages are the cluster's (1).

### Applied (build after 37d4e76)

Every "Yes" above is in. The two causes: the "105 more minutes" was the staging (its pages stood on a
frozen clock while the service ran on the real one; the product can't show more than 60, and a test says
so); the 4 vCPU against 2 was both the staging and the product (the spec line read the node's
attributes, the meters the sample: both read the sample now).

**Departures**, accepted: to give the readout a fixed 112px and keep the tracks long enough (ruling 15),
the list's column gap is 16px where the handoff has 24, and its rows stack under 1100px of room, not
900. Measured: every `cpu`, `mem` and `disk` track is 44.7px at 1440 and 274px at 500.


### Last look (designer, 2026-11-10, build a303695, staged as before)

At 1440: the list's tracks are equal down each column and the head line's sums (7.6 GB, 8 vCPU) are the
four ready servers' own; the cluster sentence counts what is still to come; the joining row no longer
names the down server; the unmanaged row's pill is "not set up". The Overview says `cpu 9%` and `mem 51%`
for the cluster, "Room for about 23 more apps like api", and "hel-1 needs a reboot". fsn-3's page has the
banner, "fsn-3's numbers were last read at 19:21 UTC.", "last seen 19:21 UTC" and no read-on line; its
confirmation has the two sentences about volumes and the other servers. fsn-2's page opens with its disk
banner. hel-1 says 2 vCPU everywhere.

## Approved

Servers, Add a server and the server page for every server are approved (designer, 2026-11-10).
