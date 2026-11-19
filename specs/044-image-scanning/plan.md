# Implementation Plan: Image scanning

**Spec**: [spec.md](./spec.md) · **Research**: [research.md](./research.md) · **Tasks**: [tasks.md](./tasks.md)

## Summary

Grype, pinned by digest, runs as a one-off Nomad batch job `_scan-<run>` per image, with its database cached in a
Caravana host volume `caravana-scanner` and a private registry's credentials in the job's own variable. A pure
package, `internal/vulns`, reads the job's result file. The service keeps a queue that runs one scan at a time. It
is fed by healthy deployments, a daily pass, the owner, and (with the gate on) a deploy before Register. It stores one
result per digest, derives the needs-you kind `image_vulnerable`, and prunes the digests nothing runs. Console: a
Security panel on the app's Deployments tab, counts in each history entry, and Operator › Security with the
settings. Migration **0034**, API **0.49.0**, no new Go module, no runtime dependency, no policy change (host volume
create/delete, `read-fs`, variables on `nomad/jobs/*` and `submit-job` already cover it), constitution 1.2.0 → 1.2.1
(patch: II names the new helper job and helper volume).

## Constitution Check

- **I Small**: no service: Grype runs as a batch job only while it scans; nothing new runs on the host. No Go
  module. ✓ The database costs 3.5 GB of disk (research R1); scans are skipped under 4 GiB free (D7).
- **II One model**: no model change. The scan job and the cache volume are Caravana's helpers, named here and in
  the constitution (patch 1.2.1). `nomadjob.ScanJob` is pure, golden-tested. ✓
- **III Safe deploys**: the gate only refuses (before anything is replaced); scans never touch a running copy. ✓
- **IV Fail loudly**: the gate's refusal names `services[i].image` and the finding; a scan that can't be made is a
  warning on the deploy, never silence; failed and skipped scans are recorded with reasons. ✓
- **V API-first**: every page reads `internal/service`; the routes are in `api/openapi.yaml` in the same commit. ✓
- **VI Nomad-native**: the official client, JSON jobs; Nomad code stays in `nomadjob`/`deploy`. The scan job is
  not part of any app, so exports and uninstalls are unchanged. ✓
- **VII Secure**: credentials only in the job's own variable, deleted with the job (sentinel tests); the scanner's
  output is parsed into fixed fields, its stderr is classified and never kept; no telemetry (research R1). ✓
- **VIII Plain Go**: table tests for the parser and rules, goldens, fakes, two lab steps. ✓

## Decisions

**D1 Settings and image.** `nomadjob.Settings.ScannerImage` (empty means `DefaultScannerImage`, Grype v0.117.0,
`anchore/grype@sha256:ddf9e9f204049f3a4a0955ef70873cabab6a31432125ad4f20a490b54950a253`, an index for amd64 and
arm64), set from `CARAVANA_SCANNER_IMAGE` / `-scanner-image` in `internal/config` and checked like
`CARAVANA_SQLITE_IMAGE` (pinned by digest).

