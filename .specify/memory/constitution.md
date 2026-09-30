<!--
Sync Impact Report
- Version change: template (unversioned) → 1.0.0
- Principles defined (all new):
  I. Small by Default
  II. One Model, Many Inputs
  III. Safe, Reversible Deploys
  IV. Fail Loudly, Never Drop Silently
  V. API-First
  VI. Nomad-Native, No Lock-In
  VII. Secure by Default
  VIII. Plain, Tested Go
- Added sections: Technology Constraints, Development Workflow, Governance
- Removed sections: none
- Templates:
  ✅ .specify/templates/tasks-template.md: tests are required (Principle VIII), Go path conventions
  ✅ .specify/templates/plan-template.md: no change; its Constitution Check reads this file
  ✅ .specify/templates/spec-template.md: no change; already requires testable stories
  ✅ CLAUDE.md: rules moved here; CLAUDE.md now links to this file
  ✅ docs/integrations.md: Aspire and Bella Baxter feature context moved out of CLAUDE.md
- Deferred TODOs: none
-->

# Caravana Constitution

Caravana is an open source, self-hosted PaaS on HashiCorp Nomad: the PaaS that grows from one
cheap VM to twenty without ever touching Kubernetes. The product vision is in `docs/vision.md`;
this document holds the rules every feature must follow.

## Core Principles

### I. Small by Default

- Caravana MUST ship as one static Go binary with SQLite storage, installable with one command.
- The control plane plus a small app MUST fit on a 2 GB VM, amd64 or arm64.
- A new runtime dependency (a service Caravana needs running) MUST NOT be added. A new Go module
  dependency MUST be justified in the commit message.

Rationale: the target user runs budget VMs; every extra moving part is a cost they pay.

### II. One Model, Many Inputs

- Every input (image, compose, git) MUST normalize into `app.App`. Nothing after normalization
  reads `Source`.
- One `App` is one Nomad job; one `Service` is one task group with one task.
- `nomadjob.Translate` MUST stay a pure function (no I/O, same input gives the same job) and is
  covered by golden-file tests.

Rationale: one model keeps translation, routing and deploys testable and the same for every source.

### III. Safe, Reversible Deploys

- A deploy MUST NOT use a floating tag. Tags resolve to `repo@sha256:...` on every deploy, and
  every deploy is a new Nomad job version.
- The update strategy MUST be health-checked with `AutoRevert: true`. Every service that
  receives traffic MUST have a health check.
- Env and secrets are declarative: each deploy fully replaces them. Values are never merged with
  earlier ones; keys the new deploy doesn't send are removed.
- Deploys are idempotent upserts keyed by app ID: the same payload twice changes nothing except
  a new deployment record.

Rationale: users must be able to deploy without fear and roll back with one action.

### IV. Fail Loudly, Never Drop Silently

- Input Caravana can't honor MUST fail with an error naming the field. No field (volume, port,
  domain, anything) is ever silently dropped.
- Input that is supported but changed in meaning (e.g. `depends_on` with conditions) MUST produce
  a warning in the deploy result.
- Volumes are explicit: each becomes a declared Nomad host volume, or the deploy fails.

Rationale: a deploy that "worked" but lost a volume is worse than one that failed.

### V. API-First

- The web UI and the public API MUST call the same `internal/service` layer. No feature exists
  only in the UI.
- `api/openapi.yaml` is the contract and MUST change in the same commit as any endpoint.
  Clients (the .NET Aspire integration) are generated from it, so breaking changes need a new
  version path.
- External tools go through the API, never straight to Nomad.
- JSON bodies MUST be accepted with or without media-type parameters (`charset=UTF-8`).
- Errors are JSON `{"error": {"code", "message", "field"}}` with the correct 4xx/5xx status.
  A bad request never gets a redirect.
- Deploy responses return the deployment ID, the Nomad job version and warnings.

Rationale: the API has real clients from day one; the UI is one of them, not a special case.

### VI. Nomad-Native, No Lock-In

- Nomad access MUST go through the official `github.com/hashicorp/nomad/api` client, submitting
  jobs as JSON. HCL is generated only by the "export jobspec" feature.
