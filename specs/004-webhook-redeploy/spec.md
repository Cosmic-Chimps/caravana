# Feature Specification: Webhook Redeploy

**Feature Branch**: `004-webhook-redeploy`

**Created**: 2026-10-02

**Status**: Draft

**Input**: User description: "Feature 004" — from docs/plan.md: "Webhook redeploy. Per-app
secret token, constant-time compare, same service call as 003. Token shown once, stored hashed."

## Context

A webhook lets something outside Caravana (a container registry after a push, a CI job after a
build) say "redeploy this app" with one HTTP request, without any other access to Caravana.
It's the first half of the Phase 1 gate: `traefik/whoami` deployed by a webhook.

Consumers of this feature:

- **Senders**: Docker Hub and GHCR push notifications, CI pipelines (GitHub Actions, a `curl` in
  any script). Many of them can only be given a URL; some can also set a header. They can't
  read Caravana's answers in any rich way and may retry.
- **Operators**: create a webhook token for an app, paste the URL into the registry or CI,
  rotate it when it leaks, turn it off. They need to see which deployments a webhook started.
- **Feature 003**: already has the service call this feature uses (`Redeploy` with trigger
  `webhook`), including digest re-resolution, ordering and "newest wins". This feature adds
  the token and the endpoint, not a second deploy path.

A **webhook token** is a long random secret that authorizes redeploying one app and nothing
else. "Redeploy" means feature 003's deploy of the stored app: every tag is resolved again, so
a moved `latest` is picked up.

## Clarifications

### Session 2026-10-02

- Q: How do senders reach the webhook while the rest of the API stays private? → A: The webhook
  route is on the main API (loopback by default) and also on an optional webhook-only listener,
  off by default, that serves nothing but the webhook route. Feature 005 exposes only that
  listener.
- Q: What does a webhook call do when nothing changed? → A: It skips. Every image is resolved;
  if each digest equals the app's latest `healthy` deployment's and the stored app hasn't
  changed since that deployment, it answers 200 with that deployment and "unchanged", and
  creates no record and no job version. Otherwise it redeploys (202).

## User Scenarios & Testing *(mandatory)*

### User Story 1 - A push redeploys the app (Priority: P1)

An operator has deployed whoami with `traefik/whoami:latest`. They create a webhook token for
it and get the webhook URL with the token, shown once. A sender calls that URL. Caravana
checks the token, redeploys the stored app exactly as a deploy with an empty body would, and
answers right away with the new deployment. The deployment record says it was started by a
webhook.

**Why this priority**: it's the feature, and half of the Phase 1 gate.

**Independent Test**: service and API tests with feature 003's fake registry and fake Nomad:
create a token, call the webhook, follow the deployment to `healthy`; in the lab, the same with
`curl`.

**Acceptance Scenarios**:

1. **Given** a stored app `whoami` with no webhook token, **When** the operator creates one,
   **Then** the response holds the token and the full webhook path, and no later request ever
   returns the token again.
2. **Given** a valid token for `whoami` and a changed image, **When** a sender calls the webhook
   with it, **Then** the client gets 202 with the new deployment (ID, Nomad job version, warnings) and a
   `Location` for it, and the deployment record has trigger `webhook`.
3. **Given** the tag `latest` now points to a new digest in the registry, **When** the webhook
   is called, **Then** the new deployment pins the new digest and the stored app is unchanged.
4. **Given** whoami `healthy` and the registry still has the same digest for every image,
   **When** the webhook is called, **Then** the client gets 200 saying nothing changed, with the
   live deployment, and no deployment record or Nomad job version is created.
5. **Given** a sender that can only be given a URL (no headers), **When** it calls the webhook
   URL as shown, **Then** it works; **and given** a sender that can set a header, **When** it
   sends the token in the header instead, **Then** it works the same.
6. **Given** the webhook call, **When** the server logs it, **Then** the log line holds method,
   path, status and duration, and never the token.

---

### User Story 2 - Only the right token deploys, and only its app (Priority: P1)

