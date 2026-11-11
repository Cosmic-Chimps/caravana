# Feature Specification: Registry credentials per path

**Feature Branch**: `alta/arctic-palm-fhmv` (worktree; no feature branch of its own)
**Created**: 2026-11-11
**Status**: Implemented
**Input**: CodeAlta task `2026-11-10-registry-credentials-per-path-two-accounts-on-one-registry-h-7239`: two accounts on one registry host. An entry in Settings › Registries may name a host and a path prefix (`ghcr.io/cosmic-chimps`). The most specific entry matching whole path segments wins, a host-only entry is the fallback, and an image matching no entry is pulled anonymously.

Today Caravana keeps one login per registry host (feature 017). Every private image on `ghcr.io` is pulled with the same user and token. That works while one person can read every private image, and fails as soon as images belong to accounts no single login can read, such as a client's organization next to Cosmic-Chimps.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Two accounts on one registry (Priority: P1)

The owner runs apps whose images live under two owners on the same registry: `ghcr.io/cosmic-chimps/...` and `ghcr.io/aerosens/...`. No single token reads both. In Settings › Registries they add `ghcr.io/cosmic-chimps` with one token and `ghcr.io/aerosens` with another. Both apps deploy, and every way of deploying (the console, Compose, CI, the MCP server, the Aspire publisher) picks the right login from the image name alone.

**Why this priority**: it is the whole reason for the feature; without it the second account's apps can't run at all.

**Independent Test**: on a registry that wants a login, put an image under each of two prefixes, add one entry per prefix with its own login, deploy both, and see both healthy; then break one entry's password and see only that prefix's deploy fail.

**Acceptance Scenarios**:

1. **Given** entries `ghcr.io/cosmic-chimps` (login A) and `ghcr.io/aerosens` (login B), **When** the owner deploys `ghcr.io/cosmic-chimps/shop:v1`, **Then** Caravana reads its digest with login A and the copy pulls with login A.
2. **Given** the same entries, **When** the owner deploys `ghcr.io/aerosens/patch:v2`, **Then** login B is used for both the digest and the pull.
3. **Given** an app with one service under each prefix, **When** it deploys, **Then** each service pulls with its own prefix's login.
4. **Given** login B is wrong, **When** an image under `ghcr.io/aerosens` deploys, **Then** the deploy fails saying the registry refused the stored credentials, and images under `ghcr.io/cosmic-chimps` still deploy.

---

### User Story 2 - The host-only entry stays the fallback (Priority: P1)

An owner who set up `ghcr.io` before this feature changes nothing. Their entry keeps working for every image on that host. When they add a prefix entry later, only the images under that prefix move to it.

**Why this priority**: existing installs must not break or move on upgrade.

**Independent Test**: upgrade an install with a `ghcr.io` entry and a running private app; it keeps deploying. Add `ghcr.io/acme`; an image under `acme/` uses the new entry, any other `ghcr.io` image still uses `ghcr.io`.

**Acceptance Scenarios**:

1. **Given** only a `ghcr.io` entry, **When** the owner upgrades Caravana, **Then** the entry is listed unchanged and every private `ghcr.io` app deploys as before.
2. **Given** entries `ghcr.io` and `ghcr.io/acme`, **When** `ghcr.io/other/app` deploys, **Then** the `ghcr.io` login is used.
3. **Given** entries `ghcr.io/acme` and `ghcr.io`, **When** `ghcr.io/acme-old/app` deploys, **Then** the `ghcr.io` login is used: a prefix matches whole path parts only.
4. **Given** only the entry `ghcr.io/acme`, **When** `ghcr.io/other/app` deploys, **Then** no login is sent, as for any public image.

---

### User Story 3 - Changing an entry reaches only the apps that use it (Priority: P2)

The owner rotates the token of `ghcr.io/aerosens`, or removes that entry. Apps already running pick up the change the next time their copies start, as they do today. Apps under other entries are not touched.

**Why this priority**: rotating one client's token must not disturb the other client's apps.

**Independent Test**: with apps under two prefixes running, save one prefix with a new password and check that only that prefix's apps hold the new login; remove a prefix and check its apps fall back to the host-only entry, or to no login when there is none.

**Acceptance Scenarios**:

