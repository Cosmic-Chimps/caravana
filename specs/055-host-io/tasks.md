# Tasks: Network and disk I/O per server

**Input**: `plan.md` (D1–D13), `spec.md`. Each phase runs `go build ./... && go vet ./... && go test ./...` and `gofmt -l .`;
the last runs the full CI list.

## Phase A: the agent (US1, US2)

- [x] T001 [FR-001, FR-002, FR-003] `internal/nodeagent/io.go`: `IOReport`, `parseNetDev`, `parseDiskstats`, `parseMountinfo`,
  `deviceOf`, `netCounters`, `diskCounters`, `dockerRoot`, `Agent.ioReport`; `Report.IO` (`omitempty`). Fixtures in
  `internal/nodeagent/testdata/proc/`; `io_test.go`.

## Phase B: the console's rates and storage (US1, US2, US3)

- [x] T002 [FR-004, FR-005] `internal/service/hostio.go`: `NodeIO` (lenient decode), `checkIO`, `ioRates`, `hostIOState`, `HostIO`,
  `HostIOSeries`; `NodeReport.IO`; `NodeSync` notes it and doesn't store it; `RemoveServer` forgets; `serverNode.Index`;
  `Host.IO`; the sampler's `addHostRows` and `foldHosts`. `hostio_test.go`.
- [x] T003 [FR-005] `internal/store/migrations/0044_host_io.sql`; `HostSample` columns, insert, scan; round-trip and 0043
  migration tests (`TestMigration0019…` skips 0044 too, since 0044 alters 0019's table).

## Phase C: API (US4)

- [x] T004 [FR-006] `api/openapi.yaml` 0.61.0 (`HostIO`, `getHost.io`), `internal/api/metrics.go`, `host_io_test.go` (a real agent
  join and syncs; `no_agent`; a bad block).

## Phase D: console (US1, US2, US3)

- [x] T005 `internal/ui/opsio.go` + `srv-io` in `opsserver.html`; `internal/ui/monitoringio.go` + the host section's template;
  `opsio_test.go`.

## Phase E: records and gates

- [x] T006 `docs/host-io.md`, `changelog.d/055-host-io.md`, CLAUDE.md paragraph, this spec.
- [x] T007 `deploy/dev/install-test.sh` `check_host_io` (a real agent on a Lima VM: two syncs, then non-null rates). Written,
  shellchecked, **not run** (about 25 minutes).
- [ ] T008 Run `install-test.sh` and look at the server page and Monitoring on a real VM: the maintainer's.
