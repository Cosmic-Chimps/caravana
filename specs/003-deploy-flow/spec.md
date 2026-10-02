# Feature Specification: Deploy Flow

**Feature Branch**: `003-deploy-flow`

**Created**: 2026-10-02

**Status**: Draft

**Input**: User description: "Feature 003: Deploy flow. A client sends an app definition, and
Caravana pins every image to a digest, translates the app into a Nomad job (feature 001),
registers it, follows the rollout, and records the outcome (feature 002's store). The webhook
(004) and the HTTPS + auto-revert proof on a real VM (005) both call this flow, so it has to be
correct and safe before either exists. It's the first feature that talks to the outside world
(a container registry and Nomad), so failures must be clear and must never leave a
half-deployed app behind."

## Context

Consumers of this feature:

- **API clients**: curl today, the generated .NET Aspire client later. They send an app and need
  a deployment ID back right away, then a way to follow that deployment to a final status.
- **Feature 004 (webhook)**: redeploys an app that's already stored, through the same service
  call the API uses.
- **Operators**: when a deploy fails, they need to know why: bad image, registry or Nomad down,
  health check failing, auto-reverted, or replaced by a newer deploy.

"Registry" means a container image registry (Docker Hub, GHCR). "Digest" is the content address
of an image (`repo@sha256:<64 hex>`). "Rollout" is Nomad's deployment of one job version: it
places the new allocations, waits for their health checks, and either succeeds, fails, or fails
and reverts to the last good version. "Deployment record" is Caravana's own record of one
deploy, defined in feature 002.

## Clarifications

### Session 2026-10-02

- Q: Endpoint shape for deploy? → A: `POST /v1/apps/{id}/deployments`. With the app as body it
  saves the app and deploys it; with no body it redeploys the stored app (404 if none). Reads
  are `GET /v1/apps/{id}`, `GET /v1/apps/{id}/deployments` and
  `GET /v1/apps/{id}/deployments/{deploymentId}`.
- Q: When is the app saved? → A: As soon as it passes validation, before the registry lookup.
  The stored app is always the last definition sent, even if its deploy then fails or reverts;
  a later deploy with no body redeploys that definition.
- Q: Status and code for an image that doesn't exist? → A: 422 with code `image_not_found` and
  `field` = `services[i].image`, the standard error body, and a `Location` header pointing at
  the `failed` deployment record.
- Q: A Nomad job with the app's ID exists and Caravana didn't create it? → A: Refuse with 409
  `job_conflict` naming the Nomad job, unless the job carries Caravana's meta. Nothing is
  registered and the record ends `failed`. A stopped or purged job of that ID isn't a conflict.
- Q: Shape of the deployment list? → A: An object `{"deployments": [...]}`, newest first, with
  an optional `?limit=` (default 20, max 100; out of range → 400 naming `limit`). No cursor
  yet; one can be added later without breaking clients.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Deploy an image and see it live (Priority: P1)

A client sends the whoami app with the image `traefik/whoami:latest`. Caravana saves the app,
resolves the tag to a digest, registers the job with Nomad and answers right away with the
deployment ID, the Nomad job version and any warnings. The client then polls the deployment
until it says `healthy`, and the app answers over HTTPS on its domain. Nomad's job holds the
digest, never the tag.

**Why this priority**: this is the product's core promise and the first half of the Phase 1
gate. Without it there's nothing for the webhook to trigger.

**Independent Test**: against the lab (`deploy/dev/lab.sh up`, `eval "$(deploy/dev/lab.sh
env)"`, with any hand-deployed `whoami` job from `lab.sh check` stopped), run Caravana, send the
whoami app, poll the deployment to `healthy`, call `https://whoami.localhost:8443`, and read the
job from Nomad to check its image.

**Acceptance Scenarios**:

1. **Given** a running lab and no stored app `whoami`, **When** a client deploys the whoami app
   with `traefik/whoami:latest`, **Then** the response comes back once Nomad has accepted the
   job, without waiting for the rollout, and holds a deployment ID, a Nomad job version and a
   (possibly empty) list of warnings.
2. **Given** that deploy, **When** the client reads the deployment until it's final, **Then** it
   moves `pending` → `running` → `healthy`, and `https://whoami.localhost:8443` answers.
3. **Given** that deploy, **When** the job is read from Nomad, **Then** its image is
   `traefik/whoami@sha256:<64 hex>`, and the deployment record holds the same digest for the
   service.
4. **Given** a multi-architecture image, **When** it's resolved, **Then** the pinned digest is the
   multi-architecture index's, so each node pulls the variant for its own architecture.
5. **Given** a service whose image already names a digest, **When** it's deployed, **Then** that
   digest is used as-is, with no registry lookup needed for it.

---

### User Story 2 - Redeploy safely and idempotently (Priority: P1)

The same payload sent again is a new deploy of the same app: the stored app doesn't change, a
new deployment record appears, and Nomad gets a new job version (with the tag resolved again,
so a moved `latest` is picked up). Two deploys of one app sent close together never interleave:
the newer one wins and the older one ends `superseded`. Deploys of different apps don't wait for
each other.

**Why this priority**: the webhook (004) is exactly "deploy again", and two quick pushes are a
real case. Without these rules the second deploy could undo the first, or the record could say
`failed` for a deploy nobody broke.

**Independent Test**: service-level tests with a fake registry and a fake Nomad: same payload
twice; two deploys of one app started at once; two deploys of different apps where one is held
up.

**Acceptance Scenarios**:

1. **Given** app `whoami` deployed and `healthy`, **When** the same payload is sent again,
   **Then** a second deployment record exists, Nomad has a job version one higher, and the
   stored app reads back identical to before, updated time included.
2. **Given** a deploy of `whoami` still rolling out, **When** a second deploy of `whoami` is
   sent, **Then** the second one ends as the live version and the first one ends `superseded`,
   not `failed`.
3. **Given** two deploys of `whoami` requested in quick succession, **When** both finish,
   **Then** the job Nomad runs matches the one requested last, never the earlier one.
4. **Given** a deploy of app `a` whose registry lookup is slow, **When** app `b` is deployed,
   **Then** `b`'s deploy completes without waiting for `a`'s.
5. **Given** a stored app, **When** a deploy is sent with no body (or feature 004 calls the
   redeploy service call), **Then** it deploys the stored app exactly as a full deploy would,
   creating a new deployment record with trigger `manual` (API) or `webhook` (004).
6. **Given** no stored app `ghost`, **When** a deploy with no body is sent for `ghost`, **Then**
   the client gets 404 and nothing is recorded.

---

### User Story 3 - A broken deploy reverts and says so (Priority: P2)

A deploy whose health check can never pass (for example, the wrong container port) doesn't take
the app down. Nomad gives up on the new version and rolls back to the last good one. The
deployment record ends `reverted`, with a message saying the health check failed and Nomad
rolled back, and the previous version keeps serving.

**Why this priority**: it's the second half of the Phase 1 gate and the "deploy without fear"
principle. P2 only because the auto-revert behavior itself is Nomad's (the translator already
asks for it); this story is about recording it truthfully.

