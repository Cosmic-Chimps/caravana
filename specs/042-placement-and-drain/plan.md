# Implementation Plan: Placement and drain

**Spec**: [spec.md](./spec.md) · **Research**: [research.md](./research.md) · **Tasks**: [tasks.md](./tasks.md)

## Summary

Three things on top of 031's servers. **Labels**: a server's labels (`ssd=true`) are stored with the server and
written to its Nomad node as dynamic node metadata (`caravana.label.<name>`, plus `caravana.server=<name>` on
every managed server), kept equal by a reconcile. **Placement**: `app.Service.Placement{Require, Prefer,
Avoid}` becomes the group's `constraint` and `affinity` blocks in `nomadjob.Translate` (pure, golden), the HCL
export writes them, a deploy plans the job first and refuses 409 `placement_impossible` naming the field when one
of Caravana's constraints fits no server. **Move work off**: `drainServer` makes the node ineligible and moves
each movable copy with Nomad's allocation stop, one at a time, waiting for the replacement; what can't move
(volumes, published ports, Traefik, batch runs) is reported; `undrainServer` cancels and makes the server
eligible. Needs-you kinds `copies_unplaced` and `server_move_incomplete`. Migration **0032**, API **0.46.0**,
policy gains `node { policy = "write" }`, no new Go module, no new runtime dependency, no constitution change.

## Constitution Check

- **I Small**: no module, no service, no root helper; the node agent is untouched (labels go through Nomad's
  API, research R1). ✓
- **II One model**: one field on `app.Service`; image, Compose and (later) Aspire normalize into it; `Translate`
  stays pure and golden-tested (`placement`). No new helper task or job. ✓
- **III Safe deploys**: unchanged; a move uses Nomad's reschedule of a copy and never a new job version. ✓
- **IV Fail loudly**: an impossible rule is refused naming the field before Register; what can't move is listed
  with its reason, never skipped silently. ✓
- **V API-first**: schema and codes in the same commit; the console calls `internal/service` only. ✓
- **VI Nomad-native**: Nomad's constraints, affinities, dynamic node metadata, eligibility and allocation stop
  through the official client; the export writes the blocks. Nomad's `node drain` is deliberately not used
  (research R4), which is a choice of Nomad primitives, not a departure from them. ✓
- **VII Secure**: labels are not secrets; `node:write` is the one new capability, explained in the policy. ✓
- **VIII Plain Go**: table tests, goldens, the fake Nomad, a lab step, cluster-test steps. ✓

No Complexity Tracking entry.

## Decisions

