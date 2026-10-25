# Implementation Plan: Logs

**Branch**: `024-logs` (stacked on `023-domains`) | **Date**: 2026-10-24 |
**Spec**: [spec.md](./spec.md) | **Page contract**: [contracts/logs-screen.md](./contracts/logs-screen.md) |
**API contract**: [contracts/api.md](./contracts/api.md) | **Research**: [research.md](./research.md)

## Summary

One page, `/logs`, with the output of every copy of every service of one project › environment,
newest first, read live from Nomad's fs API on every view (`ls` + `readat` backwards from the
newest file, with a byte budget) and never stored. Three pure functions read each line:
`stripANSI`, `timeOf` (fixed positions, a bounds check, estimated times from the older timed line
of the same stream) and `levelOf` (JSON keys, the first four words, klog/Rails/.NET prefixes,
continuation inherits, unmatched is info). Repeats fold per stream; a line is addressed by
`<copy>.<stream>.<file>.<offset>` for "lines around this"; the histogram (30 bins, unread bins
hatched) and the deploy rows and markers come from the same read and the deployment records.
Download is the filter as text, oldest first. API 0.26.0 adds `listLogs`, `getLogsAround` and
`downloadLogs`; `getLogs` gains the level and the stripped text. No migration, no new module,
no policy change.

## Technical Context

