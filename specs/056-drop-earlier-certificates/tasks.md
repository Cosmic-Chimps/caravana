# Tasks 056: Drop the earlier certificates an upload covers

Plan: `plan.md` (D1–D12). Each phase ends with `go build ./...`, `go vet ./...`, `gofmt -l .` (empty),
`go tool staticcheck ./...` and the tests of the packages it touched.

## Phase A: the agent, its command and the installer

- [ ] T001 `internal/nodeagent/certdrop.go`: the request (`CertificateDropRequest{ID, Names}`), its validation
  (D2), the pure rule `DropCovered(store []byte, names []string) (out []byte, dropped []string, err error)` (D3) and
  the folder walk over `/var/lib/caravana-host/traefik` (`acme.json`, `acme-<12 hex>.json`, regular files, 32 MiB).
- [ ] T002 The outcome (D6) in `State` and `Report` (`certificateDrop`, `traefikStore`), `applyCertificateDrop` as a
  step of `applySync` after the console address and before the clean-up, and the immediate second report (D5).
- [ ] T003 `caravana node drop-certificates --names … [--dry-run]` (`cmd/caravana/node.go`, usage text), printing
  the dropped names comma-separated on one line (empty when none).
- [ ] T004 `deploy/install/install.sh`: `drop_covered_acme_certificates` calls `"$AGENT" node drop-certificates`
  (dry run first, then stop Traefik and drop); `acme_covered` and its Python go.
- [ ] T005 Tests: `certdrop_test.go` (rule table, walk, validation, outcomes, `running` again, second post) and the
  command's test.

## Phase B: the console's record, the hand-off, the restart and the API

- [ ] T006 Migration `0045_certificate_drops.sql` and `internal/store/certificatedrops.go` (create with the open
  index, get, list newest first, mark running, settle, set restarting).
- [ ] T007 `internal/service/certificatedrops.go`: `RequestCertificateDrop`, `CertificateDrops`, `CertificateDrop`
  (D7, D8), `certificateDropHandoff` in `NodeSync` (D8b), the report's block in `NodeReport` and its validation,
  `NodeAnswer.CertificateDrop`, the restart and the pass (D9), `SetCertificateDropPassGap`, started in `Start`.
- [ ] T008 `internal/api`: the three routes, the sync answer's and report's blocks, errors, `accessRules`, the
  walks, `api/openapi.yaml` 0.62.0.
- [ ] T009 Tests: store, service (refusals, hand-off, every outcome, restart via `deploytest`, the pass, no PEM in
  rows or logs), api.

## Phase C: the console, docs and the VM check

- [ ] T010 `internal/ui/certificates.go` and `templates/settings.html`: the form, the panel and its part (D11),
  `consoleAccess`, `tools/ui-css.sh build` if styles change.
- [ ] T011 Docs: `docs/certificates.md`, research R5's pointer, `changelog.d/056-drop-earlier-certificates.md`,
  CLAUDE.md's paragraph.
- [ ] T012 `deploy/dev/install-test.sh`: the console drop in `check_certificates_host` (D12); shellcheck.
- [ ] T013 Run `ONLY=certificates deploy/dev/install-test.sh` on a VM.
