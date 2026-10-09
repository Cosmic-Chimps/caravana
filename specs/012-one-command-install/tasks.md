# Tasks: One-command install

**Input**: [plan.md](./plan.md), [one-command-install.spec.md](./one-command-install.spec.md),
[research.md](./research.md), [data-model.md](./data-model.md), [contracts/](./contracts/)

**Scale note**: 15 tasks over about 16 files, mostly shell that runs as root on someone's VM.
All three stories (install, upgrade, uninstall) are paths through one script, `install.sh`, so
Phase 3 owns that file whole and builds it story by story, one after another. The Lima VM test
(T012) is the first real run of every path. Watch the order inside the installer: preflight
before any change, data restored with the binary on a failed upgrade, console route after the
admin.

Tests are required (constitution VIII). Shell is covered by shellcheck and the VM test, Go by
`go test`.

## Phase 1: Setup

No new module or tool. `gh` is preinstalled on GitHub's runners.

## Phase 2: Foundational (blocks every story)

Files: `cmd/caravana/main.go`, `cmd/caravana/main_test.go`, `internal/api/health.go`,
`internal/api/api_test.go`, `api/openapi.yaml`, `deploy/install/nomad.hcl.tpl`,
`deploy/install/traefik.nomad.hcl`, `deploy/install/caravana-policy.hcl`,
`deploy/install/traefik-policy.hcl`, `deploy/install/caravana.service`, `cmd/caravana/lab_test.go`,
`tools/release.sh`

**Wave 1 — independent (different files):**

