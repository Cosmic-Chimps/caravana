# Research: Deploy Flow

Decisions for feature 003, each with what was tried or checked. Versions: go1.27.1,
`github.com/hashicorp/nomad/api v0.0.0-20260917172403-9dcbdc5e64ec` (already in `go.mod`
from feature 001), Nomad 2.0.7 in the lab.

## R1. Registry client: our own, not go-containerregistry (docs/plan.md open decision 1)

- **Decision**: write a small registry v2 client in `internal/registry` (reference parsing, one
  `HEAD` per image, the anonymous bearer-token handshake). No new module.
- **Rationale**: we need one operation, tag → digest, against two registries, anonymously.
  That's a few hundred lines of plain `net/http`, fully testable with `httptest`.
  `github.com/google/go-containerregistry` v0.22.1 (checked 2026-10-02) requires
  `github.com/docker/cli`, `github.com/moby/moby/client`, `golang.org/x/oauth2`,
  `github.com/spf13/cobra`, `klauspost/compress` and more: a large tree for one `HEAD`
  request, against principle I. Our own client also keeps errors in our words (FR-011).
- **Alternatives**: go-containerregistry (rejected: dependency weight, and its keychain reads
  `~/.docker/config.json`, which the control plane must not do); `github.com/distribution/reference`
  for parsing only (rejected: the subset we need is a dozen lines, see R2).
- **Revisit when**: private registries (008) or more registries are needed. Credentials are
  one extra header on the same code path.

## R2. Image reference parsing

- **Decision**: `registry.Parse(image)` accepts `[host/]path[:tag][@sha256:<64 hex>]`:
  - The first path part is a host when it contains `.` or `:`, or is `localhost`; otherwise
    the host is `docker.io`. `index.docker.io` is the same as `docker.io`.
  - On Docker Hub, a one-part path gets `library/` (`nginx` → `library/nginx`).
  - Path parts use the same lowercase grammar as `app`'s digest pattern; a tag matches
    `[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}`. No tag and no digest means `latest`.
  - A digest wins over a tag. The tag is kept in `Reference.IgnoredTag`, and `Resolve` turns it
    into a warning on `services[i].image`: `tag "v1" is ignored: the digest pins the image`
    (constitution IV: nothing is dropped silently).
  - Supported hosts: `docker.io` and `ghcr.io`. Anything else fails with "registry %q isn't
    supported yet (Docker Hub and GHCR only)".
  - The pinned image keeps the name **as the user wrote it**, minus tag and digest:
    `traefik/whoami@sha256:…`, `nginx@sha256:…`, `ghcr.io/acme/shop@sha256:…`. That matches the
    lab's hand-written `whoami.json` and `app`'s digest pattern, and Docker resolves the short
    names the same way we do.
- **Parsing is validation**: the service parses every image before saving the app, so an
  unparseable reference or unsupported registry is a 400 `invalid_field` on
  `services[i].image`, with nothing saved (spec "What counts as validation").

## R3. Resolving a tag to a digest

- **Decision**: `HEAD https://<endpoint>/v2/<repo>/manifests/<tag>` with
  `Accept: application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json`,
  and read the `Docker-Content-Digest` header. If a registry omits it, fall back to `GET` and
  hash the body with SHA-256. The result must match `sha256:<64 lowercase hex>` or the
  resolution fails.
- **Multi-arch (FR-009)**: listing the index media types first makes the registry answer with
  the index for a multi-arch tag, so the digest is the index's and each node pulls its own
  architecture. A single-arch image answers with its manifest digest.
- **Endpoints**: `docker.io` → `registry-1.docker.io`; `ghcr.io` → `ghcr.io`. The client has an
  `Endpoints` map tests override with an `httptest` URL.
- **Rate limits**: Docker Hub doesn't count `HEAD` manifest requests toward the anonymous pull
  limit, which is why the client never `GET`s unless the digest header is missing.
- **Timeout and retries**: each image gets 10 s in total. Network errors and 5xx get two
  retries (0.5 s, then 1 s); 4xx don't retry. 429 is reported as "registry is rate-limiting"
  (FR-011), not retried.
- **Several services**: resolved one at a time, in service order; the first failure stops the
  deploy and names that service's image field.

## R4. Anonymous token handshake

