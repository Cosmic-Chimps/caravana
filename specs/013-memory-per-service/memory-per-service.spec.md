# Feature Specification: Memory per service

**Feature Branch**: `013-memory-per-service`
**Created**: 2026-10-11
**Status**: Implemented, v0.1.1
**Input**: "Memory per service (backlog item in docs/plan.md): the 64 MB default (CARAVANA_DEFAULT_MEMORY_MB) is a hard Nomad limit that kills Node.js, .NET, Postgres at start unless every Compose file sets mem_limit. Raise the default (e.g. 256 MB) and let a service burst into the node's spare memory beyond what it reserves (Nomad memory_max / memory oversubscription), so a short peak isn't a kill. Then release v0.1.1 to test the installer's upgrade on the real netcup host."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - An app that says nothing about memory just runs (Priority: P1)

An owner deploys a Compose file written for `docker compose`, with no memory settings: a Node.js
web app and Postgres. Today each service gets 64 MB, a hard limit, and the app is killed as it
starts, with nothing on the page saying why. With this feature each service reserves a sensible
amount and may use more while the node has it spare, so the app starts and handles a peak
without being killed.

**Why this priority**: every real app hits this today, and the fix is the reason for the feature.

**Independent Test**: deploy the Hollo example without its memory settings. Both services reach
healthy and stay up.

**Acceptance Scenarios**:

1. **Given** a service with no memory settings, **When** it is deployed, **Then** it reserves the
   installation's default reservation and may use up to the installation's default ceiling.
2. **Given** a running service that briefly needs more than it reserves, **When** the node has
   spare memory, **Then** the service keeps running.
3. **Given** a service that goes over its ceiling, **When** that happens, **Then** it is stopped
   and restarted as today, and the copy's state in the console says it ran out of memory.

---

### User Story 2 - An owner sets memory per service (Priority: P2)

An owner knows their service: Postgres should reserve 512 MB and never pass 1 GB. They set that
in the app's definition, or in Compose the way Compose says it (`mem_reservation` and
`mem_limit`). The console's definition shows both numbers for each service.

**Why this priority**: defaults fit most services, but a database or a heavy worker needs its own
numbers.

**Independent Test**: deploy a service with a reservation and a ceiling from Compose and from the
API. Nomad's job carries both, and the console shows both.

**Acceptance Scenarios**:

1. **Given** a Compose service with `mem_limit: 1g` and `mem_reservation: 512m`, **When** it is
   deployed, **Then** it reserves 512 MB and may use up to 1024 MB.
2. **Given** a Compose service with only `mem_limit: 768m`, **When** it is deployed, **Then** its
   ceiling is 768 MB and it reserves the smaller of 768 MB and the default reservation.
3. **Given** a reservation larger than the ceiling, **When** the app is deployed, **Then** it is
   refused, naming the field.
4. **Given** an app with memory settings, **When** the owner opens its definition in the console,
   **Then** each service shows its reservation and its ceiling.

---

### User Story 3 - The upgrade to this version runs on the real host (Priority: P3)

The owner's netcup host runs `v0.1.0` with Hollo on it. This feature is released as `v0.1.1`.
The owner runs the install command again. Caravana upgrades in place, the host is set up to let
services burst, Hollo keeps answering, and the next deploy of Hollo uses the new memory rules.

**Why this priority**: it is the first upgrade on a host with real data, which the installer's
rollback path has only shown on a test VM.

**Independent Test**: on the host, rerun the install command and check the version, Hollo's
answers during the upgrade, and a redeploy of Hollo afterwards.

**Acceptance Scenarios**:

1. **Given** a host on `v0.1.0`, **When** the owner runs the install command for `v0.1.1`,
   **Then** Caravana reports `0.1.1`, its data and apps are unchanged, and the command lists what
   it changed.
2. **Given** the upgraded host, **When** Hollo is deployed again, **Then** its services get the
   new memory rules.

---

### Edge Cases

- Apps deployed before the upgrade keep the job they run with until their next deploy. Nothing is
  redeployed behind the owner's back.
- A node without enough unreserved memory for a service's reservation can't place it. The
  deployment fails with Nomad's reason, as today.
- An installation whose Nomad doesn't allow services to use more than they reserve (bursting is
  off): the ceiling has no effect. The deployment's warnings say so, rather than hiding it.
- An owner sets a ceiling equal to the reservation: no bursting for that service, which is
  allowed.
- Compose's `deploy.resources.reservations.memory` is the same as `mem_reservation`;
  `deploy.resources.limits.memory` is the same as `mem_limit`.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: A service MUST have a memory reservation, which the scheduler counts, and a memory
  ceiling, which it may use while the node has memory spare.
- **FR-002**: A service with no reservation MUST get the installation's default reservation, 256 MB
  unless the operator changes it.
- **FR-003**: A service with no ceiling MUST get the larger of its reservation and the
  installation's default ceiling, 1024 MB unless the operator changes it.
- **FR-004**: A reservation larger than the ceiling MUST be refused, naming the field.
- **FR-005**: Compose's `mem_reservation` and `deploy.resources.reservations.memory` MUST set the
  reservation. `mem_limit` and `deploy.resources.limits.memory` MUST set the ceiling.
- **FR-006**: A Compose service with a ceiling and no reservation MUST reserve the smaller of the
  ceiling and the default reservation.
- **FR-007**: The API MUST accept and return both numbers per service, and `api/openapi.yaml` MUST
  describe them in the same change.
- **FR-008**: The console's definition MUST show each service's reservation and ceiling.
- **FR-009**: The installer MUST set up Nomad so services can use memory beyond their reservation,
  on new installs and on upgrades.
- **FR-010**: A deployment on a Nomad that doesn't allow it MUST say, in its warnings, that the
  ceiling has no effect.
- **FR-011**: The operator MUST be able to change both defaults in Caravana's settings.
- **FR-012**: This feature MUST ship as release `v0.1.1`, and the upgrade from `v0.1.0` MUST keep
  data and running apps.

### Key Entities

- **Service resources** (existing): CPU, plus memory as two numbers. The existing memory value
  becomes the reservation; the ceiling is new.
- **Installation defaults**: the default reservation and the default ceiling, from Caravana's
  settings.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: The Hollo example deploys and stays healthy with no memory settings in its Compose
  file.
- **SC-002**: A service that briefly uses three times its reservation, on a node with that much
  free, is not killed.
- **SC-003**: Upgrading the real host from `v0.1.0` to `v0.1.1` keeps every app answering and loses
  no data.
- **SC-004**: Every memory value an owner sets is visible in the console's definition.

## Assumptions

- The defaults are 256 MB reserved and 1024 MB ceiling. Small services fit the reservation, and
  Node.js, .NET and Postgres fit the ceiling. A 2 GB node still schedules several services.
- The existing `memoryMB` field keeps its name and now means the reservation. An app that set it
  could only use that much before; now it can use up to the default ceiling. That is more
  permissive, so no client breaks. The new field is the ceiling.
- Bursting is a Nomad scheduler setting for the whole cluster. The installer turns it on, and so
  does the local lab.
- A service over its ceiling is killed by the kernel and restarted by Nomad, as today. Showing
  "out of memory" relies on what Nomad reports for the copy.
- Releasing `v0.1.1` (pushing the tag) and upgrading the host are the maintainer's actions.
