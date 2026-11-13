### Added
- **Pin a backup.** A backup can be pinned under a label such as `golden`, from "Pin…" on its row in
  the Backups tab or with `PUT /v1/apps/{id}/backups/{backupId}/pin`. Retention never deletes a
  pinned backup, neither its record nor its file in the destination, and only the unpinned ones count
  against how many to keep ("7 backups, plus 1 pinned"). A label is on one backup of a service at a
  time: pinning it again moves it. This holds for PostgreSQL, volume and SQLite backups.
- **Restore a pinned backup on a schedule.** A service's backup policy may have
  `restore: {schedule: "0 4 * * *", from: golden}`, or "Restore on a schedule…" on the Backups tab, or
  the two fields of the Compose form. At each time (cron, UTC) Caravana restores the backup pinned
  under that label, as the Restore button does, with no token: the clock is inside Caravana. It is
  meant for a demo that starts the day from the same state. A time that comes while a backup or
  restore is running is skipped, and one missed while Caravana was down runs once if it is less than
  an hour late. A label with no backup pinned, or a restore that fails, is a failed restore on the
  needs-you list and in the notifications. Restores now say who started them: you, or the schedule.
- **A PostgreSQL restore is one transaction**, so a restore that fails partway leaves the database as
  it was.
- API 0.39.0: `pinBackup` and `unpinBackup`, `pin` and `pinnedAt` on a backup, `trigger`, `pin` and the
  status `skipped` on a restore, and `restore` in a backup policy.

### Upgrade
- Apps with a PostgreSQL, volume or SQLite backup get the retention that spares pinned backups at
  their next deploy. Redeploy them before pinning anything, or an old backup may still be deleted by
  the previous retention. Nothing to rerun on the host.
