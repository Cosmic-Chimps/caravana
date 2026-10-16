# Feature Specification: Private registries

**Feature Branch**: `017-private-registries`

**Created**: 2026-10-16

**Status**: Draft

**Input**: "Private registry credentials (docs/plan.md backlog, Phase 2.5). Caravana resolves digests
and Nomad pulls images anonymously, so only public images on Docker Hub and GHCR deploy. Most real
apps, Bella Baxter (Phase 2.5's gate) and anything published by `PublishToCaravana` need private
images, and some live on other registries (GitLab, a self-hosted one). Add credentials per registry,
write-only like secrets, kept out of jobs, pages, logs and API answers; the digest lookup and the
image pull use them; the console and the API manage them. Any registry that speaks the Docker
Registry HTTP API v2 can be used, with or without credentials. Out of scope: per-app or per-team
credentials, registries that need a cloud provider's token exchange (Amazon ECR's IAM tokens),
pulling on more than one node (Phase 3)."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Deploy a private image (Priority: P1)

The owner publishes Umbral as a private package on GHCR. In Settings › Registries they add
`ghcr.io` with their GitHub user name and a token that can read packages. They deploy
`ghcr.io/acme/umbral:latest`: Caravana pins it to its digest and the node pulls it, both with
the stored credentials. The token is never shown again. Webhook redeploys and "Redeploy this
version" work the same way.

**Why this priority**: today a private image can't be deployed at all, which blocks most real apps
and Phase 2.5's gate (Bella Baxter through `PublishToCaravana`).

**Independent Test**: push a private image to a registry. Deploying it without credentials fails,
saying the image is missing or private and that credentials can be added in Settings. Add
credentials, deploy again: the deployment is healthy and the image is pinned by digest. Read the
registries back: the user name shows, the secret doesn't.

**Acceptance Scenarios**:

1. **Given** credentials for `ghcr.io`, **When** the owner deploys a private image from it,
   **Then** the deployment pins the image by digest and becomes healthy.
2. **Given** no credentials for a registry, **When** a private image from it is deployed, **Then**
   the deploy fails naming the image and saying it is missing or private, with a pointer to adding
   credentials for that registry in Settings.
3. **Given** credentials that the registry rejects, **When** an image from it is deployed, **Then**
   the deploy fails saying the registry refused the stored credentials, naming the registry and the
   image; nothing is registered with Nomad.
4. **Given** credentials for a registry, **When** a public image from that registry is deployed,
   **Then** it deploys as before.
5. **Given** a private app, **When** its webhook is called or a past version is redeployed, **Then**
   the digest check and the pull use the stored credentials as a manual deploy does.
6. **Given** a Compose file whose services use private images, **When** it is deployed, **Then** each
   image uses its registry's credentials.

---

### User Story 2 - Manage registry credentials (Priority: P1)

In Settings the owner sees the registries Caravana has credentials for: the registry host, the user
name, when it was added or changed, and whether the last check worked. They can add one, check it
("Test" signs in to the registry without deploying anything), replace its secret, change its user
name, and remove it. The secret is entered in a password field and never comes back.

**Why this priority**: credentials that can't be checked, rotated or removed turn into deploy
failures nobody can explain.

**Independent Test**: add credentials for a registry, test them (works), replace the secret with a
wrong one, test (fails, saying the registry refused them), fix it, remove them. The API lists the
same without ever answering the secret.

**Acceptance Scenarios**:

1. **Given** the Settings page, **When** the owner adds a registry with a user name and a secret,
   **Then** it is listed with the host and user name, and the secret is not shown anywhere.
2. **Given** stored credentials, **When** the owner tests them, **Then** the page says whether the
   registry accepted them, and the list shows the time and outcome of that check.
3. **Given** stored credentials, **When** the owner saves them again with the secret left empty,
   **Then** the user name changes and the secret is kept.
4. **Given** stored credentials used by running apps, **When** the owner removes them, **Then** the
   running apps keep running, and the next deploy of a private image from that registry fails as in
   US1-2.
5. **Given** an API client with a token, **When** it lists, adds, tests, updates or removes
   credentials, **Then** it can do everything the console can, and no answer contains a secret.

---

### User Story 3 - Images from other registries (Priority: P2)

The owner's company hosts images on GitLab (`registry.gitlab.com`) and on a self-hosted registry
(`registry.example.com:5000`). Images from any registry that speaks the Docker Registry API deploy
like Docker Hub and GHCR ones do: public ones without credentials, private ones with them.

**Why this priority**: private images often live outside Docker Hub and GHCR; refusing those hosts
makes credentials useless for them. It ranks below US1 because GHCR covers the first apps.

**Independent Test**: deploy a public image from a third registry (e.g. `quay.io`): it is pinned and
runs. Deploy a private image from a self-hosted registry with credentials: it is pinned and runs.

