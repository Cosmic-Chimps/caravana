# 063 Tasks

Decisions are in `plan.md` (D1–D16). Commit at the end of each phase, on the branch `alta/scenic-loom-xv2q`.

## Phase A: making a backup

- [x] A1 `internal/s3`: path-style Signature V4 client (`Put`, `Get`, `GetRange`, `List`, `Delete`), errors without URL/keys/bodies;
  tests: AWS's published SigV4 example as a known answer, an `httptest` fake S3 (`s3test`, in-memory, checks the signature) used by
  every package's tests.
- [x] A2 `internal/selfbackup` (pure; a test forbids `net/http`, `service`, `store`, Nomad): `Keep(path)` table, recovery key
  (`NewKey(rand)`, `ParseKey` with checksum, `Fingerprint`), `Write(w, pub, version, schema, files…)` / `ReadHeader` / `Read(r, key)`,
  the STREAM cipher (truncation, reorder, changed header and wrong key all fail; a 0-byte and a multi-chunk body round-trip).
- [x] A3 Migration `0049_self_backup.sql` (D15), store methods for settings, runs, restores and items; `Store.CopyTo(ctx, path)`
  (`VACUUM INTO`, `quick_check`), `Store.SchemaVersion()`.
- [x] A4 `deploy.ExportVariables(ctx, keep func(string) bool)` and `deploy.ImportVariables(ctx, vars)`; `deploytest` lists by prefix.
- [x] A5 `service/selfbackup.go`: run (D5), daily pass (D6), retention, settings, `CreateRecoveryKey` (owner, D3), start-up settling,
  audit notes; `service/attention_selfbackup.go` (D7) with `ProblemParts` words; `app.TriggerRestore`.
- [x] A6 API 0.70.0 (D13): routes, access rules (owner/admin), openapi, parity and roles walks, sentinel test.
- [x] A7 `go build/vet/test -race`, staticcheck, gofmt, cross-compile.

## Phase B: console

- [x] B1 Operator › Caravana backup page, nav entry, parts route, Back up now, settings form, key section and the key page (D14).
- [x] B2 Overview's first-server line; access table entries; `tools/ui-css.sh check`; ui tests and the sentinel test.

## Phase C: restore

- [ ] C1 `caravana admin restore` (D8) with `--list`, `--latest`, `--object`, `--replace`; tests with `s3test` and `deploytest`.
- [ ] C2 `service/selfrestore.go` (D10), `StartApp` fallback (D11), data step (D12), `getSelfRestore` / `restoreSelfRestoreData`, the
  restore panel on the page.
- [ ] C3 `install.sh --restore` (D9), shellcheck; `install-test.sh` `check_restore_host` (written, run if time allows).
- [ ] C4 Docs: `docs/self-backup.md` (what is backed up, the key, the restore step by step, what the owner keeps), the guide's
  page and `site/pages.mjs`/`sidebar.mjs`; `changelog.d/self-backup.md`; CLAUDE.md's feature paragraph.
- [ ] C5 Gate step `self-backup` (lab): an app with a secret, a key, Back up now, then stop Caravana, purge the app's job and secret
  variable, move the data dir aside, `admin restore --latest`, start Caravana, the restore ends, the app healthy with its secret
  rendered (Run a command `printenv`), no sentinel or key in any answer, log or data file.
