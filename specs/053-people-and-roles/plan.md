# Plan 053: People and roles

Spec: `spec.md`. Task `nvxx`. Branch: the worktree branch `alta/dewy-quince-q5ss` (from `main` at 93cba8e, where 051 is
merged). Numbers: API **0.59.0**, migration **0043**. API: `contracts/api.md`.

## Constitution check

- I Small by default: no new Go module (bcrypt is not needed: passwords stay PBKDF2), no runtime dependency.
- II One model: nothing about `app.App`, `Translate` or helper jobs changes.
- III Safe deploys: unchanged. A deploy-level person may only change images (D7), so roles can't loosen a deploy.
- IV Fail loudly: every refusal is 403 `not_allowed` with a sentence naming the role held and the one needed (and, for a
  body deploy, the field that needs maintain). Nothing is silently dropped: a filtered list says nothing about places the
  person can't see, which is the point, not a dropped field.
- V API-first: every console route and API operation share one rule set and one decision function in
  `internal/service` (D4); people management is in the API (`contracts/api.md`) and the console; the MCP server goes
  through the API and inherits the token owner's rights. The invite acceptance has an API operation; the console's
  accept page also starts a session (like `POST /login`, listed in `consoleOnly`).
- VI Nomad-native: no change. One Nomad namespace stays (D12).
- VII Secure: invite tokens are SHA-256 stored and shown once; passwords stay PBKDF2; sessions and tokens are checked
  against their person on every request; removal deletes credentials in one transaction; the default deny means a new
  route is refused to everyone until someone lists it. Constitution VII names Caravana's own secrets and hashes, which
  this follows; no amendment.
- VIII Plain, tested Go: table tests for the rules, the two route walks, store migration tests, a lab gate step and a
  console step.

## Decisions

### D1 Store (migration `0043_people.sql`)

```sql
CREATE TABLE people (
  id                  TEXT PRIMARY KEY,             -- 26 lowercase base32 (rand.Text), not secret
  email               TEXT NOT NULL UNIQUE,         -- trimmed, lower case
  org_role            TEXT NOT NULL CHECK (org_role IN ('owner','admin','member')),
  state               TEXT NOT NULL CHECK (state IN ('invited','active')),
  password_hash       TEXT NOT NULL DEFAULT '',     -- '' while invited
  password_changed_at TEXT,
  password_signin     INTEGER NOT NULL DEFAULT 1,
  webauthn_handle     BLOB,
  invite_hash         TEXT UNIQUE,                  -- hex SHA-256 of cvin_…; NULL once accepted
  invite_expires_at   TEXT,
  invited_by          TEXT,                         -- the inviter's email, kept as text
  ends_at             TEXT,                         -- NULL: no end (a guest has one)
  created_at          TEXT NOT NULL
) STRICT;
CREATE TABLE project_roles (
  person_id TEXT NOT NULL REFERENCES people (id) ON DELETE CASCADE,
  project   TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
  role      TEXT NOT NULL CHECK (role IN ('view','deploy','maintain')),
  ends_at   TEXT,
  PRIMARY KEY (person_id, project)
) STRICT;
CREATE TABLE environment_roles (
  person_id   TEXT NOT NULL,
  project     TEXT NOT NULL,
  environment TEXT NOT NULL,
  role        TEXT NOT NULL CHECK (role IN ('none','view','deploy','maintain')),
  PRIMARY KEY (person_id, project, environment),
  FOREIGN KEY (person_id, project) REFERENCES project_roles (person_id, project) ON DELETE CASCADE,
  FOREIGN KEY (project, environment) REFERENCES environments (project_id, name) ON DELETE CASCADE
) STRICT;
```

The one `admin` row becomes a person: same email, hash, `password_changed_at`, `password_signin`, `webauthn_handle`,
`org_role = 'owner'`, `state = 'active'`, `created_at`, a new ID (`lower(hex(randomblob(13)))`, 26 characters). `sessions`,
`api_tokens` and `passkeys` are rebuilt with `person_id TEXT NOT NULL REFERENCES people (id) ON DELETE CASCADE` (all
existing rows get the owner; with no admin, the tables are empty and stay so). `admin` is dropped. `deployments`,
`app_actions`, `backups` and `restores` gain `actor TEXT` (the person's email, NULL for rows from before and for
webhook, schedule and autoscale work). The store turns foreign keys on (`_pragma=foreign_keys(1)`), and
`DeletePerson` still deletes the person's sessions, tokens, passkeys and roles explicitly in its transaction.

