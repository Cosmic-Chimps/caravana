# Feature Specification: Compose translator

**Feature Branch**: `009-compose-translator`

**Created**: 2026-10-07

**Status**: Draft

**Input**: User description: "009 Compose translator (docs/plan.md): `compose-go`; loud errors
for unsupported fields, warnings for changed ones, explicit host volumes. Phase 2 gate: a real
multi-service compose app runs, with a database on a host volume and a backup that restores
(backups are feature 010)."

## Context

Today Caravana deploys apps described in its own JSON model: one or more services, each a
prebuilt image. Most self-hosters already describe their apps as a Docker Compose file. Feature
009 lets a client send that Compose file as it is. Caravana reads it, turns it into the same app
model every other input becomes, and deploys it the usual way: digest-pinned, health-checked,
blue/green.

Compose and Nomad don't mean the same things everywhere. The rule is the constitution's
principle IV: what Caravana can honor exactly, it honors; what it honors with a different
meaning, it honors and says so in a warning; what it can't honor, it refuses, naming the field.
Nothing in the file is ever dropped without a word.

Who uses this:

- **API clients**: CI, scripts and, in Phase 2.5, the .NET Aspire integration, which publishes
  an app as a Compose file. They send the file plus the things Compose has no words for (public
  domains, health checks Nomad can run, secret values).
- **The admin**, through the API for now. The console shows Compose apps like every other app.

Words used here:

- **Compose file**: the YAML a user would run with `docker compose up`.
- **Overlay**: the Caravana-only part of the request, next to the file: per service, its
  domains, its health check and its secrets.
- **Volume**: a named Compose volume. On Caravana it is a directory on one node that keeps its
  data across deploys.

## Clarifications

### Session 2026-10-07