Go 1.27. **No new module.** Storage: no migration (a new read-only store query over
`deployments` joined on `apps` by project and environment). Nomad: `AllocFS().List` and
`ReadAt` (the policy's `read-fs`, present since 016) beside today's `Logs` (`read-logs`); a copy
listing that keeps terminal copies of older versions. Memory: a semaphore of 2 environment reads
at once per Caravana, 16 MiB per read at most. New settings: none. Testing: pure table tests
over the research's example lines, a `deploytest` fake file system answering `ls`/`readat` for
files of lines, service, API (contract, parity, the scope walk), UI, a `TestConsoleLab` step
`console-logs` and a gate step `logs`; the R8 lab checks as the first task, recorded in
`research.md` as "lab, observed".

## Constitution Check

| Principle | Assessment |
| --- | --- |
| I. Small | PASS. No module, no table, no goroutine while nobody looks; a read is bounded (16 MiB, 2 at once) so two open tabs can't exhaust a 2 GB VM. A ring buffer was rejected (research R1). |
| II. One Model | PASS. Nothing touches `app.App` or `Translate`; the page reads what Nomad has for the stored apps of a place. |
| III. Safe Deploys | PASS. Read-only; deploy rows come from the records as they are. |
| IV. Fail Loudly | PASS. "Older lines were dropped.", "read from <time>" on the histogram, unread bins hatched never zero, "(gone)" for a copy, "That part of the copy's output has been rotated away" for a purged file; a time that fails the bounds check is marked estimated, never placed wrong silently. |
| V. API-First | PASS. Three operations and the `getLogs` additions in openapi 0.26.0; the console calls the same `service` methods; `levelOf` and `stripANSI` documented so clients match; deploy tokens denied by default deny with the scope walk. |
| VI. Nomad-Native | PASS. Only the official client (`AllocFS().List/ReadAt/Logs`); nothing new reaches Nomad from outside `deploy`; no Nomad-specific code leaves `deploy` (the service sees files of bytes and a line id). |
| VII. Secure | PASS. No line is written to the store, the server log, a notice or an event (the `logs` notice carries no text); Download and the API answers are `no-store`; control bytes are stripped so a downloaded file can't drive a terminal; reads need a session or a full token. |
| VIII. Plain Go | PASS. Pure functions with tables for every research example; the fs seam is a small interface with the test fake as its second implementation; the k-way merge and the backwards file walk are plain loops. |

## Decisions carried (and where the research and the contract differed)

- **The source** (research R1): live reads, `ls` for the files and sizes, `readat` backwards in
  64 KiB chunks from the newest file crossing into `n-1`, until the oldest timed line read is
  before `from`, the stream's budget (1 MiB; 16 MiB per page split evenly beyond 16 streams) is
  spent, a **deadline** (a few seconds per page; spike R8.4: 20 MiB took 0.3 s on an idle node but a
  1 MiB `readat` took up to 6.9 s beside a CPU-hungry app) passes, or no file is left. The service and
  copy filters cut the copy list before reading; the coverage says when the deadline, not the budget,
  ended a stream, in words ("not all read in time").
  `Logs` with `follow=true` (today's) is used only for Live. A read returns its coverage: the
  oldest time reached per stream and whether the budget or the end stopped it.
- **Copies** (R1): a new `deploy.LogCopies(ctx, jobIDs)` lists every allocation Nomad still has
  for the jobs, running or terminal, any version, marked `Gone` when terminal; the rail counts
  from it. Today's `listed` filter stays for every other page.
- **Lines** (R2, R3, R5, and the designer's rulings): one new pure package `internal/logline`:
  `StripANSI(string) string`, `TimeOf(line string, bounds Bounds) (t time.Time, span Span, ok bool)`,
  `LevelOf(text string, prev Level) (Level, msg string, raw bool)`, `Fold([]Line) []Line`. Order per
  line: strip, time (span taken off), level (JSON first), continuation. A line with no time takes
  the older timed line's time in the same stream (`estimated`); a stream's first untimed lines take
  the copy's start or the previous file's `ModTime`; never the read's time. A time more than a
  minute before the copy's start or after the file's `ModTime` isn't trusted (estimated). A **per-file
  skew check** (spike R8.6) goes further: the last timed line of a file against that file's `ModTime`
  (the last line was written at about the file's last write); more than a minute either way and every
  line of that file is `estimated`. It catches a west-of-UTC zone, which the bounds alone miss in an
  old copy. No zone means UTC. An unmatched line is `info` on both streams (ruling 1).
- **Folding** (R4): per stream, before merging, on the text without its time span; `repeat` = N-1;
  the row's time and id are the newest line's; a filter applies before folding; never across
  copies or streams.
- **The line id** (R4, ruling 2): `<copy>.<stream>.<file index>.<byte offset>`, the offset of the
  line's first byte in `alloc/logs/<task>.<stream>.<n>`; `?copy=<id>&at=<stream>.<n>.<offset>` in
  the console, `at=` on the API; a purged file answers in words. `around=<time>` is a second form
  for a time from elsewhere, by a binary search of the stream's files: **ships in 024** (spike R8.5: 12
  probes of 4 KiB for a 10 MiB file, about 30 ms idle; the file is chosen from `ls`'s `ModTime`s, no probe;
  a stream with no timed line in its first probe falls back to offsets). Ids and `at=` offsets come only
  from Caravana's own `readat` offsets, never from a framed read's `Offset` (spike R8.1: for a file's short
  last frame it is start + 65,536, past the file's size); a Live line has `Time` = arrival and no id.
- **Search** (R4): case-insensitive substring over the stripped text plus `<app>/<service>`,
  ≤ 200 bytes, server-side over what was read; no regexp.
- **Range and older** (R4): presets as `from`/`to`; `before=<time>` restarts the walk from that
  time (the binary search, or the previous read's coverage edge, which the page carries).
- **Download** (R4, ruling 3): oldest first by a k-way merge streamed as it is read; the first
  line `# newest last`, then `# caravana logs <project>/<environment> <from> to <to>, read from
  <coverage> (<filters>)`; tab-separated `<RFC 3339 UTC time> <app>/<service> <copy> <stream>
  <level> <text>`, an estimated time with a trailing `~`; folded rows written out; deploy rows as
  `# deploy …`; 8 MiB per stream, 64 MiB per download, one at a time; `no-store`; control bytes
  stripped; a last line `# cut: …` when the budget bit. Name
  `caravana-logs-<project>-<environment>-<from>-<to>.txt` with times as `20261025T003321Z`.
- **The histogram** (R6): 30 bins of `(to-from)/30` from the read (unfolded lines after filters,
  estimated included), `count` and `errors`, `read` false for bins wholly before the coverage;
  drawn as inline SVG with `rect`/`line` attributes and classes (`consoleCSP` forbids `style`),
  a `<title>` per bin, a visually hidden table for screen readers; recorded in DESIGN.md as the
  console's one SVG chart.
- **Deploy rows and markers** (R6): `store.ListDeploymentsIn(ctx, project, environment, from, to)`
  over `created_at` and, for a terminal status, `updated_at`; two rows per deployment in range
  ("started rolling out" at `CreatedAt`; "is live" / "failed its health checks · rolled back to
  vN" / "was replaced by a newer deploy" at the terminal `UpdatedAt`) with page 4's who-words
  (`deployWho`), merged by time, never filtered by level or search, kept by "only this service"
  only for that app. No migration.
- **Estimated times** (ruling 3): an ordinary time in fg-3, an estimated one in fg-4 with a leading
  `~` and `title` "estimated: this line carried no time; taken from the line before it".
- **Live** (R7): the console's `logs` notice from following the environment's streams while a
  Logs page with Live on is open (no text in the notice), debounced to one refetch per 2 s; the
  API gets no environment follow in 024 (`getLogs?follow=true` stays the per-app live read).
- **Not in 024**: regexp search, rendering colors, a store or counts buffer, an environment
  follow on the API, an MCP tool (optional later), Operator sources.

## Design

### `internal/logline` (new, pure)
- `StripANSI`: CSI, OSC (BEL or ST), DCS/SOS/PM/APC, two-byte ESC, the C1 forms as UTF-8, C0 but
  tab, `0x7F`; the text after the last mid-line `\r`; an unterminated sequence dropped to the end;
  a fast path for lines with no control byte.
- `TimeOf`: the shapes of research R2 at fixed positions (start; after a short prefix ≤ 8 bytes;
  inside a leading `[`; the CLF bracket after three words; `time=`/`ts=` in the first six logfmt
  pairs; JSON `time`/`ts`/`timestamp`/`@timestamp`/`@t`/`t`, string or number by magnitude); no
  zone → UTC; no year → the year inside the bounds; outside the bounds → not ok.
- `LevelOf`: JSON (`level`/`lvl`/`severity`/`LogLevel`/`@l`/`log.level`, pino numbers, `msg`/
  `message`/`Message`/`@m`/`@mt` as the text, raw kept); the first four words (brackets, `<>`, `()`,
  a trailing `:`/`,` removed; pure numbers skipped; `level=`/`lvl=`/`severity=` as a word) against
  the table of research R3; klog/glog, Rails and .NET prefixes; continuation (leading space or tab,
  `at `, `Caused by:`, `... N more`, Postgres `DETAIL:`/`HINT:`/`CONTEXT:`/`STATEMENT:`) inherits
  `prev`; else info.
- `Fold`: consecutive equal texts (time span removed) in one stream.
- Tables: every example of research R2 and R3, the lab's six lines first.

### `internal/deploy`
- `LogFiles(ctx, copy, task) ([]LogFile, error)`: `AllocFS().List(alloc, "alloc/logs")` filtered to
  `<task>.<stream>.<n>`, with size and `ModTime`. `ReadLogAt(ctx, copy, path, offset, limit)
  ([]byte, error)`: `AllocFS().ReadAt`; a chunk **shorter than asked** is checked with `Stat`, because a
  missing or purged file makes `readat` answer 200 with Nomad's error text as the body (spike R8.1), and
  `Stat` answers 404 (then `ErrLogRotated`, "rotated away"); a full-size chunk can't be that text.
  `LogCopies(ctx, jobIDs) ([]Copy, error)`: `Allocations().List`
  with a prefix/filter on the jobs, every status, `Gone` for terminal ones. `deploytest` gains a
  fake file system: files of lines per copy and stream, `ls`/`readat` over them, a way to append
  (for Live and "Show older" tests) and to purge a file (the rotated-away case). The seam is a
  small interface the service holds (`logFS`), the fake its second implementation; the fake's purged file
  answers `readat` with that error text and `Stat` with not-found, like Nomad.

### `internal/store`
- `ListDeploymentsIn(ctx, project, environment string, from, to time.Time) ([]DeploymentRecord,
  error)`: deployments of the place's apps whose `created_at` or `updated_at` falls in the range
  (`updated_at` counts only for a terminal status; the service decides the end row's words).

### `internal/service` (`logs.go` grows; `logsread.go` new)
- `ReadLogs(ctx, LogQuery) (LogRead, error)`: resolve the place (`checkPlace`-style 404s), list
  the apps, the copies (`LogCopies`), apply `App`/`Service`/`Copy` before reading; per stream the
  backwards walk under the budgets with the semaphore; per line strip, time, level, continuation;
  fold per stream; filter by levels and `q`; merge newest first by time (a stream's times made
  non-decreasing for sorting); cut to `Lines` rows; the histogram's bins and coverage; the deploy
  rows from `ListDeploymentsIn`; the copies with counts and `Gone`. Errors: `*PlaceError`,
  `ErrNomadUnavailable` (502), field errors on `from`/`to`/`levels`/`q`/`at`.
- `LogsAround(ctx, copy, at LineID, n int) (LogRead, error)`: `readat` ±16 KiB around the offset
  (crossing a file edge), both streams of the copy merged over the same time span, every level,
  the anchor's id; `ErrLogRotated` when the file is gone (404 `log_rotated`).
- `DownloadLogs(ctx, LogQuery, w io.Writer) error`: the k-way merge oldest first, streamed, the
  header and `# cut` lines, one download at a time (`ErrBusy` 409 `download_busy`).
- `WatchLogs(place, live)`: the `logs` notice while a page with Live is open (today's
  `followLogs` per stream, text never in the notice), debounced 2 s.
- Today's `Logs` (per app) keeps its shape and gains `Level`, `ID`, `TimeSource` and the
  stripped `Text` on each line.

### API 0.26.0 (`internal/api/logs.go`)
Per [contracts/api.md](./contracts/api.md): `listLogs`, `getLogsAround`, `downloadLogs`;
`LogLine` gains `level`, `id`, `timeSource` and `text` becomes the stripped text (documented);
`levelOf` and `stripANSI` in `listLogs`' description; `no-store` on every answer; deploy tokens
denied by the default deny (the scope walk lists the three routes as denied); parity entries
for `/logs`, `/parts/logs`, `/logs/download` and the app's tab.

### Console (`internal/ui/logs.go` rewritten, `templates/logs.html`, `static/js/console.js`)
Per the page contract: `/logs` (head with the range bar and Live/Paused, the filter row as one
GET form, the rail with hues by a stable hash of `<app>/<service>` over the handoff's six, the
histogram SVG, the stream with folded rows, error tint, deploy rows, the open row's strip with
Lines around this / Only `<service>` / the row's link / Raw / the first-seen note, "Older lines
were dropped." and "Show older", the footer with the retention sentence and Download);
`/logs?copy&at` (the around view with its notice and "Back to the stream"); `/parts/logs` on the
`logs` notice (the stream and the histogram only: the filters, the rail and the open row's strip
sit outside the swapped region, 023's rule; an open row keeps its id); `/logs/download`; the app's
Logs tab as the same template filtered with "All logs"; the sidebar's Logs item (`scroll-text`);
narrow at 390 (the rail as a chip row, rows on two lines). Styles "Page 7 (024)", `tools/ui-css.sh
build`; DESIGN.md: the service hue (the one identity color), the SVG chart, the estimated time.

### Lab
- The spike (R8.1–R8.8) first, recorded in `research.md`; then `TestConsoleLab` step `console-logs`
  and the gate step `logs` per [quickstart.md](./quickstart.md) §6; the lab's `nginx` or a busybox
  app printing the sample lines (a timed `ERROR`, `[warn]`, a JSON `"level":"error"`, ANSI SGR,
  OSC 8, a `\r` progress line, a NUL, a repeated line, a Go-style untimed line).

## Complexity Tracking

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| (none) | | A line store (SQLite ring) was the one alternative that would have added weight; rejected in research R1 for secrets at rest and an always-on workload. |
