# Implementation Plan: Console v2

**Branch**: `014-console-v2` | **Date**: 2026-10-12 | **Spec**: [console-v2.spec.md](./console-v2.spec.md)

**Scale note**: the biggest console feature since 007, about 35 files in six packages, but no
migration, no new module and no new Nomad capability in the policy. The usage numbers do need
`telemetry { publish_allocation_metrics = true }` in Nomad's own config, which the installer's
`nomad.hcl` sets, so an existing host reruns the installer (the 0.2.0 changelog says so). The risk is in the two forms: the edit
form must never drop a field it doesn't show (merge from the stored revision, tested on every
example app), and the Compose form must never echo a secret value. The four parts are
independent and land in story order.

## Summary

The console's Configure tab becomes an edit form whose save is a conditional deploy: the form
carries the app's **revision** (a hash of the stored definition) and the service refuses the
save if the app changed since (`If-Match` → 412 `app_changed` in the API). New app gains a
Compose tab with a server-rendered preview, then create-only deploy (`If-None-Match: *`), and
Configure gains "Deploy from Compose" with the overlay prefilled from the stored app. The Running
tab reads each copy's CPU and memory from Nomad's allocation stats every 5 s, only while the tab
is open (`listCopies?usage=true`). The Apps home opens with a "needs you" list derived on every
read from deployments, copies (now with out-of-memory and last restart) and backups
(`GET /v1/attention`), refreshed by a new `attention` change notice.

## Technical Context

**Language/Version**: Go 1.27 (`go.mod`), no CGO

**Primary Dependencies**: standard library `net/http`, `html/template`; htmx + SSE extension
(vendored); `github.com/hashicorp/nomad/api`; `compose-go` (inside `internal/compose` only). No
new module.

**Storage**: SQLite (`modernc.org/sqlite`). No migration: revision and attention are computed.

**Testing**: table-driven `testing`; `deploytest` / `registrytest` / `secretstest` fakes;
`httptest` for API and console; `TestConsoleLab` and `TestLab` against the Lima lab on the
scoped policy token.

**Target Platform**: Linux amd64/arm64 hosts (single node); the console in any current browser,
with and without JavaScript.

**Project Type**: web service (control plane) with a server-rendered console.

**Performance Goals**: usage at most 10 s old while the Running tab is open (SC-004); needs-you
updates within 30 s (SC-003); a needs-you read costs one Nomad request plus four store queries.

**Constraints**: forms work without JS (FR-025); secret values never rendered (FR-012); usage
read only while watched (FR-015); fits the 2 GB VM (no background polling when nobody looks).

**Scale/Scope**: a handful of apps and copies on one node; Compose files up to 1 MiB.

## Constitution Check

| Principle | Assessment |
| --- | --- |
| I. Small by Default | PASS. No new module, service or migration. Usage is read on demand, not polled in the background. |
| II. One Model, Many Inputs | PASS. The edit form and the Compose forms both end as `app.App` through the existing `Validate` and `Translate`; `Translate` is untouched. The Compose paths reuse `compose.Translate`. |
| III. Safe, Reversible Deploys | PASS. Every save is a normal deploy (digest pinning, health check, auto-revert). Env, domains and secrets stay declarative; the edit replaces the lists it shows, and the merge keeps what it doesn't show. The revision check makes an edit an idempotent upsert of what the owner saw, never of a definition they didn't. |
| IV. Fail Loudly | PASS. Form errors sit at the field (model paths mapped to inputs; unmapped ones named in the notice). Compose refusals name the Compose path. The Compose overlay carries backup so a redeploy can't drop it silently (R11). |
| V. API-First | PASS. Conditional deploys (`If-Match`, `If-None-Match`), `listCopies?usage=true` and `listAttention` are in `api/openapi.yaml` 0.11.0 in the same commits as their routes; the parity test lists every new console route. Errors stay JSON, 412 and 409 with codes. |
| VI. Nomad-Native | PASS. Alloc stats and task events are read in `deploy` only; `service` and above see plain values. No new capability (stats need `read-job`). |
| VII. Secure by Default | PASS. Secret inputs are password fields, never echoed, never logged; the request log records only method, path, status and duration. Nothing new is stored. Every new route needs a session or a token. |
| VIII. Plain, Tested Go | PASS. Pure functions for the merge (`applyEdit`), the path-to-field map, attention rules and usage maths, each table-tested; lab steps for the Nomad-facing parts. |

