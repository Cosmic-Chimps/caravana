# Autoscaling: a service adds copies under load and removes them when it's quiet

A service runs a fixed number of copies (`replicas`) unless it has a `scale` rule. With one, Caravana changes the
number of copies itself, between a minimum and a maximum, to keep one value near a target. There is no autoscaler to
install: Caravana already measures every service each minute (Monitoring), and the decision runs right after that.

## Say how a service scales

Through the API (`services[i].scale`), the Compose overlay (`services.<name>.scale`) or Configure's Scaling panel:

```json
{
  "name": "web",
  "replicas": 2,
  "scale": {
    "min": 1,
    "max": 4,
    "target": {"requestsPerCopy": 50},
    "cooldownSeconds": 300
  }
}
```

- `min` and `max`: the service never runs fewer or more copies (1 to 20).
- `target`: exactly one of
  - `cpu`: percent of the CPU the copies reserve (`resources.cpuMHz`), 10 to 200;
  - `requestsPerCopy`: requests a second per copy, measured at the proxy, so the service needs a domain;
  - `p95`: how long the slowest 5 % of requests may take, in milliseconds, also measured at the proxy.
- `cooldownSeconds`: how long Caravana waits after a step, or after a deploy, before it judges again (60 to 3600,
  300 when left out).
- `replicas` is the **starting count**: the first deploy runs that many (or `min` when it is left out). It must lie
  within `min` and `max`.

A service with a volume or a published port runs one copy and can't scale; the deploy is refused naming
`services[i].scale`. A `requestsPerCopy` or `p95` target on a service without a domain is refused naming
`services[i].scale.target`: scale it on `cpu` instead.

## How it decides

After each sample (every minute), Caravana looks at every scaled service of every running app, using only the
samples taken since its last step or deploy:

- **One copy more** when the value over the last 3 minutes (at least 2 with numbers) is above the target, and the
  service is under `max`.
- **One copy fewer** when, over the last 5 minutes, every minute has numbers and the load spread over one copy fewer
  would stay under 80 % of the target, with no single minute over it (for `p95`: the slowest 5 % stay under half the
  target), and the service is over `min`.
- Never more than one step at a time, never inside the cooldown, never past `min` or `max`. A count outside them
  (changed in Nomad by hand) goes back to the nearest bound at once.

Nothing happens while the app is stopped, while a deploy rolls out, while the last step's new copies are still being
placed, or while the app is busy with a restart or a volume backup. With no numbers (Traefik's metrics off, Nomad
unreachable) nothing changes either.

A step is Nomad's own scale: removed copies leave the proxy before they stop. A new copy is not a canary, so the proxy
sends it requests from the moment Nomad registers it; in the lab a scale-up lost a few requests (0 to 31 of about
4000 per run, all 502s at the two scale-up moments) while the new copy started. Nomad records each step as a new version of the job; the console
and the API show the copies as the version of the deployment they run, so the history only has deployments in it.

## What you see

- Every step is an app action `scale` with the count before and after and the reason, in Recent actions, the
  Activity feed and `GET /v1/apps/{id}/actions`: "Scaled `web` 1 → 2: 62 requests a second per copy over the last
  3 minutes, target 50", by "on its own". A step Nomad refused is a failed action with the reason, and the cooldown
  counts from it.
- Monitoring shows a scaled service's copies with its range, "2 copies (1–3)", and the place's recent steps.
- The app's status counts the copies the service has now, so a service scaled down to 1 isn't "degraded".
- **Needs you** (`scale_at_max`): a service at its maximum whose value stayed above the target over the last
  10 minutes. Raise the maximum, give the service more CPU, or add a server. It is notified like every needs-you item.

## Deploys

A deploy keeps the number of copies the service has now (kept within a new `min` and `max`), and rolls out as many
new copies beside them before the old ones stop. The starting count only applies to the first deploy; Start after a
Stop runs the job as it was, at its count. Removing `scale` makes the service run `replicas` again; Configure's **Stop scaling** keeps the
count it has as its `replicas`.

The exported job (Configure › Export) has the starting count, with a line in its header saying the service scales.

## What it never does

Scale to zero, take steps of more than one, scale on several values at once or on memory, scale a service with a
volume, or use Nomad's own autoscaler.
