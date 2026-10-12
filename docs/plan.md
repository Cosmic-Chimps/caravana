# Plan: from nothing to the vision

How we get from an empty repo to the roadmap in [vision.md](vision.md). Each numbered item
is a **Spec Kit feature**: `/speckit-specify` → `/speckit-plan` → `/speckit-tasks` →
`/speckit-implement`, one git branch per feature. The details go in each feature's spec. This file
only sets the order and the reason for it.

Versions checked 2026-09-30: Go 1.27.1, Nomad 2.0.7, Traefik v3.7.

## Phase 0: Groundwork (no product code)

| # | What | Done when |
| --- | --- | --- |
| 0.1 | Repo basics: `go.mod` (`module github.com/cosmic-chimps/caravana`, `go 1.27`), empty `cmd/caravana`, `.gitignore`, `LICENSE` (AGPL-3.0), GitHub Actions CI, first commit | CI is green: build, vet, test, `gofmt -l`, `CGO_ENABLED=0 GOOS=linux GOARCH=arm64` build |
| 0.2 | Constitution: `/speckit-constitution`. It **owns the rules**: vision principles, hard constraints, design rules, API rules. Then trim CLAUDE.md to state, commands and working notes, with a link to the constitution. | No placeholders left; no rule written in two places |
| 0.3 | Local Nomad lab (see below and `deploy/dev/README.md`). Plain scripts, not a Spec Kit feature. | Done 2026-09-30: `deploy/dev/lab.sh up` brings it up; `lab.sh check` deploys whoami by hand and it answers through Traefik at `https://whoami.localhost:8443` |

`scratchpad/` has its own `go.mod`, so it's a separate module and root `go build ./...` ignores
it until feature 001 deletes it.

