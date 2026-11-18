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

## R5 · Requests during a scale-up (lab, observed, gate step `autoscale`)

Three runs of the gate step under about 25 requests a second: 4047/31, 3824/0 and 3799/4 sent/failed, every
failure a 502, and in the run that logged their times they fell at the two scale-up moments (1m02s and 2m03s
after the load started). Scale-downs lost nothing (the group's shutdown delay takes the copy out of Traefik
first).

The cause, verified (task 6rn5, 2026-11-18): a probe scaled a whoami job 1 → 3 under 25 requests a second while it
polled Nomad's registrations, task events and checks. Each new copy's service was registered 0.7 to 2.6 s before its
task's `Started` event (before even `Task Setup`: group services register when the allocation starts, not the task),
its check passed about 1.5 s after that, and every 502 fell between the registration and `Started`. A scaled copy
has no canary tags, and Traefik's Nomad provider (`watch=true`) routes to a registration at once: Nomad's service
registrations carry no health, and Traefik's own code says so. Three runs without a fix lost 5, 3 and 0 requests of
about 830; three runs with Traefik's retry middleware on the router lost none.

The fix: every router of a service ends with the middleware `<app>-<service>-retry` (`retry.attempts=3`), defined in
the service's own tags (`nomadjob.RetryMiddleware`). Traefik v3.7's retry retries a network error only while nothing
of the request reached the backend (a refused or failed dial; its `WroteHeaders`/`WroteRequest` trace), keeps the body
for the next try and passes any answer through, 5xx included, so a POST is never sent twice. With no wait between
tries, its round robin gives each try the next copy, so three tries reach a listening copy even when a step placed
two at once. Rejected: Traefik's active health check (a new server counts as up until its first check fails, and
every configuration change rebuilds the balancer) and its passive health check (it counts the app's own 5xx answers
and would take a single copy out of the pool). It is in the tags, not in Traefik's configuration, so an app gets it at
its next deploy without rerunning the installer, and an exported job carries it. It also covers a copy placed by a
move off a server or a reschedule.
