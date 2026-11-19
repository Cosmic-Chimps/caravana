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
db	2026-11-19T06:23:17Z	v6.1.10
```

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
