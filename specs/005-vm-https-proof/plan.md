# Implementation Plan: Real HTTPS and Auto-Revert on a Cheap VM

**Branch**: `005-vm-https-proof` | **Date**: 2026-10-02 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from `specs/005-vm-https-proof/spec.md`

## Summary

Add `deploy/vm/`: `host.sh` (run on the Mac: `up`, `env`, `tunnel`, `prove`, `ssh`) and
`setup.sh` (run on the host). `up` cross-compiles Caravana for the host, copies it with the
setup files, and runs `setup.sh`, which writes a public-host Nomad config (`bind_addr
127.0.0.1`, allocation ports on a dummy `caravana0` interface so they can't bypass Traefik),
runs the lab's existing bootstrap, enables `ufw` (22/80/443), creates scoped ACL tokens, runs a
VM Traefik job with a Let's Encrypt HTTP-01 resolver, a persistent `acme.json` host volume, the
dashboard on loopback and one static file-provider route for the webhook host, and installs
Caravana as a hardened systemd service. Everything is idempotent. The lab test's gate steps
move into `runGate`, shared by `TestLab` and a new `TestGate`, which `host.sh prove` runs
against the VM through an SSH tunnel and public HTTPS. CI gains `shellcheck`.

## Technical Context

**Language/Version**: Bash (scripts), HCL (Nomad agent and the Traefik job), Go 1.27 (tests)

**Primary Dependencies**: on the host: Ubuntu 24.04's `docker.io`, `ufw`, systemd-networkd;
Nomad 2.0.7, CNI 1.9.1 (as in the lab); Traefik v3.7.13 (container). On the Mac: `ssh`, `scp`,
Go. **No new Go module.**

**Storage**: unchanged for Caravana; Traefik's `acme.json` on a Nomad host volume

**Testing**: existing CI checks + `shellcheck`; `TestLab` (opt-in) still passes after the
refactor; `TestGate` (opt-in) against the real host is the gate proof

**Target Platform**: Ubuntu 24.04 LTS, arm64 or amd64, 2 GB RAM

**Project Type**: operations scripts + test refactor (no product code change expected)

**Performance Goals**: setup + proof under 30 min from a fresh VM (SC-001); reboot to HTTPS in
under 3 min (SC-006)

**Constraints**: only 22/80/443 public; idempotent; no secrets in the repo (`host.env` is
gitignored and holds none: tokens stay on the host and are read over SSH)

**Scale/Scope**: 1 host; ~6 new files in `deploy/vm/`, 1 refactored test file, 1 CI step

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | How this plan complies | Status |
| --- | --- | --- |
| I. Small by Default | One Go binary under systemd plus Nomad and Traefik, on a 2 GB VM; no new runtime service or Go module. | ✅ |
| II. One Model | No model change. | ✅ |
| III. Safe, Reversible Deploys | The gate proves it on real infrastructure: digest pinning, blue/green, auto-revert with 0 failed requests. | ✅ |
| IV. Fail Loudly | Scripts stop at the first failed step and name it; DNS mismatches are warned about; the proof names what it saw. Traefik's certificate store is an explicit host volume. | ✅ |
| V. API-First | No API change; the operator uses the same API through a tunnel. | ✅ |
| VI. Nomad-Native | Apps still route only by service tags. **One exception**: a static Traefik route for the control plane's webhook host (Complexity Tracking). Caravana stays outside Nomad (plan item 012), so uninstalling it leaves workloads running. | ⚠️ justified |
| VII. Secure by Default | Nomad ACLs on with scoped tokens (Caravana gets submit/read, not management); Nomad, the API and the dashboard on loopback; allocation ports on a private interface; default-deny firewall; Traefik access log off; secrets only on the host (0600 env file). | ✅ |
| VIII. Plain, Tested Go | The gate refactor keeps one set of checks (no duplicate logic); scripts are shellchecked in CI. | ✅ |
| Development Workflow | Phase 1's last item; closes the gate. Not the installer (012). | ✅ |

**Post-design re-check**: still passes, with the one justified exception below. Worth knowing:
- **The dummy-interface approach to app ports** (research R4) is the part most likely to need
  adjusting on a real host; the proof checks it directly, and the fallback keeps the same
  layout.
- **`deploy/dev/setup.sh` changes in one line** (state directory from an env var), so the lab
  keeps working unchanged.

## Project Structure

### Documentation (this feature)

```text
specs/005-vm-https-proof/
├── plan.md
├── research.md            # R1–R13
├── data-model.md          # host.env, host layout, ports, gate target
├── quickstart.md
├── contracts/host-sh.md   # host.sh, setup.sh, the gate test's env and steps
├── checklists/requirements.md
└── tasks.md               # /speckit-tasks
```

### Source Code (repository root)

```text
deploy/vm/                             # NEW
├── README.md                          # what you need, the steps, troubleshooting
├── host.sh                            # Mac side: up | env | tunnel | prove | ssh
├── host.env.example                   # the five variables, commented
├── setup.sh                           # host side, root, idempotent
├── nomad.hcl.tpl                      # public-host agent config (bind 127.0.0.1, caravana0, host volume)
├── traefik.nomad.hcl                  # VM Traefik job (ACME, redirect, file-provider webhook route)
└── caravana.service                   # systemd unit

deploy/dev/setup.sh                    # state directory from an env var (default unchanged)
.gitignore                             # + deploy/vm/host.env
.github/workflows/ci.yml               # + shellcheck step

cmd/caravana/
├── gate_test.go                       # NEW: gateTarget, runGate, TestGate
└── lab_test.go                        # TestLab → in-process Caravana + runGate

CLAUDE.md                              # where we are, deploy/vm commands
docs/plan.md                           # Phase 1 gate status
```

**Structure Decision**: `deploy/vm/` mirrors `deploy/dev/` (scripts plus the files they install),
so the lab and the VM read alike and 012 can later merge them into the installer.

## Implementation notes for /speckit-tasks

- **Order**: setup (folder, `.gitignore`, `host.env.example`, shellcheck in CI and the lab
  scripts fixed) → foundational (`deploy/dev/setup.sh` state dir; the gate refactor with
  `TestLab` still passing in the lab) → US1 (host setup: config, bootstrap reuse, dummy
  interface, firewall, ACLs, Traefik job, Caravana service, idempotency) → US2 (ACME, redirect,
  webhook route, token-free logs) → US3 (`TestGate`'s VM-only steps, `host.sh prove`) → US4
  (`env`, `tunnel`) → polish (README, CLAUDE.md, docs/plan.md).
- **What can be verified without a VM**: shellcheck, the Go checks, `TestLab` in the lab, and
  `setup.sh`'s idempotency against a throwaway Lima VM (`limactl start template://ubuntu-lts`)
  if the maintainer wants a dry run. The gate itself needs the real host and domain.
- **Go idioms to explain**: test helpers taking a target struct; `t.Run` returning false to stop
  a sequence; `crypto/x509.SystemCertPool` for "publicly trusted"; dialing a TCP port with a
  timeout to check it's closed.

## Complexity Tracking

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| A static Traefik route (file provider) for the control plane's webhook host, while constitution VI says routing is configured only through service tags | Caravana runs under systemd, not as a Nomad job (plan item 012: a cold start must not wait on anything), so it has no Nomad service and no tags; senders still need `https://<webhook-host>/v1/webhooks/*` | A Nomad job registering a service for the host's port makes the webhook depend on Nomad and a job being up; a separate public port with its own certificates duplicates Traefik's TLS. The route matches only that host and path prefix and points at the webhook-only listener, so apps are unaffected (clarification Q1). |
