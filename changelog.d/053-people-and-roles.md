### Added
- **People and roles.** An installation can have several people, each with their own sign-in and API tokens. An
  owner or an admin invites someone from **Settings › People** (an email, a role, an optional end date and, for a
  member, a role per project with an override per environment) and sends them the link Caravana shows once; they
  choose a password and are in. A member sees only the places where they have a role: a **viewer** reads, a **deployer**
  deploys new images, redeploys, restarts and backs up now, a **maintainer** changes how an app is set up. A guest has
  an end date, after which they can't sign in. The installation's pages (Operator view, servers, registries, backup
  storage, notifications, people and the rest) are for owners and admins. See `docs/people.md`.
- **API tokens act as their person.** `GET /v1/me` says who a credential is, the people have their own routes
  (`listPeople`, `invitePerson`, `getPerson`, `updatePerson`, `removePerson`, `reinvitePerson`, and the public
  `acceptInvite`), and a refusal is `403 not_allowed` with the field that needs more. A deploy-level person may
  deploy images only: any other change is refused, naming the field, which is what the CI action and MCP's
  `deploy_image` send. MCP has a read tool, `get_me`.
- **Who did it.** Deployments, actions, backups started by hand and restores say who made them: "by you",
  "by ana@example.com", or "through the API by ci (ana@example.com)". The API records carry `actor`.

### Changed
- The console's live updates, lists and pages follow the person's role: a member's Apps, Projects, Logs, Requests,
  Monitoring and Schedules show their places only, and the buttons they can't use aren't drawn.

### Upgrade
- The one admin becomes the **owner** (same email, password, passkeys, sessions and tokens). Rerunning the
  installer is all: nothing else to rerun on the host. Records made before have no `actor`.
