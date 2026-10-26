# Research: Logs (024)

Read-only research on the code at `023-domains` (branch `024-logs`, API 0.25.0), on the vendored
Nomad (`hashicorp/nomad/api@v0.0.0-20260917172403-9dcbdc5e64ec` and the matching server and client
source `nomad@v1.11.0-rc.1.0.20260917172403-9dcbdc5e64ec` in the module cache, cited as `nomad:`
paths) and on the lab's live Nomad 2.0.7, queried read-only on 2026-10-25 between 00:30 and 00:40
UTC with the lab's token (`deploy/dev/.data/env`) through `curl` (cited as "lab, observed"). Nothing
in the lab was changed: only `GET` on `/v1/allocations`, `/v1/job/...`, `/v1/client/fs/ls`, `stat`,
`logs` and `readat`.

Inputs read: `docs/plan.md` row 024, the designer's contract `specs/024-logs/contracts/logs-screen.md`
(2026-10-24, before the spec), `CLAUDE.md` (007's Logs tab, 020.5, 022's output read), the
constitution, `specs/007-htmx-ui-v1/research.md` R10 (today's design), and the handoff's
`LogsScreen.jsx`. **The handoff is not in this worktree** (`design/` is gitignored and absent here);
it was read from the main checkout at
`/Users/jjchiw/Documents/cosmic-chimps/a__cc/caravana/design/console/design/ui_kits/console/LogsScreen.jsx`
(191 lines) and the Logs tab of `AppScreen.jsx` there. The rendered shot `_shots/p9-logs.png` was
not opened.

A scratch Go program outside the repo (`/tmp`, not committed) measured the cost of a naive per-line
pass; its numbers are marked "Mac, measured".

## Findings that change the contract's wording

The contract (`contracts/logs-screen.md`) was written before this research. Eight facts from the
code and the lab bear on it; each R-section carries the detail and a recommendation for the
designer's ruling.

1. **Most lines start with a time, not a level word.** Of the services running in the lab, Traefik
   (`2026-10-25T00:26:49Z ERR …`), Postgres (`2026-10-24 23:48:36.479 UTC [1] LOG: …`), nginx's
   error log (`2026/10/23 23:13:18 [notice] 1#1: …`), rclone (`2026/10/25 00:01:33 NOTICE: …`) and
   whoami (`2026/10/25 00:16:42 Starting up on port 80`) all do (lab, observed). The contract's
   level rule ("the first word or bracketed word of the line") finds a time there. The level must
   be read **after** the time is taken off the front, and from the first few words, not the first
   one (Postgres puts a `[pid]` before `LOG:`). R2 and R3.
2. **"A line on stderr with no level word is `warn`" would mark most of the lab's stderr as
   warnings.** Go's standard `log` writes to stderr with no level (whoami; in the lab twelve copies, nine of
   them running, have a 43-byte stderr of one line and nothing else, and the two read were
   `<time> Starting up on port 80`), and so
   do Python's default `logging` and many images' entrypoints. R3 recommends `info` for every
   unmatched line, stderr included.
