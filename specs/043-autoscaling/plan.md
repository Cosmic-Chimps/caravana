# Implementation Plan: Autoscaling

**Spec**: [spec.md](./spec.md) · **Research**: [research.md](./research.md) · **Tasks**: [tasks.md](./tasks.md)

## Summary

`app.Service.Scale{Min, Max, Target, CooldownSeconds}`; `Replicas` becomes the starting count. A pure rule,
`metrics.DecideScale`, reads a service's stored minute rows on each sampler tick and says one step up, one
step down or nothing; the service calls Nomad's scale API (`deploy.Scale`) under the app's lock and records an
app action `scale` (from, to, reason, the job version Nomad gave, the deployment version it scaled). Every
deploy registers a scaled service with its current count, clamped to `min`–`max`, so canaries follow it. The
app's status counts the copies a scaled service has now; a scale's job version reads as its deployment's
version. Needs-you kind `scale_at_max`. Console: Configure's Scaling panel, the actions and the feed, the
Monitoring apps table and a list of steps. Migration **0033**, API **0.47.0**, no new Go module, no runtime
dependency, no policy change, no constitution change.

## Constitution Check

- **I Small**: no autoscaler service; the decision runs in the sampler that already runs. ✓
- **II One model**: one field on `app.Service`; Compose's overlay maps into it; `Translate` stays pure: the
  current count comes in through the app it is given (D4), golden-tested (`scale`). ✓
