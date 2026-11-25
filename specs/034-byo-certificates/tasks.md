# Tasks: Bring your own certificates

**Input**: [plan.md](./plan.md), [spec.md](./spec.md), [research.md](./research.md), [data-model.md](./data-model.md), [contracts/](./contracts/)

Tests are required (constitution VIII): each layer's tests are in the task that changes it.

## Phase 1: Foundation (blocks everything)

- [x] **T001** Migration `0046_byo_certificates.sql` (data-model.md) · internal/store/migrations
- [x] **T002** [P] `internal/certs`: `Parse` (chain + key → names, subject, issuer, dates, fingerprint, key type, trusted, normalized PEMs; typed field errors), `Covers`/`CoveredBy`, `CheckDirectory`; table tests with generated chains · internal/certs
- [x] **T003** [P] Store: issuer columns (`ACMEIssuer`, `SetACMEIssuer`, `ClearACMEIssuer`), `cert_fingerprint` on domain checks, `uploaded_certificates` CRUD; tests · internal/store
- [x] **T004** [P] Edge: `Certificate.Fingerprint` set whenever a leaf was presented; tests · internal/edge
- [x] **T005** Deploy: `SaveACMEIssuer`, `ACMEIssuerSecret`, `DeleteACMEIssuer`, `ACMEStorage`, `SaveCertificate`, `DeleteCertificate`, `IngressFeatures`; `deploytest` fakes; tests · internal/deploy

## Phase 2: User Story 1 — another ACME issuer (P1)

- [x] **T006** [US1] Service: `CertificateSettings` (issuer `custom`, `issuerName`, `acme`, the two `…Unavailable` reasons), `SetCertificateIssuer`, `ResetCertificateIssuer` (directory check, keep-the-key rule, variable then SQLite), the issuer's CA in the domain checks' roots; tests · internal/service
- [x] **T007** [US1] API `putCertificateIssuer`, `deleteCertificateIssuer`, the settings' new properties, error codes; openapi; tests · internal/api, api/openapi.yaml

## Phase 3: User Story 2 — upload a certificate (P1)

- [x] **T008** [US2] Service: `UploadCertificate` (replace on equal names, limit 30), list, get, delete, `domains`/`keptEarlier`, an uploaded leaf judged by its dates in `probe`, a re-check of covered hosts after a change; tests · internal/service
- [x] **T009** [US2] Needs-you kind `certificate_expiring` (attention, `ProblemParts`, notify event `certificate`/`names`), no `domain_cert_expiring` for a domain presenting an uploaded certificate; tests · internal/service, internal/notify
- [x] **T010** [US2] API: the four upload operations; openapi 0.63.0; `admin()` rules in `access.go` and the console's `consoleAccess`, scope and role walks; tests · internal/api, api/openapi.yaml

## Phase 4: User Story 3 — who issued it (P2)

- [x] **T011** [US3] `DomainView.Uploaded`, `CertificateView.Uploaded`, `fingerprint`/`uploaded` in the API's certificate JSON; openapi; tests · internal/service, internal/api

## Phase 5: Console

- [x] **T012** Settings › Certificates: uploaded certificates section and form, Change issuer form with presets, notices and refusals (contracts/page-certificates.md); `tools/ui-css.sh build`; tests · internal/ui
- [x] **T013** Domains and needs-you words name the issuer and uploaded certificates; tests · internal/ui

## Phase 6: Traefik and the installer

- [x] **T014** Both Traefik jobs and policies per contracts/traefik.md; proven by hand in the lab (issuer variable with and without EAB items, an upload, Team only still working) · deploy/install, deploy/dev
- [x] **T015** `install.sh`: the restart-once note, the store clean-up; README · deploy/install
- [x] **T016** `install-test.sh`: policy lines, an older job replaced, upload served on 443, the clean-up on a rerun · deploy/dev (`check_certificates_host` and `check_old_traefik_host`, `ONLY=certificates`; on Ubuntu 24.04 on 2026-11-25 `ONLY=certificates` passed in 3½ min and the full run in 24 min. The full run found that every rerun said Traefik restarts: `grep -q` under `pipefail` let SIGPIPE fail the pipeline, so `run_traefik` reads the job before grepping it)

## Phase 7: Lab and polish

- [x] **T017** Gate step `certificates` (lab only): Pebble with EAB, issuer set through the API, a domain's Pebble certificate and its renewal, wildcard upload, expiring item, removal falls back, sentinels · cmd/caravana
- [x] **T018** Console step `console-certificates`: upload, list, Domains row, remove through the forms · cmd/caravana
- [x] **T019** Sentinel tests in `service`, `api`, `ui`: the key and the HMAC key reach no answer, page, log or SQLite file
- [x] **T020** `docs/certificates.md`, `changelog.d/byo-certificates.md`, CLAUDE.md paragraph
- [x] **T021** (all but `install-test.sh`, 2026-11-24: the CI checks, the lab steps `certificates` 76 s and `console-certificates` 28 s) Validate: build, vet, staticcheck, gofmt, `go test ./...`, `-race`, linux/arm64 build, `tools/ui-css.sh check`, `go mod tidy -diff`, shellcheck, `tools/release.sh check`, the lab steps, `install-test.sh`

## Dependencies

T001 → T002–T004 (parallel) → T005 → T006 → T007 → T008 → T009 → T010 → T011 → T012 → T013 → T019.
T014–T016 touch only `deploy/` and run beside the Go work. T017 and T018 need T014 and T013.
