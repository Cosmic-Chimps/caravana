# Implementation Plan: The Operator view (one host)

**Branch**: `027-operator-view` | **Date**: 2026-10-30 | **Spec**: [spec.md](spec.md)
**Input**: the spec, `research.md` (R1-R7, lab-observed 2026-10-30) and the designer's contracts
`contracts/page1-overview.md`, `page2-server.md`, `page3-updates.md` (ruled on the research).

## Summary

The console gains a second view about the host Caravana runs on. A toggle under the brand switches
between Developer (everything built so far) and Operator (pages under `/ops`). Three pages, built and
reviewed in order: **Overview** (`/ops`: the shared headline, a stats line, one server panel, every app
across places, needs-you, activity from Caravana's own records, capacity), **the server page**
(`/ops/servers/<name>`: state, numbers, copies running here, the server's facts, recent events), and
**Updates** (`/ops/updates`: a newer Caravana from GitHub's releases, the OS's pending updates and
reboot read from files on the host, how to apply them by hand, past updates, two needs-you kinds and a
release notification). Nothing is applied from the console, no vulnerability is named, no new Nomad
call or policy line is needed (R1), and Nomad's event stream is not read (R5).

## Technical Context

**Language/Version**: Go 1.27, no CGO.
**Primary Dependencies**: `net/http`, `html/template` + HTMX (vendored), `github.com/hashicorp/nomad/api`,
`modernc.org/sqlite`. No new module: `compress/gzip`, `encoding/json`, `os/exec` and `io/fs` from the
standard library cover the host files, apt's rotated history and `pro`.
**Storage**: SQLite, migration `0020_operator.sql` (four small tables, see data-model.md).
**Testing**: table tests for every parser and rule (`internal/hostos` with `fstest.MapFS`), service tests
with fakes (Nomad `deploytest`, a fake host root, a fake release server, a fake `pro`), API contract and
parity tests, UI tests, `TestConsoleLab` steps per page and a gate step for the reboot case.
**Target Platform**: Ubuntu 24.04 hosts installed by `deploy/install/install.sh` (Caravana under systemd as
`caravana`, `ProtectSystem=strict`); the Lima lab (Ubuntu 26.04) for development.
**Project Type**: single Go binary, server-rendered web console plus public API.
**Performance Goals**: Overview and the server page render under 1 s on the maintainer's host (SC-005):
every read is the store, the in-memory host block, or one `Allocations().List` with stats the Running tab
already makes. File reads are bytes, done on the sampler's tick, never per request.
**Constraints**: no root, no `apt`/`apt-check` runs; `pro security-status` only on demand (one at a time,
120 s timeout, once per 10 min, kept 1 h); GitHub at most once a day plus a rate-limited "Check now", with
an ETag; no secret or command output in any new read (SC-006); forms work without JavaScript.
**Scale/Scope**: one host, up to about 50 copies; activity keeps the last 200 problem events and 50 host
events; the release row is one row.

## Constitution Check

*GATE: checked against constitution 1.1.0 before design and again after it.*

| Principle | Assessment |
|---|---|
| I. Small | PASS. No module, no runtime dependency: `pro` is Ubuntu's own tool, optional and run only on the owner's request; without it the page shows counts. One more goroutine (the release check) wakes once a day; OS files are read on 025's existing one-minute tick. |
| II. One Model | PASS. Nothing touches `app.App`, `Translate` or any job; read-only. |
| III. Safe Deploys | PASS. No deploy path changes. |
| IV. Fail Loudly | PASS. An unreadable file, a non-Ubuntu host, GitHub unreachable, `pro` missing or timing out, Nomad unreachable: each has its own sentence and mark in the contracts; a stale update list says its age and opens no item. Nothing is drawn as 0 when unknown. |
| V. API-First | PASS. `getHost` grows `node`, plus `listActivity`, `getUpdates`, `putUpdateSettings`, `checkUpdates`, the two attention kinds and the `caravana.release` event, in openapi 0.29.0 with the routes; the console calls the same service methods; deploy tokens are denied by the default deny; console parity entries for every new page and part. |
| VI. Nomad-Native | PASS. Nomad through the official client, inside `deploy` only (the cached node's attributes and events, its Stats, `Jobs().Info("traefik")` for the image tag): the service sees plain structs. The host's OS files are not Nomad: they live in a new pure package `internal/hostos` read by the service. |
| VII. Secure | PASS. No new Nomad capability. The host files read are world-readable package metadata. The GitHub call sends only `User-Agent: caravana/<version>` and an ETag; the owner can turn it off. `pro` runs with `HOME` set to Caravana's data directory and no other environment. Command output never reaches a page (only parsed package names and counts). No root shell, no apply. |
| VIII. Plain Go | PASS. Pure parsers and rules with tables; seams with fakes for the host root (`fs.FS`), the `pro` runner and the release fetcher; one plain loop for the release check; errors wrapped. |

No violation: Complexity Tracking stays empty.

## Decisions carried (from the research and the contracts)

1. **Server facts in memory (R1, R6).** `deploy.HostStats` already reads the cached `*api.Node` (10 min)
   and its Stats every sampler tick. `deploy.Host` gains a `Node` block (name, status, eligibility,
   drain, pool, OS name and version, kernel, arch, Nomad and Docker versions, cores, total MHz, CPU model
   and per-core MHz when present, memory total, address, last status change, node events) and `BootTime`
   (`Stats.Timestamp - Uptime`). The sampler keeps the newest in memory like 025's Traefik snapshot;
   `HostView` gains `Node`. Traefik's version: `deploy.TraefikImage` reads `Jobs().Info("traefik")` on the
   same 10-minute cache. No migration for these.
2. **Activity from records (R5).** `store.ListActivity(limit, before, serverOnly)` is one `UNION ALL`
   read, newest first, over: `deployments` (started, and ended with status), `app_actions`, `backups`
   and `restores`, `schedule_runs` with outcome failed or stopped at limit, `problem_events` (new: opened,
   worsened, resolved), `host_events` (new: boots, Caravana version changes, OS package runs found in
   apt's history). The node's `Events[]` come from the in-memory node block and are merged in the
   service. The service turns rows into `ActivityEntry` with Caravana's words (page 1's kinds: deploy,
   restart, stop, start, command, backup, restore, schedule, needs you, server), never command output.
   The panel refreshes on the existing notices (`activity`, `actions`, `schedules`, `attention`) plus a
   new `host` notice when a host event is written.
3. **`problem_events`.** `sent_problems` rows are deleted on resolve, so `store.ApplyProblemCheck` also
   appends one row per opened, worsened and resolved item (same transaction), pruned to the newest 200.
4. **OS state from files (R3).** `internal/hostos` (pure, plus `real.go` for the `os.DirFS("/")` root and
   the `pro` runner): `ReadState(fsys)` returns reboot pending (and since, from the file's mtime),
   packages from `.pkgs`, the newest installed kernel (highest `linux-image-*` in the newest apt history
   blocks, else `/boot/vmlinuz-*`), counts and age from `updates-available` (defensive parse, security
   missing = 0), unattended-upgrades enabled and auto-reboot with time (comments and quotes handled), the
   distro (from `/etc/os-release`); `ReadHistory(fsys, n)` parses `history.log` and `history.log.1.gz`
   into runs (start, end, by unattended-upgrades or by hand, package count, upgraded packages);
   `ParseSecurityStatus(json)` gives package names and whether a kernel is among them; `KernelNewer`,
   `ReleaseNewer` (the installer's `sort -V` ordering; non-release versions never compare). The service
   reads the state on 025's sampler tick and keeps it in memory; the running kernel is the node's
   `kernel.version`.
5. **Package names on demand.** `Service.CheckPackages` starts `pro security-status --format json`
   (`HOME` = the data dir, 120 s, one at a time, refused within 10 min with `ErrCheckTooSoon`), keeps the
   result 1 h in memory, and notifies `updates`. Missing `pro` is a sentence, not an error.
6. **Release check (R4).** `Service.runReleaseCheck`: a minute after start, then every 24 h with up to
   1 h of jitter, and `CheckRelease` on demand (refused within 10 min). `GET
   https://api.github.com/repos/Cosmic-Chimps/caravana/releases/latest` with `If-None-Match`, 10 s timeout,
   `User-Agent: caravana/<version>`; the URL is a setting for tests (`SetReleaseURL`). One row
   `release_check` keeps tag, published time, URL, body (capped 16 KiB), ETag, checked time, last error and
   `enabled` (default on). When off, no request is made. A newer release than the last notified one
   queues `caravana.release` once (`notified_tag`). The notes shown are the body's first 6 bullet lines;
   pins (Nomad, Traefik) come from the release's `install.sh` (`NOMAD_VERSION=`) and its
   `traefik.nomad.hcl` image when both can be read, else are left out.
7. **Version history.** At start, `Service.Start` writes a `host_events` row `version` when the running
   version differs from the newest recorded one; the sampler writes `boot` when the boot time moves by more
   than 60 s, and `os_packages` for each new apt history run (keyed by its start time).
8. **Attention.** `os_reboot_required` (failed, key `os_reboot_required|||`) while the reboot file exists;
   `os_security_updates` (warning) when security count > 0, the list is under 48 h old, and, with
   unattended-upgrades on, the count has been above zero for 24 h (`host_marks.security_since`, so a
   restart doesn't reset it). Both resolve by the existing rule; their sentences follow page 3. A stale
   host reading (sampler older than 5 min) judges neither, as `disk_nearly_full` does.
9. **Notifications.** `notify.EventCaravanaRelease = "caravana.release"` with `Release{Current, Latest,
   URL}` on `notify.Event`; `Render` gives each channel kind a one-line message ("caravana 0.16.0 is out.
   You run 0.15.0."); the webhook body documents it. Problem events for the two kinds reuse the existing
   path.
10. **The mode.** `/ops` and below are the Operator view; `internal/ui` sets `caravana_mode=ops` (or `dev`
    on Developer pages) as a plain cookie (`SameSite=Lax`, `Path=/`, `HttpOnly`, `Secure` over HTTPS) and
    shared pages read it to draw the sidebar. Not a security value.

## Design

### `internal/hostos` (new, pure but `real.go`)
`state.go` (`State`, `ReadState`), `history.go` (`Run`, `ReadHistory`, gzip), `securitystatus.go`
(`ParseSecurityStatus`), `version.go` (`KernelNewer`, `ReleaseNewer`), `real.go` (`Root() fs.FS`,
`RunPro(ctx, home) ([]byte, error)`). A test forbids `net/http`, `os/exec` and `os.Open` outside
`real.go`. Fixtures under `testdata/` (Ubuntu 24.04 files: a pending kernel, a `libc6` reboot, counts
with and without the security line, auto-upgrades on/off and auto-reboot at 02:00, a history with an
unattended run and a manual run, a rotated `.gz`, a `pro` JSON with a kernel).

### `internal/deploy`
`host.go`: `Host.Node` (`NodeFacts`) and `Host.BootTime` filled from the cached node and the Stats call;
`TraefikImage(ctx)` with the same cache. `deploytest`: node attributes and events, client stats `Uptime`
and `Timestamp`, a `traefik` job. No new endpoint is called.

### `internal/store`
`0020_operator.sql`: `release_check` (one row), `host_events`, `problem_events`, `host_marks`
(data-model.md). `ListActivity`, `AppendHostEvent`, `HostEvents`, `ReleaseCheck`/`SaveReleaseCheck`,
`HostMark`/`SetHostMark`; `ApplyProblemCheck` appends `problem_events` and prunes.

### `internal/service`
`host.go`: `HostView.Node`, the OS `State` and its age, kept from the sampler tick (`sampler.go` reads
`hostos.ReadState` each tick, writes boot and package-run events). `activity.go`: `Activity(ctx,
ActivityQuery)` (limit 1-50, `Server` flag for page 2), the words table. `updates.go`: `Updates(ctx)`,
`SetReleaseCheck(ctx, enabled)`, `CheckRelease`, `CheckPackages`, `runReleaseCheck`; setters
`SetHostRoot(fs.FS)`, `SetProRunner`, `SetReleaseURL`, `SetReleaseCheckGap` for tests and the lab.
`attention.go`: the two kinds. `notifier.go`: their words and the release event. `changes.go`:
`ChangeHost` ("host": a host event or the OS state changed) and `ChangeUpdates` ("updates": a check
finished). `copies.go`: `HostCopies(ctx)` for page 2 (every copy on the node with app, service, place
and stats now), reusing `deploy.Copies` and `deploy.Usage`.

### API 0.29.0 (`internal/api/host.go`, `activity.go`, `updates.go`)
See `contracts/api.md`. Deploy tokens denied (the scope walk lists the new routes); parity entries.

### Console (`internal/ui/ops.go`, `opsserver.go`, `opsupdates.go`, `shell.go`, templates `ops.html`, `opsserver.html`, `opsupdates.html`)
The toggle and the Operator sidebar in `shell.html`; the mode cookie; `/ops`, `/ops/servers` (302),
`/ops/servers/{name}`, `/ops/updates`, `POST /ops/updates/check` (`what=release|packages`, 303 back),
`POST /ops/updates/settings` (303); parts `/parts/ops/overview` (stats, server panel, apps),
`/parts/ops/activity`, `/parts/ops/server`, `/parts/ops/updates`, swapped on `metrics`, `apps`,
`activity`, `actions`, `schedules`, `attention`, `host`, `updates`. The headline is the home's function;
needs-you is the home's template; capacity is 025's "This host" template without the chart. Styles under
`/* Page 027 */` in `internal/ui/style/input.css`; `tools/ui-css.sh build`; `DESIGN.md` records the toggle,
the Operator sidebar and the activity row.

### Installer and lab
`deploy/dev/install-test.sh`: after an install, `getUpdates` answers `os.readable: true` with a distro of
`ubuntu` (the unit's sandbox lets Caravana read the files), and `getHost` has a `node` with a kernel. No
installer change is expected; if the sandbox blocks a path, `ReadWritePaths` isn't needed (reads only)
but `ProtectSystem` exceptions would be, and the plan changes. The lab: `TestConsoleLab` steps
`console-ops-overview`, `console-ops-server`, `console-ops-updates` (the last with a fake host root
through `SetHostRoot` holding a pending kernel and counts, and a fake release server); a gate step
`os-updates` that does the same through the in-process Caravana and checks the notification reaches a
test receiver. A real pending kernel on the lab VM is optional (install a newer `linux-image` without
rebooting) and recorded if done.

### Build order
Page 1 (toggle, mode, sidebar, Overview with activity; `problem_events`, `host_events` boots and versions,
`listActivity`, `getHost.node`), its lab step, the designer's review and critique; then page 2 (server
facts, host copies, recent with apt history), its step and review; then page 3 (`hostos` state and
history in full, release check, attention kinds, notifications, `getUpdates`), its step, the gate step and
review. CHANGELOG, `docs/plan.md` (the single-host Operator pages move to 2.6) and `CLAUDE.md` at the end.

## Project Structure

### Documentation (this feature)

```text
specs/027-operator-view/
├── spec.md
├── research.md
├── plan.md              # this file
├── data-model.md
├── quickstart.md
├── contracts/
│   ├── page1-overview.md   # the designer's
│   ├── page2-server.md     # the designer's
│   ├── page3-updates.md    # the designer's
│   └── api.md
├── checklists/requirements.md
└── tasks.md             # /speckit-tasks
```

### Source Code (repository root)

```text
internal/hostos/          # new: OS update state, apt history, pro's JSON, version ordering
internal/deploy/          # host.go (node facts, boot time), Traefik image
internal/store/           # 0020_operator.sql, activity, host events, release check, marks
internal/service/         # host, activity, updates, attention, notifier, changes, copies
internal/notify/          # caravana.release
internal/api/             # host, activity, updates; openapi 0.29.0
internal/ui/              # shell toggle and mode, ops pages and parts, styles
api/openapi.yaml
cmd/caravana/             # console_ops_*_test.go, gate_os_updates_test.go
deploy/dev/install-test.sh
```

**Structure Decision**: the repository's single-project layout; one new pure package (`internal/hostos`)
for the host's OS files, as 025 added `internal/metrics` for Traefik's text.

## Complexity Tracking

None.
