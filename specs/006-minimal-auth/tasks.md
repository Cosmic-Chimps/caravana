---

description: "Task list for feature 006: minimal auth"
---

# Tasks: Minimal Auth

**Input**: Design documents from `specs/006-minimal-auth/`

**Prerequisites**: [plan.md](plan.md), [spec.md](spec.md), [research.md](research.md),
[data-model.md](data-model.md), [contracts/](contracts/), [quickstart.md](quickstart.md)

**Tests**: Required (constitution VIII, spec FR-035). In each story, write the tests first and
watch them fail.

**Organization**: US1 = the owner claims the instance on the host and the API closes. US2 = API
tokens. US3 = signing in on the web (and the page pipeline). US4 = failed sign-ins are limited.
US5 = the account page and password change. US6 = the sign-in page looks and reads like
caravana.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: can run in parallel (different files, no dependency on an unfinished task)
- **[Story]**: US1–US6 from spec.md

## Conventions for every task

- Every function doing I/O takes `ctx context.Context` first. No `panic` in library code.
  Errors are wrapped with context (`fmt.Errorf("create api token %q: %w", name, err)`).
- **No secret ever leaves where it belongs**: passwords, `cvat_` tokens, `cvwh_` tokens and
  session values never appear in logs, error messages, test failure messages (print a prefix at
  most) or any response except the one that creates a token.
- Times are UTC RFC 3339 in the store; every time-dependent rule takes its clock from
  `AuthOptions.Now`, so tests use a fake clock. Tests use `AuthOptions{PasswordIterations: 1000}`.
- Routes and `api/openapi.yaml` stay equal after every task (the contract-sync test). Copy each
  piece from `specs/006-minimal-auth/contracts/openapi.yaml` when its story lands; the file is
  complete only after US5.
- Existing helpers: `openTestStore` (store), `newEnv`/`discardLogger` (service),
  `newTestEnv`/`readError`/`syncBuffer` (api), `freeAddr`/`noEnv` (cmd/caravana).
- Never copy sample data (names, emails, domains, orgs) from the local design handoff in
  `design/console/` into tracked files.
- Keep `gofmt -l .` empty after every task; feature 001's goldens must not change.

---

## Phase 1: Setup

- [X] T001 Create `internal/store/migrations/0003_auth.sql` exactly as in data-model.md
  ("Migration `0003_auth.sql`"), with its comments. Update the expected migration count in
  `internal/store/migrate_test.go` to 3 and check `go test ./internal/store` passes.