**Independent Test**: unit tests where a fake Nomad replays scripted rollout states; in the lab,
deploy whoami healthy, then deploy it with a wrong container port and watch it revert.

**Acceptance Scenarios**:

1. **Given** whoami `healthy` at job version N, **When** a deploy with a health check that can't
   pass is sent, **Then** its record ends `reverted` with a message naming the cause, Nomad runs
   a job equal to version N again, and `https://whoami.localhost:8443` keeps answering
   throughout.
2. **Given** an app's very first deploy with a health check that can't pass, **When** the
   rollout fails, **Then** the record ends `failed` (there is no earlier version to revert to)
   with a message naming the cause.
3. **Given** a rollout Nomad marks as failed for another reason (e.g. allocations can't be
   placed before the deadline), **When** it ends, **Then** the record ends `failed` or
   `reverted` (as Nomad did) and its message carries Nomad's description.

---

### User Story 4 - Failures are loud, clear and leave nothing half-done (Priority: P2)

Every way a deploy can fail answers with the right status, the standard error body and, when
input is at fault, the field to fix. Nothing that fails before Nomad accepts the job registers
anything with Nomad.

**Why this priority**: operators and the .NET client depend on telling "your input is wrong"
from "something is down". A deploy that half-happened is the failure the constitution forbids
most.

