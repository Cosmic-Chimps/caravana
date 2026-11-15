### Added
- **A backup that only runs when you ask.** A service's backup policy can now say `schedule: never`
  (in the API, the Compose overlay and the Compose form), for every engine: Caravana takes no backup by
  itself, and Back up now, pinning a backup, the backups you take by hand (kept as many as `keep`) and a
  scheduled restore of a pinned backup all work as before. Nothing on the needs-you list or in the
  notifications complains that no scheduled backup ran. The console says "No automatic backups (Back up
  now only)" on the Backups tab, Configure and Schedules, and the Backups tab's "Back up" forms have a
  Schedule choice. An empty schedule still means every day at 03:00 UTC, and `never` is refused as a
  restore schedule.
- API 0.42.0: `BackupPolicy.schedule` accepts `never`.
