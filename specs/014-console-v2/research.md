# Research: Console v2

## R1. Refusing a stale save: the app's revision

**Decision**: an app's **revision** is the first 16 hex digits of the SHA-256 of its stored
document (the `apps.document` JSON, secret values already left out). The store computes it when
it reads an app (`AppRecord.Revision`). `getApp` sends it as an `ETag` header (`"<revision>"`)
and as a `revision` field. `deployApp` and `deployCompose` accept `If-Match: "<revision>"`: when
the stored app's revision differs, nothing is saved or deployed and the answer is
**412 `app_changed`**. The console's edit and "Deploy from Compose" forms carry the revision
they were drawn from in a hidden field and send it the same way, through the service.

The check and the save are one step: `store.SaveAppIf(ctx, a, revision)` reads the document and
writes the new one in one `BEGIN IMMEDIATE` transaction, so two saves from the same revision
can't both pass. A missing app with `If-Match` is 404 `not_found` (nothing to match).

**Rationale**: a content hash only changes when the definition changes, so a webhook redeploy or
a backup (which change nothing in the definition) never makes an open form stale, while an API
deploy or another tab's save does. `If-Match`/`ETag` is plain HTTP that generated clients
(the Aspire integration) already understand. No migration: the hash is computed from the
stored column.

**Alternatives considered**: `updatedAt` as the marker (it would need nine-digit precision in
a custom header; `If-Unmodified-Since` has one-second precision and would let two quick saves
through). A stored version counter
(a migration for what a hash gives for free). Last write wins (the spec refuses it, US1-6).

## R2. Creating only if new, for Compose

**Decision**: `deployCompose` also accepts `If-None-Match: *`: create the app only if the ID is
free. A taken ID is **409 `app_exists`**, the code `createApp` already answers, `field` `id`.
The console's New app › Compose always sends it. In the service this is
`Condition{IfNew: true}`, which saves with `store.InsertApp` (the store decides who owns the
ID, as `CreateApp` does since 007) instead of `SaveApp`.

**Rationale**: New app must never replace an existing app (US2-4), and the API needs the same
guarantee for parity. RFC 9110 says 412 for a failed `If-None-Match` on a write, but the
contract already has 409 `app_exists` for this exact case on `createApp`; one code for one
meaning is easier for clients than following the RFC to the letter.

**Alternatives considered**: a new `POST /v1/apps` variant taking Compose (two ways to send the
same file). A preview-time "is the name free" check only (a race between two tabs).

## R3. One condition type through the service

**Decision**: `service.Condition{IfRevision string; IfNew bool}`; the zero value means no
condition. `saveAndDeploy` takes it and calls `SaveAppIf` / `InsertApp` / `SaveApp`
accordingly; new thin methods `DeployIf(ctx, a, cond, trigger)` and
`DeployComposeIf(ctx, id, req, cond, trigger)` expose it, and the existing `Deploy` and
`DeployCompose` stay as they are (zero condition), so the webhook, secrets edits and tests
don't change. The errors are `service.ErrAppChanged` (new) and `service.ErrAppExists`
(existing). `saveAndDeploy` runs the secret and backup checks before saving for every path,
including create, which `CreateApp` skipped; a Compose create needs them (a secret with no
value on a new app is refused naming it, FR-011).

