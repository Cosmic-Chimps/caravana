# Research: Webhook Redeploy

Decisions for feature 004. Everything builds on feature 003's `service.Redeploy` and the
feature 002 plumbing; no new module.

## R1. Token format

- **Decision**: `cvwh_` followed by 32 random bytes from `crypto/rand`, base64url without padding
  (43 characters): 48 characters in all, URL-safe, so it can go in a query string unescaped.
- **Rationale**: 256 bits of entropy (FR-005) makes guessing infeasible, which is why Phase 1
  needs no rate limit. The prefix lets secret scanners (GitHub's push protection, gitleaks
  rules) recognize a leaked token, and makes a pasted token obviously Caravana's.
- **Alternatives**: UUIDs (122 bits, no prefix); hex (64 characters, longer for the same
  entropy).

## R2. Storing and checking tokens

- **Decision**: store `hex(SHA-256(token))` with the app ID and creation time. To check a
  presented token: hash it, load the app's stored hash by app ID, and compare the two 32-byte
  hashes with `crypto/subtle.ConstantTimeCompare`. When the app doesn't exist or has no token,
  compare against a fixed dummy hash, so every 401 path does the same work (FR-007, FR-010).
- **Rationale**: the token is long and random, so a fast hash is as safe as a slow one
  (there's no dictionary to try); SHA-256 is in the standard library. Looking up by app ID,
  not by hash, means no database index timing depends on the token. The stored hash can't be
  sent back to call the webhook (SC-005): the server hashes whatever it receives.
- **Not a secret under constitution VII**: the hash is useless to an attacker, so it lives in
  SQLite next to the app, not in Nomad Variables.
- **Alternatives**: bcrypt/argon2 (made for low-entropy passwords; adds cost and, for argon2, a
  module); HMAC with a server key (needs a key stored somewhere, gains nothing here).

## R3. Store: one migration, one table

- **Decision**: `0002_webhook_tokens.sql` creates `webhook_tokens(app_id TEXT PRIMARY KEY
  REFERENCES apps(id), token_hash TEXT NOT NULL, created_at TEXT NOT NULL) STRICT`. The primary
  key makes "at most one token per app" (FR-002) a database rule; creating a token is an upsert
  that replaces the old hash in one statement, so the old token stops working at once.
- **Store methods**: `SetWebhookToken`, `GetWebhookToken`, `DeleteWebhookToken`, and
  `LatestHealthyDeployment` (for R6), all `ctx` first and returning `ErrNotFound` like the rest.
- **Alternatives**: a column on `apps` (mixes a credential into the app document's row and its
  idempotent-save rule); several tokens per app (not needed yet).

## R4. Endpoints

- **Decision**:

  | Method and path | What | Answer |
  | --- | --- | --- |
  | `POST /v1/apps/{id}/webhook` | create or rotate the token | 201 `{token, path, createdAt}` |
  | `GET /v1/apps/{id}/webhook` | status | 200 `{enabled, createdAt?}` |
  | `DELETE /v1/apps/{id}/webhook` | turn it off | 204 (also when there was none) |
  | `POST /v1/webhooks/{id}` | the webhook | 202 / 200 `{changed, deployment}`, or an error |

  `path` is `/v1/webhooks/{id}?token=<token>`, ready to append to the public base URL.
- **Why a separate `/v1/webhooks/` prefix**: it's the only thing the webhook-only listener
  serves (R7), so "public" and "private" routes are told apart by prefix, which also makes a
  proxy rule in feature 005 trivial.
- **Response**: `WebhookResult{changed, deployment}` for both outcomes: 202 with `Location` when
  it deployed, 200 with the live deployment when unchanged (R6). The `Location` points at the
  main API (`/v1/apps/{id}/deployments/{deploymentId}`), which isn't served on the webhook-only
  listener; that's intentional and documented.
- **Rotation is `POST` again**, not a separate verb: one call, and the old token is gone.

## R5. Reading the token from a request

- **Decision**: from the `token` query parameter or `Authorization: Bearer <token>`. Both present
  with different values → 401; equal → accepted; neither → 401. The header scheme name is
  case-insensitive (`bearer` works).
- **Logging**: nothing to add. Feature 002's request log never reads the query string or
  headers (002 R10), which the "token never logged" test (SC-004) checks again with a planted
  token in all three places.
- **Body**: limited to 1 MiB with `http.MaxBytesReader` and drained to `io.Discard`, whatever the
  `Content-Type` (FR-011). It's read only after the token is accepted, so an unauthorized
  caller can't make Caravana read a body at all.
- **401 answer**: the standard error body with the new code `unauthorized`, message "missing or
  invalid webhook token", and `WWW-Authenticate: Bearer` (RFC 6750).

## R6. Skipping unchanged pushes

- **Decision**: `Service.Webhook` first resolves every image (`Deployer.Resolve`, outside the app
  lock). It skips when all of these hold: the app has a latest `healthy` deployment; the stored
  app's `UpdatedAt` isn't after that deployment's `CreatedAt`; and every resolved digest equals
  that deployment's. Then it returns `{changed: false, deployment: <that healthy one>}` with
  no record and no Nomad call. Otherwise it calls `Redeploy(ctx, appID, app.TriggerWebhook)`.
- **If the pre-check resolution fails**, the webhook doesn't answer from it: it falls through to
  `Redeploy`, which resolves again and records the failure exactly as feature 003 does (422 or
  502 with `Location`, a `failed` record with trigger `webhook`).
- **Two resolutions per changed push**: the pre-check, then `Redeploy`'s own under the app lock
  (which keeps 003's ordering guarantees). The cost is one extra `HEAD` per image, which Docker
  Hub doesn't count against pull limits. If the tag moves between the two, the deploy pins the
  newer digest, which is what the push asked for.
- **Comparing with "latest healthy"** is comparing with what Nomad runs: after a failed or
  reverted deploy, Nomad runs the last healthy version. `UpdatedAt` catches a stored app edited
  since (e.g. the reverted definition), which must redeploy even with the same digests.
- **Alternatives**: always deploy (rejected in clarify); record skips as an `unchanged`
  deployment (adds a status to 002's store and 003's contract).

## R7. The webhook-only listener

- **Decision**: setting `CARAVANA_WEBHOOK_LISTEN` / `-webhook-listen`, empty (off) by default.
  When set, `main` starts a second `http.Server` whose handler is `api.NewWebhook(svc, log)`:
  the same JSON-only 404/405 handling, request logging and error mapping as the main API, but
  with one route, `POST /v1/webhooks/{id}`. Both servers shut down together with the same
  timeout.
- **Validation**: host:port like `Listen`; startup fails naming the setting if it's invalid or
  equal to `Listen`. No "non-loopback" warning for it: being reachable is its purpose, and it
  can only redeploy apps for callers holding a token.
- **Rationale**: "only the webhook is public" becomes true by construction, not by a proxy rule
  someone can get wrong; feature 005 just exposes this port (through Traefik, or directly).
- **Timeouts**: the same as the main server's, with the webhook handler raising its own write
  deadline to 60 s as 003's deploy handler does.

## R8. Where the logic lives

- **Decision**: token generation, hashing, checking and the skip rule live in
  `internal/service/webhook.go` (business rules the UI and API share, constitution V). The API
  layer only reads the token from the request and maps errors. The store only stores hashes.
- **No new package**: the token code is a few functions; a `webhook` package would hold almost
  nothing.

## R9. Testing

- **Offline**: feature 003's `registrytest` and `deploytest` fakes. Service tests cover token
  create/rotate/revoke, every 401 path, skip vs deploy (including "stored app edited since the
  healthy one" and "no healthy deployment"), webhook trigger on records, and a webhook deploy
  ending `failed` with `Location`. API tests cover both listeners, the body and content-type
  rules, the header/query rules, and the log test with a planted token. A timing test is not
  practical; the constant-time property is covered by using `subtle.ConstantTimeCompare` on
  fixed-length hashes, checked by code review and a test that a wrong token of a different
  length still takes the same comparison path.
- **Lab** (FR-016): `TestLab` gains a webhook phase through a webhook-only listener: create the
  token right after the first API deploy, call the webhook before that rollout is healthy (202,
  the API's deployment ends `superseded`, the webhook's `healthy`), then call it again (200
  unchanged, no new job version), and check that health answers 404 on that listener.

## R10. New error code

- **Decision**: `unauthorized` (401), added to the contract's code list. Everything else reuses
  feature 003's codes: 404 for token endpoints on unknown apps, 413 for big bodies, and 003's
  422/409/502 for failed webhook deploys.
