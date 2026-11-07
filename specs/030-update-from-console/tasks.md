# Tasks: Update Caravana from the console

Read [spec.md](spec.md), [plan.md](plan.md) and the three contracts first:
[updater.md](contracts/updater.md), [api.md](contracts/api.md), [page-updates.md](contracts/page-updates.md).
Phase 1 (host) and Phase 2 (Go) are independent; Phase 3 needs Phase 2. Each Go phase ends with
`go build ./... && go vet ./... && go test ./<packages touched>` green; the host phase with shellcheck.

## Phase 1: The host side (`deploy/`, `tools/`)

- [x] T001 `deploy/install/caravana-update.sh`: the updater exactly as `contracts/updater.md` › "The
  updater" (steps 1–10), bash, `set -euo pipefail`, the body one `main` function, last line
  `main "$@"; exit $?`. Helpers: `write_status KEY=… …` (to `status.tmp`, then `mv`), `reason_of LOG`
  (last `caravana install: ` line, prefix dropped, `LC_ALL=C tr -cd '[:print:]'`, cut to 300 bytes),
  `newer` as the installer's. Journal lines through `logger -t caravana-update` or plain stderr; never the
  request's bytes.
- [x] T002 `deploy/install/caravana-update.service` and `caravana-update.timer`, as in the contract, with a
  comment header like `caravana.service`'s.
- [x] T003 `install.sh`: `install_updater` after `configure_caravana` (and before `start_version`, so the
  timer exists when the new Caravana starts): `install -d -m 0755 /var/lib/caravana-update`, the marker
  `updater` (`1`), `install -d -m 0700 /var/lib/caravana-update/logs`, the script to
  `/usr/local/libexec/caravana-update` (0755), both units to `/etc/systemd/system/` (0644), all with
  `write_if_changed` (name `updater`), `daemon-reload`, `systemctl enable --now caravana-update.timer`.
  `uninstall_caravana`: `disable --now` the timer, remove the script, both units and
  `/var/lib/caravana-update`, `daemon-reload`. The header comment's list of what runs where gains them.
- [x] T004 `tools/release.sh`: `host_files` gains the three files; the header comment says so.
- [x] T005 `deploy/install/README.md`: "What runs where" gains the updater's rows; a section "Updating"
  (from the console's Updates page: the request, the root timer, the checksums, the rollback; by hand:
  rerun the newer installer; the log at `/var/lib/caravana-update/logs/<id>.log`).
