# Research: 027 Operator view (one host)

Date: 2026-10-30. Marks: **lab, observed** (run in the Lima lab or against GitHub), **docs/code**
(with the source). The lab VM runs Ubuntu 26.04 (kernel 7.0.0-38, Nomad 2.0.7, Docker 29.1.3), not
the 24.04 the installer accepts, so its apt and `pro` behaviour is a close proxy, not proof. It was
short of memory during the spike (load 24, 20 leftover app containers), so the timings below are
noisy: clear the lab before the 027 lab steps.

## R1 Node facts

**Decision:** read the server's facts from the node Caravana already caches (`deploy` `nodeInfo()`,
`Nodes().Info`, cached 10 minutes) and uptime from the `Nodes().Stats` call 025's sampler makes every
minute. No new Nomad call and no policy line (025 added `node { policy = "read" }`).

Lab, observed (`GET /v1/node/<id>`):

| Fact | Where |
|---|---|
| Name | `Name` (also `unique.hostname`) |
| OS | attributes `os.name`, `os.version` (`ubuntu`, `26.04`) |
| Kernel | `kernel.version` (`7.0.0-38-generic`), `kernel.arch` |
| Nomad | `nomad.version` |
| Docker | `driver.docker.version` |
| CPU | `cpu.numcores`, `cpu.totalcompute` (a total in MHz); `cpu.modelname` and `cpu.frequency` exist on x86 only (absent on the arm VM): optional |
| Memory | `memory.totalbytes`; `NodeResources.Memory.MemoryMB` |
| Address | `unique.network.ip-address` |
| State | `Status`, `SchedulingEligibility`, `DrainStrategy`, `StatusUpdatedAt` |
| Node events | `Events[]` ("Node heartbeat missed", "Node reregistered by heartbeat"): capped, usable for "Recent on this server" |

- **Uptime and boot time:** `GET /v1/client/stats` has `Uptime` (seconds) and `Timestamp` (ns): boot
  time = `Timestamp/1e9 - Uptime` (observed within 3 s of `/proc/stat` btime).
- The node has no "created" time (`CreateIndex` isn't one), so the handoff's "joined" can't be drawn
  (page 2 already rules it Phase 3).
- **Rejected:** reading `/proc` or `/etc/os-release` directly: Nomad already reports them, and the
  API path works for any node in Phase 3.

## R2 Versions of the parts

| Part | How Caravana learns it | Changes with |
|---|---|---|
| Caravana | its build version (`main.version`, `/v1/health`) | a Caravana release |
| Nomad | `nomad.version` | the installer's pin (`NOMAD_VERSION`, `install.sh`) |
| Traefik | the `traefik` job's image tag, `Jobs().Info("traefik")` (the policy's `read-job` covers it; lab: `traefik:v3.7.13`) | the installer's pin (`deploy/install/traefik.nomad.hcl`) |
| Docker | `driver.docker.version` | apt (`docker.io` from Ubuntu's archive) |
| Kernel | `kernel.version` | apt, and a reboot |

**Decision:** Nomad and Traefik read as "pinned by this caravana release"; Docker and the kernel as
the OS's. The Traefik tag is what is registered (the installer re-registers it on every run), which is
what runs once the job is healthy.

## R3 OS updates and reboot

All files below are mode 644 and readable by an unprivileged user (lab, observed with `stat`; the
unit runs `User=caravana` with `ProtectSystem=strict`, which keeps `/run`, `/var/lib`, `/var/log` and
`/etc` readable: docs).

| What | Source | Freshness |
|---|---|---|
| A reboot is needed, and which packages asked | `/run/reboot-required`, `/run/reboot-required.pkgs` (one package per line; written by `notify-reboot-required` from package postinsts and by unattended-upgrades) | immediate; cleared at boot (tmpfs) |
| Updates waiting, and how many are security | `/var/lib/update-notifier/updates-available`: "N updates can be applied immediately." and, only when M > 0, "M of these updates are standard security updates." (gettext text) | rewritten by apt's post-invoke hooks after `apt-daily` and every dpkg run, so as fresh as the last `apt update`; lab: 3 days old, 55 against a live 72 |
| Which packages, including a kernel | `pro security-status --format json` (unprivileged with a writable `HOME`, `/var/lib/caravana`): each upgradable package with origin and `service_name` | live, but 4 s idle and up to 69 s on a starved 2 GB host |
| The same counts, live | `/usr/lib/update-notifier/apt-check` (`72;1` on stderr) | live, 3 s idle, ~93 MB RSS, OOM-killed twice on the starved lab: **not used** |
| Automatic updates on/off | `/etc/apt/apt.conf.d/20auto-upgrades` (`APT::Periodic::Unattended-Upgrade "1"`) | static |
| Automatic reboot and its time | `/etc/apt/apt.conf.d/50unattended-upgrades` (`Unattended-Upgrade::Automatic-Reboot`, `…-Time`; commented out = off, the default) | static |
| What was installed, when, by whom | `/var/log/apt/history.log`: blocks of `Start-Date`, `Commandline` (`/usr/bin/unattended-upgrade` for the automatic runs), `Upgrade: pkg:arch (old, new), …`, `End-Date`; rotated monthly to `history.log.N.gz` | appended on every apt run |

**Decisions:**
- Always read `/run/reboot-required` and `.pkgs`. With a `linux-image-*` in `.pkgs`, the running
  kernel is Nomad's `kernel.version` and the installed one is the highest `linux-image-*` version in
  the newest history block (or `/boot/vmlinuz-*`): "kernel 6.8.0-149 is installed, but 6.8.0-147 is
  still running".
- Read the counts from `updates-available` and show its mtime ("checked by Ubuntu 3 h ago"); parse the
  two integers defensively (missing security line = 0); older than 48 hours reads "unknown".
- Package names come only from `pro security-status`, run **on demand** ("Check now" on Updates), one
  at a time, 120 s timeout, result kept in memory for an hour. Never on a timer: on a 2 GB host it costs
  memory and minutes. Without it, the page shows counts only (page 3 allows that).
- A pending kernel can be an `-updates` package, not a security one (lab: `linux-image-virtual`
  upgradable from -updates). So "a kernel is waiting" and "security updates are waiting" are different
  facts; the reboot item covers the kernel once it is installed.
- Read the newest 10 runs from `history.log` (and `.1.gz` when it holds fewer) for Past updates and
  page 2's "Recent": "Ubuntu updates · 14 packages" with "by unattended-upgrades" or "by hand".
- A host without these files (not Ubuntu or Debian): one ○ sentence, nothing else.

## R4 A newer caravana

Lab (from the Mac), observed: `GET https://api.github.com/repos/Cosmic-Chimps/caravana/releases/latest`
answered `tag_name` `v0.15.0`, `published_at` 2026-10-30T02:29:46Z, `prerelease` false, the body =
the CHANGELOG section (2.5 KB of markdown), `html_url`, assets (tarballs, `install.sh`, `SHA256SUMS`).
Limits: 60 requests an hour per address unauthenticated; `ETag` given; a 304 to `If-None-Match` doesn't
count (docs). `/latest` skips drafts and pre-releases.

**Decisions:**
- Check about a minute after start, then every 24 hours with some jitter; a "Check now" at most once
  per 10 minutes. `User-Agent: caravana/<version>`, `If-None-Match`, 10 s timeout, no token, nothing
  about the server.
- One small SQLite row: tag, published time, URL, body (capped at 16 KiB), ETag, checked time, last
  error, and the owner's on/off setting (on by default, page 3's checkbox; the API is
  `putUpdateSettings`).
