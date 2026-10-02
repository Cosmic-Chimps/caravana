# Feature Specification: Control Plane Skeleton and API Contract

**Feature Branch**: `002-control-plane-skeleton`

**Created**: 2026-10-02

**Status**: Draft

**Input**: User description: "Feature 002: Control plane skeleton and API contract. A running
Caravana process for the deploy flow (003) and webhook (004) to live in: reads its config,
serves HTTP, speaks the API's error format, and stores apps and deployments, without calling
Nomad or any registry. Starts `api/openapi.yaml`, the contract the .NET Aspire client is
generated from."

## Context

Consumers of this feature:

- **Operators** who run the `caravana` binary on their VM (today the maintainer, next to the
  local lab). They need it to start with sensible defaults, fail clearly on bad config, and stop
  cleanly.
- **API clients** (curl today, the generated .NET client from Phase 2.5). They need a published
  contract and errors they can rely on.
- **Features 003 and 004**, which add the deploy and webhook endpoints on this server and record
  their results in its store. Everything they need from the plumbing must exist here, so they
  only add behavior.

"Endpoint", "contract" (the OpenAPI document), "store" and "migration" are the product's own
parts, described here by what they must do, not how.

## Clarifications

### Session 2026-10-02

- Q: With no auth until feature 006, what is the default listen address? → A: Loopback
  (`127.0.0.1:<port>`) by default; any other address is allowed but logs a startup warning that
  the API has no authentication yet.
- Q: Store apps as normalized tables or as a versioned document? → A: One row per app with
  `id`, `created_at`, `updated_at` and `schema_version` columns plus the full app as a JSON
  document. Deployments are a normal table, with digests and warnings as JSON columns.
- Q: Which deployment statuses exist? → A: `pending` → `running` → one final status: `healthy`,
  `failed`, `reverted` (failed and Nomad rolled back to the previous version) or `superseded` (a
  newer deploy replaced it mid-rollout). The store rejects unknown statuses and any change after
  a final status.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - An operator starts Caravana and sees it's healthy (Priority: P1)

The operator runs the binary with no configuration. It creates its data file, prepares the
store, and starts serving. A health request says it's up and its store is reachable. Stopping
it lets in-flight requests finish. Running it again reuses the same data and changes nothing.

**Why this priority**: every later feature runs inside this process. Without it there's nothing
to deploy from.

**Independent Test**: start the binary in an empty folder, call the health endpoint, stop it
with Ctrl-C, start it again, call health again.

**Acceptance Scenarios**:

1. **Given** an empty data directory, **When** Caravana starts with only defaults, **Then** it
   creates its data file, applies every migration, and the health endpoint answers 200 with a
   JSON body.
2. **Given** a data directory from an earlier run, **When** Caravana starts again, **Then** it
   applies no migration and keeps the existing data.
3. **Given** a configuration value that's invalid (e.g. default memory `-5`), **When** Caravana
   starts, **Then** it exits with a non-zero status and a message naming that setting, and
   doesn't start serving.
4. **Given** a running server with a request in progress, **When** it receives SIGINT or
   SIGTERM, **Then** it stops accepting new connections, lets the request finish within the
   shutdown timeout, and exits 0.
5. **Given** the store can't be reached, **When** a client calls health, **Then** it answers
   503 with the standard error body.

---

### User Story 2 - API clients get predictable JSON and errors (Priority: P2)

Every endpoint, now and later, follows the same rules: JSON in and out, the standard error body
with the right status, and the field named when input is wrong. A client never gets an HTML
page, a plain-text error, or a redirect for a bad request.

**Why this priority**: the .NET client is generated from the contract and maps errors by code
and field. If these rules drift endpoint by endpoint, every client breaks differently.

**Independent Test**: requests with good and bad media types, unknown fields, oversized and
malformed bodies, unknown paths and wrong methods, each checked for status and error body.
Request-body rules are tested through the shared decoding helper, since this feature's only
endpoint takes no body.

**Acceptance Scenarios**:

1. **Given** a JSON body sent as `application/json; charset=UTF-8`, **When** it's decoded,
   **Then** it's accepted exactly like plain `application/json`.
2. **Given** a body sent as `text/plain`, **When** it's decoded, **Then** the client gets 415
   with the standard error body.