A webhook URL ends up somewhere public: a CI log, a screenshot. Someone without the token, or
with another app's token, gets nothing: no deploy, and no hint whether the app exists.

**Why this priority**: the webhook is the first endpoint meant to be reachable from outside;
a weak check here lets anyone redeploy anything.

**Independent Test**: API tests with missing, wrong, other-app, rotated and revoked tokens,
checking status, body and that no deployment record was created.

**Acceptance Scenarios**:

1. **Given** no token, a wrong token, or another app's token, **When** the webhook is called,
   **Then** the client gets 401 with the standard error body, no deployment is created and
   Nomad is untouched.
2. **Given** an app ID that doesn't exist, **When** the webhook is called with any token,
   **Then** the answer is the same 401 as a wrong token, so callers can't probe which apps
   exist.
3. **Given** an app that has never had a webhook token, **When** the webhook is called,
   **Then** it answers 401 the same way.
4. **Given** two tokens that differ only in the last character, **When** the server compares
   them, **Then** the comparison takes the same time whatever position differs.
5. **Given** the store, **When** it's read directly, **Then** it holds no token in a form that
   can be used to call the webhook.
6. **Given** the webhook-only listener is turned on, **When** anything other than the webhook
   is requested on it (health, token management, deploy, reads), **Then** it answers 404 with
   the standard error body, while a valid webhook call on it redeploys as on the main API.

---

### User Story 3 - Rotate or turn off a token (Priority: P2)

The operator suspects a token leaked. They rotate it: a new token is shown once, and the old
one stops working at once. Or they turn the webhook off: the token is deleted and the app can
only be deployed through the API.

**Why this priority**: tokens leak; an operator needs a way out that doesn't involve deleting
the app. P2 because US1 and US2 work without it.

**Independent Test**: API tests: rotate, then call with old and new; revoke, then call.

**Acceptance Scenarios**:

1. **Given** a token for `whoami`, **When** the operator creates a token again, **Then** a new
   token is returned and the old one answers 401 from that moment on.
2. **Given** a token for `whoami`, **When** the operator deletes it, **Then** the webhook answers
   401 for any token, and deploying through the API still works.
3. **Given** an app with a token, **When** the operator reads the app's webhook status, **Then**
   it shows that a token exists and when it was created, never the token itself.

---

### User Story 4 - Webhook deploys behave like any other deploy (Priority: P2)

Two pushes arrive seconds apart; a push arrives for an image tag that doesn't exist; the
registry is down. A webhook deploy follows every rule feature 003 set: the newest wins, the
older one ends `superseded`, failures are recorded and answered with the right status, and
nothing is half-deployed.

**Why this priority**: senders retry and burst. P2 because the behavior is feature 003's;
this story is about proving the webhook doesn't bypass it.

**Independent Test**: service tests that drive the webhook path into each of feature 003's
outcomes.

**Acceptance Scenarios**:

1. **Given** a changed image and two webhook calls for `whoami` in quick succession, **When**
   both finish, **Then**
   the job Nomad runs is the one requested last, and the other ends `superseded`.
2. **Given** a stored app whose image tag no longer exists, **When** the webhook is called,
   **Then** the client gets 422 `image_not_found` with a `Location`, and the record (trigger
   `webhook`) ends `failed`.
3. **Given** the registry or Nomad is unreachable, **When** the webhook is called, **Then** the
   client gets feature 003's 502 for it, and the record ends `failed`.

---

### Edge Cases

- **Request body**: senders post their own payloads (Docker Hub's push JSON, GitHub's event
  JSON) or nothing. The body is read up to the usual 1 MiB limit and otherwise ignored; any
  content type is accepted. A larger body gets 413, before any deploy.
- **Method**: only `POST` triggers a redeploy. Other methods get the standard 405.
- **Token in both places** (header and query) with different values: 401. The same value in
  both: accepted.
- **Token format**: anything that isn't exactly a well-formed token answers 401 without any
  store lookup beyond what a wrong token costs, so malformed input can't be told apart either.
