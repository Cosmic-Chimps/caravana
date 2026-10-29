# Implementation Plan: One-command install

**Branch**: `012-one-command-install` | **Date**: 2026-10-09 | **Spec**: [one-command-install.spec.md](./one-command-install.spec.md)

**Scale note**: about 16 files. Most of them are shell (`install.sh`, `tools/release.sh`,
`host.sh`, `install-test.sh`), plus a release workflow and a small Go change (a version string).
The risk is in the shell: an installer runs as root on someone's VM. Watch three things: the
preflight runs before any change, the upgrade rollback restores data and binary together, and the
console only goes public after the admin exists. Shellcheck in CI, the lab VM test (R11), and
`host.sh prove` on a real VM are the guards.

## Summary

One bash script, published with each GitHub release, turns a fresh Ubuntu 24.04 VM into a
Caravana host. It checks the machine first, then installs Docker and Nomad with ACLs on (the same
pinned, checksum-verified versions as the lab). It sets up the private `caravana0` interface, the
firewall, and Traefik with Let's Encrypt. It installs Caravana under systemd from a
checksum-verified release tarball, creates the admin, and only then routes the console domain to
Caravana. Running it again upgrades in place, putting the previous binary and its data back when
the new one fails its health check. `--uninstall` removes Caravana and leaves every app running.
`deploy/vm/setup.sh` is folded into the installer, and `host.sh` becomes its SSH wrapper, so the
gate proves the install users run.

## Project Structure

```text
deploy/install/install.sh               # the installer: preflight, install, upgrade, uninstall (R1, R4-R9)
deploy/install/nomad.hcl.tpl            # moved from deploy/vm/
deploy/install/traefik.nomad.hcl        # moved; route "caravana" (console domain -> :7070) replaces the webhook-only one (R5)
deploy/install/caravana-policy.hcl      # moved
deploy/install/traefik-policy.hcl       # copy of deploy/dev/traefik-policy.hcl
deploy/install/caravana.service         # moved; ExecStart /opt/caravana/current/caravana
deploy/install/README.md                # what the installer does, options, uninstall, removing Nomad by hand
tools/release.sh                        # build <version> <dir>: tarballs, install.sh with version, SHA256SUMS (R2)
.github/workflows/release.yml           # on v* tags: tools/release.sh + gh release create
.github/workflows/ci.yml                # + release build smoke, shellcheck of the new scripts
cmd/caravana/main.go (+ main_test.go)   # var version, `caravana version` (R3)
internal/api/health.go (+ test), api/openapi.yaml 0.9.1   # health answers its version
deploy/vm/host.sh, host.env.example, README.md   # up = release build + install.sh --from (R10); setup.sh removed
cmd/caravana/gate_test.go, lab_test.go  # webhook-only step only with a webhook-only target; policy path
deploy/dev/install-test.sh              # fresh Lima VM: install, rerun, upgrade, rollback, refuse older, uninstall (R11)
README.md, CLAUDE.md, docs/plan.md
```

**Structure Decision**: everything that runs on the host lives in `deploy/install/` and ships in
the release tarball. `deploy/vm/` is only the maintainer's SSH wrapper, and `deploy/dev/` is only
the lab.

## Constitution Check

| Principle | Assessment |
| --- | --- |
| I. Small by Default | PASS, and the point of the feature: "installable with one command" on a 2 GB VM, amd64 or arm64. No new Go module, no new runtime service (Docker, Nomad and Traefik were already the stack), no release tool beyond `gh`. |
| II. One Model, Many Inputs | Not touched. |
| III. Safe, Reversible Deploys | PASS. Apps keep running through install reruns, upgrades and uninstall. An upgrade that fails health puts the previous binary and data back. |
| IV. Fail Loudly | PASS. Preflight refuses before any change and names the failing check. Downloads are checksum-verified. An older release is refused. |
| V. API-First | PASS. `GET /v1/health` gains `version` in `api/openapi.yaml` (0.9.1) in the same change. |
| VI. Nomad-Native, No Lock-In | PASS. Uninstalling Caravana leaves workloads, Nomad and Traefik running, as the principle requires. The console route (console domain to `127.0.0.1:7070`) is a static route in Traefik's configuration: it is Caravana's own route under VI as amended in 1.1.0, since Caravana runs under systemd, not as a Nomad service, and so has no service tags. |
| VII. Secure by Default | PASS. ACLs on from the first install. Caravana gets a policy-scoped token, and the bootstrap token stays root-only. Only 22, 80 and 443 are public. The console is HTTPS-only, with `__Host-` Secure cookies and per-client sign-in limits through trusted proxies, and it goes public only after the admin exists. |
| VIII. Plain, Tested Go | PASS. The Go change is small and tested. Shell is shellchecked in CI and exercised end to end by `install-test.sh` and `host.sh prove`. |

Re-checked after design: no change.