**Independent Test**: API tests with a fake registry and fake Nomad for each failure: invalid
app, unknown tag, unknown repository, registry unauthorized, registry down, Nomad down, Nomad
rejecting the job.

**Acceptance Scenarios**:

1. **Given** an app definition that fails feature 001's validation, **When** it's deployed,
   **Then** the client gets 400 with code `invalid_field` and `field` set to the validation
   error's path (e.g. `services[0].health`), and nothing is saved or recorded.
2. **Given** a service whose image tag doesn't exist, **When** it's deployed, **Then** the client
   gets 422 with code `image_not_found`, `field` = `services[<i>].image`, a message naming the
   image, and a `Location` header for the deployment record; nothing is registered with Nomad,
   and that record ends `failed` with the same message.
3. **Given** a repository that doesn't exist or isn't public, **When** it's deployed, **Then**
   the result is the same as an unknown tag, with a message saying the image couldn't be found
   or needs credentials Caravana doesn't have yet.
4. **Given** the registry can't be reached or keeps failing, **When** a deploy is sent, **Then**
   the client gets a 502 saying which registry was unreachable, nothing is registered with
   Nomad, and the record ends `failed`.
5. **Given** Nomad can't be reached, **When** a deploy is sent, **Then** the client gets a 502
   saying Nomad is unreachable, and the record ends `failed`.
6. **Given** Nomad rejects the job, **When** a deploy is sent, **Then** the error's message
   carries what Nomad said, and the record ends `failed` with that message.
7. **Given** Nomad runs a job `whoami` that Caravana didn't create (e.g. from `lab.sh check`),
   **When** app `whoami` is deployed, **Then** the client gets 409 `job_conflict` naming the
   Nomad job, the existing job is untouched, and the record ends `failed`.

---

### User Story 5 - Read apps and their deployment history (Priority: P3)

A client reads an app back exactly as it was sent, lists its deployments newest first, and reads
one deployment by ID to follow it to a final status.

**Why this priority**: needed to follow a deploy (Story 1) and for operators' first look when
something fails, but it's thin: the store already supports all of it.

**Independent Test**: API tests against a store with seeded apps and deployments, with every
response validated against the contract.

**Acceptance Scenarios**:

1. **Given** a deployed app, **When** a client reads it, **Then** the body is the app as sent
   (same field names and values, durations in the same form), plus its created and updated
   times.
2. **Given** an app with 25 deployments, **When** a client lists them with no `limit`, **Then**
   it gets the newest 20; with `limit=5` it gets the newest 5; with `limit=0` or `limit=101` it
   gets 400 naming `limit`. Deployments come newest first, each with ID, trigger, status, Nomad
   job version (if known), digests, warnings, error message (if any) and times.
3. **Given** a deployment ID, **When** a client reads it, **Then** it gets that one deployment
   with the same fields.
4. **Given** an unknown app ID or deployment ID, or a deployment that belongs to another app,
   **When** a client reads it, **Then** it gets 404 with the standard error body.

---

### Edge Cases

- **Path ID and body ID**: the app ID comes from the path. A body that omits `id` takes the
  path's; a body whose `id` differs from the path is rejected with 400 naming `id`.
- **"No body" on deploy** means an empty body. `{}` is a body: an app with no services, so it
  fails validation with 400.
- **Durations**: health check `interval` and `timeout` are written as duration strings
  (`"10s"`, `"1m30s"`), in requests and in responses. A number, or a string that isn't a
  duration, is rejected with 400 naming the field (e.g. `services[0].health.interval`). The
  stored document keeps the format feature 002 already uses.
- **Image reference forms**: short Docker Hub names (`traefik/whoami`), library images
  (`nginx` → `docker.io/library/nginx`), explicit registries (`ghcr.io/owner/repo`), a missing
  tag (means `latest`), and a reference carrying both a tag and a digest (the digest pins the
  image and the tag has no effect; the deploy result carries a warning on `services[i].image`
  saying so). A reference that can't be parsed is rejected with 400 `invalid_field` naming the
  image field.