Answered with the recommended option (unattended run, the maintainer's standing preference).

- Q: Where do domains, health checks and secret values go: inside the file (an `x-caravana`
  extension) or next to it in the request? → A: Next to it, in a JSON overlay. The file stays
  exactly what `docker compose up` runs, secret values never sit in a YAML file, and generated
  clients get typed fields. `x-` keys in the file are ignored.
- Q: Does deleting an app delete its volumes? → A: Yes, and the typed delete confirmation names
  each volume and says its data is erased. Orphaned data nobody can see or remove is the worse
  surprise.
- Q: A Compose service name that isn't a DNS label (`Web_1`)? → A: Refused, naming the service.
  Renaming it would change the name other services use to reach it.
- Q: Does 009 add a console form for Compose? → A: No. API and preview first; Compose apps show
  in the console like any app.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Deploy a Compose file (Priority: P1)

A client sends a Compose file with two services: `web`, from a public image, listening on port
80, and `worker`, with no ports. The overlay gives `web` the domain `blog.example.com`. Caravana
answers like any deploy: the deployment ID, the Nomad job version, and the warnings. `web`
answers over HTTPS on its domain and `worker` runs. Redeploying (empty body, console, webhook)
deploys the same app again; sending a changed file replaces the app's definition.

**Why this priority**: it is the feature. Everything else here refines what a file may contain.

**Independent Test**: send a two-service Compose file with a domain for one service, check both
services run, the domain answers, and the deploy result lists the warnings for what changed in
meaning.

**Acceptance Scenarios**:

1. **Given** no app `blog`, **When** a client sends a Compose file with `web` (image, port 80)
   and `worker` (image, command) and a domain for `web`, **Then** the deploy is accepted, both
   services run, and `blog.example.com` answers from `web`.
2. **Given** that file also publishes `web`'s port as `8080:80`, **When** it is deployed,
   **Then** it works and the result has a warning on that port saying the host port isn't
   opened and the service is reached through its domain or by other services.
3. **Given** that file, **When** a client asks for a preview instead of a deploy, **Then** the
   answer is the app definition Caravana would deploy plus the warnings, and nothing is saved or
   deployed.
4. **Given** the deployed app, **When** a client reads it, **Then** its source says Compose and
   its services are the ones in the file.
5. **Given** the deployed app, **When** the webhook redeploys it with a new image tag digest,
   **Then** it redeploys from the stored definition without the file.

---

### User Story 2 - Refuse what can't run, warn about what changes (Priority: P1)

A client sends a file that uses something Caravana can't honor: a `build:` section, a bind mount
of a host path, `privileged: true`, a field Caravana doesn't know. The deploy fails before
anything is saved, and the error names the field as a path in the file
(`services.web.build`). A file that uses something Caravana honors differently (`depends_on`,
a `healthcheck` command, `restart: "no"`, `container_name`) deploys, and each difference is a
warning naming its field.

**Why this priority**: a deploy that silently lost a volume or a setting is worse than one that
failed (constitution IV). Without this, story 1 is unsafe.

**Independent Test**: send files that each use one refused field and check each fails naming it;
send one with every warned field and check each warning is there.

**Acceptance Scenarios**:

1. **Given** a file with `services.web.build`, **When** it is sent, **Then** the deploy fails
   with an error whose field is `services.web.build`, and nothing is saved.
2. **Given** a file with an unknown key under a service, **When** it is sent, **Then** it fails
   naming that key.
3. **Given** a file where `web` has `depends_on: {db: {condition: service_healthy}}`, **When**
   it is deployed, **Then** it works and a warning on `services.web.depends_on` says services
   start together and Nomad restarts the ones that fail until their dependencies answer.
4. **Given** a file where `web` has a `healthcheck` command and no overlay health check, **When**
   it is deployed, **Then** `web` is checked over TCP on its port and a warning on
   `services.web.healthcheck` says so.
5. **Given** a file with an `${API_HOST}` variable that has no default, **When** it is sent,
   **Then** it fails naming the variable: Caravana never reads its own environment into a file.

---

### User Story 3 - Services reach each other by name (Priority: P1)

In Compose, `web` reaches the database at `db:5432`. On Caravana the same file works: every
service of the app can reach the others by their Compose service names, on their container
ports. When `db` is redeployed, `web` keeps reaching it.

**Why this priority**: almost every real multi-service file depends on it, and the Phase 2 gate
is a web app talking to its database.

**Independent Test**: deploy a file where one service fetches a page from another by its name,
check it works; redeploy the other service and check it still works afterwards.

**Acceptance Scenarios**:

1. **Given** a file with `api` (HTTP on port 8080) and `web`, **When** it is deployed, **Then**
   from inside `web`, `http://api:8080/` answers from `api`.
2. **Given** that app, **When** `api` is redeployed (a new image), **Then** once the rollout is
   done, `web` reaches the new `api` by the same name.
3. **Given** a file with a service `api` that has no ports, **When** another service uses the
   name `api`, **Then** nothing answers there (as in Compose), and no error is raised.

---

### User Story 4 - Volumes keep their data (Priority: P2)

A file declares a named volume `db-data` and mounts it into `db` at `/var/lib/postgresql/data`.
Caravana creates a matching volume for the app, mounts it, and keeps its data across deploys,
restarts and redeploys. Because two copies can't share it, a service with a volume runs one
copy, and its deploys replace the copy instead of running the new one beside it.

**Why this priority**: a database without durable storage isn't a database. It comes after the
three stories above because a stateless app is already useful.

**Independent Test**: deploy a service that writes a file into its volume, redeploy it, check
the file is still there.

**Acceptance Scenarios**:

1. **Given** a file with volume `db-data` mounted into `db`, **When** it is deployed, **Then**
   the volume exists for the app and `db` sees it at the mount path.
2. **Given** data written into the volume, **When** `db` is redeployed, **Then** the new copy
   sees the same data.
3. **Given** a service with a volume and `replicas: 2`, **When** it is sent, **Then** it fails
   naming the replicas field: a volume serves one copy.
4. **Given** a file with a bind mount `./data:/data` or `/srv/data:/data`, **When** it is sent,
   **Then** it fails naming the volume and saying to use a named volume.
5. **Given** a volume that two services mount, **When** it is sent, **Then** it fails naming the
   second mount: each volume belongs to one service.

---

### User Story 5 - Deleting an app removes its volumes (Priority: P3)

When the admin deletes a Compose app, its volumes go with it, and the delete confirmation says
which volumes and that their data is erased.

**Why this priority**: it matters once, at delete time, but leaving data behind without saying
so would be a surprise either way.

**Independent Test**: delete an app with a volume, check the volume is gone and the console's
confirmation named it.

**Acceptance Scenarios**:

1. **Given** app `blog` with volume `db-data`, **When** the Configure tab's delete section is
   opened, **Then** it says the volume `db-data` and its data are erased.
2. **Given** that, **When** the app is deleted, **Then** the volume is gone.

### Edge Cases

- **A file that isn't valid YAML, or isn't a Compose file**: refused with the parser's message
  and the line when it has one.
- **An empty file, or one without services**: refused on `services`.
- **A service without `image`** (only `build`): refused on `services.<name>.build`.
- **A service name that isn't a DNS label** (`Web_1`): refused naming the service. Caravana
  doesn't rename services silently.
- **More than one port with a domain**: the overlay says which port each domain uses; when a
  service has one port, the port can be left out.
- **An overlay entry for a service the file doesn't have**: refused naming the overlay entry.
- **Environment entries without a value** (`- DEBUG`): refused naming the entry, because there
  is no shell to take it from.
- **`env_file`, Compose `secrets` or `configs`**: refused, naming the field, with the hint to
  send values as plain environment in the file or as secrets in the overlay.
- **`x-` extension keys**: ignored, as Compose itself does.
- **The same file twice**: the second deploy changes nothing but adds a deployment record
  (constitution III).
- **A volume removed from the file**: the next deploy stops mounting it. Its data stays until
  the app is deleted, and the deploy result warns that it is no longer mounted.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: Clients MUST be able to deploy an app from a Compose file plus an overlay, through
  the API, with the same answer as any deploy (deployment ID, job version, warnings).
- **FR-002**: Clients MUST be able to preview a Compose file: the app definition and warnings it
  would produce, without saving or deploying anything.
- **FR-003**: Every Compose input MUST become the same app model as every other input; after
  that, nothing reads the file.
- **FR-004**: Every field of the file MUST be honored, honored with a warning naming the field,
  or refused with an error naming the field. No field is ignored without a word, except `x-`
  extensions.
- **FR-005**: Refused input MUST fail before anything is saved or deployed.
- **FR-006**: Field paths in errors and warnings MUST be paths in the Compose file
  (`services.web.ports[0]`) or in the overlay (`overlay.web.domains[0]`).
- **FR-007**: Variables (`${NAME}`) MUST come only from the file's own defaults; a variable
  without a value MUST be refused naming it.
- **FR-008**: The overlay MUST let a client give each service its domains, its health check and
  its secrets, with the same rules and meaning as the app model (secrets per feature 008).
- **FR-009**: A service with ports and no overlay health check MUST get a TCP check on its first
  port; a Compose `healthcheck` it replaces MUST produce a warning.
- **FR-010**: Services of one app MUST reach each other by their Compose service names on their
  container ports, also after any of them is redeployed.
- **FR-011**: Each named volume MUST become a durable volume of the app, created by Caravana,
  mounted where the file says; anything else in `volumes` (bind mounts, tmpfs, external
  volumes, volume drivers) MUST be refused naming it.
- **FR-012**: A service with a volume MUST run exactly one copy, and its deploys MUST replace the
  copy rather than run two at once; asking for more copies MUST be refused.
- **FR-013**: Deleting an app MUST delete its volumes, and the console's delete confirmation
  MUST name them.
- **FR-014**: The app's source MUST record that it came from Compose; reads and the console show
  it.
- **FR-015**: Caravana's scoped Nomad policy MUST allow what volumes need, and the gate MUST
  have a step that deploys a Compose app with two services talking by name and a volume that
  keeps data across a redeploy.
- **FR-016**: Every new endpoint and field MUST be in `api/openapi.yaml` in the same change.

### Key Entities

- **Compose file**: what the client sends; never stored.
- **Overlay**: per service, domains, health check and secrets.
- **Volume**: a named directory of one app on one node, mounted by one service, kept across
  deploys, removed with the app.
- **App source**: gains the kind Compose.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A two-service Compose app (web plus a database on a volume) deploys from its file
  and an overlay with one domain, with no edits to the file.
- **SC-002**: For a fixture set covering every Compose service field, 100% of fields are either
  honored, warned about or refused, and each warning and error names its field.
- **SC-003**: A refused file leaves zero saved apps, deployments, Nomad jobs or volumes.
- **SC-004**: After 3 redeploys of a service, another service of the app still reaches it by
  name in 100% of checks made once each rollout ends.
- **SC-005**: A file written into a volume survives 3 redeploys of its service.
- **SC-006**: The gate passes on the lab and a real host with the scoped policy.

## Assumptions

- **The file travels in a JSON request** next to its overlay, so the API stays JSON-only and
  clients generated from the OpenAPI contract handle it like any other call.
- **No console form for Compose in 009**: the API and its preview come first; Compose apps show
  in the console like any app.
- **The file isn't stored**: the stored app is the normalized model, which is what redeploys use.
  Export (011) works from the model.
- **No build, no file reads**: images only (git builds are Phase 4); anything that needs a file
  from the client's disk (`env_file`, bind mounts, Compose secrets and configs, `include`) is
  refused.
- **One node** (Phase 2). Volumes live on the node Nomad picks first; multi-node placement is
  Phase 3.
- **`depends_on` doesn't order starts**: Nomad starts every service together; services that fail
  because a dependency isn't up yet are restarted, which is why it is a warning.
- **Compose resource limits become Nomad reservations**: `deploy.resources.limits.memory` is the
  memory limit, `cpus` becomes CPU in MHz (1 CPU = 1000 MHz), with a warning since Nomad shares
  CPU differently.

## Verbatim Constraints

- Library: `compose-go`
- Contract file: `api/openapi.yaml`
