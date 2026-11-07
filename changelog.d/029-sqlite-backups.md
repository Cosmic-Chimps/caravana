### Added
- **SQLite backups that don't stop the app.** A service that keeps its data in one SQLite file on a
  volume can be backed up with the `sqlite` engine: `backup: {engine: sqlite, file: /data/app.db}` in
  its definition or Compose overlay, **SQLite file** in the Compose form, or **A SQLite file instead** on
  the Backups tab. Each backup is a consistent copy made with `VACUUM INTO` while the service keeps
  running, checked with `PRAGMA quick_check`, gzipped and uploaded as `<time>.sqlite.gz` to the backup
  destination, with the same schedule, retention, Back up now, needs-you items and notifications as the
  other engines. A restore downloads and checks the backup while the service runs, then stops it only for
  the few seconds it takes to put the file in place, keeping the file's owner and mode. In WAL mode the
  copy never blocks the app's writes; with a rollback journal they wait while it reads. A service with a
  sqlite backup runs two small idle helper tasks beside it (`caravana-sqlite` and `caravana-upload`,
  48 MB reserved together). API 0.32.0 (`BackupPolicy.file`, the `sqlite` engine).

### Upgrade
- Every app with a volume gets an updated `caravana-volumes` task on its next deploy;
  an app needs one deploy with this version before its first sqlite restore (setting the policy is one).
  `CARAVANA_SQLITE_IMAGE` (`-sqlite-image`) replaces the pinned `alpine/sqlite` image if you mirror
  images.
