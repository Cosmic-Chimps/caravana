# Feature Specification: The Operator view (one host)

**Feature Branch**: `027-operator-view`

**Created**: 2026-10-30

**Status**: Draft
<!-- A release updates the Status line of every feature it contains: "Implemented, vX.Y.Z" (or "Implemented, unreleased" before the release). -->

**Input**:
- **The maintainer, 2026-10-30**, on the CodeAlta task for 021: "this is not done... we have to create
  the 'operator' pages", then "let's start the operator view.... do it with the speckit flow".
- **docs/plan.md** put every Operator screen of the design handoff in Phase 3 (Cluster). This feature
  brings the ones that make sense on the single host Caravana runs on today; the screens that need
  several servers stay in Phase 3.
- **The motivating case, 2026-10-28:** the maintainer's server ran kernel 6.8.0-147 while 6.8.0-149,
  the fix for a critical kernel vulnerability, was installed and waiting for a reboot. The maintainer
  learnt it from outside the console.
- **The designer's contracts**, ruled on the research (2026-10-29/30), which decide layout, words and
  the edge cases below: `contracts/page1-overview.md`, `contracts/page2-server.md`,
  `contracts/page3-updates.md`. **The research**: `research.md` (where each page's data comes from).

## How this feature is built

As for 021 to 025: the designer's contracts and the research first (done), then this spec, the plan
and the tasks, then per page the backend and API, the page on real data, the designer's review, the
critique pass, the rulings and a last look. Page 1 first, then 2, then 3; each page is usable on its
own.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - See the server and everything on it at a glance (Priority: P1)

The owner switches the console from the Developer view to the Operator view with a two-part toggle
under the brand. The Operator view opens on **Overview**: one sentence saying how the apps are doing
(the same sentence as the Developer home), a line with the server count, apps, copies, CPU and memory
in use and the open items, then a panel for the server (its name, its size, OS and kernel, the CPU,
memory and disk meters, and every running copy as a chip grouped by app), a table of every app across
every project and environment (state, copies, an hour of CPU, memory, its address, its last change),
the needs-you list, a feed of what happened recently in Caravana's own words, and the host's capacity.
The Operator sidebar lists only pages that exist: Overview and Server, then Updates. Pages both views
share (Settings, an app's pages, New app) keep the view the owner was last in.

**Why this priority**: the owner has no single page about the server: its state, what runs on it and
what happened lately are spread over the Apps home, Monitoring and each app. The toggle and the
Overview are the frame every other Operator page hangs from.

**Independent Test**: on the lab with several apps in two environments, the toggle reaches `/ops`;
the page shows the shared headline, the stats line, the server panel with its spec line, meters and
chips grouped by app, the apps table across places sorted problems first, needs-you as on the home,
recent activity including a deploy, a restart and a failed schedule run, and capacity; Settings
opened from the Operator view keeps the Operator sidebar.

**Acceptance Scenarios**:

1. **Given** the owner is on the Developer home, **When** they choose **Operator** in the toggle,
   **Then** they land on Overview, the toggle marks Operator as current, and the sidebar shows the
   Server group (Overview, Server) and the Platform group (Updates).
2. **Given** the owner is in the Operator view, **When** they open Settings or an app's page, **Then**
   the sidebar stays in the Operator view; signing in and `/` still land on the Developer home.
3. **Given** 13 apps with 18 running copies, CPU at 38 % and memory at 63 % over the last 10 minutes
   and 3 open needs-you items, **When** Overview loads, **Then** the stats line reads "1 server · 13
   apps · 18 copies · cpu 38% · mem 63% · 3 open items" and the headline is the Developer home's
   sentence.
4. **Given** apps in Default › production and in Shop › staging, **When** Overview loads, **Then** the
   apps table lists both, the second with its place before its name, failed and unhealthy apps first.
5. **Given** a deploy, an app restart and a failed schedule run happened in the last hour, **When**
   Overview loads, **Then** Live activity lists them newest first in Caravana's words; with the page
   open, a new deploy appears in the panel without a reload, marked as new once.
6. **Given** Caravana was restarted, **When** Overview loads, **Then** Live activity still shows what
   happened before the restart.
7. **Given** Nomad can't be reached, **When** Overview loads, **Then** a notice says so, the server
   panel and its chips are replaced by that sentence, and the apps table and needs-you still show what
   Caravana knows.

---

### User Story 2 - Look at the server itself (Priority: P2)

From the sidebar's **Server** item (or the server panel on Overview) the owner opens the server page:
its name with its state in plain words, a "reboot needed" mark when one is pending, its size, OS and
kernel; any open needs-you items about the host as banners; CPU, memory and disk with what is used and
reserved; every copy running on it with its CPU and memory now and how long it has been up; the
server's facts (pool, public and private address, up since, the versions of Nomad, Docker, Traefik and
Caravana, OS and kernel, CPU); and what happened recently on it (its own events, boots, OS package
runs). The page offers no actions: a shell on the host and moving work off it wait for several servers.

**Why this priority**: the server's own facts (which kernel runs, since when it is up, which versions
of the parts run) are nowhere in the console today, and they are what an owner checks first after a
security notice or an outage.