- [X] T002 [P] Add `/.cache/` to `.gitignore` (the style tool's download cache, research R13).

---

## Phase 2: Foundational (blocks every story)

- [X] T003 [P] Store admin methods in new `internal/store/admin.go` (contracts/go-packages.md):
  `AdminRecord`, `CreateAdmin` (`ErrAlreadyExists` when the row exists), `GetAdmin`
  (`ErrNotFound`), `SetPassword` (`ErrNotFound` without an admin). Table tests in new
  `internal/store/admin_test.go`: create/get round trip, second create is `ErrAlreadyExists`,
  set password changes hash and `PasswordChangedAt`, set without admin.
- [X] T004 [P] Store session methods in new `internal/store/sessions.go`: `SessionRecord`,
  `CreateSession`, `GetSession` (`ErrNotFound`), `TouchSession`, `DeleteSession` (nil when gone),
  `DeleteSessionsExcept` ("" deletes all), `DeleteExpiredSessions(now, idle)` (deletes rows past
  `expires_at` or past `last_seen_at + idle`). Tests in new `internal/store/sessions_test.go`.
- [X] T005 [P] Store API token methods in new `internal/store/apitokens.go`: `APITokenRecord`,
  `CreateAPIToken(ctx, r, now)` (check that no *active* token at `now` has `r.Name`, then insert,
  in one transaction; `ErrAlreadyExists` otherwise), `GetAPITokenByHash` (`ErrNotFound`),
  `ListAPITokens` (newest first, `created_at DESC, rowid DESC`), `TouchAPIToken`,
  `RevokeAPIToken` (`ErrNotFound` for an unknown ID; no change when already revoked),
  `ActiveAPITokenByName`. Tests in new `internal/store/apitokens_test.go`: the name rule against
  active, expired and revoked tokens; hash uniqueness; list order; revoke twice.
- [X] T006 [P] Store sign-in limit methods in new `internal/store/signinlimits.go`:
  `SigninLimitRecord`, `GetSigninLimit` (`ErrNotFound`), `PutSigninLimit(ctx, r, pruneBefore)`
  (upsert, then delete rows with `window_start < pruneBefore`), `DeleteSigninLimit`. Tests in new
  `internal/store/signinlimits_test.go`, including pruning.
- [X] T007 [P] `OpenShared(ctx, dataDir)` and `ErrSchemaMismatch` in `internal/store/store.go`
  (research R4): open the database without taking the lock and without migrating; fail with
  `ErrSchemaMismatch` (naming both versions) unless the applied migrations are exactly the
  embedded ones; refuse a data directory with no database. Tests in
  `internal/store/migrate_test.go`: shared open while another `Store` holds the lock, both write
  and read each other's rows; mismatch when the database is one migration behind; no database.
- [X] T008 [P] New package `internal/httpx`, file `proxies.go` (research R9): `Proxies`,
  `ParseProxies` (comma-separated IPs or CIDRs via `net/netip`; a bare IP becomes a /32 or /128;
  an error names the bad entry), `ClientAddress` (peer, or the right-most untrusted
  `X-Forwarded-For` entry when the peer is trusted; IPv6 reduced to its /64 prefix string, IPv4
  kept whole), `HTTPS` (`r.TLS != nil`, or `X-Forwarded-Proto: https` from a trusted peer).
  Table tests in `internal/httpx/proxies_test.go`: untrusted peer with spoofed headers, trusted
  peer with a chain, all-trusted chain, IPv6 /64 grouping, malformed headers.
- [X] T009 [P] `internal/httpx/cookies.go` (research R6): `SessionCookie`, `SetSessionCookie`,
  `ClearSessionCookie`. Name `__Host-caravana_session` with `Secure` when HTTPS, else
  `caravana_session`; always `Path=/`, `HttpOnly`, `SameSite=Lax`; `Expires` = the absolute
  expiry; clearing sets `Max-Age=0`. Tests in `internal/httpx/cookies_test.go`.
- [X] T010 Move the request logger from `internal/api/logging.go` to
  `internal/httpx/logging.go` as `LogRequests(log, next)` (same fields: method, path, status,
  durationMs; still never the query, headers or cookies) with its `statusRecorder`. `api.handler`
  calls `httpx.LogRequests`; behavior and `internal/api` tests unchanged. Move the logger's own
  test to `internal/httpx/logging_test.go`.
- [X] T011 [P] `TrustedProxies` setting in `internal/config/config.go`:
  `CARAVANA_TRUSTED_PROXIES` / `-trusted-proxies`, default empty, parsed with
  `httpx.ParseProxies`; a bad entry fails `Load` with an error naming
  `CARAVANA_TRUSTED_PROXIES / -trusted-proxies`. Cases in `internal/config/config_test.go`:
  default, env, flag wins, bad entry.
- [X] T012 [P] Add `codeCrossOrigin = "cross_origin"` and `codeTokenNameTaken =
  "token_name_taken"` to `internal/api/errors.go`.
- [X] T013 `service.Auth` skeleton in new `internal/service/auth.go` (contracts/go-packages.md,
  research R2): `Auth`, `AuthOptions`, `NewAuth`, the error values (`ErrNoAdmin`,
  `ErrAdminExists`, `ErrBadCredentials`, `ErrSigninLimited`, `ErrWrongPassword`,
  `ErrNotAuthenticated`, `ErrTokenNameTaken`), the `SigninLimitError` and `SigninFailure` types,
  and the password helpers: `hashPassword` (PBKDF2-HMAC-SHA256 from `crypto/pbkdf2`, 16-byte
  salt, 32-byte key, `pbkdf2-sha256$<iterations>$<salt>$<key>` base64 raw std), `checkPassword`
  (reads the iteration count from the stored string; `subtle.ConstantTimeCompare`; false for a
  malformed hash), `checkPasswordRule` (≥ 12 characters, `*app.FieldError` naming the field),
  `normalizeEmail` (trim, lower case, exactly one `@` with text on both sides, else a field
  error). Table tests in new `internal/service/auth_test.go`.
- [X] T014 API token core in new `internal/service/apitokens.go` (research R10):
  `newAPIToken` (`cvat_` + base64url-no-padding of 32 random bytes), the token ID
  (`strings.ToLower(rand.Text())`), name and expiry validation (`*app.FieldError` for `name` and
  `expiresAt`), `CreateAPIToken` (`ErrNoAdmin` without an admin; `ErrTokenNameTaken` from the
  store's `ErrAlreadyExists`; logs `token_created` with the name), and `CheckAPIToken`
  (`ErrNotAuthenticated` without the `cvat_` prefix, before any store lookup; for an unknown
  hash, a revoked or expired token, or no admin; on success `TouchAPIToken` at most once a
  minute). Tests in new `internal/service/apitokens_test.go`: format
  `^cvat_[A-Za-z0-9_-]{43}$`, stored hash isn't the token, check accepts only an active token,
  a `cvwh_` value is refused, expiry with the fake clock, touch throttling.

**Checkpoint**: `go test ./...` passes; nothing visible to users yet.

---

## Phase 3: User Story 1 - The owner claims the instance from the host, and the API closes (Priority: P1) 🎯 MVP

**Goal**: `caravana admin init` creates the one admin (also while the server runs); every
protected API route refuses without a token with one uniform 401; health and the webhook call
keep working.

**Independent Test**: start on an empty store, see 401 on every protected route, run `init`, see
a second `init` refused, `reset-password` works.

### Tests for User Story 1

- [X] T015 [P] [US1] Service tests in `internal/service/auth_test.go`: `InitAdmin` (stores the
  normalized email and a hash, never the password), second `InitAdmin` is `ErrAdminExists`,
  weak password and bad email are field errors, `AdminEmail` (`ErrNoAdmin` before init),
  `ResetPassword` (old password fails, new works, every session row deleted, API tokens still
  check), `ResetPassword` without an admin is `ErrNoAdmin`.
- [X] T016 [P] [US1] API tests in new `internal/api/auth_test.go`: walk `(&server{}).routes()`,
  fill path parameters, and for every non-public route assert 401, code `unauthorized`,
  `WWW-Authenticate: Bearer`, no `Location`, both with no admin and with an admin but no token;
  `GET /v1/health` answers 200 without credentials; the webhook call still answers by its own
  token; `Authorization` values `Basic …`, `Bearer`, `Bearer  x`, a `cvwh_` token and a revoked
  `cvat_` token all get the same body; a valid token works on every route.
- [X] T017 [P] [US1] CLI tests in new `cmd/caravana/admin_test.go` (contracts/cli.md), password
  from a pipe: `init` exit 0, then exit 3 naming `reset-password`; `status` exit 3 then 0 with
  the email; `reset-password`; unknown command and missing `--email` exit 2; `init` while
  `run` holds the data directory (start `run` in a goroutine as `TestRun` does) succeeds and the
  running server then accepts that admin's token; the password never appears in stdout or
  stderr.

### Implementation for User Story 1

- [X] T018 [US1] `InitAdmin`, `AdminEmail`, `ResetPassword` in `internal/service/auth.go`,
  logging `admin_created` and `password_reset` events (no secrets). `ResetPassword` calls
  `DeleteSessionsExcept(ctx, "")`.
- [X] T019 [US1] API authentication (research R11) in new `internal/api/auth.go` and
  `internal/api/api.go`: `Deps` struct and `New(d Deps)`; `route` gains `public` (true for
  `/v1/health` and `/v1/webhooks/{id}` only); `handler` wraps non-public routes with
  `authenticate`, which accepts only `Authorization: Bearer <token>` checked by
  `Auth.CheckAPIToken` for now (the session branch comes in US3); `mapError` maps
  `service.ErrNotAuthenticated` to 401 `unauthorized` with `WWW-Authenticate: Bearer`.
- [X] T020 [US1] Test plumbing in `internal/api/api_test.go`: `newTestEnv` builds a
  `service.Auth` (fast hashing, fake-clock-ready), creates an admin and a token through it, and
  serves `New(Deps{…})` twice: `URL` behind a tiny wrapper that adds the Bearer header when the
  request has none (so existing tests keep calling `http.Get(e.URL+…)` unchanged), and `RawURL`
  without it, for auth tests. Expose `e.Auth` and `e.Token`.
- [X] T021 [US1] Contract, first slice, in `api/openapi.yaml` from
  `contracts/openapi.yaml`: version 0.4.0, the new description paragraphs, global `security`,
  the `apiToken` scheme, `security: []` on `getHealth`, the 401 on every protected operation,
  and the new `Unauthorized` description. In `internal/api/contract_test.go` add
  `TestContractSecurityMatchesRoutes`: a route is `public` exactly when its operation has an
  empty `security` override or its own non-API scheme (the webhook).
- [X] T022 [US1] The CLI in new `cmd/caravana/admin.go` (contracts/cli.md, research R4, R5,
  R16): `runAdmin(ctx, args, getenv, stdin, stdout, stderr) int` with `init`, `status`,
  `reset-password`; a flag set per command plus `-data-dir` (`CARAVANA_DATA_DIR`, default
  `data`); open the store with `store.Open`, and on `store.ErrLocked` with `store.OpenShared`;
  read passwords with `golang.org/x/term` `ReadPassword` (prompt twice, must match) when stdin is
  a terminal, else one line from stdin. Add the module with `go get golang.org/x/term` and keep
  `go mod tidy -diff` clean. In `cmd/caravana/main.go`, `run` dispatches `admin` to `runAdmin`
  (its signature gains `stdin io.Reader`, `stdout io.Writer`; update `main` and the tests).
- [X] T023 [US1] Server wiring in `cmd/caravana/main.go`: build `service.NewAuth(st, log,
  service.AuthOptions{})` and pass `api.Deps`; at startup log a warning naming
  `caravana admin init --email <email>` when `AdminEmail` is `ErrNoAdmin`; drop the old "the API
  has no authentication yet" warning. In `cmd/caravana/main_test.go`, `TestRun` checks health is
  200 and `GET /v1/apps/x` is 401 without a token.

**Checkpoint**: `go test -race ./...` passes. The API is closed; only a token created through
`service.Auth` (tests) opens it. Quickstart sections 2 and 3 work.

---

## Phase 4: User Story 2 - Clients use API tokens (Priority: P1)

**Goal**: create (API and CLI), list, revoke and expire API tokens.

**Independent Test**: create from the CLI and the API, call with it, list, revoke, call again
(401), let an expiring one pass its time (401, listed as expired).

### Tests for User Story 2

- [X] T024 [P] [US2] Service tests in `internal/service/apitokens_test.go`: `ListAPITokens`
  (states `active`/`expired`/`revoked` from the fake clock, newest first, never a value),
  `RevokeAPIToken` (then `CheckAPIToken` fails; unknown ID is `store.ErrNotFound`; twice is
  fine), `RevokeAPITokenByName`, a name reused after the first token expired or was revoked,
  `ErrTokenNameTaken` while active, `token_revoked` logged with the name.
- [X] T025 [P] [US2] API tests in new `internal/api/tokens_test.go`: `POST /v1/tokens` 201 with
  `Location`, body matching `CreatedToken` (use the existing schema check), token shown once
  (absent from `GET /v1/tokens`); 409 `token_name_taken` with `field: name`; 400 for a bad name,
  a past `expiresAt` and unknown fields; 415 for a non-JSON body; list order and states; `DELETE
  /v1/tokens/{tokenId}` 204, then 401 with that token, again 204, unknown 404; a request
  revoking its own token succeeds and the next is refused; the token never in the captured log.
- [X] T026 [P] [US2] CLI tests in `cmd/caravana/admin_test.go`: `create-token --name ci`
  prints only the token on stdout (matches the format) and its notice on stderr;
  `--expires-in 720h`; a bad duration exits 2; without an admin exits 3; `revoke-token --name
  ci` then the token is refused; revoking an unknown name exits 1.

### Implementation for User Story 2

- [X] T027 [US2] `ListAPITokens`, `RevokeAPIToken`, `RevokeAPITokenByName` and the
  `APITokenInfo` state rule in `internal/service/apitokens.go`.
- [X] T028 [US2] Token handlers in new `internal/api/tokens.go`: `createToken`, `listTokens`,
  `revokeToken`, using the existing JSON decoding (`charset` allowed, unknown fields rejected,
  1 MiB limit); routes in `internal/api/api.go`; map `service.ErrTokenNameTaken` to 409
  `token_name_taken` with `field: name`.
- [X] T029 [US2] Contract slice in `api/openapi.yaml`: `/v1/tokens`, `/v1/tokens/{tokenId}`,
  the `TokenID` parameter, the `CreateTokenRequest`, `TokenInfo`, `CreatedToken`, `TokenList`
  schemas, the `TokenNameTaken` response, the `tokens` tag and `token_name_taken` in the code
  list. (The 403s on these operations land with US3.)
- [X] T030 [US2] `create-token` and `revoke-token` in `cmd/caravana/admin.go`
  (contracts/cli.md).

**Checkpoint**: quickstart section 5 works from `curl` and the CLI.

---

## Phase 5: User Story 3 - The admin signs in on the web (Priority: P1)

**Goal**: `/login`, sessions, sign-out, redirects, CSRF, and the page pipeline 007 builds on.

**Independent Test**: HTTP tests against the pages: sign in, follow the redirect back, sign
out, expire a session with the fake clock, check cookie attributes and the cross-site refusal.

### Pipeline for User Story 3

- [X] T031 [US3] `tools/ui-css.sh` (`build` | `check`) and `tools/ui-css.versions` (research
  R13). Look up the latest Tailwind CSS v4 standalone release and the latest daisyUI 5 release;
  record exact versions and SHA-256 sums for the Tailwind binaries `darwin-arm64`, `linux-x64`,
  `linux-arm64` and for `daisyui.mjs` and `daisyui-theme.mjs`. The script picks the binary for
  the host, downloads into `.cache/ui-css/<version>/`, verifies every checksum before running
  anything (`shasum -a 256` on macOS, `sha256sum` on Linux), builds
  `internal/ui/style/input.css` → `internal/ui/static/app.css` with `--minify`, and for `check`
  builds to a temp file and diffs. `set -euo pipefail`, shellcheck-clean. In
  `.github/workflows/ci.yml` add a `tools/ui-css.sh check` step and extend shellcheck to
  `tools/*.sh`.
- [X] T032 [P] [US3] Fonts (research R14): IBM Plex Sans and IBM Plex Mono, 400/500/600, Latin
  `woff2`, from IBM's official Plex release (record the version in a comment at the top of
  `internal/ui/style/input.css`), into `internal/ui/static/fonts/`, with the SIL OFL text as
  `internal/ui/static/fonts/OFL.txt`.
- [X] T033 [US3] Style sources: `internal/ui/style/theme.css` adapted from the handoff's
  `design/console/theme/caravana.css` (`@plugin "./daisyui-theme.mjs"` paths for the standalone
  CLI; `.cv-label` at 12px; the live-dot animation without `infinite`; no sample data), and
  `internal/ui/style/input.css` (`@import "tailwindcss"`, `@plugin "./daisyui.mjs"`, `@source
  "../templates"`, the Plex `@font-face` rules with `font-display: swap`, the theme). New
  `internal/ui/generate.go` with `//go:generate ../../tools/ui-css.sh build`. Generate and commit
  `internal/ui/static/app.css`; `tools/ui-css.sh check` passes.

### Tests for User Story 3

- [X] T034 [P] [US3] Service tests in new `internal/service/sessions_test.go`: `SignIn` success
  returns a 43-character value whose SHA-256 is the stored key; wrong email and wrong password
  return the same error and both run one password hash (count calls with a test hook on the
  unexported hasher); `CheckSession` fails after 12 h idle and after 7 days absolute (fake
  clock) and touches `last_seen_at` at most once a minute; `SignOut` removes the row; expired
  rows are deleted on the next sign-in; `signin`, `signin_failed` and `signout` are logged with
  the address and no secret.
- [X] T035 [P] [US3] Page tests in new `internal/ui/ui_test.go` (contracts/pages.md): `GET
  /login` shows the form; with no admin, the host command and no form; `POST /login` success sets
  the cookie (attributes per T009; `__Host-` and `Secure` when a trusted proxy says HTTPS) and
  303s to `next` or `/account`; unsafe `next` values (`//evil.example`, `/\evil`,
  `https://evil.example`, `/v1/apps`) land on `/account`; wrong credentials show the same
  sentence for both cases with the email kept; `GET /account` without a session 303s to
  `/login?next=%2Faccount`; `POST /logout` ends the session (replaying the old cookie redirects
  to `/login`); a `POST` with `Sec-Fetch-Site: cross-site`, or an `Origin` of another host, gets
  the 403 page and changes nothing; every page has the security headers from research R12;
  `/static/app.css?v=…` is served with a long cache lifetime.
- [X] T036 [P] [US3] API tests in `internal/api/auth_test.go`: a session cookie authenticates
  `GET`s; a cookie `POST` with `Sec-Fetch-Site: cross-site` gets 403 `cross_origin` and changes
  nothing, with `same-origin` it works; a request with a bad Bearer token and a valid cookie gets
  401; Bearer requests never get 403 whatever their headers.

### Implementation for User Story 3

- [X] T037 [US3] `SignIn` (without the limit, which US4 adds), `SignOut`, `CheckSession` in new
  `internal/service/sessions.go` (research R6). A wrong email still runs `checkPassword` against
  a dummy hash made once with the configured iterations.
- [X] T038 [US3] New package `internal/ui` (research R12): `ui.go` (`Deps`, `New`, routes per
  contracts/pages.md, `requireSession`, safe `next` handling, security headers, `/static/` from
  `embed.FS` with a content-hash `?v=` and `Cache-Control`, `http.CrossOriginProtection` on
  every non-safe route with a 403 page), `login.go` (`GET`/`POST /login`, `POST /logout`),
  templates `templates/base.html`, `templates/login.html`, `templates/forbidden.html`, and a
  minimal `templates/account.html` (headline "Signed in as …" and the sign-out button; US5 fills
  it in). Pages use `httpx.ClientAddress`, `httpx.HTTPS` and the cookie helpers. No JavaScript.
- [X] T039 [US3] Session branch in `internal/api/auth.go`: no `Authorization` header and a
  valid session cookie authenticates; for non-safe methods, run
  `http.CrossOriginProtection.Check` and map a refusal to 403 `cross_origin`. Contract slice in
  `api/openapi.yaml`: the `session` scheme in components and global `security`, the
  `CrossOrigin` response, the 403 on every non-safe protected operation, `cross_origin` in the
  code list.
- [X] T040 [US3] One listener for API and pages in `cmd/caravana/main.go`: a top-level
  `http.ServeMux` with `/v1/` → `api.New(…)` and `/` → `ui.New(…)`, wrapped once in
  `httpx.LogRequests`; `api.New` stops wrapping itself (`api.NewWebhook` keeps its own logger).
  In `cmd/caravana/main_test.go`: `/login` answers on the main listener; on the webhook-only
  listener `/login` and `/static/app.css` are JSON 404s.
- [X] T041 [US3] The login page design (contracts/pages.md "`/login`", DESIGN.md): read
  `DESIGN.md` and the local `design/console/design/ui_kits/console/LoginScreen.jsx` first; build
  `templates/login.html` with the wordmark, the host line, "Sign in", labeled fields, the primary
  button and the reset-on-the-host note; error sentences with the status mark before the text and
  `role="alert"`. Rebuild `app.css` (`tools/ui-css.sh build`).

**Checkpoint**: quickstart section 4, steps 1 and 3, works in a browser; section 6 works.

---

## Phase 6: User Story 4 - Failed sign-ins are limited (Priority: P2)

**Goal**: 5 failures per 15 minutes per address (IPv6 per /64), then a 15-minute wait with the
password not checked; tries left shown; tokens and CLI unaffected.

**Independent Test**: fail repeatedly with the fake clock, read the messages, see even the right
password refused until the wait ends.

### Tests for User Story 4

- [X] T042 [P] [US4] Service tests in new `internal/service/signinlimit_test.go`: tries left
  4, 3, 2, 1 as `*SigninFailure`; the fifth failure locks (`*SigninLimitError` with
  `RetryAfter` 15 min); while locked the right password is refused and no password hash runs
  (the T034 hook); after the wait it works and the count starts over; a failure after 15 minutes
  of quiet starts a new window; success deletes the row; a new `Auth` on the same store still
  sees the lock (restart); another address is unaffected; `CheckAPIToken` works throughout;
  `signin_limited` logged with the address.
- [X] T043 [P] [US4] Page tests in `internal/ui/ui_test.go`: the sentences from
  contracts/pages.md for 4 tries, 1 try and locked (minutes rounded up), with the warning and
  failed marks; behind a trusted proxy two clients with different `X-Forwarded-For` addresses are
  limited separately; from an untrusted peer a spoofed `X-Forwarded-For` doesn't change the
  address counted.

### Implementation for User Story 4

- [X] T044 [US4] New `internal/service/signinlimit.go` (research R8, data-model.md state
  diagram) and its use in `SignIn` in `internal/service/sessions.go`: check the lock first and
  return before hashing; count a failure; delete on success; prune rows older than a day.
- [X] T045 [US4] Messages on `/login` in `internal/ui/login.go` and
  `templates/login.html` from `SigninFailure.TriesLeft` and `SigninLimitError.RetryAfter`.

**Checkpoint**: quickstart section 4, step 2, works.

---

## Phase 7: User Story 5 - The admin changes their password and manages tokens after signing in (Priority: P2)

**Goal**: the account page (change password, create/list/revoke tokens, sign out) and
`PUT /v1/account/password`.

**Independent Test**: change the password with a wrong and a right current password; other
sessions end, this one stays; create and revoke a token from the page.

### Tests for User Story 5

- [X] T046 [P] [US5] Service tests in `internal/service/auth_test.go`: `ChangePassword` with a
  wrong current password (`ErrWrongPassword`, nothing changes), a short new one (field error
  `newPassword`), success (old fails, new works, every session but `keepSession` deleted, tokens
  still check), `password_changed` logged.
- [X] T047 [P] [US5] API tests in new `internal/api/account_test.go`: `PUT
  /v1/account/password` 204; 400 `invalid_field` with `field: currentPassword` and with `field:
  newPassword`; 401 without credentials; with a session cookie the calling session survives and
  another session ends; a cross-site cookie request gets 403.
- [X] T048 [P] [US5] Page tests in `internal/ui/ui_test.go`: password form success message and
  field errors; `POST /account/tokens` answers 200 with the new token shown once and a reload
  (`GET /account`) no longer shows it; the token list shows states with their marks; `POST
  /account/tokens/{id}/revoke` 303s and the row shows revoked; every form works as a plain form
  post (no JavaScript); cross-site posts get the 403 page.

### Implementation for User Story 5

- [X] T049 [US5] `ChangePassword` in `internal/service/auth.go`.
- [X] T050 [US5] `changePassword` handler in new `internal/api/account.go` (keep the caller's
  session when it came by cookie; map `ErrWrongPassword` to 400 `invalid_field`, `field:
  currentPassword`); route in `internal/api/api.go`; contract slice in `api/openapi.yaml`:
  `/v1/account/password`, `ChangePasswordRequest`, the `account` tag. `api/openapi.yaml` now
  equals `contracts/openapi.yaml`; check with `diff`.
- [X] T051 [US5] The account page in new `internal/ui/account.go` and
  `templates/account.html` (contracts/pages.md "`/account`"): headline, password section, token
  panel with rows and the create form, the shown-once token panel with the `curl` example, sign
  out. Rebuild `app.css`.

**Checkpoint**: quickstart sections 4 and 5 work from the page as well as from `curl`.

---

## Phase 8: User Story 6 - The sign-in page looks and reads like caravana (Priority: P3)

**Goal**: the pages match DESIGN.md, work by keyboard and without JavaScript, ask nothing of
other hosts, and the stylesheet is reproducible.

**Independent Test**: render at desktop and phone widths, tab through, load with JavaScript off,
record network requests; `tools/ui-css.sh check`.

- [X] T052 [P] [US6] Tests in `internal/ui/ui_test.go`: rendered pages and `app.css` contain no
  `http://` or `https://` reference to another host and no `<script>`; every `input` has a
  `label`; error blocks have `role="alert"`; `app.css` contains a `prefers-reduced-motion` rule
  and `tabular-nums`.
- [X] T053 [US6] Design pass on `/login` and `/account`: open both with the `cmux-browser`
  skill at a desktop and a phone width (the handoff has no phone layout: keep the 380px column
  and let the account page's rows wrap), tab through them, load them with JavaScript off, and
  check the network panel. Run `/impeccable critique` on the two templates once, fix everything
  it finds in one batch in `internal/ui/templates/*.html` and `internal/ui/style/*.css`, rebuild
  `app.css`, and confirm with one more look.
- [X] T054 [US6] Run `/impeccable document` so `DESIGN.md` and `.impeccable/design.json` describe
  the built CSS instead of only the handoff; keep its source comment updated.

---

## Phase 9: Lab test and VM gate (FR-036, SC-008)

- [X] T055 In `cmd/caravana/lab_test.go`: after `run` starts, call `runAdmin` in-process for
  `init` (password from a pipe) and `create-token --name lab` while the server holds the data
  directory; pass the token as `gateTarget.APIToken`.
- [X] T056 In `cmd/caravana/gate_test.go`: `gateTarget.APIToken`; one helper for every API
  call that sets `Authorization: Bearer` (replace the `http.Post`/`http.Get` calls to `API`);
  webhook calls unchanged; a new first step `api-needs-token` (a protected route without the
  token is 401, `GET /v1/health` is 200 without it); `TestGate` reads `GATE_API_TOKEN`.
- [X] T057 In `deploy/vm/host.sh` (contracts/cli.md, research R17): new `admin` command running
  `caravana admin "$@"` on the host over `ssh -t` as the `caravana` user with
  `/etc/caravana/caravana.env`; `prove` runs `admin status` first and stops with "create the
  admin first: deploy/vm/host.sh admin init --email <email>" when there is none, creates
  `gate-<run>`, passes `GATE_API_TOKEN`, and revokes it in a `trap` when the run ends. Update the
  usage text and `deploy/vm/README.md`. shellcheck-clean (via Docker on the Mac).
- [X] T058 Run `CARAVANA_LAB=1 go test ./cmd/caravana -run TestLab -v -timeout 20m` against the
  lab (`deploy/dev/lab.sh up` first) and record the result in the summary. `host.sh prove` needs
  a VM: run it if one exists, otherwise say so in the summary.

---

## Phase 10: Polish & Cross-Cutting Concerns

- [X] T059 [P] Secret-scan test in new `cmd/caravana/secrets_test.go` (SC-005): run the server
  in-process with captured logs, use a password, an API token, a webhook token and a session
  cookie across the CLI, the API and the pages (successful and failed), then assert none of the
  four values appears in the captured stderr or in any response except the token's creation.
- [X] T060 [P] `CLAUDE.md`: "Where we are" for 006 (branch, what exists), the new setting, the
  `caravana admin` commands, `tools/ui-css.sh build|check`, `deploy/vm/host.sh admin`, and the
  `/login` URL for local development.
- [X] T061 [P] `deploy/dev/README.md`: after `lab.sh up`, how to create the admin and a token
  for local work.
- [X] T062 Run every CI check locally: `gofmt -l .`, `go mod tidy -diff`, `go vet ./...`,
  `go tool staticcheck ./...`, `go test -race ./...`, `tools/ui-css.sh check`, shellcheck (via
  Docker) over `deploy/dev/*.sh deploy/vm/*.sh tools/*.sh`, and the `linux/amd64` and
  `linux/arm64` CGO-free builds.
- [X] T063 Walk quickstart.md sections 2–7 by hand and fix anything that differs.
- [X] T064 Write the summary for the maintainer: what changed per package, the spec refinements
  made during planning (FR-012, FR-013, FR-014, FR-033, FR-036), the one new module and the
  pinned tool, and the Go idioms introduced, one line each with the C# comparison:
  `crypto/pbkdf2.Key` (`Rfc2898DeriveBytes.Pbkdf2`), `http.CrossOriginProtection` (ASP.NET
  antiforgery tokens), `embed.FS` (embedded resources), `//go:generate` (a pre-build target run
  by hand), `net/netip.Prefix`, `term.ReadPassword`, `html/template` escaping (Razor's `@`).

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (T001–T002)** → **Foundational (T003–T014)**; T003–T007 need T001; T010 before T040;
  T013 before T014.
- **US1 (T015–T023)**: after Foundational. Every later story needs US1's `authenticate`, `Deps`
  and test plumbing (T019, T020).
- **US2 (T024–T030)**: after US1.
- **US3 (T031–T041)**: after US1. T031 → T033 (T032 alongside); T037 before T038; T038 before
  T039, T040, T041.
- **US4 (T042–T045)**: after US3 (it changes `SignIn` and the login page).
- **US5 (T046–T051)**: after US2 (token service) and US3 (sessions, pages).
- **US6 (T052–T054)**: after US5 (both pages exist).
- **Lab and gate (T055–T058)**: after US2 (tokens and CLI); T058 last in that phase.
- **Polish (T059–T064)**: last; T062 before T063 before T064.

### Within each story

Tests first and failing, then implementation, then the checkpoint. Contract slices land in the
same task as their routes (T021, T029, T039, T050).

### Parallel Opportunities

- Foundational: T003–T009 and T011–T012 are separate files; T010, T013, T014 follow.
- Each story's test tasks are in different packages and can be written together (T015–T017;
  T024–T026; T034–T036; T042–T043; T046–T048).
- US3's pipeline (T031–T033) can run alongside US2.
- Lab and gate (T055–T057) can run alongside US3–US6 once US2 is done.
- Polish T059–T061 together.

## Parallel Example: User Story 1

```text
Tests at once:       T015 (service) · T016 (api) · T017 (cmd/caravana)
Then:                T018 → T019 → T020 → T021 → T022 → T023
```

## Parallel Example: User Story 3

```text
Pipeline:            T031 → T033   (T032 alongside)
Tests at once:       T034 (service) · T035 (ui) · T036 (api)
Then:                T037 → T038 → T039 · T040 · T041
```

## Implementation Strategy

### MVP first (US1 + US2)

Phases 1–4: the API is closed and clients use tokens created on the host. That alone makes
caravana safe to expose behind Traefik for API clients, with no pages yet.

### Incremental delivery

1. US1: the owner creates the admin; the API is closed.
2. US2: tokens for CI, scripts and the Aspire integration.
3. US3: signing in on the web, and the page pipeline 007 builds on.
4. US4: the sign-in page can face the internet.
5. US5: password and tokens without SSH.
6. US6: the pages match the design system; DESIGN.md records the built CSS.
7. Lab and gate, then polish.

## Notes

- One migration (`0003`), one new module (`golang.org/x/term`): say so in the commit message,
  with why. The Tailwind binary is pinned by checksum in `tools/ui-css.versions`, not in
  `go.mod` (plan, Complexity Tracking).
- Nothing in this feature goes into Nomad Variables (research R3).
- Commit when the maintainer asks.