- **Many failed attempts**: each answers 401; Phase 1 has no lockout or rate limit (a token is
  long enough that guessing it is not a practical attack). The failures are logged without the
  token.
- **Token created for an app that doesn't exist**: 404, as for any app endpoint.
- **App deployed again through the API after a token was created**: the token keeps working;
  the webhook then redeploys whatever definition was sent last (feature 003, FR-017a).
- **Retried webhook after a timeout**: if the first call's deploy hasn't reached `healthy` yet,
  the live deployment still has the old digests, so the retry redeploys too. Feature 003's
  ordering makes the later one win; the earlier ends `superseded` if it hadn't reached Nomad,
  or Nomad cancels its rollout. Once it is `healthy`, a retry is "unchanged".
- **Unrelated push**: a push of a tag the app doesn't use resolves to the same digests and is
  "unchanged" (200, nothing deployed).
- **Last deploy failed or reverted**: the comparison is with the latest `healthy` deployment,
  which is what Nomad runs; a stored app edited after it always redeploys.
- **Forcing a redeploy**: through the API (`POST /v1/apps/{id}/deployments` with an empty body),
  which always deploys.
- **Token shown in logs by the sender** (a CI log printing the URL): out of Caravana's control;
  rotation (US3) is the remedy.
- **Token in a proxy's access log**: a query-string token is visible to any proxy in front of
  the webhook-only listener. Senders that can set a header should use `Authorization: Bearer`;
  feature 005 must keep the token out of Traefik's access log (off, or the query string
  dropped).
- **Webhook-only listener**: `GET /v1/health`, token management and every app endpoint answer
  404 there, never a redirect; the same webhook call works on both listeners. Shutdown stops
  both listeners the same way.
- **Restart**: tokens survive restarts; a webhook deploy left unfinished resumes like any other
  (feature 003).

## Requirements *(mandatory)*

### Functional Requirements

**Tokens**

- **FR-001**: The system MUST let an operator create a webhook token for a stored app. The
  response MUST contain the token and the webhook path, and the token MUST NOT be retrievable
  afterwards by any request.
- **FR-002**: Each app MUST have at most one webhook token. Creating a token for an app that has
  one replaces it, and the old token MUST stop working immediately.
- **FR-003**: The system MUST let an operator delete an app's webhook token, after which the
  webhook MUST refuse every call for that app.
- **FR-004**: The system MUST let an operator see whether an app has a webhook token and when it
  was created, without revealing the token.
- **FR-005**: Tokens MUST be random, with at least 256 bits of entropy, and carry a recognizable
  prefix so secret scanners can find leaked ones.
- **FR-006**: The store MUST keep only a one-way hash of each token, never the token.
- **FR-007**: Checking a token MUST take the same time wherever it differs from the stored one.

**Webhook**

- **FR-008**: The system MUST offer one webhook endpoint per app that, given that app's valid
  token, redeploys the stored app through feature 003's service call with trigger `webhook`,
  and answers like a deploy: 202 with the deployment and a `Location`, or feature 003's error
  for the failure.
- **FR-008a**: Before redeploying, the webhook MUST resolve every image. If every digest equals
  the one in the app's latest `healthy` deployment, and the stored app hasn't changed since that
  deployment was created, it MUST NOT deploy: it answers 200, says the app is unchanged, and
  returns that deployment, with no new record and no new Nomad job version. With no `healthy`
  deployment, or any difference, it redeploys. A failed resolution fails as in feature 003.
- **FR-009**: The token MUST be accepted from a query parameter (for senders that only take a
  URL) or from an `Authorization: Bearer` header. Both present with different values MUST be
  refused.
- **FR-010**: A missing, wrong, revoked or other app's token, an app without a token and an app
  that doesn't exist MUST all get the same 401 response with the standard error body, and
  create no deployment.
- **FR-011**: The webhook MUST ignore the request body (up to the 1 MiB limit) and accept any
  content type.