- **III Safe deploys**: a scale changes only the count (Nomad's own scale, health-checked, auto-revert); a
  deploy keeps blue/green with as many canaries as copies (research R2). Env and secrets untouched. ✓
- **IV Fail loudly**: every refusal names its field; a refused scale is a failed action with its reason. ✓
- **V API-first**: schema, overlay and action fields in `api/openapi.yaml` in the same commit; the console
  calls `internal/service` only. ✓
- **VI Nomad-native**: `Jobs().Scale` through the official client; Nomad code stays in `deploy`/`nomadjob`. The
  export writes the definition's starting count (D10), which is a plain Nomad job. ✓
- **VII Secure**: no secret involved; the reason holds numbers only. ✓
- **VIII Plain Go**: table tests for the rule, goldens, the fake Nomad's scale endpoint, a lab step. ✓

No Complexity Tracking entry.

## Decisions

**D1 Model (`internal/app/scale.go`).**
```go
// Scale lets Caravana change how many copies a service runs, from its own
// metrics (feature 043). Replicas is then the starting count.
type Scale struct {
    Min             int         `json:"min"`
    Max             int         `json:"max"`
    Target          ScaleTarget `json:"target"`
    CooldownSeconds int         `json:"cooldownSeconds,omitzero"` // 0 means DefaultScaleCooldown
}
// ScaleTarget is the one value the copies are kept near. Exactly one is set.
type ScaleTarget struct {
    CPU             int `json:"cpu,omitzero"`             // percent of the CPU the copies reserve, 10..200
    RequestsPerCopy int `json:"requestsPerCopy,omitzero"` // requests a second per copy, 1..100000
    P95             int `json:"p95,omitzero"`             // the slowest 5 %, in milliseconds, 10..60000
}
const (MaxScaleCopies = 20; DefaultScaleCooldown = 300; MinScaleCooldown = 60; MaxScaleCooldown = 3600)
func (s Scale) Cooldown() time.Duration           // CooldownSeconds or the default
func (s ScaleTarget) Kind() string                // "cpu", "requestsPerCopy", "p95", "" for none
// CountFor is the count a deploy registers: the current count when the job
// runs (current non-nil), else the starting count (Replicas, or Min), always
// within Min..Max. A service without Scale returns Replicas (nil = 1).
func (svc Service) CountFor(current *int) int
```
`Service.Scale *Scale` (`scale,omitempty`). Validation in `validateService`, each a `*FieldError`
(`services[i].scale.min` < 1; `.max` < min or > 20; `.target` with none or more than one set; `.target.cpu`,
`.target.requestsPerCopy`, `.target.p95` out of range; `.cooldownSeconds` not 0 and outside 60–3600;
`services[i].replicas` outside min–max when both are set; `services[i].scale` for a service with volumes or a
published port ("a service with volumes runs one copy" / "a service with a published port runs one copy");
`services[i].scale.target` for `requestsPerCopy`/`p95` on a service with no domain ("requests are measured at
the proxy: give the service a domain, or scale on cpu")). A revision test: an existing app's document doesn't
change. `serviceCopies` in `service/apps.go` keeps reading `Replicas` for unscaled services (D7).

**D2 Translate (`internal/nomadjob`).** No signature change. `taskGroup` reads the count from `svc.Replicas` as
today (D4 puts the current count there). The spread is added when `count > 1` **or** the service has `Scale`
with `Max > 1`, so scale-ups spread too. Golden `scale` (JSON and HCL): a service with `Scale{1, 3, cpu 70}`
and `Replicas` 2: count 2, canary 2, the spread. The HCL writer needs nothing new (no new Nomad field).

**D3 The rule (`internal/metrics/scale.go`, pure).**
```go
type ScaleRule struct { Min, Max int; Kind string; Target float64; Cooldown time.Duration }
type ScaleInput struct {
    Rule       ScaleRule
    Count      int       // the group's count now
    Rows       []Row     // the service's minute rows, any order
    LastChange time.Time // the newest scale step (any outcome) or the end of the newest deploy; zero = none
    Now        time.Time
}
type ScaleMove string // "up", "down", "bound", "" (hold)
type ScaleDecision struct {
    Move    ScaleMove
    To      int     // the new count; Count when Move is ""
    Value   float64 // what the window measured (per copy for requestsPerCopy, percent for cpu, ms for p95)
    Samples int     // rows the decision read
    Hold    string  // why nothing changed, for tests and the debug log: "cooldown", "few_samples", "in_band", "at_max", "at_min", "no_value"
}
const (ScaleUpSamples = 3; ScaleUpMinSamples = 2; ScaleDownSamples = 5; ScaleDownMargin = 0.8; ScaleP95DownFraction = 0.5)
func DecideScale(in ScaleInput) ScaleDecision
func OverTargetAtMax(in ScaleInput) (value float64, ok bool) // the needs-you rule (D9)
```
Rules, in order:
1. `Count` outside `Min..Max`: `Move "bound"`, `To` the nearest bound, at once (no cooldown, no metrics).
2. `Now − LastChange < Cooldown`: hold `cooldown`.
3. Only rows with `TS ≥ LastChange` count (the copies changed at the step), newest first.
4. **Up** reads the newest `ScaleUpSamples` rows that have a value, needs at least `ScaleUpMinSamples`; the
   value is the window's aggregate: cpu = Σ`CPUMHz` / Σ`CPUReservedMHz` × 100 (rows where both are set and the
   reservation > 0); requestsPerCopy = Σ`Requests` / Σ`HTTPSecs` / `Count` (rows with both, `HTTPSecs` > 0);
   p95 = `P95Millis` of the rows folded (`Fold`), only with at least `MinTimedRequests` timed requests. Value >
   target and `Count < Max` → `up`, `To = Count+1`. Value > target at `Max` → hold `at_max`.
5. **Down** needs the newest `ScaleDownSamples` rows, every one with a value (a row with 0 requests is a value
   0 for requestsPerCopy; for p95, rows with HTTP numbers and no timed request count as 0 ms). cpu and
   requestsPerCopy: the window's aggregate projected on one copy fewer, `value × Count / (Count−1)`, is under
   `target × ScaleDownMargin`, and no single row's own value (projected) is over `target`. p95: the window's
   p95 is under `target × ScaleP95DownFraction`. And `Count > Min` → `down`, `To = Count−1`; at `Min`, hold
   `at_min`.
6. Otherwise hold `in_band` (or `few_samples` / `no_value`).
Table tests cover each rule and the edges (exactly at target, one missing row, a 0-request window, rows before
`LastChange`, cooldown boundary, Min = Max).

**D4 Deploy keeps the count (`internal/deploy/register.go`).** `Register` already reads the current job. Before
`Translate`, it makes a copy of `a` whose scaled services have `Replicas = new(svc.CountFor(current))`, where
`current` is the group's `Count` in the job read, when the job exists, isn't stopped and has the group; else
nil. Unscaled services are left alone. The copy is used only for the job: the stored app keeps its starting
`Replicas`. Every deploy path goes through `Register`, so deploy, redeploy, rollback, promotion and webhook all
keep the count; `StartApp` re-registers the stopped job as it was (its counts included). Test with the fake
Nomad: a job at 3 is registered with 3 and canary 3; a new `Max` 2 registers 2; a new job registers the start.

**D5 Nomad calls (`internal/deploy/scale.go`).**
```go
type ScaleState struct {
    Exists, Stopped bool
    Version         uint64
    Counts          map[string]int // group → count
    Rolling         bool           // the job's latest Nomad deployment is running or pending
}
func (d *Deployer) ScaleState(ctx context.Context, jobID string) (ScaleState, error) // Jobs().Info + Jobs().LatestDeployment
// Scale sets one group's count with Nomad's scale API and returns the job
// version Nomad made (read back with Info; found false when it can't be
// told, e.g. the job changed again in between).
func (d *Deployer) Scale(ctx context.Context, jobID, group string, count int, message string) (version uint64, found bool, err error)
```
`Scale` uses `Jobs().Scale(jobID, group, &count, message, false, map[string]any{"caravana.scale": "1"}, w)`,
then `Info`: `found` when the job's `JobModifyIndex` equals the answer's. Errors go through `callError` /
`registerError`'s words (never Nomad's raw body in a page beyond what `registerError` already shows). `deploytest`
gets the endpoint `POST /v1/job/<id>/scale` (sets the group's count, bumps version and modify index) and the
latest-deployment read if missing.

**D6 Records and versions (store, migration `0033_autoscaling.sql`).** `app_actions` is rebuilt (as 0014 did)
with `action` CHECK adding `'scale'` and four columns: `from_count INTEGER`, `to_count INTEGER`, `reason TEXT
NOT NULL DEFAULT ''`, `job_version INTEGER`, `base_version INTEGER`. `store.ActionScale = "scale"`;
`ActionRecord.FromCount, ToCount *int`, `Reason string`, `JobVersion, BaseVersion *uint64`. A scale action is
inserted finished (`succeeded` or `failed`), `Service` set, trigger `autoscale` (`app.TriggerAutoscale`, an
action trigger like `TriggerSchedule`, not a deployment trigger). Reads:
- `store.LastScale(ctx, appID, service) (ActionRecord, bool, error)`: the newest scale action of a service.
- `store.ScaleVersions(ctx) (map[string]map[uint64]uint64, error)`: app → job version → base version, from
  every scale action with both versions.
- `store.RecentScales(ctx, appIDs []string, limit int)`: for Monitoring.
`base_version` is the version of the deployment the scale scaled: the job's version before the scale, itself
mapped through `ScaleVersions` when it was a scale's (so a chain of scales maps to the deployment).

A service's current count lives in a small table `service_counts (app_id, service, count, updated_at,
PRIMARY KEY (app_id, service))`, written by the autoscaler on every pass from `ScaleState` (so a change made
outside Caravana, or Nomad's revert of a failed scale-up, is picked up), after each scale, and by `Register`'s
caller in the service after a deploy registers (the counts it used). Deleted with the app (`DeleteApp`).
`store.ServiceCounts(ctx) (map[string]map[string]int, error)`, `store.SetServiceCount(...)`.

**D7 Status and versions (`internal/service`).**
- `wantedCopies`/`serviceCopies` become `statusOf`'s input from the stored counts: a scaled service wants its
  stored count (falling back to `CountFor(nil)` when none is stored yet). `statusOf` stays pure: it gets the
  counts as an argument.
- Version aliasing: `deploy.Deployer` gets a hook `VersionAliases func(ctx) (map[string]map[uint64]uint64,
  error)` (set by the service at start to `store.ScaleVersions`); `Copies`, `AllCopies` and `LogCopies` rewrite
  each copy's `Version` through it (a failed read leaves versions as they are and is logged once by the
  service). So `LiveVersion`, the Running tab, Restart's "live version" filter, the home's "new copies" and the
  API's `Copy.version` all read the deployment's version. Comment why at the hook (research R1).

**D8 The autoscaler pass (`internal/service/autoscale.go`).** Run at the end of `sampleTick`, after the write,
as `s.autoscale(ctx, now)` (errors logged, never failing the tick). For each stored app, not stopped, defined,
with at least one service with `Scale`:
1. `TryLock` the app's lock (the one Stop/Restart use); busy → skip the app this tick.
2. Skip when the app's latest deployment is pending or running.
3. `ScaleState`: not existing or stopped → skip; `Rolling` → skip; write `service_counts` from `Counts`.
4. For each scaled service: rows from `store.MinuteRows` of its series over the last
   `max(ScaleDownSamples, WindowMinutes) × gap + gap`; `LastChange` = the later of `LastScale`'s time and the
   latest deployment's end (`UpdatedAt` of a terminal record); `DecideScale`.
5. A move: `deploy.Scale` with the message (`reason`); on success insert the action `succeeded` with from, to,
   reason, `job_version` (when found) and `base_version`, set `service_counts`; on failure insert it `failed`
   with the error in Caravana's words and no version. One move per app per tick (the first service that has
   one), so two services of one app never race in Nomad's deployment.
6. Publish `ChangeActions` for the app and `ChangeMetrics`.
The reason is one sentence in Caravana's words with numbers only: "62 requests a second per copy over the last
3 minutes, target 50", "CPU at 91 % of its reservation over the last 3 minutes, target 70 %", "the slowest 5 %
at 420 ms over the last 3 minutes, target 300 ms", "about 18 requests a second per copy on one copy fewer over
the last 5 minutes, target 50", "outside 1–3 copies". `scaleReason(d ScaleDecision, r ScaleRule, count int)`
in the service, with a table test. The span says minutes from the number of rows (`Minutes` of each row).
`SetAutoscaleForTests`-style knobs are not needed: the lab's sampler gap (10 s) makes the windows short.

**D9 Needs-you `scale_at_max`.** In `service/attention_scale.go`, derived on every read: for each scaled service
of a running app whose stored count ≥ `Max` and `metrics.OverTargetAtMax` (the newest `WindowMinutes` rows, at
least 5 with a value, aggregate over the target), an item `AttentionScaleAtMax`, level warning, key
`kind|app|service|`, worded once in `service.ProblemParts` ("`web` of `shop` is at its 3 copies and still over
its target …"); `AttentionKinds` lists it, so `TestEveryKindSaysTheSameWordsEverywhere` walks it; the notifier
sends `problem.opened/resolved` as for any kind. Stopped apps are ignored.

**D10 Compose, promotion, copy, export.**
- `compose.OverlayService.Scale *app.Scale` (`scale`), copied onto the service; the model's refusals renamed to
  `services.<svc>.scale...` as placement's are; a fixture.
- Promotion: the target keeps its own `Scale` (like `replicas` and resources); a service new to the target
  arrives with the source's. Copy to keeps it.
- Export writes the stored definition (the starting count) and adds a header line for a scaled service:
  `# web scales between 1 and 3 copies (Caravana's autoscaling); count is the starting count.`

**D11 API (`api/openapi.yaml` 0.47.0, `internal/api`).** `ServiceScale` and `ServiceScaleTarget` schemas on
`Service.scale` and the overlay's service; `AppAction.action` enum gains `scale`, `trigger` gains `autoscale`,
new optional `fromCount`, `toCount`, `reason`; `ServiceStatus.wanted` documented as the count the service has
now for a scaled service. No new route, no new code. Deploy tokens may deploy `scale` for their own app (it is
part of `deployApp`). Parity and scope tests unchanged in shape.

**D12 Console (`internal/ui`).**
- Configure: a Scaling panel per service after Placement (`scale.go`): one sentence ("Runs 1 to 3 copies,
  keeping each near 50 requests a second; waits 5 minutes between steps." / "Runs 2 copies." with "Scale on its
  own…"), and the form (Min, Max, Target as a select of the three kinds with one number input, Cooldown as a
  select 1, 2, 5, 10, 30, 60 min, Starting count = Replicas) posting to `/apps/{id}/services/{svc}/scale` as a
  deploy with the page's revision (`DeployIf`), errors drawn at their input; "Stop scaling" removes `scale` and
  keeps the current count as `replicas`. `applyEdit` keeps `Scale`. A kept-file app shows the panel read-only
  ("Set it in the Compose overlay"), and `composeDraft.keepScale` carries the overlay's (like `keepPlacement`).
- Recent actions and the feed (`service/activity.go`): "Scaled `web` 1 → 2" with the reason under it and the
  trigger words "on its own"; a failed one "Couldn't scale `web` 1 → 2" with the error.
- Monitoring: the apps table's copies cell reads "2 (1–3)" for a scaled service; a "Scaling" list under the
  apps table with the place's last 10 steps (service, from → to, reason, when, `clockOf`), only when there is
  one. Running tab: the service line says "2 copies · scales 1–3".
- Forms work without JavaScript; `tools/ui-css.sh build` if classes change.

**D13 Docs and notes.** `docs/autoscaling.md` (how it decides, the windows, what a deploy does, what it never
does), `docs/plan.md` line, `CLAUDE.md` paragraph, `changelog.d/autoscaling.md`.

## Tests

- Unit: `app` validation table and revision; `nomadjob` golden `scale`; `metrics` rule table; `deploy`
  Register keeps the count, `Scale`, `ScaleState`, aliasing in `Copies`; `store` migration, actions, counts,
  versions; `service` pass (busy app, rolling, stopped, cooldown, bound, failure recorded, one move per app),
  status with counts, attention kind, activity words, promotion keeps the target's scale; `compose` overlay;
  `api` schema; `ui` Configure panel, form errors, actions list, Monitoring cell.
- Lab: gate step `autoscale` (`cmd/caravana/gate_autoscale_test.go`): project-free app `lab043` with one service
  `web` (whoami, memory 32 MB, domain `lab043.localhost`, health `/`), `scale {min 1, max 3, target
  requestsPerCopy 5, cooldownSeconds 60}`; a generator of about 25 requests a second through 127.0.0.1:8443 with
  the host as server name; wait for count 3 (job group count, `service_counts` via `getApp`'s status, two scale
  actions with reasons naming "requests a second"); deploy a new env value at 3: canaries 3, count 3 after;
  stop the generator; wait for 1 (two more actions); count never outside 1–3 (sampled every 2 s); failed
  requests ≤ 2 % of those sent. Budget about 10 minutes.