**Why a Linux VM for the lab.** Nomad's `bridge` networking needs Linux network namespaces and
CNI plugins, and Nomad on macOS can't use Docker Desktop's VM for that. So the lab is a small
arm64 Linux VM on the Mac (Lima, native arm64; `lima-additional-guestagents` isn't needed). It
contains:

- Docker Engine inside the VM (Docker Desktop on the Mac isn't used), CNI plugins, `br_netfilter`.
- Nomad 2.0.7 as a **single-node server + client under systemd**, with a persistent data dir and
  **ACLs on**. It's close to production and a rehearsal for the installer (012). Caravana uses
  `NOMAD_TOKEN`; Traefik gets its own read-only token.
- Traefik v3 as a Nomad system job with the Nomad provider. It authenticates with a workload
  identity token scoped by an ACL policy, so there's no static token. It listens on 80/443 in
  the VM, which the Mac sees as 8080/8443: macOS only lets root bind low ports on `127.0.0.1`.
  Self-signed cert locally (`curl -k`). Real Let's Encrypt needs a public VM and a domain,
  which is feature 005.

Lima forwards the VM's ports to `localhost`, so the Caravana binary runs on the Mac against
`NOMAD_ADDR=http://127.0.0.1:4646` (`eval "$(deploy/dev/lab.sh env)"`). `lab.sh delete` plus
`lab.sh up` rebuilds the lab from scratch in about two minutes.

Learned while building the lab, for the installer (012) and later features:
- ARM VMs often report no CPU clock speed. Nomad then sees 0 MHz and places nothing, so the
  installer must detect `cpu.totalcompute = 0` and set `client.cpu_total_compute`.
- Generate the ACL bootstrap token first and pass it to `nomad acl bootstrap <file>`, so it's
  saved before Nomad accepts it.
- Nomad 2.0.1+ warns about services without `shutdown_delay`. The translator sets one (5s) so
  Traefik drops a route before its container stops.
- Locally, routers use `tls=true` with Traefik's default cert. With Let's Encrypt they add a
  `certresolver`. The translator needs this as an option, not a hard-coded value.

The hand-written `whoami` job from the 0.3 gate becomes the golden file for feature 001, so the
translator is tested against a job we know really runs. 0.3 and 001 can run in parallel.

## Phase 1: Engine (single node)

| # | Feature | Notes |
| --- | --- | --- |
| 001 | **App model + translator** | Move `scratchpad/` into `internal/app` and `internal/nomadjob`, then delete `scratchpad/`. Pure code, table tests and golden JSON. Fix the sketch's gaps: health check required, every domain/port routed or rejected, HTTPS by default, no floating tags. **Best first feature for learning Go**: no I/O, no infra. |
| 002 | **Control plane skeleton + API contract** | `cmd/caravana` (config from env/flags), `api/openapi.yaml`, `net/http` server, structured JSON errors, `charset` handling, log redaction, SQLite store with embedded migrations (`apps`, `deployments`). |
| 003 | **Deploy flow** | `POST` deploy (idempotent upsert) → resolve tag → digest from the registry (Docker Hub + GHCR, anonymous and token) → translate → register → watch the Nomad deployment → store its status and job version. Plain env vars only; secrets come in Phase 2. |
| 004 | **Webhook redeploy** | Per-app secret token, constant-time compare, same service call as 003. Token shown once, stored hashed. |
| 005 | **Real HTTPS + auto-revert, on a cheap VM** | One arm64 VM (a few €/month) with a domain. Traefik ACME HTTP-01. Scripted proof that a failing health check makes Nomad auto-revert and our deployment record says `failed`. |

**Phase 1 gate:** `traefik/whoami` is deployed by a webhook, is reachable over real HTTPS on the
VM, and a broken deploy auto-reverts.

**Status: passed 2026-10-04.** `deploy/vm/host.sh prove` printed `GATE PASSED` on an EC2
instance (Ubuntu 24.04, sslip.io names, Let's Encrypt production certificates): whoami deployed
and redeployed by webhook, a publicly trusted certificate, HTTP redirected to HTTPS, the webhook
host serving only the webhook, a broken deploy reverted by Nomad with no failed requests, and
every port but 22, 80 and 443 closed from outside. The same checks pass in the local lab
(`TestLab`).

## Phase 2: Compose + UI (the "Dokploy for Nomad" experience)

| # | Feature | Notes |
| --- | --- | --- |
| 006 | **Minimal auth** | One admin login for the UI, API tokens for clients. Needed before anything is public; teams and roles stay in Phase 4. |
| 007 | **HTMX UI v1** | App list, app detail, deploy button, deployment history with live status (SSE), logs streamed from Nomad allocations. Calls only `internal/service`. |
| 008 | **Secrets** | `secrets.Provider` interface, Nomad Variables implementation, declarative replace on every deploy. Per-service, write-only; a failed deploy puts the running version's secrets back (specs/008-secrets-provider). |
| 009 | **Compose translator** | `compose-go`; loud errors for unsupported fields, warnings for changed ones, explicit host volumes. Services reach each other by name (a sidecar keeps `/etc/hosts` current); named volumes are Nomad dynamic host volumes (specs/009-compose-translator). |
| 010 | **Databases + backups** | Postgres/MySQL/Redis as regular apps pinned to a node with a host volume. Backups as Nomad periodic batch jobs to S3-compatible storage. Done for PostgreSQL (dump with the service's image, upload and retention with rclone, restore with `pg_restore --clean`; specs/010-database-backups). MySQL/MariaDB and Redis are a follow-up. |
| 011 | **Rollback + export** | Redeploy any past version from the UI or API; export any app as a plain Nomad jobspec. Done: every deployment records its definition, a past healthy one redeploys with its pinned digests, and exports are HCL from a small writer of our own, parsed and planned by Nomad in the gate (specs/011-rollback-export). |
| 012 | **One-command install** | A fresh VM becomes Docker + Nomad (ACLs on) + Traefik + Caravana. Caravana runs as a systemd service, not as a Nomad job, so a cold start never waits on anything. Done: `deploy/install/install.sh` from each GitHub release, reruns upgrade with rollback, `--uninstall` keeps the apps, the console is public on one domain (specs/012-one-command-install). |
| 012.5 | **Console v2** | What the design handoff has that no feature above brings: edit the definition in the console (image, env, domains, copies), paste a Compose file on New app, CPU and memory per copy (Nomad's allocation stats), and a "needs you" list (failed deploys, unhealthy copies, failed backups). |

**Phase 2 gate:** a real multi-service compose app runs, with a database on a host volume and a
backup that restores. 008–012 are the MVP: after 012, someone else can install and use it.

**Status: passed 2026-10-11.** On a netcup VPS (Ubuntu 24.04, amd64), installed with the
published one-line command from release `v0.1.0`, behind Cloudflare DNS: Hollo
(`deploy/examples/hollo/`) runs as two services, Hollo and Postgres 17, each on a host volume,
at its own domain with a Let's Encrypt certificate, and federates. Its image was upgraded
(0.6.16 to 0.9.22) by a deploy. A backup to Cloudflare R2, then a restore, brought back the post
written before the backup and dropped the one written after.

**Working rule from 008 on:** each feature ships its console part with it, not later (backups
on the app page in 010, "redeploy this version" in 011). Constitution V already makes the API
and the console share the service layer; this keeps the console from falling behind.

## Phase 2.5: Aspire + Bella Baxter

- 013: Aspire compose fixtures, and service discovery env (`services__<name>__<endpoint>__0`) rendered from Nomad services.
- 014: Bella Baxter secrets provider (workload identity auth) and the secret-change webhook.
- The `PublishToCaravana` integration (separate C# repo) is generated from `api/openapi.yaml`.

**Gate:** Bella Baxter deploys to Caravana through `PublishToCaravana`.

## Phase 3: Cluster

Add a node over SSH, WireGuard mesh, Nomad TLS (Bella PKI or self-generated), placement and
drain in the UI, cluster-wide routing. **Gate:** 3 nodes that survive the loss of 1.

## Phase 3.5: GPU + binaries

**On demand:** not part of the MVP, and built when users ask for it. Phase 3 comes first: it
proves the core promise (one VM to twenty). Consul Connect versus native discovery over
WireGuard is decided at the start of Phase 3; Nomad namespaces per organization or project are
planned for Phase 4 (teams), so app IDs only need to be unique within one.

- GPU nodes: node bootstrap installs the NVIDIA driver, the container toolkit and Nomad's NVIDIA
  device plugin (not part of the Nomad download), then checks that the GPU fingerprint shows up.
- App model: a GPU request per service (count, vendor, minimum memory) becomes a Nomad `device`
  block. No GPU node that fits means a clear error, not a job stuck pending.
- Binary sources: a release artifact URL plus checksum becomes a Nomad `artifact` + `exec` task.
  `raw_exec` is never enabled.
- Templates: Ollama, vLLM and ComfyUI with a GPU request and a model cache on a host volume.

Needs a real GPU box to test (a rented GPU VM by the hour is enough). Nothing in Phases 1–3
changes for this. The pure translator only gains new fields.

**Gate:** an Ollama template serves a model on a GPU node, and a plain binary app deploys and
rolls back.

## Phase 4: Platform

Git sources + builds as Nomad batch jobs, app templates, teams and roles, managed Cloud tier
(needs legal review of Nomad's BSL first).

## Backlog (found while running real apps, not scheduled yet)

Each item names the phase it belongs to. Its feature spec settles the details.

### Memory per service (done: feature 013, release v0.1.1)

Done 2026-10-11 (specs/013-memory-per-service). Each service reserves memory (`memoryMB`, 256 MB
by default) and may burst to a ceiling while the node has memory spare (`memoryMaxMB`, by default
the larger of the reservation and 1024 MB). Compose's `mem_reservation` is the reservation and
`mem_limit` the ceiling. The installer and the lab turn on Nomad's memory oversubscription.

### Private registry credentials (Phase 2.5)

Added 2026-10-11. Caravana resolves digests and Nomad pulls images anonymously, so only public
images deploy. Most real apps, Bella Baxter (Phase 2.5's gate) and anything published by
`PublishToCaravana` need private ones. It needs:

- credentials per registry, write-only like secrets, kept in Nomad Variables
- the digest lookup using them (`internal/registry`)
- the job's docker `auth` taken from a template, never written into the job
- the console and the API

### Backups for more than Postgres (Phase 2.5)

Added 2026-10-11. Feature 010 backs up PostgreSQL only. Real apps also keep data in SQLite files
(Fhodor) and in plain volumes (Hollo's uploaded media). Next engines:

- `sqlite`: a consistent copy with `sqlite3 .backup` or `VACUUM INTO`, never a raw copy of a
  file in use
- `volume`: a tarball of the volume, taken while the service is stopped, or with a warning that
  it may be inconsistent
- MySQL/MariaDB and Redis, as 010 already planned

All use the same destination, retention, back up now and restore.

### Routing by path and header (Phase 3 or later)

Added 2026-10-11. Traefik routes one domain to one service today. Fediproxy, the maintainer's
ActivityPub sidecar, needs one domain split by path (`/.well-known/webfinger`, `/inbox`, `/ap/*`)
and by `Accept` header, between the app and Fediproxy. Until then, an app ships its own gateway
(YARP, Caddy) as the one service with the domain, and that gateway trusts Traefik's
`X-Forwarded-*` headers. Path prefixes and headers would join the overlay's `domains`, still as
Traefik tags. More than one router per service needs a constitution amendment, because VI names
the router `<app>-<service>`.

### Updates from the console (Phase 2.5 or 3)

Added 2026-10-11. Today Caravana is updated by running the install command again on the
server (feature 012: newer versions upgrade with rollback, apps keep running). Nothing tells the
owner a new version exists. Two steps:

1. **A notice:** the console shows "Caravana X.Y.Z is available" with the release notes and the
   command to run. It only reads the releases list.
2. **Updating from the console:** "Update" only records a request. A root systemd timer from the
   installer picks it up and runs that release's installer, checksum-verified, with the same
   rollback. Opt-in automatic updates (patch releases at a set hour) use the same timer. The
   console never gets root: Caravana's service user can't touch `/opt/caravana` or systemd, so a
   compromised console can't take over the host.

Phase 3 adds nodes over SSH with the same install, so updating Nomad and Caravana on every node
belongs with it.

### Bring your own Nomad (Phase 4)

Added 2026-10-11. Caravana can run as the PaaS alone, on a machine without Nomad. People create
an account in Caravana and connect the Nomad clusters they already run. They get Caravana's
console over those clusters: deploys, rollbacks, secrets, backups, logs and their status. Today one installation is one admin and one Nomad (`NOMAD_ADDR`)
on the same host. This is also the shape of Phase 4's managed Cloud tier. Questions its spec
has to settle:

- **Accounts:** sign-up and more than one user, which is Phase 4's teams and roles.
- **Clusters per account:** an address, a token and a CA per cluster, stored safely.
  Constitution VII keeps the control plane's own secrets in Nomad Variables, but here there is no
  local Nomad, so this needs an amendment.
- **Reaching the cluster:** Caravana calls a public Nomad API over TLS, or a small agent in the
  cluster connects out to Caravana. The agent works behind firewalls and NAT.
- **Jobs Caravana didn't create:** today they are refused (`job_conflict`). For observability,
  Caravana shows them read-only, and can later adopt them as apps.
- **What a cluster must have:** Caravana assumes Traefik's Nomad provider (routing tags), dynamic
  host volumes and Docker. It checks the cluster when connecting and says what's missing.
- **The scoped policy:** connecting includes making Caravana's own limited token from
  `caravana-policy.hcl`, never keeping a management token.

## Open decisions (settle them in the feature spec that needs them)

1. **Digest resolution**: settled in feature 003: our own small registry v2 client in
   `internal/registry`. go-containerregistry pulls in docker/cli, the moby client, oauth2 and
   cobra for one `HEAD` request (specs/003-deploy-flow/research.md R1).
2. **Tool pinning**: `staticcheck` through Go's `tool` directive in `go.mod`. Needed for 0.1.
3. **UI live updates**: settled in feature 007: server-sent events, with the htmx SSE extension
   vendored (no CDN, no Node). One stream per open page carries small notices ("deployments
   changed"); the page refetches that region, so the pages stay plain HTML and work without
   JavaScript (specs/007-htmx-ui-v1/research.md R1).

Decided 2026-09-30: Lima lab; Nomad under systemd with ACLs in the lab; AGPL-3.0; CI from 0.1;
the constitution owns the rules and CLAUDE.md links to it.
