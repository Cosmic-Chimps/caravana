# Audit log

Caravana keeps one log of **who changed what**: when, who (a person, a token, a webhook, Caravana itself), through
which door, what was done, to what, and which fields changed. It answers "who deleted the app", "who changed the
domain", "who rotated the token". Owners and admins read it in **Settings › Audit log** and over the API; it is kept 90
days by default.

## What is recorded

Every change leaves **one entry**, whether it came from the console, the API, the MCP server, a webhook, CI with a deploy
token, the host's command line or Caravana itself. The service layer writes the entry once the change is stored, so the
console and the API are recorded alike. A request that changes several things is still one entry: the first thing it
changed is the entry, the changes of the same resource add to it, and anything else it touched is listed as *related*.

An entry has:

| Part | What it says |
| --- | --- |
| **When** | The time of the first change, UTC (the console adds your local time). |
| **Actor** | Who: a person (`ana@example.com`), a person through a token (`ana@example.com through token ci`), a deploy token (`deploy token ci (ana@example.com)`), the webhook of an app, Caravana, the host's command line, or someone whose sign-in failed. |
| **Door** | `console`, `api`, `mcp`, `webhook`, `caravana` (what Caravana decides itself) or `host` (`caravana admin …` on the server). |
| **Address** | The client's address, when there was a request. |
| **Action** | A past-tense verb from a fixed list: `created`, `updated`, `deleted`, `deployed`, `redeployed`, `rolled back`, `promoted`, `copied`, `stopped`, `started`, `restarted`, `ran a command`, `opened a terminal`, `backed up`, `restored`, `pinned`, `unpinned`, `tested`, `rotated`, `revoked`, `invited`, `removed`, `accepted an invite`, `signed in`, `signed out`, `sign-in failed`, `requested`, `cancelled`, `checked`, `installed`, `uninstalled`, `refreshed secrets`, and `changed` (see *Recorded by the route* below). |
| **Resource** | Its kind (`app`, `project`, `environment`, `schedule`, `backup`, `domain`, `token`, `webhook_token`, `registry`, `backup_destination`, `notification_channel`, `dns_zone`, `secret_provider`, `person`, `passkey`, `account`, `server`, `volume`, `addon`, `git_access`, `console_address`, `settings`, `installation`), its name and ID, and its project › environment. |
| **Changes** | Field by field, as `path`, then `added X`, `removed X` or `X → Y`. Lists of named things (services, domains, variables, ports, volumes) are compared by name: `services[web].domains[new.example.com]`. Values are cut at 200 characters. |
| **Record** | A link to the deployment, app action, backup, restore or run the change started, kept as a kind and an ID, not a copy. The console links to it while it exists. |

Examples: a member deploys a new image to `shop › staging` with their token: one `deployed` entry on the app, actor
`ana@example.com through token ci`, the change `services[web].image: ghcr.io/acme/web:1.4 → ghcr.io/acme/web:1.5`, and a
link to the deployment. An admin moves a domain: `services[web].domains[old.example.com]` removed and
`services[web].domains[new.example.com]` added. A wrong password at `/login`: one `sign-in failed` entry with the address
and, when what was typed reads as an email address, that address.

A request that was **refused** (a field error, a role refusal, a conflict) leaves no entry: nothing changed. One that
changed something and then failed (an app saved, then its rollout refused) keeps its entry.

## What is never kept

No entry holds a secret value, a token, a password, a private key, an access key's secret, a webhook or Slack/Discord
address that is itself a secret, or a plain environment value. A change to such a field says only **`set`**, **`changed`**
or **`removed`**: `services[web].secrets[DB_PASSWORD].value: changed`. This is done twice: the service builds what it
compares from views that never hold the values, and the diff hides any value under a key named `password`, `token`,
`secret`, `secretAccessKey`, `privateKey`, `apiKey`, `signingSecret`, `hash` or `value`. A command run in a copy or on a
schedule is recorded as its program and the number of arguments, never an argument. Tests put sentinel values in every one
of these fields and search every entry, answer, page, CSV and the SQLite file for them.

## Who can read it

- **Owners and admins** read and export the log: Settings › Audit log, `listAuditEvents`, `exportAuditEvents` and
  `getAuditSettings`. Members get `403 not_allowed`; a deploy token gets `403 token_scope`.
- **Owners** change how long entries are kept.
- Nobody edits or deletes an entry: no route of the console or the API does. Deleting an app, a person or a project keeps
  their entries (an entry stores names and IDs as text, with no link that could cascade).

The log is a table in the same SQLite file as the rest of Caravana. Someone with access to that file on the host can change
it; entries are not signed or chained.

## Reading it

Settings › Audit log lists entries newest first, 100 a page; **Older** goes back by cursor and keeps the filters. The filters
are one form: who (an email or a token name), action, kind, resource (an ID), project, environment, and a range of UTC days.
An entry's changes are under it. An entry made by the console's route and not by a note (see below) says "recorded by the
route". `GET /v1/audit` takes the same filters (`actor`, `action`, `kind`, `resource`, `project`, `environment`, `from`,
`to`, `before`, `limit`); `next` in the answer is the `before` of the next page.

## Retention and export

Entries are kept **90 days** by default; an owner sets 30 to 3650 days in the same page (or `putAuditSettings`), and that
change is itself an entry. Older entries are removed once a day, from a minute after Caravana starts.

**Export as CSV** takes a first and a last day (UTC) and answers the entries oldest first, at most 100 000 (more is refused:
narrow the range): `id, at, actor_kind, actor, token, door, address, action, operation, resource_kind, resource_id,
resource_name, project, environment, changes, record_kind, record_id`. `changes` is one cell, `path op from → to` joined by
`; `. A cell that starts with `=`, `+`, `-`, `@`, a tab or a carriage return gets a leading `'`, so a spreadsheet doesn't run
it as a formula. The API's `exportAuditEvents` is the same with `from` and `to` as RFC 3339 times.

## Recorded by the route

The API has a safety net: a write request that succeeded and that no part of the service noted still leaves an entry, action
`changed`, on the resource the route's access rule is about (the app, the environment, the project or the installation),
marked "recorded by the route", and Caravana logs a warning naming the operation so the gap gets a note of its own. The
console has **no** such entry: a form post that noted nothing (a preview, a search, a theme choice, a save that changed
nothing) leaves none.

## What isn't recorded

- **Reads.** Opening a page, listing, exporting or following logs leaves no entry. (Opening a terminal is a change: it is
  recorded, as `opened a terminal`.)
- **The agents' routine calls.** A server's agent reports every 30 seconds and renews its certificate; neither is a change.
  A server joining or being removed is.
- **Previews that only show.** The Compose, template and promotion previews change nothing.
- **Clock-driven work.** A scheduled backup, a schedule's run, an autoscale step, the domain watcher, the sampler and the
  clean-up pass keep their own records (the Backups tab, the schedule's runs, Recent actions) and add no entry. What Caravana
  decides for the owner is recorded with **Caravana** as the actor: closing an expired preview, an automatic Caravana update
  request, a scheduled restore, a daily automatic clean-up.
- **A locked address.** A sign-in from an address that is locked out writes nothing: nothing was checked.
- **Outside systems.** There is no stream to syslog or a SIEM, and no signing of entries.
