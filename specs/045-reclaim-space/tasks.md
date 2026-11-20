# Tasks: Reclaim space (unused images and orphan volumes)

**Input**: `plan.md` (D1–D7), `contracts/agent.md`, `contracts/api.md`, `research.md`.

**Tests**: required (constitution VIII). Each phase runs `go build ./... && go vet ./... && go test ./...` and
`gofmt -l .` before it is done; the last phase runs the full CI list.

## Phase A — agent, store, service core (US1, US2, US3)

- [x] T001 [US2] [FR-001, FR-004] `internal/nodeagent/space.go`: `SpaceReport` and its types; `a.space(ctx, st)`
  (the inventory from the Runner, research R4; `du` of the volume directories every 15 minutes with the sizes in
  `state.json`); `parseDockerDF`, `parseHumanSize`, `removable(images, keep)` pure; `a.clean` (contract §3);
  `Answer.Clean` and its validation in `validate.go`; `applySync` step 4; `Report.Space`; `caravana node space` in
  `cmd/caravana/node.go`. Tests with recorded Docker outputs in `internal/nodeagent/testdata/docker/` (taken from
  the lab VM: `limactl shell caravana-lab sudo docker …`), the fake Runner, `ParseSync` refusals, a clean that
  runs once per ID, the interrupted case. Add `du` to the Runner's documented list.
- [x] T002 [US1, US2, US3] [FR-003, FR-008] `internal/store/migrations/0035_reclaim_space.sql` and
  `internal/store/space.go` (D1) with round-trip tests; the two feed legs in `store/activity.go` with a test.
- [x] T003 [US2] [FR-002] `internal/service/space.go`: `keepFrom` (pure, table test: last N healthy per app, a
  pending deployment, a stopped app's definition, helpers, dedup, the cap) and `keepDigests`.
- [x] T004 [US1, US3] [FR-007] `internal/deploy/volumes.go`: `AllHostVolumes` (list, no prefix), `HostVolumeDetail`
  (Get: HostPath, NodeID, Allocations), `DeleteHostVolume(id)` with the in-use refusal as `ErrVolumeInUse`;
  `deploytest` fakes. `internal/service/orphans.go`: `classifyOrphans` (pure, table test), the orphan view with
  size, place, "last deployed with it", in use.
- [x] T005 [US2] [FR-003, FR-005] `internal/service/cleanups.go`: `RequestCleanup`, `cleanupHandoff` in
  `NodeSync` (`NodeReport.Space`, `validateReport`, `NodeAnswer.Clean`), `cleanupPass` with the three rules
  and `SetCleanupPassGap`, `CleanupSettings`/`PutCleanupSettings`, `ServerSpace`. Tests with a fake clock and
  the store: a request handed out once, settled from the report, failed after 10 minutes unpicked, lost after
  30, the daily run once per day, `no agent` for an unmanaged node.
- [x] T006 [US3] [FR-008, FR-010] `DeleteOrphanVolume` (no archive yet: `backUpFirst` refused
  `ErrNoBackupDestination` until Phase D), the activity words for both records (`service/activity.go`, tests),
  `unmountedVolumes`'s new text naming the server (test), the disk item's link in `internal/ui/attention.go`
  (`#space`).
- [x] T007 Update `specs/031-add-a-server/contracts/node.md` (§5, §6, §9 pointers to `contracts/agent.md`) and
  `api.md` §4 (the report's `space`, the answer's `clean`).

## Phase B — API and MCP (US1–US3)

- [x] T008 [FR-011] `internal/api/space.go`: the eight operations and codes of `contracts/api.md`; the route
  table; `api/openapi.yaml` 0.50.0 (schemas, operations, codes, `ActivityEntry` unchanged); tests per operation
  (answers, refusals, `no-store`), the scope walk unchanged (default deny), the parity test (console routes of
  Phase C map to these).
- [x] T009 [FR-011] `internal/mcpserver`: `get_server_space` read tool, contract test, `docs/mcp.md` line.

## Phase C — console (US1, US2, US3)

- [ ] T010 [FR-011] Server page Space section (D6): `internal/ui/opsspace.go`, the templates in
  `opsserver.html` and a part `/parts/ops/servers/{name}/space`, the clean-up page (`opsclean.html`), the delete
  page (`opsvolumedelete.html`), the settings form, Overview's line, the app Volumes panel's link; words in the
  console's style (`DESIGN.md`); `tools/ui-css.sh build`; tests (`opsspace_test.go`: the section with and without
  an agent, a wrong typed name, the refusals drawn, no script needed).
- [ ] T011 [SC-002, SC-003] `cmd/caravana/console_space_test.go`, step `console-space` in `TestConsoleLab` (D7).

## Phase D — back up first (US4) and Docker's volumes (US5)

- [ ] T012 [US4] [FR-009] `internal/nomadjob/archive.go` `VolumeArchiveJob` + `ArchiveScript` with the golden
  `testdata/volume-archive.golden.json` and a script test under `sh` with a fake `rclone` (as 036 did);
  `internal/deploy/archive.go` `ArchiveVolume`; the service's archiving path in `DeleteOrphanVolume` (goroutine,
  settle, `OpenVolumeDeletions` failed at start); API 202 path; the console's box.
- [ ] T013 [US5] [FR-006] `dockerVolumes` through the request, the console box, the daily run never sets it
  (test).

## Phase E — lab, docs, release note

- [ ] T014 [SC-001..SC-004] `cmd/caravana/gate_space_test.go`, gate step `space` (D7); run it alone
  (`CARAVANA_LAB=1 go test ./cmd/caravana -run 'TestLab/^(api-needs-token|space)$' -v`) and `console-space`.
- [ ] T015 [SC-001] `deploy/dev/install-test.sh` `check_space_host` (D7); run `deploy/dev/install-test.sh`
  (about 20 minutes) and record the result in the plan's Deviations.
- [ ] T016 `docs/reclaim-space.md` (operator doc: what is removed, what is kept, the setting, the agent),
  `changelog.d/045-reclaim-space.md`, the `CLAUDE.md` "Where we are" paragraph, `docs/plan.md` mention.
- [ ] T017 The full check: `go mod tidy -diff`, `go build`, `go vet`, `go tool staticcheck`, `go test -race`,
  `gofmt -l`, the cross-compile, `tools/ui-css.sh check`, `tools/ui-vendor.sh check`, `tools/release.sh check`,
  shellcheck (docker `koalaman/shellcheck`) on the scripts touched.
