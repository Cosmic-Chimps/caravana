---

description: "Task list for feature 005: real HTTPS and auto-revert on a cheap VM"
---

# Tasks: Real HTTPS and Auto-Revert on a Cheap VM

**Input**: Design documents from `specs/005-vm-https-proof/`

**Prerequisites**: [plan.md](plan.md), [spec.md](spec.md), [research.md](research.md),
[data-model.md](data-model.md), [contracts/host-sh.md](contracts/host-sh.md),
[quickstart.md](quickstart.md)

**Tests**: The gate proof is the test (spec FR-011–FR-014); scripts are checked with `shellcheck`
in CI (FR-017); the Go refactor keeps `TestLab` passing (FR-014a).

**Organization**: US1 = set up a host. US2 = real HTTPS and the public webhook. US3 = the
scripted gate proof. US4 = private access through a tunnel.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: can run in parallel (different files, no dependency on an unfinished task)
- **[Story]**: US1–US4 from spec.md

## Conventions for every task

- Scripts are Bash with `set -euo pipefail`, pass `shellcheck`, and say what they're doing.
  Anything run on the host is idempotent and reports `changed: <name>` only when it changed
  something (research R8).
- No secret goes into the repo or a command line on the Mac: tokens stay on the host and are read
  over SSH when needed; `deploy/vm/host.env` holds no secrets and is gitignored anyway.
- Nothing listens publicly on the host except 22, 80, 443 (data-model.md "Ports").
- Go changes follow the repo's CI rules (gofmt, vet, staticcheck, `-race`).
- The real VM and domain are the maintainer's; tasks that need them say so.

---

## Phase 1: Setup

