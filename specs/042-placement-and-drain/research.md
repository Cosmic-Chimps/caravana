# Research: Placement and drain

Facts checked in the lab (Nomad 2.0.7, one node) on 2026-11-17, with `nomad node meta apply` and `nomad job
plan` through the management token. The probe script was `.cache/placement-probe.sh` (not committed).

## R1 Dynamic node metadata and when the scheduler sees it

`nomad node meta apply -node-id <id> caravana.label.ssd=true caravana.server=lab-one` is applied on the client
at once (`node meta read` shows it) and reaches Nomad's servers a few seconds later: `node status -verbose`
showed it after 8–10 s, and a `job plan` run **right after** the apply still said `Constraint
"${meta.caravana.label.ssd} = true": 1 nodes excluded by filter`. Once the servers had it, the same plan
placed. Dynamic metadata survives a Nomad restart (the client keeps it in its state) and sits beside the
static `meta {}` of `nomad.hcl`. Consequence: a label save waits until the server-side record shows the
metadata (poll `Nodes().Info`, up to 15 s) before it answers, so a deploy that follows plans against it.

The CLI's `-unset` honoured only one key per call (`-unset a -unset b` left `a`); the API's
`NodeMetaApplyRequest{Meta: map[string]*string{"a": nil, "b": nil}}` is what Caravana uses, and the lab
step checks that two labels are removed in one write.

## R2 Dotted keys and `!=` against a missing key

`${meta.caravana.label.ssd}` resolves (dots in a meta key are fine in a constraint attribute). A constraint
`${meta.caravana.server} != "other"` **passes** on a node that has no `caravana.server` metadata at all
(server-side record `Meta: {}`), and filters a node whose value equals. So `avoid` is a `!=` on
`caravana.server`, which Caravana writes on every managed server, and nodes without it are never avoided.

## R3 Plan output for an impossible rule

`Jobs().Plan` answers `FailedTGAllocs[<group>].ConstraintFiltered` keyed by the constraint's text, exactly
`${meta.caravana.label.ssd} = true` or `${meta.caravana.server} != lab-one`, with the count of nodes it
excluded. That text is what `deploy.checkPlacement` parses back into the field. Lack of memory shows as
`DimensionExhausted`, not a constraint, and stays no refusal.

## R5 A volume follows the rules, or the copy has nowhere to go (cluster test, 2026-11-17)

The first cluster-test run of step 6c deployed a service with a volume and `require: {ssd: "true"}`: the
volume was created on whichever node Nomad picked (not the labelled one), and the copy stayed queued between
`${meta.caravana.label.ssd} = true: 2 nodes excluded` and `missing compatible host volumes: 1 nodes excluded`.
The plan check didn't refuse it, rightly: the volume didn't exist yet when the job was planned, so the
failure wasn't Caravana's constraints alone. Nomad's dynamic host volume create takes `Constraints` to pick
the node when `NodeID` is empty, so `ensureVolumes` now sends the service's placement constraints
(`nomadjob.PlacementConstraints`) with a new volume, and refuses on `services[i].placement` a volume that
already exists on a node whose metadata the rules don't allow (`nomadjob.AllowedOn`), since a volume never
moves.

## R4 Why not Nomad's own node drain (decision, no lab run)

`nomad node drain` migrates every service allocation on the node, with no way to leave a job out except
`ignore_system_jobs`. Caravana runs no system job, and three things on a server must not move: Traefik on
the first server (a service job constrained to the ingress meta: a drain would stop it and leave it pending,
taking the console down), a service with volumes (its dynamic host volume is on that node only: the drain
stops the copy and the replacement stays pending), and a published port (only the first server declares the
host network). Nomad's drain also places the replacement and stops the old allocation in one step, so it has
the same per-copy gap as an allocation stop. Caravana therefore moves work itself: the node is made ineligible
(`ToggleEligibility`), and each movable copy is stopped with `Allocations().Stop`, Nomad's "stop and
reschedule", which the scheduler places elsewhere because the node takes no work. The deadline and the
order are Caravana's. `LastDrain` is never set, so Nomad's "draining" state is not used; the console reads
eligibility and Caravana's own move record.