- [x] **T001** [P] Version: `var version = "dev"` set by ldflags, `caravana version`, `GET /v1/health` answers `{"status":"ok","version"}`, `api/openapi.yaml` 0.9.1. Tests (research R3) · cmd/caravana/main.go, cmd/caravana/main_test.go, internal/api/health.go, internal/api/api_test.go, api/openapi.yaml
- [x] **T002** [P] Move the host files to `deploy/install/` (`git mv` from `deploy/vm/`, plus a copy of `traefik-policy.hcl`). In `traefik.nomad.hcl`, the router `caravana` (`Host(console_domain)` → `127.0.0.1:7070`) replaces the webhook-only one, and the ACME resolver becomes optional for `--acme none`. `caravana.service` runs `/opt/caravana/current/caravana`. The lab test reads the policy from its new path (research R5, R10) · deploy/install/*.hcl*, deploy/install/caravana.service, cmd/caravana/lab_test.go

**⟶ Wait for Wave 1 to finish, then:**

- [x] **T003** `tools/release.sh build <version> <out-dir>`: both architectures with the version set, `caravana_<version>_linux_<arch>.tar.gz` (the binary and the host files), `install.sh` with `CARAVANA_VERSION` set, and `SHA256SUMS` (research R2) · tools/release.sh

## Phase 3: The installer, User Stories 1, 2, 3 (P1, P2, P3) 🎯 MVP

Files: `deploy/install/install.sh`, `deploy/install/README.md`, `cmd/caravana/install_test.go`

### Implementation

- [x] **T004** [US1] `install.sh`, install path:
  - Options per the contract and `install.env`.
  - Preflight, in order, exiting 2 (R7).
  - Fetch or `--from`, checksum, unpack to `/opt/caravana/<version>` (R1).
  - Docker (`docker.io`), CNI and Nomad, with the lab's pins and checksums.
  - `caravana0`, `nomad.hcl` (with `cpu_total_compute` when no MHz is reported), ACL bootstrap, ufw.
  - Policies, the Traefik job without the console route.
  - The `caravana` user, `caravana.env` with trusted proxies, the `/usr/local/bin/caravana` wrapper, the systemd unit, the health check.
  - The admin (`/dev/tty`, or print the command) (R6), then the Traefik job with the console route (R5).
  - `changed:`, the final message (R8).

  · deploy/install/install.sh

**⟶ Wait for T004, then:**

- [x] **T005** [US2] Upgrade:
  - Compare versions with `sort -V`. Older exits 2, equal changes nothing, newer upgrades.
  - Upgrade steps: stop, copy the data, switch `current`, start, wait for health with the new version.
  - On failure, put the data and `current` back, start the old version, exit 1.
  - Keep the two newest versions and one data copy (research R4).

  · deploy/install/install.sh

**⟶ Wait for T005, then:**

- [x] **T006** [US3] `--uninstall [--purge]`: stop and remove the service, the wrapper and `/opt/caravana`. Revoke Caravana's Nomad token. Run Traefik again without the console route. With `--purge`, also remove the data, the env and the user (research R9) · deploy/install/install.sh

**⟶ Wait for T006, then:**

**Wave 2 — independent (different files):**

- [x] **T007** [P] [US1] [US2] [US3] `deploy/install/README.md`: the one-line command, the options, what gets installed and where, upgrading, uninstalling, and removing Nomad, Docker and Traefik by hand · deploy/install/README.md
- [x] **T008** [P] [US1] Go test: `install.sh` and `deploy/dev/setup.sh` pin the same Nomad and CNI versions, and `install.sh` checks every download's checksum · cmd/caravana/install_test.go

**Checkpoint**: one script installs, upgrades with rollback, and uninstalls.

## Phase 4: The rehearsal and the installer test (all stories)

Files: `deploy/vm/host.sh`, `deploy/vm/host.env.example`, `deploy/vm/README.md`,
`deploy/vm/setup.sh` (removed), `deploy/dev/install-test.sh`, `cmd/caravana/gate_test.go`

**Wave 1 — independent (different files):**

- [x] **T009** [P] `host.sh` runs the installer (research R10):
  - `up` builds a dev release with `tools/release.sh`, copies it, and runs `install.sh --from` over `ssh -t`.
  - `admin` is `ssh -t sudo caravana admin`.
  - `uninstall` is added.
  - `prove` sends `GATE_API` and `GATE_WEBHOOK` through `https://<console domain>`.
  - `host.env.example` gets `CARAVANA_DOMAIN_CONSOLE` and `CARAVANA_ADMIN_EMAIL`.
  - The README is updated, and `setup.sh` is removed.

  · deploy/vm/host.sh, deploy/vm/host.env.example, deploy/vm/README.md, deploy/vm/setup.sh
- [x] **T010** [P] `deploy/dev/install-test.sh` (research R11):
  - Create a fresh Lima Ubuntu 24.04 VM.
  - Install with `--acme none`, then check HTTPS and sign-in.
  - Rerun and expect an empty `changed:`.
  - Upgrade with whoami answering throughout.
  - Roll back after a failed health check.
  - Refuse an older version.
  - Uninstall with whoami still answering.
  - Delete the VM.

  · deploy/dev/install-test.sh
- [x] **T011** [P] Gate: the webhook-only step runs only when the target has a separate webhook-only listener (`GATE_WEBHOOK_ONLY=1`, which the lab sets). `ports-closed` also expects 7071 closed and passes when nothing listens there · cmd/caravana/gate_test.go

**⟶ Wait for Wave 1 to finish, then:**

- [x] **T012** Run `deploy/dev/install-test.sh` end to end. Fix what it finds in Phase 3's script.

**Checkpoint**: every installer path has run on a real Ubuntu VM.

## Phase 5: Releases

Files: `.github/workflows/release.yml`, `.github/workflows/ci.yml`

- [x] **T013** `release.yml` runs on `v*` tags: `tools/release.sh build`, then `gh release create` with the assets. `ci.yml` gains a release build smoke test (`tools/release.sh build 0.0.0-ci <tmp>`) and shellchecks `deploy/install/*.sh` · .github/workflows/release.yml, .github/workflows/ci.yml

## Phase 6: Polish

Files: `README.md`, `CLAUDE.md`, `docs/plan.md`

- [x] **T014** Docs. The README's install section gets the one-line command and a note that it needs a public repository and a release. CLAUDE.md gets where we are and the commands. `docs/plan.md` marks 012 done · README.md, CLAUDE.md, docs/plan.md

**⟶ Wait for T014, then:**

- [x] **T015** Validate against the Success Criteria:
  - build, vet, staticcheck, `go test -race`, gofmt, `go mod tidy -diff`
  - both cross-compiles
  - `tools/ui-css.sh check`, `tools/ui-vendor.sh check`
  - shellcheck of every script
  - `tools/release.sh build`
  - `TestLab`
  - `install-test.sh`

## Dependencies & Execution Order

- Phase 2 blocks everything. Its Wave 1 is T001 and T002, then T003.
- Phase 3 needs Phase 2 (T003 for `--from` artifacts). It runs T004, T005 and T006 in that order, then T007 and T008.
- Phase 4 needs Phase 3. Its Wave 1 is T009, T010 and T011, then T012.
- Phase 5 needs T003 and can run any time after it.
- Phase 6 comes last: T014, then T015.
