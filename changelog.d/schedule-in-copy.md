### Added
- **A schedule that runs in the running copy.** A schedule can now say where its command runs:
  `target: copy` runs it with Nomad's exec in the service's oldest healthy copy (or in every healthy
  copy, one after the other, with `allCopies: true`) instead of in a fresh one. It works on a service
  with volumes too, which a fresh copy still can't (two writers on one volume would be unsafe): a
  cleanup command, a cache purge, a nightly reset of a demo. The limit is 1, 2 or 5 minutes (1 by
  default), it has the copy's own memory and always skips a slot while its last run goes. Like Run a
  command, it keeps **no output**: a run records its status, exit code, why it failed and the copies
  it ran in, and each copy gets a `run` entry in the app's Recent actions "on a schedule". A failed
  run is a needs-you item and a notification, as before. While the app is stopped a slot is recorded
  as skipped ("the app is stopped"), and a slot with no healthy copy waits for one. In the console,
  New schedule has "Run in: A new copy / The running copy" and "In every copy", and a run's page says
  where it ran and that its output isn't kept.
- API 0.43.0: `target` and `allCopies` on `NewSchedule` and `Schedule`, `target` and `copies` on
  `ScheduleRun`, `schedule` as an app action's trigger, 404 `output_not_kept` for a run in the running
  copy, and 409 `no_running_copy` from `runSchedule`.