**Independent Test**: on the lab the Server item reaches `/ops/servers/<name>`; `/ops/servers`
redirects there; the head shows the state and the spec line with no action buttons; the numbers
match Monitoring's host section; Running here lists every copy with a starting and a failed one first;
the facts show the lab's real versions; Recent lists a boot and an OS package run.

**Acceptance Scenarios**:

1. **Given** one server named `v2202610435163537080`, **When** the owner opens `/ops/servers`,
   **Then** they are redirected to `/ops/servers/v2202610435163537080`; an unknown name answers "No
   server called …" with a link to the real one.
2. **Given** a reboot is pending, **When** the server page loads, **Then** a "reboot needed" pill next
   to the state links to Updates.
3. **Given** 18 copies on the server, one starting and one failed, **When** the page loads, **Then**
   Running here lists 18 rows, the failed and starting copies first, each with its CPU in vCPU against
   its reservation, its memory against its limit and its time up.
4. **Given** the node reports its OS, kernel, CPU and versions, **When** the page loads, **Then** the
   facts show them; on a CPU without a model name the row reads "2 cores · 4,890 MHz in total".
5. **Given** Ubuntu installed 14 packages by its automatic updates last night, **When** the page loads,
   **Then** Recent on this server has "OS packages updated by Ubuntu's automatic updates: 14 packages."
6. **Given** Nomad can't be reached, **When** the page loads, **Then** the numbers show the last stored
   values with "as of" their time and Running here says Nomad can't be read right now.

---

### User Story 3 - Know what updates are waiting and how to apply them (Priority: P3)

The owner opens **Updates** and sees what is waiting: a newer Caravana release (with its notes), the
server's OS updates (how many, how many are security updates, when Ubuntu last checked), and a pending
reboot (with the running kernel and the installed one). Below, **How to apply** gives the exact
commands to run on the server over SSH, in order, with the reboot last and its consequence stated; **How
this server updates** shows whether Ubuntu installs security updates by itself and reboots by itself,
and Caravana's one setting (whether to check for new releases); **Past updates** lists Caravana's
version changes, the server's boots and the OS package runs. A pending reboot and security updates
waiting are needs-you items and reach the owner's notification channels; a new Caravana release sends
one notification. The console never applies an update itself and never names a vulnerability.

**Why this priority**: it is the page that would have told the maintainer about 2026-10-28's kernel,
but it builds on the Operator frame (US1) and the server's facts (US2).

**Independent Test**: on a lab VM with a newer kernel installed and not rebooted, Updates shows "This
server needs a reboot: kernel <new> is installed, but <old> is still running." as a banner and a
needs-you item, a notification is sent, the Reboot row and How to apply's last step show; with a fake
newer release served to the check, the Caravana row and the release notification appear; with the
check turned off, nothing is fetched.

**Acceptance Scenarios**:

1. **Given** kernel 6.8.0-149 is installed and 6.8.0-147 runs, **When** Caravana's next check runs,
   **Then** Updates, the needs-you list and every notification channel say "This server needs a
   reboot: kernel 6.8.0-149 is installed, but 6.8.0-147 is still running.", and the Updates nav item
   carries ■.
2. **Given** 14 updates are waiting, 9 of them security updates, and Ubuntu's automatic updates are
   on, **When** they have been waiting less than 24 hours, **Then** Updates lists them but no needs-you
   item opens; after 24 hours the item "14 updates are waiting, 9 of them security updates." opens.
3. **Given** Ubuntu's list of updates is 3 days old, **When** Updates loads, **Then** the counts show
   with their age, the advice to run `sudo apt update`, and no needs-you item opens from them.
4. **Given** the owner presses **Check package names**, **When** the check finishes (it can take a
   minute), **Then** the Ubuntu updates row lists the package names, kept for an hour; a second press
   within 10 minutes is refused with the time to wait.
5. **Given** a newer Caravana release is published, **When** the daily check runs, **Then** Updates
   shows "0.15.0 → 0.16.0" with its notes and the installer command in How to apply, the Updates nav
   item shows a count, and one `caravana.release` notification is sent (not again for the same
   release).
6. **Given** the owner unticks "Check GitHub for new releases once a day" and saves, **When** a day
   passes, **Then** Caravana makes no request to GitHub, and the setting reads "off".
