# Plan 046: MySQL, MariaDB and MongoDB dumps

Spec: `spec.md`. Task `qgmm`. Branch: the worktree branch `alta/balmy-elbrus-rj6w`.

## Constitution check

- I Small by default: no new module, no new runtime dependency. The dump tools come from the service's own
  image; compression and upload from the pinned rclone image already used.
- II One model: two more values of `app.Backup.Engine`. The jobs are the existing helper jobs
  `<app>_<service>_backup` and `<app>_<service>_restore` (010), named in the constitution: no amendment.
  `nomadjob` stays pure, with goldens for each new job.
- III Safe deploys: unchanged; the backup job pins the service's digest as 010 does.
- IV Fail loudly: every refusal names its field (D10); a missing dump tool fails the run with a sentence that names
  the tool (D9).
- V API-first: `api/openapi.yaml` 0.51.0 in the same change; the console calls the same service methods.
- VI Nomad-native: jobs as JSON through the official client; the export is unchanged (backup jobs aren't exported).
- VII Secure: passwords only in the job's own Nomad Variable, never in the job, argv, a log or an answer (D5); the
  plain-env warning (QA #36) covers the new names.
- VIII Plain, tested Go: table tests, goldens, the scripts run under a real `sh` with fake tools.

## Decisions

- **D1 Dump engines.** `postgres`, `mysql` and `mongodb` are the engines that run as Nomad jobs and reach the
  database over its port. `app.DumpEngine(engine) bool` (and `Backup.Dumps()`) answers it; every place that tested
  `Engine == BackupPostgres` to mean "a Nomad job" uses it (deploy `SyncBackups`, `BackupJobIDs`, service
  `postgresBackup`, `recordApp`, `recordRestores`, the console's kind). Places that mean Postgres itself keep the
  name.
- **D2 One job shape, a recipe per engine.** `nomadjob.BackupJob` / `RestoreJob` keep their groups and tasks
  (`dump` + `upload`, `download` + `restore`); a pure per-engine recipe gives the dump and restore commands, the
  object's extension, the host/port variable names and whether the upload gzips. The Postgres tasks are unchanged.
- **D3 Objects.** `$BACKUP_PATH/<ts><ext>`: `.dump` (postgres, unchanged), `.sql.gz` (mysql), `.archive.gz`
  (mongodb). The upload task gets `BACKUP_EXT`; retention lists only files ending in it and a pin matches
  `<ts><ext>`. Expired names map back to the record ID by the digits before the first `.`.
- **D4 Compression outside the database image.** The mysql dump task writes plain SQL to
  `/alloc/data/backup.dump`; the upload task (rclone's busybox) gzips it when `BACKUP_GZIP=1`; the restore's
  download task gunzips. The database image needs only its client. MongoDB compresses itself (`--archive --gzip`).
- **D5 No password in argv.** The dump/restore scripts write the login to a file in the task's secrets dir
  (`$NOMAD_SECRETS_DIR`, `umask 077`): a `[client]` option file for mysql (`--defaults-extra-file`, read by both
  `mysqldump` and `mariadb-dump`; `MYSQL_PWD` is deprecated in MySQL 8) and a `--config` YAML file for mongodb
  (`password:`; database tools 100.3+, in the mongo 4.4+ images). Values are written double-quoted with `\` and `"`
  escaped. The scripts use `$VAR` and `$(...)` only (Nomad interpolates `${...}`).
- **D6 Variable items.** Postgres unchanged (`PGUSER`, `PGPASSWORD`, `PGDATABASE`; template `PGHOST`, `PGPORT`).
  mysql and mongodb: `DB_USER`, `DB_PASSWORD`; the template renders `DB_HOST`, `DB_PORT` from the service's main
  registration (skipping canaries), as 010 does.
- **D7 What is dumped.** mysql: every database the login sees but `information_schema`, `performance_schema`,
  `mysql` and `sys`, with `--databases` (the dump creates and selects each, so a restore needs no name),
  `--single-transaction --routines --events --triggers --no-tablespaces` (no PROCESS privilege needed). The list is
  read in its own step so a failed login fails the run (no `pipefail` in every image's `sh`). No database: the dump
  is one SQL comment, and its restore changes nothing. mongodb: everything `mongodump` dumps, with
  `--authenticationDatabase admin`.
- **D8 Restore.** mysql: the dump through the client; each table is dropped and recreated (`DROP TABLE IF EXISTS`,
  the tools' default), tables made since stay, as `pg_restore --clean`. MySQL DDL commits, so it is not one
  transaction (said in the docs). mongodb: `mongorestore --archive --gzip --drop --nsExclude 'admin.*'
  --nsExclude 'config.*'`: each collection in the dump replaced, users and internal state left as they are.
- **D9 A missing tool.** Caravana doesn't look inside an image before it runs, so this can't be a field refusal at
  deploy. The dump and restore scripts check first and fail with "the image has neither mariadb-dump nor mysqldump"
  / "the image has no mongodump" (mongorestore likewise); the run's record, the needs-you item `backup_failed` and
  its notification carry that sentence. Deviation from the task's wording, recorded here.
- **D10 Credentials and refusals.** By name, as Postgres (values may be secrets). mysql: `MARIADB_ROOT_PASSWORD`,
  then `MYSQL_ROOT_PASSWORD`, logs in as `root`; else the pair `MARIADB_USER`+`MARIADB_PASSWORD`, then
  `MYSQL_USER`+`MYSQL_PASSWORD`. mongodb: `MONGO_INITDB_ROOT_USERNAME` and `MONGO_INITDB_ROOT_PASSWORD`. Missing →
  field error on `services[i].backup` naming the variables. No port → the existing refusal. No destination → the
  existing refusal. `app.BackupPasswordNames(engine)` lists the password names for the plain-env warning
  (`service.PlainBackupPasswords`) and the template check.
- **D11 Store.** Migration `0036_dump_engines.sql` rebuilds `backups` and `restores` with the engine CHECK widened
  to `mysql` and `mongodb` (as 0022 and 0029 did). Records of dump engines carry their engine (`runRecord`,
  `restoreBackup`); an empty engine still means postgres.
- **D12 API 0.51.0.** `mysql` and `mongodb` in the engine enums (`BackupPolicy`, `Backup`, `Restore`, the overlay),
  descriptions of what each reads. No new route or code.
- **D13 Console.** The Compose form's engine select offers "MySQL or MariaDB (the service keeps running)" and
  "MongoDB (the service keeps running)"; the Backups tab's "no backup" form offers the dump for a `mysql`,
  `mariadb` or `mongo` image with a port (labels "MySQL", "MariaDB", "MongoDB"); a row's What and a built-in
  schedule's command name the engine (`mysqldump`, `mongodump`); the Apps home counts `mongo` among database images
  and any dump engine as a database.
- **D14 Templates.** `templates.Check` refuses a plain-env password of any dump engine.

## Lab

Gate steps `mysql-backup-restore` (MariaDB 11.4) and `mongodb-backup-restore` (MongoDB 8.0): the password is a
secret holding a sentinel; the policy runs every minute with `keep: 20`; the step waits for a timer run to succeed,
then Back up now while a writer inserts a row every 200 ms (no insert fails), A → back up → B → restore → A, and
searches the backup and restore jobs, the variable-free job JSON, the export, every API answer it read and
Caravana's log for the sentinel. The lab VM has 2 GiB: each step runs alone with small reservations.

## Deviations

Phase A (T001–T006):

- **One login rule, `app.FindDumpLogin`.** D10 only named `BackupPasswordNames`. Validation, the deploy's
  credential resolution (`service.syncBackups`) and the Postgres defaults (`POSTGRES_USER` is `postgres`,
  `POSTGRES_DB` is the user) are one pure function in `internal/app/dumplogin.go`, which asks a lookup for each
  name: validation's lookup knows only that a name exists (a secret's value isn't in the model), the service's
  has the values. `DumpLoginHint` words the refusal per engine.
- **`echo` can't write the option file.** dash's `echo`, the `sh` of Debian and Ubuntu images, reads
  backslash sequences as escapes, so a password with a backslash lost one on the way to the file. The scripts write the values with
  `printf 'password="%s"\n'` (the script test with `pa"ss\w` found it). A value with a line break is refused
  before any file is written, since it could add a line to the option or config file.
- **The upload script copies instead of piping.** With `BACKUP_GZIP=1` it makes `backup.dump.gz` in a file and
  uploads that, because a pipe into `rclone rcat` would hide a failed `gzip` without `pipefail`. Retention lists
  with `case "$f" in *"$BACKUP_EXT")` instead of `grep '\.dump$'`, so no extension is a regular expression, and
  the script no longer fails when a folder holds no file of the engine's extension. Every backup job's upload task
  now has `BACKUP_EXT` and `BACKUP_GZIP` in its env (Postgres: `.dump` and `0`), so the Postgres goldens changed in
  the script and those two lines; an app gets them at its next deploy.
- **The restore's download.** Postgres and MongoDB keep rclone's own arguments (`copyto dest:${NOMAD_META_backup_key}
  …`, Nomad interpolates the job's meta, as 010 did). MySQL's is `sh -c` with rclone and `gunzip`, reading the same
  meta from `$NOMAD_META_backup_key` in the task's environment, so no script has a `${`.
- **MySQL's tools come as a pair.** The dump script takes `mariadb-dump` only when `mariadb` is there too, and
  `mysqldump` only with `mysql`; the restore needs only the client and fails with "the image has neither mariadb
  nor mysql". D9's sentence for the dump is as written.
- **Records of every dump engine carry their engine**, Postgres' too (`runRecord` now takes the engine; before,
  the store defaulted it).
- **`postgresBackup` is `dumpBackup`** (service); `recordApp` maps an expired file to its record with
  `expiredRecordID` (the digits before the first dot).
- **Checked in the lab** (Phase C, 2026-11-20): MariaDB 11.4's client connects through the option file without TLS
  options, and the Mongo tools read `--config`; no script changed. Not covered: a dump that is one SQL comment (an
  empty server).

Phase B (T007, T008):

- **Images are read by their last path segment.** `dumpEngineOfImage` (`internal/ui/backups.go`) parses the image with
  `registry.Parse` and takes the last segment of the repository (`library/mariadb` → `mariadb`), so `ghcr.io/acme/mysql`
  and `mongo-express` (not mongo) are told apart as Postgres' `HasSuffix("/postgres")` did. The Compose form's
  `ShowBackup` uses the same rule (`isDumpImage`), which replaces its old `Contains "postgres"` test, so a service whose
  image only contains the word no longer gets a Backup section by itself.
- **MySQL or MariaDB by the image.** The mysql engine has two names on the page: `dumpName(engine, image)` says
  "MariaDB" for a `mariadb` image and "MySQL" otherwise (a custom image gets the engine's name).
- **The Compose form's refusal.** The Postgres entry keeps its label "PostgreSQL" (the console contract's words); the
  refusal now reads "Choose none, PostgreSQL, MySQL or MariaDB, MongoDB, Volume or SQLite file."
- **The sentinel test checks Configure for the secret only.** Configure shows plain variables by design (014), so a
  plain-env password is drawn there; the plain sentinel is searched for on every other page, the log and the warnings,
  and the secret sentinel on all of them, Configure included (`TestDumpBackupsShowNoPassword`).
- **Docs.** `docs/database-backups.md` (new), `changelog.d/046-database-dumps.md` (no Upgrade note: an existing Postgres
  job gets the upload script with `BACKUP_EXT` at its next deploy) and CLAUDE.md's Feature 046 paragraph.
