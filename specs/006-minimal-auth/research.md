# Research: Minimal Auth

Decisions for feature 006, each with why and what else was considered. R-numbers are cited
from the plan, the data model and the contracts.

## R1. Where the auth logic lives

**Decision**: a new `service.Auth` type in `internal/service` (files `auth.go`, `sessions.go`,
`apitokens.go`, `signinlimit.go`). It is built from the store alone (`service.NewAuth`), with
no deployer and no Nomad client, so the API, the web pages and the host CLI all use the same
calls.

**Why**: constitution V wants one service layer for every front door, and the spec says so
(FR-034). `service.Service` needs a deployer and a Nomad client; the CLI must work when Nomad
is down, so auth can't hang off `Service`.

**Alternatives**: a new `internal/auth` package (another package for the same layer the
constitution names; the API and UI would import two service packages); methods on `Service`
(the CLI would have to build a deployer it never uses).

## R2. Password hashing

**Decision**: PBKDF2-HMAC-SHA256 from the standard library (`crypto/pbkdf2`, Go 1.24+), 600,000
iterations, 16-byte random salt, 32-byte key. Stored as one string,
`pbkdf2-sha256$600000$<salt>$<key>` (base64 without padding). Verification reads the iteration
count from the stored string, so it can be raised later without breaking old hashes. Keys are
compared with `subtle.ConstantTimeCompare`. `AuthOptions.PasswordIterations` lets tests use a
small count; production uses the default.

**Why**: no new module (constitution I). 600,000 is OWASP's current figure for PBKDF2-SHA256.
It costs roughly 0.2–0.5 s of one CPU core on a small arm64 VM, which is acceptable for one
admin and is bounded by the sign-in limit (R8).

**Alternatives**: argon2id or bcrypt from `golang.org/x/crypto` (a new module; argon2id's
memory cost, e.g. 64 MB per attempt, is itself a denial-of-service lever on a 2 GB VM); scrypt
(same module, same concern).

## R3. No control-plane secret, so nothing in Nomad Variables

**Decision**: the design needs no secret key at all, so nothing goes into Nomad Variables:

- Sessions are random 256-bit values kept server-side. The store holds their SHA-256, never
  the value (R6). There is no signed cookie, so no signing key.
- CSRF protection uses the browser's own Fetch Metadata and `Origin` headers (R7), not tokens.
- API tokens are stored as SHA-256 hashes (R10), as webhook tokens are (feature 004, R2).
- The password is stored as a slow salted hash (R2).

**Why**: constitution VII puts the control plane's *secrets* in Nomad Variables. A hash can't be
used to sign in or call the API, so it isn't one (the same reasoning as feature 004). Keeping
auth in SQLite also means signing in and the CLI never depend on Nomad being up, which matches
VII's cold-start rule in spirit. The data directory is already `0700`, owned by the `caravana`
user on the VM.

**Alternatives**: a session-signing key in Nomad Variables (adds a Nomad round trip to startup
and makes the UI fail when Nomad is down, for no gain over server-side sessions); the password
hash in Nomad Variables (the CLI would need a Nomad token with write access to it).

## R4. The host CLI and a running server share the data directory

**Decision**: `caravana admin …` opens the store itself. It first tries the data directory's
exclusive lock, as the server does. If the lock is free (server stopped), it opens normally,
migrations included. If the lock is held (server running), it opens the database **without** the
lock and without migrating, and only if the database is already at the schema version this
binary knows; otherwise it stops with "stop caravana or run the same version". SQLite in WAL
mode with `busy_timeout` lets both processes write safely. The server reads auth state from the
store on every request, so it sees a new admin, a reset password or a new token at once
(spec edge case "admin init while caravana runs").

**Why**: no second protocol, no new listener, and the CLI works whether caravana is up or not.
The lock exists to stop two *servers* from running deploy watchers on one store; the CLI runs
none.

**Alternatives**: a Unix socket in the data directory that the server listens on for admin
commands (a second protocol and listener to secure and test); calling the HTTP API from the CLI
(needs credentials, which is what the CLI is creating); requiring the server to be stopped (a
worse experience, and the gate would have to restart caravana).

## R5. Reading the password on the command line

**Decision**: when standard input is a terminal, the CLI prompts twice without echo using
`golang.org/x/term` (`term.ReadPassword`). Otherwise it reads one line from standard input, for
scripts (`printf '%s\n' "$pw" | caravana admin init --email …`). There is no password flag
(spec edge case: flags show up in shell history and the process list).

**Why**: hiding typed characters needs terminal control. `x/term` is the Go team's small
package for exactly that; its only dependency, `golang.org/x/sys`, is already in the module
graph. Justified in the commit message (constitution I).

**Alternatives**: setting termios by hand with `golang.org/x/sys/unix` (clever code for one
call); running `stty -echo` (shells out, fragile); echoing the password (unacceptable on a
shared screen).

