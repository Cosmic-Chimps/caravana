# Database backups: PostgreSQL, MySQL, MariaDB and MongoDB

A database service can be backed up **while it keeps running**. Three engines do it, one per kind of database:
`postgres`, `mysql` (for both MySQL and MariaDB) and `mongodb`. Each takes a dump of the live database, uploads it
to the backup destination and keeps the newest ones. A restore loads a dump back into the running service.

The other engines still exist: `volume` stops the service and archives its volumes, `sqlite` copies one SQLite file
online. Use a dump engine for a database that has a server, since a volume archive stops it for every backup.

```yaml
backup: {engine: mysql}      # or postgres, mongodb; schedule, keep and restore as for every engine
```

In the console, the Backups tab offers "Back up daily" on a service whose image is `postgres`, `mysql`, `mariadb`
or `mongo` (the last path segment of the image, any registry or tag) when it has a port. The Compose form of New app has the
same choice in its Backup section; for an app that exists, use the Backups tab ("Back up daily", "Change the policy…").

## What each engine runs

Each backup is a Nomad batch job beside the app, `<app>_<service>_backup`, that runs on the schedule (and on Back
up now). It has two tasks that share a folder: **dump** runs in the service's own image, **upload** runs in
Caravana's rclone image. A restore is a one-off job, `<app>_<service>_restore`, with a **download** task and a
**restore** task. Backup jobs are not part of an app's export.

| Engine | Dump | Object name | Restore |
| --- | --- | --- | --- |
| `postgres` | `pg_dump -Fc` | `<time>.dump` | `pg_restore --clean --if-exists --no-owner --single-transaction` |
| `mysql` | `mariadb-dump` or `mysqldump`, see below | `<time>.sql.gz` | the dump through the `mariadb` or `mysql` client |
| `mongodb` | `mongodump --archive --gzip` | `<time>.archive.gz` | `mongorestore --archive --gzip --drop`, without `admin.*` and `config.*` |

Objects are at `<destination>/<app>/<service>/` in the backup destination. The time is the backup's Unix time, as
Caravana's backup IDs have it.

### What is dumped

- **PostgreSQL**: the one database the login names (`POSTGRES_DB`, which is the user's name when unset).
- **MySQL and MariaDB**: every database the login can see except `information_schema`, `performance_schema`, `mysql`
  and `sys`, with `--databases` (so the dump creates and selects each one and a restore needs no name),
  `--single-transaction --routines --events --triggers --no-tablespaces`. It doesn't lock tables, so writes go on,
  and it needs no `PROCESS` privilege. The script uses the MariaDB tools when the image has them (`mariadb-dump` and
  `mariadb`) and MySQL's (`mysqldump` and `mysql`) otherwise. A server with no database but the built-in ones
  gives a dump that is one SQL comment; restoring it changes nothing.
- **MongoDB**: everything `mongodump` takes, logging in against the `admin` database.

The mysql engine compresses outside the database image, so the image needs only its client: the dump task writes
plain SQL and the upload task gzips it. MongoDB compresses itself.

## Which variables it logs in with

The login is the one the database's own image reads to start, found by name in the service's environment or
secrets. One set of values starts the database and backs it up. A backup the service doesn't have the variables
for is refused at deploy, on `services[i].backup`, naming them.

| Engine | Looks for (first match) |
| --- | --- |
| `postgres` | `POSTGRES_PASSWORD` with `POSTGRES_USER` (default `postgres`) and `POSTGRES_DB` |
| `mysql` | `MARIADB_ROOT_PASSWORD`, then `MYSQL_ROOT_PASSWORD` (user `root`); else `MARIADB_USER` with `MARIADB_PASSWORD`, then `MYSQL_USER` with `MYSQL_PASSWORD` |
| `mongodb` | `MONGO_INITDB_ROOT_USERNAME` with `MONGO_INITDB_ROOT_PASSWORD` |

`*_FILE` variables (a password read from a file) are not read. The host and port are the service's own: Caravana
renders them from its registration in Nomad (the first port), so the service must have a port.

## The password stays out of the job

Caravana writes the login only to the backup job's own Nomad Variable. The job holds names, never values, and
no password is in a process's arguments:

- MySQL and MariaDB read a `[client]` option file (`--defaults-extra-file`) that the script writes into the task's
  private secrets directory, readable by the task's user only.
- MongoDB reads a `--config` YAML file with the password, written the same way.
- PostgreSQL uses its `PGPASSWORD` environment variable, as before.

A value with a line break is refused, since it could add a line to those files.

> [!WARNING]
> A password given as **plain environment** (not a secret) is part of the app's definition, so it shows in the API,
> the Nomad job and an export. The backup works, but the deploy warns with the variable's name. Make it a secret.
> A template that does this is refused.

## Restore semantics

Restore is on the Backups tab (type the service's name to confirm), through the API, and as the target of a
scheduled restore from a pinned backup (036). It loads the dump into the **running** service and doesn't stop it.

- **PostgreSQL**: objects in the dump are dropped and made again, in **one transaction**. A failed restore leaves the
  database as it was.
- **MySQL and MariaDB**: each table in the dump is dropped and made again; tables made since the backup **stay**.
  MySQL commits on every schema change, so a restore that fails partway is **not** undone: the tables loaded so
  far stay loaded. Run it again.
- **MongoDB**: `--drop` replaces each collection in the dump; collections made since stay. `admin` and `config`
  are not restored, so users, roles and the server's own state are left as they are, and a restore can't lock out the
  service's own login.

Records, pinning and retention, restore schedules, `schedule: never`, the needs-you items and notifications work
the same for the three engines. Retention keeps the newest `keep` backups that aren't pinned; it only looks at
objects with the engine's extension.

## Images it works with

| Database | Images | Notes |
| --- | --- | --- |
| PostgreSQL | the official `postgres` | as before |
| MySQL | the official `mysql`, 8 or later | |
| MariaDB | the official `mariadb`, 10.6 or later, including 11 | |
| MongoDB | the official `mongo`, 4.4 or later | needs the database tools 100.3 or later, which these have |

An image built on one of these works if it keeps the tools. Caravana doesn't look inside an image before it runs
the job, so a custom image without the tools can't be refused at deploy: the **run fails** with a sentence that
names the tool ("the image has neither mariadb-dump nor mysqldump", "the image has no mongodump"), which is the
backup's message, the needs-you item `backup_failed` and the notification.

## What it doesn't do

Point-in-time recovery (binary logs, the oplog), replica sets and sharded clusters beyond the one node the service
reaches, Percona XtraBackup and other servers' own tools, and Redis.