3. **Given** a body with a field the request doesn't define, **When** it's decoded, **Then** the
   client gets 400 and `field` names the unknown field.
4. **Given** a body over the size limit, **When** it's decoded, **Then** the client gets 413.
5. **Given** a model validation error from feature 001 (e.g. `services[0].domains[1].port`),
   **When** a handler returns it, **Then** the client gets 400 with that exact `field`.
6. **Given** an unknown path or a wrong method, **When** it's requested, **Then** the client gets
   404 or 405 with the standard JSON error body, never a redirect or plain text.
7. **Given** a handler fails unexpectedly, **When** the request completes, **Then** the client
   gets 500 with a generic message and no internal details.

---

### User Story 3 - The store records apps and deployments for the deploy flow (Priority: P3)

The deploy flow (003) saves the app it was given, creates a deployment record per deploy, and
later records its outcome. Anyone can then read an app and its deployment history.

**Why this priority**: 003 can't be idempotent ("same payload twice changes nothing except a new
deployment record") or report history without it. It has no endpoint yet, so it's tested
directly.

**Independent Test**: tests against a temporary store: save an app twice, create three
deployments, update one, list them.

**Acceptance Scenarios**:

1. **Given** no app `whoami`, **When** it's saved, **Then** reading `whoami` returns the same
   app; **When** a changed version is saved under the same ID, **Then** reading returns the new
   version and there's still exactly one app `whoami`.
2. **Given** an app, **When** three deployments are created for it, **Then** listing returns all
   three, newest first, each with its own ID, trigger, digests and initial status.
3. **Given** a deployment, **When** its status and Nomad job version are updated, **Then**
   reading it returns the new values and its update time changes.
4. **Given** an unknown app or deployment ID, **When** it's read or updated, **Then** the store
   reports "not found" distinctly from other failures.
5. **Given** a deployment for an app that doesn't exist, **When** it's created, **Then** the
   store rejects it.
6. **Given** a deployment that is `healthy`, **When** its status is changed to `running`,
   **Then** the store rejects the change and the record stays `healthy`.
7. **Given** a `running` deployment whose rollout Nomad reverted, **When** it's marked
   `reverted` with an error message, **Then** reading it returns `reverted` and the message.

---

### User Story 4 - The contract matches the server (Priority: P4)

`api/openapi.yaml` describes exactly the endpoints the server has, plus the shared error schema.
A test fails when a route exists without a contract entry or the reverse.

**Why this priority**: constitution V requires the contract to change with every endpoint, and
the .NET client is generated from it. Starting with one endpoint is when the guard is cheapest
to add.

**Independent Test**: the contract-sync test, and validating the document as OpenAPI 3.1.

**Acceptance Scenarios**:

1. **Given** the server's routes and the contract, **When** the sync test runs, **Then** every
   method+path pair appears in both.
2. **Given** a route added without a contract entry (or the reverse), **When** the sync test
   runs, **Then** it fails naming the missing pair.
3. **Given** the contract, **When** it's validated, **Then** it's valid OpenAPI 3.1 and its error
   schema matches the bodies the server actually returns.

---

### Edge Cases

- **Network exposure without auth**: auth arrives in feature 006, so the default listen address
  is loopback only. Setting a non-loopback address (`:<port>` with no host, `0.0.0.0:<port>`, or
a public IP) works,
  but startup logs a warning that the API has no authentication yet. Feature 006 removes the
  warning.
- **Unclean paths** (`//v1/health`, `/v1/health/`, `/v1/../v1/health`): answered with a JSON
  404 (or the matching route), never a redirect.
- **Query strings and paths in logs**: the query string is never logged, because later features
  may carry tokens there; only method, path, status and duration are.
- **Authorization or token headers**: never logged, whatever their name's case.
- **Empty body where one is required**: 400 with the standard error body.
- **Body with trailing data after the JSON value** (`{} {}`): 400.
- **Two Caravana processes on one data directory**: the second one fails at startup with a clear
  message instead of corrupting the store.
- **Data directory doesn't exist**: created; if it can't be created or written, startup fails
  naming the data directory setting.
- **A migration fails halfway**: that migration is rolled back, startup fails naming it, and the
  store stays at the previous version.
- **Store has migrations this binary doesn't know** (an older binary on a newer store): startup
  fails rather than running against a schema it doesn't understand.
