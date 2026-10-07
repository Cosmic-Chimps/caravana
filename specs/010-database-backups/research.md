# Research: Database backups

All shapes below were run in the lab (Nomad 2.0.7, postgres:17, rclone 1.71).

## R1. Job IDs

**Decision**: `<app>_<service>_backup` and `<app>_<service>_restore`. App IDs and service names
never contain `_`, so these can't collide with each other or with an app's own job, and
`nomad/jobs/<job>` is a valid variable path (`[a-zA-Z0-9-_~/]`). Every job of an app other than
its own starts with `<app>_`, so app delete finds them by prefix. A periodic run is
`<job>/periodic-<unix seconds>`.

**Alternatives considered**: `<app>-<service>-backup` (app `a`, service `b-c` and app `a-b`,
service `c` collide); dots (not valid in variable paths).

## R2. Backup job

**Decision**: `Type: batch`, `Periodic: {Enabled, SpecType: "cron", Specs: [schedule],
ProhibitOverlap: true, TimeZone: "UTC"}` (`Crons` fails with "Must specify a spec"). One group
`backup`, bridge network without ports, `RestartPolicy {Attempts 0, Mode fail}`,
`ReschedulePolicy {Attempts 0, Unlimited false}`. With the defaults, a wrong password made
about 8 attempts over 2 minutes and blocked the next scheduled run.

- `dump`: prestart, not a sidecar; the service's pinned image; `pg_dump -Fc -f
  /alloc/data/backup.dump`. Two env templates: the job's variable (as JSON-quoted values), and
  `PGHOST`/`PGPORT` from the service's main registration `<app>-<service>`, skipping canaries.
  The main registration is used, not the `-internal` one, because single-service apps have
  only the main one; it answers with the node's address and the host port.
- `upload`: rclone image, `entrypoint ["/bin/sh","-c"]`, a script that takes the time from
  `NOMAD_JOB_ID`, uploads to `dest:$BACKUP_PATH/<ts>.dump`, prints
  `caravana-backup key=<path> bytes=<n>`, then deletes the oldest dumps beyond `$BACKUP_KEEP`,
  printing `caravana-expired <file>` for each. Scripts use `$VAR`, never `${VAR}` (Nomad
  interpolates those).
- When `dump` fails, `upload` never starts ("Sibling Task Failed"). The reason is only in
  dump's stderr.

## R3. Credentials

**Decision**: Caravana writes `nomad/jobs/<job>` before registering the job. Items: `PGUSER`
(`POSTGRES_USER`, default `postgres`), `PGPASSWORD` (`POSTGRES_PASSWORD`), `PGDATABASE`
(`POSTGRES_DB`, default the user), each from the service's env or its secrets (008), and
`RCLONE_CONFIG_DEST_TYPE=s3`, `_PROVIDER=Other`, `_ENDPOINT`, `_REGION`, `_ACCESS_KEY_ID`,
`_SECRET_ACCESS_KEY`, `_FORCE_PATH_STYLE=true`, plus `BACKUP_PATH=<bucket>/<prefix>/<app>/<service>`.
The job reads its own path with no policy. A job reading another job's path doesn't fail: its
copy hangs `pending` with the template event "Missing: nomad.var.block(...)". So the restore
job gets its own copy of the variable, and the watcher fails a restore stuck pending for 2
minutes.

The destination's secret key is kept in `caravana/backup-destination`. Changing the
destination rewrites the `RCLONE_CONFIG_DEST_*` and `BACKUP_PATH` items of every backup job's
variable, so the next run uses it with no deploy (FR-012).

## R4. Runs and their outcome

**Decision**: a watcher in the service polls `GET /v1/jobs?prefix=<job>/periodic-` for every
backed-up service every 15 s. A child that is `dead` is final: its allocation is `complete`
(succeeded; size and key from upload's stdout) or `failed` (the message is the last line of
the failed task's stderr, else its last event). Each run is a row in `backups`, id
`<service>-<ts>`. The `caravana-expired` lines mark older rows expired. Nomad keeps dead
children for about 4 h (`job_gc_threshold`), and Caravana copies them into SQLite well before
that.

**Simplified**: runs that start and are garbage-collected while Caravana is down for more than
about 4 hours aren't listed; their objects are still in the bucket.

## R5. Back up now

**Decision**: `PUT /v1/job/<job>/periodic/force`. It answers an eval whose `JobID` is the child.
Two forces in the same second share one child, and the second is dropped silently, so a force
is refused while a run of that service is running, or one started in the same second.

## R6. Restore

**Decision**: purge an old `<app>_<service>_restore`, write its variable (a copy of the backup
job's), register a batch job with `Meta.backup_key` (the run's recorded object). `download` is
the rclone image's own entrypoint with args `copyto dest:${NOMAD_META_backup_key}
/alloc/data/backup.dump` (here Nomad's interpolation is wanted). `restore` runs the service's
current pinned image: `pg_restore --clean --if-exists --no-owner -d "$PGDATABASE"
/alloc/data/backup.dump`. One restore per service at a time. Its row in `restores` is final
when the job is dead.

## R7. App delete

**Decision**: purge every job with prefix `<app>_` (parents, periodic children, restore jobs):
purging a parent doesn't remove its children. Then delete the variables under
`nomad/jobs/<app>_`. The bucket is left alone.

## R8. Policy

**Decision**: the namespace already has `submit-job` (register, periodic force), `read-job`
and `read-logs`. Variables gain `path "caravana/*"` for the destination secret. The gate step
proves the rest on a real host.

## R9. Gate

**Decision**: the step runs an S3 test bucket with `rclone serve s3` as a plain Nomad job (the
gate's token) on static port 9000, sets the destination to `http://<node IP>:9000`, deploys a
Compose app with `postgres:17` on a volume and a backup policy, writes row A, backs up, changes
it to B, restores, reads A, then deletes the app and the bucket job.