**Rationale**: one place decides how an app is saved; three call sites differ only in the
condition. (C#: an options record with defaults, passed instead of overloads.)

## R4. The edit form: what it sends, and the merge

**Decision**: the form is one section per service with plain inputs, so it works without
JavaScript:

| Field | Input | Becomes |
| --- | --- | --- |
| Image | text | `Service.Image` |
| Environment | textarea, one `NAME=value` per line; blank lines and lines starting with `#` ignored | `Service.Env`, whole list |
| Container port | number, shown only when the service has exactly one port | `Ports[0].ContainerPort` |
| Domains | textarea, one host per line | `Service.Domains`, each on the domains' port (the only port) |
| Copies | number | `Service.Replicas` |
| Health check | path text (empty = TCP), interval and timeout in seconds | `Service.Health` |
| CPU | number, MHz | `Resources.CPUMHz` (empty = default) |
| Memory reserved / ceiling | numbers, MB | `Resources.MemoryMB` / `MemoryMaxMB` (empty = default) |

The handler starts from the stored app at the revision the form was drawn from, replaces only
these fields per service (by service name), and sends the result through `DeployIf` with that
revision. Command, args, secrets (names only, so values are kept), volumes, backup and any
extra ports pass through untouched. The merge is a pure function in `internal/ui`
(`applyEdit(stored app.App, form editForm) (app.App, []fieldErr)`) with a table test that
includes every example app the project ships (SC-005).

Field errors from parsing (a `NAME=value` line without `=`, a number that isn't one) carry the
form field's name; errors from `Validate` carry the model path (`services[1].resources.memoryMB`),
which the handler maps back to the form field of that service, so the message sits at the
input (FR-005). An error whose path maps to nothing the form shows goes to the form's notice,
naming the path.

**Rationale**: textareas are how a no-JS form edits a list, and `NAME=value` is what users paste
from `.env` files. Merging on the server from the stored revision is what keeps fields the form
doesn't show (FR-003).

**Alternatives considered**: one input row per env var with "add row" buttons (needs JS or a
round trip per row). A JSON textarea of the whole app (that is the API, not a console). A
service-level "patch" operation in the API (clients can already GET, change, and deploy with
`If-Match`; a patch format would be a second way to say the same thing).

Copies on a service with a volume: `Validate` already refuses more than one (009); the error
lands on the copies input. A Compose-made app shows a one-line note above the form: "Made from
a Compose file. Deploying a file again replaces these edits."

## R5. CPU in MHz

**Decision**: CPU is shown and edited in MHz, Nomad's unit and the model's (`cpuMHz`). Usage is
shown as MHz used of MHz reserved.

**Rationale**: "cores" needs the node's MHz per core, which differs per machine (and will differ
per node in Phase 3); MHz is exact and is what the definition stores. The design's "0.25 cores"
is a later polish once node facts are read.

## R6. Usage per copy: where it comes from

**Decision**: `deploy.Deployer.Usage(ctx, allocIDs []string) map[string]CopyUsage` calls Nomad's
`Allocations().Stats` once per running copy, at most 4 at a time, each with a 2 s timeout. For
each copy it reads the **service's task** (named like the service, translate.go), not the
allocation total, so the `names` prestart sidecar doesn't count against the service's own
numbers:

- CPU used: `CpuStats.TotalTicks` (MHz).
- Memory used: `MemoryStats.Usage` when Nomad measured it, else `RSS` (cgroups v2 reports
  `Usage`; v1 reports `RSS`). `Measured` says which.

A copy that isn't running gets no request; a failed request gives that copy a reason
("Nomad didn't answer for this copy") and the others are unaffected (FR-016). The reservation
and ceiling come from the stored app (the same defaults `Translate` applies, via the deployer's
settings), not from the allocation, so the page says the numbers the owner set.

Nomad's ACL for `/v1/client/allocation/:id/stats` is `read-job`, which Caravana's scoped policy
already has; the lab runs on that policy (010), so a missing capability fails `TestLab`.

**Rationale**: the stats endpoint is the live number Nomad has; nothing is stored (spec
Assumptions). Per-copy requests are fine at single-node scale (a handful of copies).

**Alternatives considered**: Nomad's telemetry / Prometheus endpoint (a runtime dependency, and
history is out of scope). Node-level stats (not per copy).

## R7. Usage in the API and the live refresh

**Decision**: `listCopies` gains `?usage=true`; with it each running copy has a `usage` object
(`cpuMHz`, `memoryMB`, and the service's `cpuReservedMHz`, `memoryReservedMB`,
`memoryCeilingMB`, `nearCeiling`), or `usageError` (a sentence). Without the parameter the answer
is as today, so the cheap list stays cheap.

The Running tab draws usage in the copies region, which `/parts/apps/{id}/copies` serves. The
app page's live route (`/live/apps/{id}`) gets a `Start` hook (the same hook the Logs tab uses)
that, only while the Running tab is the open tab, sends a `usage` event every 5 s; the copies
region refetches on `sse:copies` or `sse:usage`. When no one has the tab open, nothing reads
Nomad (FR-015). 5 s keeps SC-004 (never more than 10 s old) with room for one missed tick.

