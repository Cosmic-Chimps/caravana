# Feature Specification: Autoscaling (a service adds and removes copies from Caravana's own metrics)

**Feature Branch**: `alta/flowing-sequoia-942b` (worktree; CodeAlta task `97ks`)
**Created**: 2026-11-18
**Status**: Draft
**Input**: CodeAlta task `97ks` ("13 · Horizontal autoscaling from Caravana's own metrics", raised by the
maintainer on 2026-11-12): per-service `scale: {min, max, target, cooldown}`, the decision made by Caravana on
the sampler's tick with a pure rule, Nomad's scale API, each step recorded and shown, deploys keep the scaled
count, a needs-you item when a service stays at its maximum and over its target.

## Why

A service runs a fixed number of copies (`replicas`). Since 025 Caravana measures requests, the slowest 5 %,
CPU and memory per service every minute, and Nomad changes a group's count with one call (`Jobs().Scale`)
without a deployment of a new image. With both, a service can grow under load and shrink when it is quiet,
with no autoscaler service to run (constitution I).

## User Scenarios & Testing

### User Story 1 — A service grows under load and shrinks after it (Priority: P1)

The owner gives service `web` `scale: {min: 1, max: 3, target: {requestsPerCopy: 50}, cooldownSeconds: 300}`.
Under load it goes 1 → 2 → 3 copies, one step at a time, with the cooldown between steps; when the load is
gone it goes back to 1. Requests keep answering throughout (a removed copy leaves Traefik before it stops; a new copy
is routed to from its registration, before it listens, and the router's retry middleware sends a request it refuses to
another copy: research R5).

**Independent test**: the lab's gate step `autoscale`: a whoami service with a domain, a load generator in the
test process; the count reaches 3, then 1 after the load stops; every step is an app action `scale` with its
from, to and reason; the generator's requests all answer (a handful of failures at most, counted and bounded).

**Acceptance scenarios**
1. Given a service at 1 copy whose requests per copy stay above the target for the up window, when the
   sampler ticks, then Caravana scales the group to 2 and records the step (`scale`, 1 → 2, the measured value,
   the target).
2. Given a step less than `cooldownSeconds` ago, when the value is still above the target, then nothing
   changes until the cooldown has passed.
3. Given a service at 3 copies whose load, spread over 2 copies, would stay under 80 % of the target for the
   whole down window, when the sampler ticks, then it scales to 2; the same again goes to 1; never below `min`.
4. Given a service at `max`, when the value is above the target, then nothing is scaled.

### User Story 2 — A deploy keeps the scaled count (Priority: P1)

**Independent test**: in the gate step, while the service runs 3 copies, deploy a new environment variable:
the deployment rolls out 3 canaries and ends with 3 copies of the new version; `min`/`max` never crossed.

**Acceptance scenarios**
1. Given a scaled service at 3 copies, when the app is deployed, then the new version runs 3 copies (and 3
   canaries during the rollout), not `replicas`.
2. Given a deploy that changes `max` from 3 to 2 while 3 copies run, then the new version runs 2.
3. Given a first deploy, then the service starts at `replicas` (its starting count), or `min` when
   `replicas` is unset, within `min`–`max`.
4. Given a service whose `scale` is removed, when deployed, then it runs `replicas` copies again.

### User Story 3 — Say how a service scales, in the definition, the Compose overlay and the console (P1)

**Acceptance scenarios**
1. `deployApp` and `createApp` take `services[i].scale`; the Compose overlay takes `services.<svc>.scale`.
2. Each refusal names its field (`services[i].scale...`, `services.<svc>.scale...` for Compose): `min` < 1,
   `max` < `min` or > 20, no target or more than one, a target out of range, `cooldownSeconds` out of 60–3600
   (0 means the default 300), `replicas` outside `min`–`max`, a service with volumes or a published port
   (`services[i].scale`: it runs one copy), a `requestsPerCopy` or `p95` target on a service without a domain
   (`services[i].scale.target`: requests are measured at the proxy).
3. Configure has a Scaling panel per service: what it does in a sentence, and a form (min, max, target kind
   and value, cooldown) that saves as a deploy with the page's revision; an app edited through its Compose
   file shows it read-only, and the Compose form keeps the overlay's `scale`.

### User Story 4 — See every step (Priority: P2)

**Acceptance scenarios**
1. The app's Recent actions and the Activity feed show "Scaled `web` 1 → 2: 62 requests a second per copy over
   the last 3 minutes, target 50" with "on its own" as the trigger.
2. Monitoring's apps table shows a scaled service's copies as "2 (1–3)", and the page lists the place's recent
   scale steps.
3. The app's status counts the copies the service has now, not `replicas`: a service scaled to 1 of
   `replicas: 3` is healthy, not degraded.
4. A copy's version and the app's live version read as the deployment's version: Nomad's scale makes a new job
   version, which the console and the API never show as if it were a deployment.

### User Story 5 — Needs you when it can't grow any more (Priority: P2)

**Acceptance scenarios**
1. Given a service at `max` whose value stayed above its target over the last 10 minutes, then the needs-you
   list has `scale_at_max` ("`web` of `shop` is at its 3 copies and still over its target: raise its maximum,
   give it more CPU, or add a server"), and a notification `problem.opened` is sent.
2. When it drops under the target or below `max`, the item goes, and `problem.resolved` follows.

### Edge cases

- A stopped app, an app with a deployment rolling out, an app whose Nomad deployment is running (the last
  scale is still placing copies), and an app whose lock is held (a volume run, a restart) are not scaled on
  that tick.
- No metrics (Traefik off, Nomad unreadable, too few samples since the last step or deploy) means no change.
- A count outside `min`–`max` (changed outside Caravana) is brought back to the nearest bound, recorded with
  that reason.
- A scale Nomad refuses is a failed `scale` action with Nomad's error in Caravana's words; the cooldown counts
  from it, so a refusal isn't retried every tick.
- A scale-up whose new copies fail their checks is reverted by Nomad (auto-revert); the next tick reads the
  count Nomad has.

## Requirements

- **FR-001** `app.Service.Scale` (omitempty, so no existing revision moves), validated as in US3.
- **FR-002** The decision is a pure function in `internal/metrics` with table tests: one step at a time, the
  cooldown, the windows, never past `min`/`max`.
- **FR-003** The sampler's tick runs the decision for every scaled service of every app that isn't stopped,
  then calls Nomad's scale API with a message and records an app action `scale`.
- **FR-004** Every deploy (deploy, redeploy, rollback, promotion, webhook, start) registers each scaled
  service with its current count clamped to `min`–`max`; the first deploy uses the starting count.
- **FR-005** Needs-you kind `scale_at_max`, worded once in `service.ProblemParts`.
- **FR-006** API: `ServiceScale`, the overlay's `scale`, the action `scale` with `fromCount`, `toCount` and
  `reason`, the trigger `autoscale`. No new error code (refusals are `invalid_field`).
- **FR-007** No new Go module, no runtime dependency, no Nomad policy change (`submit-job` covers scaling,
  research R3).

## Success criteria

- The gate step `autoscale` passes in the lab: 1 → 3 under load within a few minutes, back to 1 after the
  cooldown, each step recorded with its reason, requests answering throughout, a deploy at 3 keeps 3.
- Every CI check passes.

## Out of scope

- Scaling to zero, steps larger than one, several targets at once, memory as a target, predictive or
  scheduled scaling, scaling services with volumes, Nomad's own autoscaler and its scaling policies.
