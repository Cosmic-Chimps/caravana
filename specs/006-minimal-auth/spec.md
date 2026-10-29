# Feature Specification: Minimal Auth

**Feature Branch**: `006-minimal-auth`

**Created**: 2026-10-04

**Status**: Implemented, v0.1.0

**Input**: User description: "006 Minimal auth: one admin login for the web UI, and API tokens
for clients, so caravana can be exposed publicly." Full description: one admin created on the
host with `caravana admin init` (no default password, no claim-on-first-visit page, CLI reset);
email and password sign-in at `/login` with a session cookie, sign-out, expiry and password
change; limited failed sign-ins with tries left shown; named API tokens (`cvat_`, shown once,
stored hashed, optional expiry, list and revoke) sent as `Authorization: Bearer`; every `/v1`
route protected except health and the webhook call; 401 JSON for the API and a redirect to
`/login` for pages; CSRF protection for cookie requests; no secrets in logs; `api/openapi.yaml`
updated in the same commit; TestLab and the VM gate keep passing. The login page is the first UI
page, built from the Claude Design handoff's login screen (no SSO, 2-step or email reset), and
it sets up the UI pipeline 007 builds on: templates, the Tailwind CSS v4 standalone CLI with
daisyUI 5 and the handoff theme (generated CSS committed, CLI pinned and checksum-verified), and
self-hosted IBM Plex fonts.

## Context

Until now every `/v1` route except the webhook call answers anyone who can reach it. That is
safe only because the API listens on loopback and the VM (feature 005) reaches it through an SSH
tunnel. Phase 2 puts a web console in front of people (007), so authentication has to exist
first.

Who uses this:

- **The owner**: the one person who runs the VM. They are developer and operator at once. They
  have shell access to the host, and that is what proves ownership: the first admin is created
  there, and a lost password is reset there.
- **API clients**: the .NET Aspire integration `PublishToCaravana`, CI jobs, scripts, `curl`.
  They can't fill in a login form; they carry a token.
- **Webhook senders** (feature 004): unchanged. They keep their own per-app tokens and never
  see the rest of the API.
- **Feature 007**: builds the console on the page pipeline, the session and the sign-in this
  feature delivers.

An **API token** is a long random secret, given a name by the admin, that authorizes the whole
API on the admin's behalf. A **session** is what a browser holds after the admin signs in.

## Clarifications

### Session 2026-10-04

- Q: What does the sign-in limit count against? → A: Each source address, as reported by the
  trusted proxy. An attacker can't lock the owner out of the web from another address.
- Q: What does the admin land on after signing in, before the 007 console exists? → A: A small
  account page: change password, API tokens (create, list, revoke) and sign out, without the
  007 shell. 007 turns it into the console's Settings.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - The owner claims the instance from the host, and the API closes (Priority: P1)

The owner installs or upgrades caravana. Until they act, every protected API call is refused,
and the sign-in page tells them to create the admin on the host. They run
`caravana admin init` on the host, enter an email and a password, and the instance has its one
admin. Nobody else can become admin: there's no sign-up page and no "first visitor wins".
If they forget the password, the same CLI on the host sets a new one.

**Why this priority**: closing the API is the reason this feature exists. Without an admin no
other story can be tried.

**Independent Test**: start caravana with an empty store; call every protected route and see
401; run the init command; see that a second init is refused; reset the password from the CLI
and see the old password stop working.

**Acceptance Scenarios**:

1. **Given** a fresh install with no admin, **When** any protected API route is called,
   **Then** it answers 401 with the standard error body, and nothing is deployed or changed.
2. **Given** no admin, **When** someone opens `/login`, **Then** the page says no admin exists
   yet and shows the command to run on the host, and offers no way to create one from the web.
3. **Given** no admin, **When** the owner runs `caravana admin init` on the host with an email
   and a password that meets the rules, **Then** the admin exists and can sign in.