`nearCeiling` is `memoryMB >= 0.9 × memoryCeilingMB` (FR-014), computed in the service so the
API and the console agree.

**Alternatives considered**: a service-level ticker publishing `ChangeCopies` (it would run for
every subscriber, including the Apps page). Pushing the numbers in the SSE event itself (the
pattern is notices only, regions fetch HTML; keeps no-JS rendering the same code).

## R8. Out of memory and restarts on a copy

**Decision**: `deploy.Copy` gains `LastRestart *time.Time` (the newest `TaskState.LastRestart`
of its tasks) and `OutOfMemory bool`: true when the service task's newest `Terminated` event
has `Details["oom_killed"] == "true"` (what the Docker driver records). Both come from the
allocation list Nomad already returns (`AllocationListStub.TaskStates` carries the events), so
no extra request. The API's `Copy` gains `lastRestartAt` and `outOfMemory`; the Running tab
shows "ran out of memory" on such a copy.

**Verified in the lab** (`TestConsoleLab`, step `console-needs-you`, Nomad 2.x with the Docker
driver, 2026-10-13): a busybox service with `memoryMB: 16`, `memoryMaxMB: 24` running
`head -c 64m /dev/zero | tail` is killed by the kernel, and within seconds the allocation *list*
(`GET /v1/allocations`, which is all `AllCopies` reads) carries the service task's events with
`Details["oom_killed"] == "true"` on the `Terminated` event, so the API's `listCopies` says
`outOfMemory: true` and `listAttention` has the `out_of_memory` item. No fallback is needed and no
extra request is made. (If a future Nomad drops events from the stub, the fallback is the event's
`DisplayMessage` "OOM Killed" from `Allocations().Info`, for copies with restarts only.)

A copy that has been killed for memory and then failed (Nomad's default: two restarts, then the
copy fails and is replaced after a delay) still gives the item for an hour (R9).

## R9. The "needs you" list: derived, not stored

**Decision**: `service.Attention(ctx) ([]AttentionItem, error)` builds the list on every call
from what is already read for the Apps page, plus backups:

1. `store.ListApps`, `store.LatestDeployments` (one query each, as `ListApps` does).
2. `deployer.AllCopies` (one Nomad request for all apps).
3. `store.LatestBackupRuns(ctx)` (new, one query): per app and service, the newest backup and
   the newest restore.
4. `store.GetBackupDestination` once.

Rules, per app, in this order (data-model "Attention item"):

- **deploy_failed**: the latest deployment is `failed` or `reverted`. When: its `UpdatedAt`.
  Action: the deployment on the Deployments tab (redeploy the last healthy version from there).
  This item replaces copy items for that app's copies of the failed version (one problem, one
  item, US5-5).
- **out_of_memory**: a considered copy with `OutOfMemory`. When: its `LastRestart` or
  `FinishedAt`, the newer. Action: Configure, the service's memory field
  (`#svc-<service>-memory`).
- **restarting**: in a service, over considered copies not out of memory, the restarts plus one
  per failed copy reach 3. When: the newest `LastRestart` or `FinishedAt` among them. Action: the
  Logs tab for one of those copies.

  Considered copies are those that haven't ended and those that *failed* within the last hour
  (`FinishedAt`); stopped copies never count. Caravana sends no restart or reschedule policy, so
  Nomad's service defaults apply: two restarts in 30 minutes, then the copy fails ("fail" mode)
  and a replacement is scheduled after a delay that grows from 30 seconds to an hour. A crash
  loop is therefore failed copies and fresh replacements, never one copy with three restarts, and
  ignoring terminal copies would hide it exactly while the replacement waits.
- **unhealthy**: a non-terminal copy `unhealthy` while no rollout of the app runs. When: now
  isn't known, so its `StartedAt`. Action: the Logs tab for that copy.
- **backup_failed** / **restore_failed**: the newest backup (or restore) of a service failed.
  When: its finish time. Action: the Backups tab.
- **no_backup_destination**: some service asks for backups and no destination is set; one item
  for the installation, not per app. Action: Settings › Backup destination.

Copy items are merged per service and kind ("2 copies of web keep restarting"). Items are sorted
newest first. Nomad unreadable: the store-based items (deploys, backups, destination) still
show, and the list carries a note that copies couldn't be checked (spec edge case); the API
answers 200 with `copiesUnknown: true`.

