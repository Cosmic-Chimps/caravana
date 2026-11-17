# Tasks: Published ports

Decisions are in [plan.md](./plan.md) (D1–D13). Each task names the files it touches; tests come with their task.

## Phase 1: Model and translator (US1, US2)

- [x] T001 `internal/mesh`: `PublicHostNetwork = "public"` with its comment.
- [x] T002 `internal/app`: `Port.Protocol`, `Port.Published`, the constants, `SpeaksTCP`, `IsPublished`,
  `Service.Publishes`, `ReservedPorts`, `MaxPublishedPorts` (D1); validation rules of D2 with a table test of every
  field path; a revision test that an existing app's document doesn't change.
- [x] T003 `internal/nomadjob`: reserved ports on `public`, stop-then-start for publishing groups (D3); golden
  `published` JSON and HCL; HCL writer and reflection test; export header line; schedule run test.

## Phase 2: Deploy flow (US1, US2)

- [x] T004 `internal/service`: `checkPortsTaken`, `PortTakenError`, `ErrPortTaken` (D4) with tests.
- [x] T005 `internal/deploy` (+ `deploytest`): `checkPublicNetwork`, `PublicNetworkError`, `ErrNoPublicNetwork`
  (D5); volume placement for publishing services (D6); tests.
- [x] T006 `internal/service/promote.go`: promotion keeps the target's numbers, copy drops them (D7); tests.
- [x] T007 `internal/api` + `api/openapi.yaml` 0.45.0: fields, overlay, both 409 codes and their mapping (D10);
  parity tests.

## Phase 3: Compose (US3)

- [x] T008 `internal/compose`: protocols, the tcp+udp merge, expose `/udp`, overlay `publish`, the new warning (D8);
  tests; fixtures unchanged.

## Phase 4: Console (US4)

- [x] T009 `internal/ui`: Configure's read-only block, save keeps the fields, Compose form keeps `publish` (D9); tests.

## Phase 5: Hosts

- [x] T010 `internal/nodeagent`: `State.PublicInterface`, detection in `InitFirst`, validation, the template block,
  goldens (D11).
- [x] T011 `deploy/dev/nomad.hcl`, `deploy/dev/lab.sh`, `deploy/dev/lab.yaml` comment (D12).
- [x] T012 `deploy/dev/install-test.sh` checks (D11).

## Phase 6: Proof and docs

- [x] T013 Gate step `published-ports` (`cmd/caravana/gate_published_test.go`, registered in `gate_test.go`), run in
  the lab.
- [x] T014 `docs/published-ports.md`, `docs/plan.md`, `CLAUDE.md`, `changelog.d/published-ports.md` (D13).
- [x] T015 Every CI check: build, vet, staticcheck, `go test ./...` and `-race`, gofmt, the arm64 cross-compile,
  `go mod tidy -diff`, `tools/ui-css.sh check`, `tools/ui-vendor.sh check`, `tools/release.sh check`, shellcheck.