- **Decision**: on `401` with `WWW-Authenticate: Bearer realm="…",service="…",scope="…"`,
  `GET realm?service=…&scope=repository:<repo>:pull` with no credentials, read `token` (or
  `access_token`) from the JSON, and retry once with `Authorization: Bearer <token>`. Tokens
  live only for that one resolution: no cache, never logged.
- **Not found vs private**: Docker Hub answers a missing repository with `401` even with a
  valid anonymous token, and GHCR may refuse the token itself (`401`/`403`). Without
  credentials we can't tell "missing" from "private", so a `401`/`403` after the handshake,
  a token refusal, and a `404` all map to `registry.ErrNotFound`, with the message "image not
  found, or private (Caravana doesn't support registry credentials yet)".
- **Errors**: `registry.ErrNotFound` (→ 422 `image_not_found`) and `registry.ErrUnavailable`
  (network errors, 5xx, 429, an unreadable digest → 502 `registry_unavailable`). Both are
  wrapped with the registry host and image.

## R5. Durations in API bodies: duration strings, v2 marshalers at the API boundary

- **Decision**: the API decodes and encodes bodies with `encoding/json/v2` and one shared
  option set: `json.WithUnmarshalers` / `json.WithMarshalers` for `time.Duration` (string ↔
  `time.ParseDuration` / `Duration.String`), plus `json.Deterministic(true)` and, for
  decoding, `json.RejectUnknownMembers(true)`. A zero duration encodes as `""`, which v2's
  `omitempty` leaves out, so `GET /v1/apps/{id}` returns exactly what was sent. The store keeps
  feature 002's v1 encoding (integer nanoseconds), unchanged.
- **Trial on go1.27.1**: `{"health":{"interval":"10s","timeout":"1m30s"}}` decodes to 10s and
  1m30s and re-encodes the same; `"interval": 10` fails with a `*json.SemanticError` at
  `/health/interval` ("must be a duration string such as \"10s\""); `"ten"` fails at the same
  pointer; an unset interval is left out on encode.
- **Rationale**: `app.HealthCheck` keeps `time.Duration` (no model change, no store migration,
  no golden-file change). The existing pointer → field-path conversion (`decode.go`) reports
  `services[0].health.interval` for free.
- **Rule update from feature 002's R5**: "v2 for everything the API reads or writes, v1 in the
  store". Error and health bodies switch to v2 too; their JSON doesn't change.
- **Alternatives**: a `Duration` string type in `internal/app` (rejected: changes the model and
  the stored document for an API detail); API DTOs mirroring every model type (rejected: a
  second copy of the model to keep in sync).

## R6. Registering the job and learning its version

- **Decision**:
  1. `Jobs().Info(appID)`: a 404 means no job (expected modify index 0). A job with
     `Stop == true` isn't a conflict. Otherwise the job is Caravana's only if its
     `caravana.deployment` meta names a deployment the store has for this app (R7).
  2. `Jobs().RegisterOpts(job, &RegisterOptions{EnforceIndex: true, ModifyIndex: <index from
     step 1>})`. Enforcing the index closes the gap between the ownership check and the
     write: if anyone changed the job in between, Nomad refuses and the deploy fails with
     `job_conflict` ("the Nomad job changed while deploying; try again").
  3. `Jobs().Versions(appID)`: the registered version is the one whose `caravana.deployment`
     meta equals this deployment's ID. `JobRegisterResponse` has no version field, only
     `JobModifyIndex`; matching on our own ID is exact even if something else registers
     right after us. If `Versions` fails, or doesn't list our deployment yet, retry twice
     (0.5 s, then 1 s). If it still can't be read, the job **was accepted**: `Register` returns
     `Registered{JobVersion: nil}` with no error. The service records `running` without a
     version and answers 202; the watcher first learns the version with `FindVersion` (with its
     backoff), records it (`running` → `running`), then watches.
  4. Nomad's register `Warnings` string, if any, becomes one `app.Warning` with an empty field
     and the text prefixed by "nomad: ".
- **Errors**: the client returns `api.UnexpectedResponseError` (a value type) with
  `StatusCode()` and `Body()`. 403 → `deploy.ErrNomadUnavailable` "Nomad refused the token";
  other 4xx/5xx on register → `deploy.ErrNomadRejected` with Nomad's body as the message
  (FR-014); a body containing "conflicting job modify index" → `deploy.ErrJobConflict`;
  connection errors → `deploy.ErrNomadUnavailable` "Nomad is unreachable at <addr>". The token
  is never part of a message: errors come from Nomad's response body or `net` errors, neither
  of which echoes request headers.
- **Store write fails after registration**: the service logs it and still answers 202. The
  watcher writes `running` + version (retrying with backoff) before it writes the outcome. If
  Caravana dies first, the restart path (R10) finds the version by deployment ID.
- **Context**: every call passes `(&api.QueryOptions{}).WithContext(ctx)` /
  `WriteOptions.WithContext`, so cancellation works (constitution VIII).

## R7. Who owns a Nomad job

- **Decision**: a job is Caravana's when `Meta["caravana.deployment"]` names a deployment the
  store has recorded **for that app**. The deploy package gets an `owns func(ctx, deploymentID)
  (bool, error)` callback from the service, so `deploy` never imports `store`.
- **Why not "has caravana meta"**: the lab's `deploy/dev/whoami.json` (feature 001's golden
  file) carries `caravana.app` and `caravana.deployment: "lab-manual"`. Meta alone would let
  Caravana silently take over the hand-deployed job, which is exactly what clarification Q4
  forbids. The spec's FR-013a now says this.