- [x] T006 `deploy/dev/install-test.sh` (quickstart below, steps 2–6): after the existing broken-release
  rollback and before "an older is refused". Uses `api`, `vm`, `release`, `broken`. Releases `u1`, `u2`,
  `ubad` with versions `0.0.$base`, `0.0.$((base + 1))`, `0.0.$((base + 2))` (real release versions, so
  Caravana doesn't call them development builds), served from inside the VM by
  `python3 -m http.server 8099 --bind 127.0.0.1 --directory <dir>` as `download/v<version>/<files>` and a
  fake GitHub answer `repos/x/releases/latest` (a JSON file: `tag_name`, `html_url`, `body`,
  `published_at`, `draft`, `prerelease`). Point Caravana at it with a systemd drop-in
  (`/etc/systemd/system/caravana.service.d/test.conf`, `Environment=CARAVANA_RELEASE_URL=…`) and the
  updater with `/etc/caravana/update.env` (root, 0644). Check the uninstall removes the updater's files.
- [x] T007 shellcheck the four scripts (`docker run --rm -v "$PWD:/mnt" koalaman/shellcheck …` on this Mac).

## Phase 2: Caravana (`internal/`)

- [x] T010 `internal/config`: `CARAVANA_RELEASE_URL` / `-release-url` (empty keeps GitHub's; https, or http
  to a loopback host only, as `CARAVANA_TRAEFIK_METRICS_URL` checks) and `CARAVANA_UPDATER_DIR` /
  `-updater-dir` (default `/var/lib/caravana-update`, absolute), with tests. `cmd/caravana/main.go`:
  `svc.SetReleaseURL(cfg.ReleaseURL)`, `svc.SetUpdaterDir(cfg.UpdaterDir)`; the request path is
  `<data dir>/update-request` (`svc.SetUpdateRequestPath`).
- [x] T011 Migration `0023_update_runs.sql`: `update_runs` (id TEXT PK, version, from_version, trigger CHECK
  console|api|automatic, token_name, requested_at, started_at, finished_at, state CHECK the seven, reason,
  log; times as the other tables', nine fraction digits), index by requested_at; `release_check` gains
  `auto_update INTEGER NOT NULL DEFAULT 0` and `auto_update_hour INTEGER NOT NULL DEFAULT 3 CHECK (0..23)`.
  Comment header in the house style.
- [x] T012 `internal/store/updateruns.go`: `UpdateRun` struct, `InsertUpdateRun`, `OpenUpdateRun` (requested
  or running), `UpdateRuns(limit)`, `LatestFinishedUpdateRun`, `UpdateRunExists(version)`,
  `SetUpdateRunState(id, state, startedAt, finishedAt, reason, log)`, `DeleteUpdateRun` (for a failed file
  write); `ReleaseCheck` gains `AutoUpdate`, `AutoUpdateHour`; `SetAutoUpdate(ctx, enabled, hour)`. Tests.
- [x] T013 `internal/service/updaterun.go`: `SetUpdaterDir`, `SetUpdateRequestPath`; `UpdaterInstalled()`
  (the marker is a regular file); `RequestUpdate(ctx, version, trigger, tokenName) (store.UpdateRun, error)`
  with the refusals of `api.md` as errors: `ErrUpdateUnavailable` (`*UpdateUnavailableError{Reason}`),
  `ErrUpdateRunning`, `ErrUpdateNotOffered` (`*UpdateNotOfferedError{Reason}`), an `*app.FieldError` on
  `version` for a malformed one; the ID is 16 random bytes in hex; the row first, then the file
  (`update-request.tmp`, 0600, then rename); a failed write deletes the row. One mutex makes the check and
  the insert one step. Publishes `ChangeUpdates`.
- [x] T014 Reading the status: `readUpdaterStatus(dir) (updaterStatus, bool)` — at most 4 KiB, `KEY=value`
  lines, unknown keys ignored, a malformed time leaves it zero; pure parse function with table tests
  (good, partial, garbage, oversized, wrong ID).
- [x] T015 The watcher, started in `Service.Start` beside the other loops: every 5 s while an open run
  exists (a cheap store read otherwise, or wake on `RequestUpdate`), and once at start: running → set
  `running` and `started_at`; final → set the state, times, reason and `log` path
  (`<dir>/logs/<id>.log`, not for refused), remove the request file, publish; no status for the ID after
  5 minutes → `lost` "The server's updater didn't start it within 5 minutes. Is `caravana-update.timer`
  running? (`systemctl status caravana-update.timer`)"; `running` for 45 minutes → `lost` "The updater
  stopped without saying how it went." The clock is `s.clock` so tests drive it. Tests with a temp
  directory for every transition, including a status written by "another process" between ticks and a
  restart in the middle (a new Service over the same store and dirs).
- [x] T016 Automatic updates in the same loop, checked once a minute: `autoUpdateDue(now)` per FR-008
  (pure, table-tested: on/off, hour, patch vs minor, suffix, check off, updater missing, open run, a
  version that had a run, a dev build), then `RequestUpdate(…, "automatic", "")`. `SetAutoUpdate(ctx,
  enabled, hour)` stores and publishes.
- [x] T017 `UpdatesView` gains `Updater{Installed bool; Unavailable string}`, `Run *store.UpdateRun` (open,
  else finished within 24 h), `Auto{Enabled bool; Hour int; Next time.Time; Blocked string}`; History
  merges finished runs (`Kind "update"`, the contract's text and detail) and drops a `version` event
  explained by an `updated` run within an hour (`updateHistory` stays pure, gets the runs).
- [x] T018 Needs-you: `AttentionCaravanaUpdateFailed = "caravana_update_failed"` in `attention.go`, derived
  on every read (new file `attention_update.go`), in `AttentionKinds`, its words in `ProblemParts`
  (`sentenceparts.go`, versions mono), action Open updates → `/ops/updates`; the notifier's diff needs no
  change (check the key shape). `TestEveryKindSaysTheSameWordsEverywhere` passes with it. Activity
  (`activity.go`): one line per finished run ("caravana updated `0.16.0` → `0.17.0` from the console" /
  "caravana `0.17.0` was rolled back: …" / …) and the version event it explains is skipped there too.
- [x] T019 API: `requestUpdate` (POST `/v1/updates/runs`; trigger `console` for a session, `api` with the
  token's name for a token, as deployments record them), `listUpdateRuns`, `getUpdates` and
  `putUpdateSettings` additions, the three codes in `updatesError`; `api/openapi.yaml` 0.33.0 (paths,
  `UpdateRun`, the additions, the kind in the attention enum and its description, the codes); the route
  table in the scope walk test lists both as denied to deploy tokens; `TestConsoleParity` lists the console
  routes of Phase 3. Tests: 202, each refusal's status and code, a deploy token's 403, the JSON shape.
- [x] T020 Sentinel: no test token, the request's bytes or a log line reach a page, answer or log because
  of this feature (extend `operator_sentinel_test.go` in `api` with a status whose REASON holds a
  sentinel: it is shown — it's the updater's sentence — but the log file's sentinel never is).

## Phase 3: The console (`internal/ui`)

- [x] T021 Confirmation page: `GET /ops/updates/caravana?version=` and `POST /ops/updates/caravana`
  (`opsupdates.go`, `templates/opsupdatesconfirm.html`), per contract §2; refusals redraw it with 409.
- [x] T022 The caravana row's action and its two alternatives (§1); How to apply's new words (§4).
- [x] T023 The run panel (§3): `up-run` template, `/parts/ops/updates/run` (answers the panel, and when
  finished the main region, Past updates and the nav mark by id), the 5 s poll only while open,
  `pageData` gains a `Refresh int` drawn by `base.html` as the noscript meta; the page doesn't draw the
  `caravana_update_failed` banner.
- [x] T024 Automatic caravana updates (§5): the form's two new fields, the settings post stores them (the
  existing `?saved=1`), the line under it in each state.
- [x] T025 Past updates' run rows (§6).
- [x] T026 Styles in `internal/ui/style/input.css` (tokens only, `theme.css` for any new colour — none
  expected), `tools/ui-css.sh build`; tests in `opsupdates_test.go`: every run state's words, the poll and
  the noscript meta only while open, the confirmation page with and without pins, each refusal, the
  automatic row's states, no banner for the kind on Updates, `TestNoShipWords` still green.

## Phase 4: Docs and verification

- [x] T030 `DESIGN.md` (the run panel as a banner row; the confirmation page joins Stop's shape),
  `docs/plan.md` (step 2 done in 030, out of Phase 3; Phase 3 keeps cluster-wide updates),
  `CHANGELOG.md` Unreleased (Added; Upgrade: rerun the installer once to get the updater),
  `CLAUDE.md` (a Feature 030 paragraph), `docs/mcp.md` unchanged (the MCP server doesn't offer it; say so
  only if the file lists what isn't offered).
- [x] T031 Full local CI: `go build`, `go vet`, `staticcheck`, `go test -race ./...`, `gofmt -l`, the
  CGO-free cross-compile, `tools/ui-css.sh check`, `tools/ui-vendor.sh check`, `go mod tidy -diff`,
  shellcheck.
- [x] T032 `deploy/dev/install-test.sh` on a throwaway VM, green (SC-001).
- [x] T033 Designer review of the page (the contract's Review list), its rulings recorded under the
  contract, then a last look.

## Quickstart: the install-test steps (T006)

1. (existing) install v1, upgrade to v2, roll back the broken one.
2. Build `u1`, `u2` (good) and `ubad` (broken, as `broken` does) and serve them with the fake latest
   release naming `u2`. Install `u1` by hand (`install u1`). Check `/var/lib/caravana-update/updater`, the
   timer is active, and `GET /v1/updates` says `caravana.updater.installed: true`.
3. A crafted request runs nothing: as root write `/var/lib/caravana/update-request` with
   `ID=<32 hex>` and `VERSION=0.0.1;id` and wait 75 s: `status` says `refused` with "isn't a release
   version" and `current` is still `u1`; then a symlinked request to `/etc/shadow`: no new status, nothing
   ran (journal says "isn't a plain file"). Remove the files.
4. `POST /v1/updates/check {"what":"release"}`, then `POST /v1/updates/runs {"version":"<u2>"}` → 202,
   and the same post again at once → 409 `update_running`. Within 3 minutes `/v1/health` answers `u2` and `GET /v1/updates` has `run.state: updated`; the request
   file is gone; whoami answered throughout.
5. Serve `ubad` as the latest; check; request it; within 4 minutes the run is `rolled_back` with a reason,
   `/v1/health` answers `u2`, `GET /v1/attention` has `caravana_update_failed`.
