### Added
- **MySQL, MariaDB and MongoDB are backed up without stopping them.** `backup: {engine: mysql}` (for the official
  `mysql` and `mariadb` images, or one built on them) and `backup: {engine: mongodb}` (the official `mongo` image)
  work like PostgreSQL's: each scheduled run and each Back up now dumps the live database and uploads it to the
  backup destination (`<time>.sql.gz`, `<time>.archive.gz`), and a restore loads a dump back into the running
  service. Records, pins, retention, restore schedules, `schedule: never`, the needs-you items, notifications and
  the built-in schedules list work as for PostgreSQL. The login is the one the image reads to start
  (`MARIADB_ROOT_PASSWORD` and friends, `MONGO_INITDB_ROOT_USERNAME` and `MONGO_INITDB_ROOT_PASSWORD`) and reaches the
  backup job only through its own Nomad Variable, never in its arguments; a password given as plain environment is a
  deploy warning, as for PostgreSQL. In the console the Backups tab offers "Back up daily" on these images and the
  Compose form has the two engines. API 0.51.0 (`mysql` and `mongodb` engines). See `docs/database-backups.md`.

### Changed
- **A PostgreSQL backup lists only its own objects when it keeps the newest ones.** The upload step now looks at
  files ending in the engine's extension (`.dump`) and is told it, which is what lets the three engines share one
  script. An app gets it at its next deploy.