4. **Given** an admin exists, **When** `caravana admin init` runs again, **Then** it refuses,
   says an admin already exists, names the reset command, and changes nothing.
5. **Given** an admin with a forgotten password, **When** the owner runs the CLI password
   reset on the host, **Then** the new password works, the old one doesn't, and every open
   session ends. API tokens keep working.
6. **Given** an upgrade of an install from before this feature, **When** caravana starts,
   **Then** deployed apps keep running, webhook calls keep working, and protected API calls are
   refused until the admin is created.

---

### User Story 2 - Clients use API tokens (Priority: P1)

The owner creates an API token named `ci-github` for their CI, optionally with an expiry. The
token is shown once. The CI sends it as `Authorization: Bearer` and can deploy. Later the owner
lists their tokens (name, when created, when last used, expiry) and revokes one; it stops
working at once.

**Why this priority**: the API has real clients from day one (constitution V). Without tokens,
closing the API would break the Aspire integration, CI and the gate.

**Independent Test**: create a token from the host CLI and through the API; call a protected
route with it; list; revoke; call again and get 401; let an expiring token pass its expiry and
get 401.

**Acceptance Scenarios**:

1. **Given** a signed-in admin or another valid token, **When** the client creates a token
   named `ci-github`, **Then** the response holds the token once, starting with `cvat_`, and no
   later request returns it again.
2. **Given** shell access to the host, **When** the owner creates a token with the CLI,
   **Then** it prints the token once, for scripts and the gate.
3. **Given** a valid token, **When** a client calls any protected route with
   `Authorization: Bearer <token>`, **Then** the call works as before this feature.
4. **Given** several tokens, **When** the admin lists them, **Then** each shows its name,
   creation time, last-used time, expiry and whether it is active, never the value.
5. **Given** a token, **When** the admin revokes it, **Then** the next call with it answers
   401.
6. **Given** a token whose expiry has passed, **When** it is used, **Then** it answers the same
   401, and the list shows it as expired.

---

### User Story 3 - The admin signs in on the web (Priority: P1)

The admin opens caravana in a browser and lands on the sign-in page: the caravana wordmark,
"Sign in", email, password. They sign in and stay signed in across pages until they sign out
or the session expires. A page opened without a session sends them to sign in, then back to
where they were going.

**Why this priority**: the console (007) is the human front door; it needs a signed-in admin.

**Independent Test**: HTTP tests against the page routes: sign in with right and wrong
credentials, follow the redirect back, sign out, expire a session with a fake clock, and check
the cookie's attributes.

**Acceptance Scenarios**:

1. **Given** the admin's email and password, **When** they submit the sign-in form, **Then**
   they are signed in and taken to the page they asked for, or to the landing page.
2. **Given** a wrong email or a wrong password, **When** the form is submitted, **Then** the
   page shows the same sentence for both, so nobody learns which email is the admin's.
3. **Given** a signed-in admin, **When** they sign out, **Then** the session ends on the server
   too: the old cookie, replayed, is not signed in.
4. **Given** a session that has been idle past its limit, or that is past its absolute limit,
   **When** the admin opens a page, **Then** they are asked to sign in again.
5. **Given** no session, **When** a page is requested, **Then** the browser is redirected to
   `/login`; **and given** no credentials, **When** an API route is requested, **Then** it gets
   401 JSON and never a redirect.
6. **Given** a sign-in link carrying a "return to" address on another site, **When** the admin
   signs in, **Then** they land on caravana's own landing page, never on the other site.
7. **Given** a signed-in browser, **When** another site makes the browser submit a
   state-changing request to caravana, **Then** caravana refuses it.

---

### User Story 4 - Failed sign-ins are limited (Priority: P2)

Someone tries passwords against the sign-in page. After a few failures they have to wait, and
the page says, in plain words, how many tries are left and, once they're used up, when the next
try is allowed. The owner's own access never depends on this page: the CLI and API tokens keep
working.