3. **Nginx's access log has its time in the middle** (`192.168.5.15 - - [25/Oct/2026:00:33:21
   +0000] "GET / …"`, lab, observed). Folding "the same text after the time prefix is removed"
   folds nothing there: the health check every 10 s differs only by that bracket. The time parser
   must return the span it read, wherever it was, and folding compares the line without that span.
   R2 and R4.
4. **"Lines around this" by time is ambiguous** for a line with no time of its own (R2), and two
   lines can share a millisecond. Nomad's frames name the file and byte offset each read came from,
   and the files are append-only with a growing index, so `<stream>.<file index>.<byte offset>` is
   an exact, stable address for a line while Nomad keeps the file. R4 recommends that address in
   the URL instead of `around=<time>`.
5. **A time range can't be promised; only what was read can.** Nomad keeps output by size
   (Caravana's app tasks get Nomad's default 10 files of 10 MiB per stream), not by time, and a page
   reads a bounded number of bytes from the end. The histogram must not draw zero for minutes it
   didn't read. R1 and R6 add a "read from <time>" coverage line and empty (not zero) bins before it.
6. **Dead copies of older versions are hidden by today's listing.** `deploy.listed`
   (`internal/deploy/copies.go:186-220`) drops terminal copies whose job version isn't the newest,
   so their lines never reach the Logs tab even while Nomad still has them. The contract's "a copy
   that is gone: its lines stay, its rail entry reads (gone)" needs a listing of its own. R1.
7. **The console's CSP forbids style attributes** (`consoleCSP = "default-src 'self'; …"`,
   `internal/ui/terminal.go:21`), so "a bar's height = count / the busiest × 44" can't be an inline
   `style`. Inline SVG with `height`/`y` attributes (presentation attributes, not CSS) works without
   script and without loosening the policy. R6.
8. **ANSI sequences are common, not an edge case**: 287 of the 296 lines of the lab's Traefik
   stdout carry SGR escapes (lab, observed: `\x1b[90m2026-10-25T00:26:49Z\x1b[0m \x1b[31mERR\x1b[0m
   …`). Stripping must run before the time and level are read, or Traefik's lines get neither. R5.

---

## R1. Where the lines come from

**Decision**: read Nomad live on every page view, **no store of lines and no ring buffer**. Bounded
reads with the fs API's `ls` + `readat` (backwards from the end, a byte budget per stream and per
page) serve the range, search, histogram, "lines around this" and Download; the logs endpoint with
`follow=true` (today's `deploy.Logs`) serves only Live. The counts-only in-memory buffer the
contract allows as a fallback is not needed by the lab numbers below; revisit it only if the host
check (R8) misses the 2 s target.

### What Caravana does today

- `deploy.Logs` / `readLogs` (`internal/deploy/logs.go:52-155`) call `AllocFS().Logs(alloc, follow,
  task, stream, "end", tailBytes+1, …)` (`logs.go:85`): framed, `origin=end`, one byte more than the
  tail to know whether the first line is cut. `lineSplitter` (`logs.go:166-210`) splits frames into
  lines, cuts a line at 64 KiB (`maxLogLine`, `logs.go:32`), replaces invalid UTF-8 and drops a
  trailing `\r`. The `cut` flag (`readLogs`'s last argument, used by 022's `ScheduleRunOutput`,
  `internal/deploy/schedules.go:309-333`) says whether the tail left the beginning out.
- `service.Logs` (`internal/service/logs.go:96-129`) reads 64 KiB from the end of each (copy,
  stream) (`logTailBytes`, `logs.go:34`), concatenates them **per copy in start order, stdout before
  stderr**, keeps the last 500 lines (`maxLogLines`) and, with `Follow`, interleaves new lines as
  they arrive with `Time` = arrival (`followLogs`, `logs.go:202-245`). The tail has no time
  (`LogLine.Time` nil, `logs.go:21`). The copies come from `LogCopies` → `Service.Copies` →
  `deploy.Copies` (`internal/deploy/copies.go:156-166`, `Jobs().Allocations(jobID, false)` then
  `listed`).
- The task read is the service's own (`t.copy.Service`, `logs.go:176`): the `names` sidecar
  (`internal/nomadjob/names.go`) and `caravana-volumes` task (016) are never read. Keep it so.
- Callers: the console's Logs tab (`internal/ui/logs.go`: `loadLogs` 500 lines, `pumpLogs` for the
  SSE `log` events), the API's `getLogs` (`internal/api/copies.go:161-205`, and `followLogs`), the
  backup outcome reader (`internal/deploy/backups.go:322`, 16 KiB) and 022's run output.
- Policy: `read-logs` and `read-fs` are both in `deploy/install/caravana-policy.hcl:22` (read-fs since
  016). The logs endpoint accepts either (`nomad:client/fs_endpoint.go:384-389`); `ls` and `stat`
  need `read-fs` (`List`, `Stat`, `stream` at `fs_endpoint.go:102-199`); `readat` is served by the
  agent's `FileReadAtRequest` (`nomad:command/agent/fs_endpoint.go:136`) over the same stream RPC, and
  answered with the lab's token (management, so the scoped token is a lab check, R8). **No capability is added.**

### How much Nomad keeps

- Caravana's app task sets no `LogConfig` (`task`, `internal/nomadjob/translate.go:493-…`; only
  `ScheduleRunJob` sets one: 2 files of 1 MiB, `internal/nomadjob/schedule.go:29-30,93`), so Nomad's
  default applies: `MaxFiles: 10, MaxFileSizeMB: 10` (`nomad:nomad/structs/structs.go:7882-7888`).
  Lab, observed on job `hollo`: every task `LogConfig {MaxFiles 10, MaxFileSizeMB 10}`,
  `EphemeralDisk.SizeMB 300`.
- logmon runs **one rotator per stream** (`nomad:client/logmon/logmon.go:140-157`), each keeping
  `MaxFiles` files of `MaxFileSizeMB` (purge when `logFileIdx - oldestLogFileIdx >= MaxFiles`,
  `nomad:client/logmon/logging/rotator.go:163-190`). So up to **100 MiB per stream, 200 MiB per task**
  (from source; `LogConfig.Validate` checks only one product, 100 MB, against the 300 MB disk,
  `structs.go:7893-7909`). Not observed full in the lab.
- Files are `alloc/logs/<task>.<stdout|stderr>.<n>`, `n` growing from 0, opened `O_APPEND`
  (`nomad:client/allocrunner/taskrunner/logmon_hook.go:197-198`, `rotator.go:168,224`). A task
  restart in the same copy continues the newest file (`lastFile`, `rotator.go:196-215`), so output
  survives restarts within a copy. Lab, observed: `ls /alloc/logs` lists `web.stdout.0`, `web.stderr.0`
  plus the hidden FIFOs `.web.stdout.fifo` (size 0) and the sidecars' files.
- **Retention is by size, so by time it varies by orders of magnitude.** The lab's nginx (a health
  check every 10 s, nothing else): `web.stdout.0` = 1,295,456 bytes, 8,938 lines (145 B/line) over
  25.4 h (copy started 2026-10-23 23:13 UTC) = about 51 KB/h; 100 MiB would hold about 85 days. An
  app writing 100 lines/s of 150 B fills 100 MiB in under 2 h. The contract's footer ("Kept as long
  as Nomad keeps them") is right; the handoff's "Kept for 7 days" is not true for Caravana.
- **Dead copies.** A terminal copy keeps its directory until the client's GC: `gc_max_allocs` 50
  terminal allocations or disk over 80 % (`nomad:client/config/config.go:950-954`; neither
  `deploy/dev/` nor `deploy/install/nomad.hcl.tpl` sets these, 022 research R2). Lab, observed: the
  previous version's copies of `hollo` (`5044446a…` and `80cfd1f6…`, `complete`/`stop` since
  00:10:26 UTC) still answer `ls` and `logs`; the job is at version 2 and the server lists only those
  4 allocations, so version 0's copies were already collected server-side (when exactly is a lab
  check). Today `listed` hides the two terminal ones because their version isn't the newest.

### What a read costs (lab, observed)

Nomad in the Lima VM, called from the Mac through the forward `127.0.0.1:4646` (on a host, Caravana
calls Nomad on loopback, so this is a fair stand-in; the client library tries the node's own address
first, `internal/deploy` `NewNomadClient` keeps that try short).

| Read | Time |
|---|---|
| One `logs?origin=end&offset=65536&plain=true` (64 KiB, nginx) | 2.6-6.5 ms |
| 10 running copies × 2 streams, 64 KiB each, one after another (curl processes) | 0.29-0.30 s total |
| The same 20 reads in parallel | **0.072 s** total (each 1.5-7.6 ms) |
| `readat` 1 MiB of nginx's file | 4.2 ms |
| 20 × `readat` 1 MiB in parallel | **0.126 s** total |
| Framed (`plain=false`, base64 JSON frames, what the Go client uses) | about 1.33× the bytes, same time |

- 64 KiB of nginx's access log is 452 lines, about 1.25 h of that app.
- A naive per-line pass in Go (strip ANSI, try 4 time shapes, scan 5 words for a level, lower-case
  substring search) over 20 MiB of the lab's nginx and Traefik output (147,745 lines): **128-140 ms**
  (Mac, measured: go1.27.1 darwin/arm64, one goroutine, `time.Parse` per attempt, no tuning). A 2 GB
  arm64 VM is slower; even at 5× it is under 0.7 s.

So reading **1 MiB from each of 20 streams (10 copies), parsing it and folding it fits well inside
the contract's 2 s**, with room for 20 copies (40 streams) at a smaller budget.

### Options

**(a) Read live, every view** (recommended).

- The environment: `store.ListAppsIn(ctx, project, environment)` (`internal/store/apps.go:243`),
  then each app's copies. A new listing beside `Copies` keeps terminal copies of every version
  Nomad still lists (no `listed` filter; mark them gone), so a crashed or replaced copy's lines show
  while Nomad has them. One `Allocations().List` with a filter on the job IDs, as `AllCopies` does
  (`copies.go:170-183`), is one call for the whole environment.
- Per stream: `AllocFS().List(alloc, "alloc/logs")` for the files and sizes (one call per copy:
  both streams are in one listing), then `ReadAt` backwards in chunks from the newest file,
  crossing to `n-1` when a file is exhausted, until the oldest timed line read is before the range's
  start, or the stream's budget is spent, or there are no more files. This is the same walk Nomad's
  `findClosest` does for `origin=end` (`nomad:client/fs_endpoint.go:918-985`), but bounded at both
  ends: the logs endpoint without `follow` always streams **to the end** of the newest file, so it
  can't read a window in the middle (`logsImpl`, `fs_endpoint.go:544-640`), which "Show older",
  "lines around this" and a past range need.
- Budgets (to confirm on a host, R8): **1 MiB per stream, 16 MiB per page**, split evenly when an
  environment has more streams than 16; at most **2 reads at once per Caravana** for this page (a
  semaphore), so two open tabs can't hold 64 MiB of strings on a 2 GB VM. A page answer carries the
  coverage: per stream, the time of the oldest line read and whether the budget ended the read.
- Live: today's `followLogs` per stream, only while a page with Live on is open (R7).
- Costs: no disk, no memory between views, no new goroutine when nobody looks. A view of a busy
  environment covers less than the range (shown, R6); search sees only what was read.

**(b) A SQLite ring buffer of lines**, filled by following every copy all the time.

- Gains: a histogram over a full 24 h or 7 d, search over more than the tail, lines of copies Nomad
  has collected, times for every line (arrival time).
- Costs:
  - Following every copy always: 2 open streams per copy to Nomad (40 for 20 copies) and parsing
    all output on the control plane, constantly, whether anyone looks or not.
  - Disk at the app's rate: the lab nginx is about 1.2 MB/day per copy, but an app at 100 lines/s is
    about 1.3 GB/day per copy. A cap (bytes or days) is needed, and a cap below Nomad's 100 MiB per
    stream keeps **less** than Nomad already keeps.
  - SQLite write amplification (WAL, index on time) at the log rate, on the same file as deploys.
  - **Secrets at rest.** App output may hold anything the app prints (020.5's reason for never
    keeping Run a command's output; 022 masks a run's output before storing 64 KiB of it). Today
    no line of an app's output is ever written by Caravana (`service/logs.go:93-95`, constitution
    VII). A ring of lines puts app output in `caravana.db` and in every copy of it (installer's
    upgrade backup, `install.sh`), and masking every line needs every service's secret values in
    memory all the time.
  - Constitution I: not a new runtime dependency, but a standing workload on a 2 GB VM.
- Rejected.

**(c) A counts-only ring in memory** (the contract's fallback): per (copy, minute, level) counts for
24 h. About 20 copies × 1,440 min × 4 levels × 8 B = 0.9 MB, no lines, no secrets. It still needs (b)'s
always-on following and parsing, and it lives only as long as the process (a restart empties the
histogram). Kept as the fallback, not built in 024.

**Rationale**: (a) answers in well under 2 s for the lab's scale, adds nothing that runs while nobody
looks, keeps app output out of Caravana's store, and needs no policy change. Its limit (a busy app's
range isn't fully read) is shown, not hidden.

**Alternatives**: Docker's own `json-file` logs carry a timestamp per line, but Nomad's docker logger
reads the container with `Timestamps` off (`nomad:drivers/docker/docklog/docker_logger.go:99-104`) and
Caravana reaches only Nomad (constitution VI): rejected. A log shipper (Vector, Loki): a runtime
dependency (constitution I), and the plan's "send them somewhere longer" connection for later.

---

## R2. Timestamps

**Decision**: one pure function `timeOf(line) (t time.Time, span [2]int, ok bool)` reads a time from a
small set of shapes **at known positions** (the start, after a short prefix, the Common Log Format
bracket, a JSON field), returns the byte span it used, and every line carries a `timeSource`:
`line` (read from the line), `received` (Live: the arrival time, as today) or `estimated` (none of
its own). A line with no time takes the time of the nearest older timed line of the **same stream**;
a stream's first lines with none take the copy's start; a read time ("now") is never used for an
old line.

### What Nomad gives

- Nothing: Nomad's lines are the bytes the task wrote (`deploy.LogLine` has no time,
  `internal/deploy/logs.go:23-28`; docker logger without timestamps, above).
- Bounds per stream, for free: the copy's task start (`Copy.StartedAt`, `copies.go:41`) and each
  file's `ModTime` from `ls` (lab, observed: `"ModTime":"2026-10-24T17:48:08.265893964-06:00"`, the
  node's local offset, RFC 3339). Every line of file `n` was written between `ModTime(n-1)` and
  `ModTime(n)` (file 0: between the task's start and its `ModTime`). These bounds check a parsed
  time (below) and bound an estimate.

### The shapes (each with a line from the lab where one exists)

| Shape | Example | Where |
|---|---|---|
| RFC 3339 / ISO 8601 at the start (`T` or space, fraction, `Z`/offset) | `2026-10-25T00:26:49Z ERR …` | Traefik (after ANSI strip), lab, observed |
| Same with a zone word | `2026-10-24 23:48:36.479 UTC [1] LOG: …` | Postgres, lab, observed |
| Go `log` / nginx error log, no zone | `2026/10/25 00:16:42 Starting up on port 80`; `2026/10/23 23:13:18 [notice] 1#1: …` | whoami, nginx, rclone, lab, observed |
| Common Log Format bracket after 3 words | `192.168.5.15 - - [25/Oct/2026:00:33:21 +0000] "GET / …"` | nginx access log, lab, observed |
| Redis: `pid:role` then `02 Jan 2006 15:04:05.000` | `1:M 23 Oct 2026 23:13:18.730 * Ready to accept connections tcp` | Redis, lab, observed |
| Python `logging` default (comma fraction) | `2026-10-25 00:33:21,123 INFO …` | not in the lab |
| Bracketed at the start (gunicorn, Rails) | `[2026-10-25 00:33:21 +0000] [1] [INFO] …`; `I, [2026-10-25T00:33:21.123456 #1]  INFO -- :` | not in the lab |
| logfmt key | `time=2026-10-25T00:33:21.123Z level=INFO msg=…` (Go `slog` text), `ts=…` | not in the lab |
| JSON keys | `time`, `ts`, `timestamp`, `@timestamp`, `@t` (Serilog compact), `t`; a string (RFC 3339) or a number (seconds as zap's float `ts`, milliseconds as pino's `time`, told apart by magnitude) | not in the lab |
| syslog RFC 3164 / klog | `Oct 25 00:33:21 …`; `I1025 00:33:21.123456 1 x.go:12] …` (no year) | not in the lab |

Rules:

- Strip ANSI first (R5); only then look.
- Look only at the start, after one short prefix token (at most 8 bytes, like `1:M`, `I,`, `<34>1`),
  inside a leading `[`, at the CLF bracket after exactly three space-separated words, at
  `time=`/`ts=` among the first logfmt pairs, or in a JSON object's keys. Never search the whole
  line: a message that mentions a date ("expires 2026-11-01") must not become its time.
- A time without a zone (Go `log`, nginx error log, Redis, Python) is read as **UTC** (containers'
  default `TZ`; Caravana sets no `TZ` in `task`). A time without a year (syslog, klog) takes the
  year that puts it inside the stream's bounds.
- **Check against the bounds**: a parsed time more than 1 minute before the copy's start or after the
  file's `ModTime` is not trusted (a container with a local `TZ`, a replayed log, a date in a
  message that happened to sit at the start): the line becomes `estimated`. This keeps a wrong zone
  from scattering an app's lines across the histogram.
- Within a stream the order of bytes is the order of lines. The merged view sorts streams by time
  but never reorders lines of one stream: a stream's times are made non-decreasing (a line older
  than the one before it, a multi-threaded app's few-ms jitter, takes the one before's time for
  sorting and keeps its own for display).

### Lines without a time

| Option | For | Against |
|---|---|---|
| The read's time ("now") | simple | wrong for every old line: puts the whole tail in the last histogram bin; today's tab avoids it by showing no time |
| **The previous (older) timed line's time in the same stream** (recommended) | right for continuation lines (stack traces, .NET's two-line console format, Postgres `DETAIL:`/`HINT:`), which follow the line they belong to | a stream with no timed lines at all has nothing to carry |
| Interpolate by byte offset between the bounds | gives every line some time | a guess dressed as data |
| "No time" (not in the histogram, sorted by stream order) | honest | breaks "newest first" across copies for apps that never print a time |

Recommended: carry the older timed line's time (`estimated`); a stream's lines before its first timed
line take the copy's start or, for file `n > 0`, the previous file's `ModTime` (`estimated`, the
earliest they can be). The row draws an estimated time in fg-4 with `≈` and a `title` "this line has
no time of its own; shown at the line before it" (a ruling for the designer). Live lines keep
`received` exactly as `followLogs` does now (`service/logs.go:229`).

**Alternatives**: a regular expression per format (rejected: slower and harder to read than a few
fixed-layout checks; the measured naive pass is already fast); a per-app "time format" setting
(rejected for 024: nobody should have to configure a log viewer; maybe later for the rare app).

### Spike follow-up (2026-10-24, the gate step)

The gate step `logs` (lab, Nomad 2.0.7) found two faults in the rule above; the rule changed:

1. **A stream with no timed line put every line at the copy's start.** The sample app's stderr was
   untimed `[warn] careful` every 2 s: the first line took the start and each later one the line
   before it, so after 15 minutes they all fell out of the 15-minute range, and "lines around" an
   ERROR on stdout found no stderr line. Now, per file: a file with no timed line in what was read
   (or a skewed one) has **every line placed by byte offset between its write bounds** (`ModTime(n-1)`,
   or the copy's start for file 0, to `ModTime(n)`; the newest file's upper bound is its last write,
   about now for a live stream), `estimated`. The "interpolate by byte offset" option rejected above
   for timed streams is the only honest estimate when a stream carries no time at all. In a file with
   timed lines, the lines before its first timed line are placed by offset between the file's lower
   bound and that line's time; the lines after a timed line keep its time, as before. The page walk
   stops at the range's start by these placed times when a file has shown no timed line, not at its
   budget, and `around`'s read of the other stream finds untimed lines the same way (the search by
   time's fallback to offsets, `placeByProportion`, is the same placement read backwards).
2. **The copy's start isn't a strict lower bound.** The copy's `StartedAt` (04:01:00.039) was later
   than the first stdout line's own time (04:00:59): the container's clock against Nomad's
   task-start event. The untimed line before it was stamped later than the timed line after it, and
   `around=<time>` anchored on it. Now a timed line earlier than the start by less than the slack
   (a minute, as the bounds already allowed) is trusted, and the lower bound moves as far again
   before it (never more than the slack before the start), so the untimed lines before it are placed
   before it; a time more than a minute before the start is still refused. The search by time
   returns the first line at or after the time even when it is before the file's first timed line.

The placement lives in the service (`placeLines`, `internal/service/logsread.go`), which knows the
files' sizes and the lines' offsets; `logline.Parse` is unchanged.

---

## R3. Levels

**Decision**: a pure `levelOf(line, prevLevel) (level string, msg string)` applied after ANSI
stripping and after the time span is taken off. Order:

1. **JSON** (the line starts with `{` and parses as an object; at most 64 KiB, the line limit):
   the first of `level`, `lvl`, `severity`, `LogLevel` (.NET's JSON console), `@l` (Serilog compact;
   **absent means Information**), `log.level`; string values mapped by the table below, numbers as
   pino/bunyan (10 trace, 20 debug, 30 info, 40 warn, 50 error, 60 fatal). The text shown is `msg`,
   `message`, `Message`, `@m` or `@mt` when present; the raw line stays for "Raw".
2. **Words**: the first **4 words** of what is left (a word is split by spaces, with surrounding `[]`,
   `<>`, `()`, a trailing `:` or `,` removed, compared case-insensitively) are looked up in the table;
   the first hit wins. `level=<x>` / `lvl=<x>` / `severity=<x>` in the first 6 logfmt pairs counts as
   a word. Pure numbers (`[1]`, a pid) are skipped, not counted as a miss.
3. **Single-letter prefixes**: klog/glog `^[IWEF]\d{4} ` (I info, W warn, E error, F error); Rails
   `^[DIWEF], \[` (same letters, D debug); .NET console's `^(trce|dbug|info|warn|fail|crit): ` at the
   very start (fail and crit error, trce and dbug debug).
4. **Continuation**: a line that starts with a space or a tab, or with `at ` / `Caused by:` /
   `... N more`, or is a Postgres `DETAIL:`/`HINT:`/`CONTEXT:`/`STATEMENT:` line, takes the
   **previous line's level** in the same stream (stack traces stay red under their error; .NET writes
   the message on the line after `fail: Category[0]`, indented 6 spaces).