Store methods (`internal/store/people.go`): `CreatePerson`, `GetPerson(id)`, `GetPersonByEmail`, `GetPersonByInvite(hash)`,
`ListPeople`, `UpdatePerson` (role, ends), `SetPersonRoles(id, []ProjectRole)` (replaces project and environment rows in
one transaction), `PersonRoles(id)`, `AcceptInvite(id, hash, at)`, `SetInvite(id, hash, expires)`, `DeletePerson(id)`,
`CountOwners`, `FirstOwner`. Sessions, tokens and passkeys take and return `PersonID`. The admin-singular methods
(`CreateAdmin`, `GetAdmin`, `SetPassword`, `SetPasswordSignIn`, `SetWebAuthnHandle`) become per-person
(`SetPassword(personID, …)`, …); `CountPasskeys(rpID)` takes the person. No old method stays as an alias.

### D2 Auth per person (`internal/service`)

`Auth` keeps its job (sign-in, sessions, tokens, passkeys, the limit) for many people:

- `SignIn(email, …)` looks the email up among active people (an invited, ended or unknown email costs the same dummy hash
  and the same failure). Password sign-in off is per person (033's rule per person: off **and** that person has a
  passkey at the console domain).
- Passkeys: the WebAuthn user handle is per person (made at their first registration). A passkey sign-in finds the
  person by the credential (`GetPasskeyByCredential`) and checks the user handle is that person's.
- `CheckSession` and `CheckAPITokenNamed` return the `Principal` (D3): they load the person and refuse (the same 401)
  when the person is gone, not active, or past `ends_at`. A person past their end date has their sessions deleted on
  that check.
- Tokens: `CreateAPIToken` and `CreateScopedAPIToken` take the person; `ListAPITokens` and `RevokeAPIToken` act on the
  person's own; a scoped token needs the person to hold deploy or more on some environment of the scoped project (else
  `ErrNotAllowed`).
- `ChangePassword`, the sign-in settings and passkey list, rename and remove act on the session's person.
- Host CLI: `InitAdmin` creates the first owner (refused once anyone exists); `ResetPassword(email, …)`,
  `CreateAPIToken` for the CLI take an email (`--email`, default the first owner); `admin status` lists each person
  (email, role, state); `admin passkeys list` shows each passkey's person.

### D3 Principal and grants

`service.Principal{PersonID, Email, OrgRole, Roles []ProjectRole, Scope *TokenScope, TokenName}`; `ProjectRole{Project,
Role, EndsAt, Environments map[string]Role}`. Loaded fresh from the store on every request (one person row plus its role
rows), so a role change or a removal applies to the next request. `service.WithPrincipal(ctx, p)` /
`PrincipalFrom(ctx)` carry it; the API's `authenticate` and the console's guard put it in the request context (and
`WithTokenName` stays for the deployment's token name). A context without a principal is Caravana itself (the
watchers, the notifier, the CLI): nothing is filtered and nothing is refused.

Pure functions in `internal/service/access.go`:
- `Level` (`LevelNone < LevelView < LevelDeploy < LevelMaintain`).
- `(p Principal) LevelAt(project, environment string, now time.Time) Level`: owner and admin → maintain; a member →
  the environment's override if set, else the project role, a role past its `EndsAt` counting as none; a preview
  environment (`app.IsPreviewEnvironment`) takes the project role.
- `(p Principal) ProjectLevel(project, now) Level`: the project role (for project settings and creating environments).
- `(p Principal) Admin() bool`, `Owner() bool`.

### D4 One decision, two route tables

`service.Need` is what a route needs, built by constructors in `service/access.go`:

- `NeedSignedIn()` — any person (their own account, `/v1/me`, lists the service filters (D6)).
- `NeedAdmin()` — owners and admins.
- `NeedApp(level)` — the app whose ID the caller gives; its place is `app.PlaceOf(id)` (an old ID with `--` is
  Default › production, as 019).
- `NeedPlace(level)` — a project and environment the caller gives.
- `NeedProject(level)` — a project's own role (`ProjectLevel`).

`(*Service).Allow(ctx, need, target) error` (target carries the app ID or project/environment) answers every check:
nil, or `*NotAllowedError{Have, Need Level, Place, Field}` which wraps `ErrNotAllowed` and reads "your role in shop ›
production is view; this needs deploy". `NeedApp` on an app that doesn't exist is decided by its ID's place, so an
outsider gets the same 403 whether or not the app exists.