- **After an auto-revert**, the running job carries an earlier deployment's ID, which the store
  has, so it's still Caravana's. **With a fresh data directory**, Caravana's own old jobs
  become conflicts: loud, and adopting them is left to 011.

## R8. Following the rollout

- **Decision**: `deploy.Watch(ctx, appID, jobVersion)` loops on
  `Jobs().Deployments(appID, false, q)` with blocking queries (`WaitIndex` = last index,
  `WaitTime` 5 min), picks the deployment whose `JobVersion` equals ours, and maps it:

  | Nomad `Status` | `StatusDescription` | Outcome |
  | --- | --- | --- |
  | `successful` | any | `healthy` |
  | `failed` | contains ` - rolling back to job version ` | `reverted` |
  | `failed` | anything else (incl. ` - no stable job version to auto revert to`) | `failed` |
  | `cancelled` | `Cancelled due to newer version of job` | `superseded` |
  | `cancelled` | anything else (e.g. `Cancelled because job is stopped`) | `failed` |
  | `pending`, `running`, `paused`, `blocked`, `unblocking`, `initializing` | — | keep watching |

  The outcome's message is Nomad's description (e.g. "Failed due to unhealthy allocations -
  rolling back to job version 3").
- **Why parse descriptions**: Nomad's deployment object has no "rolled back" field. The rollback
  and newer-job texts come from `nomad/structs` (`DeploymentStatusDescriptionRollback`,
  `…NewerJob`, `…NoRollbackTarget`), which the `api` module doesn't export. They're constants
  in `internal/deploy`, covered by the fake-Nomad tests and confirmed against the real lab by
  the opt-in lab test (quickstart step 4), so a Nomad upgrade that changes them fails a test
  instead of mislabelling records.
- **No deployment yet**: right after registration Nomad may not have created one; keep
  waiting. If the job's current version is already newer than ours and no deployment for ours
  ever appeared, the outcome is `superseded`.
- **Nomad unreachable while watching** (FR-015): errors back off (1 s doubling to 30 s) and
  retry forever; only `ctx` cancellation (shutdown) stops the loop, leaving the record
  `running` for the next start (R10). The minimum poll gap (1 s) stops a fake or misbehaving
  server from causing a busy loop; tests shorten it.
- **Bound**: Nomad's own progress deadline (10 min, feature 001) ends every rollout.

## R9. Concurrency: per-app lock plus "latest request wins"

- **Decision**: the service keeps, per app ID, a `sync.Mutex` and the ID of the most recently
  requested deployment. A deploy:
  1. records itself as the app's latest (under a short lock on the map),
  2. takes the app's mutex, resolves the images, then **checks it's still the latest**. If a
     newer request arrived meanwhile, it ends `superseded` without registering,
  3. registers, records `running` + job version, and releases the mutex.

  Watching happens after the mutex is released, so a newer deploy can register while the older
  rollout is still running. Nomad then cancels the older rollout, and its watcher records
  `superseded`.
- **Why the latest check**: `sync.Mutex` isn't FIFO; without the check, an older request could
  get the mutex after a newer one and register last. With it, an older deploy can never
  register after a newer one (FR-019), whatever order the goroutines wake in.
- **Different apps** use different mutexes (FR-020). The map grows by one small entry per app
  ever deployed, which is fine at Phase 1 scale.
- **Response for a deploy superseded before registration**: 202 with `status: superseded` and
  no `jobVersion`. It isn't an error: the client's intent (deploy the latest) is carried out by
  the newer request.

## R10. Restart while deploys are unfinished

- **Decision**: `Service.Start(ctx)` lists every deployment still `pending` or `running` (new
  store method) and handles each in its own goroutine, so startup never waits on Nomad:
  - `running` with a job version → watch it again (R8).
  - `pending` → look in `Jobs().Versions(appID)` for a version whose `caravana.deployment` is
    this ID. Found: record `running` + that version, then watch. Not found: `failed`, "Caravana
    stopped before registering this deployment".
  - Nomad unreachable → the same backoff as R8 until it answers or shutdown.
- **Shutdown**: `main` cancels the service's context after the HTTP server has shut down, then
  `Service.Wait()` waits for the watchers to return. Records stay `running` and are resumed
  next time.

## R11. Deploys outlive their request

- **Decision**: the deploy work after validation runs with
  `context.WithoutCancel(r.Context())`, and its 25 s budget **starts once the deploy holds its
  app's lock** (R9). Waiting for the lock is bounded too: at most one earlier deploy's work
  (≤ 25 s), because queued older requests supersede themselves as soon as they get the lock.
  The deploy handler raises its own write deadline to 60 s with
  `http.NewResponseController(w).SetWriteDeadline`; the server-wide 30 s write timeout stays for
  every other route. A client that disconnects after Nomad accepted the job doesn't cancel the
  deploy, and its outcome is still recorded (spec Edge Cases). Watching uses the service's own
  long-lived context, never the request's.
- **Go idiom**: `context.WithoutCancel` keeps the request's values but drops its cancellation,
  much like not passing the request's `CancellationToken` to a background `Task` in C#.
  `http.ResponseController` changes one request's deadlines, like a per-endpoint timeout in
  ASP.NET.

## R12. Testing without a network: fakes as `httptest` servers, no interfaces

- **Decision**: the registry and Nomad fakes are real HTTP servers (`httptest.Server`) in
  `internal/registry/registrytest` and `internal/deploy/deploytest`. The real
  `registry.Client` and the real `*api.Client` talk to them.
  - `registrytest`: serves manifests by repo and tag (index or single manifest), the token
    endpoint and `WWW-Authenticate` challenge, and scripted 401/404/429/500 answers, with a
    hook to hold a request (for the concurrency tests).
  - `deploytest`: serves `GET /v1/job/{id}`, `PUT /v1/jobs`, `GET /v1/job/{id}/versions`
    and `GET /v1/job/{id}/deployments`, keeps registered jobs, and replays a scripted list of
    deployment states per job version (successful, failed, failed with rollback, cancelled by
    a newer version, unreachable).
- **Rationale**: no interface is needed (constitution VIII: interfaces only when needed), and
  the tests exercise the real request building, error decoding and blocking-query handling,
  which a Go interface fake would skip.
- **Lab tests**: `cmd/caravana/lab_test.go` runs the whole binary in-process (`run(...)`)
  against the lab when `CARAVANA_LAB=1`, and skips otherwise, so CI never needs it (FR-024).
  An env var rather than a build tag, so `go vet` and `staticcheck` still check the file.

## R13. Settings: Nomad address and token

- **Decision**: `NOMAD_ADDR` / `-nomad-addr` (default `http://127.0.0.1:4646`, Nomad's own
  default) and `NOMAD_TOKEN` (env only; spec FR-021). The token is stored in `config.Secret`,
  a string type whose `String`, `GoString` and `LogValue` (slog) all return `[redacted]`, so
  printing the config by mistake can't leak it. `main` logs `nomadAddr`, never the token.
- **Client construction**: `deploy.NewNomadClient(addr, token)` builds the `*api.Client`, so
  `main` doesn't import Nomad's package (constitution VI). `api.NewClient` fills unset fields
  from `api.DefaultConfig()`, which also reads Nomad's other standard env vars (`NOMAD_CACERT`,
  `NOMAD_NAMESPACE`, `NOMAD_REGION`, …). We accept that: they're Nomad's own names, and Phase 3
  TLS needs them.