7. **Given** the server isn't Ubuntu or Debian, **When** Updates loads, **Then** the OS rows say
   Caravana can't read this server's pending updates, and nothing else about the OS.
8. **Given** the reboot is done, **When** Caravana's next check runs, **Then** the reboot item
   resolves (and its notification says so), and Past updates shows the boot.

---

### Edge Cases

- **No host sample yet** (the first minute after a start): the stats line ends "waiting for the first
  reading"; the server block of facts is empty for at most a minute.
- **The node is down** (Nomad lost its heartbeat): the server page's state pill reads "down" with a
  banner and no action; Running here shows Nomad's last known copies.
- **More than 40 copies**: the server panel shows the first 40 chips and "and N more copies".
- **A schedule run that succeeded**: not in Live activity (only failed runs and runs stopped at their
  limit), so routine runs don't drown the feed.
- **GitHub unreachable or rate-limited**: the Caravana row is left out with "Couldn't check GitHub
  (last checked …)"; no item, no notification.
- **A build that isn't a release version** (`dev`, `0.0.0-dev.N`): no comparison, no release row.
- **A reboot asked for by a package other than the kernel**: the item names it ("`libc6` was updated
  and needs a reboot.").
- **The package-name check fails or times out** (2 minutes): the row says so and keeps the counts.
- **A deploy token** calls any of the new API operations: refused (403 `token_scope`) by the default
  deny.
- **Without JavaScript**: every page works as plain HTML and forms; Live activity and the package-name
  check show their state on reload.

## Requirements *(mandatory)*

### Functional Requirements

**The two views (US1)**

- **FR-001**: The console MUST offer a Developer/Operator toggle under the brand, as two links; the
  current view is marked for assistive technology.
- **FR-002**: The view MUST follow the address: pages under `/ops` are the Operator view. Pages both
  views share MUST draw the sidebar of the view last used, remembered per browser; signing in and `/`
  MUST land on the Developer home.
- **FR-003**: The Operator sidebar MUST list only pages that exist: Server › Overview, Server; Platform
  › Updates, with Updates marked ■ for a pending reboot, ◆ for security updates waiting, else the
  count of updates available.

**Overview (US1)**

- **FR-004**: Overview MUST show the Developer home's headline sentence (the same wording from the same
  source) and a stats line with servers, apps, copies, CPU and memory in use over the last 10 minutes,
  and the count of open needs-you items.
