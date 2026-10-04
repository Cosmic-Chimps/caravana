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
(`CARAVANA_WEBHOOK_LISTEN`) that serves nothing else. Feature 005 is implemented on branch
`005-vm-https-proof` (stacked on 004): `deploy/vm/host.sh` turns a VM you own into a public
Caravana host over SSH (Let's Encrypt via Traefik, only 22/80/443 open, Nomad and the API on
loopback, app ports on the private `caravana0` interface, Caravana under systemd with a scoped
Nomad token). The gate checks live in `runGate` (`cmd/caravana/gate_test.go`): `TestLab` runs
them in the lab, `TestGate` (via `host.sh prove`) runs them plus the public-only checks against
the VM. The Phase 1 gate passed on 2026-10-04 (`host.sh prove` on an EC2 instance). Feature 006 is
implemented on branch `006-minimal-auth` (stacked on 005): one admin, created on the host with
`caravana admin init` (the CLI opens the SQLite store beside a running server), and the API closed:
every `/v1` route but health and the webhook call needs an API token (`cvat_…`, named, optional
expiry, shown once, SHA-256 stored) or a signed-in session (server-side, 12 h idle / 7 d, cookie
`caravana_session`, `__Host-` and `Secure` over HTTPS), with one uniform 401 and
`http.CrossOriginProtection` (403 `cross_origin`) on cookie requests. Failed sign-ins are limited
per address (5 per 15 minutes, IPv6 per /64, stored, `CARAVANA_TRUSTED_PROXIES` says whose
`X-Forwarded-*` to believe). Auth is `service.Auth` (PBKDF2 passwords, no secret key, nothing in
Nomad Variables); `internal/httpx` has the client address, cookie and request-log helpers;
`internal/ui` serves `/login`, `/account` (password, API tokens) and `/static` on the main
listener, never on the webhook-only one. The stylesheet is generated, not hand-written:
`tools/ui-css.sh build|check` runs the pinned Tailwind v4 standalone CLI with daisyUI 5 over
`internal/ui/style/` and the templates into the committed `internal/ui/static/app.css` (CI runs
`check`); styles follow `DESIGN.md`. The gate has a first step `api-needs-token`; `host.sh admin`
runs the CLI on the VM and `prove` makes and revokes its own token. For local work:
`go run ./cmd/caravana`, then `go run ./cmd/caravana admin init --email you@example.com`, then
sign in at `http://127.0.0.1:7070/login`. Branches 001–006 are stacked and not yet merged into
`main`. Next: 007.

Product features go through Spec Kit (`/speckit-specify` → `/speckit-plan` → `/speckit-tasks`
→ `/speckit-implement`), one feature per git branch. Nomad runs in a Lima Linux VM
(`deploy/dev/`), because Nomad bridge networking needs Linux. From the Mac, apps are at
`https://<name>.localhost:8443`.

## Current phase: 2 — Compose + UI (single node)

Phase 1 (Engine) is done; its gate passed on 2026-10-04. Scope right now, in the order of
`docs/plan.md`, nothing beyond it unless asked:
- 006 Minimal auth: one admin login for the UI, API tokens for clients (before anything is public)
  (done, branch `006-minimal-auth`)
- 007 HTMX UI v1: app list and detail, deploy button, deployment history with live status, logs
  from Nomad allocations. It calls only `internal/service`. Open decision: SSE vs. HTMX polling.
  The maintainer has a visual design made in Claude Design to share when 007 starts.
- 008 Secrets: `secrets.Provider`, Nomad Variables, declarative replace on every deploy
- 009 Compose translator (`compose-go`): loud errors, warnings for changed meaning, explicit
  host volumes
- 010 Databases + backups: pinned to a node with a host volume; backups as periodic batch jobs to
  S3-compatible storage
- 011 Rollback + export: redeploy any past version; export any app as a plain Nomad jobspec
- 012 One-command install (`deploy/vm/` is its rehearsal)

Gate to finish Phase 2: a real multi-service compose app runs, with a database on a host volume
and a backup that restores.

Out of scope for now: git builds, multi-node, WireGuard, teams and roles, Aspire/Bella Baxter
(Phase 2.5), GPUs and binaries.

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
go run ./cmd/caravana admin init --email you@example.com      # the one admin (asks for a password); works while the server runs
go run ./cmd/caravana admin status | reset-password
TOKEN="$(go run ./cmd/caravana admin create-token --name local)"   # an API token, printed once
curl -s -H "Authorization: Bearer $TOKEN" localhost:7070/v1/tokens
tools/ui-css.sh build              # regenerate internal/ui/static/app.css after changing internal/ui/style or templates
tools/ui-css.sh check              # fails if the committed app.css is out of date (CI runs it)
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

```bash
deploy/vm/host.sh up               # set up or update the public VM (settings in deploy/vm/host.env)
deploy/vm/host.sh admin init --email you@example.com   # once per host: the admin the gate and the pages need
deploy/vm/host.sh prove            # the Phase 1 gate against the VM (~12 min), ends GATE PASSED/FAILED
deploy/vm/host.sh tunnel           # private access to the VM's API, Nomad and Traefik dashboard
eval "$(deploy/vm/host.sh env)"    # NOMAD_ADDR, NOMAD_TOKEN, CARAVANA_URL through the tunnel
shellcheck deploy/dev/*.sh deploy/vm/*.sh tools/*.sh
```

CI (`.github/workflows/ci.yml`) runs all of these plus `go test -race` and `go mod tidy -diff`.
Run them before saying a change is done.

## Working with the maintainer

The maintainer comes from C#/.NET and is learning Go. When introducing a Go idiom that differs
from C# (interfaces satisfied implicitly, error returns instead of exceptions, goroutines and
channels, `new(expr)` for pointer fields), add a one-line explanation in your summary. Prefer
clear, plain Go over clever Go.