- **FR-012**: The token MUST NEVER appear in logs, error messages or responses other than the
  one that creates it.
- **FR-012a**: The system MUST offer an optional webhook-only listener, with its own address
  setting (env var and flag), off by default. It MUST serve only the webhook route: every other
  path, including health and every management endpoint, answers with the standard 404 body,
  and the webhook behaves there exactly as on the main API. Startup MUST fail, naming the
  setting, if its address is invalid or the same as the main listener's.

**API**

- **FR-013**: The webhook endpoint and the token endpoints (create, read status, delete) MUST be
  in `api/openapi.yaml` in the same change, with request and response bodies validated against
  it in tests, and the new error code (`unauthorized`) listed.
- **FR-014**: The token endpoints MUST go through `internal/service`, like every other endpoint
  (constitution V); the webhook MUST use the same `Redeploy` call feature 003's empty-body
  deploy uses.

**Testing**

- **FR-015**: Tests MUST run without a network, using feature 003's fake registry and fake
  Nomad, and cover: create, call, rotate, revoke, every 401 case, the body and content-type
  rules, the log never holding the token, and a webhook deploy reaching each of feature 003's
  outcomes.
- **FR-016**: The opt-in lab test MUST, through the webhook-only listener: call the webhook
  right after whoami's first API deploy (its rollout still running, so no `healthy` deployment
  yet) and see that webhook deploy reach `healthy` while the API's ends `superseded`; then call
  it again and get "unchanged" with no new job version. (The lab can't move a Docker Hub tag,
  so this is how a real redeploy is exercised; moved tags are covered by the fake-registry
  tests.)

### Key Entities

- **Webhook token**: belongs to one app; at most one per app. Stored as a hash plus its creation
  time; the plain token exists only in the create response.
- **Deployment record** (feature 002/003): unchanged; webhook deploys carry trigger `webhook`.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: In the lab, a webhook call for whoami that has to deploy answers 202 within 10
  seconds and its deployment reaches `healthy`; the next call answers 200 "unchanged".
- **SC-001a**: A webhook call whose images are unchanged answers 200 and creates 0 deployment
  records and 0 Nomad job versions.
- **SC-002**: 100% of webhook calls with a missing, wrong, revoked, other-app or unknown-app
  token in tests answer the same 401 body and create 0 deployments.
- **SC-003**: After a rotation, the old token succeeds 0 times.
- **SC-004**: A token planted in the query string, the header and the body appears 0 times in
  captured logs and in every response except the one that created it.
- **SC-005**: The stored token data can't be used to call the webhook: 0 successful calls in
  tests made with the stored value.
- **SC-006**: Every CI check passes, and the contract-sync test reports 0 differences.
- **SC-007**: On the webhook-only listener, 100% of requests to any route other than the webhook
  answer 404, and the webhook answers the same as on the main API.

## Assumptions

- **Open questions for `/speckit-clarify`**, with the defaults this spec uses:
  1. **Where senders reach the webhook**: settled in Clarifications (webhook-only listener).
  2. **A push that changes nothing**: settled in Clarifications (skip, 200, no record).
  3. **Signed webhooks.** GitHub signs its payloads (HMAC). Default: out of scope; the token
     covers every sender, and GHCR users can use it from a GitHub Actions step.
- **Token management is unauthenticated for now**, like every other endpoint until feature 006,
  which is why the server stays on loopback by default.
- **The token hash is not a secret** in the constitution's sense (VII): it can't be used to call
  the webhook, so it lives in the SQLite store next to the app, not in Nomad Variables.
- **A fast hash is enough**: tokens are long and random, so a slow password hash adds nothing.
  The plan picks the exact hash.
- **One token per app** keeps rotation simple; several named tokens per app can come later.
- **Out of scope**: exposing the webhook-only listener publicly over HTTPS (005), auth for the management
  endpoints (006), UI for tokens (007), HMAC-signed payloads, filtering by pushed tag, rate
  limiting.
