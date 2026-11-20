# Implementation Plan: Image scanning

**Spec**: [spec.md](./spec.md) · **Research**: [research.md](./research.md) · **Tasks**: [tasks.md](./tasks.md)

## Summary

The image scanner is an add-on the owner installs (D15), off until then. Installed, Grype runs as one-off Nomad batch
jobs `_scan-<run>`, pinned by digest, all on the one server that holds the Caravana host volume `caravana-scanner`.
That volume keeps the one database of the installation (D16) and a CycloneDX SBOM per scanned digest, so the daily
rescan reads the SBOM instead of pulling the image (D17). A private registry's credentials reach only a first scan,
through the job's own variable. A pure package, `internal/vulns`, reads the job's result file into findings with
stable IDs, one severity scale, EPSS and KEV (D18). The service keeps a queue that runs one scan at a time, fed by
the install, healthy deployments, a daily pass, the owner, and (with the gate on) a deploy before Register. It stores
one result per digest, derives the needs-you kind `image_vulnerable`, and prunes the digests nothing runs. Console: a
Security panel on the app's Deployments tab, counts in each history entry, and Operator › Security with the add-on,
the settings and the lists. Migration **0034**, API **0.49.0**, no new Go module, no runtime dependency, no policy
change (host volume create/delete, `read-fs`, variables on `nomad/jobs/*` and `submit-job` already cover it),
constitution 1.2.0 → 1.2.1 (patch: II names the new helper job and helper volume).

## Constitution Check

- **I Small**: no service: Grype runs as a batch job only while it scans, and only once the owner installed it. No
  Go module. ✓ The database costs 3.5 GB of disk on one server (research R1); scans are skipped under 4 GiB free (D7).
- **II One model**: no model change. The scan job and the volume are Caravana's helpers, named here and in the
  constitution (patch 1.2.1). `nomadjob.ScanJob` is pure, golden-tested. ✓
- **III Safe deploys**: the gate only refuses (before anything is replaced); scans never touch a running copy. ✓
- **IV Fail loudly**: the gate's refusal names `services[i].image` and the finding; a scan that can't be made is a
  warning on the deploy, never silence; failed and skipped scans are recorded with reasons. ✓
- **V API-first**: every page reads `internal/service`; the routes are in `api/openapi.yaml` in the same commit. ✓
- **VI Nomad-native**: the official client, JSON jobs; Nomad code stays in `nomadjob`/`deploy`. The scan job is
  not part of any app, so exports and uninstalls are unchanged. ✓
- **VII Secure**: credentials only in a first scan's own variable, deleted with the job (sentinel tests); rescans
  need none; the scanner's output is parsed into fixed fields, its stderr is classified and never kept; no telemetry
  (research R1); no new token kind. ✓
- **VIII Plain Go**: table tests for the parser and rules, goldens, fakes, two lab steps. ✓

## Decisions

