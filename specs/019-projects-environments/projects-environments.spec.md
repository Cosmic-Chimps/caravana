# Feature Specification: Projects and Environments

**Feature Branch**: `019-projects-environments`

**Created**: 2026-10-17

**Status**: Implemented, v0.6.0

**Input**: "Organization / Project / Environment (docs/plan.md, Phase 2.5, 019). Projects group
apps; environments (production, staging, ...) hold each project's own copies of its apps with
their own env, secrets and domains, and promotion between them. The organization is the
installation until teams (Phase 4). Existing apps move to a default project and a `production`
environment, so nothing running moves. Every feature keys state (jobs, variables, volumes, backup
paths, routers) on the bare app ID, so it gets costlier the longer it waits. Phase 2.5's gate: a
git-built app goes staging → production, and a failure notifies."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Group apps into projects and environments (Priority: P1)

The owner runs a shop (a web app, a worker and Postgres) and a blog on the same Caravana. They
create a project "shop" (it starts with one environment, `production`) and add a `staging`
environment to it. They create the web app in staging and in production: the same app name,
`web`, in both, each with its own env values, secrets and domains (`staging.shop.example` and
`shop.example`). The console's home shows the projects with their environments and apps, and
every app page says which project and environment it belongs to.

**Why this priority**: without it there is no staging at all; today two copies of one app are two
unrelated apps with made-up names.

**Independent Test**: create project `shop` with `staging`, create app `web` in both
environments with different env values, secrets and domains, deploy both. Each answers on its
own domain with its own values; neither can read the other's secrets; the home groups them under
shop › staging and shop › production.

**Acceptance Scenarios**:

1. **Given** a fresh project, **When** it is created, **Then** it has one environment named
   `production`.
2. **Given** a project, **When** the owner adds an environment, **Then** it is empty and listed
   after the existing ones.
3. **Given** two environments of one project, **When** an app of the same name is created in
   each, **Then** both exist side by side, each with its own definition, env, secrets, domains,
   volumes, backups, webhook token and history.
4. **Given** an app in one environment, **When** its service reads its secrets, **Then** it sees
   only its own environment's values, never another environment's.
5. **Given** a project or environment that still has apps, **When** the owner tries to delete it,
   **Then** it is refused, naming the apps that are still in it.

---

### User Story 2 - Nothing moves on upgrade (Priority: P1)

The owner upgrades an installation that already runs apps. After the upgrade every app is in a
project named "Default", in its `production` environment. Nothing is redeployed or restarted.
Every app keeps its ID, its console address, its API address, its webhook URL and token, its
domains, its volumes and its backups, and scripts that call the API keep working unchanged.

**Why this priority**: the installations that exist today run real apps (Hollo, Uptime Kuma); an
upgrade that moves or restarts them is not acceptable.

**Independent Test**: on an installation with apps (including one with a volume, a backup and a
webhook token), upgrade. The home shows Default › production with all of them; no Nomad job
gets a new version; the old webhook URL still redeploys; the old API paths still answer.

**Acceptance Scenarios**:

1. **Given** apps from before the upgrade, **When** the new version starts, **Then** each is in
   project Default, environment `production`, with its ID unchanged.
2. **Given** an app from before, **When** its webhook is called with its old token, **Then** it
   redeploys as before.
3. **Given** a new app created in Default › production after the upgrade, **When** it is created,
   **Then** its ID is its name, exactly as before the feature.

---

### User Story 3 - Promote what was tested (Priority: P1)

The owner deploys a new version of `web` to staging, checks it, and presses "Promote to
production". Production then runs exactly the images staging ran (the same digests, not whatever
the tag points to now) and staging's service shape (commands, arguments, ports, health checks,
volume mounts), while it keeps its own env values, secrets, domains, memory, number of copies and
backup settings. The deploy shows in production's history as promoted from staging's deployment,
and its notification says so.

**Why this priority**: promotion is the reason environments exist and the second half of the
Phase 2.5 gate ("a git-built app goes staging → production").

**Independent Test**: deploy `web` to staging with a tag, move the tag to a different image in
the registry, promote: production runs the digest staging ran. Production's env values,
secrets and domains are unchanged. The production deployment names the staging deployment it
came from.

**Acceptance Scenarios**:

