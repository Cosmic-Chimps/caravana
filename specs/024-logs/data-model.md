# Data model: Logs (024)

Nothing is stored. Every entity below is built for one read and discarded; the only store access
is a read of `deployments` (no migration).

## `logline.Line` (one line of a copy's output)

| Field | Type | Notes |
| --- | --- | --- |
| `ID` | `LineID{Copy, Stream, File int, Offset int64}` | the first byte of the line in `alloc/logs/<task>.<stream>.<file>`; stable while Nomad keeps the file; written `<copy>.<stream>.<file>.<offset>` |
| `Time` | `time.Time` (UTC) | read from the line (`TimeOf`), else the older timed line's in the same stream, else the copy's start or the previous file's `ModTime` |
| `TimeSource` | `line` \| `estimated` \| `received` | `received` only for a Live line (arrival time, as today) |
| `App`, `Service`, `Copy` | strings | the copy's short id is the first 8 of its allocation id |
| `Stream` | `stdout` \| `stderr` | |
| `Level` | `error` \| `warn` \| `info` \| `debug` | `LevelOf`, after `StripANSI` and the time span |
| `Text` | string | stripped, the time span removed for folding only (shown with it) |
| `Raw` | string, optional | the line as received when it was JSON (for "Raw") |
| `Repeat` | int | N-1 for a folded row of N; 0 otherwise |

Validation: a line is at most 64 KiB (today's `maxLogLine`); invalid UTF-8 is replaced.

## `service.LogQuery` (what a read is asked)

| Field | Notes |
| --- | --- |
| `Project`, `Environment` | required unless Default › production; a missing one is `*PlaceError` |
| `App`, `Service` (`<app>/<service>`), `Copy` | applied to the copy list before reading |
| `Stream` | `stdout`, `stderr`, `both` (default) |
| `Levels` | set of levels, default error, warn, info |
| `Q` | case-insensitive substring over `Text + " " + App/Service`; ≤ 200 bytes |
| `From`, `To` | RFC 3339; or `Range` in {15m, 1h, 24h, 7d} (default 15m, `To` = now) |
| `Before` | "Show older": the walk starts from this time instead of `To` |
| `Lines` | rows after folding, 1–1000, default 500 |

## `service.LogRead` (one view's answer)

| Field | Notes |
| --- | --- |
| `Lines` | `[]Line`, newest first (per-stream order never changed; streams merged by non-decreasing time) |
| `Deploys` | `[]DeployRow` in the range |
| `Histogram` | 30 `Bin{From, Count, Errors, Read bool, Estimated int}` and `BucketSeconds` |
| `Coverage` | `{From time.Time, Complete bool}`: the oldest time every read stream reached; `Complete` when every stream reached `From` or its oldest file |
| `Copies` | `[]LogCopy{ID, Short, App, Service, Gone bool, Lines int}` for the rail |
| `Dropped` | true when a stream's oldest line read is the oldest Nomad keeps |

## `service.DeployRow`

| Field | Notes |
| --- | --- |
| `DeploymentID`, `App`, `Service` (empty: the whole app) | |
| `At` | `CreatedAt` for `started`; the terminal `UpdatedAt` for the end |
| `Kind` | `started` \| `live` \| `failed` \| `reverted` \| `superseded` |
| `JobVersion`, `RevertedTo` | for the words |
| `Who` | page 4's who-words (`deployWho`) |

## Read budgets (constants in `service`)

| Name | Value |
| --- | --- |
| `logStreamBudget` | 1 MiB per stream for a page read; 8 MiB for a download |
| `logPageBudget` | 16 MiB per page read; 64 MiB per download |
| `logReadsAtOnce` | 2 page reads at once per Caravana; 1 download |
| `logChunk` | 64 KiB per `readat` |
| `logAroundWindow` | 16 KiB before and after the anchor |

## Derived, not stored

- The histogram, the coverage, the copy counts and the first-seen note are computed from the read.
- The service hue is `hues[hash(app + "/" + service) mod 6]` (FNV-1a), the console's one identity
  color (DESIGN.md).
- Live lines are `followLogs` arrivals, `TimeSource` `received`, and are never written anywhere.
