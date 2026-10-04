# Feature Specification: Real HTTPS and Auto-Revert on a Cheap VM

**Feature Branch**: `005-vm-https-proof`

**Created**: 2026-10-02

**Status**: Draft

**Input**: User description: "feature: 005" — from docs/plan.md: "Real HTTPS + auto-revert, on a
cheap VM. One arm64 VM (a few €/month) with a domain. Traefik ACME HTTP-01. Scripted proof that
a failing health check makes Nomad auto-revert and our deployment record says it." This feature
closes the Phase 1 gate: `traefik/whoami` deployed by a webhook, reachable over real HTTPS on
the VM, and a broken deploy auto-reverts.

## Context

Everything so far runs in the local Lima lab with a self-signed certificate. This feature runs
the same Caravana on a real, public machine, the way the target user will: one cheap VM, a
domain, certificates from Let's Encrypt, and a webhook a registry or CI can reach.

Consumers of this feature:

- **The maintainer as operator**: has an SSH-reachable VM and a domain, and wants one script to
  turn the VM into a Caravana host and one script to prove the Phase 1 gate on it.
- **Visitors**: anyone on the internet opening `https://whoami.<domain>`, with a browser or
  `curl` that trusts only public certificate authorities.
- **Webhook senders** (feature 004): a registry or CI reaching the webhook over public HTTPS.
- **Feature 012 (one-command install)**: will turn what this feature learns into the product
  installer. This feature is the rehearsal on real infrastructure, not the installer.

"Host" means the VM after setup. "Public" means reachable from the internet; "private" means
reachable only from the VM itself (and from the operator through an SSH tunnel).

## Clarifications

### Session 2026-10-02

- Q: How does Traefik reach Caravana's webhook listener, which isn't a Nomad job? → A: One
  static Traefik route for the control plane's own webhook host, limited to
  `Host(<webhook-host>) && PathPrefix(/v1/webhooks/)` → the webhook-only listener on loopback.
  Apps keep routing only through service tags; this is the one documented control-plane
  exception (plan's Complexity Tracking), not a constitution change.
- Q: What is the proof built from? → A: One set of gate checks in a Go test that targets any
  running Caravana through settings (API, webhook, app and Nomad addresses). The lab test starts
  Caravana in-process and runs them; the VM proof script opens the SSH tunnel and runs the same
  checks against the host, printing one line per step.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Turn a fresh VM into a Caravana host (Priority: P1)

The operator creates a small Ubuntu VM at any provider, points the domain's DNS at it, and runs
one setup script from their Mac with the VM's SSH address and the domain. The script installs
everything the lab has (Docker, Nomad with ACLs, Traefik), configures Traefik to get Let's
Encrypt certificates, installs the Caravana binary as a system service with the webhook-only
listener on, and closes everything else to the internet. Running it again changes nothing, or
upgrades Caravana when a newer binary is given.

**Why this priority**: nothing else in this feature can happen without a host.

**Independent Test**: run setup against a fresh VM; check from outside which ports answer, that
Caravana's service is running, and that a second run reports no changes.

**Acceptance Scenarios**:

1. **Given** a fresh Ubuntu VM reachable over SSH and DNS records pointing at it, **When** the
   operator runs setup with the VM address and the domain, **Then** it finishes without manual
   steps and the host runs Docker, Nomad (ACLs on), Traefik and Caravana as services that start
   on boot.
2. **Given** the host, **When** it's probed from the internet, **Then** only SSH, HTTP (for
   certificate challenges and redirects to HTTPS) and HTTPS answer; Nomad's API, Caravana's API
   and Traefik's dashboard are not reachable.
3. **Given** a host already set up, **When** setup runs again with the same inputs, **Then** it
   completes and changes nothing that's running (no service restarts, no new Nomad job
   versions).
4. **Given** a host and a newer Caravana binary, **When** setup runs with it, **Then** Caravana is
   replaced and restarted, its data kept, and unfinished deployments resume (feature 003).
5. **Given** the host reboots, **When** it comes back, **Then** Nomad, Traefik, Caravana and the
   deployed apps all come back without any action.

