# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What we're building

Caravana: an open source, self-hosted PaaS (Heroku/Dokploy-style) on **HashiCorp Nomad**
instead of Docker Swarm or Kubernetes. Two first-class front doors: the HTMX web UI in this repo,
and a public HTTP API whose first client is a .NET Aspire integration (`PublishToCaravana`,
separate C# repo).

- Vision, principles and roadmap: @docs/vision.md
- **The rules every change must follow**: @.specify/memory/constitution.md
- Ordered plan: `docs/plan.md`. Aspire and Bella Baxter feature context: `docs/integrations.md`
- Target layout and Go conventions with C# comparisons: `docs/go-guide.md`

Rules live only in the constitution. Don't restate them here; amend it with `/speckit-constitution`.

## Where we are

Phase 0 (groundwork) is done: repo + CI, constitution, local Nomad lab. Feature 001 is
implemented on branch `001-app-model-translator`: the app model in `internal/app` and the pure
translator in `internal/nomadjob`, whose whoami output matches the hand-written
`deploy/dev/whoami.json` and deploys in the lab (`lab.sh check <job.json>`). The `scratchpad/`
sketch is gone. Feature 002 is implemented on branch `002-control-plane-skeleton` (stacked on
001): config from env and flags (`internal/config`), the HTTP API with `GET /v1/health` and the
shared JSON/error plumbing (`internal/api`), the SQLite store with embedded migrations
(`internal/store`), and `api/openapi.yaml`, kept in sync with the routes by a test. Feature 003
is implemented on branch `003-deploy-flow` (stacked on 002): `POST /v1/apps/{id}/deployments`
saves and deploys an app (empty body: redeploy the stored one), plus reads for the app and its
deployments. `internal/registry` pins tags to digests (Docker Hub and GHCR, anonymous),
`internal/deploy` registers the job and follows the Nomad rollout, `internal/service` holds the
flow (per-app ordering, newest deploy wins, resume after restart). Test fakes: `registrytest`
and `deploytest`. Deploys are blue/green (canaries hidden from Traefik until promoted). Feature
004 is implemented on branch `004-webhook-redeploy` (stacked on 003): per-app webhook tokens
(`cvwh_…`, shown once, stored as a SHA-256 hash), `POST /v1/webhooks/{id}` (token in `?token=` or
`Authorization: Bearer`; skips when no image changed), and an optional webhook-only listener
(`CARAVANA_WEBHOOK_LISTEN`) that serves nothing else. Next: feature 005 (real HTTPS and the
auto-revert proof on a VM).

Product features go through Spec Kit (`/speckit-specify` → `/speckit-plan` → `/speckit-tasks`
→ `/speckit-implement`), one feature per git branch. Nomad runs in a Lima Linux VM
(`deploy/dev/`), because Nomad bridge networking needs Linux. From the Mac, apps are at
`https://<name>.localhost:8443`.

## Current phase: 1 — Engine (single node)

Scope right now, nothing beyond it unless asked:
- Deploy a prebuilt Docker image as a Nomad job
- Webhook endpoint that triggers a redeploy
- Resolve image tag → digest on every deploy; each deploy = new Nomad job version
- Routing + HTTPS through Traefik (Nomad provider), single node
- The deploy and webhook endpoints are described in `api/openapi.yaml` from the first commit

Gate to finish Phase 1: `traefik/whoami` deployed via webhook, reachable over HTTPS, and a
failed deploy auto-reverts.

Out of scope for now: compose translation, git builds, multi-node, WireGuard, UI polish, auth/RBAC.

## Architecture

The core flow: API/UI handler → `internal/service` → `internal/deploy` (resolve the digest via
`internal/registry`, `nomadjob.Translate`, register, watch the Nomad deployment) →
`internal/store` records the result. `cmd/caravana` only wires dependencies together.

## Commands

```bash
go mod tidy
go build ./...
go test ./...
go test ./internal/nomadjob -run 'TestTranslate/whoami' -v   # one test or subtest
go vet ./...
go tool staticcheck ./...                                     # pinned in go.mod as a tool
gofmt -l .                                                    # must print nothing
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...          # proves the no-CGO cross-compile
go run ./cmd/caravana                                         # 127.0.0.1:7070, data in ./data
go run ./cmd/caravana -webhook-listen 127.0.0.1:7071          # plus the webhook-only listener
curl -s localhost:7070/v1/health
```

```bash
eval "$(deploy/dev/lab.sh env)"                               # Caravana reads NOMAD_ADDR / NOMAD_TOKEN
CARAVANA_LAB=1 go test ./cmd/caravana -run TestLab -v -timeout 20m   # opt-in end-to-end test (~12 min)
```

```bash
deploy/dev/lab.sh up               # start (or create) the local Nomad lab
deploy/dev/lab.sh check            # deploy whoami.json by hand and call it over HTTPS
eval "$(deploy/dev/lab.sh env)"    # NOMAD_ADDR + NOMAD_TOKEN for Caravana on the Mac
deploy/dev/lab.sh shell            # inside the VM: sudo lab-nomad <cmd> (management token)
```

CI (`.github/workflows/ci.yml`) runs all of these plus `go test -race` and `go mod tidy -diff`.
Run them before saying a change is done.

## Working with the maintainer

The maintainer comes from C#/.NET and is learning Go. When introducing a Go idiom that differs
from C# (interfaces satisfied implicitly, error returns instead of exceptions, goroutines and
channels, `new(expr)` for pointer fields), add a one-line explanation in your summary. Prefer
clear, plain Go over clever Go.
