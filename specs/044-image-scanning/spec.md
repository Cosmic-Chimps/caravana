# Feature Specification: Image scanning (known vulnerabilities in the images Caravana runs)

**Feature Branch**: `alta/gallant-helm-7qwv` (worktree; CodeAlta task `z57f`)
**Created**: 2026-11-19
**Status**: Draft
**Input**: CodeAlta task `z57f` ("11 · Scan deployed images for known vulnerabilities (CVEs)", raised by the
maintainer on 2026-11-19): scan every deployed image with a pinned open-source scanner run as a Nomad batch job,
after each deploy that brings a new digest and once a day; show the findings per app and on the Operator view; a
needs-you item and a notification for a new fixable critical; an optional gate that refuses such an image; an API
and an MCP read tool.

**Changes of 2026-11-19** (the maintainer, relayed by the `caravana` session with the `banana` session's needs):
the scanner is an add-on the owner installs, off until then (US0); one database per installation, with an optional
mirror URL (US6); daily rescans from a kept SBOM, with no pull and no credentials (US3); findings recorded with
stable IDs, first and last seen, one severity scale, EPSS and KEV, and the scanner's version (FR-010).

## Why

Caravana deploys images by digest and never looks inside them. An app can run for months on an image whose OS
packages or libraries have known vulnerabilities, some with a fix one rebuild away, and the owner isn't told.
Advisories are published every day, so an image that was clean when it was deployed can stop being clean without
anyone touching it.

## User Scenarios & Testing

### User Story 0 — The owner installs the image scanner, or removes it (Priority: P1)

The image scanner is an add-on, off until the owner installs it. Operator › Security says what it costs (about
3.5 GB of disk on one server and about 204 MB downloaded a day) and has "Install the image scanner". Installing
creates the `caravana-scanner` volume on one server and downloads the database there; the add-on's record says
installing, installed (with the scanner's version and the database's date) or failed (with the reason). The scanner
is the one `CARAVANA_SCANNER_IMAGE` pins: there is no version to pick.

**Acceptance scenarios**
1. Given the add-on isn't installed, then no scanner image is pulled, no volume exists, nothing is downloaded, no
   scan is queued or run, the deploy gate isn't offered, `image_vulnerable` is never raised, and the app's
   Security panel says "The image scanner isn't installed" with a link to Operator › Security.
2. Given the owner installs it, then the volume is made, the database is downloaded, the add-on is `installed`,
   and every digest a running app uses is queued for a scan.
3. Given the owner removes it, then the queue is emptied (a running scan is stopped), the daily pass stops, the
   volume and its database and SBOMs are deleted, the stored results and runs are deleted, and the
   `image_vulnerable` items go away without "resolved" notifications.

### User Story 1 — A deploy is scanned and its findings are shown (Priority: P1)

The owner deploys `web` with an image whose zlib has a known critical vulnerability with a fix. A few minutes after
the deployment is live, the app's Deployments tab has a Security panel: the live version's images with counts by
severity (critical, high, medium, low), and for each finding the package, the installed version, the version that
fixes it (when there is one) and the advisory ID linked to its page. The history entry of that deployment says
"1 critical · 9 high". Operator › Security lists every image the installation runs with the same counts.

**Independent test**: the lab's gate step `image-scan`: deploy `alpine:3.14.0` (busybox httpd); the app's scan has
`CVE-2022-37434` in `zlib` 1.2.11-r3, critical, fixed in 1.2.12-r2, with its link; `GET /v1/scans` lists the
image with the app.

**Acceptance scenarios**
1. Given the image scanner is installed, when a deployment that brought a digest not scanned in the last 24 hours ends healthy,
   then a scan of that digest is queued and its result is attached to the digest (and so to every deployment and
   app that runs it).
2. Given a scan in progress, when another digest is queued, then it waits: one scan runs at a time in the whole
   installation.
3. Given a scan that failed (the database couldn't be downloaded, the registry refused, the image couldn't be
   read), then the page shows the reason in Caravana's words and the last successful result with its date, and
   no scanner output is shown, stored or logged.
4. Given a deployment whose digests no app runs any more, then its history entry shows no counts ("not kept:
   this image no longer runs").

### User Story 2 — A fixable critical reaches the owner, and a fixed image resolves it (Priority: P1)

**Independent test**: the gate step: after the scan of `alpine:3.14.0`, `/v1/attention` has an
`image_vulnerable` item naming the app, the service, `zlib` and the fixed version, and a notification channel
receives `problem.opened` for it; a deploy of `alpine:3.14.10` (zlib fixed) makes the item go away and
`problem.resolved` is sent.

**Acceptance scenarios**
1. Given a live image with a finding whose severity is at or above the notify level (critical by default, or
   high) and whose fix is known, then the needs-you list has one `image_vulnerable` item per app, service and
   package, naming the package, its version, the fixed version and the advisory (and "and N more" when the
   package has several).
2. Given findings without a fix, then they are listed on the pages but open no item.
3. Given an item, when a deploy brings an image whose package has no such finding, then the item is gone at the
   next read, and the notifier sends `problem.resolved` as for every kind.
4. Given a stopped app, then its findings open no item.

### User Story 3 — The daily rescan finds a new advisory for an image that didn't change (Priority: P1)

Once a day, at an hour the owner can change (04:00 UTC by default), every digest a running app uses is scanned
again with the refreshed database. The first scan of a digest keeps a CycloneDX SBOM of it on the scanner's volume,
and the daily rescan reads that SBOM: no image pull and no registry credentials (a digest without a kept SBOM is
scanned from its registry, as the first time).

**Independent test**: the gate step: one stored finding of the running image is dropped (a lab-only hook stands in
for "the advisory didn't exist yet"); the daily pass, run by the lab hook, scans the unchanged digest and the
finding is back, with the needs-you item opened again.

### User Story 4 — A private image is scanned with the registry's credentials (Priority: P1)

**Independent test**: the gate step deploys an image from the lab's password-protected registry; its scan succeeds;
the password (a sentinel) appears in no answer, page, log line, Nomad job or SQLite file.

### User Story 5 — The owner can refuse to deploy a fixable critical (Priority: P2)

Operator › Security has "Refuse to deploy an image with a fixable critical vulnerability", off by default. With it
on, a deploy scans each image it brings that has no scan younger than 24 hours before Nomad gets the job, and an
image with a fixable critical fails the deploy with 422 `image_vulnerable`, field `services[i].image`, and a message
naming the advisory, the package and the fixed version. A scan that can't be made doesn't block: the deploy goes on
with a warning saying the image wasn't checked.

### User Story 6 — One database, a mirror, offline or short of room (Priority: P2)

1. One database per installation: on a cluster the scanner's volume is on one server and every scan job runs
   there, so three servers still download 204 MB a day, not 600.
2. An optional database URL (Grype's update URL, `https://`) lets an owner mirror the database instead of
   fetching it from Anchore.
3. A scan is skipped, and recorded as skipped with the reason, while the server that would run it has less free
   memory than the scan reserves or less than 4 GiB of free disk.
4. Without network the scanner uses the database it has for up to 5 days (it says so in the result's date); after
   that scans fail with "the vulnerability database is more than 5 days old".

### Edge cases

- An image reference Caravana runs that isn't pinned (it always is, constitution III) is never scanned by tag.
- A scan result larger than 4 MiB keeps its first findings by severity and says it was cut.
- A digest used by several apps is scanned once; its result is shown on each.
- Deleting an app removes the scans of the digests no other app runs.
- The image scanner removed while a scan runs: the scan is stopped and settled "the image scanner was removed".
- Caravana restarting during a scan: the run is settled as failed ("Caravana restarted during the scan") and its
  job purged; queued scans stay queued.

## Requirements

- **FR-001** The scanner is Grype (research R1), pinned by digest, run as a Nomad batch job named `_scan-<run ID>`
  with no app; its database and the kept SBOMs are on a Caravana host volume `caravana-scanner` on one server, and
  every scan job runs on that server.
- **FR-002** The scanner is an add-on (US0): an add-on record (name, state, version, when installed, the reason of a
  failure) that the owner installs and removes; nothing of FR-003 to FR-011 happens while it isn't installed.
- **FR-003** Scans are queued (install, deploy, daily, owner, gate) and run one at a time; each run is recorded with
  the scanner's exact version and the database's build date.
- **FR-004** The result per digest is stored and replaced by each successful scan (FR-010 says what a finding
  holds). Results of digests no app's live deployment runs are pruned after the daily pass and when an app is
  deleted.
- **FR-005** Needs-you kind `image_vulnerable` (key `image_vulnerable|<app>|<service>|<package>`), notified like
  every kind, with the package, versions, advisory and severity in the event.
- **FR-006** Settings: notify level (critical | high), refuse fixable criticals (off), the daily hour (UTC), the
  database URL (empty: Anchore's).
- **FR-007** API: install and remove the add-on; read an app's scan, a deployment's scan, the list of images, the
  runs; read and write the settings; request a scan. MCP read tool `get_app_scan`. Deploy tokens are denied by the
  default deny. No new token kind.
- **FR-008** Credentials of a private registry reach the scan only through the job's own Nomad Variable, deleted
  with the job; no page, log, job, answer or row holds them. A rescan from a kept SBOM needs none.
- **FR-009** No data about the host or its apps is sent anywhere: the scanner's update check is off, it has no
  telemetry, and it downloads only its database (from Anchore's public listing, or the owner's mirror) and the
  image's own layers (from the image's registry).
- **FR-010** A finding, for a later read-only security-posture reader (the `banana` project) to stay cheap:
  - a stable ID (the check, the advisory with the package and its version, plus the asset, the digest), the same on
    every run, with the first and the last time a scan saw it;
  - its asset IDs: the image digest; the apps and the servers that run it are given with it on reads;
  - one severity scale: critical, high, medium, low, info (Grype's negligible and unknown are info);
  - the advisory ID, the CVE ID (the advisory itself, or the CVE it is related to), the package, its installed
    version, its type, the fix state, the fixed versions, EPSS (score and percentile) and KEV (known exploited) when
    Grype gives them, a remediation sentence in Caravana's words and the reference link;
  - nothing an app wrote (image labels, descriptions of packages): only what the scanner's database says and the
    package's name and version.
- **FR-011** An OCSF Vulnerability Finding export is not in 044 (plan, "Not in 044"); FR-010's fields are enough
  to write one later.

## The Updates page and the Security page (decided)

027's Updates page counts the host's OS updates and names no vulnerability: those are the server's packages,
fixed by `apt` (or unattended-upgrades), and naming CVEs there would only repeat what `pro` says when it is asked.
The Security page is about what the owner deploys: the fix is to rebuild or bump an image, and the advisory ID is
what the owner needs to judge whether a finding matters to their app (is the vulnerable function reachable, is the
package even used). So Security names advisories with their links; Updates keeps counting.

## Success criteria

- The gate step `image-scan` and the console step `console-security` pass in the lab.
- A scan of a small image with a warm database takes under a minute; the first one, with the download, under five.
