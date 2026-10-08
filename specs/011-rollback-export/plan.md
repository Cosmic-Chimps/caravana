# Implementation Plan: Rollback and export

**Branch**: `011-rollback-export` | **Date**: 2026-10-08 | **Spec**: [rollback-export.spec.md](./rollback-export.spec.md)

**Scale note**: about 22 files in seven packages, plus the contract and the gate. The new code
that carries the most risk is the HCL writer in `nomadjob`: escaping mistakes are silent until
Nomad parses the file. Golden files, a field-coverage test, and the gate's parse-and-plan check
guard it. Everything else reuses the deploy path that already exists.

## Summary

Every deployment now records the app definition it deployed. Redeploying a past healthy
deployment saves that definition as the app's own and runs the normal deploy with the recorded
digests pinned. Tags aren't resolved again. A registry check refuses the redeploy first if an
image is gone, and the existing secret check refuses it if a secret it uses has no value. Export
renders `nomadjob.Translate`'s job for a deployment as HCL with a small hand-written writer, plus
a header comment that lists what the job needs from the cluster. No new module and no new Nomad
capability.

## Project Structure

```text
internal/store/migrations/0006_rollback.sql      # deployments.definition, deployments.source_deployment
internal/store/deployments.go (+ tests)          # record carries Definition and Source
internal/registry/client.go (+ tests)            # Exists: HEAD the manifest of a digest
internal/deploy/deploy.go (+ tests)              # CheckDigests; Export (Translate + HCL + header)
internal/nomadjob/hcl.go, hcl_test.go, testdata/*.golden.nomad.hcl, translate.go (package doc)
internal/service/deploy.go, rollback.go (+ tests)   # pinned digests through saveAndDeploy; RedeployVersion, RedeployImpact, Export; removedSecrets
internal/api/deployments.go, export.go, api.go, errors.go (+ tests), parity_test.go
api/openapi.yaml                                 # 0.8.0 → 0.9.0
internal/ui/app.go, configure.go, view.go, templates/app.html (+ tests), static/app.css
cmd/caravana/gate_test.go, console_lab_test.go   # step rollback-export; console redeploy step
CLAUDE.md, docs/plan.md
```

**Structure Decision**: the HCL writer is pure and sits next to `Translate` in `nomadjob`, the
only package that knows Nomad's job shape (Constitution VI). `deploy` puts the job and its header
together. `service` decides what may be redeployed or exported. API and console call
`service` only.

## Constitution Check

| Principle | Assessment |
| --- | --- |
| I. Small by Default | PASS. No new Go module (R5 rejects `hclwrite`), no new service. One migration adding two columns. |
| II. One Model, Many Inputs | PASS. The recorded definition is an `app.App`. Redeploy and export both go through `Translate`, which stays pure. The HCL writer is pure and covered by golden files. |
| III. Safe, Reversible Deploys | PASS, and it is the feature's point. A redeploy uses the recorded digests and is a new deployment and job version, with the normal health checks and auto-revert. Secrets stay declarative: the past version's names, today's values, and removed secrets are warned about. |
| IV. Fail Loudly | PASS. A missing image, a missing secret value, a changed rule, an unhealthy source or a missing definition is refused before anything changes, naming the reason or field. The coverage test fails naming any job field the HCL writer would drop. |
| V. API-First | PASS. Redeploy and both exports are API operations in `api/openapi.yaml` 0.9.0. The console calls the same service, and the parity test lists the new routes. The export's body is HCL, and its errors stay JSON. |
| VI. Nomad-Native, No Lock-In | PASS. This feature is where the constitution lets HCL be generated. Exports are plain jobs that run without Caravana, and the gate proves it with Nomad's own parse and plan. |
| VII. Secure by Default | PASS. Definitions are stored without secret values, as apps are. Exports name secrets and their variable paths and never hold a value or token. Nothing new is logged. |
| VIII. Plain, Tested Go | PASS. Table tests for the store, registry, service and handlers. Golden HCL files. A lab gate step. |

Re-checked after design: no change.