- **Registry not supported in Phase 1** (anything other than Docker Hub or GHCR): rejected with
  400 `invalid_field` naming the image field and saying the registry isn't supported yet.
- **What counts as validation**: anything decidable from the request alone (feature 001's rules,
  an image reference that can't be parsed, an unsupported registry) is validation: 400, nothing
  saved, no record. Anything that needs the registry or Nomad happens after the app is saved and
  the record exists.
- **Registry rate limit**: an answer of "too many requests" is reported as the registry
  refusing for now (a 502 naming the registry), never as "image not found".
- **Several services, one bad image**: no service is registered; the error names the first
  failing service's image field. Resolving one image never leaves the others half-applied.
- **Same payload twice**: a new deployment record and a new Nomad job version every time (each
  deployment ID goes into the job, per feature 001), even when every digest is unchanged.
- **Tag moved between deploys**: the second deploy pins the new digest; the stored app still
  holds the tag the user wrote.
- **Redeploy after a failed deploy**: the stored app is the failed definition (e.g. the bad
  tag), so a deploy with no body (or a webhook) fails the same way again, loudly, until a
  corrected app is sent. It never silently falls back to an older definition. After a revert,
  Nomad runs the previous version while the stored app holds the reverted one.
- **First deploy that never gets healthy**: `failed`, not `reverted`, because Nomad has no
  earlier stable version.
- **Newer deploy arrives before the older one reached Nomad**: the older one must not register
  after the newer one. It ends `superseded` without being registered.
- **Caravana restarts during a rollout**: on startup, every deployment still `running` is
  followed again from Nomad's current state until it ends, so no outcome is lost. A deployment
  still `pending` is matched to Nomad by its deployment ID (which the job carries): if Nomad has
  it, it continues as `running`; if not, it ends `failed` with a message saying Caravana stopped
  before registering it.
- **Nomad unreachable while a rollout is being followed**: Caravana keeps retrying and the
  record stays `running`; a lost connection is never recorded as a failed rollout.
- **Nomad token missing or not allowed**: deploys fail with a 502 saying Nomad refused the
  token, without the token's value. Startup doesn't require Nomad to be up (health and reads of
  stored data still work).
- **Job registered but the job version can't be read back, or the store write fails after
  registration**: the record is not left `pending`; following the rollout picks up the version,
  and the error is logged. The client gets an error only if Nomad didn't accept the job;
  otherwise it gets 202, possibly without `jobVersion`, which appears on the record once known.
- **Job with the app's ID that Caravana didn't create**: refused with 409 `job_conflict`
  naming the job, so Caravana never takes over a workload it doesn't own. Ownership is the
  Caravana meta every translated job carries (feature 001), and that meta must name a
  deployment this Caravana recorded for the app. A stopped or purged job of that ID
  isn't a conflict and is registered over. Taking over an existing job on purpose is left to a
  later feature (011, import/export).
- **Request canceled by the client after Nomad accepted the job**: the deploy continues and its
  outcome is still recorded.

## Requirements *(mandatory)*

### Functional Requirements

**Service layer**

- **FR-001**: The system MUST offer one service layer with: deploy an app (definition + trigger),
  redeploy a stored app (trigger), get an app, list an app's deployments, and get one
  deployment. The API MUST call only this layer; feature 004 and the later UI use the same
  calls.

**API**

- **FR-002**: The system MUST offer `POST /v1/apps/{id}/deployments`. With a body (the full app
  definition: feature 001's model and camelCase field names, app ID from the path) it saves the
  app and starts a deploy; saving is an upsert keyed by app ID (one stored app per ID, replaced
  whole). With no body it redeploys the stored app, and answers 404 if there is none.
- **FR-003**: The deploy response MUST return the deployment ID, the Nomad job version and the
  warnings, as soon as Nomad has accepted the job, without waiting for the rollout.
- **FR-004**: The system MUST offer `GET /v1/apps/{id}` (the app),
  `GET /v1/apps/{id}/deployments` (its deployments, newest first, as
  `{"deployments": [...]}` with an optional `limit`: default 20, max 100, out of range → 400
  naming `limit`) and
  `GET /v1/apps/{id}/deployments/{deploymentId}` (one deployment of that app).
- **FR-005**: Every endpoint, request body and response body MUST be described in the API
  contract (`api/openapi.yaml`) in the same change, and API tests MUST validate request and
  response bodies against it. New error codes MUST be listed in the contract.
- **FR-006**: Health check durations in request and response bodies MUST be duration strings;
  invalid ones fail with 400 naming the field.

**Digest resolution**

- **FR-007**: On every deploy, every service's image MUST be resolved to `repo@sha256:<64 hex>`
  before translation; Nomad never receives a tag.
- **FR-008**: Resolution MUST support Docker Hub (short names and library images) and GHCR,
  anonymously, including the registry's token handshake for anonymous pulls.
- **FR-009**: For a multi-architecture image, the pinned digest MUST be the index's, not a single
  architecture's.
- **FR-010**: An image that already carries a digest MUST be used as-is. If it also carries a
  tag, the tag is ignored and the deploy result MUST include a warning on `services[i].image`
  (constitution IV).
- **FR-011**: An image or tag that doesn't exist (or isn't public) MUST fail with 422
  `image_not_found` naming the service's image field; a registry that is unreachable, erroring
  or rate-limiting MUST fail with a 502 naming the registry. Neither case may register anything
  with Nomad.

**Nomad**

- **FR-012**: The translated job MUST be registered through the official Nomad client as JSON
  (constitution VI), and the Nomad job version MUST be recorded as soon as registration returns
  it.
- **FR-013**: After registering, the system MUST follow the job's Nomad rollout until it ends and
  record exactly one final status: `healthy` (succeeded), `reverted` (failed and Nomad rolled
  back to the previous version), `failed` (failed with no rollback), or `superseded` (a newer
  deploy of the same app replaced it). Failed and reverted records MUST carry a message saying
  why, including Nomad's description.
- **FR-013a**: Before registering, the system MUST check for a Nomad job with the app's ID that
  isn't stopped and isn't Caravana's, and refuse with 409 `job_conflict` naming it, registering
  nothing. A job is Caravana's when its `caravana.deployment` meta names a deployment this
  Caravana recorded for that app; meta alone isn't enough (the lab's hand-written
  `whoami.json` carries `caravana.*` meta too).