The API keeps `scopeRules` (020) and adds `accessRules map[string]accessRule` keyed like it ("METHOD pattern"), where
an `accessRule` is `func(r *http.Request) (service.Need, service.Target, bool)` reading only the path, query and peeked
body, as scope rules do. `authenticate` runs: credentials (401) → scope rules for a deploy token (403 `token_scope`) →
the access rule (403 `not_allowed`; a route with no entry is refused for everyone, default deny; `ok == false` (the
request doesn't say what it is about) is refused too) → the handler. The console has the same table for its routes
(`internal/ui/access.go`), applied by the guard that replaces `requireSession`, which renders the refusal as a 403 page
("You can't do this here" + the sentence) and a part as a 403 fragment. Both use the same constructors and
`Service.Allow`, so the decision lives in one place.

A few routes need two places: promote (view on the source app, deploy on the target `to`), copy (view on the source,
maintain on the target environment). Their rule returns the target environment's need, and the service method checks
the source too (D5).

Route classes (the child classifies every route; this is the rule):

| Class | Need |
| --- | --- |
| own account: `/v1/me`, password, sign-in settings, passkeys, own API tokens, appearance, sign-out | signed in |
| reads of an app (record, deployments, copies, logs, requests, metrics, backups, schedules, run output, volumes, domains, exports, compose file, scan) | app view |
| reads of a place (`/v1/logs`, `/v1/requests`, metrics of a place, a project's environment) | place view |
| deploy (empty body or images only, D7), redeploy a version, restart, start, run a schedule now, back up now, refresh secrets, the build log | app deploy |
| promote into an environment | target deploy (+ source view) |
| configure: deploy with a changed definition, compose deploy, secrets, domains and their options and passwords, scaling, placement, backup policy and places, pins, restores, schedules' create/edit/delete, webhook token, deploy token for the app, Git access, stop, delete, Run a command, terminal | app maintain |
| create an app in a place, copy into it | place maintain |
| a project's settings, create or delete an environment (a preview: project deploy), preview settings, close a preview (project deploy) | project maintain / deploy |
| cross-project lists: apps, projects, recent deployments, attention, previews, schedules, domains | signed in (filtered, D6) |
| everything else: settings of the installation, servers, host, updates, operator pages, activity feed, registries, notifications, destinations, DNS, secret providers, certificates, builds, templates catalog settings, scans and add-ons, console address, cleanups, volumes deletions, people, create or delete a project | admin |

Templates' list and preview are signed in (a member with maintain installs into their place: `installTemplate` is place
maintain). Public routes (health, webhook call, node calls, discovery, invite acceptance) stay public.

### D5 Checks inside service methods

Some checks depend on stored state, so they live in the service method and read the principal from the context:
- `DeployIf`/`Deploy` with a body (D7), `DeployComposeIf` (maintain), creating an app (`checkPlace` path: maintain on the
  place).
- `Promote` and `PreviewPromotion`: view on the source app (the route checked deploy on the target).
- `CopyApp`: view on the source.
- `CreateScopedAPIToken`: deploy on the project (D2).
- People management (D8): owner-only changes.

### D6 Filtering

`(*Service).visible(ctx) visibility` gives a predicate `canSee(project, environment) bool` (always true for no principal,
an owner or an admin). Every service method that returns rows of several places filters with it: `ListApps`,
`Projects` (a member sees a project where some environment is visible, and only those environments), `GetProject`,
`RecentDeployments`, `Attention` (place items by visibility; installation items (servers, disk, OS, updates, Caravana's
update, backup destination missing) only for admins), `Previews`, `ListSchedules`, `ListDomains`, `ListCertificates`
(admin), `Activity` (admin route). The console's live streams filter each notice by the app it names (an app-less notice,
like `servers` or `metrics` of the host, only for admins). Search and counts read these lists, so they follow. A
member's `/logs`, `/requests` and `/monitoring` without a place go to the first visible place.

### D7 Images-only deploys

`app.ImagesOnlyChange(stored, next app.App) (field string, ok bool)`: true when `next` equals `stored` except each
service's `image` (same services in the same order). `DeployIf` with a principal below maintain at the app's place
refuses anything else with `*NotAllowedError{Field: "services[i].<first differing field>"}`. An app that doesn't exist
yet needs maintain (it is created). The CI action and MCP's `deploy_image` send the stored app with one new image, so a
deploy-level token works for them.

### D8 People (`internal/service/people.go`)

- `InvitePerson(ctx, PersonInput{Email, OrgRole, EndsAt, Projects []ProjectRoleInput})` → `Invited{Person, Token}`:
  admins may invite members; owners anyone. Email unique among people (409 `email_taken`). Projects and environments
  must exist (404 `project_not_found`/`environment_not_found` on `projects[i].project` /
  `projects[i].environments.<env>`). Owners and admins have no project roles (field error `projects` if given).
  Token `cvin_` + 32 random bytes in base32, SHA-256 stored, 7 days.
- `ReinvitePerson(id)` (only `invited`; else 409 `not_invited`): a new token, the old one void.
- `AcceptInvite(ctx, token, password, address)`: unknown, expired or used → `ErrInviteInvalid` (404 `invite_invalid`),
  counted against the address like a failed sign-in (`beginAttempt` first, so a locked address is refused before the
  token is looked up); sets the password, `state = active`, clears the invite. Returns the person; the console then
  starts a session.
- `UpdatePerson(id, input)`: replaces org role, end date and roles. An admin may change members only, and can't make
  anyone owner or admin; the last owner can't be demoted (409 `last_owner`).
- `RemovePerson(id)`: same rights; the last owner can't be removed; deletes the person and their credentials in one
  transaction (D1). Records keep the email in `actor`.
- `People(ctx)`, `Person(id)`, `Me(ctx)`.
- An invite's link is `<public URL or the request's origin>/invite?token=<token>`, shown once. Caravana sends no mail.
  The request log never writes query strings (httpx), so the token isn't logged.

### D9 Who did what

The acting person's email goes on `deployments.actor` (set in `deployWarned` from the principal, beside the token name),
`app_actions.actor`, `backups.actor` (Back up now) and `restores.actor`. The console's who-words (`deployWho`, the action
and backup rows) add "by ana@example.com", or "by you" when the viewer is that person; through a token "through the API
by `ci` (ana@example.com)". The API's `Deployment`, `AppAction`, `Backup` and `Restore` gain `actor`.

### D10 Console

- The guard (D4) replaces `requireSession`; `pageData` carries the principal so templates call `{{if .Can "deploy"}}`
  -style helpers for the main actions: the Deploy form, Configure's forms, Restart/Stop/Start/Delete, Run a command and
  Terminal links, Secrets, Domains' edits, Backups' actions, Schedules' New and edits, New app (needs maintain somewhere).
- The sidebar shows the Operator toggle and Settings' installation groups only to admins. A member's Settings shows
  their account, sign-in, tokens and appearance.
- Settings › People (`/settings/people`, Security group, admin): the list (email, role, state, end, projects), Invite
  (`/settings/people/new`: email, role, end date, per project a role and per environment an override), the link shown
  once (200 `no-store`), a person's page (`/settings/people/{id}`: change roles, New invite link, Remove…), each a form
  that works without JavaScript.
- `/invite?token=` (no session): the email, a password and its confirmation; `POST /invite` accepts, starts a session and
  answers 303 to `/apps`. Listed in `consoleOnly` (it starts a session), beside `acceptInvite` in the API.

### D11 API (0.59.0)

`contracts/api.md`. `getMe`, `listPeople`, `invitePerson`, `getPerson`, `updatePerson`, `removePerson`,
`reinvitePerson`, `acceptInvite` (public), `actor` on records, codes `not_allowed`, `person_not_found`, `email_taken`,
`invite_invalid`, `not_invited`, `last_owner`. `listTokens`, `createToken`, `revokeToken`, the passkey and sign-in
operations now act on the caller's own. MCP: a read tool `get_me`; nothing else changes (the token's owner limits it).

### D12 Open questions of the task, decided

- **Managed tier:** this comes first; nothing of the managed tier is built here.
- **SSO:** after this (a follow-up task); 053 keeps passwords and passkeys.
- **Nomad namespaces per project:** later. Caravana is the only Nomad client and enforces roles itself; 019's reason
  (Traefik's provider watches one namespace) still holds.
- **What each role sees:** view reads logs, requests, metrics and a schedule run's output (app output, like logs, masked
  of secrets); Run a command and the terminal need maintain (a command can print any secret); a secret's value is never
  readable by anyone; its name is view.
- **Webhook tokens and the update request:** an app's webhook token stays the app's credential (made and rotated with
  maintain; the call itself is public with its token, as today). Updating Caravana (030) is admin.

## Phases

- A Store, Auth per person, principal and rules (D1, D2, D3, the pure part of D4); every existing test passes with one
  owner.
- B Service: `Allow`, people management, filtering, images-only, actor (D4, D5, D6, D7, D8, D9).
- C API: access rules for every route, people routes, `/v1/me`, openapi 0.59.0, the route walk, MCP `get_me`.
- D Console: the guard and its table, the route walk, People pages, the invite page, hiding, who-words.
- E Lab gate step `people-roles` (API and MCP), console step `console-people`, docs (`docs/people.md`), CLAUDE.md,
  changelog note, `docs/plan.md`.

## Complexity tracking

| Choice | Why | Simpler option rejected |
| --- | --- | --- |
| Route tables plus checks inside a few service methods | The default deny of 020 makes every route a decision; state-dependent checks (images-only, promote's source) need the stored app | Checks only inside every service method: hundreds of methods, and a forgotten one is an open door; route tables only: can't see a deploy body's diff |
| `POST /invite` in `consoleOnly` | it starts a browser session, like `POST /login` | none: the API's `acceptInvite` does the rest |
