# Tasks: Autoscaling

Decisions are in [plan.md](./plan.md) (D1–D13). Each task names the files it touches; tests come with their task.
Phases 1 to 3 are each one piece of work for a sub-agent, reviewed before the next starts.

## Phase 1: Model, translator, rule, Compose (US1, US3)

- [x] T001 `internal/app/scale.go`: `Scale`, `ScaleTarget`, the limits, `Cooldown`, `Kind`, `Service.CountFor`;
  `Service.Scale`; validation in `validate.go` (D1) with a table test of every field path; a revision test.
- [x] T002 `internal/nomadjob`: the spread for a scaled service (D2); golden `scale` JSON and HCL.
- [x] T003 `internal/metrics/scale.go`: `DecideScale`, `OverTargetAtMax` (D3) with the table tests.
- [x] T004 `internal/compose`: `OverlayService.scale` (D10) with a fixture and the renamed field errors.

## Phase 2: Store, deploy, service, API (US1, US2, US4, US5)

- [x] T005 `internal/store`: migration `0033_autoscaling.sql` (app_actions rebuilt, `service_counts`), the
  action fields, `LastScale`, `ScaleVersions`, `RecentScales`, `ServiceCounts`, `SetServiceCount`, `DeleteApp`
  (D6); tests.
- [x] T006 `internal/deploy` (+ `deploytest`): `Register` keeps the count (D4), `ScaleState`, `Scale` (D5), the
  `VersionAliases` hook in `Copies`/`AllCopies`/`LogCopies` (D7); the fake's scale endpoint; tests.
- [x] T007 `internal/service`: the counts after a deploy, `statusOf` with counts, the alias hook wired at start
  (D7), the autoscaler pass and reasons (D8), `scale_at_max` everywhere a kind is worded and notified (D9),
  activity words, promotion and copy (D10), export header (D10); tests.
- [x] T008 `internal/api` + `api/openapi.yaml` 0.47.0 (D11); parity and scope tests.

## Phase 3: Console (US3, US4)

- [ ] T009 `internal/ui`: Configure's Scaling panel and form, `applyEdit` keeps scale, `keepScale` in the
  Compose form, Recent actions words, Monitoring's copies cell and Scaling list, the Running tab's line (D12);
  tests; `tools/ui-css.sh build` if needed.

## Phase 4: Proof and docs

- [ ] T010 Gate step `autoscale` (`cmd/caravana/gate_autoscale_test.go`, registered in `gate_test.go`), run in the
  lab.
- [ ] T011 `docs/autoscaling.md`, `docs/plan.md`, `CLAUDE.md`, `changelog.d/autoscaling.md` (D13).
- [ ] T012 Every CI check: build, vet, staticcheck, `go test ./...` and `-race`, gofmt, the arm64
  cross-compile, `go mod tidy -diff`, `tools/ui-css.sh check`, `tools/ui-vendor.sh check`,
  `tools/release.sh check`, shellcheck.