**Acceptance Scenarios**:

1. **Given** a public image on any registry that speaks the Docker Registry API, **When** it is
   deployed, **Then** it is pinned by digest and runs, without credentials.
2. **Given** a registry on a host with a port (`registry.example.com:5000`), **When** an image from
   it is deployed, **Then** the host and port are kept in the pinned image.
3. **Given** a registry only reachable over plain HTTP, **When** an image from it is deployed,
   **Then** the deploy fails saying Caravana only uses HTTPS registries (HTTP is out of scope).
4. **Given** an image reference whose host isn't a registry (it answers but not with the registry
   API), **When** it is deployed, **Then** the deploy fails naming the image and saying the host
   didn't answer as a container registry.

### Edge Cases

- **Docker Hub credentials**: also lift Docker Hub's anonymous rate limit for every image from Docker
  Hub; the list says so.
- **A secret is replaced while a deploy runs**: that deploy uses the credentials it read when it
  started; the next one uses the new secret.
- **The node pulls a copy hours after the deploy** (a restart or a reschedule): it uses the
  credentials stored at that time, so a removed or rotated-away secret makes that pull fail; the
  copy shows the pull error and the app is on the needs-you list.
- **Backup and restore jobs** (features 010 and 016) use only Caravana's own pinned images and the
  service's image for Postgres dumps; a private Postgres image is pulled with the same credentials.
- **The same host twice**: one set of credentials per registry host; adding a host again replaces it.
- **A user name or secret with unusual characters** (quotes, `$`, newlines in a JSON key file):
  stored and used as given, never interpreted.
- **Export** (feature 011): an exported job never contains credentials; its header says which
  registries need credentials on the node.
- **Uninstalling Caravana**: running apps keep running; a later pull of a private image by Nomad
  alone may fail, and the uninstall notes say so.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The owner MUST be able to add, list, update and remove credentials per registry host:
  the host, a user name and a secret (password or token).
- **FR-002**: The secret MUST be write-only: it MUST NOT appear in any page, API answer, log line,
  Nomad job, exported jobspec or deployment record. Saving without a secret keeps the stored one.
- **FR-003**: The owner MUST be able to test stored credentials against their registry without
  deploying, and see the time and outcome of the last test.
- **FR-004**: Resolving an image's digest MUST use the credentials stored for its registry when there
  are some, and anonymous access otherwise.
- **FR-005**: The node MUST pull images from a registry with stored credentials using them, for every
  deploy, webhook redeploy, version redeploy, restart and reschedule.
- **FR-006**: A deploy MUST fail before anything is registered, naming the image and its registry,
  when the registry refuses the stored credentials, or when an image is missing or private and no
  credentials are stored (with a pointer to adding them).
- **FR-007**: Images from any registry speaking the Docker Registry HTTP API v2 over HTTPS MUST be
  accepted, including hosts with a port.
- **FR-008**: Removing or changing credentials MUST NOT restart, stop or change any running app.
- **FR-009**: The API MUST offer everything the console does with credentials, with the secret
  write-only, and every new endpoint and field in `api/openapi.yaml` in the same change.
- **FR-010**: Credentials MUST be kept where Caravana keeps its own secrets (constitution VII), and
  a cold start of the host MUST NOT wait on a registry.
- **FR-011**: The gate MUST have a step that deploys a private image with credentials and shows the
  same deploy failing without them.

### Key Entities

- **Registry credentials**: host (e.g. `ghcr.io`, `registry.example.com:5000`), user name, secret
  (write-only), created and updated times, last test time and outcome (with the registry's message
  when it failed). One per host, per installation.
- **Image reference** (existing): gains any registry host, not only Docker Hub and GHCR.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A private image on GHCR deploys from the console within 2 minutes of the owner
  pasting a token, without touching the server.
- **SC-002**: No secret appears in any page, API answer, log, job, export or deployment record,
  checked with a sentinel secret on every path that handles credentials.
- **SC-003**: A deploy that fails for credentials says which registry and what to do, in 100 % of
  the failure cases listed in FR-006.
- **SC-004**: Rotating a registry's secret takes one form save and affects no running app.
- **SC-005**: Umbral (or Bella Baxter) as a private GHCR package deploys and redeploys by webhook on
  the maintainer's host.

## Assumptions

- One set of credentials per registry host for the whole installation; per-app and per-team
  credentials are later (teams are Phase 4).
- Registries that need a cloud provider's token exchange (ECR's IAM tokens that expire every 12 h)
  are out of scope; Google Artifact Registry's JSON key as a password and GitLab deploy tokens work
  as plain user name and secret.
- HTTPS only; plain-HTTP registries are out of scope.
- One node: credentials reach the node Caravana runs on. Multi-node distribution is Phase 3.
- The secret is a password or a token of up to a few kilobytes (a JSON key file fits).