## R6. Sessions

**Decision**:

- Value: 32 random bytes, base64url, in a cookie. The store keeps its SHA-256 as the primary
  key, with `created_at`, `last_seen_at` and `expires_at` (sign-in + 7 days).
- A session is valid while `now < expires_at` and `now < last_seen_at + 12h` (FR-010).
  `last_seen_at` is written at most once a minute, so browsing doesn't write on every request.
- Cookie: `Path=/`, `HttpOnly`, `SameSite=Lax`, `Expires` = the absolute expiry. When the
  request came over HTTPS (R9) it is also `Secure` and named `__Host-caravana_session`;
  otherwise it is `caravana_session` (plain HTTP on loopback during development).
- A new session value on every sign-in; sign-out deletes the row and clears the cookie. A
  password change deletes every other session; a reset deletes all of them. Expired rows are
  deleted when someone signs in.

**Why**: server-side sessions can be ended instantly (sign-out, reset), which a signed
stateless cookie can't, and they need no key (R3). `Lax` keeps the admin signed in when they
follow a link to caravana from elsewhere, while R7 blocks cross-site writes. The `__Host-`
prefix stops another subdomain from planting the cookie.

**Alternatives**: signed or encrypted cookies (need a key, can't be revoked); `SameSite=Strict`
(the admin looks signed out after following any link from chat or email).

## R7. CSRF protection

**Decision**: `net/http.CrossOriginProtection` (standard library, Go 1.25+). It refuses a
non-safe request (`POST`, `PUT`, `DELETE`, …) when the browser marks it as cross-site:
`Sec-Fetch-Site` other than `same-origin` or `none`, or, for browsers without it, an `Origin`
whose host differs from `Host`. Requests with neither header aren't from a browser, can't be
CSRF, and pass. It applies to every page form (including the sign-in and sign-out forms) and
to API requests authenticated by the session cookie. It never applies to Bearer-token or
webhook requests. A refused API request gets 403 `cross_origin`; a refused page gets a 403
page.

**Spec refinement**: FR-012 said "carry proof that it came from caravana's own pages". The
proof is the browser's `Sec-Fetch-Site` / `Origin` header, which a page on another site can't
forge. The spec's FR-012 now says so.

**Why**: standard library, no tokens to thread through every form and HTMX request (007), no
secret (R3). Every browser caravana's users run has sent these headers since 2023. Together
with `SameSite=Lax` this is OWASP's recommended defense.

**Alternatives**: synchronizer tokens in every form (more code in every template and in 007's
HTMX requests, and no extra protection in current browsers); double-submit cookies (the same
plumbing, and weaker).

## R8. Limiting failed sign-ins

**Decision**: per source address (clarified 2026-10-04). One row per address: `failures`,
`window_start`, `locked_until`.

- A failure inside the 15-minute window increments `failures`; an older window starts over at
  1. At 5, `locked_until = now + 15 min`.
- While `now < locked_until`, a sign-in from that address is refused **before** the password is
  hashed: an attacker can't use the slow hash to burn CPU from one address.
- A success deletes the row. Rows older than a day are pruned on each write.
- IPv6 addresses count per `/64`, because one host can use a whole `/64`; IPv4 counts per
  address.
- The page shows `5 − failures` tries left, and when locked, the minutes until `locked_until`.
- The limit lives in the store, so it survives a restart (FR-016). It only touches `/login`:
  API tokens and the CLI never consult it (FR-015).

**Why**: per-address limiting can't be turned against the owner from somewhere else. The slow
hash (R2) already caps how fast one address could guess, so 5 tries per 15 minutes means
about 480 guesses a day per address, against a password of at least 12 characters.

**Alternatives**: per account (anyone on the internet can keep the admin locked out of the
web); both (more state; a large botnet can still lock the account); an in-memory limiter (resets
on restart, which FR-016 rules out).

## R9. Client address and HTTPS behind a proxy

**Decision**: a new setting, `CARAVANA_TRUSTED_PROXIES` / `-trusted-proxies`: a comma-separated
list of IPs or CIDRs, empty by default.

- If the connection's peer (`RemoteAddr`) is in the list, the client address is the right-most
  `X-Forwarded-For` entry that isn't a trusted proxy, and the request counts as HTTPS when
  `X-Forwarded-Proto` is `https`.
- Otherwise the client address is the peer and HTTPS means the connection itself is TLS
  (`r.TLS != nil`). Forwarded headers from anyone else are ignored (FR-014).

In 006 nothing is proxied: `host.sh` keeps the API and pages on loopback behind the SSH tunnel,
so the list stays empty. Feature 007, which publishes the console through Traefik, sets it to
Traefik's address.

**Why**: Traefik is the only proxy caravana runs behind, and trusting forwarded headers from
anywhere would let anyone pick their own address and dodge the limit.

**Alternatives**: always trusting `X-Forwarded-For` (spoofable); a public-URL setting to decide
`Secure` (answers HTTPS, not the client address); PROXY protocol (Traefik supports it, but it
needs a listener change and nothing else uses it).

## R10. API tokens

**Decision**:

- Token: `cvat_` + 32 random bytes in base64url without padding (48 characters), like
  `cvwh_` webhook tokens. The store keeps its hex SHA-256 in a unique column and looks it up
  by hash.
- ID: 26 lowercase base32 characters from `crypto/rand.Text`, like deployment IDs. It names the
  token in URLs (`DELETE /v1/tokens/{tokenId}`); it is not secret.
- Name: 1–64 characters, letters, digits, `.`, `_`, `-`, starting with a letter or digit;
  unique among *active* tokens, checked and inserted in one store transaction (409
  `token_name_taken`).
- Expiry: optional RFC 3339 time in the future (API, account page) or a Go duration (CLI
  `--expires-in 720h`).
- State is derived: `revoked` if `revoked_at` is set, else `expired` if `expires_at` has
  passed, else `active`. Revoking keeps the row so the list shows it.
- `last_used_at` is written at most once a minute per token.
- A `Bearer` value that doesn't start with `cvat_` (a webhook token, a typo) is refused without
  a store lookup, with the same 401 as any other failure (FR-024, FR-025).

**Why**: one random 256-bit value can't be guessed, so a fast hash and a direct lookup are safe;
the lookup key is the hash of what the caller sent, which tells a caller nothing about other
tokens. Same shape as webhook tokens, so the codebase has one pattern.

**Alternatives**: an ID-plus-secret token (`cvat_<id>_<secret>`) with a constant-time compare
(more format for no gain at 256 bits); JWTs (need a signing key, can't be revoked without a
store lookup anyway).

## R11. Routing pages and API in one listener

**Decision**: `cmd/caravana` mounts a top-level `http.ServeMux` with `/v1/` → the API handler
and `/` → the new `internal/ui` handler. The webhook-only listener is unchanged (`api.NewWebhook`
alone, FR-026).

In the API, each route in the table gets a `public` flag (true only for health and the webhook
call). Protected routes run `authenticate` first:

1. An `Authorization` header present → it must be a valid `cvat_` token; the session cookie is
   then ignored (spec edge case).
2. No header, a valid session cookie → allowed; non-safe methods also pass R7's check.
3. Otherwise → 401 `unauthorized` with `WWW-Authenticate: Bearer`.

The contract test grows a check that every route's `public` flag matches its `security` in
`api/openapi.yaml` (empty `security` means public).

The UI handler has its own `requireSession`: no valid session → `303` to
`/login?next=<path>`. `next` is used only if it is a path starting with `/` that isn't `//…`,
`/\…` or `/v1/…`; anything else lands on `/account` (FR-011).

**Why**: one listener for the console and its API is what 007 needs (HTMX calls the API with
the session cookie), and keeping API and pages in separate handlers keeps the API's JSON-only
rules (no redirects, JSON 404s) intact.

## R12. The pages

**Decision**: package `internal/ui`: `html/template` files and static assets embedded with
`embed`, three pages for now:

- `/login` (GET form, POST sign-in), shell-less, from the handoff's login screen cut to what
  006 offers (FR-029). When no admin exists it shows the host command instead of the form.
- `/account` (the signed-in landing page, clarified 2026-10-04): change password, API tokens
  (create, list, revoke) and sign out, laid out without the 007 shell.
- `/logout` (POST only).
- `/` redirects to `/account`; `/static/…` serves the embedded assets.

Forms are plain `POST` + `303` redirect (post/redirect/get), so everything works without
JavaScript (FR-030). The one exception is creating a token: the response renders the page with
the new token in it (200), because it is shown once and a redirect would lose it.

Every page response sets `Content-Security-Policy: default-src 'self'; frame-ancestors 'none';
form-action 'self'; base-uri 'none'`, `X-Content-Type-Options: nosniff` and
`Referrer-Policy: same-origin`. Static files are served under a content-hash query
(`/static/app.css?v=<sha>`) with a long cache lifetime.

No JavaScript ships in 006. HTMX arrives with 007, when there is something to update live.

**Why**: the smallest set of pages that delivers the spec and exercises the pipeline 007 builds
on (templates, embedded assets, styles, fonts, CSRF, sessions).

## R13. The stylesheet pipeline

**Decision**:

- Sources: `internal/ui/style/input.css` (`@import "tailwindcss"`, `@plugin "./daisyui.mjs"`,
  `@source` pointing at the templates, the IBM Plex `@font-face` rules) and
  `internal/ui/style/theme.css`, adapted from the handoff's `theme/caravana.css`, with the two
  fixes DESIGN.md records: `.cv-label` at 12px, and the live-dot ping once per event, not
  `infinite`.
- Tools: the Tailwind CSS v4 standalone binary and daisyUI 5's `daisyui.mjs` and
  `daisyui-theme.mjs`, at exact versions with SHA-256 sums listed in `tools/ui-css.versions`
  (one line per file and platform: darwin-arm64, linux-x64, linux-arm64).
- Script: `tools/ui-css.sh build|check` downloads the pinned files into `.cache/ui-css/`
  (gitignored), verifies every checksum before running anything, and builds
  `internal/ui/static/app.css` with `--minify`. `check` builds to a temporary file and fails if
  it differs from the committed one.
- `internal/ui/generate.go` has `//go:generate ../../tools/ui-css.sh build` (C#: like a
  pre-build target you run by hand; `go build` never runs it).
- CI runs `tools/ui-css.sh check` on ubuntu, and shellcheck now covers `tools/*.sh`.
- The exact versions are the latest Tailwind CSS v4 and daisyUI 5 releases at implementation
  time; the task that adds the script records them with their checksums.

**Why**: the maintainer chose Tailwind + daisyUI with no Node (PRODUCT.md, Stack). Committing
the generated CSS keeps `go build` free of the tool (FR-032), and the `check` step stops the
committed CSS from drifting from its sources.

**Constitution**: "dev tools are pinned with the `tool` directive in `go.mod`" can't apply to a
non-Go binary; pinning by version and checksum is the equivalent. Recorded in Complexity
Tracking.

**Alternatives**: hand-written CSS over the tokens (rejected by the maintainer at
`/impeccable init`); the npm packages (need Node); a CDN build (no external requests, FR-031).

## R14. Fonts

**Decision**: IBM Plex Sans and IBM Plex Mono, weights 400, 500 and 600, `woff2`, Latin subset,
taken from IBM's official Plex release at a pinned version, committed under
`internal/ui/static/fonts/` with the SIL Open Font License text. `@font-face` with
`font-display: swap`; live values use `font-variant-numeric: tabular-nums` (DESIGN.md, The
Tabular Rule). About six files, roughly 250 KB in the binary.

**Why**: DESIGN.md's type, served by caravana itself (FR-031). The OFL allows embedding and
redistribution with the license.

**Alternatives**: Google Fonts (an external request); system fonts (not the design).

## R15. Logging

**Decision**: the request log keeps logging only method, path, status and duration, never the
query string, headers or cookies. The logger moves from `internal/api` to a small
`internal/httpx` package and wraps the main listener's top-level mux once, so pages and API
are logged the same way; the webhook-only listener keeps its own. `service.Auth`
logs security events (FR-028) as structured lines: `event` (`signin`, `signin_failed`,
`signin_limited`, `signout`, `password_changed`, `password_reset`, `token_created`,
`token_revoked`), `address` where there is one, and `tokenName` for token events. A test plants
a password, an API token, a webhook token and a session value in requests and scans the
captured log for each (SC-005).

**Why**: never logging headers or queries in the first place is stronger than redacting them.
Security events give the owner a trail without any secret in it.

## R16. The CLI

**Decision**: `caravana admin <command>` dispatched from `run` when the first argument is
`admin`; anything else starts the server as today. Commands: `init`, `status`,
`reset-password`, `create-token`, `revoke-token` (contract: `contracts/cli.md`). They share the
server's `-data-dir` / `CARAVANA_DATA_DIR` setting. Exit codes: 0 done, 1 failed, 2 bad usage,
3 the state doesn't allow it (`init` with an admin, `status` with none). `create-token` prints
only the token on standard output, so scripts can capture it.

On the VM, `deploy/vm/host.sh admin <command> …` runs it over SSH as the `caravana` user with the
service's environment, with a terminal for the password prompt.

**Why**: the owner proves ownership with shell access (spec), and scripts (the gate, CI setup)
need a non-interactive way to get a token.

## R17. The lab test and the VM gate

**Decision**:

- `TestLab` starts caravana in-process as today, then runs `admin init` (password from a
  pipe) and `admin create-token` in-process **while the server runs**, which exercises R4's
  shared open. The gate target gets an `APIToken`.
- Every gate API call goes through one helper that sets `Authorization: Bearer`. A new first
  step, `api-needs-token`, checks a protected route answers 401 without the token and that
  `GET /v1/health` doesn't need one. Webhook calls keep using only the webhook token.
- `host.sh prove` requires an admin (`admin status`; if none, it stops and says to run
  `deploy/vm/host.sh admin init`), creates a token named `gate-<run>`, passes it as
  `GATE_API_TOKEN`, and revokes it when the run ends, pass or fail.

**Why**: FR-036 and SC-008. Creating the owner's admin with a password nobody knows would be
wrong, so `prove` asks the owner to do it once.