**Rationale**: everything is already known; storing items would need acknowledgement and
cleanup, which the spec puts out of scope (items vanish when the condition does, FR-020).

**Alternatives considered**: an `attention` table filled by watchers (state that can disagree
with Nomad, and needs expiry). Folding it into `listApps` (a different shape for a different
question; Aspire's client wants the list on its own).

## R10. Live updates for the list

**Decision**: a new change kind `attention`, published (to all subscribers, `AppID` "") by
`deploymentChanged`, `copiesChanged`, `backupsChanged` and `DeleteApp`. The Apps page's live
route adds it, and the needs-you region (`/parts/attention`) refetches on `sse:attention`. The
copies watcher already runs while the Apps page is open, so a restart or an unhealthy copy
reaches the page within the coalesce interval, well inside SC-003's 30 s.

## R11. The Compose forms (New app and Deploy from Compose)

**Decision**: two server-rendered steps, no JavaScript needed:

1. **Preview** (`POST /apps/new/compose/preview`, `POST /apps/{id}/compose/preview`): name (new
   only) and the file in a textarea. The handler calls `PreviewCompose`; the page comes back with
   the file still in the textarea, the services table (image, ports, volumes, memory, copies),
   the warnings and refusals with their Compose paths, and, when there are no refusals, the
   overlay fields per service:
   - services with ports: domains (textarea, one host per line), health path, interval, timeout;
   - every secret name the file declares and every secret the app already has: a password input,
     always empty; for an existing secret the hint says "empty keeps the current value";
   - backup: engine (none or postgres), schedule, keep, shown for services whose image looks
     like Postgres or that already have a backup.

   For "Deploy from Compose" the overlay starts from the stored app (domains, health, secret
   names, backup) and the preview lists services added and removed against the stored app.
2. **Deploy** (`POST /apps/new/compose`, `POST /apps/{id}/compose`): the same fields; the handler
   builds `compose.Request` and calls `DeployComposeIf` with `IfNew` (new) or the revision
   (existing), then redirects to the deployment's progress.

The overlay includes backup, which the spec's FR-009 didn't list: a Compose deploy replaces the
whole app, so leaving backup out of the form would drop an existing backup on every redeploy
(Constitution IV). Changing the file after preview and pressing deploy is allowed; the deploy
runs the same translation and refuses the same way, so a stale preview can't sneak anything
through.

Secret values are never written back into a page (FR-012): after a failed deploy the password
inputs come back empty with the notice "Secret values aren't kept; type them again." The file
body limit is the API's (1 MiB); the form handler sets `MaxBytesReader` to 1 MiB plus room for
the overlay, and a larger file is an error at the file field.

## R12. Where things go in the UI

**Decision**:

- New app gets two tabs at the top: "Image" (today's form, unchanged) and "Compose file"
  (`/apps/new?from=compose`). Plain links, so no JS.
- Configure: the read-only definition panels become the edit form (one panel per service, the
  app's own panel keeps name and source). "Deploy from Compose" is a link to
  `/apps/{id}/compose` (its own page under the Configure tab). Secrets, webhook, export and delete
  stay where they are.
- Running: the copies table gets CPU and Memory columns (number, "of N MHz reserved" / "of N MB
  ceiling, N reserved", a thin meter for memory against the ceiling, warning mark at 90 %), and
  the status column says "ran out of memory" when `OutOfMemory`.
- Apps home: the needs-you list sits under the headline, above the apps table; the headline's
  "N apps need you" sentence counts the list. "Nothing needs you." when empty.

Styles follow DESIGN.md (status as shape plus color, one sentence per item); new classes go into
`internal/ui/style/` and `tools/ui-css.sh build` regenerates `app.css`.

## R13. Gate and lab coverage

**Decision**: `TestConsoleLab` gains steps: edit an app's memory and env from Configure and see
the deployment healthy with both changed and its volume kept; create the compose example from
New app › Compose; read usage on the Running part; deploy a failing image and see the needs-you
item appear and then vanish after a healthy deploy. The gate (`runGate`) gains one step,
`console-v2-reads`, that calls `listCopies?usage=true` and `listAttention` with the scoped token
on the VM, which catches a policy gap for alloc stats on a real host.