- **Startup doesn't need Nomad**: `api.NewClient` makes no network call, and resuming
  deployments (R10) runs in the background; health stays store-only (spec Edge Cases).
- **Address check**: must parse as an `http` or `https` URL with a host; otherwise startup
  fails naming `NOMAD_ADDR / -nomad-addr`.

## R14. New error codes and statuses

- **Decision**:

  | Code | Status | When |
  | --- | --- | --- |
  | `image_not_found` | 422 | the registry has no such repository or tag, or it's private (R4) |
  | `job_conflict` | 409 | a Nomad job with the app's ID isn't Caravana's, or changed during the deploy (R6, R7) |
  | `registry_unavailable` | 502 | the registry is unreachable, erroring or rate-limiting |
  | `nomad_unavailable` | 502 | Nomad is unreachable or refused the token |
  | `nomad_rejected` | 502 | Nomad refused the job; the message is Nomad's |

  Existing codes keep their meaning: `invalid_field` (400) for validation, a bad `limit` and a
  body/path ID mismatch; `not_found` (404) for an unknown app or deployment.
- **Why 502 for upstream failures**: Caravana is acting as a gateway to the registry and Nomad;
  `503 unavailable` stays reserved for Caravana's own store (feature 002).
- **Why 502 for Nomad rejecting a job**: the translator already enforces every rule it knows
  (constitution IV), so a rejection means Caravana and Nomad disagree. That's a server-side
  problem, not something the client can fix by changing a field. The message still says
  exactly what Nomad said.