**D1 Model (`internal/app/placement.go`).**
```go
// Placement says where a service's copies may run (feature 042).
type Placement struct {
    Require map[string]string `json:"require,omitempty"` // label → value: only servers that have it
    Prefer  []Preference      `json:"prefer,omitempty"`  // affinities, in order
    Avoid   []string          `json:"avoid,omitempty"`   // server names the service never runs on
}
type Preference struct {
    Label  string `json:"label"`
    Value  string `json:"value"`
    Weight int    `json:"weight,omitzero"` // 1..100; 0 means DefaultPreferenceWeight
}
const DefaultPreferenceWeight = 50
const (MaxServerLabels = 16; MaxRequire = 8; MaxPrefer = 8; MaxAvoid = 16)
var labelNameRE  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
var labelValueRE = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,64}$`)
func ValidLabelName(s string) bool; func ValidLabelValue(s string) bool
// CheckLabels validates a server's label set (the service uses it): names, values, count. field is the prefix ("labels").
func CheckLabels(labels map[string]string, field string) error
```
`Service.Placement *Placement` (`placement,omitempty`): no existing revision moves. Validation in
`validateService` (`validate.go`), each a `*FieldError`: an empty object (`require`, `prefer` and `avoid` all
empty) is refused on `services[i].placement` ("has no rule: leave it out"); `require.<name>` bad name or value
(the name is the field's last segment, so a bad name is reported on `services[i].placement.require`); more than
8 on `services[i].placement.require`; `prefer[j].label`, `prefer[j].value`, `prefer[j].weight` (1–100, 0 allowed
as the default), more than 8 on `services[i].placement.prefer`, the same label twice on the second
`prefer[j].label`; `avoid[k]` not `mesh.ValidName` or `mesh.ReservedName`, twice on the second, more than 16 on
`services[i].placement.avoid`. `mesh` is already imported by `app`? It is not: `app` must stay free of `mesh`
(`mesh` imports nothing of ours, so `app` may import it; check the import graph with `go list -deps` and, if
`mesh` would make a cycle, copy the one regexp into `app` with a comment naming `mesh.ValidName`).

**D2 Translate (`internal/nomadjob`).** Keys live here (pure, used by `deploy` for writing):
```go
const ServerMetaKey = "caravana.server"          // the server's name, on every managed node
const LabelMetaPrefix = "caravana.label."         // + the label's name
func LabelMetaKey(name string) string
func LabelAttribute(name string) string           // "${meta.caravana.label.<name>}"
const ServerAttribute = "${meta.caravana.server}"
```
`taskGroup`: when `svc.Placement != nil`, `tg.Constraints` = one `api.Constraint{LTarget: LabelAttribute(k),
RTarget: v, Operand: "="}` per `require` entry, keys sorted, then one `{LTarget: ServerAttribute, RTarget:
name, Operand: "!="}` per `avoid` entry, sorted; `tg.Affinities` = one `api.Affinity{LTarget, RTarget, Operand:
"=", Weight: new(int8(w))}` per `prefer`, in order, `w` the weight or the default. `ScheduleRunJob` puts the
same on the run's group (`placementBlocks(svc) (constraints, affinities)` shared). HCL: in `group`, after
`spread`, `constraint { attribute = … value = … operator = "!=" }` (operator only when not `=`) and
`affinity { attribute = … value = … weight = N }`; the reflection test covers both. Export header: no new
line (nothing outside the job is needed). Golden `placement` (JSON and HCL): service `web` with two copies,
`require: {ssd: "true", region: "eu"}`, `prefer: [{ssd true weight 80}, {disk nvme}]`, `avoid: ["three",
"two"]`; a second service `worker` without placement, to show nothing else changes. A schedule-run test shows
the run job carries the blocks.

**D3 Store (migration `0032_placement_drain.sql`).**
```sql
ALTER TABLE servers ADD COLUMN labels TEXT NOT NULL DEFAULT '{}';  -- JSON object, the owner's labels
CREATE TABLE server_moves (
  id          TEXT PRIMARY KEY,                 -- "mv_" + id
  server_idx  INTEGER NOT NULL REFERENCES servers (idx) ON DELETE CASCADE,
  state       TEXT NOT NULL CHECK (state IN ('moving', 'done', 'incomplete', 'cancelled')),
  started_at  TEXT NOT NULL,
  deadline_at TEXT NOT NULL,
  ended_at    TEXT NOT NULL DEFAULT '',
  moved       INTEGER NOT NULL DEFAULT 0,       -- copies that left the server
  left        INTEGER NOT NULL DEFAULT 0,       -- movable copies still there when it ended
  stays       TEXT NOT NULL DEFAULT '[]',       -- JSON [{app, service, reason}]: what never moves
  error       TEXT NOT NULL DEFAULT ''          -- one sentence in Caravana's words
) STRICT;
CREATE INDEX server_moves_server ON server_moves (server_idx, started_at);
```
(Check whether `servers` rows are deleted with `DeleteServer` through a plain `DELETE`; if foreign keys are
not enabled in the store, delete the moves in `DeleteServer` explicitly.) `store.Server.Labels
map[string]string` (scanned from the JSON; `{}` → empty map). Functions: `SetServerLabels(ctx, idx, labels)`,
`InsertServerMove(ctx, ServerMove)`, `UpdateServerMove(ctx, id, moved, left int)`, `EndServerMove(ctx, id,
state, endedAt, moved, left, errText)`, `NewestServerMove(ctx, idx) (ServerMove, error)` (ErrNotFound for none),
`OpenServerMoves(ctx) ([]ServerMove, error)`.

**D4 Deploy (`internal/deploy/placement.go`, `nodework.go`).**
- `(*Deployer).SetNodeLabels(ctx, nodeID, server string, labels map[string]string) error`: reads the node
  (`Nodes().Info`, not the cache), builds `NodeMetaApplyRequest{NodeID, Meta}` with `ServerMetaKey` → server,
  each label → `LabelMetaKey(name)` → value, and every `caravana.label.*` key the node has that isn't in
  `labels` → nil (unset); `Nodes().Meta().Apply`; then `forgetNodeInfo(nodeID)`; then polls `Nodes().Info`
  every `Timing.PollGap` until the server-side `Meta` matches, up to `LabelWait` (15 s; `Timing` field for
  tests): a timeout is `ErrLabelsPending` (the write was accepted; Nomad's servers haven't shown it). A 403 is
  `capabilityError("write node metadata", "node:write", err)`, so the message says to run the installer again.
- `(*Deployer).NodeLabels(ctx, nodeID) (server string, labels map[string]string, err)`: from the cached
  `nodeInfo` record (10 minutes; fresh after a write). `deploy.Node` gains nothing (the list stubs have no meta).
- `(*Deployer).SetNodeEligible(ctx, nodeID string, eligible bool) error`: `Nodes().ToggleEligibility`; 403 is
  the capability error.
- `(*Deployer).MoveCopy(ctx, copyID string) error`: `Allocations().Stop(&api.Allocation{ID: copyID})`
  (Nomad's "stop and reschedule": the scheduler places the replacement on an eligible node). 404 is
  `ErrCopyNotFound`.
- `(*Deployer).WaitCopyMoved(ctx, jobID, group, oldCopyID, nodeID string, checked bool) (newID string, err)`:
  polls `Jobs().Allocations(jobID)` every `PollGap` until an allocation of `group`, other than `oldCopyID`,
  with `NodeID != nodeID`, client status running, and (when `checked`) every check of it passing
  (`Allocations().Checks`) exists; returns its ID. The context's deadline bounds it (the context's error is
  returned then).
- `checkPlacement(ctx, job *api.Job, a app.App) error`, called in `Register` after `checkPublicNetwork` and only
  when some service has a `Placement`: `Jobs().PlanOpts(job, &api.PlanOptions{Diff: false})`; for each group
  with `FailedTGAllocs`, if `ConstraintFiltered` has a key whose text starts with `${meta.caravana.` (research
  R3), the deploy is refused with `*PlacementError{Field, Message}` (`Unwrap() → ErrPlacementImpossible`):
  `${meta.caravana.label.<k>} = <v>` → field `services[i].placement.require.<k>`, message `no server that
  accepts work has the label <k>=<v>`; `${meta.caravana.server} != <n>` → field `services[i].placement.avoid`,
  message `every server that accepts work is avoided`. `i` is the index of the service named by the group. A
  Plan that fails for lack of room (`DimensionExhausted`) is not a refusal. The plan call needs `submit-job`,
  which the policy has.
- `(*Deployer).QueuedCopies(ctx) (map[string]map[string]int, error)`: `Jobs().List` → for every stub with a
  `JobSummary`, `Summary[group].Queued` by job ID and group (only non-zero entries).
- `deploytest.Nomad` gains: `POST /v1/client/metadata?node_id=` (merges into the node's `Meta`, nil unsets;
  `SetNodeMetaDelay(d)` makes the `GET /v1/node/{id}` record lag behind by `d` for the wait test),
  `POST /v1/node/{id}/eligibility`, `PUT /v1/allocation/{id}/stop` (marks the copy stopping and, with
  `MoveCopiesTo(nodeID, nodeName)` set, makes a replacement copy of the same job and group on that node,
  running after `SetStartDelay`; without it the replacement stays pending), `PUT /v1/job/{id}/plan` (answers
  `FailedTGAllocs` from `SetPlanFailure(group, constraintText, count)`; otherwise none), and `GET /v1/jobs`
  stubs carry `JobSummary` with `SetQueued(jobID, group, n)`. `FailNodeWrites(403)` makes the writes answer
  403.

**D5 Service: labels (`internal/service/serverlabels.go`).**
- `SetServerLabels(ctx, name string, labels map[string]string) (ServerView, error)`: `app.CheckLabels(labels,
  "labels")` (field errors are 422 through `unprocessable`); the row by name (`ErrServerNotFound`); a `waiting`
  row is `ErrServerNotReady` (409 `server_not_ready`: "This server hasn't joined yet."). Store first; then,
  when Nomad lists the server's node and it isn't down, `deployer.SetNodeLabels`; `ErrLabelsPending` is not a
  failure (the view says `LabelsApplied: false`); a capability error is returned (the labels stay stored and
  the reconcile retries). Publish `ChangeServers`.
- `ServerView.Labels map[string]string` (from the row; `{}` for an unmanaged node) and `LabelsApplied bool`
  (the node's `caravana.server` equals the name and its labels equal the stored set; false while Nomad can't be
  read or the node is down).
- `reconcileNodeLabels(ctx)`: for every managed row that has a node Nomad lists as not down, compare
  `deployer.NodeLabels` with the stored labels and name; write when they differ. Run from `Start` (after the
  watchers), when `NodeSync` marks a server ready, after `SetServerLabels` fails with a capability error (no:
  just at the next pass), and every `labelReconcileGap` (5 minutes; `SetLabelReconcileGap`). Errors are
  logged, never fatal.
- `RemoveServer` is unchanged (the node is gone).

**D6 Service: move work off (`internal/service/servermove.go`).**
- `MoveWorkOff(ctx, name string, deadline time.Duration) (ServerView, error)`: `deadline` 1 min to 2 h (0 =
  `DefaultMoveDeadline` 15 min; outside the range a field error on `deadlineSeconds`). Refusals: unknown name
  404; a row not `ready`, or whose node Nomad doesn't list or says down, `ErrServerNotReady`; an open move
  `ErrServerMoving` (409 `server_moving`); no *other* node that is ready, eligible and not down
  `ErrNoOtherServer` (409 `no_other_server`: "No other server accepts work: there is nowhere to move to.").
  Then: `deployer.SetNodeEligible(node, false)`, insert the move row (`moving`, `deadline_at = now +
  deadline`, `stays` from the first classification), publish `ChangeServers`, start `runMove` in a goroutine
  (`s.watchers`), answer the view.
- Classification (`classifyCopies(apps []store.AppRecord, copies []deploy.Copy) (movable, stays)`, pure):
  from `deployer.AllCopies` filtered to the node's name and non-terminal copies. By job ID: a stored app's job
  → its service: volumes → stays "its data is on this server"; a published port → stays "its port <n> is on
  this server's address"; otherwise movable. `traefik` → stays "the proxy runs here". A job ID that is a
  Caravana helper (`<app>_run-`, `_backup`, `_restore`, periodic children) → stays "a run that ends on its
  own". Anything else → stays "not deployed by Caravana". Stays are one entry per app and service (or job),
  never per copy.
- `runMove(ctx, move)`: loop until no movable copy is left or the deadline passes: list the node's copies
  again (the classification repeats each round, so a copy that appeared meanwhile is seen), and for each
  movable copy, in app order then copy order: `tryLockApp` (busy → skip this round), `deployer.MoveCopy`,
  `deployer.WaitCopyMoved` with the remaining time (at most `moveWait` 5 minutes per copy), unlock, `moved++`,
  `UpdateServerMove`, publish `ChangeServers`. A copy whose replacement didn't come within its wait is counted
  as left for this round and tried again next round only if it still runs there (Nomad may have placed it
  late). Between rounds with nothing movable but busy apps: sleep `movePollGap` (5 s). End states: no movable
  copy left → `done` (`left` 0); the deadline passed → `incomplete` with `left` = the movable copies still
  there and `error` "<n> copies had nowhere to go within the deadline" (or "… were busy"); cancelled (D7) →
  `cancelled`. The node stays ineligible after `done` and `incomplete`. Publish `ChangeAttention` at the end.
- `resumeMoves(ctx)` at `Start`: every `OpenServerMoves` row is resumed with its stored deadline (a goroutine
  each); one whose deadline already passed is settled `incomplete` with "Caravana restarted during it".
- `ServerView.Move *MoveView{ID, State, StartedAt, DeadlineAt, EndedAt, Moved, Left, Stays []MoveStay{App,
  Service, Reason}, Error}` from `NewestServerMove` (nil when none). `AcceptsWork` keeps its meaning
  (eligibility); the console draws "moving work off" while `Move.State == moving` and "not taking new work"
  when ineligible otherwise.

**D7 Accept work again.** `AcceptWork(ctx, name string) (ServerView, error)` (`undrainServer`): a moving move is
cancelled (the loop checks a cancel flag under `moveMu` before each copy and ends the row `cancelled` with
`moved` so far), then `deployer.SetNodeEligible(node, true)`. For a server with no open move it only toggles
eligibility (a server made ineligible with Nomad's CLI, or after a finished move). Refusals: 404; a row not
ready or a node Nomad can't see, `ErrServerNotReady`. Publishes `ChangeServers` and `ChangeAttention`.

**D8 Needs-you.** `AttentionCopiesUnplaced = "copies_unplaced"` (`attention_placement.go`): for every stored
app that isn't stopped whose service has a `Placement`, `deployer.QueuedCopies()[app.ID][service] > 0` → one
item per service, key `copies_unplaced|<app>|<service>|`, `Copies` empty, `Crashes` = the queued count carried
in a new field `Queued int`, level failed, `When` now. Not judged while Nomad can't be read (like the copy
kinds). Words: "web (shop): 2 copies can't be placed: no server that accepts work matches its placement rules,
or none has room." (`copyCount`). `AttentionServerMoveIncomplete = "server_move_incomplete"`
(`attention_servers.go`): the newest move of a server is `incomplete` and the server is still ineligible → key
`server_move_incomplete|||<name>`, level warning, `Server`, `Left` in `Crashes`? No: a new field `Left int`.
Words: "two: the move-off passed its deadline; 2 copies still run there." Both kinds go in `AttentionKinds`,
`ProblemParts`, the notifier's three switches and `activity.go`'s (the tests walk them).

**D9 API 0.46.0 (`api/openapi.yaml`, `internal/api/servers.go`, `placement` on `Service`).**
- `Service.placement` (schema `Placement`: `require` object of strings, `prefer` array of `Preference{label,
  value, weight}`, `avoid` array of strings) on the app schema, the Compose overlay's service
  (`OverlayService.placement`, same schema) and wherever `Service` is reused.
- `Server` gains `labels` (object, `{}` when none), `labelsApplied` (bool), `move` (nullable `ServerMove`:
  `id`, `state`, `startedAt`, `deadlineAt`, `endedAt`, `moved`, `left`, `stays: [{app, service, reason}]`,
  `error`).
- `putServerLabels`: `PUT /v1/servers/{name}/labels` body `{"labels": {"ssd": "true"}}` → 200 `Server`; 404
  `server_not_found`; 409 `server_not_ready`; 422 `invalid_field` on `labels.<name>`; 502 when Nomad refuses.
- `drainServer`: `POST /v1/servers/{name}/drain` body `{"deadlineSeconds": 900}` (optional) → 202 `Server`
  (`no-store`); 404; 409 `server_not_ready`, `server_moving`, `no_other_server`; 422 on `deadlineSeconds`.
- `undrainServer`: `DELETE /v1/servers/{name}/drain` → 200 `Server`; 404; 409 `server_not_ready`.
- `deployApp` and friends: 409 `placement_impossible` with the field (through `*deploy.PlacementError`, mapped
  beside `no_public_network`).
- The four new codes in the error-code list. Deploy tokens: the server routes are denied by the default
  deny (the scope walk lists them); a scoped `deployApp` may send placement rules for its own app.
- Attention items: the two kinds, `queued` and `left` numbers on the item schema.

**D10 Compose.** `OverlayService.Placement *app.Placement` (`placement`), copied onto the service beside
`Identities` (`service.go`'s last line); validation errors are renamed from `services[i].placement…` to
`services.<svc>.placement…` like the backup ones. `Request.WithoutSecretValues` keeps it. A fixture with
placement in the overlay.

**D11 Service flow.** `promoted()` takes the source's `Placement` (`"placement"` in `Changed` when it differs);
`CopyApp` keeps it. `applyEdit` (console) leaves it unless the placement form sets it. Schedule runs: the
run's job carries the blocks (D2), so a run can't start on a server the service avoids.

**D12 Console.**
- Server page (`opsserver.go`, `opsserver.html`): the facts panel gains a **Labels** row: the labels as
  chips (`ssd=true`), a form under it (one text input `labels` holding `ssd=true, region=eu`; a bare word
  means `=true`; Save) posting to `POST /ops/servers/{name}/labels` → 303 back; a refusal draws the page with
  the sentence at the form. The head gains the actions: **Move work off…** (a link to
  `GET /ops/servers/{name}/move`) for a ready server that accepts work, **Accept work again** (post to
  `POST /ops/servers/{name}/accept`) for a ready server that doesn't; the state pill says "moving work off"
  during a move and "not taking new work" when ineligible. The body's first panel during or after a move is
  the **move panel**: "Moving work off · 3 of 5 moved · until 14:30 UTC" with Cancel (the accept route), or
  "Moved 5 copies in 2 min · 1 stays: db (shop): its data is on this server · not taking new work" with
  Accept work again, or the incomplete sentence with the copies left; it lives in the body, so the stream's
  `servers` notice refreshes it. The confirmation page `/ops/servers/{name}/move` (template `moveserver.html`
  beside `removeserver.html`, through `showServerSub`): what moves (n copies, by app and service), what stays
  with its reason, the one-copy note, a deadline select (5, 15, 30, 60 min), the button; the post answers 303
  to the server page, and a refusal (409) draws the confirmation again with the reason. The list
  (`servers.html`): labels under the name, the state words.
- Configure (`configure.go`, `app.html`): a **Placement** panel per service: the rules as sentences ("Only on
  servers with `ssd=true`"; "Prefers `region=eu`"; "Never on `three`"; or "Any server") and, for an app edited
  in the console, a form: Require (text, `label=value` pairs separated by commas), Prefer (text, the same with
  an optional ` weight` number after a pair, e.g. `ssd=true 80`), Avoid (one checkbox per managed server,
  by name); `POST /apps/{id}/services/{svc}/placement` with the page's revision → `DeployIf` (a deploy, like a
  secrets edit); a field error draws the panel with the sentence at the field. A kept-file app shows the rules
  read-only and "Set in the Compose overlay" (the Compose form keeps them: `keepPlacement`, like
  `keepIdentities`). `tools/ui-css.sh build` if a class is added.

**D13 Policy and installer.** `deploy/install/caravana-policy.hcl`: `node { policy = "write" }` with the comment
rewritten: metadata (labels), eligibility and allocation stop (moving work off, feature 042); still no
`agent:read`. The installer applies it on a rerun; `TestLab` applies it on every run. `install-test.sh`: a
grep of the policy line.

**D14 Docs.** `docs/placement-and-drain.md` (labels, the three rules with examples, what moves and what stays,
the one-copy gap, `avoid` and unmanaged servers), `docs/plan.md` (the Phase 3 rows), `CLAUDE.md` paragraph,
`changelog.d/placement-and-drain.md` (`### Added`, `### Upgrade`: rerun the installer for `node:write`).

## Tests

- `app`: every validation rule's field path; the document of an app without placement is byte-identical.
- `nomadjob`: golden `placement` (JSON, HCL), the schedule run, the HCL reflection test, keys.
- `deploy` (+ `deploytest`): `SetNodeLabels` (unsets stale keys, waits for the server-side record, pending on a
  lag, 403), `SetNodeEligible`, `MoveCopy`, `WaitCopyMoved`, `checkPlacement` (the two fields; room is no
  refusal; an app without placement never plans), `QueuedCopies`.
- `store`: labels, moves, cascade on delete, migration.
- `service`: labels save and view, reconcile, `MoveWorkOff` end to end with the fake (done, incomplete at the
  deadline, cancelled, resumed at start, busy app, the stays), the refusals, both attention kinds, promotion
  and copy keep placement.
- `api`: the codes, the schema parity, the scope walk.
- `compose`: the overlay field and its field names.
- `ui`: the labels form, the move confirmation and panel, Accept work again, Configure's panel and form, the
  Compose form keeps placement, sentinel words.
- Lab: gate step `placement` (`cmd/caravana/gate_placement_test.go`): `require: {ssd: "true"}` → 409
  `placement_impossible` on the field and no job; `avoid: ["nobody"]` and a `prefer` deploy healthy;
  `exportMatchesLive`; `drainServer` on the lab's one node → 409 `no_other_server`; `putServerLabels` on the
  unmanaged node → 404. Console step `console-placement` (`console_placement_test.go`): Configure's form sets
  and clears a rule.
- `deploy/dev/cluster-test.sh` steps 12–14 (after step 11's leave? No: before step 7, while the three servers
  are up): labels on `two` (API; two labels, then one removed), `require` runs on two only, `prefer` lands a
  copy on two, an impossible `require` is 409 naming the field, `avoid: ["three"]` keeps copies off three;
  then move work off `two` with `spread` watched every half second and a volume app on `two`: every movable
  copy leaves within the deadline, `move.state` is `done`, the volume app is in `stays`, `spread.test` kept
  answering (0 failed calls allowed? the copies move one at a time, so a bound of 5), then Accept work again
  and `acceptsWork` is true. The comment at the top of the script gains the steps.
