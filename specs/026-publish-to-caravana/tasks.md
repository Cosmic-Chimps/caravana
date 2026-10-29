# Tasks: PublishToCaravana (026)

## Caravana (this repository)
- [X] T001 Compose: default `driver` on networks and volumes accepted, one networks warning per
  file, the network refusal's message fixed; tests.
- [X] T002 `entrypoint`: `app.Service.Entrypoint`, Compose mapping, Translate (docker
  `entrypoint`), HCL export, validation, goldens, openapi 0.21.0, console read-only; tests.
- [X] T003 Fixtures `internal/compose/testdata/aspire/` (as generated: fails naming the variable;
  resolved: translates, golden app, expected warnings).
- [X] T004 Docs: CHANGELOG, CLAUDE.md 026 paragraph, docs/plan.md 026 row.
- [ ] T005 Full check (+ TestLab if anything outside compose/app/nomadjob changed).
  - Left open 2026-10-29: nothing in this repository (this file, `docs/plan.md`, `CHANGELOG.md`) records that the full check ran for this feature, so it is not ticked. Its checks were part of CI on the merge to `main`; the maintainer can tick it or drop it.

## aspire-hosting-caravana (`../aspire-hosting-caravana`)

Tracked in that repo (`Cosmic-Chimps/aspire-hosting-caravana`), not here. Published as `CosmicChimps.Aspire.Hosting.Caravana` 0.1.0 on NuGet (2026-10-20); the Phase 2.75 gate passed on 2026-10-21 with Frunchy (`docs/plan.md`). Ticked 2026-10-29 from that record.

- [X] A001 Solution, projects, LICENSE (MIT), nuget.config, .gitignore (incl. `aspire-output/`),
  CI workflow; `PublishToCaravana`, settings, values, per-resource extensions, the pipeline step.
- [X] A002 The transform (pure) + tests on the fixture.
- [X] A003 The API client, deploy flow and waiter + tests with a fake handler.
- [X] A004 README and the example AppHost.
- [X] A005 End to end against the lab's Caravana (main session).
