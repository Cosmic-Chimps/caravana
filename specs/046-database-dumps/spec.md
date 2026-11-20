# Feature 046: MySQL, MariaDB and MongoDB backups without stopping the database

CodeAlta task `qgmm` (raised by the maintainer on 2026-11-12 while comparing Caravana with other PaaS consoles).

## Why

Caravana backs up Postgres with a dump that never stops the service (010), any volume by stopping the service
(016) and a SQLite file online (029). MySQL, MariaDB and MongoDB, the other databases people run most, only have
the volume engine, which stops the database for every backup.

## User stories

### US1 (P1): back up a MySQL or MariaDB service while it serves writes

The owner sets `backup: {engine: mysql}` on a service running the official `mysql` or `mariadb` image (or one
built on it). Each scheduled run and each Back up now dumps every database the login sees with the image's own
`mariadb-dump` or `mysqldump` (`--single-transaction --routines --events --triggers`), never stopping the service,
and uploads `<ts>.sql.gz` to the backup destination; retention keeps `keep` unpinned dumps. A restore loads a dump
into the running server.

Acceptance: a MariaDB app writes A, is backed up (on its schedule and by Back up now while a writer inserts rows),
changes to B, is restored and reads A.

### US2 (P1): back up a MongoDB service while it serves writes

`backup: {engine: mongodb}` on a service running the official `mongo` image dumps every database with
`mongodump --archive --gzip` (`<ts>.archive.gz`); a restore is `mongorestore --archive --gzip --drop`.

Acceptance: as US1, with a MongoDB app.

### US3 (P1): no password escapes

The login comes from the service's env or secrets: `MARIADB_ROOT_PASSWORD` / `MYSQL_ROOT_PASSWORD` (user `root`),
else a `MARIADB_USER`/`MARIADB_PASSWORD` or `MYSQL_USER`/`MYSQL_PASSWORD` pair; `MONGO_INITDB_ROOT_USERNAME` and
`MONGO_INITDB_ROOT_PASSWORD`. It is written to the backup job's own Nomad Variable only. A password given as plain
env (not a secret) is a deploy warning, as for Postgres (QA #36).

Acceptance: a sentinel password is in no job, export, page, API answer or log line.

### US4 (P2): the same everywhere

Records, pins and retention, restore schedules (036), "never" (039), the needs-you items, notifications, the
built-in schedules list, the Compose overlay and form, the Backups tab's "no backup" forms and the API work for the
two engines exactly as for Postgres.

## Refusals (field errors)

- No backup destination: `services[i].backup` (as today, every engine).
- No port: `services[i].backup` ("reaches the database over its port").
- No credentials: `services[i].backup`, naming the variables the engine reads.
- An image without the dump tool: see plan D9 (a run that fails naming the missing tool).

## Out of scope

Point-in-time recovery (binlogs, oplog), replica sets and sharded clusters beyond dumping the node the service
reaches, `*_FILE` credential variables, other MySQL-protocol servers' own tools (Percona XtraBackup), Redis.
