# Tasks: Logs (024)

**Input**: `specs/024-logs/` (spec.md, plan.md, research.md, data-model.md, contracts/api.md,
contracts/logs-screen.md with the designer's rulings, quickstart.md)

**Tests**: required (constitution VIII): table tests over every example line of research R2 and
R3 beside the pure package, a fake file system in `deploytest` for the service, the API contract
test against `api/openapi.yaml`, the parity and scope-walk tests, sentinel searches (a line a copy
prints is found only in the Logs page, the download and that copy's API answers), a
`TestConsoleLab` step and a gate step.

**Order**: the lab spike first (its answers fix the file walk and the line id), then the pure
package, the Nomad reads and the store query, the service, the API, the console on real data,
the lab steps, docs, and the review cycle. A Sonnet agent builds each phase; Opus reviews before
the next.

## Phase 0: The lab spike (no story label)

- [x] T001 Run research R8's checks against the lab (no Caravana change: a hand-registered busybox
  job with `LogConfig {MaxFiles 3, MaxFileSizeMB 1}` writing past rotation, plus the lab's running
  apps): (1) `logs?origin=end&offset=<more than the newest file>` continuing into `<n-1>` and the
  frames' `File` changing mid-read; `readat` past a file's size, and on a purged file; (2) file
  names after rotation and after a Restart from the console (append to the newest, no new `.0`);
  (3) a replaced version's copy still answering `ls`/`logs`, and roughly when it stops being
  listed; (4) 20 streams × 1 MiB through `readat` in parallel from the Mac with a semaphore of 2,
  the time and Caravana's RSS; (5) a stream with no timed lines (whoami) for the `around=<time>`
  binary search: decide whether it ships in 024 or waits; (6) a container with `TZ=Europe/Madrid`
  writing Go `log` times: the bounds check marks them estimated; (7) the scoped policy's token
  (`deploy/install/caravana-policy.hcl`) on `ls` and `readat`; (8) a job printing SGR, OSC 8,
  OSC 52, `\r` progress and a NUL: what `logs` and `readat` hand back byte for byte. Record each as
  "lab, observed" under research R8 and adjust plan.md where one differs. Clean every lab object up.

## Phase 1: Foundation (no story label)

- [x] T002 [P] New pure package `internal/logline` (`ansi.go`, `time.go`, `level.go`, `fold.go`,
  `line.go` with `Line`, `LineID`, `ParseLineID`/`String`, `Bounds`, `Span`, `Level`): `StripANSI`,
  `TimeOf`, `LevelOf`, `Fold` per plan.md's Design, with tables holding every example of research
  R2 (the shapes) and R3 (the examples table and the words table), the lab's six lines first, the
  bounds check, the no-zone-is-UTC rule, the fast path, the mid-line `\r`, an unterminated
  escape, pino numbers, Serilog's absent `@l`, continuation inheritance; a test that no import of
  `net/http`, `nomad` or `service` exists in the package.
- [x] T003 [P] `internal/deploy/logfiles.go` + `deploytest`: `LogFiles(ctx, copy, task)` from
  `AllocFS().List(alloc, "alloc/logs")` (name, size, `ModTime`, the stream and index parsed from
  `<task>.<stream>.<n>`), `ReadLogAt(ctx, copy, path, offset, limit)` from `AllocFS().ReadAt`,
  `LogCopies(ctx, jobIDs)` keeping terminal copies of every version (`Gone`), errors mapped as
  `Logs` maps them (`ErrNomadUnavailable`, a 404 as `ErrLogRotated`); the `deploytest` fake file
  system (files of lines per copy and stream, `ls`/`readat`, append, purge) as the seam's second
  implementation; tests for the walk's inputs.
- [x] T004 [P] `internal/store/deployments.go` + test: `ListDeploymentsIn(ctx, project,
  environment, from, to)` over `created_at` and the terminal `updated_at` of the place's apps
  (join on `apps`); no migration.

## Phase 2: Service (US1 the stream, US2 narrow, US3 deploys and live, US4 download)

- [x] T005 [US1] `internal/service/logsread.go` + tests on the fake fs: `LogQuery`, `LogRead`,
  `ReadLogs` per plan.md: the place and app checks, `LogCopies`, the service and copy filters
  before reading, the backwards walk per stream in 64 KiB chunks across files under the budgets
  (1 MiB per stream, 16 MiB per page split evenly, the semaphore of 2), per line strip → time →
  level → continuation, the estimated time rule (the older timed line of the stream, else the
  copy's start or the previous file's `ModTime`, never now), `Fold` per stream, the level and `q`
  filters (case-insensitive over text + `<app>/<service>`, ≤ 200 bytes), the merge newest first
  with per-stream non-decreasing times, `Lines` rows, `Coverage`, `Dropped`, the copies with
  counts and `Gone`. Tests: a sample file per research example, the budget cut, a rotation
  crossed, an untimed stream, two copies' equal lines not folded, a filter applied before folding.
- [x] T006 [US1] `internal/service/logshist.go` + tests: the 30 bins from the read (`Count`,
  `Errors`, `Estimated`, `Read` false before the coverage), `BucketSeconds`; the first-seen note's
  count (equal texts in what was read).
- [x] T007 [US3] `internal/service/logsdeploys.go` + tests: `DeployRow`s from
  `ListDeploymentsIn` (`started` at `CreatedAt`; `live`/`failed`/`reverted`/`superseded` at the
  terminal `UpdatedAt`, with `RevertedTo` and the who-words through a shared `deployWho`-like
  helper moved to the service or duplicated minimally), merged into the read by time, never
  filtered by level or `q`, kept by `Service` only for that app.
- [x] T008 [US2] `internal/service/logsaround.go` + tests: `LogsAround(ctx, copy, at, n)`: `readat`
  ±16 KiB around the offset crossing a file edge, both streams of the copy over the same span,
  every level, the anchor's id, `ErrLogRotated` on a purged file, `ErrNotFound` on a gone copy.
- [x] T009 [US4] `internal/service/logsdownload.go` + tests: `DownloadLogs(ctx, q, w)`: the k-way
  merge oldest first streamed, `# newest last` then the header, tab-separated fields, `~` on an
  estimated time, folded rows written out, `# deploy …` rows, the 8/64 MiB budgets with `# cut:`,
  one at a time (`ErrDownloadBusy`), control bytes stripped; the file name helper.
- [ ] T010 [US3] `internal/service/logs.go` + `live.go` + tests: `WatchLogs(place, on)` and the
  `logs` notice from `followLogs` per stream only while a Logs page with Live is open, debounced
  2 s, no text in the notice; today's per-app `Logs` lines gain `ID`, `Level`, `TimeSource` and
  the stripped `Text` (and `Time` on tail lines read from the line); the sentinel test: a line's
  text never reaches a store row, the server log, a notice or an event.

## Phase 3: API 0.26.0 (US4)

- [ ] T011 [US4] `api/openapi.yaml` 0.26.0 + `internal/api/logs.go` + tests: `listLogs`,
  `getLogsAround`, `downloadLogs` per contracts/api.md (the parameters' rules and codes, `no-store`,
  the attachment header, `levelOf` and `stripANSI` written out in `listLogs`' description),
  `LogLine`'s new properties on `getLogs` (unchanged order), the contract test over every new
  route, `TestScopedTokenWalksEveryRoute` with the three routes denied, parity entries for
  `/logs`, `/parts/logs`, `/logs/download` and the app's tab.

## Phase 4: Console (US1, US2, US3, US4, US5)

- [x] T012 [US1] `internal/ui/style/input.css` "Page 7 (024)" + `DESIGN.md`: the head, the
  segmented bars, the filter row, the rail (hues by `hash(app/service) mod 6` over the handoff's
  six: the one identity color), the histogram SVG classes (`cv-hbar`, `cv-hbar-err`,
  `cv-hbar-unread` hatched, `cv-hmark`), the stream rows (grid, error tint, fresh tint with the
  One Tick Rule and reduced motion, the `×N` chip, the estimated time in fg-4 with `~`, the
  ordinary time in fg-3), the open row's strip, the deploy row, the footer; narrow rules (the rail
  as a chip row, rows on two lines, the histogram's labels); `tools/ui-css.sh build`.
- [ ] T013 [US1] `internal/ui/logs.go` (rewritten) + `templates/logs.html` + tests: `GET /logs`
  (the query → `LogQuery`, the place choosers only with more than one, the rail from the copies
  grouped by app, the histogram SVG with `title`s and the hidden table, the stream with folded
  rows and deploy rows, "Older lines were dropped." and "Show older" (`before=` from the
  coverage), the empty sentences, the retention footer and the Download link, the no-script
  snapshot sentence, Nomad unreachable in the stream with empty bars), `GET /parts/logs` on the
  `logs` notice swapping only the stream and the histogram (the filters, the rail and the open
  row's strip outside the region; an open row keeps its id), `WatchLogs` on the page's stream
  with `?live=1`, the sidebar's Logs item (`scroll-text`) after Domains, the console's `TestNoShipWords`
  and the parity test green.
- [ ] T014 [US2] `internal/ui/logs.go` + template + tests: the open row (`?open=<id>#line-…`: the
  full text, the stream named, Lines around this, Only `<service>`, the row's link as selectable
  text, Raw for JSON, the first-seen note), the around view (`?copy&at`: the notice sentence,
  the highlight, "Back to the stream", the rotated-away and gone sentences), the level chips as
  links with counts, the search with "N lines", every filter as one GET form.
- [ ] T015 [US3] `internal/ui/logs.go` + `console.js` + tests: Live/Paused (`aria-pressed`, the
  post that flips `?live`), the fresh tint, "paused · N new lines since" with "Show" (script only),
  the deploy rows as links to the entry with page 4's words, the markers' `title`s.
- [ ] T016 [US4] `internal/ui/logs.go` + tests: `GET /logs/download` (the service's download,
  the attachment header, `no-store`), the footer's link carrying the filter.
- [ ] T017 [US5] `internal/ui/app.go` + `logs.html` + tests: the app's Logs tab as the same
  template filtered to the app (its services in the rail, its deploys, "All logs" to
  `/logs?project&environment`); the old Copy and Stream selects gone; `console-reads` and the
  existing Logs-tab tests updated.

## Phase 5: Lab tests

- [ ] T018 `cmd/caravana/console_logs_test.go` + `console_lab_test.go`: the step `console-logs`
  per quickstart §6 (the two sample apps through the forms, every sample line's level and time,
  folding, search, the chips' counts, only-service and a copy, around with the highlight, Raw, a
  deploy during the step as a row and marker, Show older, Download's first lines and order, the
  app's tab, the sidebar item, a gone copy, no escape byte in any page or file).
- [ ] T019 `cmd/caravana/gate_logs_test.go` + `gate_test.go`: the gate step `logs` per quickstart
  §6 (`listLogs`, `getLogsAround`, `downloadLogs` with the gate's token, a deploy token's 403 on
  each, a wrong `at` 422, a purged file 404 `log_rotated`, no `\x1b`/`\x07`/NUL in any answer).

## Phase 6: Docs

- [ ] T020 `CHANGELOG.md` Unreleased (Added: the Logs page and what it reads; Changed: the app's
  Logs tab, `getLogs`' stripped text and new fields; Upgrade: rerun nothing, no policy change),
  `docs/plan.md` row 024 ("Built (specs/024-logs)…" with what changed from the row's text: read
  live with a budget, no store, levels and times by the pure rules, the line id, Download oldest
  first), `CLAUDE.md` "Feature 024" paragraph in 023's style.

## Phase 7: Review cycle

- [ ] T021 Checks (the CI list, `go test -race ./...`), `TestConsoleLab` (every step) and the gate
  step `logs` in the lab; stage the review instances (the two sample apps, a deploy during the
  review, a gone copy), tell the designer; their review; the impeccable critique (no detector);
  the rulings into the contract; the fixes; the last look.

## Dependencies

T001 → T002, T003, T004 (parallel) → T005 → T006, T007, T008, T009, T010 (parallel) → T011 →
T012 → T013 → T014, T015, T016, T017 (parallel) → T018, T019 (parallel) → T020 → T021.

## Parallel work

- Phase 1's three tasks touch disjoint packages.
- Phase 2's T006–T010 each add a file beside T005's reader.
- Phase 4's T014–T017 share `logs.go` and `logs.html`: one agent in order, or two with T017 alone.
- T018 and T019 are independent files; one lab run at a time.
