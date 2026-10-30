# Data model: 027 Operator view

## In memory (no table)

### Node facts (`deploy.NodeFacts`, on `deploy.Host` and `service.HostView.Node`)
From the cached `*api.Node` (10 min) and the sampler's Stats call (1 min). Fields optional where Nomad
may not report them.

| Field | Source | Notes |
|---|---|---|
| `Name` | `Node.Name` | the page 2 address |
| `State` | `Status`, `SchedulingEligibility`, `DrainStrategy` | ready, draining, ineligible, down, initializing |
| `Pool` | `NodePool` | `default` |
| `OS`, `OSVersion` | `os.name`, `os.version` | `ubuntu`, `24.04` |
| `Kernel`, `Arch` | `kernel.version`, `kernel.arch` | the running kernel |
| `NomadVersion`, `DockerVersion` | `nomad.version`, `driver.docker.version` | |
| `Cores`, `TotalMHz`, `CPUModel?`, `CoreMHz?` | `cpu.numcores`, `cpu.totalcompute`, `cpu.modelname`, `cpu.frequency` | model and per-core MHz absent on arm |
| `MemoryMB` | `NodeResources.Memory.MemoryMB` | |
| `Address` | `unique.network.ip-address` | the private address; the public one is the console's host |
| `LastHeartbeat` | `StatusUpdatedAt` or the Stats time | "last seen" |
| `Events[]` | `Node.Events` (time, subsystem, message) | for server activity lines |
| `BootTime` | `Stats.Timestamp/1e9 - Stats.Uptime` | "up since" |
| `TraefikVersion` | the `traefik` job's image tag | |

Validation: a missing attribute is empty, never a made-up value; the page shows only what is known.

### OS state (`hostos.State`, kept by the service from the sampler tick)

| Field | Source |
|---|---|
| `Readable`, `Distro` | `/etc/os-release` (`ID`, `VERSION_ID`); not readable or not ubuntu/debian = one sentence |
| `RebootRequired`, `RebootSince`, `RebootPackages[]` | `/run/reboot-required` (exists, mtime), `.pkgs` (one per line) |
| `InstalledKernel?` | highest `linux-image-*` in the newest apt history run, else `/boot/vmlinuz-*` |
| `Updates`, `Security`, `ListCheckedAt` | `/var/lib/update-notifier/updates-available` (two integers, missing security = 0), its mtime |
| `AutoUpdates?`, `AutoReboot?`, `AutoRebootTime?` | `20auto-upgrades` (`APT::Periodic::Unattended-Upgrade "1"`), `50unattended-upgrades` (`Unattended-Upgrade::Automatic-Reboot`, `…-Time`, `//` comments ignored) |
| `Packages[]?`, `PackagesKernel`, `PackagesCheckedAt`, `PackagesError?` | `pro security-status --format json`, on demand, 1 h |

Rules: the list is **stale** past 48 h (shown with its age, judged by no item); a kernel in
`RebootPackages` makes the reboot sentence name both kernels; the running kernel is `NodeFacts.Kernel`.

### Activity entry (`service.ActivityEntry`, built per read)
`Time`, `Kind` (deploy, restart, stop, start, command, backup, restore, schedule, needs you, server),
`Status` (healthy, warning, failed, idle: the mark), `Sentence` (Caravana's words), `AppID?`, `Place?`,
`ID` (stable: source table and row id, for the panel's swap). Never command output, arguments or secret
values.

## SQLite: migration `0020_operator.sql`

### `release_check` (one row, `id = 1`)

| Column | Type | Notes |
|---|---|---|
| `enabled` | INTEGER NOT NULL DEFAULT 1 | the owner's setting |
| `latest_tag` | TEXT | `v0.16.0` |
| `published_at` | TEXT | RFC 3339 |
| `url` | TEXT | the release page |
| `body` | TEXT | capped at 16 KiB |
| `nomad_pin`, `traefik_pin` | TEXT | from the release's installer, when read |
| `etag` | TEXT | for `If-None-Match` |
| `checked_at` | TEXT | the last attempt |
| `error` | TEXT | the last failure in Caravana's words, empty when ok |
| `notified_tag` | TEXT | the last tag a `caravana.release` was queued for |

### `host_events`

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PRIMARY KEY | |
| `kind` | TEXT NOT NULL CHECK (kind IN ('boot','version','os_packages')) | |
| `at` | TEXT NOT NULL | the event's time (boot time, start time, the apt run's start) |
| `detail` | TEXT NOT NULL | JSON: `{"from":"0.14.0","to":"0.15.0"}`, `{"packages":14,"by":"unattended","reboot":true}` |
| `key` | TEXT UNIQUE | dedup: `boot|<boot time>`, `version|<version>`, `os|<apt start>` |

Pruned to the newest 200. Written by `Service.Start` (version) and the sampler (boot, os_packages).

### `problem_events`

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PRIMARY KEY | |
| `key` | TEXT NOT NULL | the `sent_problems` key |
| `kind` | TEXT NOT NULL | the attention kind |
| `app_id` | TEXT NOT NULL DEFAULT '' | |
| `change` | TEXT NOT NULL CHECK (change IN ('opened','worsened','resolved')) | |
| `at` | TEXT NOT NULL | |
| `summary` | TEXT NOT NULL | the sentence sent |

Appended by `ApplyProblemCheck` in its transaction, pruned to the newest 200; an app's rows go with the
app.

### `host_marks`

| Column | Type | Notes |
|---|---|---|
| `key` | TEXT PRIMARY KEY | `security_since` |
| `at` | TEXT NOT NULL | when the security count first went above zero; deleted when it is zero |

## State transitions

- **Reboot item**: file appears → opened (failed) on the next check; file gone (after a boot) → resolved
  after `resolveAfter`; a `boot` host event is written.
- **Security item**: count > 0 → `security_since` set; opened when the list is fresh and (auto-updates off,
  or 24 h since `security_since`); count 0 or the list stale → not judged / resolved.
- **Release**: check finds a tag newer than the running version and than `notified_tag` → shown, one
  notification, `notified_tag` = tag; Caravana upgraded to it → no longer newer; a `version` host event.