- [x] T001 Create `deploy/vm/`; add `deploy/vm/host.env` to `.gitignore` (under "Local Nomad lab
  state"); write `deploy/vm/host.env.example` with the five variables from data-model.md, each
  with a one-line comment and an example value.
- [x] T002 Add a `shellcheck` step to `.github/workflows/ci.yml` (`shellcheck deploy/dev/*.sh
  deploy/vm/*.sh`; preinstalled on `ubuntu-latest`). Run it locally (`brew install shellcheck`;
  ask the maintainer before installing) and fix every finding in `deploy/dev/lab.sh` and
  `deploy/dev/setup.sh` without changing behavior.

---

## Phase 2: Foundational (blocks every story)

- [x] T003 In `deploy/dev/setup.sh`: take the state directory from `CARAVANA_STATE_DIR` (default
  `/var/lib/caravana-lab`, so the lab is unchanged), and restart Nomad only when its unit file or
  `/etc/nomad.d/` changed since the last run (keep a checksum file in the state directory);
  otherwise `systemctl enable --now nomad`. Re-run `deploy/dev/lab.sh up` on the lab: it must
  still come up and `lab.sh check` must pass.
- [x] T004 Gate refactor (research R11): new `cmd/caravana/gate_test.go` with `gateTarget` (API
  URL, webhook URL, app host, app URL, app HTTP client, Nomad client, `public *publicTarget` with
  the public IP and staging flag, nil in the lab) and `runGate(t, target)` running the steps from
  contracts/host-sh.md as sequential `t.Run` subtests, stopping at the first failure (`if
  !t.Run(...) { return }`). Move the step logic out of `cmd/caravana/lab_test.go` (deploy,
  webhook during the first rollout, unchanged webhook, same payload, broken deploy reverts with 0
  failed requests, Nomad runs the last healthy deployment, missing tag → 422); the app domain in
  the deploy body comes from the target. `TestLab` keeps starting Caravana in-process with the
  webhook listener and calls `runGate` with the lab target. Public-only steps are skipped when
  `public` is nil.
- [x] T005 Run `CARAVANA_LAB=1 go test ./cmd/caravana -run TestLab -v -timeout 20m` against the lab
  (`deploy/dev/lab.sh up`, `eval "$(deploy/dev/lab.sh env)"`); it must pass with one line per
  step, exactly as before the refactor.

**Checkpoint**: CI checks pass; the lab gate passes through `runGate`.

---

## Phase 3: User Story 1 - Turn a fresh VM into a Caravana host (Priority: P1) 🎯 MVP

**Goal**: `deploy/vm/host.sh up` turns a fresh Ubuntu VM into a host running Docker, Nomad,
Traefik and Caravana, with only 22/80/443 public, idempotently.

**Independent Test**: on the maintainer's VM: `up` twice (second: `changed:` empty), then
quickstart §6's port scan and reboot.

### Implementation for User Story 1

- [x] T006 [P] [US1] `deploy/vm/nomad.hcl.tpl` (research R3, R4, R5, R9): like
  `deploy/dev/nomad.hcl` but `bind_addr = "127.0.0.1"`, `client.network_interface =
  "caravana0"`, `client.host_volume "traefik-acme" { path = "/var/lib/caravana-host/traefik" }`,
  and a `@CPU_TOTAL_COMPUTE@` line that setup fills or removes.
- [x] T007 [P] [US1] `deploy/install/caravana-policy.hcl`: ACL policy for Caravana (namespace
  `default`: `submit-job`, `read-job`, `list-jobs`), with a comment on why it isn't management.
- [x] T008 [P] [US1] `deploy/vm/caravana.service` (research R7): `User=caravana`,
  `EnvironmentFile=/etc/caravana/caravana.env`, `ExecStart=/usr/local/bin/caravana`,
  `After=network-online.target nomad.service`, `Restart=on-failure`, `NoNewPrivileges=yes`,
  `ProtectSystem=strict`, `ProtectHome=yes`, `PrivateTmp=yes`,
  `ReadWritePaths=/var/lib/caravana`.
- [x] T009 [P] [US1] `deploy/vm/traefik.nomad.hcl`, base version: like
  `deploy/dev/traefik.nomad.hcl` (system job, host network, workload identity, Nomad provider
  with `watch=true` and `exposedByDefault=false`) but the `traefik` entrypoint on
  `127.0.0.1:8081`, and a comment that the access log stays off (spec FR-009). HCL2 variables for
  the webhook host, ACME email and staging flag are declared now and used in T013–T014.
- [x] T010 [US1] `deploy/vm/setup.sh` (runs on the host as root; contracts/host-sh.md order): a
  `write_if_changed SRC DEST MODE` helper that records names in a `changed` list; render
  `/etc/nomad.d/nomad.hcl` from the template (CPU line only when `lscpu` shows no MHz:
  `nproc × 2000`); run `deploy/dev/setup.sh` with `CARAVANA_STATE_DIR=/var/lib/caravana-host`;
  create the dummy interface with `/etc/systemd/network/10-caravana0.netdev` (`Kind=dummy`) and
  `.network` (`Address=10.99.0.1/32`), `systemctl enable --now systemd-networkd`, and make sure
  it exists before Nomad needs it; `ufw` default deny incoming, allow 22/80/443, enable
  (non-interactively, SSH allowed first); apply the `traefik-read` policy
  (`deploy/dev/traefik-policy.hcl`) and the `caravana` policy; create the Caravana token only
  when `/etc/caravana/caravana.env` has none; run the Traefik job with its `-var`s; create the
  `caravana` user and `/var/lib/caravana` (0700); write `/etc/caravana/caravana.env` (0600, the
  variables in data-model.md); install the binary from `$CARAVANA_BINARY` only if it differs;
  install the unit; restart Caravana only when its binary, env or unit changed; wait up to 30 s
  for `curl -fs 127.0.0.1:7070/v1/health`; print `changed: …`.
- [x] T011 [US1] `deploy/vm/host.sh` with `up` and `ssh` (contracts/host-sh.md): load and validate
  `host.env` (each missing variable named); check that both DNS names resolve to the host's IP
  (warn, don't fail); detect the host's architecture over SSH (`dpkg --print-architecture`);
  build `CGO_ENABLED=0 GOOS=linux GOARCH=<arch> go build -o <tmp>/caravana ./cmd/caravana`; copy
  the binary and `deploy/vm/*`, `deploy/dev/setup.sh`, `deploy/dev/traefik-policy.hcl` to a
  temp directory on the host; run `sudo env … bash setup.sh` there; print its `changed:` line and
  the public URLs; clean up the temp directory.
- [x] T012 [US1] Maintainer, on their VM (quickstart §2–§3, §6): `host.sh up` succeeds; a second
  `up` prints an empty `changed:`; `nmap -Pn -p- <ip>` shows only 22, 80, 443; after `sudo
  reboot` every service is back. Record anything that needed changing in research.md R4/R8.

**Checkpoint**: a host that's up, closed and idempotent.

---

## Phase 4: User Story 2 - whoami deployed by webhook, over real HTTPS (Priority: P1)

**Goal**: Let's Encrypt certificates, HTTP → HTTPS, and the public webhook host routed only to
`/v1/webhooks/`.

**Independent Test**: after `up`: `curl https://hooks.<domain>/v1/webhooks/x -X POST` → 401 with
a trusted certificate; `curl https://hooks.<domain>/v1/health` → 404.

### Implementation for User Story 2

- [x] T013 [US2] In `deploy/vm/traefik.nomad.hcl` (research R5): the `letsencrypt` resolver with
  HTTP-01 on `web`, email from the variable, storage `/acme/acme.json`, the staging CA server when
  the staging variable is `1`; `web` redirects to `websecure`; the `traefik-acme` host volume
  mounted at `/acme`.
- [x] T014 [US2] In `deploy/vm/traefik.nomad.hcl` (research R6): a `template` block rendering the
  file-provider route from research R6 into `local/webhook.yml`, `--providers.file.filename` set
  to it; the router named `caravana-webhook`, matching only `Host(<webhook-host>) &&
  PathPrefix(/v1/webhooks/)`, service `http://127.0.0.1:7071`.
- [x] T015 [US2] In `deploy/vm/setup.sh`: `CARAVANA_CERT_RESOLVER=letsencrypt` in
  `caravana.env`, and the Traefik job's `-var`s for the webhook host, email and staging flag.

**Checkpoint**: quickstart §3's expected answers hold on the maintainer's host.

---

## Phase 5: User Story 3 - Scripted proof of the Phase 1 gate (Priority: P1)

**Goal**: `deploy/vm/host.sh prove` runs the shared gate plus the public-only checks and prints
`GATE PASSED` only when all pass.

**Independent Test**: `host.sh prove` on the maintainer's host.

### Implementation for User Story 3

- [x] T016 [US3] `TestGate` in `cmd/caravana/gate_test.go` (contracts/host-sh.md): skip unless
  `CARAVANA_GATE=1`; build the target from `GATE_*` and `NOMAD_*`; the app client uses the system
  roots (`x509.SystemCertPool`); add the public-only steps: `https-trusted` (a verified TLS
  handshake to the app host; with `GATE_ACME_STAGING=1` it reports "staging certificate, not
  publicly trusted" and fails the gate), `http-redirects` (plain HTTP → 301/308 to HTTPS),
  `webhook-host-only-webhook` (`GET /v1/health` → 404, `POST /v1/webhooks/x` → 401 on the
  webhook host), `ports-closed` (dial 22/80/443 → open; 4646, 7070, 7071, 8081 and whoami's
  allocation port from the Nomad API → closed, each with a 3 s timeout, on `GATE_PUBLIC_IP`).
- [x] T017 [US3] `prove` in `deploy/vm/host.sh`: open the tunnel in the background (`ssh -N -f`
  with a control socket, closed by a `trap`), read the management token over SSH, export the gate
  variables (the public IP from the SSH host), run `CARAVANA_GATE=1 go test ./cmd/caravana -run
  TestGate -v -count=1 -timeout 20m`, and turn `--- PASS/FAIL: TestGate/<step>` lines into
  `PASS <step>` / `FAIL <step>: <first error line>`, ending with `GATE PASSED` or `GATE FAILED`
  and the matching exit code.
- [x] T018 [US3] Maintainer, on their VM with `CARAVANA_ACME_STAGING=0` (quickstart §5): `host.sh
  prove` prints `GATE PASSED`. That closes the Phase 1 gate.

**Checkpoint**: the gate is proven on a real host.

---

## Phase 6: User Story 4 - Operate the host privately (Priority: P2)

**Goal**: the operator's tunnel and env, as with the lab.

**Independent Test**: quickstart §4.

### Implementation for User Story 4

- [x] T019 [US4] `env` and `tunnel` in `deploy/vm/host.sh` (research R10): `env` prints
  `NOMAD_ADDR=http://127.0.0.1:14646`, `NOMAD_TOKEN=<read over ssh>`,
  `CARAVANA_URL=http://127.0.0.1:17070`; `tunnel` runs `ssh -N` with the three forwards and
  prints the local URLs first. `prove` (T017) reuses the same forward list.

---

## Phase 7: Polish & Cross-Cutting Concerns

- [x] T020 [P] `deploy/vm/README.md`: what you need (VM, DNS, provider firewall), the commands, the
  ports table, staging vs production, troubleshooting (DNS not pointing, certificate pending,
  port 80 blocked, `journalctl -u caravana`, `sudo lab-nomad …` on the host).
- [x] T021 [P] Update `CLAUDE.md` ("Where we are": feature 005 on branch `005-vm-https-proof`,
  `deploy/vm/`; next: Phase 2 starting with 006) and its commands (`deploy/vm/host.sh up | prove
  | tunnel | env`); update `docs/plan.md`'s Phase 1 table and gate line with the result of T018.
- [x] T022 Run every CI check (gofmt, `go mod tidy -diff`, vet, staticcheck, `go test -race`, both
  cross-compiles, `shellcheck`) and `TestLab` in the lab; fix anything they report.
- [x] T023 Write the summary for the maintainer: what changed, what was verified where (offline,
  lab, VM), anything T012/T018 changed, and the Go idioms introduced (test target structs,
  `t.Run` as a stoppable sequence, `x509.SystemCertPool`, dialing to check a port is closed), one
  line each with the C# comparison.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (T001–T002)** → **Foundational (T003–T005)** → stories.
- **US1 (T006–T012)**: after Foundational (T010 reuses T003's `deploy/dev/setup.sh`).
- **US2 (T013–T015)**: after T009–T011 (extends the Traefik job and setup).
- **US3 (T016–T018)**: T016 needs T004; T017 needs T011 and T019's forward list (write T019
  first or alongside); T018 needs US1 and US2 on a real host.
- **US4 (T019)**: after T011.
- **Polish (T020–T023)**: last; T021's gate line waits for T018.

### Within each story

T006–T009 before T010; T010 before T011; T013 and T014 edit the same file (in order); T012 and
T018 are the maintainer's, on the real VM.

### Parallel Opportunities

- T006, T007, T008, T009 (four new files).
- T016 (Go) alongside T013–T015 (Traefik/setup).
- T020, T021 together.

## Parallel Example: User Story 1

```text
Files at once:   T006 nomad.hcl.tpl · T007 caravana-policy.hcl · T008 caravana.service · T009 traefik.nomad.hcl
Then:            T010 setup.sh → T011 host.sh up → T012 (maintainer, VM)
```

## Implementation Strategy

### MVP first (US1)

Phases 1–3: a closed, idempotent host running Caravana. Everything after builds on it.

### Incremental delivery

1. US1: the host.
2. US2: real certificates and the public webhook.
3. US3: the proof; the Phase 1 gate closes when it passes on the real host.
4. US4: comfortable private access (needed by `prove` too).
5. Polish: README, docs, CI.

## Notes

- Without the maintainer's VM and domain, every task except T012 and T018 can be done and
  checked (shellcheck, CI, `TestLab` in the lab). T012 and T018 need the real host.
- The static webhook route is the plan's one Complexity Tracking entry; don't add others.
- Commit when the maintainer asks.
