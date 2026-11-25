# Implementation Plan: Audit log

**Spec**: [spec.md](./spec.md) · **Tasks**: [tasks.md](./tasks.md) · **API**: [contracts/api.md](./contracts/api.md)

## Summary

A pure package `internal/audit` (the entry's shape, the diff with its redaction, the CSV writer), migration
`0044_audit.sql` (`audit_events`, `audit_settings`), a recorder in `internal/service` that every mutating
service method calls **after** its change succeeded, and a per-request scope that the API and the console open
for write requests so one request is one entry. API **0.61.0**: `listAuditEvents`, `exportAuditEvents`,
`getAuditSettings`, `putAuditSettings`. Console: Settings › Audit log. No new module, no policy change, no Nomad
change.

Numbering: 034 (BYO certificates, unmerged on another branch) also took migration 0044 and API 0.61.0; whichever
merges second renumbers, as 049 did.

## Constitution Check

- **I Small**: a table and a goroutine pass; no service, no module. ✓
- **II One model**: untouched; the diff reads the stored app document as it is. ✓
- **III Safe deploys**: untouched; the recorder never fails a change (a failed write of an entry is logged). ✓
- **IV Fail loudly**: the filters refuse a bad value naming the field (422 `invalid_field`). ✓
- **V API-first**: entries are written by the service, so console and API changes are recorded alike; the
  reads are in `api/openapi.yaml` 0.61.0 in the same change. ✓
- **VI Nomad-native**: nothing of Nomad. ✓
- **VII Secure**: secrets, tokens, passwords, keys and env values never reach an entry (redaction by the
  service's views and by the diff's denylist, sentinel tests). The log is readable by owners and admins only. ✓
- **VIII Plain Go**: a pure diff with table tests; the route walk. ✓

## Decisions

- **D1 The service notes, after the change.** `s.audit(ctx, audit.Entry)` (and `a.audit` in `Auth`) is called by
  each public service method that changes state, once the change is stored. A method that fails before
  changing anything notes nothing, so no status gating is needed and the console (which answers form errors
  with 200) needs none either.
- **D2 One request, one entry: the scope.** The API (for every non-GET route, public ones included) and the
  console (for every POST) put an `*service.AuditScope` in the request context (`service.WithAuditScope`) with
  the door, the client address and the operation (`METHOD /pattern`). A note inside a scope is collected; the
  first note is the entry, later notes about the same resource add their changes, later notes about other
  resources are listed in `related` (kind, ID, action). `scope.Finish` writes the entry when the handler
  returns. Outside a scope (a watcher, the reaper, the CLI, a GET such as the terminal) a note is written at
  once.
- **D3 The API's fallback makes "exactly one" structural.** When an API write request ends with a status
  below 400 and nothing noted, `Finish` writes a fallback entry: action `changed`, the operation, the actor the
  scope was told by `authenticate`, the resource from the route's access rule's `Target` (app, environment,
  project, or `installation`), `fallback = 1`, and logs a warning naming the operation. The console has no
  fallback (it has no route-to-resource rule); its coverage comes from the service notes. Tests fail on a
  fallback entry where a note is expected.
- **D4 Actor.** From the context: a `Principal` with a token is kind `token` (`deploy_token` with a scope), its
  person's email and the token name; without a token, `person`. No principal: the scope's door says `webhook`
  (actor is the app's webhook, name = app ID), `host` (the CLI marks its context with
  `service.WithAuditDoor(ctx, "host")`), else `caravana`. A sign-in failure is `anonymous` with the typed email
  only when it parses as one address of at most 254 bytes (`net/mail`), else none. MCP is a door: the API sees
  the `User-Agent` `caravana-mcp/…`.
- **D5 Diff.** `audit.Diff(before, after any) []Change` marshals both to JSON and walks them. Lists of objects
  are compared by key: the first of `name`, `host`, `id`, `label`, `path` that every item has; otherwise by
  index. Paths read `services[web].domains[new.example.com].path`: an object's field after a dot, a list's
  item by its key (or index) in brackets. Values are strings cut at 200 bytes; objects added or
  removed whole show as one change with a short summary (`{…}`), not every leaf.
- **D6 Redaction, two layers.** (a) `audit.Diff` compares raw values, then redacts any leaf whose key,
  case-insensitively, is or ends with one of `password`, `token`, `secret`, `secretaccesskey`, `privatekey`,
  `apikey`, `signingsecret`, `hash`, `value`: a redacted change shows `set` (added), `changed` or `removed`,
  never `from`/`to`. That covers secret and env values (`app.EnvVar` and `app.Secret` are `{name, value}`).
  An item added or removed whole (a new secret) shows only its key, never its leaves. (b) The service builds
  what it diffs: the "before" app view gives every stored secret the value `stored`, so a request that sends a
  value for an existing secret shows `…secrets[DB_PASSWORD].value: changed`; settings with secrets
  (registries, destinations, channels, providers, DNS zones, Git access) diff their stored rows, which hold
  no secret, plus a redacted `secret` leaf when the request carried one. Tests put sentinels in every such field.
- **D7 Actions.** A closed list in `audit`: `created`, `updated`, `deleted`, `deployed`, `redeployed`,
  `rolled back`, `promoted`, `copied`, `stopped`, `started`, `restarted`, `ran a command`, `opened a terminal`,
  `backed up`, `restored`, `pinned`, `unpinned`, `tested`, `rotated`, `revoked`, `invited`, `removed`,
  `accepted an invite`, `signed in`, `signed out`, `sign-in failed`, `requested`, `cancelled`, `checked`,
  `installed`, `uninstalled`, `refreshed secrets`, `changed` (fallback). A note with another action is a
  programming error caught by a test over every `audit.Action` constant used.
- **D8 Resource kinds.** `app`, `project`, `environment`, `schedule`, `backup`, `domain`, `token`,
  `webhook_token`, `registry`, `backup_destination`, `notification_channel`, `dns_zone`, `secret_provider`,
  `person`, `passkey`, `account`, `server`, `volume`, `addon`, `git_access`, `console_address`, `settings`
  (the name says which: `certificates`, `updates`, `templates`, `builds`, `cleanup`, `scans`, `previews`,
  `audit`, `sign-in`), `installation` (fallback with no target).
- **D9 Links.** `record_kind` (`deployment`, `app_action`, `backup`, `restore`, `schedule_run`, `update_run`,
  `cleanup`, `volume_deletion`, `console_address_change`) and `record_id`, text with no foreign key, so a record
  deleted with its app leaves the entry (the console then shows the ID without a link).
- **D10 Store.** `audit_events` has no foreign key (entries outlive apps and people). `store.InsertAuditEvent`,
  `ListAuditEvents(filter)` (newest first, `before` cursor on the integer ID, limit ≤ 500), `ExportAuditEvents`
  (a callback per row, oldest first, at most 100 000 rows), `PruneAuditEvents(before time)`,
  `AuditSettings`/`SetAuditSettings`. No update or delete of a single entry exists.
- **D11 Prune.** `runAuditPrune` in `Service.Start`: once a minute after start, then every 24 hours, deletes
  entries older than the retention. `SetAuditPruneGap` for tests.
- **D12 Exceptions to "every write route".** `syncNode` and `renewNodeCertificate` (the agent's routine
  calls, every 30 s), `previewCompose`-style reads sent as POST (`previewCompose`, `previewTemplate`), the
  acceptance of an invite is noted as `accepted an invite` by the person. The walk keeps the exceptions in a
  named set with a reason each.
- **D13 Who reads.** Owners and admins read and export; owners change the retention. Members get 403
  `not_allowed`; deploy tokens 403 `token_scope`.
- **D14 Clock-driven runs are not entries.** A scheduled backup, a schedule's run, an autoscale step, the domain
  watcher, the sampler and the cleanup pass write their own records and note nothing. Caravana itself is the
  actor of what it decides on the owner's behalf: closing an expired preview, an automatic Caravana update
  request, a scheduled restore, a daily automatic clean-up.

## Files

- `internal/audit/` (new, pure: no `net/http`, no `store`, no `service`): `audit.go` (Entry, Actor, Resource,
  Change, Record, Related, Action and Kind constants), `diff.go`, `csv.go`, tests.
- `internal/store/migrations/0044_audit.sql`, `internal/store/audit.go`.
- `internal/service/audit.go` (scope, recorder, actor, prune, settings, list, export), `audit_views.go` (app
  audit view and other views), notes in every mutating method.
- `internal/api/audit.go` (routes, filters), the scope in `handler()`, `authenticate` telling the scope the
  actor, access rules, `api/openapi.yaml` 0.61.0.
- `internal/ui/audit.go`, `templates/audit.html`, the scope in `routes()`, nav entry, console access rules.
- `docs/audit-log.md`, `changelog.d/057-audit-log.md`, `CLAUDE.md`.

## Deviations

(Filled in as the phases land.)