- **FR-014**: When Nomad rejects the job, the error and the record MUST carry what Nomad said.
- **FR-015**: Nomad unreachable at deploy time MUST fail with a 502 and a clear message; Nomad
  unreachable while following a rollout MUST NOT end the record (it stays `running` and
  following continues).
- **FR-016**: On startup, the system MUST resume following every deployment left `running`, and
  settle every deployment left `pending` as described under Edge Cases.

**Records**

- **FR-017**: A deploy that passes validation MUST create a deployment record, and a failure
  after that point (registry, Nomad) MUST end the record `failed` with the error message, so the
  history shows it. Input that fails validation MUST create no record and save nothing.
- **FR-017a**: A deploy with a body MUST save the app as soon as it passes validation, before
  any registry lookup. The stored app is the last definition sent, whatever its deploy's
  outcome; a deploy with no body redeploys exactly that definition.
- **FR-018**: The same payload deployed twice MUST leave the stored app unchanged (updated time
  included) and create a new deployment record and a new Nomad job version.

**Concurrency**

- **FR-019**: Deploys of the same app MUST be ordered: the most recently requested deploy is the
  one Nomad ends up running (or failing on), and every older one still in progress ends
  `superseded`. An older deploy MUST NOT register after a newer one.
- **FR-020**: Deploys of different apps MUST NOT wait on each other.

**Configuration**

- **FR-021**: The Nomad address MUST be a setting with an env var (`NOMAD_ADDR`) and a flag; the
  Nomad token MUST come from the env var `NOMAD_TOKEN` only (a flag would show it in the process
  list). These are the names `deploy/dev/lab.sh env` prints. The token MUST never appear in
  logs, errors or responses.

**Errors**

- **FR-022**: Every failure MUST answer with the standard error body (`code`, `message`,
  `field` when input is at fault) and the matching status: 400 for invalid input, 404 for
  unknown app or deployment, 422 `image_not_found` for an image the registry doesn't have, 409
  `job_conflict` for a Nomad job Caravana doesn't own, 502
  for an unreachable or refusing registry or Nomad, and for a job Nomad rejects.
