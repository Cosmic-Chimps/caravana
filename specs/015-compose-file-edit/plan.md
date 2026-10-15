# Implementation Plan: Keep and edit the Compose file

**Branch**: `015-compose-file-edit` | **Date**: 2026-10-15 | **Spec**: [compose-file-edit.spec.md](./compose-file-edit.spec.md)

**Input**: Feature specification from `specs/015-compose-file-edit/compose-file-edit.spec.md`

**Scale note**: smaller than 014, about 25 files in five packages. One migration (two nullable
columns), no new module, no new Nomad capability, no new gate step. The risk is drift: a path that
changes the app must keep the file in step or drop it with a warning (SC-004), and a secret value
must never reach a kept file (SC-003). Both are handled at one choke point (`saveAndDeploy`) and
tested per path.

## Summary

Every Compose deploy keeps its request (the file text byte for byte, the name and the overlay,
secret values removed) in a new `compose` column on the app and on the deployment. The app's
revision now covers it, so 014's conditional deploys protect file edits too. Configure for an app
with a kept file opens on 014's Compose form, prefilled from the kept request, and shows the
definition read-only. A Compose app from before this feature gets a "paste it once" note and the
same form, empty. Rollback brings the version's file back, history entries offer the file for
download, and the API reads it with `getCompose` / `getDeploymentCompose` (0.12.0). A deploy of a
plain definition drops the file with a warning.

## Technical Context

**Language/Version**: Go 1.27 (`go.mod`), no CGO

**Primary Dependencies**: standard library `net/http`, `html/template`, `database/sql`; htmx
(vendored, optional here); `compose-go` stays inside `internal/compose`. No new module.

**Storage**: SQLite (`modernc.org/sqlite`). Migration `0007_compose_file.sql`: `apps.compose` and
`deployments.compose`, nullable TEXT (JSON of `compose.Request`, secret values removed).

**Testing**: table-driven `testing`; store tests on a temp database; service tests with the
`deploytest`/`registrytest`/`secretstest` fakes; `httptest` for API and console; round-trip test
for `draftOf`; `TestConsoleLab` step `compose-file-edit`.

**Target Platform**: Linux amd64/arm64 server (the control plane); the console in any browser,
without JavaScript.

**Project Type**: web service (control plane + server-rendered console)

**Performance Goals**: Configure for a Compose app renders in the same time as today (one row
read). Edit-and-deploy of an image tag takes under one minute end to end (SC-001).

**Constraints**: secret values never stored or shown (FR-002); file kept byte for byte; files at
most 512 KiB (`compose.MaxComposeBytes`); plain textarea, no Node build; fits a 2 GB VM (a few
KiB per deployment row).

**Scale/Scope**: a handful of apps and up to hundreds of deployments each on one node.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Assessment |
| --- | --- |
| I. Small by Default | PASS. No new module or service. One migration with two nullable columns in the existing SQLite store. |
| II. One Model, Many Inputs | PASS. The file is kept beside `app.App`, not inside it: the model stays the normalized app, `Translate` is untouched, and nothing after normalization reads the kept file except to show it. `Source.Kind` is read only to pick Configure's note, as 014 already does for display. |
| III. Safe, Reversible Deploys | PASS. A file edit is a normal Compose deploy (digests, health check, auto-revert). Rollback restores the file with the definition. The revision covers the file, so a stale edit is refused instead of overwriting. Secrets stay declarative: kept names with no value keep the value, as in 014. |
| IV. Fail Loudly | PASS. Dropping a kept file always produces a warning (FR-009, R5). A stored file that today's rules refuse is still shown, and the preview names the refused path. Nothing is dropped silently. |
| V. API-First | PASS. `getCompose`, `getDeploymentCompose` and `composeKept` are in `api/openapi.yaml` 0.12.0 in the same commits as the routes. The parity test maps the new console download and the reads. 404 `compose_not_kept` is JSON. |
| VI. Nomad-Native | PASS. Nothing touches Nomad. Export is unchanged (it exports the job, not the file). |
| VII. Secure by Default | PASS. Secret values are removed in the service and again in the store; a sentinel test runs on every page, download and API answer that shows a file (SC-003). The new routes need a session or an API token. The request log still records only method, path, status and duration. |
| VIII. Plain, Tested Go | PASS. `draftOf` and `WithoutSecretValues` are pure and table-tested; store and service per-path tests; no new interface. |