1. **Given** an app whose last deploy in the source environment is healthy, **When** it is
   promoted to another environment of the same project that has an app of the same name,
   **Then** that app is deployed with the source's image digests and service shape and keeps its
   own env, secrets, domains, memory, copies and backups.
2. **Given** the promote form, **When** the owner opens it, **Then** it shows what will change in
   the target before anything is deployed: images (from, to) and any shape change per service.
3. **Given** a source app whose last deploy failed or is still running, **When** the owner
   promotes, **Then** it is refused: only a healthy deployment can be promoted.
4. **Given** a source app with a service the target lacks, **When** promoted, **Then** the
   service is added to the target with the source's shape and env values and without domains;
   if it needs a secret the target doesn't have, the promotion is refused, naming the secret.
5. **Given** a target app with a service the source lacks, **When** promoted, **Then** it is
   refused, naming the service (removing a service, maybe with its data, is never a side effect
   of a promotion).
6. **Given** a target environment with no app of that name, **When** the owner promotes, **Then**
   it is refused, with a way to copy the app into that environment first (Story 4).
7. **Given** an image that was deleted from the registry since the source deployed it, **When**
   promoted, **Then** it is refused before anything changes, naming the image.

---

### User Story 4 - Copy an app into another environment (Priority: P2)

The owner has `web` in production and wants it in staging too. "Copy to environment" creates
`web` in staging with production's definition (images, shape, env values), without domains and
without secret values; the secrets it needs are listed for the owner to fill in. The copy is not
deployed until the owner deploys it. Its volumes start empty.

**Why this priority**: creating every app of a project again by hand, field by field, is what
would keep people from using staging.

**Independent Test**: copy an app with a domain, two secrets and a volume to a new environment.
The copy exists, undeployed, with the same services and env, no domains, the two secret names
marked as missing, and its own (empty) volume once deployed.

**Acceptance Scenarios**:

1. **Given** an app, **When** it is copied to another environment of its project, **Then** the
   copy has the same services, images, env values and backup settings, no domains, and no secret
   values.
2. **Given** a copy whose secrets have no values yet, **When** the owner deploys it without
   setting them, **Then** the deploy is refused, naming the missing secrets.
3. **Given** a target environment that already has an app of that name, **When** the owner
   copies, **Then** it is refused (promotion is the way to update it).

---

### User Story 5 - Address apps by project and environment from the API (Priority: P2)

A CI script or the .NET Aspire integration deploys `web` to `shop`'s staging. It can list
projects, environments and their apps, create projects and environments, create an app in an
environment, promote and copy, and find any app's ID from its project, environment and name, then
use the existing app operations with that ID.

**Why this priority**: the console and the API reach the same things (constitution V); Aspire and
build-from-source (020) need to target an environment.

**Independent Test**: with an API token only, create project `shop`, environment `staging`, app
`web` in it, deploy, promote to production, and read both apps' deployments.

**Acceptance Scenarios**:

1. **Given** an API token, **When** a client creates a project, an environment and an app in it,
   **Then** each is visible in the console the same way.
2. **Given** a project, environment and app name, **When** a client asks for that app, **Then** it
   gets the app with its ID, and every existing app operation works with that ID.
3. **Given** a client that only knows the old app paths, **When** it calls them with an app ID,
   **Then** they answer as before, now with the app's project and environment included.

---

### Edge Cases

- A project, environment or app name that is not a lowercase DNS label, or one with a double
  dash, is refused, naming the field.
- A combination whose ID would exceed the length Caravana allows for an app ID is refused with
  the length it has and the length allowed, before anything is created.
- Two environments of one project can't have the same name; two projects can't have the same
  name. Project display names may contain anything printable.
- The Default project and its `production` environment can't be deleted (they hold the apps
  that existed before the feature, whose IDs depend on them). Default can be renamed for display.
- Promotion between environments of different projects is refused.
- A promotion while the target is mid-deploy behaves like any deploy that arrives then: the
  newest deploy wins.
- The needs-you list, notifications, the activity feed and the app list name each app's project
  and environment, so "web failed" says which `web`.
- An app's webhook token, backups and volumes belong to that environment's app only; copying an
  app never copies its token, its backup runs or its volume contents.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The installation MUST hold projects; each project MUST hold one or more ordered
  environments; each environment MUST hold apps. An app belongs to exactly one environment.
