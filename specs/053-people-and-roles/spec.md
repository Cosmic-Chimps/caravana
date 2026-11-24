# Spec 053: People and roles

Task `nvxx` (2026-11-11, "Users, teams and permissions: more than one person, with roles per project and environment").
Plan: `docs/plan.md`, Phase 4, `MembersScreen`. The maintainer asked for it on 2026-11-24 ("Do this follow-up task"),
which is the approval constitution's Development Workflow asks for a feature outside the current phase.

## Why

Caravana has one admin (006) and API tokens (full, or deploy tokens for one app since 020). Nobody else can sign in and
nothing says who may do what. A small team, a client who should see only their project, and the managed tier (one
Caravana per customer, several people each) all need more than one person, with roles.

## User stories

1. **Invite (P1).** As the owner, I invite a person by email with an organization role and, for a member, a role per
   project with overrides per environment. Caravana shows me a one-time link (valid 7 days) that I send myself; the
   person opens it, sets a password and is signed in.
2. **Roles hold everywhere (P1).** A member with deploy on staging and view on production of project `shop` can, through
   the console, the API and the MCP server, read `shop`'s apps, logs and deployments in both, deploy and restart in
   staging, and nothing else. Everything else is refused with 403 `not_allowed`, naming the role they have and the one
   needed. Projects they have no role in don't appear anywhere: not in lists, counts, needs-you items, live notices or
   search.
3. **Admins run the installation (P1).** An admin does everything an owner does except changing owners and admins:
   settings, servers, registries, notifications, updates, the Operator view, every project, and inviting or removing
   members.
4. **Remove at once (P1).** Removing a person ends their sessions, API tokens, deploy tokens and passkeys within the
   request: their next call is 401.
5. **Guests (P2).** A person, or one project role of a person, may end on a date; after it, the person can't sign in (or
   the role counts as none) without anyone acting.
6. **Tokens follow people (P1).** An API token acts with its owner's rights, never more; a deploy token is limited by its
   scope (020) and by its owner's rights; a person sees and revokes only their own tokens and passkeys.
7. **Who did what (P2).** Deployments, app actions (restart, stop, start, Run a command, terminal), backups taken by
   hand and restores record the person, and the console says "by ana@example.com" (or "by you").
8. **Upgrade changes nothing (P1).** An existing host's one admin becomes its owner, with its sessions, tokens and
   passkeys; nothing else changes and no one else exists.

## Roles

- **Organization role:** `owner` (everything; the only role that can make, change or remove owners and admins),
  `admin` (everything else, including people who are members), `member` (only the projects they're given).
- **Project role** (members only; owners and admins have maintain everywhere): `view` (read pages, deployments, logs,
  requests, metrics, backups, schedules and their output, secret names, exports), `deploy` (view, plus deploy a new image
  or redeploy, roll back, restart, start, promote into the environment, run a schedule now, back up now, refresh
  secrets, open and close previews), `maintain` (deploy, plus configure, secrets, domains, scaling, placement, backups
  policy and restores, pins, schedules, webhook and deploy tokens for the app, Git access, stop, delete, Run a command,
  the terminal, create apps and environments, the project's settings).
- **Environment override:** per environment of a project, `none`, `view`, `deploy` or `maintain` replaces the project
  role there. Previews (`pr-<n>`) take the project role.
- **End dates:** a person's `endsAt`, and a project role's `endsAt`.

## Requirements

- FR-001 People are stored with email (unique, lower case), organization role, state (`invited`, `active`), optional
  end date, their own password, password-sign-in setting, WebAuthn handle and passkeys. At least one owner always exists.
- FR-002 Inviting makes an `invited` person with their roles and a one-time token `cvin_…` (SHA-256 stored, 7 days);
  accepting sets the password (12+ characters), makes them `active`, voids the token, and (in the console) signs them in.
  A wrong or used token counts against the address's sign-in limit. A new link voids the old one.
- FR-003 Only owners invite, change or remove owners and admins; admins invite, change and remove members. The last
  owner can't be removed or demoted (409 `last_owner`).
- FR-004 Every route of the API and the console is listed with what it needs; a route with no entry is refused for
  everyone (default deny, as 020's scope rules). One decision function in `internal/service` answers every check, and a
  test walks every route for every role.
- FR-005 Lists that span projects (apps, projects, recent deployments, needs-you, previews, schedules, domains, logs,
  requests, metrics, live notices) show a member only places where their effective role is at least view.
  Installation-wide items (servers, disks, updates, the host, Caravana's own update) are for owners and admins.
- FR-006 A deploy with a body by someone whose role is deploy may change only the services' images; any other change
  (or creating the app) needs maintain, refused with 403 `not_allowed` naming the field that changed.
- FR-007 Removing a person deletes their sessions, tokens and passkeys in the same transaction; a person past their end
  date is refused like a removed one (401), and their sessions end.
- FR-008 Every session and token is checked against its person on every request, so role changes apply to the next
  request.
- FR-009 Deployments, app actions, manual backups and restores record the acting person's email.
- FR-010 `GET /v1/me` says who the credential is and what it may do; the console's sidebar and pages hide what the
  person can't do (the server still refuses it).
- FR-011 The migration makes the admin the owner and gives them every existing session, token and passkey.
- FR-012 The host CLI acts on the owner (`admin init` creates the first owner, `reset-password` and `create-token` take
  `--email`, default the first owner; `status` lists people).

## Not in 053

Single sign-on, the audit log with diffs (`AuditSettings`), Nomad namespaces per project, personal notifications,
sending invite mail, several organizations, API scopes finer than roles.

## Success criteria

- SC-001 The Done-when of task `nvxx`: an owner invites an admin and a member (deploy on staging, view on production of one
  project); each can do exactly that through the console, the API and the MCP server, and is refused (403 `not_allowed`)
  for everything else (lab gate step `people-roles`, console step `console-people`).
- SC-002 Removing a person ends their sessions and tokens within the request.
- SC-003 `TestRolesWalkEveryRoute` (API) and `TestConsoleRolesWalkEveryRoute` (console) walk every route for every role.
- SC-004 A store migrated from 0042 with one admin has one owner with the same email, password hash, sessions, tokens and
  passkeys, and no other person.
