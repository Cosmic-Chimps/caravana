# Quickstart: SQLite backups

## 1. Unit checks

```bash
go test ./internal/app ./internal/nomadjob ./internal/deploy ./internal/service ./internal/store ./internal/api ./internal/ui ./internal/config
```

## 2. By hand, against the lab

```bash
deploy/dev/lab.sh up && eval "$(deploy/dev/lab.sh env)"
go run ./cmd/caravana   # with a backup destination set in Settings
```

Deploy an app whose service writes to a WAL database:

```json
{"id": "notes", "services": [{
  "name": "db", "image": "alpine/sqlite:3.53.4",
  "entrypoint": ["/bin/sh", "-c"],
  "args": ["sqlite3 /data/app.db 'PRAGMA journal_mode=WAL' 'CREATE TABLE IF NOT EXISTS t(v INTEGER, at TEXT)'; i=0; while :; do i=$((i+1)); sqlite3 -cmd '.timeout 5000' /data/app.db \"INSERT INTO t VALUES ($i, datetime())\"; sleep 0.2; done"],
  "volumes": [{"name": "data", "path": "/data"}],
  "backup": {"engine": "sqlite", "file": "/data/app.db", "keep": 2}
}]}
```

Back up now, then Run a command `sqlite3 /data/app.db "select count(*) from t"` twice: the count
keeps growing, and the copy's task has no restart. Restore: the count drops back to about the
backup's, and the service was stopped a few seconds.

## 3. Gate step `sqlite-backup-restore` (`cmd/caravana/gate_sqlite_test.go`)

In `runGate`, after `volume-backup-restore`, against the same `rclone serve s3` bucket:

1. Refusals: `file` outside every volume, on a read-only volume, ending in `-wal`, and `file` with
   the `volume` engine: each 400 `invalid_field` naming `services[0].backup.file`.
2. Deploy the app of §2 as `lab029`; wait until it's healthy and `count(*) > 20`.
3. Note the copy's ID and its service task's restart count; Back up now; wait until it succeeds.
   The copy is the same, its restart count unchanged, `count(*)` grew during and after the run, and
   the record has `engine: sqlite`, an object ending `.sqlite.gz` and bytes > 0.
4. `INSERT` a marker row (`CREATE TABLE marks(v)`); restore the backup; wait until it succeeds.
   `marks` doesn't exist, `stat -c %u /data/app.db` is unchanged, `stoppedSeconds` is set.
5. Two more backups with `keep: 2`: the first backup is expired.
6. No line of the run's records, Caravana's log or the job holds the bucket's secret key.
7. Delete the app.

```bash
CARAVANA_LAB=1 go test ./cmd/caravana -run 'TestLab/gate/sqlite-backup-restore' -v -timeout 20m
```
