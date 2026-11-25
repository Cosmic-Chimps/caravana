# Feature 057: Audit log

From CodeAlta task `jf6s` ("24 · Audit log: who did what, kept 90 days"), raised by the maintainer on 2026-11-12.
**Plan**: [plan.md](./plan.md) · **Tasks**: [tasks.md](./tasks.md) · **API contract**: [contracts/api.md](./contracts/api.md)

## Why

Since 054 more than one person signs in, and the owner needs to know who did what: who deleted the app, who
changed the domain, who rotated the token. Caravana records some of it in feature-specific places (a
deployment's trigger, token name, actor and origin; app actions; backup and restore runs; the Operator view's
Activity), but not changes to settings, secrets, domains, tokens, registries, notifications or people, and not
in one place.

## User stories

### US1 (P1) — Every change leaves one entry

As an owner, every change made in Caravana, whoever made it and through whichever door (console, API, MCP, a
webhook, CI with a deploy token, Caravana itself), leaves one entry in one log: when, who, what was done, to
what, and what changed.

- Given a member deploys a new image to `shop › staging` through the API with their token, the log has one
  entry: actor `member@example.com` through the token `ci`, action `deployed`, resource app
  `shop--staging--web` in `shop › staging`, the change of `services.web.image`, and a link to the deployment.
- Given an admin changes an app's domain from `old.example.com` to `new.example.com`, the entry's changes show
  `old.example.com` removed and `new.example.com` added.
- Given someone sets a secret's value, the entry shows the secret's name with `changed` (or `set` when it is
  new), never the value.
- Given a webhook redeploys an app, the actor is the webhook of that app.
- Given Caravana closes an expired preview by itself, the actor is Caravana.
- Given someone types a wrong password at `/login`, the log has one `sign-in failed` entry with the address
  it came from; a correct one leaves `signed in`, and signing out `signed out`.
- Given a write request is refused (a field error, a role refusal, a conflict), no entry is left: nothing changed.

### US2 (P1) — Nothing secret in the log

No entry holds a secret value, a token, a password, a private key, an access key's secret, a webhook URL that
is itself a secret (Slack, Discord) or a plain env value: each shows as `set`, `changed` or `removed`.
Sentinel tests search every entry, answer, page, CSV and the SQLite file.

### US3 (P2) — Read the log

As an owner or an admin, Settings › Audit log lists the entries newest first, filtered by actor, action,
resource kind, resource, project › environment and a time range, each with its changes; a range exports as CSV.
`listAuditEvents` gives the same over the API; members and deploy tokens are refused.

### US4 (P2) — Kept 90 days, never edited

Entries are kept 90 days by default and pruned daily; an owner may set the retention between 30 and 3650 days
(the change is itself an entry). No route of the console or the API edits or deletes an entry.

### US5 (P3) — Records link, not copy

An entry about a deployment, an app action, a backup or a restore links to that record (kind and ID) instead
of copying it; the console's entry links to the record's page while it exists.

## Requirements

- **FR-001** One table `audit_events`, written only by the service layer (`internal/service`), never by a
  handler.
- **FR-002** An entry has: time; actor (kind `person`, `token`, `deploy_token`, `webhook`, `caravana`, `host`,
  `anonymous`; the person's email; the token's name; the person's ID); the door (`console`, `api`, `mcp`,
  `webhook`, `caravana`, `host`); the client address when there was a request; the action (a past-tense verb
  from a closed list); the operation (the route, `METHOD /path`); the resource (kind, ID, name, project,
  environment); the changes; an optional link to a record; `related` resources the same request touched.
- **FR-003** Exactly one entry per successful write request of the API (any non-GET route but the listed
  exceptions), and per console form post that changed something.
- **FR-004** A refused or failed request that changed nothing leaves no entry. A request that changed
  something and then failed (a deploy whose rollout is refused after the app was saved) leaves its entry.
- **FR-005** Changes are field-level (`path`, `op` added/removed/changed, `from`, `to`), lists of named items
  (services, domains, env, secrets, ports, volumes) compared by name, values cut to 200 characters; secret
  material shown only as `set` / `changed` / `removed`.
- **FR-006** `listAuditEvents`, `exportAuditEvents` (CSV), `getAuditSettings` for owners and admins;
  `putAuditSettings` for owners. Deploy tokens are denied (default deny).
- **FR-007** Retention 90 days by default, 30–3650, pruned at start and every 24 hours.
- **FR-008** No route edits or deletes an entry; deleting an app, a person or a project keeps their entries.

## Not in 057

Entries for reads; a per-app audit tab; an MCP tool; streaming the log to an outside system (syslog, SIEM);
signing or hash-chaining entries; entries for the agent's routine calls (`syncNode`, certificate renewals);
entries for clock-driven runs (scheduled backups, schedule runs, autoscale steps), which keep their own records.
