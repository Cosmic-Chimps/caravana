# People and roles

An installation may have **several people**. Each has their own sign-in, their own API tokens, and a **role** that says what
they may see and do. Before this, Caravana had one admin; that admin is now the **owner**, with nothing to change.

## Roles

There are two kinds of role.

**In the installation** (`orgRole`):

- **owner**: everything, and the only role that makes, changes or removes owners and admins.
- **admin**: everything else: the installation's settings, servers, updates, every project, and members. An admin
  can't make, change or remove an owner or another admin.
- **member**: only what their project roles give.

**In a project**, for a member (`view`, `deploy`, `maintain`), with an **override per environment** (`none`, `view`,
`deploy`, `maintain`). Each includes the one before:

| Level | What it lets a person do in that place |
| --- | --- |
| **view** | Read: apps, deployments, copies, logs, requests, metrics, backups, schedules and their run output, domains, exports, the build log, the image scan. A secret's name, never its value. |
| **deploy** | Deploy a new image (or the stored app again), redeploy a version, restart and start an app, run a schedule now, back up now, refresh secrets, promote *into* it. |
| **maintain** | Change how an app is set up: variables and secrets, domains, scaling, placement, backups and restores, schedules, Git access, the webhook and deploy tokens, Compose deploys. Stop and delete an app. Run a command and open a terminal. Make an app (or copy one in), and change the project's settings and its environments. |

A few rules that follow:

- A member sees only the places where their level is at least view. Lists (apps, projects, recent deployments, what
  needs you, previews, schedules, domains) show those and nothing else; an app they can't see is "not found", the same
  whether or not it exists.
- **Deploy is images only.** A person at deploy may deploy a body that differs from the stored app only in the
  services' `image`; anything else (an env value, a port, a domain) is `403 not_allowed` naming the first field. This is
  what the CI action and MCP's `deploy_image` send, so a deploy-level token works for them.
- **Previews** (`pr-<n>` environments) take the project role, never an override. Because CI opens and defines them with
  a deploy token, deploy is enough to open, define and close one.
- **Run a command and the terminal need maintain**: a command can print any secret. No role can read a secret's value.
- The installation's pages are admin only: Operator view, servers, host, updates, the activity feed, Registries,
  Builds, Secret providers, Backup storage, Notifications, Certificates, DNS providers, the image scanner, console
  address, templates catalog settings, creating or deleting a project, and People.
- A role may have an **end date** (`endsAt`): after it the role counts as none, and the person stays. A person may have
  one too: after it they can't sign in and their tokens stop working. That is a **guest**.

## Inviting someone

**Settings › People › Invite someone** (or `POST /v1/people`): an email, the role, an optional end date and, for a member,
a role per project with an optional override per environment. Owners may pick owner, admin or member; admins only member.

Caravana sends no mail. The answer shows an **invite link** once (`/invite?token=cvin_…`, valid for seven days): copy it
and send it yourself. Caravana keeps only a hash, so the link can't be shown again; **New invite link** on the person's
page (or `POST /v1/people/{id}/invite`) makes a new one and voids the old. The people list shows an invited person as
"Invited".

The link opens a page with no sign-in: the person sees their email, chooses a password (12 characters or more, typed
twice) and is signed in. An API client uses `POST /v1/invites/accept` with the token and the password, and signs in
afterwards. A link that is unknown, expired or used says "This invite link isn't valid any more. Ask for a new one." A
wrong link counts against the address like a wrong password: after five in 15 minutes the address is refused.

## Tokens

An API token **acts as its person**: the same role, checked at every request, so a changed role or a removal applies to
the very next call. A person makes their own tokens (Settings › API tokens, or `POST /v1/tokens` with a session or a
token); a person's tokens are the only ones they list and revoke. A **deploy token** (one app of one project, from 020)
needs its person to hold deploy or more on some environment of that project, and on every call it is held to both the
scope and the person's role. `GET /v1/me` says who a credential is.

## Removing someone

**Remove…** on a person's page, or `DELETE /v1/people/{id}`: the person, their sessions, their tokens and their passkeys
are deleted in one transaction. Their token and session are `401` on the next call. Deployments, actions, backups
and restores they made keep their **email** (`actor`) and say "by ana@example.com". The last owner can't be removed or
made an admin (`409 last_owner`): make another owner first.

## Who did what

Deployments, app actions, backups started by hand and restores record the person's email as `actor`. The console says
"by you" when it is the viewer, else "by ana@example.com", and "through the API by ci (ana@example.com)" for a token.
Records from before, and the work nobody asked for (the webhook, a schedule, autoscaling), have none and keep their
old words.

## The API and MCP

`getMe`, `listPeople`, `invitePerson`, `getPerson`, `updatePerson`, `removePerson`, `reinvitePerson` and `acceptInvite`
(public); the codes `not_allowed` (403, with the `field` that needs more), `person_not_found`, `email_taken`,
`invite_invalid`, `not_invited` and `last_owner`. A route with no rule is refused for everyone, an owner too. MCP has one
new read tool, `get_me`; everything else an agent can do is what its token's person may.

## Upgrading

Rerunning the installer is all: the one admin becomes the owner (same email, password, passkeys, sessions and tokens),
and every record is as before. Nothing to rerun on the host. Records made before have no `actor`.

On a host, `sudo caravana admin status` lists each person with their role and state, and `admin reset-password`,
`admin create-token` and `admin passkeys` take `--email` (default: the first owner).

## Not in it

Single sign-on, custom roles, teams, an audit log, per-project Nomad namespaces (the roles are Caravana's: it is the
only Nomad client), sending the invite by email, and a roles page per app. A member can't see the installation's pages
at all.
