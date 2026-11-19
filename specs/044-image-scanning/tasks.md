# Tasks: Image scanning

Decisions are in [plan.md](./plan.md) (D1–D14). Each task names the files it touches; tests come with their task.
Phases 1 to 4 are each one piece of work for a sub-agent, reviewed before the next starts.

## Phase 1: Pure pieces and the Nomad side (US1, US4)

- [ ] T001 `internal/vulns`: `Severity`, `Finding`, `Result`, `Counts`, `Template`, `Parse` (D3) with table tests
  (the research R2 lines, a missing trailer, tabs, a non-https link, duplicates, the cut at `MaxFindings`, sorting).
- [ ] T002 `internal/nomadjob/scan.go`: `ScanJobID`, `ScanVolume`, `ScanResultFile`, `ScanTask`, `ScanMemoryMB`,
  `ScanJob` (D2), `Settings.ScannerImage` and `DefaultScannerImage` (D1); goldens `scan` and `scan-private`.
- [ ] T003 `internal/config` + `cmd/caravana/main.go`: `CARAVANA_SCANNER_IMAGE` / `-scanner-image` (D1), checked
  pinned; test.
- [ ] T004 `internal/deploy/scan.go` (+ `deploytest`): `EnsureScanVolume`, `DeleteScanVolume`, `StartScan`,
  `ScanState`, `ScanResult`, `ScanFailure`, `PurgeScan` (D4); tests with the fake.

## Phase 2: Store and service (US1, US2, US3, US5, US6)

- [ ] T005 `internal/store`: migration `0034_image_scans.sql` and the functions (D6); tests.
- [ ] T006 `internal/service/scanner.go`: the queue and runner (D5), room (D7), triggers (D8), prune (D10), settings,
  `RequestScan`, the read models for D12, the lab hooks; tests with the fakes (a fake result per image, a second
  scan finding a new advisory, failures, skipped for room, restart recovery).
- [ ] T007 `internal/service`: the gate in `deployWarned` (D9) with tests (refused naming the field, warning when
  the scan fails, cached result used).
- [ ] T008 `internal/service` + `internal/notify`: `image_vulnerable` (D11) in attention, `ProblemParts`,
  `AttentionKinds`, the activity feed, the notifier's event fields; tests (`TestEveryKindSaysTheSameWordsEverywhere`
  passes).

## Phase 3: API and MCP (US1–US5)

- [ ] T009 `internal/api/scans.go` + `api/openapi.yaml` 0.49.0 (D12): routes, schemas, `image_vulnerable` mapping,
  the attention kind and webhook properties; parity and scope tests.
- [ ] T010 `internal/mcpserver`: `get_app_scan` (D12) with the contract test.

## Phase 4: Console (US1, US5, US6)

- [ ] T011 `internal/ui`: the Security panel and part, the history counts, Scan now, Operator › Security with the
  settings and the lists, the nav entry, the live notice (D13); tests (pages render, no secret, forms without
  JavaScript); `tools/ui-css.sh build`.

## Phase 5: Lab, docs (all)

- [ ] T012 `cmd/caravana`: the gate step `image-scan` (US1–US5: alpine:3.14.0 findings, the item and notifications,
  the fixed image resolves it, the daily rescan finds a dropped advisory again, a private image with a sentinel
  password, the gate's 422) and the console step `console-security`.
- [ ] T013 Lab run: check Grype's stderr phrases for D4's classes, the volume's node, timings; record them in
  `research.md` (R4).
- [ ] T014 Docs (D14): `docs/image-scanning.md`, `changelog.d/image-scanning.md`, CLAUDE.md, constitution 1.2.1.
