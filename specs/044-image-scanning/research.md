# Research: Image scanning

## R1 Grype or Trivy (decided: Grype)

Both are Apache-2.0, open source, multi-arch (amd64 and arm64 images), read an image straight from its registry
without a Docker daemon, and take registry credentials from the environment. Measured in the lab VM (arm64, 2 GiB)
on 2026-11-19, scanning `alpine:3.14.0` from Docker Hub:

| | Grype v0.117.0 | Trivy 0.69.3 |
| --- | --- | --- |
| Image | `anchore/grype`, 27.6 MB, `FROM scratch` (no shell), runs as root | `aquasec/trivy`, Alpine-based |
| Database source | `grype.anchore.io/databases/v6/latest.json`, a 204 MB `.tar.zst`, built daily | `mirror.gcr.io/aquasec/trivy-db:2` (OCI artifact) |
| Database on disk | 3.5 GB (`vulnerability.db`, SQLite) | 1.7 GB (`trivy.db`, bbolt) |
| First scan (download included) | 105 s | 25 s |
| Scan with a warm database | 8 s, memory peak 477 MB | not measured |
| Outbound calls besides the database and the image | an update check, off with `GRYPE_CHECK_FOR_APP_UPDATE=false` | telemetry and a version check, both on by default since 0.63; `--disable-telemetry` and `--skip-version-check` turn them off, but a user report says the request still goes out ([discussion #10971](https://github.com/aquasecurity/trivy/discussions/10971)) |
| Supply chain | no known incident | v0.69.4 and Docker images 0.69.5/0.69.6 were malicious (CVE-2026-33634, March 2026); 0.69.2 and 0.69.3 are known safe |
| Output for Caravana | Go template from a file (`-o template -t file --file out`) | Go template inline |

Grype is the choice: the task's rule is that no data about the host or its apps is sent anywhere, and Grype's one
extra call is an update check that a documented setting turns off, where Trivy's telemetry is on by default and
reportedly not fully off with its flags. The cost is disk (3.5 GB, and about twice that for a moment while a new
database replaces the old) and a 204 MB download a day, which the scan's disk check (D7) and the daily update cap
(D2) keep in hand.

The image has no shell, so one job scans one image; Caravana runs the jobs one after another (D5).

## R2 What Grype prints (lab, observed)

With the template of D3, `alpine:3.14.0` gives lines like

```
caravana-scan v1
m	CVE-2022-37434	Critical	fixed	1.2.12-r2	zlib	1.2.11-r3	apk	https://security.alpinelinux.org/vuln/CVE-2022-37434
m	CVE-2024-5535	Critical	unknown		libssl1.1	1.1.1k-r0	apk	https://nvd.nist.gov/vuln/detail/CVE-2024-5535
m	CVE-2022-48174	Critical			busybox	1.33.1-r2	apk	https://nvd.nist.gov/vuln/detail/CVE-2022-48174
db	2026-11-19 06:23:17 +0000 UTC	v6.1.10	0.117.0
```

(The first draft of this section showed the build time as RFC 3339; the template prints Go's own form, and
`vulns.Parse` reads both. Since D3/D18 an `m` line also has the related IDs, the EPSS score and percentile and
`kev`, and the trailer the scanner's version.)

- Severity is one of `Critical`, `High`, `Medium`, `Low`, `Negligible`, `Unknown`.
- The fix state is `fixed`, `not-fixed`, `wont-fix`, `unknown` or empty; only `fixed` with a version is fixable.
- One advisory appears once per package it hits (`libssl1.1` and `libcrypto1.1` both).
- Grype warns on stderr for an end-of-life distribution ("vulnerability data may be incomplete"); it still exits 0.
- The database is a directory `6/` under the cache with `vulnerability.db`, `import.json` and `last_update_check`.
- A database written to the VM's `/tmp` (a tmpfs) used the VM's memory and got the scan killed for memory: the
  cache must be on disk, which a host volume is.

## R3 Nomad (lab, observed, Nomad 2.0.7)

- A batch job named `_scan-probe` registers, runs and purges; its task reads the variable
  `nomad/jobs/_scan-probe` through a template with no policy line (a job reads its own path). App IDs never contain
  `_` and are never empty, so `_scan-…` can be no app's job and starts with no app's prefix `<app>_`.
- A batch group with `network { mode = "host" }` runs a docker task without any plugin setting, **but the container
  stays on Docker's bridge** (R5, found by the lab step with a loopback registry; this probe's call to `127.0.0.1:4646`
  printed nothing, which already said so). The task also needs `network_mode = "host"` in its docker config to see the
  node's network and reach a registry the way the node's Docker does.

## R4 Phase 1 in the lab (Grype v0.117.0, 2026-11-19)

- **Field names.** `.RelatedVulnerabilities[].ID`, `.Vulnerability.EPSS[].EPSS` and `.Percentile`,
  `.Vulnerability.KnownExploited`, `.Descriptor.Version` all exist under those names. `alpine:3.14.0` has EPSS on
  all 138 matches (CVE-2022-37434: 0.19243, percentile 0.97389) and no KEV; a log4shell image
  (`ghcr.io/christophetd/log4shell-vulnerable-app`) gave 10 KEV findings. `Fix.State` and `Artifact.Type` are named
  string types, which sprig's `replace` refuses, so the template passes fields through `printf "%s"` first; EPSS
  numbers are printed as they are.
- **Two outputs in one run.** `-o template=<file> -t <tmpl> -o cyclonedx-json=<file>` writes both and exits 0.
- **SBOM rescans (D17).** `grype sbom:<the CycloneDX file the registry scan wrote>` gave a template output
  byte-identical to the registry scan's (140 lines). D17 stands; only this OS-package image was compared, so the lab
  step compares again (T013).
- **`grype db update`** exits 0: on an empty cache it downloads and installs the database in 96 s, on a warm one it
  says "No vulnerability database update available".
- **Errors.** Grype logs warnings, then one `[000n] ERROR …` line with its causes indented below; only the text from
  the first ERROR on is classified (`classifyScanFailure`, with the samples in its test):
  - "failed to load vulnerability db: database does not exist" (after "unable to download listing"): database
    download;
  - "… the vulnerability database was built 10 hours ago (max allowed age is 1 hour)": database too old;
  - "UNAUTHORIZED: authentication required" (Docker Hub), "DENIED: requested access to the resource is denied"
    (ghcr.io), "401 Unauthorized: incorrect username or password": registry credentials;
  - "failed to catalog: unable to open file …", "unable to decode sbom: sbom format not recognized": SBOM missing;
  - "MANIFEST_UNKNOWN: manifest unknown", "connection refused": the image couldn't be read.
  "failed to load vulnerability db" also covers a database that can't be loaded for another reason (a full disk), so
  the console's words for that class are "the vulnerability database couldn't be downloaded or loaded".
- **Registry authority.** `docker.io`, `index.docker.io` and `registry-1.docker.io` as
  `GRYPE_REGISTRY_AUTH_AUTHORITY` all made Grype send the credentials to Docker Hub.
- **Not yet seen** (T013): a scan as a Nomad job with host networking, the volume's ownership, whether Grype makes
  `/cache/sbom/` itself, and a real private registry.

## R5 The lab steps (2026-11-19)

The gate step `image-scan` (`cmd/caravana/gate_image_scan_test.go`) and the console step `console-security`
(`console_security_test.go`) pass in the lab (Lima VM, arm64, 2 GiB, Nomad 2.0.7, Grype 0.117.0). Run them alone with
`CARAVANA_LAB=1 go test ./cmd/caravana -run 'TestLab/^(api-needs-token|image-scan)$' -v -count=1 -timeout 40m` (283 s) and
`-run 'TestConsoleLab/^(console-sign-in|console-security)$'` (136 s). Both install and remove the add-on, so each downloads the
database again (the volume goes with the removal), and they must not run at the same time.

**Timings** (gate step, three passing-or-nearly runs agree to a few seconds):

| | Time |
| --- | --- |
| Install (volume, 204 MB database, `db update`) | 1 m 42 s – 1 m 53 s |
| Deploy of `alpine:3.14.0` to its first scan result (registry scan, database warm, SBOM written) | 24 – 31 s (includes the deploy's own rollout, about 15 s) |
| Daily rescan from the kept SBOM | 7 – 8 s |
| Deploy of `alpine:3.14.10` to its scan result | 23 – 29 s |
| Deploy of an image from the password-protected registry to its scan result | 22 s |
| Remove (volume and results) | 1 s with the database never refreshed; the volume holds 3.5 GB |

**What the step shows** (everything in plan D2 to D18 that research R4 listed as "not yet seen"):

- A scan as a Nomad job works: the volume `caravana-scanner` is mounted at `/cache`, Grype writes `/cache/db` and creates
  `/cache/sbom/` itself, and the job writes the result file the service reads. The scan ran on the volume's node
  (`lima-caravana-lab`).
- **The SBOM rescan gives the same result as the registry scan** for `alpine:3.14.0`: after one finding was dropped from the store, the
  daily run (`trigger daily`, `source sbom`, status ok) found it again, with the same stable ID and a later `first_seen`, and the
  image's finding count was the same as the registry scan's. A kept SBOM is read for the image's rescan with no pull and no variable.
- Findings of `alpine:3.14.0`: `CVE-2022-37434` in `zlib 1.2.11-r3`, critical, fixable, fixed in `1.2.12-r2`, an https advisory link,
  a 16-hex ID, first and last seen, scanner `0.117.0`, the database's build date, SBOM kept. The same image also opens three more
  items (`libssl1.1` and `libcrypto1.1` for CVE-2021-3711, `apk-tools` for CVE-2021-36159), one per package, so one scan gives four
  `problem.opened`. All four were resolved (`problem.resolved`, "Resolved: <the item's sentence>") by a deploy of `alpine:3.14.10`.
- **The zlib-fixed tag is `alpine:3.14.10`** (zlib 1.2.12-r3; 3.14.8 has the same). It has no fixable critical in zlib; other packages may
  still have findings, which the step doesn't judge.
- A resolved event carries the app, the service and "Resolved: <sentence>" (no package property); the step finds zlib in the title.
- A private image in a registry with htpasswd (`127.0.0.1:15000`, plain HTTP): the scan succeeded with the credentials from the job's own
  variable (`GRYPE_REGISTRY_AUTH_AUTHORITY=127.0.0.1:15000` is enough for Grype; no `GRYPE_REGISTRY_INSECURE_USE_HTTP` was needed for a
  loopback address: go-containerregistry tries https, then http for loopback). The step polled Nomad every 300 ms during the scan and saw
  the job, with no password in its definition, and the variable holding the password; both were purged when the scan ended. The password
  is in no API answer, the server log, the data directory's files or the scan job.
- The deploy check: with it on, `alpine:3.14.0` is 422 `image_vulnerable` on `services[0].image`, the message naming the worst fixable
  critical, its fix and how many more ("web's image has CVE-2021-36159 (critical) in apk-tools 2.12.5-r1, fixed in 2.12.6-r0 and 3 more").
  The cached scan (under 24 hours) was used.
- Removal: the add-on lists as `available`, the host volume is gone, `/v1/scans` and `/v1/scans/runs` are empty, no `image_vulnerable`
  item is left and no `problem.resolved` is sent for the items that went with it.

**What the lab found** (one production fix, two facts):

1. **The group's `network { mode = "host" }` doesn't put the container on the host network.** Docker still used its bridge, so the scan of
   an image on a registry at the node's loopback failed with Grype's "connection refused" (class "the image couldn't be read from its
   registry"). R3 had tested a public image only. `nomadjob.ScanJob` now also sets `network_mode = "host"` in the docker config (goldens and
   a shape test updated). Reproduced by hand with the pinned Grype image: with `--network host` it scans the registry's image with the
   credentials; without, `dial tcp 127.0.0.1:15000: connect: connection refused` on https and http.
2. **The add-on has no database date until its first scan.** The install's run is `grype db update`, which reports no date, and
   `fillScannerFacts` takes it from the newest successful scan run, so Operator › Security says "database of …" only once an image has
   been scanned (an installation with no running app shows none). The step asserts the date after the first scan. Decided (review, 2026-11-19): kept as
   it is. The install queues a scan of every image a running app uses, so the date shows within a minute on any installation that
   runs apps; a second job with `grype db status` would cost another container start for a date nobody needs before the first scan.
3. A Docker bug of the lab, not of Caravana: `/tmp` in the VM is a tmpfs, so a by-hand Grype with its cache in `/tmp` fails with "no space
   left on device" (R2 had the memory side of this).

**Stderr classes (D4)**: the one phrase seen this time is "oci-registry: failed to get image descriptor from registry … connection refused"
(class image read); "failed to load vulnerability db: database does not exist" (after a write error on a full disk) was seen again from the
hand-run (class database). The classes of R4 stand.