**Why this priority**: the page will face the internet; unlimited guessing is not acceptable.
P2 because US1–US3 already work without it.

**Independent Test**: HTTP tests with a fake clock: fail repeatedly, read the tries-left
message, hit the limit, see that even the right password is refused until the wait ends, and
see that API tokens are unaffected.

**Acceptance Scenarios**:

1. **Given** a failed sign-in, **When** the page answers, **Then** it says how many tries are
   left before a wait.
2. **Given** the limit is reached, **When** another sign-in is tried, even with the right
   password, **Then** it is refused without checking the password, and the page says when to
   try again.
3. **Given** the wait has passed, **When** the admin signs in with the right password, **Then**
   it works and the count starts over.
4. **Given** sign-ins are being limited, **When** an API token or the host CLI is used,
   **Then** it works normally.
5. **Given** the limit is reached, **When** the server logs it, **Then** the log line says
   sign-ins are being limited and from where, without any password.

---

### User Story 5 - The admin changes their password and manages tokens after signing in (Priority: P2)

Signed in, the admin can change their password (giving the current one) and manage API tokens
without the host CLI. Changing the password signs out every other browser.

**Why this priority**: a password that can only change over SSH is awkward day to day. P2
because the CLI covers the need.

**Independent Test**: HTTP tests: change the password with the wrong and right current one;
check that other sessions end and the current one stays; create and revoke a token from the
signed-in page.

**Acceptance Scenarios**:

1. **Given** a signed-in admin, **When** they change their password with the right current
   password and a new one that meets the rules, **Then** the new password works, the old one
   doesn't, every other session ends, and this one stays signed in.
2. **Given** a wrong current password, **When** they try to change it, **Then** nothing
   changes and the page says the current password was wrong.
3. **Given** a signed-in admin, **When** they land after signing in, **Then** they see a
   small account page: change password, API tokens (create, list, revoke, with a new token
   shown once) and sign out, laid out without the 007 shell.
4. **Given** the account page, **When** the admin creates a token there, **Then** it is the
   same token the API and CLI create, listed and revoked the same way.

---

### User Story 6 - The sign-in page looks and reads like caravana (Priority: P3)

The sign-in page is the first piece of the console anyone sees. It follows the design system:
dark ground, Plex type, the lowercase wordmark, a calm sentence when something is wrong, with
the warning shape next to it. It works with only a keyboard and without JavaScript, and it asks
nothing of any other server.

**Why this priority**: the page has to work (P1); this story is about getting it right and
leaving 007 a pipeline to build on.

**Independent Test**: render the page in a browser at desktop and phone widths, check against
DESIGN.md, tab through it, load it with JavaScript off, and record every network request;
rebuild the stylesheet from source and compare with the committed one.

**Acceptance Scenarios**:

1. **Given** the sign-in page, **When** it loads, **Then** every font, style and script comes
   from caravana itself, and no request goes to another host.
2. **Given** a keyboard only, **When** the admin tabs through the page, **Then** every field
   and the button get a visible focus ring, in a sensible order, and the form submits with
   Enter.
3. **Given** an error, **When** it is shown, **Then** it is a plain sentence with the warning or
   failed shape next to its color, announced to screen readers, and the email stays filled in.
4. **Given** JavaScript is turned off, **When** the admin signs in, **Then** it still works.
5. **Given** the committed style sources and the pinned style tool, **When** the stylesheet is
   rebuilt, **Then** the result is identical to the committed stylesheet.

---

### Edge Cases

- **Admin init while caravana runs**: the CLI works against the same data whether the server is
  running or not; a running server sees the new admin without a restart.
- **Init or reset with a weak password**: refused with a sentence naming the rule; nothing is
  stored.
- **Password passed on the command line**: the CLI reads the password from a prompt or standard
  input, never from a flag, so it doesn't end up in shell history or the process list.
- **Email format**: the admin's email is a sign-in name; caravana never sends mail to it. It is
  compared without regard to letter case and surrounding spaces.
