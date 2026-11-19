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
- A batch group with `network { mode = "host" }` runs a docker task without any plugin setting: the task sees the
  node's network, so it reaches a registry the way the node's Docker does (a loopback registry included, which the
  gate's private-registry step uses).

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
