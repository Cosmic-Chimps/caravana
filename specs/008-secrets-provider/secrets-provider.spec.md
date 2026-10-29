# Feature Specification: Secrets

**Feature Branch**: `008-secrets`

**Created**: 2026-10-07

**Status**: Implemented, v0.1.0

**Input**: User description: "008 Secrets: a `secrets.Provider` interface in `internal/secrets`, a
Nomad Variables implementation (secrets at `nomad/jobs/<app-id>`, rendered as env by a template in
the task), and declarative replace of the app's secrets on every deploy. Secret values are
write-only through the API and UI: reads return names only, and values are never logged. If the
store is unreachable, running apps keep their last env and only new deploys fail with a clear
error. Caravana's scoped Nomad policy gains the rules it needs. Console and API both go through
`internal/service`. The Bella Baxter provider is not in scope (feature 014): only keep the
interface shaped for it. No Vault, no new runtime dependency."

## Context

Today an app's services can only carry plain env, which is stored in Caravana's database and put
in the Nomad job as is. Anyone who can read the job, the stored app or an API response sees the
values. That's fine for `LOG_LEVEL`, not for `DATABASE_URL` or an API key.

Feature 008 adds **secrets**: named values a service receives as environment variables, which
Caravana writes once and never shows again. The values live in the cluster's own secret store
(Nomad Variables), not in Caravana's database and not in the job. Feature 014 will add Bella
Baxter as a second store, so the store sits behind one small seam; 008 builds that seam with only
the built-in store behind it.

Who uses this:

- **API clients** (the .NET Aspire integration, CI): they send the full definition on every
  deploy, secrets included, and expect it to be the whole truth: what they leave out is gone.
- **The admin in the console**: they add a secret, rotate one value, or remove one, without
  retyping the others.

Words used here:

- **Secret**: a name and a value one service receives as an environment variable.
- **Secret set**: all the secrets of one service. A deploy replaces the set as a whole.
- **Secret store**: where values live. In 008 it is always Nomad Variables.
- **Keep**: a deploy that names a secret without a new value, asking for the value it has now.

## Clarifications

### Session 2026-10-07