- **Token name already used**: refused with an error naming the field. Names are unique among
  active tokens.
- **Token expiry in the past or unparseable**: refused with an error naming the field.
- **Bearer header with a webhook token on a `/v1` route**, or an API token in a webhook call:
  401. The two kinds of token never stand in for each other.
- **Malformed `Authorization` header** (wrong scheme, empty, several): 401, same body as a wrong
  token.
- **A session cookie and a Bearer token on the same request**: the token decides; a bad token is
  401 even with a valid cookie.
- **Session cookie sent to the API**: a signed-in browser may call the API with its cookie (007
  needs it); state-changing calls then need the CSRF proof. Bearer calls never do.
- **Sign-out with an already expired session**: still lands on `/login` without an error.
- **Clock changes**: expiry is judged by the server's clock as it reads at each request. If the
  clock moves backwards, an expired token or session passes again until the clock catches up;
  hosts keep time with NTP, so this is accepted rather than guarded against.
- **Revoking the token the request is using**: allowed; that request finishes, the next one is
  refused.
- **Deleting the last token**: allowed; the CLI can always create another.
- **Webhook-only listener**: unchanged. It serves only `POST /v1/webhooks/{id}`; `/login`, the
  styles and every other path answer 404 there.
- **Webhook token management** (`/v1/apps/{id}/webhook`): now protected like every other
  management route.
- **Restart**: admin, password, tokens, sessions and sign-in limits survive a restart; a
  restart doesn't reset the failed-try count.
- **Over plain HTTP on loopback** (local development): sign-in works; the cookie is only marked
  for HTTPS when caravana is served over HTTPS.

## Requirements *(mandatory)*

### Functional Requirements

**Admin account**

- **FR-001**: The system MUST have at most one admin account, and MUST offer no web flow that
  creates one.
- **FR-002**: A host CLI command (`caravana admin init`) MUST create the admin from an email and
  a password, read from a prompt or standard input, and MUST refuse when an admin already
  exists.
- **FR-003**: A host CLI command MUST set a new admin password without the old one. It MUST end
  every session and MUST leave API tokens working.
- **FR-004**: Passwords MUST be at least 12 characters; there are no other composition rules.
  Refusals MUST name the rule.
- **FR-005**: The store MUST keep only a salted, deliberately slow one-way hash of the password.
- **FR-006**: While no admin exists, every protected route MUST refuse with 401, and `/login`
  MUST say how to create the admin on the host.

**Sign-in and sessions**

- **FR-007**: The admin MUST be able to sign in at `/login` with email and password. A wrong
  email and a wrong password MUST get the same answer and take about the same time.
- **FR-008**: A successful sign-in MUST start a server-side session identified by a random
  value of at least 256 bits, kept by the browser in a cookie that scripts can't read, isn't
  sent on cross-site requests, and is marked for HTTPS whenever caravana is served over HTTPS.
  The store MUST keep only a hash of the session value.
- **FR-009**: Signing out MUST end the session on the server.
- **FR-010**: Sessions MUST end after 12 hours without use and 7 days after sign-in, whichever
  comes first.
- **FR-011**: A page request without a valid session MUST redirect to `/login` and, after
  sign-in, return to the requested caravana path. A return address outside caravana MUST be
  ignored.
