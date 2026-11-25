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

### Phase C (the installation and accounts)

- **Secrets in settings** (`audit_installation.go`): each setting is diffed as a map of what SQLite stores plus one redacted leaf per
  secret, built by `secretIn` (`stored` on both sides when the setting has one, `stored+` on the after side when the request carried
  one: `set` for a first secret, `changed` for a replaced one, nothing for a kept one). The leaf keys are the ones `audit.Redacted`
  hides: registry `password`; destination `secretAccessKey`; provider `apiKey` and `privateKey`; DNS zone `token`; channel `secret` (a
  Slack, Discord or ntfy address, or a webhook's signing secret), `token` (ntfy, Telegram) and `password` (SMTP). A channel's address is
  shown as the console shows it (masked where it is the secret). Saving what a setting already is notes nothing (`noChange`).
- **Actions and resources.** Server: a join command is `created`, the join arriving (`Join`, a request, not a sync) is `updated`
  with `state waiting → joining` (no principal, so the actor is Caravana), labels `updated` with the labels' diff, Move work off
  `requested` and Accept work again `updated` (`acceptsWork`), Remove `removed`, Clean up now `requested` with the cleanup as the record
  (the daily automatic one is the same note with no principal: Caravana), `caravana server adopt-first` `created` or `rotated` (noted by
  `AdoptFirstServer` through a writer on the CLI's own store). An orphan volume deletion is `deleted` on the `volume` (`requested`
  when it archives first) with the deletion as the record. Updates are on `settings/updates`: settings `updated`, Check now `checked`,
  an update request `requested` with the run as the record (the automatic one has no principal: Caravana). Templates: settings and
  Check now on `settings/templates`; an install is `installed` on the app with `template` (`<name> <version>`) as its change, through a
  context override (`withDeployNote`) that `noteDeploy` honors. Add-ons: `installed`/`uninstalled` on the `addon` (installing what is
  installed, removing what isn't: no change); a scan request `requested` on the app, or on `settings/scans` for all of them; scan,
  build and cleanup settings `updated`. Console address: `requested` (start), `cancelled`, `updated` (switch) on the
  `console_address` change, which is also the record.
- **Accounts.** A person invited `invited` (the token never), a new link `invited` with `inviteExpiresAt`, an acceptance
  `accepted an invite` by the person who accepted (with the address), an update `updated` with the role diff (`orgRole`,
  `projects[<name>].role`, `.environments.<env>`), `removed`. A token (full or deploy) `created` (name, `owner`, `expiresAt`, `scope`)
  and `revoked` (revoking twice: no change). A passkey `created` (name and domain, never a credential ID or key), `updated` (rename),
  `removed`. Sign-in settings `updated` on `settings/sign-in` (the actor says whose). A password change and the host's reset are
  `updated` on the `account` with `password: changed` and no value. Sign-in and sign-out are on the `account` with the address and the
  method; a failed sign-in is `anonymous`, with the typed value as the email only when it reads as one address, and a wrong invite
  token and a failed passkey sign-in are failed sign-ins too (no email). A locked address writes nothing (nothing was checked). The
  session an accepted invite starts (`StartSessionFor`) is not noted again: the acceptance covers it.
- **Actor inside Auth.** `actorUnlessKnown`/`actorFor` give the person as the actor of an account note only when the context has no
  principal, no API scope that was told the actor and isn't the host's CLI, so a token keeps being a token and the CLI stays the host.
- **The CLI** (`caravana admin …`, `caravana server …`) runs with `WithAuditDoor(ctx, host)`.
- **Existing tests.** Making the admin and a token now writes entries, so three tests that expected an empty log after setup count
  from after it (`TestAuthNotesLikeService`, `TestAuditSettingsEndpoints`, and the two list tests of `internal/api/audit_test.go`); no
  assertion was weakened.
- **A real-handler fallback check.** With a temporary line printing every fallback entry (removed again), the whole `internal/api`
  suite, which calls the real handlers for most routes, wrote exactly one: `PUT /v1/updates/settings` with `{}`. `PutUpdateSettings`
  now calls `noChange` when nothing is set. The routes the API tests never call are covered only by the stub walk.
- **Exported mutating methods left without a note of their own**, found by listing every exported `Service` and `Auth` method whose
  name starts with a state-changing verb and reading what it calls:
  - Notes happen in what they call: `Deploy`, `DeployIf`, `DeployIfResult`, `DeployCompose*`, `Redeploy`, `RedeployVersion`, `Promote`,
    `CreateApp` (`deployWarned`/Phase B's helpers); `SetSecret*`, `RemoveSecret*`, `SetDomainsIf`, `SetBackupPolicy`,
    `SetRestoreSchedule`, `SetBackupPlaces` (deploys of one change); `StopApp`, `StartApp` (`actions.go`'s one note);
    `SetGitToken`, `RemoveGitAccess`, `SetPreviewSettings`, `SetProjectBackupDestination` (their Phase B helper);
    `CreateAPIToken` (→ `CreateScopedAPIToken`), `RevokeAPITokenByName` (→ `RevokeAPIToken`), `SetBackupDestination`
    (→ `SaveBackupDestination`), `RequestCleanup` (→ `requestCleanup`, which the daily pass also calls), `InstallAddon` and
    `RemoveAddon` (→ the scanner's), `PutUpdateSettings` (→ `SetReleaseCheck`, `SetAutoUpdate`), `AdoptFirst` (→
    `AdoptFirstServer`), `RemovePasskey`, `RemovePasskeyUnchecked` (→ `deletePasskey`).
  - Routine calls of the agent, D12: `NodeSync`, `NodeCertificate`.
  - No stored change worth an entry: `BeginPasskeySignIn`, `BeginPasskeyConfirm`, `BeginPasskeyRegistration`, `BeginJoin` (open a
    ceremony or an attempt in memory), `VerifyPassword`, `FinishPasskeyConfirm` (only move a session's `verified_at`),
    `StartSessionFor` (above), `CheckSession`, `CheckSessionQuiet`, `CheckAPIToken` (a last-used time), `CheckWebhookToken`,
    `CheckUpdateOffer`, `MarkDomainsWatched`, `RefreshDomains` (reads and watchers), `InviteFor` (a read that counts a bad token, and
    notes a failed sign-in as `AcceptInvite` does).
  - Set up, not state: every `Set…Gap`, `Set…ForTests`, `SetEdge*`, `SetPublicURL`, `SetVersion`, `SetAuth`, `SetDNSProviders`,
    `SetSecretSources`, `Set…URL`, `SetProRunner`, `SetAptRunner`, `SetHostRoot`, `SetDockerRoot`, `SetCertResolver`, `SetACME`,
    `SetUpdaterDir`, `SetUpdateRequestPath`, `SetTerminalLimits`, `Start`; the readers `Addons`, `Addon`, `Updates`,
    `CleanupSettings`, `ScanSettings`, `ScanRuns`, `ScansOf`, `DeploymentScan`, `ScanWait`, `UpdaterInstalled`, and the test hooks
    `RunDailyScansForTests`, `DropFindingForTests`.

