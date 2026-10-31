# Tasks: The Operator view (one host)

**Input**: `specs/027-operator-view/` (spec.md, plan.md, research.md, data-model.md, contracts/page1-overview.md,
page2-server.md, page3-updates.md, contracts/api.md, quickstart.md)

**Tests**: required (constitution VIII): table tests for every parser and rule, service tests with fakes, API
contract and parity tests, UI tests, a `TestConsoleLab` step per page and a gate step. Tests ride with each task;
a task is done when its tests pass.

**Organization**: one phase per page (user story), in plan.md's build order. Each page phase ends with its lab step
and the designer's review cycle (review, impeccable critique with no detector, rulings, fixes, last look), as
021-025.

## Format: `[ID] [P?] [Story] [Requirements] Description`

- **[P]**: can run in parallel (different files, no dependency on an unfinished task)
- **[Story]**: US1 (page 1, Overview), US2 (page 2, the server), US3 (page 3, Updates)
- **[Requirements]**: the spec's FR and SC IDs a task implements

## Phase 1: Setup

- [X] T001 Confirm the lab is cleared of leftover apps and running (`deploy/dev/lab.sh up`; research found it
  starved at 2 GB), and record the baseline: `go build ./... && go test ./internal/...` green on branch
  `027-operator-view` before any change; note the result under T001 in this file.
  **Baseline (2026-10-29/30)**: `go build ./...` and `go test ./internal/...` green on `027-operator-view` at
  `eb1c66a` (every package ok; api 148 s, service 241 s, ui 199 s; the run overlapped the first edits of T002, so
  the store package was compiled with them, and nothing failed). The lab VM `caravana-lab` was **Running**
  (aarch64, 2 CPUs, 2 GiB); it was not started, stopped or cleared, and its leftover apps were not looked at, since
  no lab step runs before page 1's T014.

## Phase 2: Foundational (blocks every page)