- No Kubernetes, Docker Swarm or Consul code paths. Service discovery uses Nomad's native
  provider (`Provider: "nomad"`).
- Networking is bridge mode with dynamic host ports (container ports via `To`). Traefik routing
  is configured only through service tags, with router name `<app>-<service>`.
- Any app MUST be exportable as a plain Nomad job, and uninstalling Caravana MUST leave running
  workloads untouched.
- Nomad-specific code stays inside the packages that talk to Nomad (`nomadjob`, `deploy`), so the
  orchestrator could be replaced later (Nomad's BSL terms are a business risk).

Rationale: Nomad is the product's reason to exist; users must never be trapped by Caravana itself.

### VII. Secure by Default

- Secrets live in Nomad Variables at `nomad/jobs/<app-id>` and are rendered as env by a template.
  External secret providers (Bella Baxter) are optional; Caravana MUST work fully without them.
- Secret values and tokens MUST NEVER be logged. Request logging redacts env values and tokens.
- The control plane's own secrets, and those of any secret provider, live in Nomad Variables. A
  cold start of the cluster MUST NOT wait on an external secret provider being up.
- If an external secret provider is unreachable, running apps keep their last rendered env; only
  new deploys fail, with a clear error.
- Installs enable Nomad ACLs from the first install; multi-node clusters add Nomad TLS and an
  encrypted network between nodes.

Rationale: users who never read a security guide must still end up with a safe setup.

### VIII. Plain, Tested Go

- `go build`, `go vet`, `staticcheck`, `go test -race`, `gofmt` and the CGO-free cross-compile
  (linux/amd64, linux/arm64) MUST pass before a change is done. CI enforces them.
- Every feature ships with tests: table-driven tests with the standard `testing` package, and
  golden JSON files for translator output in the package's own `testdata/` folder.
- Errors are returned and wrapped with context (`fmt.Errorf("register job %q: %w", id, err)`).
  Library code MUST NOT panic.
- `context.Context` is the first argument of anything doing I/O.
- Prefer clear, plain Go over clever Go. Interfaces are added only when a second implementation
  or a test needs one. Packages stay small under `internal/`.

Rationale: the maintainer is learning Go; code must be easy to read, change and trust.

## Technology Constraints

- Go 1.27+ (`go` line in `go.mod`), no CGO.
- HTTP: standard library `net/http` with its routing patterns. No web frameworks.
- UI: `html/template` + HTMX, assets embedded with `embed`. No SPA, no Node build step.
- Storage: SQLite via `modernc.org/sqlite` (pure Go).
- Orchestrator: Nomad 2.x. Router: Traefik v3 with its Nomad provider.
- Dev tools (e.g. `staticcheck`) are pinned with the `tool` directive in `go.mod`.

## Development Workflow

- Work follows the phases in `docs/vision.md` and the ordered plan in `docs/plan.md`. Features
  outside the current phase MUST NOT be built without the maintainer's approval.
- Product features go through Spec Kit (specify → plan → tasks → implement), one feature per git
  branch. Small infrastructure tasks (scripts, CI) may skip it.
- Every plan's Constitution Check lists each principle it touches; a violation needs an entry
  in the plan's Complexity Tracking table, or the plan changes.
- Originality: code, UI, docs text, names and branding MUST NOT be copied from Dokploy, Coolify
  or other PaaS projects. Feature ideas are fine; implementations are ours.

## Governance

- This constitution overrides other project guidance. `CLAUDE.md` holds working notes and
  commands, and links here instead of repeating rules.
- Amendments are made through `/speckit-constitution` in their own commit, with the Sync Impact
  Report updated and dependent templates brought in line in the same change.
- Versioning follows semver: MAJOR removes or redefines a principle; MINOR adds a principle or
  section, or materially expands one; PATCH clarifies wording.
- Reviews check compliance with this document; the plan's Constitution Check is the first gate.

**Version**: 1.0.0 | **Ratified**: 2026-09-30 | **Last Amended**: 2026-09-30