- **`Location` on errors**: every error raised after the deployment record exists carries
  `Location: /v1/apps/{id}/deployments/{deploymentId}` (FR-022a). The error body shape doesn't
  change.

## R15. Blue/green rollouts with canaries (found by the lab test)

- **Finding**: the first full lab run showed a broken deploy taking whoami down for its whole
  10-minute rollout (1184 of 1193 requests failed). Feature 001's update strategy was a plain
  rolling update: with one replica, Nomad stopped the only healthy allocation as soon as it
  placed the new one. Nomad also doesn't fail a deployment at the 3-minute healthy deadline; it
  keeps rescheduling the unhealthy allocation until the 10-minute progress deadline.
- **Decision**: the translator now makes every deploy blue/green. Each task group gets
  `Update{Canary: Count}` (Nomad merges it with the job's update block) and the job's update
  block gets `AutoPromote: true`. Services with domains get `CanaryTags:
  ["caravana.canary=true"]`: no `traefik.enable` tag, so Traefik (which Caravana runs with
  `exposedByDefault=false`) never routes to an unpromoted canary. A first try with
  `traefik.enable=false` hid the canaries too, but the lab showed Traefik dropping the whole
  whoami router for about a second whenever the canaries changed, presumably because Nomad's
  merged service tags then held both `traefik.enable=true` and `=false`. Without the tag: 0 failed
  requests in four measured handovers. Once
  every canary is healthy, Nomad promotes them (their registrations switch to the normal tags)
  and stops the old allocations after their 5 s shutdown delay. A deploy that never gets
  healthy fails at the progress deadline, Nomad drops the canaries and reverts the job, and
  the old allocations serve throughout.
- **Traefik must follow Nomad's event stream** (`--providers.nomad.watch=true`, now in
  `deploy/dev/traefik.nomad.hcl`). With the default 15 s polling, Traefik kept routing to
  deregistered allocations for up to 15 s, longer than the 5 s `shutdown_delay`, which gave
  about 4 s of 502s at each promotion. Watch mode needs only the `read-job` policy Traefik
  already has. Feature 012's installer must set both `watch=true` and `exposedByDefault=false`.
- **Cost**: during a deploy each service briefly runs twice its replicas, so a node needs room
  for both versions at once. Acceptable on Phase 1's single node; feature 012's sizing advice
  should mention it.
- **Unchanged**: deployment descriptions (R8) and the statuses Caravana records; the golden
  files and `deploy/dev/whoami.json` change by exactly these three fields.