---

### User Story 2 - whoami deployed by webhook, over real HTTPS (Priority: P1)

The operator deploys whoami with its domain `whoami.<domain>`, creates its webhook token, and a
sender calls the public webhook URL. whoami answers at `https://whoami.<domain>` with a
certificate every standard client trusts.

**Why this priority**: it's the Phase 1 gate's first two clauses.

**Independent Test**: from a machine outside the VM, call the public webhook, then `curl` the app
URL without disabling certificate checks.

**Acceptance Scenarios**:

1. **Given** the host, **When** the operator deploys whoami through Caravana's private API
   (reached through an SSH tunnel), **Then** the deployment reaches `healthy` and
   `https://whoami.<domain>` answers with a valid, publicly trusted certificate for that name.
2. **Given** whoami's webhook token, **When** a sender on the internet calls the public webhook
   URL, **Then** it gets the same answers as in feature 004 (202 with a webhook deployment when
   something changed, 200 unchanged otherwise, 401 for a bad token).
3. **Given** the public webhook host, **When** anything other than the webhook path is requested
   on it, **Then** it answers 404 (Traefik doesn't route it, and the webhook-only listener
   wouldn't serve it either).
4. **Given** plain `http://whoami.<domain>`, **When** it's opened, **Then** it redirects to HTTPS.
5. **Given** a webhook call with the token in the query string, **When** the host's logs are
   read, **Then** the token appears in none of them (Caravana's, Traefik's, the system's).

---

### User Story 3 - Scripted proof of the Phase 1 gate (Priority: P1)

One script, run from the operator's Mac against the host, proves the gate end to end and prints
a pass/fail line per step: whoami deployed by a webhook, reachable over trusted HTTPS, a deploy
whose health check can never pass ends `reverted` while the app keeps answering every request,
and the record says so.

**Why this priority**: the gate is only met when it's proven, repeatably, on the real host.

**Independent Test**: run the proof script against the host; it exits 0 only if every step passes.

**Acceptance Scenarios**:

1. **Given** the host, **When** the proof runs, **Then** it deploys whoami, triggers a
   redeploy through the public webhook and sees that webhook deployment reach `healthy`
   (feature 004's lab technique: call it while the first rollout is still running).
2. **Given** whoami healthy, **When** the proof deploys it with a health check that can't pass,
   **Then** the deployment ends `reverted`, every request to `https://whoami.<domain>` during and
   after the rollout succeeds with a trusted certificate, and afterwards Nomad runs the last
   healthy deployment.
3. **Given** a failed step, **When** the proof ends, **Then** it exits non-zero and names the
   step and what it saw.
4. **Given** the proof has run, **When** it runs again, **Then** it passes again (it starts from
   a clean whoami each time).

---

### User Story 4 - Operate the host privately (Priority: P2)

The operator reaches Caravana's API, Nomad's UI and Traefik's dashboard from their Mac through
an SSH tunnel, using the same commands as with the lab (`NOMAD_ADDR`, `NOMAD_TOKEN`, the API
on a local port).

**Why this priority**: without it the operator can't deploy or debug, but it only wraps SSH.

**Independent Test**: open the tunnel, run feature 003's quickstart commands against it.

**Acceptance Scenarios**:

1. **Given** the host, **When** the operator opens the tunnel, **Then** Caravana's API, Nomad's
   API/UI and Traefik's dashboard answer on local ports, and the Nomad token is available the
   same way `lab.sh env` provides it.
2. **Given** the tunnel is closed, **When** those ports are tried from the internet, **Then**
   nothing answers.

---

### Edge Cases

- **DNS not pointing at the VM yet**: setup still completes, but checks the records and warns
  which names don't resolve to the VM; certificates can't be issued until they do. The proof
  fails at its first HTTPS step, naming the name that doesn't resolve.
- **Let's Encrypt rate limits**: repeated test runs must not exhaust production limits. Setup
  can use Let's Encrypt's staging environment; the proof's "trusted certificate" checks need
  production, and say so when the certificate is a staging one.
- **First certificate takes time**: the first HTTPS request may come before the certificate is
  issued; the proof waits a bounded time (with a clear message) instead of failing at once.
- **Certificates survive restarts**: Traefik's certificate store persists on the host, so a
  reboot or Traefik restart doesn't request new certificates.
- **arm64 and amd64 VMs**: setup detects the architecture and installs matching binaries (the
  lab's setup already does this for Nomad and CNI).
- **ARM VMs reporting 0 MHz CPU** (learned in the lab): setup sets Nomad's CPU total so jobs can
  be placed.
- **Setup interrupted** (network drop): running it again finishes the job.
- **The VM's provider firewall** blocks ports: setup's own checks report which public port didn't
  answer.
- **App ports bypassing Traefik**: Nomad maps each app's container port to a dynamic host port;
  if that port were bound on the public interface, `http://<vm-ip>:<port>` would reach the app
  without TLS. The ports are bound to loopback, and the proof tries whoami's actual port from
  outside to confirm it's closed.
- **Token in a proxy log**: Traefik's access log is off on the host, or excludes query strings
  (feature 004's note).
- **Two apps on one host**: each gets its own certificate through the same resolver; nothing in
  this feature is specific to whoami except the proof.
- **Running costs**: the proof leaves only whoami running; nothing in the feature needs more than
  the smallest VM tier.

## Requirements *(mandatory)*

### Functional Requirements

**Host setup**

- **FR-001**: The system MUST provide a setup script, run from the operator's Mac, that turns a
  fresh Ubuntu LTS VM reachable over SSH into a Caravana host, given the VM's SSH address, the
  domain names to use and an email for Let's Encrypt.
- **FR-002**: Setup MUST install and run as boot-time services: Docker, Nomad (single node,
  ACLs bootstrapped), Traefik (Nomad provider, event-stream watch, `exposedByDefault=false`)
  and Caravana (the given binary, with its data on the host's disk).
- **FR-003**: Setup MUST be idempotent: a second run with the same inputs changes nothing that is
  running; a run with a new Caravana binary replaces and restarts only Caravana.
- **FR-004**: Setup MUST work on arm64 and amd64 VMs, and MUST set Nomad's CPU total when the
  VM reports none.
- **FR-005**: Setup MUST close every port except SSH, HTTP and HTTPS to the internet; Nomad's
  API, Caravana's API and Traefik's dashboard MUST listen on loopback only.
- **FR-005a**: The host ports Nomad maps to app containers MUST NOT be reachable from the
  internet: apps are reached only through Traefik over HTTPS. Because Nomad's port mappings are
  address rewrites that bypass an ordinary host firewall, setup MUST bind them to loopback (or an
  equivalent private address), with a default-deny firewall as a second layer.
- **FR-006**: Caravana MUST run with the Let's Encrypt certificate resolver in its translator
  settings, so every routed app gets a real certificate (feature 001's `CertResolver`).

**Public endpoints**

- **FR-007**: Traefik MUST obtain certificates with Let's Encrypt's HTTP-01 challenge, store them
  persistently, and redirect plain HTTP to HTTPS (except the challenge path).
- **FR-008**: Caravana's webhook-only listener MUST be reachable from the internet over HTTPS at
  a configured webhook host name, and nothing else of Caravana MUST be. The route MUST be a
  single static Traefik route matching only that host and the `/v1/webhooks/` path prefix, to
  the listener on loopback; it is the only route not defined by Nomad service tags.
- **FR-009**: Webhook tokens MUST NOT appear in any log on the host: Traefik's access log is off,
  or records no query strings.
- **FR-010**: The setup MUST support Let's Encrypt's staging environment for test runs, chosen by
  the operator, and default to production.

**Proof**

- **FR-011**: The system MUST provide a proof script, run from the operator's Mac against the
  host, that exits 0 only when every Phase 1 gate step passes, and prints one line per step.
- **FR-012**: The proof MUST: start from a clean whoami; deploy it through the private API;
  create its webhook token; redeploy it through the public webhook and see that deployment reach
  `healthy`; check `https://whoami.<domain>` with full certificate verification; deploy a
  version whose health check can't pass and see it end `reverted` while every request during and
  after the rollout succeeds; and check that Nomad then runs the last healthy deployment.
- **FR-013**: The proof MUST check that, from the internet, only the expected ports answer
  (including whoami's own Nomad-mapped host port, which must not), and the public webhook host
  serves nothing but the webhook.
- **FR-014**: A failed proof step MUST stop the proof with a non-zero exit, naming the step and
  what it observed.
- **FR-014a**: The gate checks MUST exist once: the local lab test and the VM proof run the same
  checks, each pointed at its Caravana, Nomad and app addresses. The lab test keeps passing
  against the local lab after the change.

**Operating**

- **FR-015**: The system MUST provide a way to open an SSH tunnel to the host's private endpoints
  and print the same `NOMAD_ADDR`/`NOMAD_TOKEN` exports `lab.sh env` prints, plus Caravana's
  API address.

**Code and tests**

- **FR-016**: Changes to Caravana itself, if any are needed (e.g. a setting for how the webhook
  listener is reached), MUST follow features 002–004's rules: in the contract when they touch the
  API, covered by offline tests, and every CI check passing.
- **FR-017**: The scripts MUST be checked by CI at least for syntax and with `shellcheck` (or
  equivalent), since CI can't run them against a real VM.

### Key Entities

- **Host**: a VM after setup: its SSH address, architecture, domain names (app names and the
  webhook host), Let's Encrypt account email and environment.
- **Host configuration**: the inputs setup takes; the same inputs make setup a no-op.
- **Proof run**: the ordered steps of FR-012 with their pass/fail results.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: From a freshly created VM with DNS already pointing at it, setup plus the proof
  complete in under 30 minutes, with no manual step on the VM.
- **SC-002**: `https://whoami.<domain>` is served with a certificate that standard clients accept
  without any "insecure" option, 100% of the time after issuance.
- **SC-003**: During the broken rollout, 0 of the requests the proof makes to whoami fail, and the
  deployment ends `reverted`.
- **SC-004**: A port scan from the internet finds only 3 open ports (SSH, HTTP, HTTPS), and
  whoami's Nomad-mapped host port is among the closed ones.
- **SC-005**: A second setup run with the same inputs restarts 0 services and creates 0 Nomad job
  versions.
- **SC-006**: After a reboot, whoami answers over HTTPS within 3 minutes with no operator action.
- **SC-007**: A webhook token planted in a public request appears 0 times in the host's logs.
- **SC-008**: The VM costs no more than the smallest tier of a budget provider (a few € a month).

## Assumptions

- **The operator provides the VM and the domain**: a fresh Ubuntu 24.04 LTS VM (arm64 or amd64,
  2 GB RAM is enough) reachable as root (or a sudo user) over SSH, and DNS records for the app
  name(s) and the webhook host pointing at it (a wildcard record works). Creating the VM and the
  records is outside the scripts; any provider works. Docs suggest a Hetzner CAX11-class ARM VM.
- **Open questions for `/speckit-clarify`**, with the defaults this spec uses:
  1. **How Traefik reaches Caravana's webhook listener**: settled in Clarifications.
  2. **Provider automation.** Default: provider-agnostic (any SSH-reachable VM); no provider API
     or Terraform.
  3. **Let's Encrypt environment.** Default: production, with a staging switch for test runs.
- **Reuse over rewrite**: setup reuses the lab's host bootstrap (`deploy/dev/setup.sh`: Docker,
  CNI, Nomad with ACLs) and Traefik job, extended for ACME, the firewall and Caravana's service.
- **The proof and the lab test share their checks** (Clarifications); in the lab, app URLs use
  the self-signed certificate and skip the "publicly trusted" checks, which only the VM proof
  makes.
- **Single node only.** Multi-node, WireGuard and Nomad TLS are Phase 3.
- **Out of scope**: the product installer (012), auth for the API (006; the API stays private),
  backups, monitoring, automatic OS updates, IPv6-only hosts.
