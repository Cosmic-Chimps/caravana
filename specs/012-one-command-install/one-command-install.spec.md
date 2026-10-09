# Feature Specification: One-command install

**Feature Branch**: `012-one-command-install`
**Created**: 2026-10-08
**Status**: Draft
**Input**: "012 One-command install: a fresh VM becomes Docker + Nomad (ACLs on) + Traefik + Caravana with one command, Caravana as a systemd service (not a Nomad job) so a cold start never waits on anything. deploy/vm/ (host.sh over SSH + setup.sh) is its rehearsal. Phase 2, per docs/plan.md."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Install on a fresh VM with one command (Priority: P1)

Someone who has never seen Caravana rents a small Ubuntu VM, opens a shell on it, and pastes one
command from the README. A few minutes later the VM runs Docker, Nomad with ACLs on, Traefik with
Let's Encrypt, and Caravana under systemd. The command ends by printing the console's HTTPS
address and how to sign in. They open it in a browser, sign in as the admin, and deploy their
first app. They never copy a binary, write a config file, or open a second tool.

**Why this priority**: until this works, only the maintainer can run Caravana. It is the end of
the MVP: "after 012, someone else can install and use it".

**Independent Test**: on a fresh Ubuntu 24.04 VM, run the published command with an email and
a domain, sign in to the printed address, and deploy whoami from the console.

**Acceptance Scenarios**:

1. **Given** a fresh Ubuntu 24.04 VM (amd64 or arm64) with ports 22, 80 and 443 reachable and
   a domain pointing at it, **When** the owner runs the one command as root, **Then** it ends
   with the console's HTTPS address, and that address answers with a trusted certificate.
2. **Given** the command ran with an admin email in a terminal, **When** it asks for a password
   and the owner types it twice, **Then** the owner can sign in to the console with them.
3. **Given** the command ran without a terminal (for example from a provisioning script),
   **When** it ends, **Then** it prints the one command that creates the admin later.
4. **Given** the installed host, **When** anyone on the internet scans it, **Then** only 22, 80
   and 443 answer, as on the rehearsal host.
5. **Given** the installed host restarts, **When** it comes back, **Then** Caravana answers
   without waiting on any app, and every app comes back as Nomad restarts it.

---

### User Story 2 - Upgrade by running the same command again (Priority: P2)

A new Caravana version is out. The owner runs the same command again. Caravana is replaced in
place. The data, the admin, the API tokens, the certificates and every running app stay as they
were. If the new version doesn't come up healthy, the previous one is put back and the command
says so.

**Why this priority**: an install nobody can upgrade is a trap, but the first install has to
exist before the second one matters.

**Independent Test**: install one version, deploy an app, run the command for a newer build,
and check that the app kept answering and the console shows the same data under the new version.

**Acceptance Scenarios**:

1. **Given** an installed host with apps running, **When** the owner runs the command again for a
   newer version, **Then** Caravana runs the new version, the apps never stopped answering, and
   nothing the owner set up is lost.
2. **Given** an installed host, **When** the owner runs the command again for the same version,
   **Then** it changes nothing and says so.
3. **Given** the new version doesn't pass its health check, **When** the command notices,
   **Then** the previous version runs again and the command exits with an error that says so.
4. **Given** a version older than the installed one, **When** the owner runs its command,
   **Then** it is refused before anything changes, because the stored data may be newer than it
   can read.

---

### User Story 3 - Remove Caravana, keep the apps (Priority: P3)

An owner decides to stop using Caravana. One command removes Caravana itself: its service, its
binary, and optionally its data. Nomad, Traefik and every app keep running, so the owner can go on
with plain Nomad and the jobspecs feature 011 exports.

**Why this priority**: "no lock-in" is a product principle, and the install has to be undoable,
but few owners will use it early on.

**Independent Test**: install, deploy whoami, export its jobspec, uninstall Caravana, and check
that whoami still answers over HTTPS and Nomad still runs its job.

**Acceptance Scenarios**:

1. **Given** an installed host with apps, **When** the owner runs the uninstall command, **Then**
   Caravana no longer runs, and every app and Traefik keep answering.
2. **Given** the uninstall ran without asking to delete data, **When** the owner installs again,
   **Then** Caravana starts with its old data, admin and apps.

---

### Edge Cases

- The domain doesn't point at the VM yet. The command stops before changing anything, naming the
  domain and the address it resolves to, because Let's Encrypt would fail.
- No domain is given. The command uses a name derived from the VM's public IP address (an
  `sslip.io` name), so a first try needs nothing but a VM.
- The OS isn't Ubuntu 24.04, the architecture isn't amd64 or arm64, the VM has less than 2 GB of
  memory, or the command isn't run as root. It stops before changing anything and says which.