**D1 Settings and image.** `nomadjob.Settings.ScannerImage` (empty means `DefaultScannerImage`, Grype v0.117.0,
`anchore/grype@sha256:ddf9e9f204049f3a4a0955ef70873cabab6a31432125ad4f20a490b54950a253`, an index for amd64 and
arm64), set from `CARAVANA_SCANNER_IMAGE` / `-scanner-image` in `internal/config` and checked like
`CARAVANA_SQLITE_IMAGE` (pinned by digest). Its version (the tag the digest was taken from) is
`nomadjob.DefaultScannerVersion = "0.117.0"`; the version a run reports comes from Grype itself (D3's trailer).

**D2 The job (`internal/nomadjob/scan.go`, pure).**
```go
// ScanJobID is the job of scan run runID. It starts with "_", which no app ID does,
// so it is no app's job and has no app's prefix.
func ScanJobID(runID string) string { return "_scan-" + runID }
// ScanVolume is the host volume that keeps the scanner's database and SBOMs. It has
// no "_", so it is no app's volume (<app>_<volume>).
const ScanVolume = "caravana-scanner"
const ScanResultFile = "alloc/data/scan.txt"
const ScanTask = "grype"
// ScanSource says what a run reads.
type ScanSource struct {
    Image   string // repo@sha256:…, the digest scanned
    FromSBOM bool  // read the kept SBOM (D17) instead of the registry
}
type ScanOptions struct {
    NodeID      string // the node that holds ScanVolume (D16)
    DatabaseURL string // Grype's update URL; empty: Grype's own (D16)
    UpdateOnly  bool   // the install's run: download the database, scan nothing (D15)
}
func ScanJob(runID string, src ScanSource, opt ScanOptions, s Settings, private map[string]bool) *api.Job
// SBOMPath is where the SBOM of image is kept, inside the task: /cache/sbom/<hex of the digest>.cdx.json.
func SBOMPath(image string) string
```
- Type batch, meta `caravana.scan` = runID, `caravana.image` = image, no `caravana.app`.
- One group `scan`, `batchGroup` rules (no restart, no reschedule) on Docker's bridge like every other job, with the
  host volume `ScanVolume` mounted at `/cache` (read-write) and a constraint `${node.unique.id} = NodeID` (D16). Only a
  registry scan of an image whose registry is on the node's loopback (`registry.IsLoopback`) gets the host's network,
  on the group and in the docker config (`network_mode = "host"`; research R5, and the review of 2026-11-19: on the
  host's network the scanner could reach every service on loopback, so nothing else gets it).
- Task `grype`, driver docker, `image` = the scanner. `args`:
  - registry scan: `["registry:" + image, "-o", "template=/" + ScanResultFile, "-t", "/local/scan.tmpl",
    "-o", "cyclonedx-json=" + SBOMPath(image)]` (Grype writes several outputs in one run; T013 checks it in the lab);
  - SBOM rescan: `["sbom:" + SBOMPath(image), "-o", "template=/" + ScanResultFile, "-t", "/local/scan.tmpl"]`;
  - update only: `["db", "update"]`, then the outcome is the exit code and `db status` is read on the next scan.
- Env: `GRYPE_DB_CACHE_DIR=/cache/db`, `GRYPE_CHECK_FOR_APP_UPDATE=false`,
  `GRYPE_DB_UPDATE_CHECK_MAX_FREQUENCY=24h` (one database download a day at most), `GRYPE_DB_MAX_ALLOWED_BUILT_AGE=120h`,
  and `GRYPE_DB_UPDATE_URL` when `DatabaseURL` is set.
- Template 1: the result template (D3) written to `local/scan.tmpl` with delimiters `[[%` / `%]]`, so Nomad's
  template engine leaves Grype's `{{ }}` alone.
- Template 2, only for a registry scan of an image on a private registry: the job's own variable rendered as env,
  items `GRYPE_REGISTRY_AUTH_AUTHORITY`, `GRYPE_REGISTRY_AUTH_USERNAME`, `GRYPE_REGISTRY_AUTH_PASSWORD`. The job holds
  no value.
- Resources: CPU 500 MHz, memory 512 MB, memory max 2048 MB (`ScanMemoryMB` exported for D7); log config 2 × 1 MB;
  kill timeout 10 s.
- Goldens `scan`, `scan-private`, `scan-sbom`, `scan-update` and `scan-loopback` (JSON) in `testdata/`.

**D3 Result template and parser (`internal/vulns`, pure).** The template prints a header line `caravana-scan v1`,
one tab-separated `m` line per match, and a trailer `db <built> <schema> <grype version>` (`.Descriptor.Version`).
An `m` line has: advisory ID, Grype's severity, fix state, fixed versions joined by `,`, package name, version, type,
the advisory link (`.Vulnerability.DataSource`), the related CVE IDs joined by `,` (`.RelatedVulnerabilities`), the
EPSS score and percentile of the first EPSS entry (`.Vulnerability.EPSS`, empty when none), and `kev` when
`.Vulnerability.KnownExploited` has an entry (field names checked against v0.117.0's template model in the lab;
missing ones are left empty, never invented). Fields have tabs and newlines replaced by spaces. Nothing else is
printed: no description, no image label (FR-010).
```go
type Severity int // Info < Low < Medium < High < Critical
func FromGrype(string) Severity // "Negligible" and "Unknown" are Info
func (Severity) String() string // "critical", "high", "medium", "low", "info"
type Finding struct {
    ID         string    // D18
    Advisory   string    // Grype's ID: CVE-…, GHSA-…, ALPINE-…
    CVE        string    // the advisory when it is a CVE, else the first related CVE, else ""
    Package, Version, Type, FixState, URL string
    FixedIn    []string
    Severity   Severity
    EPSS       *EPSS     // nil when Grype gave none
    KEV        bool
}
type EPSS struct{ Score, Percentile float64 }
func (f Finding) Fixable() bool       // FixState == "fixed" && len(FixedIn) > 0
func (f Finding) Remediation() string // "Upgrade zlib to 1.2.12-r2 or later and rebuild the image." /
                                      // "No fix is known yet." / "The distribution won't fix it."
type Result struct {
    Findings []Finding // sorted: severity desc, fixable first, KEV first, package, advisory
    DBBuilt  time.Time
    DBSchema string
    Scanner  string    // Grype's version, from the trailer
    Cut      bool      // more than MaxFindings
}
type Counts struct{ Critical, High, Medium, Low, Info, FixableCritical, FixableHigh, KEV int }
const MaxFindings = 20000
const MaxResultBytes = 4 << 20
func Parse(image, text string) (Result, error) // no header or no trailer: an error (the scan didn't finish)
func (r Result) Counts() Counts
const Template = "..."
```
A link that isn't `https://` is dropped. Duplicate (advisory, package, version) lines are merged. `nomadjob` imports
`vulns.Template` (pure to pure).

**D4 Deploy layer (`internal/deploy/scan.go`).**
- `EnsureScanVolume(ctx) (nodeID string, err error)`: create `ScanVolume` with the `mkdir` plugin when it isn't there
  (Nomad picks a node of the default pool), wait until ready, return its node; when it exists, return its node.
- `DeleteScanVolume(ctx)`: delete it (retrying while a scan job holds it, as `DeleteVolumes` does); gone is done.
- `StartScan(ctx, job, private)`: like `StartScheduleRun`: write the job's variable when a template of the job reads it
  (a registry scan of a private image; the three Grype items from `nomadjob.ScanRegistryItems`), register; a Nomad refusal is `*RunStartError`.
- `ScanState(ctx, jobID)`: `ScheduleRunState(ctx, jobID, nomadjob.ScanTask)`.
- `ScanResult(ctx, allocID) (text string, found bool, err error)`: `readCopyFile` with a limit of
  `vulns.MaxResultBytes + 1` (the limit becomes a parameter).
- `ScanFailure(ctx, allocID) string`: reads the task's last 4 KiB of stderr and returns one of the fixed classes
  `ScanDBDownload`, `ScanDBOld`, `ScanRegistryAuth`, `ScanImageRead`, `ScanSBOMMissing`, `ScanOther` from substrings
  (lab-checked); the text itself never leaves the function.
- `PurgeScan(ctx, jobID)`: purge the job and delete its variable.
- `StopScan(ctx, jobID)`: deregister without purge (the remove and the 20-minute limit).
- `deploytest` gains what these need (volumes create/delete, alloc fs file, stderr log).

**D5 The queue and the runner (`internal/service/scanner.go`).** One goroutine, started in `Service.Start` with the
other watchers; it does nothing while the add-on isn't `installed` (D15). `scan_runs` rows are the queue: `queued` →
`running` → `ok` | `failed` | `skipped`.
- `queueScan(ctx, image, trigger)` inserts a `queued` row unless one is queued or running for the image; returns the
  run; refused with `ErrScannerNotInstalled` when the add-on isn't installed. Triggers: `install`, `deploy`, `daily`,
  `owner`, `gate`. A `gate` run goes first (`ORDER BY trigger = 'gate' DESC, created_at`).
- The loop waits on a wake channel (or 30 s), takes the next queued row and runs it: D7's room check → `skipped`
  with its reason; `EnsureScanVolume` (its node is the job's constraint, D16); the source: the kept SBOM for `daily`
  when the digest's stored result says one is kept (D17), else the registry; build the job (`ScanJob` with
  `privateRegistries`); `StartScan`; poll `ScanState` every 5 s (and on the copies wake) until terminal or 20
  minutes (then `StopScan` and `failed` "took longer than 20 minutes"); read the result; `vulns.Parse`; store (D6);
  purge; publish the live notice `scans`; wake waiters. A `daily` run from an SBOM that fails `ScanSBOMMissing` is
  queued again from the registry.
- Outcomes and their fixed words: `ok`; `failed` with "the vulnerability database couldn't be downloaded or loaded", "the
  vulnerability database is more than 5 days old", "the registry refused the credentials", "the image couldn't be
  read from its registry", "the scanner stopped without a result", "Nomad refused the scan", "took longer than 20
  minutes", "Caravana restarted during the scan", "the image scanner was removed"; `skipped` with D7's words.
- At start, `running` rows are failed "Caravana restarted during the scan" and their jobs purged (`_scan-<id>`).
- `ScanWait(ctx, runID)` returns when the run ends (for the gate).

**D6 Store (migration `0034_image_scans.sql`).**
```sql
CREATE TABLE addons (               -- D15; one row per add-on the owner installed or tried to
  name TEXT PRIMARY KEY,            -- 'image-scanner'
  state TEXT NOT NULL CHECK (state IN ('installing','installed','failed','removing')),
  version TEXT NOT NULL,            -- the version Caravana pins
  installed_at TEXT, updated_at TEXT NOT NULL, reason TEXT
);
CREATE TABLE image_scans (          -- one per digest: the newest successful result, and the newest try
  image TEXT PRIMARY KEY,           -- repo@sha256:...
  scanned_at TEXT, db_built TEXT, db_schema TEXT, scanner TEXT, cut INTEGER NOT NULL DEFAULT 0,
  sbom_kept INTEGER NOT NULL DEFAULT 0,
  critical INTEGER NOT NULL DEFAULT 0, high …, medium …, low …, info …,
  fixable_critical …, fixable_high …, kev …,
  tried_at TEXT NOT NULL, status TEXT NOT NULL CHECK (status IN ('ok','failed','skipped')), reason TEXT
);
CREATE TABLE image_findings (
  id TEXT NOT NULL,                 -- D18
  image TEXT NOT NULL REFERENCES image_scans(image) ON DELETE CASCADE,
  advisory TEXT NOT NULL, cve TEXT NOT NULL, package TEXT NOT NULL, version TEXT NOT NULL, type TEXT NOT NULL,
  severity TEXT NOT NULL CHECK (severity IN ('critical','high','medium','low','info')),
  fix_state TEXT NOT NULL, fixed_in TEXT NOT NULL, url TEXT NOT NULL,
  epss REAL, epss_percentile REAL, kev INTEGER NOT NULL DEFAULT 0,
  first_seen TEXT NOT NULL, last_seen TEXT NOT NULL,
  PRIMARY KEY (image, id)
);
CREATE TABLE scan_runs (
  id TEXT PRIMARY KEY, image TEXT NOT NULL,          -- '' for the install's database run
  trigger TEXT NOT NULL CHECK (trigger IN ('install','deploy','daily','owner','gate')),
  source TEXT NOT NULL CHECK (source IN ('registry','sbom','database')),
  status TEXT NOT NULL CHECK (status IN ('queued','running','ok','failed','skipped')),
  reason TEXT, scanner TEXT, db_built TEXT, created_at TEXT NOT NULL, started_at TEXT, ended_at TEXT
);
CREATE TABLE scan_settings (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  notify_level TEXT NOT NULL DEFAULT 'critical' CHECK (notify_level IN ('critical','high')),
  refuse_fixable_critical INTEGER NOT NULL DEFAULT 0, daily_hour INTEGER NOT NULL DEFAULT 4,
  database_url TEXT NOT NULL DEFAULT '', last_daily TEXT
);
INSERT INTO scan_settings (id) VALUES (1);
```
`SaveScanResult(image, result, sbomKept, at)` in one transaction: upserts each finding by `(image, id)` keeping
`first_seen` and setting `last_seen = at`, deletes the findings this result doesn't have, writes the counts. A failed
or skipped try only updates `tried_at`/`status`/`reason` (the findings stay). Runs keep the newest 200.
`PruneImageScans(keep []string)` deletes the rest. `ForgetScans()` deletes every scan, finding and run (D15).

**D7 Room.** Before a run, from the stored host samples (025, 031) of the scanner's node (D16), or before the volume
exists the node with the most room: skip with "not enough free memory on server <name> (<n> MB, the scan needs 512
MB)" when total minus reserved memory is under `ScanMemoryMB`, or "not enough free disk on server <name> (<n> GB free,
the scanner's database needs 4 GB)" when the Nomad disk's free space is under 4 GiB. No sample, or one older than 5
minutes, doesn't block. The install checks the same and fails with the same words.

**D8 Triggers.**
- Install: D15 queues every live digest after the database run.
- Deploy: when a deployment ends healthy (where `notifyDeploy` is called for a healthy end), each of its digests whose
  stored result is older than 24 hours (or absent) is queued `deploy`.
- Daily: the scanner loop checks once a minute; at or after `daily_hour` UTC, when `last_daily` isn't today, it
  queues `daily` every digest of every app's live deployment (newest healthy) whose app isn't stopped, then prunes
  (D10) and writes `last_daily`. A lab hook `RunDailyScansForTests` runs the pass now.
- Owner: `RequestScan(ctx, appID)` queues the live digests of one app, or of every app when `appID` is empty.
- Gate (D9).

**D9 The gate.** Offered only while the add-on is installed (a setting saved while it isn't is refused with
`ErrScannerNotInstalled`). In `deployWarned`, after the digests are known (resolved or pinned) and the newest-deploy
check, before secrets: when `refuse_fixable_critical` is on and the add-on is installed, for each service's digest with
no `ok` result younger than 24 hours, queue `gate` and wait (bounded by the deploy's budget); then any critical finding
that is fixable fails the deploy with `*ImageVulnerableError{Field: "services[i].image", Image, Finding}` → 422
`image_vulnerable`, message "web's image has CVE-2022-37434 (critical) in zlib 1.2.11-r3, fixed in 1.2.12-r2" (+ "and
N more"). A scan that fails or is skipped adds the warning (field `services[i].image`) "web's image wasn't checked for
vulnerabilities: <reason>; deployed without the check." Redeploys, rollbacks, promotions and the webhook pass through
`deployWarned` and are gated the same way.

**D10 Prune.** After the daily pass and after `DeleteApp`: keep the digests of every app's live deployment, of
deployments still pending or running, and of queued or running runs; delete the other `image_scans` rows (findings
cascade). Their SBOMs stay on the volume until the next install (they are small, and a later deploy of the same digest
reuses them); D17's rescan reads one only when the stored result says it is kept.

**D11 Needs-you `image_vulnerable` (`service/attention_scan.go`).** Only while the add-on is installed. For each app not
stopped, each service of its live deployment, the stored findings of its digest with severity ≥ the notify level and
`Fixable()`, grouped by package: one item, key `image_vulnerable|<app>|<service>|<package>`, level `failed` when one is
critical, else `warning`. Its parts (`ProblemParts`, `sentenceparts.go`): "`zlib` 1.2.11-r3 in web has
`CVE-2022-37434` (critical), fixed in `1.2.12-r2`" plus "and 2 more advisories", and "known exploited" when one is
KEV. `notify.Event` gains `Package`, `PackageVersion`, `FixedIn`, `Advisory`, `Severity`, `Image`. Add to
`AttentionKinds`, the activity feed's words, every switch over kinds. When the add-on is removed, the notifier drops
its `sent_problems` rows of this kind without sending "resolved" (`diffProblems` is told the kind isn't judged, as
for the copy kinds while Nomad can't be read, and the rows are deleted by `ForgetScans`).

**D12 API 0.49.0 (`internal/api/scans.go`, `addons.go`).**
- `GET /v1/addons` `listAddons` → `[Addon{name, title, state, version, installedAt, reason, cost:{diskBytes,
  downloadBytesPerDay}}]` (the image scanner is listed also when not installed, state `available`).
- `PUT /v1/addons/image-scanner` `installAddon` → 202 `Addon` (`installing`); 409 `addon_busy` while installing or
  removing. `DELETE /v1/addons/image-scanner` `removeAddon` → 202 `Addon` (`removing`). Another name is 404
  `addon_not_found`.
- `GET /v1/apps/{id}/scan` `getAppScan` → `AppScan{app, installed, services: [ServiceScan{service, image, servers[],
  scan: ImageScan|null}]}` (`servers` from the app's copies, for FR-010's asset IDs).
- `GET /v1/apps/{id}/deployments/{deploymentId}/scan` `getDeploymentScan` → the same for that deployment's digests.
- `GET /v1/scans` `listImageScans` → `[ImageScanSummary{image, usedBy:[{app, service, servers[]}], counts, scannedAt,
  dbBuilt, scanner, status, reason, triedAt}]`.
- `POST /v1/scans` `requestScan` body `{app?}` → 202 `{runs:[ScanRun]}`.
- `GET /v1/scans/runs` `listScanRuns` (newest 50).
- `GET /v1/scans/settings` / `PUT /v1/scans/settings` → `ScanSettings{notifyLevel, refuseFixableCritical, dailyHour,
  databaseUrl}` (`databaseUrl` `https://` or empty, a field error otherwise).
- `ImageScan{image, status, reason, triedAt, scannedAt, dbBuilt, dbSchema, scanner, cut, sbomKept, counts: ScanCounts,
  findings:[Finding{id, advisory, cve, severity, package, version, type, fixState, fixedIn[], fixable, url, epss:
  {score, percentile}|null, kev, remediation, firstSeen, lastSeen}]}`.
- Codes: `image_vulnerable` (422, field) on the deploy routes, `scanner_not_installed` (409) on the scan routes that
  act and on the settings PUT turning the gate on, `addon_busy`, `addon_not_found`; attention kind `image_vulnerable`;
  the webhook message's new properties. Deploy tokens: denied by the default deny (the scope walk lists every new
  route). No new token kind.
- MCP: `get_app_scan` (read tool, `{app}`), in 026.5's tool list and the MCP contract test.

**D13 Console.**
- Operator › Security (`/ops/security`, nav entry with icon `shield` after Updates):
  - not installed: what the scanner does, what it costs (about 3.5 GB of disk on one server, about 204 MB downloaded
    a day, 512 MB of memory while it scans), what it sends nowhere, and "Install the image scanner" (POST
    `/ops/security/install`, 303); installing: a progress line refreshed on the `scans` notice; failed: the reason
    and "Try again";
  - installed: the add-on line (version, server, database date) with "Remove…" (a confirmation page
    `/ops/security/remove`, then POST), the settings form (POST `/ops/security/settings`, 303 `?saved=1`), "Scan all
    now", the images table (image, used by, counts, scanned, status), the recent runs.
- App › Deployments: a Security panel above the history (`/parts/apps/{id}/security`, refreshed on the `scans`
  notice): not installed, "The image scanner isn't installed" with a link to Operator › Security; installed, per
  service of the live version, the counts as chips (critical and high in the failed and warning tones), "scanned 14:15
  UTC · database of Nov 19", a fixable list first, then a `<details>` "All N findings" table (advisory linked with
  `rel="noopener noreferrer"`, severity, KEV, package, version, fixed in); a status line for failed, skipped, queued
  or running; "Scan now" (POST `/apps/{id}/security/scan`, 303).
- Each history entry: "1 critical · 9 high" (or "no known vulnerabilities", or nothing when not scanned).
- Forms work without JavaScript; no inline script; `tools/ui-css.sh build` if classes change. Every clock time
  through `clockOf`.

**D14 Docs and notes.** `docs/image-scanning.md` (installing and removing, what is scanned, when, what it costs,
privacy, the mirror URL, the gate, the difference with Updates), `changelog.d/image-scanning.md`, CLAUDE.md's feature
paragraph, constitution 1.2.1 (II's list gains `_scan-<id>` and the volume `caravana-scanner`).

**D15 The add-on (the maintainer, 2026-11-19).** The scanner is off until the owner installs it. `service/addons.go`:
- `Addons(ctx)` lists the known add-ons (today one, `image-scanner`, title "Image scanner") with their row or
  `available`. A small record, not a plugin system: a later add-on adds a name and its own install and remove.
- `InstallImageScanner(ctx)`: refused while `installing` or `removing` (`ErrAddonBusy`); writes `installing` with the
  pinned version; then in a goroutine: D7's room check, `EnsureScanVolume`, a `database` run (`ScanOptions.UpdateOnly`,
  trigger `install`, image `''`) through the same runner; ok → `installed` (with `installed_at`) and every live digest
  queued `install`; failed → `failed` with the run's reason. A process restart during `installing` settles it
  `failed` "Caravana restarted during the install".
- `RemoveImageScanner(ctx)`: writes `removing`; empties the queue (queued runs `failed` "the image scanner was
  removed"), stops a running scan job (`StopScan`, then purge), `DeleteScanVolume`, `ForgetScans`, deletes the row
  (so it lists as `available`); a failure leaves `failed` with the reason and Remove can be tried again.
- The add-on's database date comes from the newest successful scan (the install's `db update` reports none), so
  it shows once the install's first scans end (research R5).
- While not `installed`: the runner idles, `queueScan` refuses, the deploy hook (D8) and the daily pass do nothing,
  the gate is off whatever the setting says, `image_vulnerable` isn't derived, and nothing is pulled or downloaded.

**D16 One database per installation (the maintainer, 2026-11-19).** The volume is made once, on one node Nomad picks
(the default pool), and `EnsureScanVolume` returns that node; every scan job carries the constraint
`${node.unique.id} = <that node>` (Nomad would place a job that needs the volume there anyway; the constraint says it
in the job). Three servers download one database a day, not three. Settings' `databaseUrl` is passed as
`GRYPE_DB_UPDATE_URL` (the `latest.json` listing of a mirror); empty means Anchore's. A server that is down holds the
scanner: runs fail with Nomad's placement, and D7 reports the stale samples; moving the scanner is Remove then Install.

**D17 Rescans from a kept SBOM (the maintainer, 2026-11-19).** A registry scan also writes the image's CycloneDX SBOM
(`-o cyclonedx-json=<SBOMPath>`) on the volume, one file per digest; the result's `sbom_kept` says so. The daily rescan
of a digest with a kept SBOM runs `grype sbom:<path>`: no pull, no registry credentials, no variable. Deploy, owner,
gate and install scans read the registry (a new digest has no SBOM; the owner's Scan now is a full scan). If the lab
shows that an SBOM rescan loses findings the image scan has (research R4, T013), the daily rescan reads the registry and
research.md says why.

**D18 Findings for a posture reader (the `banana` project, later).** A finding's `id` is the first 16 hex digits of
SHA-256 over `grype|<advisory>|<package>|<version>|<type>|<image>`: the check and the asset, the same on every run.
`first_seen` and `last_seen` are kept across scans (D6). The asset of a finding is its digest; reads add the apps,
services and servers that run it (D12). One severity scale (D3). The fields are D3's `Finding`; nothing app-written.
No new token kind: an outside reader's credentials are Bella Baxter's token exchange, not this feature's.

## Not in 044

Scanning images that aren't deployed (a preview before a deploy is the gate's job); an OCSF Vulnerability Finding
export (D18 keeps every field one needs; it is a mapping and a route to add later, not worth a schema version and its
tests now); SBOM export through the API; license findings; per-finding ignore lists; other scanners or a version
picker; other add-ons (Beszel, task 31, reuses D15's record); an MCP tool that requests scans; scanning the host.
