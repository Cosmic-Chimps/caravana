# Tasks: Workload identities for apps

**Plan**: [plan.md](./plan.md)

## Part A — identities on a service

- [ ] **T001** `app.Identity`, `Service.Identities`, `TTLOrDefault`, validation (FR-002, D1) with a table test of
  every rule and path.
- [ ] **T002** `nomadjob`: `workloadIdentities(svc)` in `task()` and `ScheduleRunJob`; golden `identity`
  (`.golden.json`, `.golden.nomad.hcl`); the HCL writer's `identity` block; reflection test passes; schedule run
  test with identities.
- [ ] **T003** Compose overlay `identities` (D3), error paths, a test; console Compose form carries the kept
  overlay's identities through a deploy from the form.
- [ ] **T004** Promotion keeps the target's identities, a new service brings its own; copy copies (D4); tests.
- [ ] **T005** API: `Identity` schema, `Service.identities`, 0.41.0; parity test.
- [ ] **T006** Console Configure read-only identities (D5); `applyEdit` keeps them; tests.

## Part B — the cluster's issuer

- [ ] **T007** `Service.ClusterIssuer()` (D7); `issuerFor` (D9) with a table test; `NodeAnswer.Issuer`;
  `NodeReport.Issuer` read; `inFlight` in `Service`.
- [ ] **T008** Agent: `Answer.Issuer` validated (D8), `Report.Issuer`, `State.Issuer`, rendered in `server {}`
  (golden `server-issuer.golden.hcl`, `first-issuer.golden.hcl`), restart (or reload, R1) on change for a server
  only; tests.
- [ ] **T009** Discovery routes (D10) in `internal/api` with `deploy.NomadDiscovery`; openapi; tests (404, 200,
  502, not on the webhook listener, deploy-token scope walk).

## Part C — docs, lab, Aspire

- [ ] **T010** `docs/workload-identity.md` (Bella recipe, the file path, renewal, issuer = console URL, what a
  domain change does, registering the workload), `changelog.d/workload-identity.md` (Added; Upgrade: Nomad servers
  restart once, one at a time, after the update), CLAUDE.md feature paragraph.
- [ ] **T011** Gate step `workload-identity` (plan §Tests) and `exportMatchesLive` for it; run in the lab;
  research R3, R4 into research.md.
- [ ] **T012** `install-test.sh` checks (D11); run it; research R1, R2 into research.md.
- [ ] **T013** Aspire (`../aspire-hosting-caravana`, branch `workload-identity`, not pushed):
  `WithCaravanaIdentity(name, audience, ttl)` and `WithCaravanaBellaIdentity(audience)` writing the overlay's
  `identities`; its tests.
