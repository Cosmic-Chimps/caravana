# Implementation Plan: App Model and Nomad Job Translator

**Branch**: `001-app-model-translator` | **Date**: 2026-10-02 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from `specs/001-app-model-translator/spec.md`

## Summary

Build the pure step every deploy goes through: `nomadjob.Translate(app, deployment, settings)`
turns the `internal/app` model into a Nomad `*api.Job`, or fails with an `*app.FieldError` naming
the field. The model is small on purpose (image source only; no secrets, volumes or pinning).
Validation is split: model rules in `app.Validate`, Nomad limits and settings in `nomadjob`. The
whoami case is checked field for field against the lab's own `deploy/dev/whoami.json`, and
golden files hold the exact register body, which `lab.sh check <file>` can deploy. The
`scratchpad/` sketch is deleted at the end.

## Technical Context

**Language/Version**: Go 1.27 (`go.mod`), local toolchain go1.27.1

**Primary Dependencies**: `github.com/hashicorp/nomad/api` at the v2.0.7 commit
(`v0.0.0-20260917172403-9dcbdc5e64ec`, [research R1](research.md#r1-nomad-api-client-version)),
types only. Standard library for everything else (`regexp`, `encoding/json`, `reflect`, `flag`,
`slices`, `maps`).

**Storage**: N/A (pure code)

**Testing**: `testing` package: table-driven validation tests, golden JSON in
`internal/nomadjob/testdata/` with an `-update` flag, a field-for-field test against
`deploy/dev/whoami.json`, determinism tests. `go test -race` in CI.

**Target Platform**: linux/amd64 and linux/arm64, CGO off (also builds and tests on darwin/arm64)

**Project Type**: internal library packages inside the single `caravana` binary module

**Performance Goals**: none that matter: a translation is microseconds, once per deploy

**Constraints**: no I/O, no clock, no randomness; deterministic bytes; no new dependency besides
the Nomad API client; no `panic`

**Scale/Scope**: 2 packages, about 6 source files, 8 golden files, about 40 validation cases

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | How this plan complies | Status |
| --- | --- | --- |
| I. Small by Default | One new module dependency (`nomad/api`), mandated by VI; justified in the commit message. No runtime dependency. | ✅ |
| II. One Model, Many Inputs | `app.App` is the model; one App → one job, one Service → one group with one task. `Translate` is pure, with golden tests. `Source` is validated, never read by the translator. | ✅ |
| III. Safe, Reversible Deploys | Digest-only images (tag+digest rejected); deployment ID in meta forces a new version; `HealthCheck: "checks"` + `AutoRevert: true`; health check required for every service with ports. Env is passed as given (declarative; storage of earlier values is 003's concern). | ✅ |
| IV. Fail Loudly | Every rule returns a `FieldError` with a path. Duplicate env names are detectable because env is a list (R8). Extra digests fail. Warnings list exists (empty in Phase 1). No volumes in the model, so none can be dropped. | ✅ |
| V. API-First | No endpoint added, so `openapi.yaml` is unchanged. Field paths are camelCase so 002's request body and the error `field` line up ([contract](contracts/translate.md)). | ✅ |
| VI. Nomad-Native | Official `api` types, JSON only; `Provider: "nomad"`; bridge + dynamic ports with `To`; routing only via tags, router `<app>-<service>`. Nomad limits live in `nomadjob`, not `app` (R5). The golden is a plain Nomad job (export-ready). | ✅ |
| VII. Secure by Default | No secrets in this feature. Hostnames are strictly validated, so app input can't inject Traefik rule syntax. Nothing is logged. | ✅ |
| VIII. Plain, Tested Go | Table tests + goldens in `testdata/`; errors returned, never panics; no `context.Context` because there's no I/O; no interfaces; `new(expr)` instead of a helper (R2). All CI checks in the quickstart. | ✅ |
| Workflow | Phase 1 feature 001 from `docs/plan.md`, own branch, Spec Kit. Original code (the sketch is ours). | ✅ |

No violations, so Complexity Tracking is empty.

**Post-design re-check (after Phase 1)**: still passes. The design adds no interface, no
dependency beyond R1, and nothing outside Phase 1. One addition to note: `lab.sh check` gains an
optional file argument (R12), a small script change that the workflow allows without its own
feature.

## Project Structure

### Documentation (this feature)

```text
specs/001-app-model-translator/
├── plan.md               # this file
├── research.md           # Phase 0: decisions R1–R12
├── data-model.md         # Phase 1: types, rules, mapping to the Nomad job
├── quickstart.md         # Phase 1: how to validate, up to a lab deploy
├── contracts/
│   └── translate.md      # Phase 1: Go signatures, validation order, error catalogue
├── checklists/
│   └── requirements.md   # spec quality checklist
└── tasks.md              # Phase 2 (/speckit-tasks)
```

### Source Code (repository root)

```text
internal/app/
├── app.go               # package doc; App, Source, Service, EnvVar, Port, Domain, HealthCheck, Resources
├── deployment.go        # Deployment, Trigger
├── errors.go            # FieldError, Warning
├── validate.go          # Validate(App, Deployment) error; name/host/digest regexps
└── validate_test.go     # one table: every model rule, asserting Field

internal/nomadjob/
├── translate.go         # Settings, Result, Translate; Nomad limits; job, group, service, task builders
├── translate_test.go    # golden table, lab field-for-field, determinism, meta-only change, Nomad-limit and settings errors
└── testdata/
    ├── whoami.golden.json             # the lab case (deploy/dev/whoami.json equivalent)
    ├── minimal-web.golden.json        # replicas omitted, default resources, TCP check
    ├── multi-domain.golden.json       # 2 hosts, 1 router; check on a different port
    ├── worker.golden.json             # no ports: no service, no check; command, args, env
    ├── internal-service.golden.json   # port + check, no domains, no Traefik tags
    ├── letsencrypt.golden.json        # cert resolver set
    ├── custom-resources.golden.json   # own CPU/memory and check interval/timeout
    └── multi-service.golden.json      # web + worker in one job, order kept

deploy/dev/lab.sh        # `check` takes an optional job file (R12)
docs/go-guide.md         # ptr(v) helper line → new(expr)
scratchpad/              # deleted
```

**Structure Decision**: the two packages `docs/go-guide.md` already reserves. `app` has no
dependency on Nomad; `nomadjob` imports `app` and `nomad/api`. The test for `nomadjob` reads
`../../deploy/dev/whoami.json` directly (R3). Nothing in `cmd/caravana` changes: wiring starts
in 002/003.

## Implementation notes for /speckit-tasks

- **Order**: `app` types → `app.Validate` + its table test → `nomadjob` builders → whoami
  field-for-field test (story 1) → remaining goldens (story 3) → Nomad-limit and settings errors
  → lab check → delete `scratchpad/` → docs.
- **Story mapping**: US1 = whoami golden + lab field-for-field + determinism + lab deploy. US2 =
  the validation tables. US3 = the other goldens.
- **Go idioms to explain in the summary** (CLAUDE.md asks for these): `new(expr)` for pointer
  fields; `*int` as "optional" (C# `int?`); `error` values and `errors.As` instead of exceptions
  and `catch (FieldError e)`; table tests with `t.Run` (xUnit `[Theory]`); `testdata/` and the
  `-update` flag; `reflect.DeepEqual` following pointers; implicit interfaces (`*FieldError`
  satisfies `error` just by having `Error() string`); `regexp.MustCompile` in package-level
  `var`s (panics only at program start, on a constant pattern, which is the accepted exception
  to "no panic").
- **Commit message** for the dependency: "Add github.com/hashicorp/nomad/api (v2.0.7 commit):
  the official Nomad client required by constitution VI; types only in this feature."

## Complexity Tracking

No violations to justify.