- Ports 80 or 443 are already in use by something else. It stops before changing anything,
  naming the port and what holds it.
- Nomad or Docker is already installed by someone else. It stops and says so, rather than taking
  over a cluster it didn't set up.
- The run is interrupted halfway (network drop, Ctrl-C). Running the command again finishes the
  install.
- The download of a file is corrupted or tampered with. The checksum check fails and nothing is
  installed.
- An ARM VM reports no CPU clock speed. The install still gives Nomad a CPU capacity, as the
  rehearsal does.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: One command, run as root on a fresh Ubuntu 24.04 VM (amd64 or arm64), MUST install
  Docker, Nomad with ACLs on, Traefik with Let's Encrypt, and Caravana under systemd.
- **FR-002**: The command MUST take the admin email, the console domain and the Let's Encrypt
  email as options. Only the admin email is required.
- **FR-003**: Without a console domain, the command MUST use a name derived from the VM's public
  IP address.
- **FR-004**: The console and the API MUST be served over HTTPS on the console domain, with a
  trusted certificate. Webhook calls MUST work there too.
- **FR-005**: The command MUST check the OS, architecture, memory, root, free ports 80 and 443,
  no foreign Nomad or Docker, and the domain's DNS before changing anything, and stop naming the
  first check that fails.
- **FR-006**: Every file the command downloads MUST be checked against a published checksum
  before it is used.
- **FR-007**: Caravana MUST run as a systemd service under its own user, with a Nomad token
  limited to Caravana's policy, and start without waiting on any Nomad job.
- **FR-008**: The host MUST answer from the internet on 22, 80 and 443 only.
- **FR-009**: In a terminal, the command MUST create the admin, asking for the password twice
  without echoing it. Without a terminal, it MUST print the command that creates the admin.
- **FR-010**: The command MUST end by printing the console's address and the next step.
- **FR-011**: Running the command again MUST be safe: it finishes an interrupted install, changes
  nothing when nothing changed, and says what it changed.
- **FR-012**: Running the command for a newer version MUST upgrade Caravana in place, keeping its
  data, admin, tokens, certificates and running apps.
- **FR-013**: If the upgraded Caravana fails its health check, the command MUST put the previous
  version back and exit with an error.
- **FR-014**: The command MUST refuse a version older than the installed one before changing
  anything.
- **FR-015**: An uninstall command MUST remove Caravana's service and binary, and its data only
  when asked, leaving Nomad, Traefik and every app running.
- **FR-016**: Admin commands (create the admin, reset the password, make a token) MUST work on the
  host as `sudo caravana admin …`, with no extra setup.
- **FR-017**: Each tagged release MUST publish the installer, Caravana binaries for amd64 and
  arm64, and their checksums. The installer MUST install the binaries of its own release.
- **FR-018**: The rehearsal (`deploy/vm/host.sh`) MUST set hosts up with this installer, so the
  gate proves the same install users run.

### Key Entities

- **Release**: a published Caravana version: the installer, one binary per architecture, and a
  checksum file. The installer is tied to its release.
- **Installed host**: a VM the command set up. It records the installed version, the console
  domain and the Let's Encrypt settings, so a later run can upgrade, change or uninstall.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: On a fresh 2 GB VM, the one command finishes in under 10 minutes and the printed
  address answers over trusted HTTPS.
- **SC-002**: A first-time user goes from a fresh VM to a deployed app with one pasted command and
  the console, without reading anything but the README.
- **SC-003**: The gate passes against a host set up by the installer, every step included.
- **SC-004**: An upgrade keeps every running app answering throughout, and keeps all stored data.
- **SC-005**: A second run with nothing changed changes nothing.
- **SC-006**: After an uninstall, every app still answers over HTTPS.

## Assumptions

- Ubuntu 24.04 LTS is the only supported OS, the one the rehearsal used. Others come later.
- The console is public on its domain by default, protected by the admin sign-in and API tokens
  from feature 006, which were built for this. Nomad, Traefik's dashboard and Caravana's own
  ports stay on loopback.
- One console domain serves the console, the API and webhook calls. The separate webhook host
  from feature 005 is no longer needed. Each app still brings its own domains.
- Without a domain, `sslip.io` provides one from the public IP, as in the Phase 1 gate run. It is
  a public third-party service, fine for a first try. Real installs pass their own domain.
- The installer is published with each GitHub release. The usual form downloads the latest
  release's installer and pipes it to a shell. Publishing a release is the maintainer's action
  (pushing a tag).
- Uninstall keeps Nomad, Docker and Traefik, because the apps need them. Removing those is the
  owner's call and is documented, not automated.
- Let's Encrypt production is the default. A staging option stays for testing, as in the
  rehearsal.
