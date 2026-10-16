# Implementation Plan: Private registries

**Branch**: `017-private-registries` | **Date**: 2026-10-16 | **Spec**: [private-registries.spec.md](./private-registries.spec.md)

**Input**: Feature specification from `specs/017-private-registries/private-registries.spec.md`

**Scale note**: about 30 files in seven packages: one migration, no new Nomad capability, one gate
step. The pull mechanism was proven in the lab first (research P1–P7). Docker's `auth` block
interpolates credentials from a template of the task's own Nomad Variable, so the job holds only
placeholders. The one trade-off, accepted and documented, is that the credentials reach the app's
own container environment. The risks:

- **A secret leaking** into a page, log, job, export or record. A sentinel test covers every path.
- **The registry package's auth changes breaking public pulls.** The existing registry tests stay
  as they are, and the gate deploys public images as before.

## Summary

Owners store credentials per registry host in Settings or through the API (`/v1/registries`). The
secret is write-only and kept in Caravana's Nomad Variables; the facts the console shows are in
SQLite.

**Digest lookup** sends the credentials: Basic on the token request, or Basic directly. Refused
credentials fail the deploy with 422 `registry_auth_failed`.

**The pull**: the node pulls private images with a Docker `auth` block interpolated from a template
of the task's own variable. Caravana writes that variable before each register, and rewrites every
copy when credentials change or are removed.

**Any registry host** that speaks the Docker Registry API v2 over HTTPS is accepted, plus loopback
over HTTP (as Docker allows). A **Test** checks credentials without deploying. API 0.14.0.

## Technical Context

**Language/Version**: Go 1.27, no CGO

**Primary Dependencies**: standard library (`net/http`, `encoding/base64`);
`github.com/hashicorp/nomad/api`. No new module.

**Storage**: SQLite (`0009_registries.sql`, metadata only) and Nomad Variables (secrets and their
per-task copies).

**Testing**:
- `internal/registry` against an `httptest` registry with both Bearer and Basic challenges;
- golden jobs;
- the `deploytest` fakes;
- a sentinel secret on every path;
- the gate step `private-registry` against a real `registry:2` with htpasswd on the lab node.

**Target Platform**: single-node Linux host, amd64 or arm64.

**Project Type**: web service (control plane, API, console).

**Performance Goals**: one extra variable read per image on each deploy. The console page reads
SQLite only.

**Constraints**:
- the secret never appears in a page, API answer, log, job, export or record;
- no app restarts when credentials change;
- HTTPS only, except loopback.

**Scale/Scope**: a handful of registries per installation.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Assessment |
| --- | --- |
| I. Small by Default | PASS. No new module or service; one table. |
| II. One Model, Many Inputs | PASS. Images from the API, the console and Compose all go through `registry.Parse` and `Resolve`. `Translate` stays pure: whether a service's registry has credentials is an input to it, not a lookup. |
| III. Safe, Reversible Deploys | PASS. Every image is still pinned by digest; credentials are checked before register, so refused credentials register nothing; changing credentials restarts nothing. |
| IV. Fail Loudly | PASS. Refused credentials, missing or private images, a non-HTTPS host and a non-registry host each fail with a message naming the image and the registry. |
| V. API-First | PASS. Four `/v1/registries` operations and `registry_auth_failed` are in `api/openapi.yaml` 0.14.0 in the same commits; the parity table maps the console routes. |
| VI. Nomad-Native | PASS. Official client; credentials reach the node only through Nomad Variables and a template; the export names the variable. Nomad code stays in `nomadjob` and `deploy`. |
| VII. Secure by Default | PASS, with one documented trade-off (research R1). Secrets live in Nomad Variables and are write-only everywhere Caravana shows data. They do reach the private app's own container env, which the console says and which asks for a read-only token; a node-level helper is a Phase 3 backlog item. |
| VIII. Plain, Tested Go | PASS. Table tests for parsing, endpoints and challenges; pure job shapes with goldens; the gate on real Nomad. |

No violations. **Re-checked after design**: no change.

## Project Structure

### Documentation (this feature)

```text
specs/017-private-registries/
├── private-registries.spec.md
├── plan.md
├── research.md          # P1–P7 lab findings, R1–R8 decisions
├── data-model.md
├── quickstart.md
├── contracts/{api.md,console.md}
├── checklists/requirements.md
└── tasks.md             # /speckit-tasks
```

### Source Code

```text
internal/registry/reference.go, client.go (+ tests)     # any host, Endpoint, Credentials, Basic, ErrUnauthorized, Test
internal/store/migrations/0009_registries.sql
internal/store/registries.go (+ tests)
internal/nomadjob/translate.go, backup.go, hcl.go (+ goldens)  # auth block + template for private services
internal/deploy/registries.go (+ tests)                 # secret variables, per-task copies, sync, test; ErrRegistryAuth
internal/deploy/deploy.go, register.go, backups.go      # Resolve/CheckDigests use credentials; ensure copies before register
internal/deploy/deploytest/                             # fake registry credentials if needed
internal/service/registries.go (+ tests)
internal/api/registries.go, errors.go, api.go (+ tests), parity_test.go
api/openapi.yaml                                         # 0.13.0 → 0.14.0
internal/ui/settings.go, templates/settings.html (+ tests), view.go (fix action)
cmd/caravana/gate_test.go                                # step private-registry
CHANGELOG.md, CLAUDE.md, docs/plan.md
```

**Structure Decision**:
- `registry` only speaks HTTP;
- `deploy` owns every Nomad Variable involved;
- `nomadjob` turns "this service's registry has credentials" into the job shape, purely;
- `service` validates and orders save, then sync.

## Delivery order

1. **Foundation**:
   - `registry` accepts any host, with `Endpoint` and loopback HTTP;
   - Basic and credentialed Bearer, `ErrUnauthorized`, `Test`;
   - migration and store;
   - the deploy secret variable and the `Credentials` hook.
2. **US2, manage credentials (P1)**: service, API, console Settings section. It is useful alone:
   save, test, remove.
3. **US1, deploy private images (P1)**: job shape (auth and template), per-task copies before
   register, sync on change, backup and restore jobs, `registry_auth_failed`, the fix action, and
   the gate step.
4. **US3, other registries (P2)**: mostly done in the foundation; tests for hosts with ports, a
   non-HTTPS host and a non-registry answer, and the console hint.
5. **Docs**: CHANGELOG, CLAUDE.md, docs/plan.md (renumbering, backlog).

US2 goes before US1 because US1's tests and gate need saved credentials.

## Complexity Tracking

None.