No violations; Complexity Tracking is empty.

Re-checked after design: no change.

## Project Structure

### Documentation (this feature)

```text
specs/014-console-v2/
├── console-v2.spec.md
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── contracts/
│   ├── api.md
│   └── console.md
├── checklists/requirements.md
└── tasks.md            # /speckit-tasks
```

### Source Code

```text
internal/store/apps.go (+ tests)                 # AppRecord.Revision; SaveAppIf; ErrRevisionMismatch
internal/store/backups.go (+ tests)              # LatestBackupRuns
internal/deploy/copies.go (+ tests)              # Copy.LastRestart, Copy.OutOfMemory
internal/deploy/usage.go (+ tests)               # Usage: alloc stats per running copy, 4 at a time, 2 s each
internal/deploy/deploytest/                      # fake stats and task events
internal/service/deploy.go, compose.go (+ tests) # Condition; DeployIf; DeployComposeIf; ErrAppChanged
internal/service/usage.go (+ tests)              # CopyUsage; CopiesWithUsage (reservations with defaults applied)
internal/service/attention.go (+ tests)          # Attention rules (pure attentionOf + the reads)
internal/service/changes.go, backups.go, apps.go # ChangeAttention published
internal/api/apps.go, deployments.go, compose.go, copies.go, attention.go, errors.go, api.go (+ tests)
internal/api/parity_test.go                      # new console routes
api/openapi.yaml                                 # 0.10.0 → 0.11.0
internal/ui/edit.go (+ tests)                    # editForm, applyEdit, model path → form field
internal/ui/configure.go, app.go (+ tests)       # Configure as a form; POST /apps/{id}/configure
internal/ui/composeform.go (+ tests)             # Compose draft: preview and deploy, new and existing
internal/ui/newapp.go (+ tests)                  # Image / Compose tabs
internal/ui/attention.go (+ tests), apps.go, view.go   # needs-you region, sentences, headline
internal/ui/live.go, app.go                      # usage event on the Running tab; attention on /live
internal/ui/templates/{app,apps,newapp,parts,compose}.html
internal/ui/style/*.css → internal/ui/static/app.css   # tools/ui-css.sh build
cmd/caravana/console_lab_test.go, gate_test.go   # console steps; gate step console-v2-reads
CLAUDE.md, docs/plan.md
```

**Structure Decision**: the rules (what counts as a problem, near the ceiling, a stale save)
live in `service` so the API and console agree; Nomad's stats and events stay in `deploy`; form
parsing and the merge from the stored revision live in `ui`, because the API's equivalent is
"GET, change, deploy with `If-Match`" and needs no merge.

## Delivery order

1. **US1 Edit** (P1): store revision → service `Condition` → API `If-Match` + contract → console
   form, merge, errors, stale panel → lab step.
2. **US5 Needs you** (P2): copy facts (`LastRestart`, `OutOfMemory`) → store `LatestBackupRuns`
   → service `Attention` + `ChangeAttention` → API `listAttention` → console region + headline →
   lab step.
3. **US2 Compose on New app** (P2): `If-None-Match` → `DeployComposeIf` → console preview and
   create → lab step.
4. **US3 Deploy from Compose** (P3): prefilled overlay and added/removed services.
5. **US4 Usage** (P3): `deploy.Usage` → `CopiesWithUsage` → `listCopies?usage=true` → Running
   columns and `usage` event → lab step and gate step.
6. Docs: CLAUDE.md "Where we are", `docs/plan.md` (012.5 done, renumber Phase 2.5).

Each story is shippable on its own; US3 depends on US2's form code.

## Complexity Tracking

None.
