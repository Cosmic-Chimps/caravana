### Changed
- **The console and Caravana's watchers follow Nomad's event stream.** One subscription tells Caravana
  when a copy, a rollout, a job or a server changes, so pages refresh, rollouts end and schedule runs and
  backups are recorded within moments instead of on the next poll. Every watcher still reads Nomad on a
  slow timer as a safety net, so nothing stops working if the stream is down: Caravana logs that it lost
  the stream once, reconnects by itself and looks at everything again when it is back. The
  all-allocations query that ran while a page was open is gone.

### Upgrade
- Nothing to do: the token's existing `read-job` and `node` rules cover the stream's topics.
