# Implementation Plan: Minimal Auth

**Branch**: `006-minimal-auth` | **Date**: 2026-10-04 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from `specs/006-minimal-auth/spec.md`

## Summary

Close the API and give caravana its first pages. One admin is created on the host with
`caravana admin init` (the CLI opens the SQLite store itself, even while the server runs). The
admin signs in at `/login` and gets a server-side session (random 256-bit value, only its
SHA-256 stored, 12 h idle / 7 d absolute). Clients use named `cvat_` API tokens (SHA-256 stored,
optional expiry, list, revoke). Every `/v1` route except health and the webhook call needs a
token or a session; failures are a uniform 401 JSON. Cookie requests from another site are
refused with the standard library's `http.CrossOriginProtection`. Failed sign-ins are limited per
source address (5 per 15 minutes, IPv6 per /64, stored so they survive restarts), using a new
`CARAVANA_TRUSTED_PROXIES` setting to believe forwarded headers only from Traefik. Passwords use
PBKDF2-SHA256 from the standard library, so nothing in the design is a secret key and nothing
goes to Nomad Variables.

The pages (`/login`, `/account`) are `html/template` + embedded assets in a new `internal/ui`
package, styled by the Tailwind CSS v4 standalone CLI with daisyUI 5 and the handoff theme
(pinned by version and checksum, generated CSS committed, CI checks it), with self-hosted IBM
Plex fonts. One new module: `golang.org/x/term`, for the password prompt.

## Technical Context

**Language/Version**: Go 1.27 (go1.27.1)

**Primary Dependencies**: standard library (`crypto/pbkdf2`, `crypto/rand`, `crypto/sha256`,
`crypto/subtle`, `net/http` with `CrossOriginProtection`, `net/netip`, `html/template`,
`embed`); `golang.org/x/term` (**new**, R5; its dependency `golang.org/x/sys` is already in the
graph). Build-time only, not Go modules: the Tailwind CSS v4 standalone binary and daisyUI 5's
`.mjs` plugin files (R13), and IBM Plex `woff2` files (R14).

**Storage**: SQLite; migration `0003_auth.sql` (admin, sessions, api_tokens, signin_limits).
`store.OpenShared` for the CLI while the server holds the lock (R4).

**Testing**: `testing` + `httptest`; real store in `t.TempDir()`; `AuthOptions` with a fake
clock and a small PBKDF2 count; contract validation with kin-openapi plus a new check that every
route's `public` flag matches its `security`; a log scan for planted secrets; `TestLab` and
`TestGate` with tokens.

**Target Platform**: linux/amd64 and linux/arm64, CGO off; macOS + Lima lab for development.

**Project Type**: single Go binary (control plane) serving an API, pages and a CLI.

**Performance Goals**: a sign-in costs one PBKDF2 (about 0.2–0.5 s on a small arm64 VM); a
token or session check is one indexed SQLite read plus at most one write a minute. No effect on
deploy latency.

**Constraints**: no secret in logs, errors or later responses; same 401 for every reason; no
redirects on the API; no external requests from pages; pages work without JavaScript; `go build`
never needs Node or the style tool.

**Scale/Scope**: 1 migration, 4 new API operations (`createToken`, `listTokens`, `revokeToken`,
`changePassword`) plus auth on the 7 existing protected ones, 2 new error codes, 1 setting, 3 pages, 5 CLI
commands, 2 new packages (`internal/ui`, `internal/httpx`), 1 tools script.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | How this plan complies | Status |
| --- | --- | --- |
| I. Small by Default | No new runtime service; pages and CLI are in the one binary. One new module, `golang.org/x/term` (R5), justified in its commit. The style tool and fonts are build-time files, not runtime dependencies. | ✅ |
| II. One Model | `app.App` and the translator don't change. | ✅ |
| III. Safe, Reversible Deploys | Deploys don't change; they now need credentials. | ✅ |
| IV. Fail Loudly | Bad input names its field (`email`, `newPassword`, `name`, `expiresAt`); a bad `CARAVANA_TRUSTED_PROXIES` entry fails startup naming the setting. The uniform 401 hides only *why* auth failed. | ✅ |
| V. API-First | Everything the account page does exists in the API (`createToken`, `listTokens`, `revokeToken`, `changePassword`), through the same `service.Auth` calls the CLI uses. Sign-in and sign-out are sessions, which only browsers have. `api/openapi.yaml` changes with the routes; the API never redirects; JSON with `charset` still accepted. | ✅ |
| VI. Nomad-Native | No Nomad code. Auth never touches Nomad, so it keeps working when Nomad is down. | ✅ |
| VII. Secure by Default | Nothing logged that could authenticate; the design has no secret key, so nothing for Nomad Variables (R3); hashes only in the store; uniform 401; per-address limit; CSRF by Fetch Metadata; `__Host-`/`Secure`/`HttpOnly`/`SameSite=Lax` cookies; CSP and `frame-ancestors 'none'` on pages; no default password, admin created only on the host. | ✅ |
| VIII. Plain, Tested Go | Table tests, fake clock, no new interface (`Auth` is a concrete type), `ctx` first, errors wrapped. CI gains `tools/ui-css.sh check` and shellcheck over `tools/`. | ✅ |
| Tech: `html/template` + HTMX, `embed`, no SPA, no Node | Pages are `html/template` with embedded assets; no JavaScript at all in 006 (HTMX arrives with 007); the CSS tool is a standalone binary, not Node. | ✅ |
| Tech: dev tools pinned with the `tool` directive | The Tailwind CLI isn't a Go program, so it's pinned by exact version and SHA-256 in `tools/ui-css.versions` instead. | ⚠️ see Complexity Tracking |
| Development Workflow | 006 is the next Phase 2 item; the account page stays shell-less, the console is 007. Originality: the login page comes from our own Claude Design handoff. | ✅ |

