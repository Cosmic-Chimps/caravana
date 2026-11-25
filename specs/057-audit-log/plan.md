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

### Phase A (the log itself)

- **`audit.Event` and the store's `at`.** `audit.Entry` is what the service notes; `audit.Event` is an `Entry` with the
  `ID` and `At` the store gives it (`Event` embeds `Entry`). `store.InsertAuditEvent` takes the time as an argument: the
  scope writes the time of its **first note**, a note outside a scope the service clock's now. The API's JSON is
  `auditEventJSON` in `internal/api/audit.go` (the API speaks `encoding/json/v2`), reusing the small audit types.
- **Diff details D5/D6 left open.** The root object is always compared field by field (a creation lists its top-level
  fields, not one `{…}`); an object or list that appears or goes whole is one change summarised `{…}` / `[…]` (so the
  first secret of a service shows `services[web].secrets added […]`, a second one `secrets[B] added {…}`); a value
  under a denylisted key that is itself an object or list is one redacted change; an empty object or list counts as
  absent; an empty string is shown `""` so a missing `from`/`to` always means "not shown"; the 200 bytes include the
  ellipsis; scalar lists compare by index. Added: `MaxChanges` = 100 per entry, a last change `…` counts the rest, so one
  entry stays small whatever a request carried.
- **`WithAuditScope` is unbound; `Service.BeginAudit` binds it.** `Finish` needs a writer to write a fallback entry that no
  method noted, so the API and the console open their scopes with `s.svc.BeginAudit(...)` (which is `WithAuditScope` plus
  the service's writer). `AuditScope.SetFallback(resource)` is how `authenticate` hands the scope the resource its
  route's access rule read (`accessAllows` now returns the `Target`); `Finish(status, fallback)` uses its argument when
  it has a kind, else the stored one, else `installation`. The webhook route (public, no rule) sets its own from `{id}`.
- **Actor and door outside a scope.** A context with a principal and no scope or door (a GET stream such as the terminal)
  counts as door `api`; with neither it is `caravana`. `service.AnonymousActor(typed)` builds the actor of a failed
  sign-in (D4's `net/mail` rule), for Phase C.
- **A note after `Finish`** (a goroutine that outlived the request) is written at once with the scope's door and actor.
- **Exceptions (D12) gain `previewPromotion`** (`POST /v1/apps/{id}/promote/preview`), another read sent as POST. The
  production table is `auditExempt` in `internal/api/audit.go`; the test keeps its own `auditExceptions`, and a test fails
  when they differ.
- **Export.** The count is checked before the first byte (`ErrAuditExportTooLarge`, then 422 on `from`); the headers are
  set before the first row and taken back for a refusal. An error after rows were written can only end the file with the
  error's JSON, since the status is gone.
- **Prune.** `SetAuditPruneGap(first, every)`; the test helper sets the first gap to an hour so no pass runs under a test.
- **Retention note.** `SetAuditSettings` notes `updated` on resource `settings`/`audit` with the diff of
  `{retentionDays}`; a refused value (field error on `retentionDays`) notes nothing.
- No change to numbering: migration 0044, API 0.61.0.

### Phase B (apps and what hangs off them)

- **Every deploy is noted in one place.** `deployWarned` notes right after the deployment record exists (`noteDeploy`),
  so `Deploy`, `Redeploy`, the webhook, a Compose deploy, `RedeployVersion`, `Promote`, `RefreshSecrets`, `CreateApp`
  and a template install share it. `made` gained `action`, `diff` and `before`: `saveAndDeploy` sets `diff` and the
  stored app it replaced; a new app's action is `created`, a plain deploy's `deployed`, and the callers that mean
  something else say so (`redeployed` with no diff, `rolled back`, `promoted`, `refreshed secrets`). If the app was
  stored but the deployment record could not be made, the entry is written anyway, with no record to link.
- **Configure's own methods say `updated`.** The secret edits, `SetDomainsIf` and the backup policy, restore schedule and
  places methods are deploys of one change: action `updated`, with the deployment as the record. Placement, scaling
  and a service's build settings reach the service as a plain `DeployIf`, so they read `deployed`, and the diff says
  what moved.
- **The diff's views** (`internal/service/audit_views.go`): the stored app and the deployed app both give every typed
  secret the value `\x00stored` (the typed one `\x00stored+`), so a replaced secret is `changed` with no value and a kept
  one is no change at all; references (`from`, `template`) compare as they are. The helper serves every app path.
- **Resources.** A backup action's resource is the backup (`backup`, ID the backup's, name the service, the app's place);
  a restore is noted on the backup it restored, linking the restore record. A pin lists the backup that lost the
  label as `related`. Run now and Stop run are on the `schedule` (`ran a command`, `stopped`), linking the run.
  A Team-only password rotation and a domain check (`checked`: a POST that updates what the Domains page shows, so
  it would otherwise be a fallback entry) are on the `domain` (ID the host). Git access is `git_access` with the ID
  `<app>/<service>`; its diff shows the kind and user name and the key or token only as `set`. A webhook token is
  `webhook_token` (`created`, or `rotated` when it replaced one). Preview settings are on the project
  (`updated`, limit, days and `password` as set or changed), not on `settings`/`previews`, so the project filter
  finds them. Projects are `<project>`, environments `<project>/<environment>`.
- **What the log keeps of a command.** A run in a copy and a schedule note the program and the number of arguments, never
  an argument (it may be a secret someone typed): `program` is the first word, `arguments` a count.
- **Closing a preview is one entry** on the environment (`deleted`), listing the apps it deleted as `related`
  (`DeleteApp` and `DeleteEnvironment` split into noting and quiet halves so `ClosePreview` doesn't write one entry
  per app). The reaper's close has no principal, so its actor is Caravana; so is a scheduled restore (noted in the
  restore clock). A backup, a schedule run, an autoscale step and the watchers note nothing.
- **`AuditScope.NoChange()`.** A request that succeeds and changes nothing (a webhook whose images are the live ones, a
  push to another branch, a refresh with nothing new, an unpin of an unpinned backup, a pause of a paused schedule, a
  delete of a token there isn't) calls it through `s.noChange(ctx)`, so `Finish` writes no fallback entry for it.
- **Deploy tokens are noted where they are made**, in `Auth.CreateScopedAPIToken`, only when it has a scope (`token`,
  placed in its project, with `scope` as `<project>/<app>`; the token itself never). A full token's entry is phase C's.
- **Phase C's, still unnoted.** A template install is a Compose deploy, so for now it reads `created`/`deployed` on the app.