- A release counts when its version is higher than the running one by the installer's ordering
  (`sort -V`); builds that aren't a release version (`dev`, `0.0.0-dev.N`) don't compare.
- The pins of the newer release (Nomad, Traefik) come from its published `install.sh` (the
  `NOMAD_VERSION` line) and `traefik.nomad.hcl` inside the tarball, or are left out; the notes say what
  changed only when both are read.
- Updating is rerunning the newer release's installer as root (`install.sh` copies the data first and
  puts the old binary back if the new one fails health). Caravana runs unprivileged: it shows the
  command and the notes, never runs them.

## R5 Live activity

Nomad's event stream needs `namespace:read-job` for Deployment, Allocation, Job, Evaluation and Service,
and `node:read` for Node (docs); the scoped policy has both, but not `*`. Lab, observed (15 s,
management token, a just-rebooted VM): 106 frames, about 700 KB: an Allocation event is about 8 KB (the
whole allocation), a Job event about 11 KB (the whole job), Evaluation events dominate (160 of 266). The
buffer lives in Nomad's memory and is lost when Nomad restarts.

Caravana's own records already hold what an owner reads, in Caravana's words, and survive restarts:
`deployments` (with `deployWho` and origin), `app_actions` (stop, start, restart, run, terminal),
backups and restores, `schedule_runs`, `sent_problems` (needs-you opened and resolved). The `/live`
notices already push on each (`ChangeActivity`, `ChangeApps`, `ChangeAttention`, `ChangeActions`,
`ChangeSchedules`). Out-of-memory kills and crash loops are already derived (needs-you).

**Recommendation (for the designer's ruling):** build Live activity from Caravana's records (one union
read, newest first) plus the node's `Events[]` for server lines, and leave Nomad's event stream out of
027. It would add a long-lived connection, reconnect logic, megabytes of JSON with every job's env, and
noise (evaluations, backup periodics, schedule-run jobs, sidecars). Revisit it only if live per-copy
lines that Caravana doesn't record are wanted. With records the feed survives restarts, so page 1's
"Since caravana started at …" isn't needed.

## R6 What 025 already gives

`service.Host(ctx)` reads only the store (never 502): sampled time and staleness, CPU and memory used,
reserved and total, the watched disks with their states (tied to `disk_nearly_full`), the room sentence,
7 days of hourly memory, the sources' outcomes. `/parts/platform` (the Apps home's Platform panel) and
`/parts/monitoring/host` swap on the `metrics` notice; the API is `getHost` (0.28.0). It has no name,
OS, versions or uptime.

**Decision:** extend `HostView` with a server block (name, state, pool, OS, kernel, Nomad, Docker,
Traefik, cores, MHz, model when known, memory total, boot time, last heartbeat, addresses), filled by
the sampler's existing tick from the cached node and its Stats, held in memory like 025's Traefik
snapshot: no migration; for at most a minute after a restart the block is empty. Overview and the
server page reuse `Host()` and its templates.

## R7 Several servers later (Phase 3)

| Source | Works for any node? |
|---|---|
| Node attributes, client stats, uptime, node events | Yes, through the Nomad API |
| Nomad, Docker, kernel versions | Yes (attributes) |
| Traefik version | Yes while Traefik is one job |
| Caravana version, the release check | The control plane only (agents would report their own) |
| `reboot-required`, `updates-available`, apt history, `pro` | No: files and processes on the host Caravana runs on; each node needs an agent or an SSH read |
| Applying updates (installer, apt, reboot) | Per host over SSH: Phase 3's add-node channel |
