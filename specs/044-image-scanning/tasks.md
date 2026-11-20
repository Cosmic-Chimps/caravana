# Tasks: Image scanning

Decisions are in [plan.md](./plan.md) (D1–D18). Each task names the files it touches; tests come with their task.
Phases 1 to 4 are each one piece of work for a sub-agent, reviewed before the next starts.

## Phase 1: Pure pieces and the Nomad side (US1, US4)

- [x] T001 `internal/vulns`: `Severity`, `Finding`, `Result`, `Counts`, `Template`, `Parse` (D3, D18: stable IDs, one severity scale, CVE, EPSS, KEV, remediation, the scanner's version) with table tests
  (the research R2 lines, a missing trailer, tabs, a non-https link, duplicates, the cut at `MaxFindings`, sorting).
- [x] T002 `internal/nomadjob/scan.go`: `ScanJobID`, `ScanVolume`, `ScanResultFile`, `ScanTask`, `ScanMemoryMB`,
  `ScanSource`, `ScanOptions`, `SBOMPath`, `ScanJob` (D2, D16, D17), `Settings.ScannerImage`, `DefaultScannerImage`,
  `DefaultScannerVersion` (D1); goldens `scan`, `scan-private`, `scan-sbom`, `scan-update`.
- [x] T003 `internal/config` + `cmd/caravana/main.go`: `CARAVANA_SCANNER_IMAGE` / `-scanner-image` (D1), checked
  pinned; test.
- [x] T004 `internal/deploy/scan.go` (+ `deploytest`): `EnsureScanVolume`, `DeleteScanVolume`, `StartScan`,
  `ScanState`, `ScanResult`, `ScanFailure`, `PurgeScan`, `StopScan` (D4); tests with the fake.

## Phase 2: Store and service (US1, US2, US3, US5, US6)

- [x] T005 `internal/store`: migration `0034_image_scans.sql` and the functions (D6: add-ons, scans with first and
  last seen, runs, settings, `ForgetScans`); tests.
- [x] T005b `internal/service/addons.go`: the add-on, install and remove (D15); tests.
- [x] T006 `internal/service/scanner.go`: the queue and runner (D5), room (D7), triggers (D8), prune (D10), one node and the database URL (D16), SBOM rescans (D17), settings,
  `RequestScan`, the read models for D12, the lab hooks; tests with the fakes (a fake result per image, a second
  scan finding a new advisory, failures, skipped for room, restart recovery).
- [x] T007 `internal/service`: the gate in `deployWarned` (D9) with tests (refused naming the field, warning when
  the scan fails, cached result used).
- [x] T008 `internal/service` + `internal/notify`: `image_vulnerable` (D11) in attention, `ProblemParts`,
  `AttentionKinds`, the activity feed, the notifier's event fields; tests (`TestEveryKindSaysTheSameWordsEverywhere`
  passes).

## Phase 3: API and MCP (US1–US5)

- [x] T009 `internal/api/scans.go`, `addons.go` + `api/openapi.yaml` 0.49.0 (D12): routes, schemas, `image_vulnerable` mapping,
  the attention kind and webhook properties; parity and scope tests.
- [x] T010 `internal/mcpserver`: `get_app_scan` (D12) with the contract test.

## Phase 4: Console (US1, US5, US6)

- [x] T011 `internal/ui`: Operator › Security with install and remove (D15), the Security panel and part, the history counts, Scan now, Operator › Security with the
  settings and the lists, the nav entry, the live notice (D13); tests (pages render, no secret, forms without
  JavaScript); `tools/ui-css.sh build`.

## Phase 5: Lab, docs (all)

- [x] T012 `cmd/caravana`: the gate step `image-scan` (US0–US5: install, alpine:3.14.0 findings, the item and notifications,
  the fixed image resolves it, the daily rescan finds a dropped advisory again, a private image with a sentinel
  password, the gate's 422, remove) and the console step `console-security`.
- [x] T013 Lab run: check Grype's stderr phrases for D4's classes, the multi-output run and an SBOM rescan against the image scan (D17), EPSS/KEV fields, the volume's node, timings; record them in
  `research.md` (R4).
- [x] T014 Docs (D14): `docs/image-scanning.md`, `changelog.d/image-scanning.md`, CLAUDE.md, constitution 1.2.1.