No violations. **Re-checked after design**: no change.

## Project Structure

### Documentation (this feature)

```text
specs/015-compose-file-edit/
├── compose-file-edit.spec.md
├── plan.md               # this file
├── research.md           # R1–R12
├── data-model.md
├── quickstart.md
├── contracts/
│   ├── api.md
│   └── console.md
├── checklists/requirements.md
└── tasks.md              # /speckit-tasks
```

### Source Code

```text
internal/compose/compose.go (+ tests)               # Request.WithoutSecretValues
internal/store/migrations/0007_compose_file.sql     # apps.compose, deployments.compose
internal/store/apps.go (+ tests)                    # AppRecord.Compose; SaveApp/SaveAppIf/InsertApp take the file; revisionOf covers it
internal/store/deployments.go (+ tests)             # DeploymentRecord.Compose; CreateDeployment takes the file
internal/store/*_test.go, internal/{service,api,ui}/*_test.go, cmd/caravana/*_test.go
                                                    # mechanical: nil file at existing call sites
internal/service/deploy.go (+ tests)                # made{file, warnings}; saveAndDeploy/deployWarned; drop warning; Redeploy passes stored file
internal/service/compose.go (+ tests)               # DeployComposeIf keeps the request
internal/service/secretedit.go (+ tests)            # secret edits keep the file, overlay names follow
internal/service/rollback.go (+ tests)              # RedeployVersion restores the version's file
internal/api/compose.go, deployments.go, apps.go (+ tests)   # getCompose, getDeploymentCompose, composeKept
internal/api/errors.go                              # compose_not_kept
internal/api/parity_test.go                         # new console route and reads
api/openapi.yaml                                    # 0.11.0 → 0.12.0
internal/ui/composeform.go (+ tests)                # draftOf (round trip)
internal/ui/configure.go, composedeploy.go (+ tests)  # Compose section, read-only definition, not-kept note, 409 on per-field save
internal/ui/versions.go, ui.go (+ tests)            # history link; GET /apps/{id}/deployments/{deploymentId}/compose
internal/ui/templates/{app,compose}.html            # Compose section on Configure, editor attributes and hint
internal/ui/style/*.css → internal/ui/static/app.css   # tools/ui-css.sh build, only if new classes
cmd/caravana/console_lab_test.go                    # step compose-file-edit
CLAUDE.md, docs/plan.md                             # where we are
```

**Structure Decision**: what is kept and when it is dropped is decided in `service`, at
`saveAndDeploy`, so the API and the console can't disagree. The store only writes what it is
given, in one statement with the app, so the revision always matches. The console reuses 014's
Compose form and adds only `draftOf` and the Configure layout.

## Delivery order

1. **Foundation**: `Request.WithoutSecretValues` → migration 0007 → store records, write calls,
   revision (with every existing call site passing `nil`; all tests green before going on).
2. **US1 Edit the kept file** (P1): service `made` + `DeployComposeIf` keeps the file + drop
   warning → `draftOf` with round-trip test → Configure Compose section and read-only definition
   → Deploy from Compose prefill → 409 on per-field save → lab step (first half).
3. **US2 Not kept yet** (P2): Configure note + empty editor + `draftFrom` overlay; per-field form
   stays editable.
4. **US3 History** (P2): `RedeployVersion` restores the file → history download route and link →
   lab step (rollback half).
5. **US4 Everything else + API** (P3): `editSecrets` and `Redeploy` carry the file → `getCompose`,
   `getDeploymentCompose`, `composeKept`, OpenAPI 0.12.0, parity → sentinel tests on every path
   (SC-003).
6. Docs: CLAUDE.md "Where we are", `docs/plan.md` (015 done).

US1 depends on the foundation; US2 and US3 depend only on US1's service change; US4's API reads
need only the foundation and can go in parallel with US1–US3.

## Complexity Tracking

None.
