# Tasks: PublishToCaravana (026)

## Caravana (this repository)
- [X] T001 Compose: default `driver` on networks and volumes accepted, one networks warning per
  file, the network refusal's message fixed; tests.
- [ ] T002 `entrypoint`: `app.Service.Entrypoint`, Compose mapping, Translate (docker
  `entrypoint`), HCL export, validation, goldens, openapi 0.21.0, console read-only; tests.
- [ ] T003 Fixtures `internal/compose/testdata/aspire/` (as generated: fails naming the variable;
  resolved: translates, golden app, expected warnings).
- [ ] T004 Docs: CHANGELOG, CLAUDE.md 026 paragraph, docs/plan.md 026 row.
- [ ] T005 Full check (+ TestLab if anything outside compose/app/nomadjob changed).

## aspire-hosting-caravana (`../aspire-hosting-caravana`)
- [ ] A001 Solution, projects, LICENSE (MIT), nuget.config, .gitignore (incl. `aspire-output/`),
  CI workflow; `PublishToCaravana`, settings, values, per-resource extensions, the pipeline step.
- [ ] A002 The transform (pure) + tests on the fixture.
- [ ] A003 The API client, deploy flow and waiter + tests with a fake handler.
- [ ] A004 README and the example AppHost.
- [ ] A005 End to end against the lab's Caravana (main session).
