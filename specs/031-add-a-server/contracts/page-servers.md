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

(The designer's review, the critique's rulings and the last look are added here as they happen.)