- **FR-022a**: A deploy that fails after its deployment record exists MUST answer with the
  standard error body (unchanged shape) plus a `Location` header pointing at that record.

**Testing**

- **FR-023**: Unit tests MUST run without a network: registry resolution against a local test
  server (token handshake, index vs single manifest, 404, 401); rollout following against a fake
  Nomad that replays scripted states (successful, failed, failed with auto-revert, cancelled by a
  newer version); service tests for idempotency and the newer-wins rule.
- **FR-024**: Tests that need the lab MUST be opt-in, so CI passes without it.

### Key Entities

- **App** (feature 001, stored by feature 002): the definition the client sends, with image
  references as the user wrote them (tags allowed). The stored app never holds digests.
- **Deployment record** (feature 002): one deploy of an app: ID, app ID, trigger (`manual` or
  `webhook`), digests per service, status (`pending` → `running` → `healthy` | `failed` |
  `reverted` | `superseded`), Nomad job version, warnings, error message, created and updated
  times.
- **Image reference**: registry, repository, and a tag or a digest, parsed from a service's
  `image`.
- **Resolved image**: `repo@sha256:<64 hex>` for one service in one deployment.
- **Nomad settings**: address and token.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: In the lab, deploying whoami with `traefik/whoami:latest` through the API returns a
  deployment ID and job version in under 10 seconds, and the deployment reaches `healthy` with
  the app answering over HTTPS.
- **SC-002**: In 100% of deploys tested, the job Nomad holds contains digests only, never a tag.
- **SC-003**: The same payload sent twice produces 2 deployment records, 2 consecutive job
  versions and 0 changes to the stored app.
- **SC-004**: In the lab, a deploy with a health check that can't pass ends `reverted` and the
  previous version answers 100% of requests made during and after the rollout.
- **SC-005**: A deploy with a tag that doesn't exist fails with 422 naming the image field, and
  Nomad's job version count for that app is unchanged.
- **SC-006**: Of N deploys of one app started at once, exactly 1 ends as the live version and
  the other N−1 end `superseded`; 0 end `failed`.
- **SC-007**: Every scripted rollout state in the tests maps to the expected final status, 100%
  of the time.
- **SC-008**: 100% of request and response bodies in API tests validate against the contract,
  and the contract-sync test reports 0 differences.
- **SC-009**: The Nomad token appears 0 times in captured logs and responses across all tests.
- **SC-010**: Every CI check passes with no lab and no network.

## Assumptions

- **The four open questions from the description**: endpoint shape is settled in Clarifications
  (`POST /v1/apps/{id}/deployments`). The other three take the maintainer's stated leaning,
  which clarify didn't need to reopen:
  1. **Registry credentials**: public images only in Phase 1. A token in config would be a
     secret living outside Nomad Variables (constitution VII); private registries come with
     secrets (008).
  2. **Deploy that fails before Nomad**: recorded as a `failed` deployment, because the history is
     the first place an operator looks. Feature 002's status rules already allow `pending` →
     `failed`.
  3. **Restart mid-rollout**: resume following from Nomad on startup; Nomad keeps the rollout's
     state, so nothing is lost and no "unknown" status is needed.
- **Deploy response status** is 202 Accepted with a `Location` of
  `/v1/apps/{id}/deployments/{deploymentId}`, since the rollout continues after the response.
- **Trigger**: the API's deploy call records `manual`; feature 004 passes `webhook`.
- **Following a rollout** stops at the rollout's end; Nomad's own deadlines (from feature 001's
  update strategy) bound how long that takes.
- **Registry client** (docs/plan.md open decision 1): our own small client, no new module
  (plan research R1).
- **Nomad client** is the official `github.com/hashicorp/nomad/api` module the constitution
  names. It's already in `go.mod` from feature 001, so this feature adds no module.
- **Out of scope**: the webhook endpoint (004), real HTTPS on a public VM and the scripted
  auto-revert proof (005), auth (006), UI (007), secrets and private registries (008), compose
  (009), rollback to a chosen version and job export (011), multi-node.