- **FR-002**: Project, environment and app names MUST be lowercase DNS labels without a double
  dash. App names MUST be unique within an environment; environment names within a project;
  project names within the installation. Projects also have a free-text display name.
- **FR-003**: A new project MUST start with one environment, `production`. The owner MUST be able
  to add and delete environments and projects and to change a project's display name; deleting
  MUST be refused while apps remain, naming them. Names themselves never change (they are part
  of each app's ID).
- **FR-004**: Each app MUST have one ID that is unique in the installation and stable for its
  life: its name when it is in the Default project's `production` environment, otherwise derived
  from project, environment and name by one documented rule that clients can compute. Every
  per-app resource that exists today (the orchestrator job, secrets, volumes, backups, routing,
  webhook token, history) MUST stay keyed by that ID, so environments never share any of them.
- **FR-005**: On upgrade, every existing app MUST be placed in the Default project's `production`
  environment with its ID unchanged, without any redeploy or restart, and with every console
  address, API path, webhook URL and token still working.
- **FR-006**: Promotion MUST deploy the target app with the image digests the source's last
  healthy deployment ran and the source's service shape (services, commands, arguments, ports,
  health checks, volume mounts), and MUST keep the target's env values, secrets, domains, memory,
  copies and backup settings. It MUST be refused when the source's last deployment isn't healthy,
  when the two apps are in different projects, when the target has a service the source lacks,
  when an added service needs a secret the target lacks, or when an image no longer exists.
- **FR-007**: Before a promotion is deployed, the owner MUST be able to see what it changes
  (images from and to, shape changes per service, services added), and the deployment record and
  its notification MUST name the source environment and deployment.
- **FR-008**: Copying an app into another environment of its project MUST create an undeployed
  app with the source's services, images, env values and backup settings, no domains and no
  secret values, and MUST list the secrets that still need values. It MUST be refused when the
  target already has an app of that name.
- **FR-009**: The console MUST show projects, their environments and apps on the home page; let
  the owner create apps in a chosen project and environment; show every app's project and
  environment; and from an app, show its counterparts in the project's other environments with
  Promote and Copy actions.
- **FR-010**: The API MUST offer everything the console does here (list, create and delete
  projects and environments, change a project's display name; list and create apps in an environment; find an app by
  project, environment and name; promote; copy), and every existing endpoint MUST keep its path
  and meaning, adding the app's project and environment to what it returns.
- **FR-011**: The needs-you list, notifications and activity MUST name the project and
  environment of each app they mention.

### Key Entities

- **Project**: a named group of apps, with a display name and an ordered list of environments.
  One project, Default, holds the apps from before this feature.
- **Environment**: a named stage of one project (production, staging, preview, ...), holding that
  project's apps for that stage.
- **App**: as today, plus the environment it belongs to and its name within it; its ID is the
  installation-wide key every per-app resource uses.
- **Promotion**: a deployment of a target app whose images and shape came from a source app's
  healthy deployment in another environment; recorded on the target's deployment.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Upgrading an installation with running apps causes zero restarts and zero new job
  versions, and every pre-upgrade webhook URL, console link and API path still works.
- **SC-002**: The owner can go from "a project with production only" to "the same app running in
  staging" in under 2 minutes in the console, without retyping its services.
- **SC-003**: A promotion runs, in production, exactly the image digests staging's healthy
  deployment ran, every time, even when tags have moved since.
- **SC-004**: No secret value of one environment is ever readable by another environment's apps.
- **SC-005**: Every promotion that would remove a service or leave a needed secret empty is
  refused before anything changes.

## Assumptions

- The organization is the installation; teams, roles and per-project permissions are Phase 4.
- Defaults chosen without asking, per the maintainer's "go autonomous": promotion is per app;
  services the source added are added to the target; services only the target has block the
  promotion; memory, copies and backups stay per environment; copies of an app are created
  undeployed.
- All environments run on the same node(s) as today; nothing about placement changes.
- Environments are not isolated from each other on the network beyond what apps already are;
  service names reach only within an app, as today.

## Out of Scope

- Moving or renaming an app between environments or projects (its ID, and so its job, volumes
  and backups, would change).
- Promoting a whole environment at once; automatic promotion on a schedule or after tests.
- Restoring a backup from one environment into another (restores stay within their app).
- Per-project or per-environment notification channels (channels stay installation-wide; messages
  name the project and environment).
- Preview environments per pull request (needs 020, build from source).
