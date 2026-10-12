# Implementation Plan: Memory per service

**Branch**: `013-memory-per-service` | **Date**: 2026-10-11 | **Spec**: [memory-per-service.spec.md](./memory-per-service.spec.md)

## Summary

Each service gets two memory numbers. The reservation is what Nomad's scheduler counts
(`MemoryMB`, default 256 MB). The ceiling is what the service may use while the node has memory
spare (`MemoryMaxMB`, default 1 GB). The translator computes both, and Compose's
`mem_reservation` and `mem_limit` map to them. The installer and the lab turn on Nomad's memory
oversubscription, without which Nomad ignores the ceiling. When it's off, Nomad's own register
warning reaches the deployment through the existing path. The console shows both numbers, and
the result ships as `v0.1.1`.

## Project Structure

```text
internal/app/app.go, validate.go (+ tests)          # Resources.MemoryMaxMB; ceiling ≥ reservation
internal/nomadjob/translate.go (+ tests, testdata/) # Settings.DefaultMemoryMaxMB; reservation and ceiling rules
internal/config/config.go (+ tests)                 # CARAVANA_DEFAULT_MEMORY_MAX_MB (1024); default reservation 256
internal/compose/fields.go, service.go (+ tests, testdata/)   # mem_reservation / reservations.memory; mem_limit is the ceiling
internal/ui/configure.go (+ test)                   # "256 MB reserved, up to 1024 MB"
api/openapi.yaml                                    # 0.10.0: Resources.memoryMaxMB
deploy/install/install.sh, deploy/dev/lab.sh        # scheduler memory oversubscription on
deploy/examples/hollo/compose.yaml                  # comment no longer about 64 MB
CLAUDE.md, docs/plan.md
```

**Structure Decision**: the rules live in the pure translator and the app model. Compose only
maps its two fields. The cluster setting belongs to the installer and the lab, the only things
holding a management token.

## Constitution Check

| Principle | Assessment |
| --- | --- |
| I. Small by Default | PASS. A 256 MB reservation keeps several services schedulable on a 2 GB node. No new module or service. |
| II. One Model, Many Inputs | PASS. One new field on `app.Service.Resources`, from JSON and Compose alike. `Translate` stays pure, with golden files. |
| III. Safe, Reversible Deploys | PASS. Running apps aren't touched until their next deploy. A service past its ceiling is restarted by Nomad, as today. |
| IV. Fail Loudly | PASS. A reservation above the ceiling is refused, naming the field. A ceiling Nomad ignores shows up as a deployment warning. Compose reservations other than memory stay refused, naming the path. |
| V. API-First | PASS. `api/openapi.yaml` 0.10.0 adds `memoryMaxMB` in the same change. It's additive, so clients keep working. |
| VI. Nomad-Native | PASS. Nomad's own `memory_max` and scheduler configuration, through the official client and CLI. |
| VII. Secure by Default | PASS. Caravana's scoped policy doesn't change. The scheduler setting is applied with the management token, by the installer only. |
| VIII. Plain, Tested Go | PASS. Table tests for the rules, golden files, a Compose fixture, and the installer VM test. |

Re-checked after design: no change.