5. **Unmatched**: `info`, **on stdout and stderr alike** (finding 2). The row's stream stays visible
   in the open row ("stderr").

Table (case-insensitive):

| Words | Level |
|---|---|
| `error`, `err`, `eror`, `fatal`, `ftl`, `panic`, `pnc`, `crit`, `critical`, `alert`, `emerg`, `emergency`, `fail`, `severe` | error |
| `warn`, `warning`, `wrn`, `wrng` | warn |
| `info`, `inf`, `information`, `notice`, `log` (Postgres), `ok` | info |
| `debug`, `dbg`, `dbug`, `trace`, `trc`, `trce`, `verbose`, `debug1`…`debug5` (Postgres), `fine`, `finer`, `finest` | debug |
| Line starts `panic:`, `fatal error:`, `Traceback (most recent call last):`, `Exception in thread` | error |

Examples (the first six are the lab's own lines):

| Line (after ANSI strip) | Time taken off | Level | Why |
|---|---|---|---|
| `2026-10-25T00:26:49Z ERR error="middleware …" routerName=…` (Traefik) | `2026-10-25T00:26:49Z` | error | word 1 `ERR` |
| `2026-10-24 23:48:36.479 UTC [1] LOG:  database system is ready` (Postgres) | `… UTC` | info | `[1]` skipped, `LOG` |
| `2026/10/23 23:13:18 [notice] 1#1: start worker processes` (nginx) | `2026/10/23 23:13:18` | info | `notice` |
| `2026/10/25 00:01:33 NOTICE: Failed to copyto: …` (rclone) | `2026/10/25 00:01:33` | info | `NOTICE` (rclone writes its errors as `ERROR :`) |
| `1:M 23 Oct 2026 23:13:18.804 # WARNING: Redis does not require authentication…` (Redis) | `23 Oct 2026 23:13:18.804` | warn | `#` isn't a word, `WARNING` is |
| `2026/10/25 00:16:42 Starting up on port 80` (whoami, stderr) | `2026/10/25 00:16:42` | info | unmatched; stderr doesn't make it warn |
| `192.168.5.15 - - [25/Oct/2026:00:33:21 +0000] "GET / HTTP/1.1" 200 896 …` (nginx access) | the bracket | info | unmatched (an HTTP status isn't a level; R4's search finds ` 502 `) |
| `{"level":"error","ts":1792886809.1,"msg":"dial tcp: refused"}` (zap) | `ts` | error | JSON `level` |
| `{"@t":"2026-10-25T00:33:21Z","@mt":"Started {Id}","Id":3}` (Serilog compact) | `@t` | info | no `@l` means Information |
| `fail: Microsoft.AspNetCore.Server.Kestrel[13]` then `      Connection id "0HN…"…` (.NET console) | none | error, error | `fail:`; then continuation |
| `time=2026-10-25T00:33:21Z level=WARN msg="slow query"` (Go slog) | `time=` | warn | logfmt `level` |
| `E1025 00:33:21.123456       1 reflector.go:138] failed to list` (klog) | `1025 00:33:21.123456` | error | `E` + 4 digits |
| `[2026-10-25 00:33:21 +0000] [1] [INFO] Booting worker with pid: 7` (gunicorn) | the bracket | info | `[1]` skipped, `[INFO]` |
| `WARNING:root:disk almost full` (Python basicConfig) | none | warn | `WARNING` before `:` (split on `:` too for the first word) |
| `Error handling is disabled for /health` | none | error | a known false positive of "first 4 words"; accepted |
| `\tat com.example.Foo.bar(Foo.java:42)` | none | the previous line's | continuation |

**Rationale**: a table of words over the first few tokens covers the formats the lab and the .NET/Aspire
apps (the maintainer's Frunchy) actually write, with one pure function and a table test, as the contract
asks. Matching only the first 4 words keeps "an error occurred" deep in an info line from turning it red.

**Alternatives**: inferring `error` from an HTTP status in an access log (rejected for 024: it is a
different kind of rule, and a 404 isn't an app error; a later "requests" view belongs to 025's Traefik
metrics); the stderr → warn fallback (rejected, finding 2); per-app level rules (later, if ever).

---

## R4. Folding, around, only this service, search, range, Download

### Folding

- **Per stream, before merging**: consecutive lines of one stream (one copy's stdout or stderr) whose
  text **without the time span** (R2) is byte-equal fold into one row with `repeat` = N - 1 (the "×N"
  chip shows N), the row's time the newest, its id the newest line's. Lab, observed: nginx's 8,938
  lines are almost all the health check `"GET / HTTP/1.1" 200 896 "-" "Nomad/2.0.7 …"` every 10 s,
  differing only in the CLF bracket; they fold to one row per run.
- Folding never crosses streams or copies (two copies' identical health checks are two rows: that is
  information), and a filter is applied **before** folding (a search for `502` folds the 502s only).
- The open row's note "First seen 09:12 UTC today, 3 times since" (contract) counts equal texts (time
  span removed) in what was read; it says "in what was read" when the coverage (R6) is partial.

### Lines around this

- **Address a line by `<copy>.<stream>.<file index>.<byte offset>`** (the offset of the line's first
  byte in `alloc/logs/<task>.<stream>.<n>`), not by time. Nomad's frames carry `File` and `Offset`
  (`nomad:client/lib/streamframer/framer.go:20-33`; lab, observed: `{"Data":"…","File":
  "alloc/logs/web.stdout.0","Offset":1290816}` where `Offset` is where that frame's data ends), and
  `readat` reads any window (lab, observed: `readat?path=/alloc/logs/web.stdout.0&offset=1000000&
  limit=400` returned the bytes at that offset). Files are append-only and their index only grows, so
  the address stays valid until Nomad purges that file; then the page says "That part of the copy's
  output has been rotated away" (a 404-like notice, not an error page).
- The read: `readat` from `offset - 16 KiB` to `offset + 16 KiB` of that file (crossing into `n-1` or
  `n+1` when the window passes a file's edge), split into lines, the 50 before and 50 after, every
  level, both streams of that copy merged by time (the other stream read over the same time span),
  the line highlighted. The element id is `line-<copy short>-<stream>-<n>-<offset>` (stable, page 1's
  rule).
- URL: `/logs?copy=<id>&at=<stream>.<n>.<offset>` (and `/v1/logs/around?…`, R7). The contract's
  `around=<time>` stays possible as a second form for a time from elsewhere (an alert's time): binary
  search the stream's files by the time of the first timed line in a 4 KiB `readat` probe (logs are
  append-only, so times are nearly monotonic per stream); about `log2(100 MiB / 4 KiB)` ≈ 15 probes
  at most (a lab check, R8).

### Only this service

- A filter `service=<app>/<service>` (an environment can have two apps with a `web`), applied to the
  copy list **before reading**, so choosing a service reads only its copies and gets the whole budget.
  `copy=<id>` likewise. The rail's copy counts come from the listing, not from the read.

### Search

- **Case-insensitive substring** over the stripped text plus the service name (the handoff's
  `(text + ' ' + service).toLowerCase().includes(q)`), using a Unicode-aware fold
  (`strings.ToLower` on both, or `strings.EqualFold` over windows), applied server-side to what the
  read returned. One field, the whole query as one string (a space is part of it). At most 200 bytes.
- **No regular expressions in 024.** Go's `regexp` is linear-time (no ReDoS), so it would be safe, but
  the handoff's placeholder ("text, booking id, status code") asks for substrings, a regexp syntax in
  a search box surprises people who type `?` or `.`, and it can be added later as `re:` without
  breaking anything.
- Search does not widen the read: it filters the bytes the budget read. "N lines" counts matching
  lines (unfolded); when coverage is partial the field's note says "in the last <span> read".

### Time range

- The contract's presets 15 min, 1 h, 24 h, 7 d (default 15 min), as `from`/`to` in the API (R7). The
  read walks back until `from` or the budget (R1). `before=<time>` ("Show older") restarts the walk
  from that time using the same binary search as `around=<time>`.

### Download

- `GET /logs/download?<the page's query>` and `GET /v1/logs/download`: `text/plain; charset=utf-8`,
  `Content-Disposition: attachment; filename="caravana-logs-<project>-<environment>-<from>-<to>.txt"`
  (times as `20261025T003321Z`, no `:` in a file name; as `exportApp` does with `mime.FormatMediaType`,
  `internal/api/export.go:47`), `Cache-Control: no-store` (the output may hold anything, as
  `runCommand`, `internal/api/actions.go:82`).
- One line per log line: `2026-10-25T00:33:21.123Z\t<app>/<service>\t<copy short>\t<stream>\t<level>\t<text>`
  (an estimated time with a trailing `~`), folded rows unfolded, deploy rows as `# deploy …` lines, a
  first line `# caravana logs <project>/<environment> <from> to <to>, read from <coverage> (<filters>)`.
- **Oldest first** in the file (a ruling for the designer: a log file reads top-down, and it lets the
  server stream the answer with a k-way merge, constant memory per stream, instead of holding the
  whole range).
- Limit: a bigger budget than the page (**8 MiB per stream, 64 MiB per download**, at most one
  download at a time per Caravana) and a last line `# cut: the budget ended at <time>; older lines
  weren't included` when it bites. Control characters are stripped (R5) so `cat` of the file can't
  run escape sequences in the reader's terminal.

---

## R5. ANSI escape sequences

**Decision**: strip, with one small pure function `stripANSI(string) string` in the logs code (no
module), applied to every line before `timeOf` and `levelOf`, in the page, the API, the stream events
and Download. No color is rendered (the contract's ruling: the console's hues mean status).

- What it removes (ECMA-48): CSI `ESC [` parameter bytes `0x30-0x3F`, intermediates `0x20-0x2F`, a
  final byte `0x40-0x7E` (SGR colors `ESC[31m`, cursor moves, erase line); OSC `ESC ]` … ended by BEL
  (`0x07`) or ST (`ESC \`), which includes hyperlinks (OSC 8) and clipboard writes (OSC 52); DCS / SOS /
  PM / APC (`ESC P`, `ESC X`, `ESC ^`, `ESC _`) to ST; the other two-byte `ESC` + `0x40-0x5F`; the C1
  forms `U+009B` (CSI) and `U+009D` (OSC) as UTF-8; and the remaining C0 controls except tab (BEL,
  backspace, NUL, `0x7F`). An unterminated sequence at the end of a line is dropped to the end.
- A carriage return inside a line (progress bars: `10%\r20%\r30%`) keeps only the text after the last
  `\r`, as a terminal would show it (`readLogs` already drops a trailing one, `logs.go:113`).
- Fast path: a line with no byte `< 0x20` except tab, no `0x7F`, and no `0xC2` (the lead byte of
  `U+0080`-`U+009F`) is returned as it is (the measured pass, R1, includes the strip).
- Lab, observed: Traefik's stdout, 287 of 296 lines with SGR (`\x1b[90m`, `\x1b[31m`, `\x1b[1m`,
  `\x1b[36m`, `\x1b[0m`); stripped, they read `2026-10-25T00:26:49Z ERR error="middleware
  \"lab023-web-13ab8f6e-auth@file\" does not exist" entryPointName=websecure …`.
- Rendering SGR as spans was considered: html/template would escape the text, and spans with classes
  (not `style`, which `consoleCSP` refuses) would pass the policy, but app colors would compete with
  the status hues and with the per-service hues the contract adds. Rejected with the contract.
- Today's tab and API send the raw text (escapes included, shown by `textContent`, harmless in a
  browser); 024 changes `getLogs`' `text` to the stripped form (R7). The handoff's `LogsScreen.jsx`
  has no ANSI handling at all (its sample lines are plain).

---

## R6. The histogram and the deploy markers

**Decision**: 30 bins of `(to - from) / 30` from the same read as the stream; bins before the read's
coverage are drawn **empty with a hatch, not as zero**; deploy markers and deploy rows from the
store; drawn as **inline SVG** with attributes, no script and no style attribute.

### Bins

- Each read line (unfolded, after filters, `estimated` included) counts in the bin of its time; error
  lines are also counted in the bin's `errors`. Height = count / the busiest bin × 44 px, at least
  2 px when non-zero (contract).
- **Coverage**: the oldest time any stream's read reached when its budget ended (the newest of those,
  since a stream that stopped early has nothing older). Bins wholly before it are "not read": drawn as
  a 2 px dashed stub with a `title` "not read: older output wasn't loaded", and the axis says "read
  from 14:21 UTC" in fg-3. A bin is never drawn as zero lines when its minutes weren't read.
  Estimated-time lines count where they are placed; the open bin `title` says how many were
  estimated ("24 lines, 3 at an estimated time").

### Deploy markers and rows

- Source: deployments of the environment's apps (`apps` joined on `project`, `environment`) whose
  `created_at` or, for a finished deployment, `updated_at` falls in `[from, to]`. Fields already in
  `store.DeploymentRecord` (`internal/store/deployments.go:37-70`): `CreatedAt` (started rolling out),
  `RegisteredAt` (Nomad accepted it; nil before 021), `UpdatedAt` (for a terminal status it is the end:
  terminal statuses have no next status, `allowedNext`, `deployments.go:31-34`; `stages.go:84` draws
  "Live" from it), `Status`, `JobVersion`, `Deployment.Origin`, `TokenName`, `SourceApp`/
  `SourceEnvironment`. The words are page 4's `deployWho` (`internal/ui/home.go:59`). There is no
  `finishedAt` column and none is needed.
- A store query `ListDeploymentsIn(ctx, project, environment, from, to)`; `deployments_by_time
  (created_at DESC)` exists (`internal/store/migrations/0004_console.sql:2`); `updated_at` has no index,
  which is fine at Caravana's deployment counts. **No migration.**
- Two rows per deployment in range (contract): "started rolling out" at `CreatedAt` and "is live" /
  "failed its health checks · rolled back to vN" / "was replaced by a newer deploy" at `UpdatedAt` when
  terminal. Markers on the histogram at the same two times (`title`s "v24 rolling out", "v24 live").
  A deploy row is merged into the stream by time like a line and is never filtered by level or
  search (the handoff keeps `deploy` rows through every filter except service); "only this service"
  keeps only the deploys of that service's app.

### Drawing without JavaScript

- `consoleCSP` (`internal/ui/terminal.go:21`) means no `style` attribute and no inline `<style>`. The
  console draws its bars with `<meter>` today (`app.html:444`, `schedules.html:67`; CSS
  `input.css:231-236`), but a vertical stacked bar with a dashed marker at a fractional x isn't a
  meter.
- **Inline SVG**: `<svg viewBox="0 0 300 48" preserveAspectRatio="none" role="img" aria-labelledby=…>`,
  one `<rect>` per bin part (x, y, width, height as attributes, which CSP doesn't govern) with a class
  (`cv-hbar`, `cv-hbar-err`, `cv-hbar-unread`) whose `fill` comes from `app.css` tokens, one `<line>`
  per deploy marker (class `cv-hmark`, `stroke-dasharray` in CSS), a `<title>` per bin for the hover
  value. html/template escapes attribute values in SVG as in HTML (numbers here). This is the
  console's **first inline SVG** (`grep '<svg' internal/ui/templates` finds none); `tools/ui-css.sh`
  must see the new classes.
- Accessibility: the SVG's label is a one-sentence summary ("612 lines in the last 15 minutes, 4
  errors, 1 deploy"), and the bins' numbers are also in a visually hidden `<table>` for screen readers.
- Alternative: 23 height classes (`cv-h-0` … `cv-h-22`, 2 px steps) on `<span>`s: works, but markers
  need fractional positions, which classes can't give without another 30 of them. Rejected for SVG.

---

## R7. The API

### What `getLogs` lacks today

`GET /v1/apps/{id}/logs` (`api/openapi.yaml:1016-1086`, schemas `LogLine`/`LogLines` at `:5089-5118`;
handler `internal/api/copies.go:161-205`): one app; `copy`, `stream`, `lines` (≤ 500), `follow`;
oldest first; no time on tail lines; no level; no search; no range; no around; no download; text
with raw escapes; terminal copies of older versions not included.

### Likely operations (0.26.0, all additive)

- **`listLogs`** `GET /v1/logs` — query `project`, `environment` (both required unless Default ›
  production), `app`, `service` (`<app>/<service>`), `copy`, `stream` (`stdout|stderr|both`), `levels`
  (comma list, default `error,warn,info`), `q`, `from`, `to` (RFC 3339; or `range=15m|1h|24h|7d`),
  `before`, `lines` (rows after folding, 1-1000, default 500). Answer:
  - `lines[]`: `LogEntry{id, time, timeSource (line|estimated|received), app, service, copy, stream,
    level, text, raw?, repeat}`, newest first; `id` is the R4 address; `raw` only for a JSON line;
  - `deploys[]`: `LogDeploy{deploymentId, app, service?, at, kind (started|live|failed|reverted|
    superseded), jobVersion, who}` (or the rows merged into `lines` with `kind: deploy`; the contract
    leans to rows with the read, either shape works);
  - `histogram{from, to, bucketSeconds, buckets[]{from, count, errors, read}}`;
  - `coverage{from, complete}` and `copies[]{id, app, service, gone, lines}` for the rail.
- **`getLogsAround`** `GET /v1/logs/around?copy=&at=<stream>.<n>.<offset>&lines=50` (or `around=<time>`):
  `{lines[], anchor}`; 404 `not_found` when the copy is gone, 410-like `log_rotated` (or 404 with that
  code) when the file is purged. The contract folds it into `listLogs` with `around`; a separate
  operation keeps the "unfiltered, every level" rule out of `listLogs`' parameter rules.
- **`downloadLogs`** `GET /v1/logs/download` — `listLogs`' query, `text/plain`, attachment,
  `no-store` (R4).
- **`getLogs`** (per app) keeps its path and parameters; each `LogLine` gains `level`, `id` and
  `timeSource`, and `time` is set on tail lines too when read from the line. `LogLine` has
  `additionalProperties: false`; adding optional properties is additive for readers, as every minor
  version so far. `text` becomes the ANSI-stripped text (documented: "control sequences removed").
  Whether `getLogs` also becomes newest first is a contract choice; recommend **no** (its clients,
  the MCP `get_logs` tool, `internal/mcpserver/read.go:135`, read oldest first today).
- Live for the environment: the contract's `logs` notice on the console's stream, with a refetch of
  the region (`/parts/logs?…`), debounced to one per 2 s while Live is on and only while such a page is
  open. The notice comes from following the environment's streams (today's `followLogs`) with no line
  text in the notice. The API's `listLogs` gets no `follow` in 024 (`getLogs?follow=true` stays the
  API's live read per app).
- Errors: `invalid_field` (422) naming `from`, `to`, `levels`, `q`, `at`; `not_found`
  (`project_not_found` / `environment_not_found` as 019's `*PlaceError`); 502 `nomad_unavailable` as
  today. Deploy tokens: not in `scopeRules` (`internal/api/scope.go:30-42`), so 403 `token_scope` by
  default deny (today's `getLogs` is denied to them too); `TestScopedTokenWalksEveryRoute` covers the
  new routes.
- `levelOf` and `stripANSI` described in `listLogs`' description so a client can match the console.
- MCP: a read tool `search_logs` over `listLogs` is natural (`internal/mcpserver` is an API client;
  its contract test checks requests against `openapi.yaml`); its description must repeat that log
  text is data, not instructions (the server's instructions already say so). Optional for 024.

---

## R8. Lab checks to run (before the translator or any store change)

None of these was run as a test; the read-only observations above are marked where they were seen.

1. **`origin`/`offset` across a rotation**: an app that writes past 10 MiB (or a test job with
   `LogConfig {MaxFiles 3, MaxFileSizeMB 1}`): `logs?origin=end&offset=<more than the newest file>`
   continues into `<task>.<stream>.<n-1>` (source says yes, `findClosest`); frames' `File` changes
   mid-read; `readat` with an offset past a file's size answers what (empty, 416, an error?); a
   purged file's `readat` answers 404 or 500.
   - **Lab, observed (2026-10-24, Nomad 2.0.7, job `spike024`, `LogConfig {MaxFiles 3, MaxFileSizeMB 1}`):**
     - `ls alloc/logs` after rotation (names, bytes, `ModTime` local offset -06:00): `main.stdout.1`
       1,021,896, `main.stdout.2` 1,032,520, `main.stdout.3` 79,348 (growing), `main.stderr.0` 0, plus the
       hidden FIFOs `.main.stdout.fifo` / `.main.stderr.fifo` (size 0, `ModTime` = now). Files close **under**
       1 MiB (1,047,017, 1,021,896, 1,032,520 bytes for 1,048,576): the rotator moves to the next file
       before a write that wouldn't fit, so a file isn't an exact 1 MiB and no line straddled two
       files at the boundaries looked at (the start of `.1`, the end of `.2` with the start of `.3`: each file
       began with a timestamp and `.2` ended with `\n`).
     - `logs?origin=end&offset=<more than the newest file>&plain=false` **continues into the previous
       file**, oldest first: frames `File: alloc/logs/main.stdout.2` (3 frames: 65,536 + 65,536 + 20,310
       bytes), then `File: alloc/logs/main.stdout.3` from its offset 0 (frames of 65,536). `File` changes
       mid-read and the first frame of the next file restarts at its start.
     - **The frames' `Offset` is not the end of the frame's data for a short last frame.** For full frames
       it is the end offset (`.2`: 799,716, 865,252, 930,788, 996,324, each +65,536); for the last, short
       frame of a file it is the frame's start + 65,536, not + its length (`.2` is 1,032,520 bytes, the
       short frame started at 996,324 and said `Offset: 1,061,860`; another read: start 1,012,210, length
       20,310, `Offset: 1,077,746`, beyond the file's size). **A line id must come from Caravana's own
       `readat` offsets, never from a frame's `Offset`**; Live (`follow=true`) therefore can't mint
       `<file>.<offset>` ids from frames either, unless it reads the offset back with `Stat`.
     - A framed read also carries zero-length frames with a `File`/`Offset` (end of file reached while
       the writer is faster than the reader) and a bare `{}` (a heartbeat), then continues; a consumer
       must skip both. `plain=true` of the same read returned the same bytes, concatenated.
     - `readat` with an offset **past the end** of a file: `HTTP 200`, empty body (Content-Length 0), for
       an offset 5,000 bytes past and for one at 50,000,000.
     - `readat` of a file that doesn't exist (a purged `main.stdout.0`, or `main.stdout.99`): **`HTTP 200`
       with the error text as the body**, `stat /opt/nomad/data/alloc/<alloc>/alloc/logs/main.stdout.0:
       no such file or directory` (115 bytes, `text/plain`). `stat` of the same name answers a proper
       `404` with the same text. So a purged file read by `readat` can come back as "data" that is an error
       message. Rule for `ReadLogAt`: a chunk **shorter than asked** is checked with `Stat` (404: the file
       is gone, say "rotated away"; else the chunk is the end of the file); a full-size chunk can't be
       that text (115 bytes against 64 KiB asked).
2. **File names and index**: after rotation, `ls` shows `.<n>` with `n` growing and the oldest gone
   (`MaxFiles` kept); a task restart (Restart in the console) keeps appending to the newest file, not
   a new `.0`.
   - **Lab, observed:** the first file was purged as soon as `.3` was created (`ls` showed `.1 .2 .3`),
     and `.1` as soon as `.4` was created: `MaxFiles` files kept, `n` growing (`.0` to `.4` in about 8 min),
     `main.stderr.0` untouched (0 bytes, no rotation). `PUT /v1/client/allocation/<alloc>/restart`
     (`HTTP 200`, events `Restart Signaled`, `Terminated Exit Code 137`, `Restarting`, `Started`)
     **appended to the newest file**: the new run's `line 1` sits at byte 886,791 of `main.stdout.3` (the
     file held 886,791 bytes just before), no new `.0`, and rotation went on from there (`.4` next). Output
     survives a restart inside a copy, as the source said.
3. **Dead copies**: after a deploy, the previous version's copy still answers `ls` and `logs` (seen for
   `hollo`, 2026-10-25); how long until the server stops listing it and the client deletes its
   directory, with the installer's defaults; a failed copy (a crash loop) keeps its output.
   - **Lab, observed:** `spike024` was redeployed with a changed arg (job version 1, 02:37:55 UTC). The
     version-0 copy `9be67afc…` went `complete`/`stop` at once, and for the whole observation (300 s,
     polled every 30 s, until 02:43:49) it was still **listed** by `GET /v1/job/spike024/allocations`
     and still answered `ls` and `logs` with 200 (and `readat` on its files). The version-1 copy was
     listed beside it from its start and, once its job was stopped without purge (02:40), stayed listed and
     readable too. The client's GC settings from `GET /v1/agent/self` are the defaults:
     `GCMaxAllocs 50`, `GCDiskUsageThreshold 80`, `GCInodeUsageThreshold 70`, `GCInterval 60 s`,
     `GCParallelDestroys 2`. So a dead copy's output is readable for **at least minutes**, in practice until
     50 terminal allocations pile up on the node or the disk is over 80 %; not measured longer (the server's
     own GC of old allocations was not seen in 5 minutes and, from source, is the evaluation GC at 1 h or the
     job GC at 4 h, not verified). A failed copy (a crash loop) wasn't run.
4. **The read cost on a real host** (2 GB arm64 if possible): 20 copies × 2 streams × 1 MiB through
   `readat`, plus parse and fold, under 2 s with the semaphore of 2; peak RSS of Caravana during it.
   - **Lab, observed (Mac to the Lima VM through `127.0.0.1:4646`, `curl`, `xargs -P 2`):** the lab has
     only **one stream over 1 MiB** (nginx's `web.stdout.0`, 1.4 MB), 44 (copy, stream) pairs in all, most
     of them 0 to 43 bytes. So the check was run two ways. **(a)** The 20 largest pairs, each read from
     offset 0 up to 1 MiB (1,121,814 bytes in all): wall **0.26 to 0.31 s** (three runs; median call 2 to
     7 ms, slowest 31 ms). **(b)** The nginx file read 20 times, 1 MiB each (20 MiB), two at a time: wall
     **0.28 to 0.31 s** on a warm run, **0.96 s** on a cold first one (median call 8 to 58 ms, slowest 171
     ms). Both are inside the 2 s of the contract, with room.
   - **A CPU-hungry neighbour breaks it.** The same reads were first run while `spike024` (a busy loop
     writing as fast as it can) ran on the node, and the Mac was also at load average 12 (another
     project's tests): the 20 reads took **18.8 s, 25.9 s and 74.3 s**, single calls up to **33.8 s** (even a
     43-byte read took 11 to 33 s), and a 1 MiB `readat` took 0.8 to 6.9 s. Stopping the loop brought them
     back to 0.06 to 0.24 s per MiB while the Mac's load average stayed at 12, so the loop was the main
     cause. Nomad's file reads share the node's CPU with the apps, so on a small
     host a log view during a CPU storm is slow however cheap the read; Caravana needs a deadline per
     page (the page answers with what it read and says the rest was not read), not only a semaphore.
   - **Caravana's RSS is not measurable here** (Caravana wasn't part of this spike, no Go code ran); the
     check of peak RSS with the semaphore of 2 stays a check on a real host.
5. **The binary search by time** (`around=<time>`, `before=`): probes needed on a 100 MiB stream, and
   behavior on a stream with no timed lines (falls back to offsets).
   - **Lab, observed:** a probe-by-probe binary search for a CLF time over nginx's 1.4 MB
     `web.stdout.0` (a Python client, one keep-alive connection, probes of 4 KiB, taking the first whole timed
     line of each probe, offsets from `stat`): **10 probes** for the target 24 Oct 09:00, 10 for 25 Oct 01:00
     and 10 for 25 Oct 02:00, each landing within a 2.7 KB window of the right line, **14 to 28 ms** in all
     (2.2 to 3.1 s in total while the node was busy, see item 4). The count is `log2(size / 4 KiB)`: 1.4 MB gives 10,
     **a full 10 MiB file gives 12, and 100 MiB (10 files of 10 MiB) gives 12 probes plus none to choose
     the file** (the file comes from `ls`'s `ModTime` and the previous file's, no probe). A probe took
     1.4 to 2.8 ms on the idle node, so 12 probes are about 30 ms; with the busy node's 200 to 300 ms a
     probe they are 2 to 4 s, so the same deadline applies.
   - **No stream with only untimed lines exists in the lab**: whoami's stderr is one timed Go-log line
     (`2026/10/25 00:16:42 Starting up on port 80`); nginx's start has 2 untimed entrypoint lines then CLF;
     Redis's own shape (`1:M 23 Oct 2026 23:13:18.804`, 6 KB) and Postgres's (`2026-10-24 23:53:36.390 UTC`)
     are timed in other shapes. The nearest case, Redis read as if it had CLF times, gave **no timed line in
     the first probe at all, so the search stops after 1 probe and falls back to offsets** (the middle of
     the file, then the page offers "from the start"/"from the end"): that is the fallback working as the
     plan has it, not a measured untimed app.
   - **Recommendation: ship `around=<time>` in 024.** It is 12 probes, about 30 ms idle; the fallback
     for an untimed stream is cheap and already in the plan. Proposal, not run: a probe that lands
     on an untimed stretch (a stack trace) uses the next timed line within the probe, and a probe with none
     steps forward by one more chunk, at most 4 times, then falls back.
6. **Time zones**: a container with `TZ=Europe/Madrid` writing Go `log` times: the bounds check marks
   its lines `estimated` rather than placing them 1-2 h off.
   - **Lab, observed:** `TZ=Europe/Madrid` had **no effect** in the `busybox:1.37` image (no zoneinfo
     there: the lines carried UTC, `2026/10/25 02:31:15`, the same as the file's `ModTime`
     20:31:15 -06:00), so the job was rerun with the POSIX zone `TZ=CEST-2`. The container wrote
     `2026/10/25 04:38:22 plain go-log style` at a `ModTime` of `2026-10-24T20:38:22-06:00` = **02:38:22
     UTC: the written time is exactly 2 h ahead** (CEST; CET would be 1 h) (the copy's `StartedAt` was `02:32:13Z`), so the plan's bound "a time more than a minute after the
     file's `ModTime`" catches **an east-of-UTC zone** for every line of the file.
   - **Not caught (by reasoning, not run): a west-of-UTC zone** (`America/Denver`, -6 h) writes times
     6 h *early*, and the bounds only reject a time **before the copy's start** or before the previous
     file's `ModTime` by more than a minute: a copy older than 6 h passes it for every line except the
     first 6 h of its life. A stronger check costs nothing: **the last timed line of a file against that
     file's `ModTime`** (the last line was written at about the file's last write): a difference over a
     minute marks that file's lines `estimated` (skew over a minute), in both directions. This is added to
     the plan's decisions.
7. **Policy**: with the scoped token from `deploy/install/caravana-policy.hcl` (as `TestLab` uses), `ls`
   and `readat` on an app's `alloc/logs` succeed (needs `read-fs`, present since 016) and fail on
   Traefik's job only if a deny exists (none does today: Traefik's logs are readable, which 023's R2
   noted as useful).
   - **Lab, observed:** a token made from `deploy/install/caravana-policy.hcl` (policy
     `caravana-spike024`, type client, deleted afterwards; the lab's own `caravana` policy and
     `caravana-lab-test` token were not touched). On an app's copy (`hollo`'s `api` task): `ls` 200, `stat`
     200, `readat` 200, `logs` 200. On **Traefik's allocation**: `ls` 200, `stat` 200, `readat` 200 (it read
     the first bytes of Traefik's log), `logs` 200. No deny exists. With no token the same `ls` answers 403.
8. **ANSI and control bytes end to end**: a job printing SGR, OSC 8, OSC 52, `\r` progress and a NUL:
   the page, `listLogs`, the SSE notice path and Download hold none of the escape bytes.
   - **Lab, observed (job `spike024esc`):** `readat` and `logs?plain=true` returned **the same 73 bytes,
     byte for byte** what the shell printed: `1b 5b 33 31 6d` (SGR) and `1b 5b 30 6d`, the OSC 8 pair
     `1b 5d 38 3b 3b … 1b 5c`, OSC 52 `1b 5d 35 32 3b 63 3b … 07`, the progress line as `31 30 25 0d 32 30 25
     0d 33 30 25 0a` and the NUL as `61 00 62 0a`. `plain=false` carries the same bytes base64-encoded in
     `Data` (one frame, `File: alloc/logs/main.stdout.0`, `Offset: 73`). **Nothing is stripped, folded or
     escaped by Nomad**, so every control byte reaches Caravana and the stripping (R5) is entirely ours.
9. **Where steps go**: a console step `console-logs` in `TestConsoleLab` (an app printing a timed
   `ERROR`, a `[warn]`, a JSON `"level":"error"`, an ANSI-colored line and a repeated line, a deploy
   during the step; filters, around, download) and a gate step `logs` (the API reads with the gate's
   token, a deploy token's 403), as 022 and 023 did.


### Spike findings (2026-10-24)

The lab spike (T001): every item above was run against the Lima lab; the jobs `spike024`, `spike024tz` and
`spike024esc`, the scoped token and its policy were removed afterwards, and the review apps were only read.
What it changes in `plan.md` ("Decisions carried"):

- **`readat` of a missing file answers 200 with the error text** (item 1): `ReadLogAt` checks a short chunk
  with `Stat`; the deploy test fake returns the same shape for a purged file. Added to the plan.
- **Frames' `Offset` is unreliable for a short last frame** (item 1): line ids and `at=` offsets come only
  from Caravana's own `readat` reads; Live lines get `Time` = arrival and no offset id (they take one when
  the next view reads them back). Added to the plan.
- **A per-file skew check** (item 6): the last timed line of a file against that file's `ModTime` (over a
  minute either way: the file's lines are `estimated`), which also catches a west-of-UTC zone that the
  start/`ModTime` bounds can't. Added to the plan.
- **A deadline per page** (item 4): reads are cheap on an idle node (20 MiB in 0.3 s) and 100 times slower on
  a CPU-starved one (a 1 MiB `readat` up to 6.9 s); the page carries what it read in time and says the rest
  was not read. Added to the plan.
- **`around=<time>` ships in 024** (item 5): 12 probes for a 10 MiB file, about 30 ms idle. The plan's
  "R8.5 decides" becomes "ships". Edited in the plan.
- Unchanged: live reads over `ls` + `readat` (item 1: continuing into `n-1` works; files close under 1 MiB
  and don't split lines), the dead copies being readable at least minutes (item 3: they stay listed and
  readable; no plan change, `LogCopies` keeps listing terminal copies), no new capability (item 7: the
  scoped token reads app and Traefik logs), and R5's stripping (item 8: nothing is stripped by Nomad).
- Not measured here: Caravana's RSS, a failed (crash-looping) copy's files, how long until Nomad's server and
  client collect a dead copy (observed 5 min, still there), a stream of only untimed lines.

---

## Summary of decisions

| Question | Decision |
|---|---|
| R1 source | Live reads only: `ls` + `readat` backwards with a budget (1 MiB per stream, 16 MiB per page, 2 at once); `followLogs` only for Live; no ring buffer (lines would put app output and secrets in SQLite; counts-only stays a fallback). Lab: 20 × 64 KiB in 72 ms, 20 × 1 MiB in 126 ms; parse of 20 MiB 128-140 ms (Mac) |
| R1 retention | Nomad's default 10 × 10 MiB per stream (Caravana sets none for app tasks); by size, so hours to months; dead copies readable until GC, with a listing that keeps them |
| R2 time | `timeOf` at fixed positions (start, short prefix, CLF bracket, logfmt, JSON), checked against the copy's start and the file's `ModTime`; no zone means UTC; untimed lines carry the older timed line's time (`estimated`); never "now" for an old line |
| R3 level | After ANSI strip and time removal: JSON keys, then the first 4 words against a table, single-letter prefixes, continuation inherits; unmatched is `info` on stderr too |
| R4 the rest | Fold per stream on text without the time span; around by `<stream>.<file>.<offset>`; service and copy filters before reading; case-insensitive substring search, no regexp; Download oldest first, 8/64 MiB budget, `no-store` |
| R5 ANSI | Strip CSI, OSC, DCS, C1 and C0 (not tab), keep text after a mid-line `\r`; no rendering |
| R6 histogram | 30 bins from the read, unread bins hatched not zero, deploy markers and rows from `CreatedAt`/terminal `UpdatedAt`, inline SVG with attributes and classes; no migration |
| R7 API | `listLogs`, `getLogsAround`, `downloadLogs` (0.26.0); `getLogs` gains `level`, `id`, `timeSource`, stripped `text`; deploy tokens denied by default |
| R8 lab | Rotation and offsets, dead copies' lifetime, host read cost, binary search by time, a non-UTC container, policy, escapes end to end |