- **FR-005**: Overview MUST show one panel for the server with its name, state, pool, a spec line (CPU,
  memory, OS, kernel), the CPU, memory and disk meters with used and reserved (025's numbers), and each
  running copy as a chip grouped by app, linking to the app.
- **FR-006**: Overview MUST list every app across every project and environment with its state, a mark
  per copy, the last hour of CPU, memory used now, its first address and its last change, problems
  first, each row linking to the app.
- **FR-007**: Overview MUST show the needs-you list exactly as the Developer home does.
- **FR-008**: Overview MUST show the last 8 entries of Caravana's activity, newest first, built from its
  own records (deploys and their outcome, app actions, backups and restores, schedule runs that failed
  or were stopped at their limit, needs-you items opened and resolved) and the server's own events
  (registered, heartbeat missed, back, boots). Entries MUST be in Caravana's words, survive a restart,
  and arrive while the page is open without a reload. Command output MUST never appear.
- **FR-009**: Overview MUST show the host's capacity with 025's meters and room sentence.

**The server page (US2)**

- **FR-010**: The server page MUST live at `/ops/servers/<name>`; `/ops/servers` MUST redirect to it
  while there is one server; an unknown name MUST answer with the real one.
- **FR-011**: Its head MUST show the name, the state in plain words (ready, draining, not taking new
  work, down, starting), a "reboot needed" mark linking to Updates when one is pending, and the spec
  line, and MUST offer no actions.
- **FR-012**: It MUST show the host's open needs-you items as banners, CPU, memory and disk with used,
  reserved and total, and, when two watched disks differ, both.
- **FR-013**: It MUST list every copy on the server with its app (and service, and place when not
  Default › production), its state, its CPU and memory now against its reservation and limit, and its
  time up, problems first.
- **FR-014**: It MUST show the server's facts: pool, public and private address, up since, the versions
  of Nomad, Docker, Traefik and Caravana, OS and kernel, CPU (model and frequency when known), last
  heartbeat.
- **FR-015**: It MUST show the last 8 recent entries for the server: its activity entries, its boots,
  and OS package runs (with how many packages, whether Ubuntu's automatic updates or a person ran
  them, and whether a reboot is needed).

**Updates (US3)**

- **FR-016**: Caravana MUST check GitHub's latest Caravana release about a minute after it starts and
  then once a day with some jitter, sending nothing but its own version as the user agent, and only
  while the owner's setting allows it (on by default). A **Check now** MUST be refused within 10
  minutes of the last check.
- **FR-017**: A release MUST count as newer only when its version is higher than the running one by the
  installer's ordering and it is not a pre-release; non-release builds MUST never compare.
- **FR-018**: Caravana MUST read, from files on its own host and without elevated privileges: whether a
  reboot is pending and which packages asked for it, the running kernel and the newest installed one,
  how many updates are waiting and how many are security updates with when Ubuntu last checked,
  whether Ubuntu installs updates and reboots by itself, and the history of OS package runs.
- **FR-019**: Package names MUST be read only when the owner asks (**Check package names**), one check
  at a time, at most once per 10 minutes, kept for an hour.
- **FR-020**: Updates MUST list what is waiting (Caravana, OS updates, reboot), **How to apply** with the
  exact commands in order (OS updates, then Caravana's installer, then the reboot last, with "every app
  stops for about a minute"), **How this server updates** (two read-only settings of the host and
  Caravana's release-check setting), and **Past updates** (Caravana's version changes, boots, OS
  package runs), the last 10.
- **FR-021**: Caravana MUST never install, upgrade or reboot anything, and MUST never name a
  vulnerability.
- **FR-022**: A pending reboot MUST be a needs-you item (`os_reboot_required`, failed) naming the kernel
  versions or the package; security updates waiting MUST be a needs-you item (`os_security_updates`,
  warning) opened only when the list is less than 48 hours old and, with Ubuntu's automatic updates
  on, only after 24 hours. Both MUST notify through the existing problem events and resolve when the
  condition clears.
- **FR-023**: A newer Caravana release MUST send one `caravana.release` notification per release to
  every channel, and MUST NOT be a needs-you item.
- **FR-024**: Caravana MUST record its own version at each start, keeping a row when it changes, for
  Past updates.

**API and access**

- **FR-025**: Everything the pages show MUST be in the public API (principle V): the host read grows the
  server's facts; a read of recent activity; a read of updates, a write of the release-check setting and
  a request to check now. Deploy tokens MUST be refused by the default deny. New reads of host files
  MUST carry no secret and no command output.

### Key Entities

- **Server facts**: what Nomad reports about the node (name, state, pool, OS, kernel, versions, CPU,
  memory, addresses, boot time, last heartbeat), refreshed with 025's sampling, held in memory.
- **Activity entry**: a time, a kind in Caravana's words, a status mark, a sentence, and optionally the
  app and its place; derived from records Caravana already keeps.
- **Release check**: the latest release seen (version, date, notes, address), when it was checked, its
  last error, and the owner's on/off setting; kept across restarts.
- **OS update state**: reboot pending and its packages, running and installed kernel, updates waiting
  and security count with their age, the automatic-update settings, package names when checked; read
  from the host's files, not stored.
- **Version history**: Caravana's version at each start, kept when it changes.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: On the maintainer's host, a reboot pending after a kernel update appears on Updates, on
  the needs-you list and as a notification on every channel within one sampling period (2 minutes) of
  the reboot file appearing, naming both kernel versions.
- **SC-002**: The owner can tell which kernel and which versions of Nomad, Docker, Traefik and Caravana
  their server runs, and since when it is up, in one click from any page.
- **SC-003**: A new Caravana release is shown on Updates and notified within 25 hours of its
  publication, and never notified twice.
- **SC-004**: With the release check off, Caravana makes zero requests to GitHub (checked with a
  request log over 48 hours in the lab).
- **SC-005**: Overview and the server page load in under 1 second on the maintainer's host, and the
  package-name check, the only slow read, never runs on its own.
- **SC-006**: No page, API answer, log or notification of this feature contains a secret value or
  command output (checked with sentinels, as 020.5 and 022 do).
- **SC-007**: Every page works without JavaScript, at 1440, 1200, 640 and 390 pixels wide, and passes
  the designer's review against the contracts.

## Assumptions

- One server. The Operator pages that need several (a servers list, adding a server, placement,
  cluster certificates, pools, capacity across servers) and actions on the host (a shell, draining,
  applying updates, an update window) stay in Phase 3.
- The server is Ubuntu 24.04, as the installer requires; other distributions get one sentence where
  OS updates would be.
- The release check is on by default with an opt-out checkbox (the designer's contract); the maintainer
  may flip the default before release.
- Activity, Operator logs and Proxy pages come later, each with its own contract; until then Live
  activity has no "See all" link.
- The installer may need to change only if the research's file reads fail under the systemd unit's
  sandbox on a real host; the lab check is part of the plan.