- [X] T002 [FR-008, FR-024] Migration `internal/store/migrations/0020_operator.sql` with `release_check` (one row,
  `enabled` default 1), `host_events` (kind boot|version|os_packages, `key` UNIQUE, pruned to 200),
  `problem_events` (opened|worsened|resolved, pruned to 200, an app's rows go with the app) and `host_marks`, as
  data-model.md; store code in `internal/store/operator.go` (`AppendHostEvent`, `HostEvents`, `ReleaseCheck`,
  `SaveReleaseCheck`, `HostMark`, `SetHostMark`, `DeleteHostMark`) with tests in `internal/store/operator_test.go`
  (dedup by key, pruning, the single row's defaults).
- [X] T003 [FR-008] `store.ApplyProblemCheck` (`internal/store/notifications.go`) also appends a `problem_events` row
  per opened, worsened and resolved item in the same transaction; `DeleteApp` (`internal/store/apps.go`) deletes the
  app's rows; tests in `internal/store/notifications_test.go`.
- [X] T004 [FR-008] `store.ListActivity(ctx, limit, before, serverOnly)` in `internal/store/activity.go`: one
  `UNION ALL` read, newest first, over `deployments` (started; ended with status), `app_actions` (never arguments or
  output), `backups`, `restores`, `schedule_runs` whose outcome is failed or stopped at its limit, `problem_events`,
  `host_events`; rows carry source table, row id, time, app, place and the fields the words need. `serverOnly` keeps
  host events, problem events of host kinds and of copies' kinds (out of memory, restarting), and app actions that
  restart. Tests in `internal/store/activity_test.go` with a row of each source, ordering, `before` paging and
  `serverOnly`.
- [X] T005 [P] [FR-005, FR-014] `internal/deploy/host.go`: `Host.Node` (`NodeFacts` as data-model.md: name, state
  from status/eligibility/drain, pool, OS and version, kernel and arch, Nomad and Docker versions, cores, total MHz,
  optional model and per-core MHz, memory, private address, last heartbeat, events) and `Host.BootTime`
  (`Stats.Timestamp/1e9 - Uptime`), from the cached node and the existing Stats call; `TraefikImage(ctx)` reading
  `Jobs().Info("traefik")`'s image tag on the same 10-minute cache. `internal/deploy/deploytest`: node attributes
  and events, client stats `Uptime` and `Timestamp`, a `traefik` job. Tests in `internal/deploy/host_test.go`
  (arm node without model, x86 with, a down node, boot time arithmetic).
- [X] T006 [FR-005, FR-014] `internal/service/sampler.go` and `internal/service/host.go`: keep the newest
  `NodeFacts`, `BootTime` and Traefik version in memory on each tick (like the Traefik snapshot) and expose them as
  `HostView.Node` (nil before the first reading); write a `host_events` `boot` row when the boot time moves by more
  than 60 s; tests in `internal/service/host_test.go`.
- [X] T007 [FR-024] `Service.Start` (`internal/service/service.go`) writes a `host_events` `version` row when the
  running version differs from the newest recorded one (`detail` from and to); `ChangeHost` ("host") in
  `internal/service/changes.go`, sent when a host event is written; tests in `internal/service/host_test.go`.
- [X] T008 [FR-001, FR-002, FR-003] The Operator shell in `internal/ui/shell.go` and `internal/ui/templates/shell.html`:
  the toggle under the brand (two links, `aria-current="page"` on the current, `nav aria-label="View"`), the mode from
  the path (`/ops…` is ops) and for shared pages from the cookie `caravana_mode=dev|ops` (`SameSite=Lax`, `Path=/`,
  `HttpOnly`, `Secure` over HTTPS) set by every page of a mode; the Operator sidebar (Server › Overview, Server;
  Platform › Updates, with a slot for its mark) and the active rule of page 1's contract; the Menu panel under 900px;
  sign-in and `/` stay Developer. Tests in `internal/ui/shell_test.go` (toggle on both views, the cookie carried to
  Settings and an app page, active items).

**Checkpoint**: foundation ready; pages can be built.

## Phase 3: User Story 1, page 1: Overview (P1) 🎯 MVP

**Goal**: the toggle reaches `/ops`, an Overview of the host and everything on it.

**Independent Test**: quickstart §2; `console-ops-overview` passes; the designer approves page 1.

- [X] T009 [US1] [FR-008] `internal/service/activity.go`: `Activity(ctx, ActivityQuery{Limit 1-50, Before, Server})`
  merges `store.ListActivity` with the in-memory node `Events[]` (registered, heartbeat missed, back) and turns each
  into `ActivityEntry{ID, Time, Kind, Status, Sentence, AppID, Place}` through a words table: deploy (page 4's
  who-words: "`supplier-gw` v24 is live, pushed to `main` by ana."), restart, stop, start, command (the program only,
  never arguments or output), backup, restore, schedule (failed, or stopped at its limit), needs you (opened,
  worsened, resolved with the summary), server (node events, boots, version changes). Tests in
  `internal/service/activity_test.go`: every kind's sentence, ordering across sources, a sentinel printed by a command
  never appears.
- [X] T010 [US1] [FR-025] API 0.29.0 begins: `getHost` gains `node` (contracts/api.md) in `internal/api/host.go`;
  `listActivity` (`GET /v1/activity?limit&before&server`) in `internal/api/activity.go`; `api/openapi.yaml` 0.29.0
  with both; deploy tokens denied (`internal/api/scope_test.go` walk); parity entries for `/ops` and its parts in
  `internal/api/parity_test.go`; contract tests in `internal/api/host_test.go`, `internal/api/activity_test.go`.
- [X] T011 [US1] [FR-004, FR-005, FR-006, FR-007, FR-009] The Overview: `internal/ui/ops.go` and
  `internal/ui/templates/ops.html` at `/ops`, per `contracts/page1-overview.md`: the headline from the Developer home's
  function, the stats line (servers, apps, copies, cpu and mem over 10 min from 025, open items; "waiting for the
  first reading"), the Server section with the room sentence and one NodePanel (state mark, name, pool chip, copies
  count, spec line, three meters, copies as AllocChips grouped by app with their phases, "and N more copies" past 40,
  linking to the server page), the Apps table across every place (place prefix, kind word, quiet state pill, copy
  marks, CPU 1h sparkline from 025's samples in fg-2, memory now, first domain or "no address", last change; problems
  first; the fold under 1000px; Reach min 140px), Needs you with the home's template, Live activity (the last 8,
  `EventRow`s: time UTC with local in `title`, kind word, mark, sentence; "from caravana's records"), Capacity with
  025's "This host" template without the chart; Nomad unreachable and no-apps states. Tests in
  `internal/ui/ops_test.go`.
- [X] T012 [US1] [FR-008] Parts and live refresh: `/parts/ops/overview` (stats, server panel, apps) and
  `/parts/ops/activity` in `internal/ui/ops.go`, swapped on `metrics`, `apps`, `activity`, `actions`, `schedules`,
  `attention` and `host` notices (forward `host` in the live stream), rows swapped by id with the fresh tint once;
  without script the page is a snapshot. Tests in `internal/ui/ops_test.go`.
- [X] T013 [P] [US1] [SC-007] Styles under `/* Page 027 */` in `internal/ui/style/input.css` (toggle, Operator
  sidebar groups, NodePanel, AllocChip, EventRow, the apps table and its fold), `tools/ui-css.sh build`; `DESIGN.md`
  records the toggle, the Operator sidebar and the activity row; text never fg-4.
- [X] T014 [US1] [SC-005, SC-007] `cmd/caravana/console_ops_test.go`: `TestConsoleLab` step `console-ops-overview` (the
  toggle, the cookie onto Settings, the stats line, the server panel with the lab's spec line, the apps table across two
  places, a deploy and a restart appearing in Live activity, still there after `RestartCaravana`, no command output
  in the page, the page renders in under 1 s).
  - 2026-10-30, lab: `console-ops-overview` passes in 47 s (with `console-sign-in`, `console-clean`, `console-create`
    and `console-delete`). Two test fixes on the way: the API check after `RestartCaravana` waits for the new process's
    first sample (the node block is empty until then, as the spec allows), and the step reopens the app page's live
    stream on its way out, since its restart ends the stream `console-delete` reads.
- [X] T015 [US1] Page 1 review cycle: stage the lab for the designer, the designer's review against
  `pe-ops-now.png`, the impeccable critique (critique only, no detector), the designer's rulings, the fixes, the last
  look; record "Approved" in `contracts/page1-overview.md`.
  - 2026-10-30: review (6 fixes, f0fd007), recheck and critique (28/40, 0 P0; d713575), last look (9210661): Approved.

**Checkpoint**: page 1 works on its own and is approved.

## Phase 4: User Story 2, page 2: the server (P2)

**Goal**: the server page with its facts, its copies and its recent history.

**Independent Test**: quickstart §3; `console-ops-server` passes; the designer approves page 2.

- [x] T016 [P] [US2] [FR-015] `internal/hostos/history.go` (new package; the pure part of plan.md's `hostos`):
  `ReadHistory(fsys, n)` parses `var/log/apt/history.log` and `history.log.1.gz` into runs (start, end, by
  unattended-upgrades when `Commandline` is `/usr/bin/unattended-upgrade`, else by hand, package count, upgraded
  packages with old and new versions); fixtures in `internal/hostos/testdata/`; tests in
  `internal/hostos/history_test.go`. Done with page 3's reboot reader (`ReadReboot`, `state.go`) and `KernelNewer`, which page 2's
  "reboot needed" pill needs.
- [X] T017 [US2] [FR-015] The sampler (`internal/service/sampler.go`) reads `hostos.ReadHistory` from the host root on
  each tick and writes a `host_events` `os_packages` row per new run (key `os|<start>`, detail packages, by, reboot
  when the run installed what `reboot-required.pkgs` names); `Service.SetHostRoot(fs.FS)` (default `os.DirFS("/")`)
  for tests and the lab; tests in `internal/service/host_test.go`.
- [X] T018 [US2] [FR-013] `internal/service/copies.go`: `HostCopies(ctx)` (every copy on the node with app, service,
  place, phase, status, CPU used and reserved, memory used and limit, started), reusing `deploy.Copies` and
  `deploy.Usage`; `listHostCopies` is not added (page 2 reads the service; `listCopies` per app already exists in the
  API, recorded in contracts/api.md as the parity source); tests in `internal/service/copies_test.go`.
- [X] T019 [US2] [FR-010, FR-011, FR-012, FR-013, FR-014, FR-015] `internal/ui/opsserver.go` and
  `internal/ui/templates/opsserver.html` per `contracts/page2-server.md`: `/ops/servers` (302 to the one server),
  `/ops/servers/{name}` (404 page naming the real one), the head (back link, name, state pill in plain words, "reboot
  needed" pill linking to Updates when known, spec line, no actions), host needs-you banners, the three numbers panel
  (and the second disk), the room line, Running here (AllocChip, CPU and memory now against reservation and limit, Up;
  problems first; the fold), the facts (pool, addresses, up since, the agent line, os, cpu with optional model, last
  seen), Recent on this server (`Activity` with `Server`, boots, `os_packages`), Nomad unreachable and node down;
  `/parts/ops/server` swapped on `metrics`, `copies`, `host`; styles in `internal/ui/style/input.css`, `tools/ui-css.sh
  build`. Tests in `internal/ui/opsserver_test.go`.
  - The page's live part is `/parts/ops/servers/{name}` (the body, with the head swapped by id), not
    `/parts/ops/server`, which is page 1's server panel. Ids: `srv-head`, `srv-body`, `srv-copy-<copy id>`.
    Before the sampler's first reading of the node (no name to redirect to) `/ops/servers` is a 200 page that says so.
- [X] T020 [US2] [SC-002, SC-005, SC-007] `cmd/caravana/console_ops_test.go`: step `console-ops-server` (redirect, the
  head with no actions, the lab's real versions and kernel, Running here with every copy, Recent with an
  unattended-upgrades run; in the lab Caravana runs on the Mac, not on the VM, so the apt history comes from the
  `hostos` fixture through `SetHostRoot`; the page renders in under 1 s); run it and record the result here.
  - Written in `cmd/caravana/console_ops_server_test.go`, listed after `console-ops-overview`.
    The lab's in-process Caravana gets `CARAVANA_HOST_ROOT` (an empty folder from `labHostRoot`, `gateTarget.HostRoot`);
    the step writes two apt runs dated a few minutes ago and the reboot marker into it (the sampler reads the folder on
    every tick) and removes them on its way out, so a fixture with fixed dates can't fall out of the newest eight entries.
    On a target with no `HostRoot` the apt and reboot checks are skipped.
  - 2026-10-30, lab: `console-ops-server` passes in 40 s (with `console-ops-overview`, 48 s). One test fix on the
    way: the 404 check looked for the sentence across the page's `<span>`s, which `pageText` splits.
- [X] T021 [US2] Page 2 review cycle as T015, against `pf-node.png`; record "Approved" in `contracts/page2-server.md`.
  - 2026-10-30: review of 2d70ccd (6 fixes, 1419bc1); fixes in 02beb0f (a copy whose health isn't decided is starting,
    in a rollout from the job's active deployment and outside one from its checks: the shared copy status, so the
    Running tab and page 1 too) and 3f243f8. Lab at 3f243f8: every TestConsoleLab step passes (the full run to
    console-domains-states, then console-monitoring, console-ops-overview 48 s, console-ops-server 40 s), but
    console-logs failed 2 of 3 runs alone (the part, a download, or the sentinel count read nothing; a different check
    each time) and passed the one run at 1419bc1. Its reads take about 1 s each.
  - 2026-10-31, console-logs alone on a cleared lab and an otherwise idle Mac, alternating: 3 of 3 pass at HEAD
    (b9b8a10) and 3 of 3 at 1419bc1. The two failing runs overlapped a fix agent's parallel `go test` of
    service, api and ui on the same Mac. So the step is sensitive to load on the machine running it (its page reads
    have a 5 s deadline), not to the copy-status change.
  - 2026-10-31: critique 27/40 (b9b8a10 rulings), one fix pass (6f8773e, 85785b4), last look (468f3e1) and its three
    fixes (4344394): Approved. Lab at 4344394: console-monitoring, console-ops-overview (61 s), console-ops-server (40 s).

**Checkpoint**: pages 1 and 2 work and are approved.

## Phase 5: User Story 3, page 3: Updates (P3)

**Goal**: what updates are waiting and how to apply them; the reboot case reaches the owner.

**Independent Test**: quickstart §4-5; `console-ops-updates` and the gate step `os-updates` pass; the designer approves
page 3.

- [x] T022 [P] [US3] [FR-018] `internal/hostos/state.go`: `ReadState(fsys)` (distro from `etc/os-release`; reboot file,
  its mtime and `.pkgs`; the newest installed kernel from the newest history run or `boot/vmlinuz-*`; counts and age
  from `var/lib/update-notifier/updates-available`, defensive, missing security = 0; `20auto-upgrades` and
  `50unattended-upgrades` with `//` comments and quotes) and `version.go` (`KernelNewer`, `ReleaseNewer` with
  `sort -V` ordering; non-release versions never compare); fixtures for a pending kernel, a `libc6` reboot, counts with
  and without the security line, auto-upgrades on and off, auto-reboot at 02:00, a Debian host, a non-Debian host;
  tests in `internal/hostos/state_test.go`, `version_test.go`.
- [x] T023 [P] [US3] [FR-019] `internal/hostos/securitystatus.go` (`ParseSecurityStatus`: names, whether a kernel is
  among them) and `internal/hostos/real.go` (`Root()`, `RunPro(ctx, home)` with `pro security-status --format json`,
  `HOME` set, 120 s); a test in `internal/hostos/imports_test.go` forbids `net/http`, `os/exec` and `os.Open` outside
  `real.go`; tests with a recorded JSON in `testdata/`.
- [x] T024 [US3] [FR-018, FR-022] The sampler reads `hostos.ReadState` each tick and keeps it in memory with the running
  kernel from the node; `host_marks.security_since` set while the security count is above zero; `ChangeUpdates`
  ("updates") when the state changes; tests in `internal/service/updates_test.go`.
- [x] T025 [US3] [FR-016, FR-017, FR-019, FR-021, FR-023] `internal/service/updates.go`: `Updates(ctx)` (the
  contracts/api.md shape), `SetReleaseCheck(ctx, enabled)`, `CheckRelease` and `CheckPackages` (one at a time, refused
  within 10 min with `ErrCheckTooSoon`, `ErrCheckDisabled`, `ErrCheckUnavailable`), `runReleaseCheck` (a minute after
  start, every 24 h with up to 1 h jitter; `If-None-Match`, 10 s, `User-Agent: caravana/<version>`, no request when off;
  body capped 16 KiB; pins from the release's `install.sh` and `traefik.nomad.hcl` when readable; `caravana.release`
  queued once per tag); setters `SetProRunner`, `SetReleaseURL`, `SetReleaseCheckGap`; started in `Service.Start`.
  Tests in `internal/service/updates_test.go` with a fake release server (ETag 304, a pre-release ignored, a dev build,
  off makes zero requests, notified once) and a fake `pro`.
- [x] T026 [US3] [FR-022] Attention kinds `os_reboot_required` (failed, both kernels or the package named) and
  `os_security_updates` (warning; fresh list under 48 h; with auto-updates on only after 24 h of `security_since`) in
  `internal/service/attention.go`; not judged when the host reading is stale; resolve by the existing rule; the
  notifier's words in `internal/service/notifier.go`; tests in `internal/service/attention_test.go`,
  `notifier_test.go`.
- [x] T027 [P] [US3] [FR-023] `internal/notify`: `EventCaravanaRelease = "caravana.release"`, `Event.Release{Current,
  Latest, URL}`, `Render` for webhook, Slack, Discord and ntfy ("caravana 0.16.0 is out. You run 0.15.0." with the
  link); tests in `internal/notify/render_test.go`.
- [ ] T028 [US3] [FR-025] API: `getUpdates`, `putUpdateSettings`, `checkUpdates` (202; 429 `check_too_soon`, 409
  `check_disabled`, 409 `check_unavailable`, 422 `invalid_field`) in `internal/api/updates.go`; the two attention kinds,
  the `caravana.release` event and its webhook body in `api/openapi.yaml`; scope walk and parity; tests in
  `internal/api/updates_test.go`.
- [ ] T029 [US3] [FR-003, FR-020, FR-021] `internal/ui/opsupdates.go` and `internal/ui/templates/opsupdates.html` per
  `contracts/page3-updates.md`: head and banners (■ before ◆, "How to apply" anchor), Available (caravana, Ubuntu
  updates with "Check package names" and the ◐ checking state, Reboot with both kernels), How to apply (only the steps
  that apply, reboot last with its consequence, command boxes), How this server updates (two read-only rows, the
  release-check form), Past updates (the last 10), the stale-list and non-Ubuntu sentences, GitHub unreachable;
  `POST /ops/updates/check` (`what=release|packages`, 303, the refusal drawn on the page) and `POST
  /ops/updates/settings` (303); `/parts/ops/updates` on `updates` and `host`; the Updates nav mark in the shell (■
  reboot, ◆ security, else the count); styles and `tools/ui-css.sh build`. Tests in `internal/ui/opsupdates_test.go`.
- [ ] T030 [US3] [SC-001, SC-003, SC-004, SC-007] `cmd/caravana/console_ops_test.go` step `console-ops-updates` (a fake
  host root with a pending kernel and 14/9 counts, a fake release server: banner, item, steps, release row, nav ■; the
  check turned off makes no request; a second package check within 10 minutes refused) and
  `cmd/caravana/gate_os_updates_test.go` gate step `os-updates` (`problem.opened` for the reboot reaches a test
  receiver within one sampling period, `problem.resolved` after the file is removed, one `caravana.release`); run both
  and record here.
- [ ] T031 [US3] [SC-001] `deploy/dev/install-test.sh`: after the install, `GET /v1/updates` answers `os.readable: true`
  with `distro.id: ubuntu` and `GET /v1/host` has `node.kernel`, proving the unit's sandbox allows the reads; shellcheck;
  run it on a throwaway VM if possible and record here (or note it as pending for the maintainer).
- [ ] T032 [US3] Page 3 review cycle as T015, against `pg-updates.png`; record "Approved" in
  `contracts/page3-updates.md`.

**Checkpoint**: all three pages work and are approved.

## Phase 6: Polish and cross-cutting

- [ ] T033 [P] [SC-006] A sentinel test across the feature (`internal/service/operator_sentinel_test.go`): a secret
  value and a command's output never appear in `Activity`, `Updates`, `Host`, their API answers, the three pages, the
  server log or any notification.
- [ ] T034 [P] `CHANGELOG.md` `## Unreleased` (Added: the Operator view, its three pages, API 0.29.0, the two needs-you
  kinds, the release notification; Upgrade: nothing to rerun, the release check is on by default and can be turned off on
  Updates); `docs/plan.md` (the single-host Operator pages move to 2.6; Updates takes over the "update available"
  notice; the screen table's rows for `NowScreen`, `NodeScreen`, `UpdatesScreen`); `CLAUDE.md` paragraph for 027.
- [ ] T034a One source for the needs-you wording: page 1's review (f0fd007) copied the needs-you sentences into
  `internal/service/sentenceparts.go` (`problemParts`) because `service` can't import `ui`, so they now live there and in
  `internal/ui/attention.go`. Make the service's parts the one source and have `ui/attention.go` draw them (or add a test
  that both say the same for every attention kind), so the feed and the needs-you list can't drift.
- [ ] T035 Full checks: build, vet, staticcheck, gofmt, both cross-compiles, `go mod tidy -diff`, `tools/ui-css.sh
  check`, `tools/ui-vendor.sh check`, shellcheck, `go test -race -timeout 30m ./...`; record the result here.

## Dependencies & Execution Order

- Phase 1 → Phase 2 (T002-T008) → US1 (T009-T015) → US2 (T016-T021) → US3 (T022-T032) → Polish (T033-T035).
- Within Phase 2: T002 → T003, T004; T005 → T006 → T007; T008 independent of the store work.
- US2 needs US1's shell, activity and API; US3 needs US2's `hostos` package start (T016) and the host root setter (T017).
- Each page's review cycle (T015, T021, T032) gates the next page's start in the designer's method; the next page's
  backend tasks may start before the review ends.

## Parallel opportunities

- Phase 2: T005 (deploy) beside T002-T004 (store) and T008 (ui shell).
- US1: T013 (styles) beside T011-T012 once the markup settles.
- US2: T016 (`hostos` history, pure) beside T018 (host copies).
- US3: T022, T023 (pure `hostos`) and T027 (`notify`) beside each other; then T024-T026, T028-T029.

## Implementation strategy

MVP is US1 (page 1): the toggle and Overview give the Operator view its frame and its home. Each page then lands whole
(backend, API, page, lab step, review) before the next, so the feature can stop after any page and still be usable.
Coding goes to Sonnet agents with self-contained briefs; the main session reviews every diff and runs the tests before
a task is ticked.
