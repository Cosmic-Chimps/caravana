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

Phase 0 (groundwork) is done: repo + CI, constitution, local Nomad lab. Next: feature 001
(app model + translator), whose golden file target is the hand-written `deploy/dev/whoami.json`.

The only product code is a proof-of-concept sketch in `scratchpad/`. It's a separate Go module,
so the root build ignores it. It doesn't build on its own: three packages share one folder, and
the imports use the old path `github.com/you/nomadpaas`. Use it as reference, not as precedent;
where it disagrees with the constitution, the constitution wins. Feature 001 moves it into
`internal/` and deletes it.

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
channels, pointer helpers like `ptr[T]`), add a one-line explanation in your summary. Prefer
clear, plain Go over clever Go.
