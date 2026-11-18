# Research: Autoscaling

Lab, observed, Nomad 2.0.7 in `caravana-lab`, 2026-11-18, with a copy of `deploy/dev/whoami.json` named
`scaleprobe` (one group `web`, `Count 1`, `Canary 1`, `AutoRevert`, `AutoPromote`).

## R1 · What `POST /v1/job/<id>/scale` does

- It makes a **new job version** (v0 → v1), bumps `JobModifyIndex`, and keeps the job's meta as it was
  (`caravana.deployment` unchanged). The group's `Update.Canary` stays what it was.
- It starts a **Nomad deployment** for the new version with `DesiredCanaries 0`: no canaries, the new copies
  are placed beside the old ones and the deployment ends `successful` when they pass their checks (about 20 s
  for whoami).
- The copies that already ran are **updated in place to the new version** (allocation `66d3f754` went from
  job version 0 to 1, then 2, then 3 across scales): no restart, but their `JobVersion` changes.
- Scaling down stops the newest copies (the shutdown delay first, so Traefik drops them before they stop),
  and the oldest copy keeps running; again a new version and a short deployment.
- `GET /v1/job/<id>/scale` keeps an event per scale with `Count`, `PreviousCount`, `Message`, `Meta` and the
  time. Caravana keeps its own record (an app action), so it doesn't read these.

Consequence: Caravana must not show a scale's job version as if it were a deployment (US4.4). Each scale
action stores the job version Nomad gave it and the version of the deployment it scaled; copies of a scale's
version read as that deployment's version (plan D6).

## R2 · A deploy while scaled

With the group at 3 (by scale), registering a new version with `Count 3, Canary 3` rolls out 3 canaries
beside the 3 old copies, auto-promotes and ends with 3 copies of the new version. So a deploy keeps the count
by registering the current count, and the canaries follow it (`Canary = Count`, as `Translate` already
writes). `PreserveCounts` was not used: it would keep the count but leave `Canary` at the starting count, so a
deploy of a service scaled to 3 with `replicas: 1` would roll out one canary at a time instead of blue/green.

## R3 · The policy

A token made from `deploy/install/caravana-policy.hcl` (`submit-job`, no `scale-job`) scaled the job and read
its scale status: `submit-job` covers scaling. No policy change.

## R4 · Not tried, expected from Nomad's documented behaviour

A scale-up whose new copies fail their checks fails its Nomad deployment and, with `AutoRevert`, Nomad
reverts to the last stable version (the count before the scale). Caravana reads the count from the job on the
next tick and records nothing more; the failed step is visible as the copies' failures.
