### Fixed
- **No more 502s while a new copy starts outside a deploy.** Nomad registers a copy before it listens, and a copy
  placed by an autoscale step, Move work off or a reschedule isn't hidden the way a deploy hides its new copies,
  so the proxy sent it a few requests too early. Every router now retries a connection that failed on the next
  copy. Only a request that never reached the copy is retried, so a POST is never sent twice.

### Upgrade
- Each app picks the retry up at its next deploy; no installer rerun is needed.
