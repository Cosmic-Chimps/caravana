# Tasks: Placement and drain

Decisions are in [plan.md](./plan.md) (D1–D14). Each task names the files it touches; tests come with their task.
Phases 1 to 4 are each one piece of work for a sub-agent, reviewed before the next starts.

## Phase 1: Model, translator, Compose (US1, US2, US4)

- [x] T001 `internal/app/placement.go`: `Placement`, `Preference`, the limits, `ValidLabelName`,
  `ValidLabelValue`, `CheckLabels`; `Service.Placement`; validation in `validate.go` (D1) with a table test of
  every field path; a revision test that an existing app's document doesn't change.
- [x] T002 `internal/nomadjob`: the meta keys and attributes, `placementBlocks`, constraints and affinities on
  the group and on the schedule run (D2); golden `placement` JSON and HCL; HCL writer blocks and the reflection
  test.
- [x] T003 `internal/compose`: `OverlayService.placement` (D10) with a fixture and the renamed field errors.

## Phase 2: Store, deploy, service, API (US1–US3, US5)

- [x] T004 `internal/store`: migration `0032_placement_drain.sql`, `Server.Labels`, the move functions (D3);
  tests.
- [x] T005 `internal/deploy` (+ `deploytest`): `SetNodeLabels`, `NodeLabels`, `SetNodeEligible`, `MoveCopy`,
  `WaitCopyMoved`, `checkPlacement` in `Register`, `PlacementError`, `QueuedCopies` (D4); the fake's endpoints;
  tests.
- [x] T006 `internal/service`: labels and the reconcile (D5), `MoveWorkOff`, `runMove`, `resumeMoves`,
  `AcceptWork` (D6, D7), the view fields, promotion and copy (D11); tests.
- [x] T007 `internal/service`: the two needs-you kinds everywhere they are worded and notified (D8); tests.
- [x] T008 `internal/api` + `api/openapi.yaml` 0.46.0: the schema, the three server operations, the codes, the
  mapping of `PlacementError` (D9); parity and scope tests.
- [x] T009 `deploy/install/caravana-policy.hcl` `node:write` with its comment; `deploy/dev/install-test.sh`
  checks the line (D13).

## Phase 3: Console (US3, US4)

- [x] T010 `internal/ui`: the server page's Labels row and form, the head's actions, the move panel, the
  confirmation page, Accept work again, the list's labels and words (D12); tests.
- [x] T011 `internal/ui`: Configure's Placement panel and form, `applyEdit` keeps placement, the Compose form
  keeps the overlay's (D12); tests; `tools/ui-css.sh build` if needed.

## Phase 4: Proof and docs

- [~] T012 Gate step `placement` (passed in the lab 2026-11-17, 17 s) (`cmd/caravana/gate_placement_test.go`, registered in `gate_test.go`) and the
  console step `console-placement` (`console_placement_test.go`), run in the lab.
- [~] T013 `deploy/dev/cluster-test.sh` (written: steps 6b and 6c; not run yet) steps for labels, placement and the move (plan "Tests"); run it.
- [x] T014 `docs/placement-and-drain.md`, `docs/plan.md`, `CLAUDE.md`, `changelog.d/placement-and-drain.md` (D14).
- [ ] T015 Every CI check: build, vet, staticcheck, `go test ./...` and `-race`, gofmt, the arm64
  cross-compile, `go mod tidy -diff`, `tools/ui-css.sh check`, `tools/ui-vendor.sh check`,
  `tools/release.sh check`, shellcheck.