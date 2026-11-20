# Tasks 046: MySQL, MariaDB and MongoDB dumps

## Phase A: model, jobs, deploy, service, store, API

- [x] T001 `internal/app`: `BackupMySQL = "mysql"`, `BackupMongoDB = "mongodb"`, `DumpEngine`, `Backup.Dumps()`,
  `BackupPasswordNames`; `validateBackup` accepts them and checks port and credentials by name (D10); tests.
- [x] T002 `internal/nomadjob/backup.go`: per-engine recipe (D2–D7), the upload script with `BACKUP_EXT` and
  `BACKUP_GZIP`, the mysql and mongodb dump and restore scripts, the restore download that gunzips; goldens for the
  four new jobs, the Postgres goldens updated for the script only; script tests under real `sh` with fake tools
  (the password reaches the option/config file, never argv; a missing tool fails with its sentence; an empty
  database list; retention with the new extensions and pins).
- [x] T003 `internal/deploy`: `SyncBackups` for dump engines with `DB_USER`/`DB_PASSWORD`; `BackupJobIDs`;
  `BackupRuns` expired names; `Restore` unchanged in shape; tests.
- [x] T004 `internal/service`: `postgresBackup` → dump engines; credentials per engine (D10) in `syncBackups`;
  `runRecord` and `restoreBackup` record the engine; `recordApp` maps expired names (D3); `recordRestores`;
  `PlainBackupPasswords` for every dump engine; tests.
- [x] T005 `internal/store`: migration 0036 (D11); store comments; a migration test that a mysql and a mongodb
  backup and restore save and read back.
- [x] T006 `internal/api` + `api/openapi.yaml` 0.51.0 (D12); `internal/templates/check.go` (D14); tests.

## Phase B: console and docs

- [ ] T007 `internal/ui`: engine select, `backupEngineFor`, labels, `builtinCommand`, `kindOf`, the Compose form's
  refusal text, `composepage` ShowBackup for mysql/mariadb/mongo images (D13); a sentinel test that no page shows a
  password; `tools/ui-css.sh build` if classes changed.
- [ ] T008 Docs: `docs/database-backups.md` (what each engine runs, what it reads, restore semantics, the images it
  works with), `changelog.d/046-database-dumps.md`, CLAUDE.md's feature entry.

## Phase C: lab

- [ ] T009 Gate steps `mysql-backup-restore` and `mongodb-backup-restore` (plan "Lab") in
  `cmd/caravana/gate_dumps_test.go`, listed in `runGate` after `sqlite-backup-restore`; both pass in the lab run
  alone.
- [ ] T010 Full checks: build, vet, staticcheck, `go test -race ./...`, gofmt, cross-compile, `tools/ui-css.sh
  check`, `tools/release.sh check`.
