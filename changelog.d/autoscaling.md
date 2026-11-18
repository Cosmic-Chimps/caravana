### Added
- **Autoscaling.** A service may carry `scale: {min, max, target, cooldownSeconds}` (API, Compose overlay or
  Configure's Scaling panel), with one target: `cpu` (percent of its reservation), `requestsPerCopy` or `p95`
  (milliseconds; both need a domain). After each minute's sample Caravana adds one copy when the value stayed over
  the target, removes one when the load would stay well under it on one copy fewer, and waits the cooldown between
  steps, never past `min` or `max`. Each step is an app action `scale` with its counts and reason, shown in Recent
  actions, the Activity feed and Monitoring. A deploy keeps the count the service has now; `replicas` becomes the
  starting count. A service at its maximum and still over its target is on the needs-you list (`scale_at_max`).
  A service with a volume or a published port can't scale. See `docs/autoscaling.md`. API 0.47.0.