- **Shutdown timeout passes with requests still running**: they're cut off and the process exits
  non-zero, saying so.
- **Saved app stored, then the model gains a field** (later features): each app is stored as one
  document with a schema version, so a new optional field needs no migration and older
  documents still read back (the new field takes its "not set" value). A change that can't be
  read that way bumps the schema version, and the code that reads it upgrades older documents.
- **App document with a newer schema version than this binary knows**: reading it fails with a
  clear error rather than silently dropping fields it doesn't understand.
- **Deployment status changes**: a deployment starts `pending`. From `pending` it may become
  `running`, `failed` (it broke before reaching Nomad, e.g. the image couldn't be resolved) or
  `superseded`. From `running` it may stay `running` (to record the Nomad job version) or become
  `healthy`, `failed`, `reverted` or `superseded`. Final statuses never change. Any other change,
  or an unknown status, is rejected and leaves the record as it was.

## Requirements *(mandatory)*

### Functional Requirements

**Process and configuration**

- **FR-001**: The binary MUST only wire dependencies together: read config, open the store, run
  migrations, build the server, serve, and shut down. Behavior lives in internal packages.
- **FR-002**: Every setting MUST be readable from an environment variable and overridable by a
  command-line flag with the same meaning; a flag wins over the environment.
- **FR-003**: The settings MUST be exactly: listen address, data directory, Traefik entrypoint,
  certificate resolver, default CPU and default memory. No setting is added before a feature
  uses it.
- **FR-004**: Every setting MUST have a default that works for local development, so the binary
  starts with no configuration and without root. The default listen address MUST be loopback
  (`127.0.0.1`).
- **FR-004a**: When the listen address is not a loopback address, startup MUST log a warning
  that the API has no authentication yet, and then serve normally.
- **FR-005**: Invalid settings MUST stop startup with a non-zero exit and a message naming the
  setting (its environment variable and flag). The translator settings MUST be checked with the
  same rules feature 001 applies to them.
- **FR-006**: On SIGINT or SIGTERM the server MUST stop accepting connections, wait for
  in-flight requests up to a shutdown timeout, close the store, and exit 0 if everything
  finished in time, non-zero otherwise.

**HTTP and the contract**

- **FR-007**: All endpoints MUST live under `/v1`. This feature adds exactly one:
  `GET /v1/health`, answering 200 `{"status": "ok"}` when the store is reachable and 503 with the
  standard error body when it isn't.
- **FR-008**: `api/openapi.yaml` MUST be OpenAPI 3.1 and describe every endpoint and the error
  schema. An automated test MUST fail when the server's routes and the contract's operations
  differ.
- **FR-009**: Every error response MUST be JSON `{"error": {"code", "message", "field"}}` with
  `Content-Type: application/json`, where `field` is present only when one field is at fault.
  Codes are stable strings listed in the contract.
- **FR-010**: Unknown paths MUST get 404 and wrong methods 405 (with an `Allow` header), both
  with the standard error body. No request to the API gets a redirect.
- **FR-011**: Unexpected failures MUST get 500 with a generic message. Internal error details go
  to the log, never to the client.

**Request bodies** (shared helper every later endpoint uses)

- **FR-012**: A JSON body MUST be accepted when its media type is `application/json`, with or
  without parameters such as `charset=UTF-8` (case-insensitive). Any other or missing media type
  gets 415.
- **FR-013**: A body field the request doesn't define MUST get 400 naming that field.
- **FR-014**: A body over the size limit MUST get 413. Malformed JSON, an empty body, or data
  after the JSON value MUST get 400.
- **FR-015**: A feature-001 field error MUST map to 400 with code `invalid_field` and its field
  path unchanged. Request bodies MUST use the same camelCase names as those paths.

**Logging**

- **FR-016**: Every request MUST be logged once, structured, with method, path, status and
  duration.
- **FR-017**: Logs MUST NEVER contain request or response bodies, query strings, header values
  of `Authorization` or any header whose name contains `token`, `secret` or `key`, or env values.
  An automated test MUST prove a planted secret never reaches the log output.

**Store**

- **FR-018**: The store MUST be a single local file in the data directory, with schema
  migrations embedded in the binary, applied in order at startup, forward-only, each in its own
  transaction, and recorded so each runs once.
- **FR-019**: The store MUST support: save an app by ID (insert or replace, keeping one record
  per ID), get an app by ID, create a deployment for an existing app, update a deployment's
  status and Nomad job version, get a deployment by ID, and list an app's deployments newest
  first.
- **FR-020**: An app MUST round-trip through the store unchanged: every field of feature 001's
  model (including "replicas not set" vs a value, and env order) reads back exactly as saved.
- **FR-020a**: Each app MUST be stored as one record holding its ID, created and updated times,
  a schema version, and the whole app as a single document. Reading a document whose schema
  version is newer than the binary supports MUST fail with an error naming the app and version.
  Deployments MUST be a table of their own, with digests and warnings stored as documents in
  their columns.
- **FR-021**: A deployment record MUST hold its ID, app ID, trigger, digests per service,
  status, Nomad job version (empty until known), warnings, error message (empty unless failed),
  and created and updated times.
- **FR-021a**: Deployment status MUST be one of `pending`, `running`, `healthy`, `failed`,
  `reverted`, `superseded`. New deployments start `pending`. The store MUST allow only the
  changes listed under Edge Cases ("Deployment status changes") and reject anything else,
  including any change to a deployment in a final status (`healthy`, `failed`, `reverted`,
  `superseded`), with an error callers can tell apart from "not found".
- **FR-022**: Reading or updating an unknown ID MUST return a "not found" result that callers can
  tell apart from other errors, so the API can answer 404.
- **FR-023**: Only one Caravana process MAY use a data directory at a time; a second one MUST
  fail at startup naming the data directory.

### Key Entities

- **Configuration**: listen address, data directory, entrypoint, certificate resolver, default
  CPU and memory; with defaults, an environment variable and a flag each.
- **App record**: an app as feature 001 defines it, stored whole as one document with a schema
  version, keyed by app ID, with created and updated times.
- **Deployment record**: one deploy of an app: ID, app ID, trigger, digests, status, Nomad job
  version, warnings, error message, created and updated times. Status runs `pending` →
  `running` → `healthy` | `failed` | `reverted` | `superseded`; final statuses never change.
- **Error body**: code, message, optional field.
- **Migration**: an ordered, embedded schema change, applied once.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: From an empty folder, the binary starts with zero configuration and answers a
  health check within 2 seconds on the lab Mac.
- **SC-002**: A second start on the same data applies 0 migrations and loses 0 records.
- **SC-003**: 100% of invalid settings tested stop startup with a message naming the setting.
- **SC-004**: 100% of error responses in tests are JSON with the documented shape; 0 redirects
  and 0 plain-text bodies.
- **SC-005**: The contract-sync test reports 0 differences between routes and the contract.
- **SC-006**: A secret planted in a request (header, query string and body) appears 0 times in
  the captured log output.
- **SC-007**: An app saved and read back is identical in 100% of round-trip test cases,
  including the eight app shapes from feature 001's golden tests.
- **SC-008**: Every CI check passes, including the CGO-free cross-compile for amd64 and arm64
  with the store included.

## Assumptions

- **Default listen port** avoids the ports the lab already forwards on the Mac (8080, 8081,
  8443, 4646). The plan picks it.
- **Default data directory** is a folder under the working directory that git ignores, so
  `go run ./cmd/caravana` works from the repo root. Production installs (feature 012) set it to
  a system path.
- **Default translator settings** are the lab's: entrypoint `websecure`, no certificate resolver,
  100 MHz CPU, 64 MB memory.
- **Body size limit** is 1 MiB; compose files (009) may need a larger, per-endpoint limit later.
- **Shutdown timeout** is 10 seconds.
- **Health is unauthenticated** now and stays so after feature 006 (load balancers and install
  scripts need it).
- **Error codes** are lowercase snake_case: `bad_request`, `invalid_field`, `unknown_field`,
  `unsupported_media_type`, `body_too_large`, `not_found`, `method_not_allowed`, `internal`,
  `unavailable`. 003 and 004 may add more, each listed in the contract.
- **No HTTP API for apps or deployments yet**: the store functions are tested directly; 003 adds
  the endpoints that use them.
- **Dependencies**: the pure-Go SQLite driver is the one new module the constitution already
  names; the commit that adds it justifies it.
- **Out of scope**: calling Nomad or a registry (003), deploy and webhook endpoints (003, 004),
  auth (006), UI (007), secrets (008), metrics, multi-node.