- **FR-012**: Every state-changing request authenticated by a session cookie, including sign-in
  and sign-out, MUST be refused when the browser reports that it came from another site (its
  `Sec-Fetch-Site` or `Origin` header, which a page on another site can't forge). Requests
  authenticated by a Bearer token MUST NOT need this check.
- **FR-013**: A signed-in admin MUST be able to change their password by giving the current
  one, on the account page or through the API. The change MUST end every other session and
  keep the current one.
- **FR-013a**: After sign-in the admin MUST land on an account page that offers change
  password, API token create (shown once), list and revoke, and sign out, using the same
  service calls as the API. It follows DESIGN.md and needs no JavaScript.
  *Superseded by 007 and 021 (2026-10-29): the account page is Settings (`/settings`, later `/settings/tokens` and the grouped Settings pages); `/account` redirects to it.*

**Failed sign-in limits**

- **FR-014**: After 5 failed sign-ins within 15 minutes, further sign-ins MUST be refused,
  without checking the password, for 15 minutes, counted per source
  address, so guessing from one address can't lock the owner out from another. The source
  address is the client address reported by the trusted proxy in front of caravana (Traefik);
  a forwarded address from anyone else is ignored. IPv6 addresses count per /64 network. Each
  failure MUST say how many tries are left, and a refusal MUST say when to try again.
- **FR-015**: Sign-in limits MUST NOT affect API tokens or the host CLI.
- **FR-016**: Failed-try counts MUST survive a restart.

**API tokens**

- **FR-017**: An authenticated caller MUST be able to create an API token with a name and an
  optional expiry. The response MUST contain the token, and no later request may return it.
- **FR-018**: A host CLI command MUST create an API token and print it once.
- **FR-019**: Tokens MUST be random with at least 256 bits of entropy and carry the `cvat_`
  prefix, so secret scanners can find leaked ones. The store MUST keep only a one-way hash.
- **FR-020**: An authenticated caller MUST be able to list tokens with name, creation time,
  last-used time, expiry and state (active, expired, revoked), never the value.
- **FR-021**: An authenticated caller MUST be able to revoke a token; from then on it MUST be
  refused. An expired token MUST be refused the same way.
- **FR-022**: Every active token MUST grant the whole API, as the admin. There are no scopes.
  *Superseded by 020 (2026-10-29): a deploy token is an API token with a scope, one app name in one project (`specs/020-deploy-from-ci/`). A token without a scope still grants the whole API.*

**Access rules**

- **FR-023**: Every `/v1` route MUST require a valid session or API token, except
  `GET /v1/health` and `POST /v1/webhooks/{id}`, which keeps its per-app webhook token.
- **FR-024**: A refused API request MUST get 401 with the standard error body and code
  `unauthorized`, the same for every reason (missing, malformed, wrong, expired, revoked, no
  admin yet), and MUST NOT be redirected.
- **FR-025**: Webhook tokens and API tokens MUST NOT be accepted in each other's place.
- **FR-026**: The webhook-only listener MUST keep serving only the webhook call; it MUST NOT
  serve `/login`, pages or styles.

**Logs**

- **FR-027**: Passwords, API tokens, webhook tokens, session values and CSRF values MUST NEVER
  appear in logs, error messages, or any response except the one that creates a token. Request
  logs MUST redact the `Authorization` and `Cookie` headers and `token` query values.
- **FR-028**: The server MUST log sign-ins, failed sign-ins, sign-in limiting, sign-outs,
  password changes and resets, and token creation and revocation (by token name), without
  secrets.

**Sign-in page and UI groundwork**

- **FR-029**: The `/login` page MUST follow DESIGN.md and the handoff's login screen, reduced
  to: the wordmark, the heading "Sign in", email, password, a "Sign in" button and the error
  sentence. It MUST NOT show SSO buttons, 2-step codes or a password-reset link; it MUST say
  that a forgotten password is reset on the host.
- **FR-030**: The page MUST work without JavaScript, with a keyboard only, and with a screen
  reader (labeled fields, errors announced), and MUST respect reduced-motion settings.
- **FR-031**: Every page asset (styles, fonts, scripts) MUST be served by caravana itself,
  from inside the binary, with no request to another host.
- **FR-032**: The stylesheet MUST be built from committed sources with a pinned,
  checksum-verified style tool that needs no Node, and the generated stylesheet MUST be
  committed. CI MUST fail when rebuilding gives a different result, and building caravana MUST
  NOT need the style tool.

**API contract and layering**

- **FR-033**: `api/openapi.yaml` MUST declare the Bearer and session schemes, the 401 on every
  protected route, the token endpoints (create, list, revoke), the password change endpoint and
  the new error codes, in the same change as the routes. Pages (`/login`, the landing page) are not API routes and are not in
  the contract.
- **FR-034**: Admin, session and token logic MUST live behind `internal/service`, used by the
  API, the pages and the CLI alike (constitution V).

**Testing**

- **FR-035**: Tests MUST run without a network and cover: every protected route refusing
  without credentials (walked from the route table), each 401 reason, token create, list,
  revoke and expiry, sign-in and sign-out, session expiry with a fake clock, the redirect and
  its return address, CSRF refusal, sign-in limits, password change and reset, and a scan of
  captured logs for planted secrets.
- **FR-036**: The opt-in lab test (`TestLab`) and the VM gate (`deploy/vm/host.sh prove`) MUST
  use an API token created with the host CLI for every API call, and check that a call without
  it is refused. The lab test creates the admin itself. On the VM the owner creates the admin
  once (`deploy/vm/host.sh admin init`); `prove` stops with that instruction when there is
  none, and revokes its own token when it ends.

### Key Entities

- **Admin account**: the one admin. An email (sign-in name), a password hash, when the password
  last changed.
- **Session**: a signed-in browser. Hash of its value, when it started, when it was last used,
  when it ends.
- **API token**: a name, a hash of the value, created, last used, optional expiry, revoked at.
- **Sign-in attempts**: per source address, the recent failed sign-ins and when a wait ends.
- **Webhook token** (feature 004): unchanged, and kept separate from API tokens.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: On a fresh install, 100% of protected routes answer 401 without credentials, and 0
  of them redirect.
- **SC-002**: From a fresh install, the owner can create the admin, create a token and make a
  working authenticated `curl` call in under 2 minutes, following the docs.
- **SC-003**: After revocation or expiry, a token succeeds 0 times.
- **SC-004**: Once the sign-in limit is reached, 0 sign-ins succeed until the wait ends, even
  with the right password, while 100% of valid token calls keep working.
- **SC-005**: Passwords, tokens and session values planted in requests appear 0 times in
  captured logs and in every response except the one that created a token.
- **SC-006**: The sign-in page makes 0 requests to other hosts, can be completed with a keyboard
  alone, and works with JavaScript turned off.
- **SC-007**: Every CI check passes, including the contract-sync test (0 differences) and the
  stylesheet rebuild check (0 differences).
- **SC-008**: `TestLab` and `host.sh prove` pass with auth on.

## Assumptions

- **The account page becomes 007's Settings**: 007 moves it into the console shell; until
  then it is the signed-in landing page.
- **Hashes are not secrets** in the constitution's sense (VII). Password, token and session
  hashes can't be used to sign in or call the API, so they live in the SQLite store, as
  webhook token hashes do (feature 004). If the plan needs a real secret (for example a key to
  sign something), it lives in Nomad Variables; the plan should prefer designs that need none.
- **One admin, full power**: every token and every session can do everything. Users, roles and
  token scopes are Phase 4.
- **Exposure doesn't change here**: `host.sh` keeps the API and pages on loopback, reached
  through the tunnel. Publishing the console over HTTPS comes with 007, when there is a console
  to publish.
- **HTTPS detection**: caravana knows it is served over HTTPS from its configuration or from
  the trusted proxy in front of it; the plan picks which.
- **Session lifetimes** (12 hours idle, 7 days absolute) and **limit values** (5 tries, 15
  minutes) are defaults chosen for a single-admin console; they are settings only if the plan
  finds a reason.
- **No email**: caravana sends no mail. The email is a sign-in name, and reset happens on the
  host.
- **Out of scope**: more than one user, roles, teams, token scopes, SSO/OIDC, 2FA, email
  delivery, "remember me", the console shell and app screens (007), publishing the console
  publicly (007/012).
