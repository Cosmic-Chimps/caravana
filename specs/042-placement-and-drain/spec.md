# Feature Specification: Placement and drain (say where services run, move work off a server)

**Feature Branch**: `alta/stout-canyon-xdrv` (worktree; CodeAlta task `jcfq`)
**Created**: 2026-11-17
**Status**: Draft
**Input**: CodeAlta task `jcfq` ("11 · Placement and drain", raised by the maintainer on 2026-11-12: "are we
using the greater features of Nomad?"): server labels, per-service placement rules translated into Nomad
constraints and affinities, moving work off a server, and the needs-you items for both.

## Why

Since 031 a Caravana has several servers, but the owner can't say where anything runs: the only placement
rules are the `spread` of a multi-copy service and Traefik's ingress constraint. Nomad's scheduler places by
constraints and affinities, and can be told to take no more work on a server. The plan's Phase 3 names
"placement and drain".

## User Scenarios & Testing

### User Story 1 — Label a server and require it (Priority: P1)

The owner gives server `two` the label `ssd=true` on its page. A service with `placement: {require: {ssd:
"true"}}` runs only on `two`. A rule no server satisfies is refused at deploy time, naming the field, before
anything is registered.

**Independent test**: in the cluster test (three servers), label server two, deploy a two-copy service that
requires the label: both copies run on two; deploy a service requiring `gpu=true`: 409 `placement_impossible`
on `services[0].placement.require.gpu`, and no job exists.

**Acceptance scenarios**
1. Given server `two` labelled `ssd=true`, when a service with `require: {ssd: "true"}` is deployed, then
   every copy of it runs on `two` and the deploy ends healthy.
2. Given no server labelled `gpu`, when a service with `require: {gpu: "true"}` is deployed, then the answer
   is 409 `placement_impossible` with the field `services[i].placement.require.gpu`, and no Nomad job was
   registered.
3. Given a label removed from the only server that had it, when the service is deployed again, then the
   deploy is refused the same way; its running copies keep running.
4. Given a label saved in the console, when the page is read again, then it shows the label, and the API's
   `Server.labels` has it.

### User Story 2 — Prefer and avoid (Priority: P1)

`placement.prefer` is an affinity: when more than one server fits, copies go to the preferred one while it has
room. `placement.avoid` names servers a service never runs on.

**Acceptance scenarios**
1. Given a three-copy service with `prefer: [{label: "ssd", value: "true"}]` and one server labelled so, when
   it is deployed, then at least one copy runs on that server (Nomad's `spread` still spreads the rest).
2. Given `avoid: ["three"]`, when the service is deployed, then no copy runs on `three`.
3. Given `avoid` naming every server that accepts work, when the service is deployed, then it is refused 409
   `placement_impossible` on `services[i].placement.avoid`.

### User Story 3 — Move work off a server (Priority: P1)

On a server's page the owner chooses "Move work off…", sees what will move and what will stay, picks a
deadline and confirms. The server stops taking new work, each movable copy is moved to another server one at a
time, and the page follows the move until the server is empty of what can move. "Accept work again" lets the
server take work again.

**Acceptance scenarios**
1. Given a three-copy service spread over three servers and a two-copy service, when work is moved off `two`
   while the first service's domain is called every half second, then every copy of both leaves `two` within
   the deadline, the calls keep answering, and the server's `move.state` is `done`.
2. Given a service with a volume on `two`, when work is moved off `two`, then that service's copy stays, the
   move reports it ("its data is on this server") and still ends `done` once everything else moved.
3. Given the first server (Caravana and Traefik run there), when work is moved off it, then Traefik and
   Caravana are untouched, the apps' copies move, and the console keeps answering.
4. Given a move going, when the owner cancels it (Accept work again), then the copies already moved stay
   where they are, the server takes work again, and the move is `cancelled`.
5. Given a move whose deadline passes with copies left (nowhere to put them), then the move is `incomplete`,
   the server page says which copies are still there, and the needs-you list has `server_move_incomplete`.
6. Given only one server that accepts work, when work is moved off it, then the request is refused 409
   `no_other_server`.

### User Story 4 — Placement in the console and in Compose (Priority: P2)

Configure shows a service's placement and lets the owner set the three rules for an app defined in the
console; an app kept as a Compose file takes them from the overlay (`services.<svc>.placement`), and the
Compose form keeps them through a deploy.

### User Story 5 — Needs you (Priority: P2)

- `copies_unplaced`: a service with placement rules whose copies Nomad can't place (its job has queued
  allocations): "web (shop): 2 copies can't be placed: no server that accepts work matches its placement rules,
  or none has room."
- `server_move_incomplete`: a move that passed its deadline with copies left: "two: the move-off passed its
  deadline; 2 copies still run there."

### Edge cases

- **A single copy moves with a gap.** Nomad places the replacement and stops the old copy in one step, so a
  one-copy service is unreachable while its new copy starts elsewhere, as with Nomad's own drain. A
  two-copy service keeps answering (the copies move one at a time). The confirmation page says so.
- **What never moves**: a service with volumes (its data is on that server), a service with a published port
  (the port is on the first server's address), Traefik (the ingress), Caravana's own helper jobs (a backup or
  schedule run that ends on its own), and anything Caravana didn't deploy. Each is listed with its reason.
- **A busy app** (a deploy or a volume run holds its lock) is tried again until the deadline.
- **Caravana restarts during a move.** The server stays ineligible; the move resumes at start from the
  copies still on the server, with the same deadline.
- **Labels and Nomad's servers.** A label written through the node's client API reaches Nomad's servers a
  few seconds later (research R1); a save waits for them, up to 15 s, so a deploy right after it plans
  against the new label. A server that is down keeps its stored labels and gets them when it comes back.
- **`avoid` and unlabelled servers.** `avoid` is a `!=` constraint on the node meta `caravana.server`, which
  Caravana writes on every managed server. A node without it (the lab's, a server not yet reconciled) passes
  the constraint (research R2), so `avoid` can only keep copies off servers Caravana manages.
- **Promotion and copy.** Placement rules name labels and servers, which belong to the cluster, not to an
  environment: a promotion takes the source's rules (like `dns`); Copy to keeps them.
- **Label names and values.** A name is `^[a-z][a-z0-9-]{0,31}$`, a value `^[A-Za-z0-9._:/-]{1,64}$`; at most 16
  labels per server, 8 `require`, 8 `prefer`, 16 `avoid` per service. A bare label in the console ("ssd")
  means `ssd=true`.

## Requirements

- **FR-001** A server has labels (name → value) stored with the server and written to its Nomad node as
  dynamic node metadata `caravana.label.<name>` plus `caravana.server=<name>`; Caravana keeps the node's
  metadata equal to the stored labels (at save, when a server becomes ready, at start, and every 5 minutes).
- **FR-002** `app.Service.Placement{Require, Prefer, Avoid}` is translated by `nomadjob.Translate` (pure,
  golden-tested) into the group's `constraint` and `affinity` blocks, written by the HCL export, and applied
  to the service's schedule runs too.
- **FR-003** A deploy of an app with placement rules first asks Nomad to plan the job; a group that can't be
  placed because of one of Caravana's own constraints is refused 409 `placement_impossible` naming the field,
  and nothing is registered. Lack of room is not a refusal (as today).
- **FR-004** `drainServer` marks the server ineligible and moves its movable copies one at a time with
  Nomad's allocation stop (which reschedules elsewhere), waiting for each replacement to run and pass its
  checks; `undrainServer` cancels a move and makes the server eligible. The ingress server is drained of apps
  only; nothing ever moves Traefik, Caravana, a service with volumes or a published port, or a batch run.
- **FR-005** API 0.46.0: `Server.labels`, `Server.labelsApplied`, `Server.move`, `putServerLabels`,
  `drainServer`, `undrainServer`, `Placement` on services and the Compose overlay, codes
  `placement_impossible`, `server_moving`, `server_not_ready`, `no_other_server`. Deploy tokens may deploy
  placement rules for their own app and are denied every server call by the default deny.
- **FR-006** Needs-you kinds `copies_unplaced` and `server_move_incomplete`, worded once (`ProblemParts`),
  notified like the rest.
- **FR-007** The console: labels on the server page (edit) and the list (read); Move work off (confirmation,
  progress, cancel, Accept work again); Configure's Placement section; the Compose form keeps the overlay's
  placement. Forms work without JavaScript.
- **FR-008** Caravana's Nomad policy gains `node { policy = "write" }` (node metadata, eligibility; the
  installer applies it on a rerun). Without it the console says Nomad refused and to run the installer again.

## Success Criteria

- **SC-001** In the cluster test: a service with `require: {ssd: "true"}` runs only on the labelled server;
  `prefer` lands copies on the preferred server; an impossible rule fails the deploy naming the field.
- **SC-002** In the cluster test: moving work off a server moves every movable copy within the deadline while
  the spread app keeps answering, and the volume service on it is reported, not moved.
- **SC-003** In the lab (one node, unmanaged): `placement_impossible` names the field, `avoid` and `prefer`
  deploy healthy, the export of a placed app plans with no diff, and the console's forms work (gate step
  `placement`, console step `console-placement`).
- **SC-004** Every existing app deploys exactly as before (goldens and revision tests unchanged).

## Out of scope

Node pools and datacenters; `distinct_hosts` and other Nomad constraint operators; placement by resources
(memory, CPU); moving a volume between servers; Nomad's own `node drain` (see plan D6 for why); a scheduler
settings page (`SchedulerScreen`); labels from the installer's command line; per-task constraints.
