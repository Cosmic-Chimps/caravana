# Feature Specification: Image scanning (known vulnerabilities in the images Caravana runs)

**Feature Branch**: `alta/gallant-helm-7qwv` (worktree; CodeAlta task `z57f`)
**Created**: 2026-11-19
**Status**: Draft
**Input**: CodeAlta task `z57f` ("11 · Scan deployed images for known vulnerabilities (CVEs)", raised by the
maintainer on 2026-11-19): scan every deployed image with a pinned open-source scanner run as a Nomad batch job,
after each deploy that brings a new digest and once a day; show the findings per app and on the Operator view; a
needs-you item and a notification for a new fixable critical; an optional gate that refuses such an image; an API
and an MCP read tool.

## Why

Caravana deploys images by digest and never looks inside them. An app can run for months on an image whose OS
packages or libraries have known vulnerabilities, some with a fix one rebuild away, and the owner isn't told.
Advisories are published every day, so an image that was clean when it was deployed can stop being clean without
anyone touching it.

## User Scenarios & Testing

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
1. Given scanning is on, when a deployment that brought a digest not scanned in the last 24 hours ends healthy,
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
again with the refreshed database.

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

### User Story 6 — Turned off, offline or short of room (Priority: P2)

1. Scanning can be turned off on Operator › Security. Then no job runs, nothing is downloaded, the scanner's
   database is deleted from the server, and the pages say "Scanning is off" above the last results.
2. A scan is skipped, and recorded as skipped with the reason, while the server that would run it has less free
   memory than the scan reserves or less than 4 GiB of free disk.
3. Without network the scanner uses the database it has for up to 5 days (it says so in the result's date); after
   that scans fail with "the vulnerability database is more than 5 days old".

### Edge cases

- An image reference Caravana runs that isn't pinned (it always is, constitution III) is never scanned by tag.
- A scan result larger than 4 MiB keeps its first findings by severity and says it was cut.
- A digest used by several apps is scanned once; its result is shown on each.
- Deleting an app removes the scans of the digests no other app runs.
- Caravana restarting during a scan: the run is settled as failed ("Caravana restarted during the scan") and its
  job purged; queued scans stay queued.

## Requirements

- **FR-001** The scanner is Grype (research R1), pinned by digest, run as a Nomad batch job named `_scan-<run ID>`
  with no app; its database is cached in a Caravana host volume `caravana-scanner`.
- **FR-002** Scans are queued (deploy, daily, owner, gate) and run one at a time; each run is recorded.
- **FR-003** The result per digest is stored and replaced by each successful scan; findings carry advisory ID,
  severity, package, installed version, package type, fix state, fixed versions and the advisory's link.
- **FR-004** Results of digests no app's live deployment runs are pruned after the daily pass and when an app is
  deleted.
- **FR-005** Needs-you kind `image_vulnerable` (key `image_vulnerable|<app>|<service>|<package>`), notified like
  every kind, with the package, versions, advisory and severity in the event.
- **FR-006** Settings: on/off (on by default), notify level (critical | high), refuse fixable criticals (off), the
  daily hour (UTC).
- **FR-007** API: read an app's scan, a deployment's scan, the list of images, the runs; read and write the
  settings; request a scan. MCP read tool `get_app_scan`. Deploy tokens are denied by the default deny.
- **FR-008** Credentials of a private registry reach the scan only through the job's own Nomad Variable, deleted
  with the job; no page, log, job, answer or row holds them.
- **FR-009** No data about the host or its apps is sent anywhere: the scanner's update check is off, it has no
  telemetry, and it downloads only its database (from Anchore's public listing) and the image's own layers (from
  the image's registry).

## The Updates page and the Security page (decided)

027's Updates page counts the host's OS updates and names no vulnerability: those are the server's packages,
fixed by `apt` (or unattended-upgrades), and naming CVEs there would only repeat what `pro` says when it is asked.
The Security page is about what the owner deploys: the fix is to rebuild or bump an image, and the advisory ID is
what the owner needs to judge whether a finding matters to their app (is the vulnerable function reachable, is the
package even used). So Security names advisories with their links; Updates keeps counting.

## Success criteria

- The gate step `image-scan` and the console step `console-security` pass in the lab.
- A scan of a small image with a warm database takes under a minute; the first one, with the download, under five.
