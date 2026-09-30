# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What we're building

An open source, self-hosted PaaS (Heroku/Dokploy-style) that uses **HashiCorp Nomad** as the
orchestrator instead of Docker Swarm or Kubernetes. Tagline: *the PaaS that grows from one cheap
VM to twenty without ever touching Kubernetes.* Full vision, principles and roadmap:
@docs/vision.md

The platform has two front doors, and both are first-class:
- the HTMX web UI in this repo
- a public HTTP API, whose first external client is a .NET Aspire hosting integration
  (`PublishToCaravana`, C#, separate repo, modeled on
  https://github.com/Cosmic-Chimps/aspire-hosting-dokploy)

External tools always go through this API, never straight to Nomad. Jobs created behind the
control plane's back have no domains, secrets, history or rollback.

## Where we are

Day zero. The only code is a proof-of-concept sketch in `scratchpad/` (written before the repo
was set up). It does not build: three packages share one folder, and the imports use the old path
`github.com/you/nomadpaas`. Use it as reference, not as precedent: where it disagrees with the
rules below, the rules win. Feature 001 moves it into `internal/` and deletes it.

The ordered plan is in `docs/plan.md`. Work goes through Spec Kit (`/speckit-specify` →
`/speckit-plan` → `/speckit-tasks` → `/speckit-implement`), one feature per git branch. There is
no local Nomad yet. The lab is a Lima Linux VM set up in `deploy/dev/` (plan item 0.3), because
Nomad bridge networking needs Linux.

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

## Stack and hard constraints

- **Go 1.27+** (`go 1.27` in `go.mod`; CI reads the version from `go.mod`), one static binary. No CGO (must cross-compile to linux/amd64 and linux/arm64).
- HTTP: standard library `net/http` with Go 1.22 routing patterns. No web frameworks.
- UI: `html/template` + HTMX, assets baked in with `embed`. No SPA, no Node build step.
- Storage: SQLite via `modernc.org/sqlite` (pure Go).
- Nomad: official client `github.com/hashicorp/nomad/api` only. Submit jobs as JSON through the
  API; never generate HCL (except a future "export jobspec" feature).
- No Consul dependency. Use Nomad native service discovery (`Provider: "nomad"`).
- Target Nomad 2.x (2.0.7 at the time of writing) and Traefik v3.

## Architecture

Target layout and Go conventions (with C# comparisons): `docs/go-guide.md`. The core flow:
API/UI handler → `internal/service` → `internal/deploy` (resolve digest via `internal/registry`,
`nomadjob.Translate`, register, watch the Nomad deployment) → `internal/store` records the result.
`cmd/caravana` only wires dependencies together.

Design rules:
- Every input (image, compose, git) normalizes into `app.App`. Nothing downstream reads `Source`.
- One `App` = one Nomad job; one `Service` = one task group with one task.
- `nomadjob.Translate` stays a pure function: same input, same job. Keep it easy to unit test.
- Bridge networking with dynamic host ports; container ports via `To`.
- Traefik routing only through service tags; router name = `<app>-<service>`.
- Secrets live in Nomad Variables at `nomad/jobs/<app-id>`, rendered as env via a template.
- Update strategy always health-checked with `AutoRevert: true`.
- Never deploy a floating tag; always `repo@sha256:...`.
- Env is declarative: each deploy fully replaces an app's env and secrets. Never merge with
  previous values; keys the new deploy doesn't send are removed.
- Volumes are explicit. A compose or API volume becomes a declared host volume, or the deploy
  fails. Never silently drop a volume, or any other field, that we can't honor.
- Unsupported input fails loudly with an error naming the field; supported-but-changed input
  (e.g. `depends_on` with conditions) produces a warning in the deploy result.

## API rules

- API-first: the UI and the API call the same internal service layer. No feature exists only
  in the UI.
- `api/openapi.yaml` is the contract, updated in the same change as any endpoint. Clients (the
  Aspire integration) are generated from it, so breaking changes need a new version path.
- Accept `application/json` with or without parameters (`charset=UTF-8` must work).
- Deploys are idempotent upserts keyed by app ID: sending the same payload twice is a no-op
  apart from a new deployment record.
- Errors are structured JSON: `{"error": {"code", "message", "field"}}`, with a correct 4xx/5xx.
  Never answer a bad request with a redirect.
- Deploy responses return the deployment ID, Nomad job version and warnings, so clients can poll
  status and surface problems.
- Never log secret values; redact env values and tokens in request logging.

## Aspire support (Phase 2.5, after the compose translator)

The Aspire integration publishes via Aspire's Docker Compose output, pushes images, and POSTs
compose + domains + secrets to our API. This repo's job is to make that path work well:
- The compose translator must handle Aspire-generated compose files (keep test fixtures of real
  Aspire output under `internal/compose/testdata/aspire/`).
- Generate Aspire-style service discovery env (`services__<name>__<endpoint>__0`) from Nomad
  native service discovery via templates, so Aspire apps find each other with no config.
- Optionally expose the Aspire dashboard as a normal app; its OTLP ports stay internal.

Gate for Phase 2.5: Bella Baxter itself deploys to Caravana via `PublishToCaravana`, with its
database on a host volume, working service discovery and HTTPS.

## Bella Baxter integration (dogfooding)

Bella Baxter (https://docs.bella-baxter.io/, same maintainer) is a secret management gateway
with a Go SDK, keyless OIDC auth, webhooks, an SSH CA and PKI. We use it in three ways, and it
is always **optional**: Caravana must work fully with Nomad Variables alone.

Secrets (Phase 2):
- Secrets sit behind a `secrets.Provider` interface in `internal/secrets`. Nomad Variables is
  the built-in default; Bella Baxter is the first external provider, using its Go SDK.
- Prefer keyless auth: tasks authenticate to Bella with their Nomad workload identity JWT
  (validated against Nomad's JWKS endpoint), so no static Bella API key is stored anywhere.
- A Bella webhook on secret change calls our deploy webhook; the app is redeployed with its
  full, current env (declarative env rule above) through the normal health-checked rollout.

Cluster hardening (Phase 3):
- Node bootstrap uses short-lived SSH certificates from Bella's SSH CA when configured, instead
  of the control plane storing private keys.
- Nomad cluster TLS certificates can be issued from Bella PKI. Fallback: certs generated by the
  control plane at install time.

Avoid the bootstrap cycle:
- The control plane's own secrets and Bella's own secrets live in Nomad Variables, never in
  Bella. A cold start of the whole cluster must never wait on Bella being up.
- If Bella is unreachable, running apps keep their last rendered env; only new deploys fail,
  with a clear error.

## Commands

```bash
go mod tidy
go build ./...
go test ./...
go test ./internal/nomadjob -run 'TestTranslate/whoami' -v   # one test or subtest
go vet ./...
gofmt -l .                                                    # must print nothing
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...          # proves the no-CGO cross-compile
```

Run `go build ./...` and `go test ./...` before saying a change is done.

## Code conventions

- Wrap errors with context: `fmt.Errorf("register job %q: %w", id, err)`. No panics in library code.
- Pass `context.Context` as the first argument to anything doing I/O.
- Table-driven tests with the standard `testing` package; golden JSON files for translator output
  in the package's own `testdata/` folder (e.g. `internal/nomadjob/testdata/`).
- Small packages under `internal/`, exported API kept minimal.
- `gofmt` everything; keep dependencies few and justify any new one in the PR/commit message.

## Working with the maintainer

The maintainer comes from C#/.NET and is learning Go. When introducing a Go idiom that differs
from C# (interfaces satisfied implicitly, error returns instead of exceptions, goroutines and
channels, pointer helpers like `ptr[T]`), add a one-line explanation in your summary. Prefer
clear, plain Go over clever Go.

## Don'ts

- Don't add Kubernetes, Swarm, or Consul code paths.
- Don't copy code, UI, docs text, names or branding from Dokploy, Coolify or other PaaS projects.
  Feature ideas are fine; implementations are ours.
- Don't widen scope beyond the current phase without asking.