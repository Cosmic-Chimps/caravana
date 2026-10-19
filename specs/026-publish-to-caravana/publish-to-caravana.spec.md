# Feature Specification: PublishToCaravana (Aspire)

**Feature Branch**: `026-publish-to-caravana` (Caravana side, stacked on `020.6-terminal`) and the
repository `Cosmic-Chimps/aspire-hosting-caravana` (local clone `../aspire-hosting-caravana`)

**Created**: 2026-10-19

**Status**: Draft

**Input**: the maintainer, 2026-10-19: "we should start with PublishToCaravana; we have to create a
new project and folder like we did with caravana-action". docs/plan.md 026; docs/integrations.md.
First user: Frunchy (an F# Aspire AppHost: an ASP.NET app and Postgres, later more services).

## User Scenarios & Testing *(mandatory)*

### User Story 1 - `aspire deploy` to Caravana (Priority: P1)

The owner adds `builder.PublishToCaravana("frunchy", ...)` to their AppHost with Caravana's address,
a token, the project and environment, and marks which resource gets a domain. `aspire deploy` builds
and pushes the images (as Aspire does), turns the AppHost into one Caravana app (each resource a
service; Postgres with its volume), deploys it, and waits until it is healthy, printing the
deploy's warnings, or fails with Caravana's reason. Running it again updates the same app.

**Why this priority**: the AppHost becomes the one description of the app; no hand-written
definition next to it.

**Independent Test**: an AppHost with a project resource, Redis with a password parameter and a
container with a data volume: `aspire deploy` against a Caravana creates one app with those
services, each reachable from the others by name, the project's domain answering, the volume
created, and no secret value in the stored app or Compose file. A second run deploys a new version
of the same app.

**Acceptance Scenarios**:

1. **Given** an AppHost with `PublishToCaravana`, **When** `aspire deploy` runs, **Then** one app is
   deployed in the configured project and environment, with one service per resource (the
   dashboard only when asked for), and the step ends when the deploy is healthy.
2. **Given** a parameter marked secret (a password, a connection string that contains it), **When**
   the app is deployed, **Then** its value reaches the service only as a Caravana secret: never in
   the Compose file sent, the stored app, a log line or the console.
3. **Given** a resource with a data volume, **When** deployed, **Then** it gets a Caravana volume
   with a valid name derived from the resource, the same on every run, so data survives redeploys.
4. **Given** a deploy that fails or is reverted, **When** the step waits, **Then** `aspire deploy`
   fails with Caravana's reason and the deploy's warnings.
5. **Given** the same AppHost deployed again, **When** nothing changed but the images, **Then** the
   same app is redeployed (no duplicate apps), and changes someone made in the console to things
   the AppHost doesn't describe (secrets set by hand) are kept.

---

### User Story 2 - What Compose can't say (Priority: P1)

Per resource, the owner can give: a domain (literal or a parameter), the health check path and
port, a backup policy (Postgres dump or volume archive, schedule, how many to keep), memory, and
exclusion. Defaults are sensible: a resource with an external HTTP endpoint and no domain gets
none; health is the first port over TCP unless a path is given.

**Acceptance Scenarios**:

1. **Given** `WithCaravanaDomain(...)` on a project, **When** deployed, **Then** that host routes to
   it over HTTPS.
2. **Given** `WithCaravanaBackup(...)` on Postgres, **When** deployed, **Then** the service has that
   backup policy (and Caravana refuses it, naming the field, if no backup destination is set).
3. **Given** `WithCaravanaExclude()`, **When** deployed, **Then** that resource isn't part of the app.

---

### User Story 3 - Caravana takes what Aspire writes (Priority: P1)

Aspire's generated Compose file deploys on Caravana as it comes, once its variables are resolved:
its default network and named volumes with `driver` set, and `entrypoint` (Aspire's Redis uses it)
are accepted, and the warnings are few and useful.

**Acceptance Scenarios**:

1. **Given** real Aspire output (fixtures in Caravana's tests), **When** previewed, **Then** it
   translates without an error and with at most one warning about networks.
2. **Given** a service with `entrypoint`, **When** deployed, **Then** the container runs that
   entrypoint with the service's command as its arguments, as in Compose.

### Edge Cases

- The project or environment doesn't exist: created when the token may (a full token); with a
  deploy token the step fails saying to create them first.
- A parameter without a value at deploy time: Aspire asks or fails as it does today; never sent
  empty.
- An image on a private registry: the step says Caravana needs credentials for that registry
  (Settings › Registries) when Caravana answers `image_not_found` or `registry_auth_failed`.
- The app was edited from the console as a definition (not from Compose): the next run replaces it
  from the AppHost, with Caravana's warning that the kept file changes.

## Requirements *(mandatory)*

- **FR-001**: A NuGet package `CosmicChimps.Aspire.Hosting.Caravana` MUST add
  `PublishToCaravana(name, configure)` to an AppHost, deploying it to Caravana as one app in a
  project and environment during `aspire deploy`, after Aspire builds and pushes the images.
- **FR-002**: It MUST send Aspire's Compose output with every variable resolved, the dashboard left
  out unless asked for, volume names made valid and stable, and every environment value that holds
  a secret parameter moved to the overlay's secrets.
- **FR-003**: It MUST offer per-resource domain, health, backup, memory and exclusion settings.
- **FR-004**: It MUST wait for the deploy and fail `aspire deploy` with Caravana's reason when it
  fails, and print the deploy's warnings.
- **FR-005**: It MUST never print or log a secret value or the token.
- **FR-006**: Caravana MUST accept Aspire's Compose output as generated (network and volume
  `driver`, `entrypoint`), with fixtures of real output in its tests.
- **FR-007**: Re-running MUST update the same app (keyed by project, environment and app name).

## Success Criteria *(mandatory)*

- **SC-001**: Frunchy's AppHost deploys to the maintainer's Caravana with `aspire deploy` and no
  hand-written definition.
- **SC-002**: A secret parameter's value is found nowhere outside Nomad Variables after a deploy.
- **SC-003**: Two runs of `aspire deploy` give one app with two deployments.

## Assumptions

- Aspire 13.6.1, .NET 10, the Docker Compose publisher (`Aspire.Hosting.Docker`).
- The package is MIT (like the Dokploy one and caravana-action); it is a client of Caravana's API.
- It follows the API until 1.0 (Phase 2.6's end); we maintain both.

## Out of Scope

- Building on the host (Phase 3), Aspire-style service discovery rendered from Nomad (references
  already resolve by service name inside one app), Bella Baxter as a secrets provider (027).