Answered with the recommended option (unattended run, the maintainer's standing preference).

- Q: How does one secret change without resending the others? → A: An explicit keep per name in
  the deploy body. The console's set and remove actions use the same deploy, so there is no
  second way to change an app (not a separate secrets endpoint, not "always send every value").
- Q: Does the New app form take secrets? → A: No. Secrets are added on Configure once the app
  exists; the API can send them with the first deploy.
- Q: Does the definition carry a per-secret source (ready for Bella Baxter) now? → A: No. A
  secret is a name and a value or keep; feature 014 adds whatever it needs.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Deploy an app with secrets through the API (Priority: P1)

A client deploys an app whose `web` service has two secrets, `DATABASE_URL` and `API_KEY`. The
new copies of `web` start with both as environment variables, exactly as sent. Reading the app
back through the API or the console shows the two names and never a value. A later deploy that
sends only `DATABASE_URL` removes `API_KEY`: the copies of that deploy no longer have it.

**Why this priority**: it is the feature. Without it no real app (a database password, a payment
key) can run on Caravana safely, and the compose and Aspire work after it needs it.

**Independent Test**: deploy an app with a secret through the API, check that the running
container sees the value, check that no API response, log line, stored record or Nomad job holds
the value, then deploy again without it and check it is gone.

**Acceptance Scenarios**:

1. **Given** no app `blog`, **When** a client deploys `blog` with service `web` carrying secret
   `DATABASE_URL`, **Then** the deploy succeeds and the copies of `web` see `DATABASE_URL` with
   the exact value sent.
2. **Given** that deploy, **When** a client reads the app or its deployments, **Then** the
   response lists `DATABASE_URL` under `web` and contains no value.
3. **Given** `web` has secrets `DATABASE_URL` and `API_KEY`, **When** a client deploys with only
   `DATABASE_URL`, **Then** `API_KEY` is removed from the store and the new copies don't have it.
4. **Given** `web` has a secret, **When** a client deploys with a new value for it, **Then** the
   new copies see the new value and the old copies keep the old one until the rollout replaces
   them.
5. **Given** a deploy where `web` has both a plain env var and a secret named `PORT`, **When** it
   is sent, **Then** it fails with an error naming the secret's field, and nothing is saved.
6. **Given** two services `web` and `worker` with a secret of the same name and different values,
   **When** the app deploys, **Then** each service sees only its own value.

---

### User Story 2 - Redeploys keep the secrets (Priority: P1)

A redeploy (deploy with no body, the console's Deploy button, or the webhook) deploys the stored
definition, and the stored definition only knows secret names. So a redeploy keeps every secret
value exactly as it is. A webhook that pushes a new image never touches secrets.

**Why this priority**: webhook redeploys are how most apps ship. If a redeploy lost or blanked a
secret, every push would break the app.

**Independent Test**: deploy an app with a secret, trigger the webhook with a new image, and check
the new copies still see the value.

**Acceptance Scenarios**:

1. **Given** `blog` deployed with a secret, **When** the webhook redeploys it, **Then** the new
   copies see the same secret value.
2. **Given** `blog` deployed with a secret, **When** a client deploys with an empty body, **Then**
   the new copies see the same secret value.
3. **Given** the stored definition names a secret that is no longer in the store (removed outside
   Caravana), **When** the app is redeployed, **Then** the deploy fails with an error naming the
   missing secret and the running copies keep running.

---

### User Story 3 - Manage secrets from the console (Priority: P2)

On an app's Configure tab the admin sees, for each service, the names of its secrets. They can
add a secret or give an existing one a new value, and they can remove one. Each change goes out as
a deploy: the other secrets keep their values, and the deploy shows up in Deployments like any
other. The value field is never filled in, and after saving the value is never shown again.

**Why this priority**: rotating a leaked key from a phone is a real need, but the API already
covers it, so the console comes second.

**Independent Test**: from the console, add a secret to a running app, watch the deploy finish,
check the copies see it; then remove it and check it is gone. The API can do the same through a
deploy that names the other secrets with keep.

**Acceptance Scenarios**:

1. **Given** `blog` with secrets `DATABASE_URL` and `API_KEY` on `web`, **When** the admin opens
   Configure, **Then** both names are listed under `web` and no value appears anywhere on the
   page or in its source.
2. **Given** that page, **When** the admin sets `API_KEY` to a new value and saves, **Then** a
   deploy starts, `DATABASE_URL` keeps its value, and the new copies see the new `API_KEY`.
3. **Given** that page, **When** the admin removes `API_KEY` and confirms, **Then** a deploy
   starts and the new copies no longer have `API_KEY`.
4. **Given** a client deploy that names `DATABASE_URL` with keep and `API_KEY` with a new value,
   **When** it is sent, **Then** the result is the same as scenario 2.
5. **Given** a deploy that names a secret with keep that has no stored value, **When** it is sent,
   **Then** it fails with an error naming that secret's field.

---

### User Story 4 - A failed deploy doesn't strand bad secrets (Priority: P2)

A deploy that changed secrets can fail: the new value is wrong and the copies never get healthy,
or the store can't be reached at all. Running copies keep the env they started with throughout.
If the store can't be reached, the deploy fails right away with a clear error and nothing
changes. If the new copies fail and the deploy is reverted, the store goes back to the secrets the
running version uses, so a later restart of a running copy doesn't pick up the bad value.

**Why this priority**: it is what makes changing a secret safe. It matters less often than the
stories above, but when it does, it prevents an outage that would surface hours later.

**Independent Test**: deploy with a secret that makes the health check fail, wait for the revert,
and check the store holds the previous value again; separately, make the store unreachable and
check a deploy fails with the store error while the running copies keep serving.

**Acceptance Scenarios**:

1. **Given** `blog` running with `API_KEY=a`, **When** a deploy sets `API_KEY=b` and its copies
   fail their health check, **Then** the deploy ends failed or reverted, the old copies keep
   serving, and the store holds `API_KEY=a` again.
2. **Given** the same failing deploy, **When** Caravana restarts before the deploy finishes,
   **Then** the store still ends up holding `API_KEY=a` once the deploy is settled.
3. **Given** the secret store is unreachable, **When** a client deploys, **Then** the deploy fails
   with an error that says the secret store is unreachable, distinct from other deploy errors, and
   the running copies keep serving with their env.
4. **Given** the secret store is unreachable, **When** a running copy is restarted by Nomad,
   **Then** it keeps the env it had (Caravana doesn't need to be involved).
5. **Given** a running app, **When** its secrets change in the store without a deploy, **Then**
   no running copy restarts or changes its env.

---

### User Story 5 - Deleting an app removes its secrets (Priority: P3)

When the admin deletes an app, its secrets are deleted from the store too. Creating a new app with
the same name later starts with no secrets.

**Why this priority**: leaving values behind is a leak, but it only matters at delete time.

**Independent Test**: delete an app with secrets, check the store has nothing left for it, then
create an app with the same name and check it has no secrets.

**Acceptance Scenarios**:

1. **Given** `blog` with secrets, **When** it is deleted, **Then** the store holds no secret for
   `blog`.
2. **Given** that, **When** a new `blog` is created without secrets, **Then** its copies see none
   of the old ones.

### Edge Cases

- **An empty value**: allowed, the variable is set and empty. A value with a NUL byte is refused.
- **Values with newlines, quotes, `=`, `$` or `{{`**: delivered byte for byte, never interpreted.
- **A secret name outside letters, digits and `_`, or starting with a digit**: refused with an
  error naming the field. This is stricter than plain env, because the store renders secrets
  through an env file whose keys allow only these.
- **A value that isn't valid UTF-8 text**: refused, because it couldn't be delivered byte for byte.
- **The same name twice in one service's secrets**, or in its secrets and its env: refused.
- **A set larger than the store accepts**: refused before anything is written, with an error
  naming the service's secrets field.
- **Two deploys of one app at once**: the newer one wins (as today), and a superseded deploy never
  writes its secrets.
- **A deploy that fails before Nomad accepts the job** (bad image, registry down): no secret is
  written, or what was written is put back.
- **A service removed from the app**: its secrets are removed from the store.
- **Secrets on an app that has none yet**: the first deploy with secrets works without any setup.
- **A value in an error message**: never. Errors name the field and the secret's name only.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: A deploy MUST accept, per service, a list of secrets, each a name with either a
  value or keep.
- **FR-002**: Each deploy that carries a definition MUST replace each service's secret set as a
  whole: a name it doesn't send is removed, a name with a value gets that value, and a name with
  keep keeps its current value.
- **FR-003**: A redeploy without a body, from the console's Deploy action or from the webhook,
  MUST keep every service's secrets as they are.
- **FR-004**: A service's copies MUST receive each of its secrets as an environment variable with
  the exact bytes sent, and MUST NOT receive another service's secrets.
- **FR-005**: Secret values MUST NOT appear in any API response, console page, server-sent event,
  log line, error message, deployment record, Caravana's database or the Nomad job.
- **FR-006**: Reading an app MUST return each service's secret names.
- **FR-007**: A secret name MUST be letters, digits and `_`, not starting with a digit, and MUST
  be unique across the service's secrets and env; a value MUST be UTF-8 text without a NUL byte;
  a violation fails the deploy with an error naming the field, and nothing is saved or written.
- **FR-008**: A keep for a name with no stored value, and a stored name whose value is missing
  from the store at deploy time, MUST fail the deploy with an error naming the secret.
- **FR-009**: Values MUST live in Nomad Variables under `nomad/jobs/<app-id>`, and only there.
- **FR-010**: Changing a value in the store MUST NOT restart or change running copies; new values
  reach copies only through a deploy's rollout.
- **FR-011**: When the secret store is unreachable, a deploy MUST fail before Nomad is asked to
  change the job, with an error code of its own that says so, and running copies MUST keep running
  with their env.
- **FR-012**: A superseded deploy MUST NOT write its secrets, and a deploy that fails before the
  job is accepted MUST leave the store as it was.
- **FR-013**: When a deploy that changed secrets ends failed or reverted, the store MUST hold the
  secrets of the version still running, also when Caravana restarted during the deploy.
- **FR-014**: Deleting an app MUST delete all its secrets from the store; removing a service MUST
  delete that service's secrets.
- **FR-015**: The console's Configure tab MUST list each service's secret names and let the admin
  set one secret's value or remove one secret; each change MUST start a deploy that keeps the
  other secrets.
- **FR-016**: Secret value fields in the console MUST start empty, MUST NOT be echoed back after a
  failed save, and MUST NOT be offered for autofill.
- **FR-017**: The store MUST sit behind one seam in `internal/secrets` (`secrets.Provider`) that
  takes and returns whole secret sets per app and service, reports unreachable distinctly, and has
  nothing specific to Nomad in it; the service layer MUST only use that seam.
- **FR-018**: A test store MUST exist alongside the Nomad one, and the service tests MUST run
  against it.
- **FR-019**: Caravana's scoped Nomad policy MUST allow exactly what the Nomad Variables store
  needs for `nomad/jobs/*`, and the gate MUST have a step that fails when it doesn't.
- **FR-020**: Every new or changed endpoint and field MUST be in `api/openapi.yaml` in the same
  change.

### Key Entities

- **Secret**: a name and a value that one service of one app receives as an environment variable.
  The name is visible and part of the app's definition; the value is write-only and lives only in
  the store.
- **Secret set**: all the secrets of one service. It is what a deploy replaces and what the store
  holds as one unit.
- **Secret store**: where values live (Nomad Variables in 008, Bella Baxter later). It can be
  unreachable, which is a failure of its own.
- **App definition**: gains, per service, the list of its secret names. It never holds values.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: An app deployed with secrets on two services sees 100% of its own values, byte for
  byte (including a value with a newline and quotes), and none of the other service's.
- **SC-002**: A unique marker value deployed as a secret is found in zero API responses, console
  pages, log lines, database rows and Nomad job definitions across a full deploy, redeploy,
  rotate, failed deploy and delete cycle.
- **SC-003**: 10 webhook redeploys in a row keep every secret value unchanged.
- **SC-004**: After a deploy with a bad secret value is reverted, the store holds the previous
  value within the time the deploy takes to settle, in 100% of runs, with or without a Caravana
  restart in the middle.
- **SC-005**: With the store unreachable, a deploy fails in under 30 seconds with the store's own
  error code, and the running app answers 100% of requests meanwhile.
- **SC-006**: The admin rotates one secret from the console in under one minute from opening the
  app, without retyping any other secret.
- **SC-007**: The gate passes on a host whose Caravana uses the scoped policy, including a deploy
  with a secret.

## Assumptions

- **Secrets are per service**, like plain env. Two services can each have a `DATABASE_URL` with
  different values (compose and Aspire both work this way).
- **Keep is not a merge.** The constitution says each deploy fully replaces env and secrets. A
  deploy still declares the whole set; keep only says "this name, with the value it has now",
  explicitly, per name. Nothing is ever kept by omission. This is what lets the console change
  one secret without asking for the others.
- **Changing a secret is a deploy.** There is no separate "save secrets" that takes effect later:
  values reach copies only through a health-checked rollout, which can be reverted.
- **Rolling back to an old version (feature 011) uses the current secrets**, not the secrets that
  version had. Only a failed or reverted deploy restores the previous set (FR-013).
- **Names are case-sensitive**, as env var names are.
- **The console shows names only**, not when each value was last set.
- **New app stays as it is**: secrets are added on Configure after the first deploy.
- **Plain env stays as it is**: stored and shown in full. Moving a value from env to secrets is
  the user's choice.
- **Bella Baxter (014)** will read values itself through workload identity, so the seam must not
  assume Caravana always holds values; 008 doesn't build any of that.
- **No new runtime dependency**: Nomad Variables is part of Nomad. Vault isn't used.

## Verbatim Constraints

- Package and interface: `internal/secrets`, `secrets.Provider`
- Store path: `nomad/jobs/<app-id>`
- Contract file: `api/openapi.yaml`