**Post-design re-check**: still passes, with the one tracked deviation. Worth knowing:

- **No Nomad Variables in this feature.** VII puts the control plane's *secrets* there; this
  design has none (R3). Password, token and session hashes stay in SQLite, as webhook token
  hashes do.
- **Spec refinements during planning** (already applied to spec.md): FR-012 now names the
  browser headers that prove same-site origin (R7); FR-013 and FR-033 add the
  `changePassword` endpoint so the account page has no UI-only feature (V); FR-014 counts IPv6
  per /64 (R8); FR-036 says the owner creates the VM's admin once with `host.sh admin init`, and
  `prove` revokes its own token (R17).

## Project Structure

### Documentation (this feature)

```text
specs/006-minimal-auth/
├── plan.md
├── research.md            # R1–R17
├── data-model.md          # setting, migration, entities, state, request auth flow
├── quickstart.md
├── contracts/
│   ├── openapi.yaml       # replaces api/openapi.yaml (0.4.0)
│   ├── go-packages.md
│   ├── pages.md
│   └── cli.md
├── checklists/requirements.md
└── tasks.md               # /speckit-tasks
```

### Source Code (repository root)

```text
api/openapi.yaml                         # from contracts/openapi.yaml

cmd/caravana/
├── main.go                              # admin dispatch; top-level mux /v1/ + /; no-admin warning
├── admin.go                             # NEW: runAdmin (init, status, reset-password, create-token, revoke-token)
├── admin_test.go                        # NEW
├── main_test.go                         # + pages and API on one listener; webhook listener unchanged
├── gate_test.go                         # + APIToken, Bearer helper, api-needs-token step
└── lab_test.go                          # + admin init and create-token while running

internal/config/
├── config.go                            # + TrustedProxies
└── config_test.go

internal/httpx/                          # NEW
├── proxies.go                           # ParseProxies, ClientAddress (IPv6 /64), HTTPS
├── cookies.go                           # session cookie name, set, clear
├── logging.go                           # LogRequests (moved from internal/api)
└── *_test.go

internal/store/
├── migrations/0003_auth.sql             # NEW
├── store.go                             # + OpenShared, ErrSchemaMismatch
├── admin.go                             # NEW
├── sessions.go                          # NEW
├── apitokens.go                         # NEW
├── signinlimits.go                      # NEW
└── *_test.go

internal/service/
├── auth.go                              # NEW: Auth, NewAuth, AuthOptions, admin, passwords (PBKDF2)
├── sessions.go                          # NEW: SignIn, SignOut, CheckSession
├── signinlimit.go                       # NEW
├── apitokens.go                         # NEW
└── *_test.go

internal/api/
├── api.go                               # Deps, route.public, authenticate, CSRF on cookie requests
├── auth.go                              # NEW: authenticate, token and password handlers
├── errors.go                            # + cross_origin, token_name_taken
├── logging.go                           # removed (now httpx)
├── auth_test.go                         # NEW: every route 401 without credentials, 403 cross-site, Bearer beats cookie
├── contract_test.go                     # + public flag vs security
└── (existing tests)                     # send a token

internal/ui/                             # NEW
├── ui.go                                # New, routes, requireSession, security headers, static
├── login.go, account.go                 # handlers
├── generate.go                          # //go:generate ../../tools/ui-css.sh build
├── templates/{base,login,account,forbidden}.html
├── style/{input.css,theme.css}          # CSS sources (theme from the handoff, DESIGN.md fixes)
├── static/app.css                       # generated, committed
├── static/fonts/*.woff2, OFL.txt        # IBM Plex Sans + Mono 400/500/600
└── ui_test.go                           # pages, redirects, next, limit messages, no-JS forms, headers

tools/
├── ui-css.sh                            # NEW: build | check, downloads pinned files, verifies SHA-256
└── ui-css.versions                      # NEW: versions and checksums per platform

deploy/vm/host.sh                        # + admin command; prove: status check, gate token, revoke
.github/workflows/ci.yml                 # + tools/ui-css.sh check; shellcheck tools/*.sh
.gitignore                               # + /.cache/
CLAUDE.md                                # where we are, new setting, admin CLI, ui-css commands
```