**D2 The job (`internal/nomadjob/scan.go`, pure).**
```go
// ScanJobID is the job of scan run runID. It starts with "_", which no app ID does,
// so it is no app's job and has no app's prefix.
func ScanJobID(runID string) string { return "_scan-" + runID }
// ScanVolume is the host volume that keeps the scanner's database. It has no "_",
// so it is no app's volume (<app>_<volume>).
const ScanVolume = "caravana-scanner"
// ScanResultFile is where the task writes its result, in the allocation.
const ScanResultFile = "alloc/data/scan.txt"
const ScanTask = "grype"
// ScanJob scans image (repo@sha256:…) with Grype. private is as for Translate:
// when image comes from one of those registries the task gets the registry's
// credentials from the job's own variable.
func ScanJob(runID, image string, s Settings, private map[string]bool) *api.Job
```
- `batchJob`-like: type batch, meta `caravana.scan` = runID, `caravana.image` = image, no `caravana.app`.
- One group `scan`, `batchGroup` rules (no restart, no reschedule) but `Networks: [{Mode: "host"}]` (research R3:
  the task reaches the registry the way the node's Docker does) and the host volume `ScanVolume` mounted at
  `/cache` (read-write).
- Task `grype`, driver docker, `image` = the scanner, `args` = `["registry:" + image, "-o", "template", "-t",
  "/local/scan.tmpl", "--file", "/" + ScanResultFile]`.
- Env: `GRYPE_DB_CACHE_DIR=/cache`, `GRYPE_CHECK_FOR_APP_UPDATE=false`,
  `GRYPE_DB_UPDATE_CHECK_MAX_FREQUENCY=24h` (one database download a day at most), `GRYPE_DB_MAX_ALLOWED_BUILT_AGE=120h`.
- Template 1: the result template (D3) written to `local/scan.tmpl` with delimiters `[[%` / `%]]`, so Nomad's
  template engine leaves Grype's `{{ }}` alone.
- Template 2, only when the image is on a private registry: the job's own variable rendered as env
  (`variableTemplate`-like, path `nomad/jobs/<job>`), whose items are `GRYPE_REGISTRY_AUTH_AUTHORITY`,
  `GRYPE_REGISTRY_AUTH_USERNAME`, `GRYPE_REGISTRY_AUTH_PASSWORD`. The job holds no value.
- Resources: CPU 500 MHz, memory 512 MB, memory max 2048 MB (`ScanMemoryMB` is exported for D7); log config 2 ×
  1 MB; kill timeout 10 s.
- Goldens `scan` and `scan-private` (JSON) in `testdata/`.

**D3 Result template and parser (`internal/vulns`, pure).** The template prints a header line `caravana-scan v1`, one
tab-separated `m` line per match (ID, severity, fix state, fixed versions joined by `,`, package name, version,
type, the advisory link from `.Vulnerability.DataSource`), and a trailer `db <built> <schema>` (research R2).
Fields have tabs and newlines replaced by spaces in the template (`replace`).
```go
type Severity int // Unknown < Negligible < Low < Medium < High < Critical
func ParseSeverity(string) Severity; func (Severity) String() string // "critical", ...
type Finding struct {
    Advisory, Package, Version, Type, FixState, URL string
    FixedIn  []string
    Severity Severity
}
func (f Finding) Fixable() bool // FixState == "fixed" && len(FixedIn) > 0
type Result struct {
    Findings []Finding  // sorted: severity desc, fixable first, package, advisory
    DBBuilt  time.Time
    Cut      bool       // more than MaxFindings
}
type Counts struct{ Critical, High, Medium, Low, Negligible, Unknown, FixableCritical, FixableHigh int }
const MaxFindings = 20000
const MaxResultBytes = 4 << 20
func Parse(text string) (Result, error) // no header or no trailer: an error (the scan didn't finish)
func (r Result) Counts() Counts
// Template is the text D2 writes to local/scan.tmpl.
const Template = "..."
```
A link that isn't `https://` is dropped (only shown as text). Duplicate (advisory, package, version) lines are
merged. `nomadjob` imports `vulns.Template` (pure to pure).

**D4 Deploy layer (`internal/deploy/scan.go`).**
- `EnsureScanVolume(ctx) (nodeID string, err error)`: create `ScanVolume` with the `mkdir` plugin when it isn't there
  (Nomad picks a node of the default pool), wait until ready, return its node.
- `DeleteScanVolume(ctx)`: delete it (retrying while a scan job holds it, as `DeleteVolumes` does); gone is done.
- `StartScan(ctx, job)`: like `StartScheduleRun`: write the job's variable when its task has an auth block
  (`withPullCredentials` with the three Grype item names), register; a Nomad refusal is `*RunStartError`.
- `ScanState(ctx, jobID)`: `ScheduleRunState(ctx, jobID, nomadjob.ScanTask)`.
- `ScanResult(ctx, allocID) (text string, found bool, err error)`: `readCopyFile` with a limit of
  `vulns.MaxResultBytes + 1` (the limit becomes a parameter).
- `ScanFailure(ctx, allocID) string`: reads the task's last 4 KiB of stderr and returns one of the fixed classes
  `ScanDBDownload`, `ScanDBOld`, `ScanRegistryAuth`, `ScanImageRead`, `ScanOther` from substrings (lab-checked
  in T013); the text itself never leaves the function.
- `PurgeScan(ctx, jobID)`: purge the job and delete its variable.
- `deploytest` gains what these need (volumes create/delete, alloc fs file, stderr log).

**D5 The queue and the runner (`internal/service/scanner.go`).** One goroutine, started in `Service.Start` with the
other watchers. `scan_runs` rows are the queue: `queued` → `running` → `ok` | `failed` | `skipped`.
- `queueScan(ctx, image, trigger)` inserts a `queued` row unless one is queued or running for the image; returns
  the run. Triggers: `deploy`, `daily`, `owner`, `gate`. A `gate` run goes first (`ORDER BY trigger = 'gate'
  DESC, created_at`).
- The loop waits on a wake channel (or 30 s), takes the next queued row and runs it: settings off → `skipped`
  ("scanning is off"); D7's room check → `skipped` with its reason; `EnsureScanVolume`; build the job
  (`ScanJob` with `privateRegistries`); `StartScan`; poll `ScanState` every 5 s (and on the copies wake) until
  terminal or 20 minutes (then `StopScheduleRun`-style deregister and `failed` "took longer than 20 minutes");
  read the result; `vulns.Parse`; store (D6); purge; publish the live notice `scans`; wake waiters.
- Outcomes and their fixed words: `ok`; `failed` with "the vulnerability database couldn't be downloaded", "the
  vulnerability database is more than 5 days old", "the registry refused the credentials", "the image couldn't be
  read from its registry", "the scanner stopped without a result", "Nomad refused the scan", "took longer than 20
  minutes", "Caravana restarted during the scan"; `skipped` with D7's words or "scanning is off".
- At start, `running` rows are failed "Caravana restarted during the scan" and their jobs purged (`_scan-<id>`).
- `ScanWait(ctx, runID)` returns when the run ends (for the gate).

**D6 Store (migration `0034_image_scans.sql`).**
```sql
CREATE TABLE image_scans (          -- one per digest: the newest successful result, and the newest try
  image TEXT PRIMARY KEY,           -- repo@sha256:...
  scanned_at TEXT, db_built TEXT, cut INTEGER NOT NULL DEFAULT 0,
  critical INTEGER NOT NULL DEFAULT 0, high …, medium …, low …, negligible …, unknown …,
  fixable_critical …, fixable_high …,
  tried_at TEXT NOT NULL, status TEXT NOT NULL CHECK (status IN ('ok','failed','skipped')), reason TEXT
);
CREATE TABLE image_findings (
  image TEXT NOT NULL REFERENCES image_scans(image) ON DELETE CASCADE,
  advisory TEXT NOT NULL, package TEXT NOT NULL, version TEXT NOT NULL, type TEXT NOT NULL,
  severity INTEGER NOT NULL, fix_state TEXT NOT NULL, fixed_in TEXT NOT NULL, url TEXT NOT NULL,
  PRIMARY KEY (image, advisory, package, version)
);
CREATE TABLE scan_runs (
  id TEXT PRIMARY KEY, image TEXT NOT NULL,
  trigger TEXT NOT NULL CHECK (trigger IN ('deploy','daily','owner','gate')),
  status TEXT NOT NULL CHECK (status IN ('queued','running','ok','failed','skipped')),
  reason TEXT, created_at TEXT NOT NULL, started_at TEXT, ended_at TEXT
);
CREATE TABLE scan_settings (
  id INTEGER PRIMARY KEY CHECK (id = 1), enabled INTEGER NOT NULL DEFAULT 1,
  notify_level TEXT NOT NULL DEFAULT 'critical' CHECK (notify_level IN ('critical','high')),
  refuse_fixable_critical INTEGER NOT NULL DEFAULT 0, daily_hour INTEGER NOT NULL DEFAULT 4,
  last_daily TEXT
);
INSERT INTO scan_settings (id) VALUES (1);
```
`SaveScanResult(image, result, at)` replaces the findings and counts in one transaction; a failed or skipped try
only updates `tried_at`/`status`/`reason` (the findings stay). Runs keep the newest 200. `PruneImageScans(keep
[]string)` deletes the rest. `DeleteApp` doesn't prune (the service does, D10).

**D7 Room.** Before a run, from the stored host samples (025, 031): the node of the scan volume (or, before it
exists, the node with the most room). Skip with "not enough free memory on server <name> (<n> MB, the scan needs
512 MB)" when total minus reserved memory is under `ScanMemoryMB`, or "not enough free disk on server <name> (<n>
GB free, the scanner's database needs 4 GB)" when the Nomad disk's free space is under 4 GiB. No sample, or one
older than 5 minutes, doesn't block.

**D8 Triggers.**
- Deploy: when a deployment ends healthy (where `notifyDeploy` is called for a healthy end), each of its digests
  whose stored result is older than 24 hours (or absent) is queued `deploy`.
- Daily: the scanner loop checks once a minute; at or after `daily_hour` UTC, when `last_daily` isn't today, it
  queues `daily` every digest of every app's live deployment (newest healthy) whose app isn't stopped, then prunes
  (D10) and writes `last_daily`. A lab hook `RunDailyScansForTests` runs the pass now.
- Owner: `RequestScan(ctx, appID)` queues the live digests of one app, or of every app when `appID` is empty
  (refused with `ErrScanningOff`, 409 `scanning_off`, when off).
- Gate (D9).

**D9 The gate.** In `deployWarned`, after the digests are known (resolved or pinned) and the newest-deploy check,
before secrets: when `refuse_fixable_critical` is on, for each service's digest with no `ok` result younger than
24 hours, queue `gate` and wait (bounded by the deploy's budget); then any `Critical` finding that is fixable fails
the deploy with `*ImageVulnerableError{Field: "services[i].image", Image, Finding}` → 422 `image_vulnerable`,
message "web's image has CVE-2022-37434 (critical) in zlib 1.2.11-r3, fixed in 1.2.12-r2" (+ "and N more"). A
scan that fails, is skipped or is off adds the warning (field `services[i].image`) "web's image wasn't checked for
vulnerabilities: <reason>; deployed without the check." Redeploys, rollbacks, promotions and the webhook pass
through `deployWarned` and are gated the same way.

**D10 Prune.** After the daily pass and after `DeleteApp`: keep the digests of every app's live deployment, of
deployments still pending or running, and of queued or running runs; delete the other `image_scans` rows (findings
cascade).

**D11 Needs-you `image_vulnerable` (`service/attention_scan.go`).** For each app not stopped, each service of its
live deployment, the stored findings of its digest with severity ≥ the notify level and `Fixable()`, grouped by
package: one item, key `image_vulnerable|<app>|<service>|<package>`, level `failed` when one is critical, else
`warning`. Its parts (`ProblemParts`, `sentenceparts.go`): "`zlib` 1.2.11-r3 in web has `CVE-2022-37434`
(critical), fixed in `1.2.12-r2`" plus "and 2 more advisories". `notify.Event` gains `Package`, `PackageVersion`,
`FixedIn`, `Advisory`, `Severity`, `Image`. Add to `AttentionKinds`, the activity feed's words, every switch over
kinds. Scanning off doesn't remove items (they come from stored results, which stay until pruned).

**D12 API 0.49.0 (`internal/api/scans.go`).**
- `GET /v1/apps/{id}/scan` `getAppScan` → `AppScan{app, services: [ServiceScan{service, image, scan: ImageScan|null}],
  enabled}`.
- `GET /v1/apps/{id}/deployments/{deploymentId}/scan` `getDeploymentScan` → the same for that deployment's digests.
- `GET /v1/scans` `listImageScans` → `[ImageScanSummary{image, usedBy:[{app, service}], counts, scannedAt, dbBuilt,
  status, reason, triedAt}]`.
- `POST /v1/scans` `requestScan` body `{app?}` → 202 `{runs:[ScanRun]}`; 409 `scanning_off`.
- `GET /v1/scans/runs` `listScanRuns` (newest 50).
- `GET /v1/scans/settings` / `PUT /v1/scans/settings` `getScanSettings` / `putScanSettings` → `ScanSettings{enabled,
  notifyLevel, refuseFixableCritical, dailyHour}`; turning it off deletes the database volume (D4).
- `ImageScan{image, status, reason, triedAt, scannedAt, dbBuilt, cut, counts: ScanCounts, findings:[Finding{advisory,
  severity, package, version, type, fixState, fixedIn[], url, fixable}]}`.
- Code `image_vulnerable` (422, field) on the deploy routes; attention kind `image_vulnerable`; the webhook
  message's new properties. Deploy tokens: denied by the default deny (the scope walk lists every new route).
- MCP: `get_app_scan` (read tool, `{app}`), in `contracts/tools.md` of 026.5's list and the MCP contract test.

**D13 Console.**
- App › Deployments: a Security panel above the history (`/parts/apps/{id}/security`, refreshed on the `scans`
  notice): per service of the live version, the counts as chips (critical and high in the failed and warning
  tones), "scanned 14:15 UTC · database of Nov 19", a fixable list first, then a `<details>` "All N findings"
  table (advisory linked with `rel="noopener noreferrer"`, severity, package, version, fixed in); a status line for
  failed, skipped, queued or running; "Scan now" (POST `/apps/{id}/security/scan`, 303). Scanning off: "Scanning
  is off" with a link to Operator › Security.
- Each history entry: "1 critical · 9 high" (or "no known vulnerabilities", or nothing when not scanned).
- Operator › Security (`/ops/security`, nav entry with icon `shield`, between Updates and Servers or after
  Updates): the settings form (POST `/ops/security/settings`, 303 `?saved=1`), "Scan all now", the images table
  (image, used by, counts, scanned, status), the recent runs. Forms work without JavaScript; no inline script;
  `tools/ui-css.sh build` if classes change. Every clock time through `clockOf`.

**D14 Docs and notes.** `docs/image-scanning.md` (what is scanned, when, what it costs, privacy, the gate, turning it
off, the difference with Updates), `changelog.d/image-scanning.md`, CLAUDE.md's feature paragraph,
constitution 1.2.1 (II's list gains `_scan-<id>` and the volume `caravana-scanner`).

## Not in 044

Scanning images that aren't deployed (a preview of an image before it is deployed is the gate's job), SBOM export,
license findings, per-finding ignore lists, other scanners, an MCP tool that requests scans, scanning the host.