1. **Given** apps under `ghcr.io/cosmic-chimps` and `ghcr.io/aerosens`, **When** the owner saves a new token for `ghcr.io/aerosens`, **Then** only the aerosens apps' stored pull logins change.
2. **Given** an app under `ghcr.io/acme` deployed while only `ghcr.io` existed, **When** the owner adds `ghcr.io/acme`, **Then** that app's stored pull login becomes the new entry's.
3. **Given** entries `ghcr.io` and `ghcr.io/acme` and an app under `acme/`, **When** the owner removes `ghcr.io/acme`, **Then** the app's stored pull login becomes the `ghcr.io` one; with no `ghcr.io` entry it becomes empty, as a removal does today.
4. **Given** any of these changes, **When** it is saved, **Then** no app is deployed or restarted by it.

---

### User Story 4 - Testing an entry with a path says what it checked (Priority: P3)

Test on a prefix entry asks the registry to accept the login. A registry's login check doesn't say which images the login can read, so the result says that the first deploy of an image under the prefix shows it.

**Why this priority**: an honest Test avoids false confidence, but it doesn't block deploys.

**Independent Test**: Test a prefix entry with a right and a wrong password and read both results.

**Acceptance Scenarios**:

1. **Given** a prefix entry with a valid login, **When** the owner tests it, **Then** it reads as working, with a note that the first deploy under the prefix shows whether the login can read those images.
2. **Given** a prefix entry with a wrong password, **When** the owner tests it, **Then** it reads as refused, as a host entry does today.

### Edge Cases

- An entry equal to a whole image name (`ghcr.io/acme/shop`) applies to that image only.
- `docker.io/library` covers Docker Hub's official images (`nginx` is `library/nginx`).
- A path written with a leading or trailing `/` is read without it. A path with capitals, spaces or empty parts is refused, naming the path.
- Two entries that differ only by path are two entries. Saving one never changes the other.
- An app whose job Caravana can no longer read when an entry changes keeps today's behavior: its stored login is rewritten only when it named the changed entry.
- The webhook-only listener, deploy tokens and every read that lists registries never show a password, as today.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: An entry MUST name a registry host and MAY name a path under it (`ghcr.io/cosmic-chimps`).
- **FR-002**: For an image, Caravana MUST use the entry whose path is the longest whole-part prefix of the image's path on that host, else the host-only entry, else no login.
- **FR-003**: The digest lookup and the copy's pull MUST use the same entry for an image.
- **FR-004**: Entries saved before this feature MUST keep working as host-only entries with no action from the owner.
- **FR-005**: Saving or removing an entry MUST rewrite the stored pull logins of exactly the apps whose entry changes or is the saved one, and of no other app.
- **FR-006**: Saving or removing an entry MUST NOT deploy, restart or stop any app.
- **FR-007**: A path that isn't a valid image path MUST be refused with an error naming the path.
- **FR-008**: Test on an entry with a path MUST say that it checked the login only.
- **FR-009**: The public API MUST let a client list, save, test and remove a path entry, and calls written before this feature MUST keep their meaning.
- **FR-010**: Settings › Registries MUST accept `host/path` in the registry field and show each entry with its path.
- **FR-011**: The count of apps using an entry MUST count the apps that entry serves, not every app on its host.
- **FR-012**: No password or token MUST appear in any page, answer, error, log, job, export or record.

### Key Entities

- **Registry entry**: a host, an optional path under it, a user name, the time it was saved and the last Test's outcome. Its secret stays outside the database, as today. Its key is the host plus the path.
- **Stored pull login of a service**: the copy of one entry's login that a service's copies pull with, plus which entry it came from.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Two private images on one registry, under two paths with two different logins, both deploy healthy on the first try.
- **SC-002**: A third image on that registry uses the host-only entry, and an image that matches no entry is pulled with no login.
- **SC-003**: After saving one entry, the stored pull logins of apps under other entries are byte for byte unchanged.
- **SC-004**: An install upgraded with host-only entries deploys every existing private app with zero changes by the owner.
- **SC-005**: The search for test passwords across pages, answers, errors, logs, jobs, exports and records finds none.

## Assumptions

- Paths are compared exactly as the image names them; image paths are lowercase, so entries are too.
- One entry per (host, path). The same login may be saved under several entries.
- The console keeps a single registry field (`host` or `host/path`) rather than adding a second input.
- The API keeps its routes and adds an optional `path` parameter, so older clients are untouched.
- Test can't prove which images a login reads without naming one, so it states that limit.

## Verbatim Constraints

- Example entries: `ghcr.io/cosmic-chimps`, `ghcr.io/aerosens`, `ghcr.io`.
- Operations: `listRegistries`, `putRegistry`, `deleteRegistry`, `testRegistry`.