**Structure Decision**: two new packages. `internal/ui` holds the pages and their assets, the
place 007 grows the console. `internal/httpx` holds the few HTTP helpers both the API and the
pages need (client address, cookies, request log), so neither imports the other. Auth logic
sits in `internal/service` as its own type, so the CLI can use it without Nomad.

## Implementation notes for /speckit-tasks

- **Order**: foundations (migration + store methods, `OpenShared`, `httpx`, config setting,
  error codes, `service.Auth` skeleton with PBKDF2) → US1 (CLI `init`/`status`/`reset-password`,
  `public` flag and `authenticate` on every route, uniform 401, existing API tests send a token,
  no-admin warning) → US2 (tokens: service, API, CLI `create-token`/`revoke-token`, contract) →
  US3 (sessions, the UI package and pipeline: `ui-css.sh`, fonts, theme, `/login`, `/logout`,
  `requireSession`, CSRF) → US4 (sign-in limit, messages) → US5 (`/account`, `changePassword`
  API and page) → US6 (design pass, keyboard, no-JS, no external requests, `check` in CI) →
  lab test and gate (`host.sh admin`, `prove`) → polish (CLAUDE.md, docs).
- **The contract grows per story**, as in 003 and 004: routes and `api/openapi.yaml` stay
  equal at every step, or the sync test fails. The global `security` and the 401s land with
  US1; token operations with US2; `changePassword` with US5.
- **Existing tests**: every API test that calls a protected route needs a token. Add one helper
  in `api_test.go` (create the admin and a token in the test store) rather than touching each
  test's requests by hand.
- **Design**: before writing the login and account templates, read DESIGN.md and
  `design/console/design/ui_kits/console/LoginScreen.jsx` (local only). Copy no sample data from
  the handoff into tracked files. After the pages work, run `/impeccable critique` or
  `/impeccable polish` on them once, then `/impeccable document` again so DESIGN.md describes the
  built CSS.
- **Pinned versions**: the task that writes `tools/ui-css.versions` looks up the latest Tailwind
  CSS v4 and daisyUI 5 releases and IBM Plex release, and records versions and SHA-256 sums for
  darwin-arm64, linux-x64 and linux-arm64.
- **Go idioms to explain**: `crypto/pbkdf2.Key` (C#: `Rfc2898DeriveBytes.Pbkdf2`);
  `http.CrossOriginProtection` (no C# equivalent; ASP.NET uses antiforgery tokens);
  `//go:embed` and `embed.FS` (C#: embedded resources); `//go:generate` (C#: a pre-build target
  you run by hand); `net/netip.Prefix` for CIDRs; `term.ReadPassword`; `html/template`'s
  automatic escaping (C#: Razor's `@` encoding).
- **Commit message notes**: one migration; one new module (`golang.org/x/term`, why); the
  Tailwind binary pinned by checksum instead of the `tool` directive.

## Complexity Tracking

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| Tailwind CSS standalone CLI pinned by version + SHA-256 in `tools/ui-css.versions`, not by the `tool` directive in `go.mod` | The maintainer chose Tailwind v4 + daisyUI 5 without Node (PRODUCT.md, Stack). The tool is a standalone binary, not a Go program, so `go.mod` can't pin it. It runs only to regenerate the committed CSS; `go build` never needs it, and CI's `check` keeps the committed CSS honest. | Hand-written CSS over the tokens: rejected by the maintainer. npm packages: need Node (constitution: no Node build step). A CSS CDN: external requests from the pages (FR-031). |
