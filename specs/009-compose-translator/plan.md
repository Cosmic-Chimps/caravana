# Implementation Plan: Compose translator

**Branch**: `009-compose-translator` | **Date**: 2026-10-07 | **Spec**: [compose-translator.spec.md](./compose-translator.spec.md)

**Scale note**: about 30 files in seven packages (`app`, `compose` (new), `nomadjob`, `deploy`,
`service`, `api`, `ui`) plus the policy, the gate and docs. Watch two seams: the model gains
volumes (every package below `app` sees them), and every multi-service app gains a sidecar task
and a second registration, which changes existing golden jobs on purpose.

## Summary

A new pure package `internal/compose` turns a Compose file plus a JSON overlay into an
`app.App` and warnings, using `compose-go` v2 (loaded from bytes, no disk, no OS environment,
variables only from the file's defaults). An allowlist of Compose attribute paths decides what
is honored. Everything outside it is refused naming its path, and a short list is honored with
a warning. The API gains `deployCompose` and `previewCompose`. The service deploys the result
like any app, with the translator's warnings added to the deployment record.

Two capabilities are added to the model and the translator, and so to every input, not just
Compose:

- **Services by name.** In an app with more than one service, every task group gets a small
  prestart sidecar. It keeps the group's shared `/etc/hosts` pointing each service name at that
  service's live (non-canary) allocation IP, and each service with ports gets a second
  registration in `alloc` address mode. Proven in the lab, including through a blue/green
  redeploy with no restarts (research R3).
- **Named volumes.** They become Nomad dynamic host volumes (`mkdir` plugin), created by
  `internal/deploy` before the job is registered and deleted with the app. A group with a volume
  runs one copy and deploys stop-then-start (no canary), because a single-writer volume can't
  hold two allocations (R5).

The new module is `github.com/compose-spec/compose-go/v2` (Apache-2.0, pure Go). It is
justified in the commit message (constitution I).

## Project Structure

```text
internal/app/
├── app.go              # SourceCompose, Service.Volumes, Volume{Name, Path, ReadOnly}
└── validate.go         # volume rules: name, path, one service per volume, one copy
internal/compose/       # NEW, pure: Compose file + overlay -> app.App + warnings
├── compose.go          # Request, Overlay, Translate, load options, variable lookup
├── fields.go           # the allowlist and the warned attributes, with their messages
├── service.go          # one ServiceConfig -> app.Service
├── compose_test.go     # table tests: honored, warned, refused
└── testdata/           # *.compose.yaml fixtures and *.golden.json app models
internal/nomadjob/
├── translate.go        # names sidecar, internal registration, volumes, no canary with a volume
├── translate_test.go
└── testdata/           # multi-service and new volume goldens
internal/deploy/
├── volumes.go          # EnsureVolumes, DeleteVolumes (dynamic host volumes)
├── volumes_test.go
├── register.go         # Register calls EnsureVolumes first
└── deploytest/nomad.go # fake /v1/volume/host/* and /v1/volumes?type=host
internal/service/
├── compose.go          # DeployCompose, PreviewCompose
├── compose_test.go
├── deploy.go           # deploy takes extra warnings; removed-volume warning
└── apps.go             # DeleteApp deletes volumes
internal/api/
├── compose.go          # POST /v1/apps/{id}/compose, POST /v1/apps/{id}/compose/preview
└── compose_test.go
internal/config/config.go          # CARAVANA_NAMES_IMAGE (default: busybox pinned by digest)
internal/ui/configure.go, app.go   # Volumes row, source "compose", delete names volumes
api/openapi.yaml                   # 0.7.0: ComposeRequest, ComposePreview, Volume, SourceKind compose
deploy/vm/caravana-policy.hcl      # host-volume-create/-delete, host_volume "*_*" write
cmd/caravana/gate_test.go          # gate step compose-app
CLAUDE.md, docs/plan.md
```

**Structure Decision**: Compose parsing lives in its own package, so `compose-go` is imported in
exactly one place, and the rest of the code sees only `app.App`. Volumes and service names are
model and translator features, so a JSON-defined app gets them too (constitution II: one model).
Nomad volume API calls stay in `internal/deploy` (VI).

## Constitution Check

| Principle | Assessment |
| --- | --- |
| I. Small by Default | PASS with a justified module: `compose-go` (pure Go, Apache-2.0, ~10 transitive deps, no CGO, cross-compile verified). No runtime dependency: dynamic host volumes and the sidecar use what Nomad and Docker already have. |
| II. One Model, Many Inputs | PASS for normalization: Compose becomes `app.App`, nothing after reads the file, `Translate` stays pure and golden-tested. **Violation**: "one Service is one task group with one task". See Complexity Tracking. |
| III. Safe, Reversible Deploys | PASS. Images still pinned by digest, the sidecar image too (a pinned digest setting). Groups with a volume trade canaries for stop-then-start, still health-checked with `AutoRevert`. Every ported service gets a check (a TCP default). |
| IV. Fail Loudly | PASS, and it is the core of the feature: an allowlist refuses everything not listed, naming its path; changed meanings are warnings; volumes are explicit or the deploy fails. |
| V. API-First | PASS. Two new operations in `api/openapi.yaml` in the same commit; JSON in, JSON errors out; the console reads Compose apps through the same service. |
| VI. Nomad-Native | PASS. Native service discovery (`provider = "nomad"`, `address_mode = "alloc"`), no Consul. Bridge mode with dynamic host ports stays for routing. Volumes are Nomad host volumes, so an exported job runs without Caravana. Nomad calls stay in `deploy`/`nomadjob`. |
| VII. Secure by Default | PASS. Secret values only through the overlay (feature 008), never in a file; no bind mounts of host paths; `privileged` and capabilities are refused; the scoped policy gains only host-volume create/delete and mounting `*_*` volumes. |
| VIII. Plain, Tested Go | PASS. Table tests over fixture files, golden app models and jobs, the fake Nomad gains volume endpoints, gate and lab steps. |

## Complexity Tracking

| Violation | Why needed | Simpler alternative rejected because |
| --- | --- | --- |
| A second task (`names`, a prestart sidecar) in each group of a multi-service app | Compose services reach each other by name, and Nomad without Consul has no DNS for that. The group's `/etc/hosts` is the only place a name can point at a moving allocation, and only a task can rewrite it when registrations change (lab, R3). | `extra_hosts` is written once per allocation and goes stale on the first redeploy (lab-verified). A change script in the app's own task needs `/bin/sh` in every image. Env-only discovery would make users rewrite their files. Consul is ruled out by VI. |
