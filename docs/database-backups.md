# Database backups: PostgreSQL, MySQL, MariaDB and MongoDB

A database service can be backed up **while it keeps running**. Three engines do it, one per kind of database:
`postgres`, `mysql` (for both MySQL and MariaDB) and `mongodb`. Each takes a dump of the live database, uploads it
to the backup destination and keeps the newest ones. A restore loads a dump back into the running service.

The other engines still exist: `volume` stops the service and archives its volumes, `sqlite` copies one SQLite file
online, and `opensearch` has an OpenSearch or Elasticsearch cluster take a snapshot of itself (see
[OpenSearch and Elasticsearch snapshots](#opensearch-and-elasticsearch-snapshots)). Use a dump engine for a database
that has a server, since a volume archive stops it for every backup.

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

## OpenSearch and Elasticsearch snapshots

`backup: {engine: opensearch, repository: <folder>}` backs up an OpenSearch or Elasticsearch cluster **while it
keeps running**. It is meant for OpenSearch, its forks and Elasticsearch, which share the snapshot API. OpenSearch
2.19.1 is checked end to end: the lab's gate step `opensearch-snapshot` runs a backup and a restore through
Caravana's own scripts. Elasticsearch 8.17 was only checked by hand, with the same API calls and not Caravana's
scripts. *Elasticsearch 7 and the Wazuh indexer (whose images' facts were observed, with no backup or restore run
against them) are as of writing, not checked live.* Unlike the dump engines it is not a
Nomad job beside the app: the cluster takes the snapshot through its own API, and Caravana archives it, with the same
destination, retention, pins, copies, restore schedules and records as every other engine.

What the service must have:

- **A port.** The backup reaches the cluster over its port, so a service with none is refused at deploy on
  `services[i].backup` ("the backup reaches the cluster over its port, and the service has none"). The calls go to the
  service's **first** port, which must be the cluster's HTTP API (9200 in the usual images).
- **Tools in its image.** The calls run in the service's own copy, so its image needs `sh`, `curl`, `grep`, `sed` and
  `mktemp`. The official OpenSearch and Elasticsearch images have them.
- **A writable volume** holding the snapshot folder (below).

```json
{"backup": {"engine": "opensearch", "repository": "/usr/share/opensearch/snapshots", "schedule": "0 3 * * *", "keep": 7}}
```

The same policy goes in a Compose request (`services.<name>.backup`) and in the console's Compose form on New app,
where the engine is **OpenSearch or Elasticsearch snapshot (the service keeps running)** and the folder is **Snapshot
folder**. On the Backups tab, a service that already has a backup policy gets **Change the policy…**, which offers the
engine when the service has a port and a writable volume. A service with no policy is offered only **Back up daily**,
and only with the volume engine, so set the `opensearch` policy through the API or a Compose request (or take
**Back up daily**, then **Change the policy…**).

### The snapshot folder and `path.repo`

`repository` is an absolute folder **as the service sees it**, equal to or inside one of the service's writable
volumes (a folder elsewhere is refused at deploy, naming `services[i].backup.repository`). The cluster only writes
snapshots under a folder listed in its **static** `path.repo` setting, so you set both to the same place:

- **OpenSearch and Elasticsearch images**: an environment variable named `path.repo`, for example
  `path.repo=/usr/share/opensearch/snapshots` (the images' entrypoints turn dotted variables into settings).
- **The Wazuh indexer** (its entrypoint ignores dotted variables) and any image that doesn't: a line in
  `opensearch.yml`, given to the service as a [file](opensearch-apps.md): `path.repo: ["/var/lib/wazuh-indexer-snapshots"]`.

A deploy **warns** on `services[i].backup.repository` when the service has neither an environment variable named
`path.repo` nor a file whose content contains `path.repo`. The warning also appears when `path.repo` is set only in a
file whose content is a secret, since Caravana doesn't read a secret's content: the setting works, and you can ignore
the warning. Without the setting the first run fails with the cluster's own sentence: "snapshot: location [...]
doesn't match any of the locations specified by path.repo".

The folder is Caravana's: the console says "Caravana keeps its snapshots in caravana-* folders there and removes them
once each backup or restore is done, so keep nothing of your own under those names." Each run works in a fresh
`caravana-<id>` folder inside it and removes the `caravana-*` folders earlier runs left, and nothing else. A snapshot is
made on the volume first, then archived, so the volume needs **free disk for one snapshot** of the cluster while it
runs.

### What a backup holds

Every index whose name doesn't start with a dot, without the cluster's global state. The object is
`<time>.opensearch.tar.gz`, a full, self-contained snapshot (nothing is incremental), so any backup restores on its
own, and retention, pins and copies work as for the other engines. It does **not** hold:

- the cluster's global state: index templates, persistent cluster settings and ingest pipelines;
- indices whose name starts with a dot, which include the security plugin's configuration (`.opendistro_security`),
  Dashboards' saved objects and the plugins' own indices;
- data streams: *as of writing, not checked live.* On Elasticsearch 7.9 and later and 8 the pattern a backup uses can match a data stream's
  name, so one may be included, and restoring one was never tried.

Keep the security configuration and the templates in your own definition (for example the Wazuh template makes them at
install), not in the backup.

### The login

When the cluster has the security plugin on, the backup logs in as the admin. The values are found by name in the
service's environment or secrets, first match:

| Looks for | User |
| --- | --- |
| `OPENSEARCH_INITIAL_ADMIN_PASSWORD` | `admin` |
| `INDEXER_PASSWORD` with `INDEXER_USERNAME` | `INDEXER_USERNAME`, default `admin` |
| `ELASTIC_PASSWORD` | `elastic` |

None found means no login: a cluster with the security plugin off is backed up without one, so nothing is required at
deploy. A cluster that answers 401 fails the run with a message that ends with "OpenSearch refused the login: set
OPENSEARCH_INITIAL_ADMIN_PASSWORD, INDEXER_PASSWORD or ELASTIC_PASSWORD" (after the step, `snapshot:` or `restore:`). Caravana connects to `127.0.0.1` inside the
service's own copy, https first and then http, without checking the certificate (the security plugin's are
self-signed, and the connection never leaves the copy).

The login travels on the standard input of Nomad's exec only: it is in no job, file or argument and in no variable
other than the service's own secret it is read from. A password given as **plain environment** warns at deploy as it
does for the dump engines. Make it a secret.

### Restore

Restore is on the Backups tab (type the service's name to confirm), through the API and as the target of a scheduled
restore from a pinned backup. The backup is downloaded and unpacked while the cluster runs. Then each index in the
snapshot that exists now is **closed**, the snapshot is restored over it, and it is opened again. Every index of the
backup is restored, including one deleted since: it is made again. Indices made since the backup **stay**, and the
cluster's settings, templates and dot indices are not touched. While it runs, the snapshot's indices are closed, so
queries and the dashboard on them **fail for that long**, though the service itself never stops. If the restore fails
after the close, the script tries to open them again, and says in the record's message whether it could.

Caravana doesn't stop the service for a backup or a restore, and a run restarted by Caravana's own restart is failed
with "Caravana restarted during the run" (a restore adds that the indices it closed may still be closed).

## What it doesn't do

Point-in-time recovery (binary logs, the oplog), replica sets and sharded clusters beyond the one node the service
reaches, Percona XtraBackup and other servers' own tools, and Redis. The `opensearch` engine doesn't back up the
cluster's global state or its dot indices, or make an S3 repository (the repository plugin isn't in the official OpenSearch image; Elasticsearch 8 bundles `repository-s3`).
