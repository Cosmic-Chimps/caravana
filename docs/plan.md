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
| 012.5 | **Console v2** | What the design handoff has that no feature above brings. Done as feature 014 (specs/014-console-v2): Configure edits each service (image, env, port, domains, copies, health check, CPU, memory) and saves with a deploy, refused if the app changed since the form was drawn (`If-Match` on the app's revision in the API); New app takes a pasted Compose file with a preview, and Configure redeploys one; the Running tab shows CPU and memory per copy (Nomad's allocation stats, read only while the tab is open); the Apps home opens with a "needs you" list (failed or reverted deploys, out-of-memory kills, crash loops, unhealthy copies, failed backups and restores, no backup destination), `GET /v1/attention`. |
| 015 | **Keep and edit the Compose file** | Done (specs/015-compose-file-edit). An app made from Compose keeps the file as typed, comments included, with each deployment, and never a secret value. Configure edits the file and its overlay (preview, then deploy, refused if the app changed since the form was drawn) and shows the definition read-only. An app made before the file was kept is asked for it once. Rollback brings back the version's file, history downloads each entry's file, and secret edits, redeploys and the webhook keep it. A deploy from a definition drops it, with a warning. API 0.12.0: `getCompose`, `getDeploymentCompose`, `composeKept`. |
| 016 | **Volumes panel + volume backups** | Done (specs/016-volume-backups). A `volume` backup engine: the service stops, its volumes are archived to 010's destination with its retention, back up now and restore, and it starts again. It runs as a `caravana-volumes` prestart task in the app's own copy, triggered by restarting the copy's tasks, so a backup adds no job version. Caravana schedules these backups itself. The Backups tab has a Volumes section (mount, host path, size, last backup). API 0.13.0: `listVolumes`. First test case: Uptime Kuma 2 with its embedded MariaDB. |
| 017 | **Private registries** | Done (specs/017-private-registries). Settings › Registries keeps a user name and a write-only password or token per registry host, in a Nomad Variable (`caravana/registries/<key>`), with Test. The digest lookup (Basic and credentialed Bearer) and the node's pull both use them: each private service gets a per-task copy of the credentials that its docker `auth` block interpolates, so the job holds placeholders only. Images from any host work (ports, GitLab, Quay, self-hosted). Changing credentials updates the copies with no restarts. A refused credential is 422 `registry_auth_failed` with a console fix. API 0.14.0: `listRegistries`, `putRegistry`, `deleteRegistry`, `testRegistry`. Accepted trade-off: the credentials reach each private app's own container environment, so use a read-only token. |

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

- 018: Aspire compose fixtures, and service discovery env (`services__<name>__<endpoint>__0`) rendered from Nomad services.
- 019: Bella Baxter secrets provider (workload identity auth) and the secret-change webhook.
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
The real host upgraded from `v0.1.0` to `v0.1.1` on 2026-10-12 with the published command, Hollo
answering throughout: the first upgrade on a server with real data.

### Private registry credentials (done: feature 017)

Added 2026-10-11, done 2026-10-16 (specs/017-private-registries). Caravana resolved digests and
Nomad pulled images anonymously, so only public images deployed. Most real apps, Bella Baxter
(Phase 2.5's gate) and anything published by `PublishToCaravana` need private ones. Built:

- credentials per registry, write-only like secrets, kept in Nomad Variables
- the digest lookup using them (`internal/registry`)
- the job's docker `auth` taken from a template, never written into the job
- the console (Settings › Registries) and the API (`/v1/registries`)

### Node-level registry credentials (Phase 3)

Added 2026-10-16, from feature 017's research. Nomad's docker driver has no per-job way to give a
pull credential except the task's `auth` block, so 017 hands each private service its credentials
through a template with `env = true`. That puts `CARAVANA_REGISTRY_USERNAME` and
`CARAVANA_REGISTRY_PASSWORD` in the app's own container environment: the owner's own app can read
them (one owner per installation until Phase 4), which is why the console asks for a read-only
token. A client-level Docker auth file or credential helper on each node would keep the secret out
of containers, but Caravana can't own a file the Nomad client reads: it runs as a sandboxed
service, and in Phase 3 on another node than the copies. Once Phase 3 puts a Caravana agent on
each node, the agent can serve a node-level credential helper that reads the credentials from
Nomad Variables, and the per-task copies and the `auth` block go away.

### Amazon ECR (Phase 2.5 or later, on demand)

Added 2026-10-16. ECR's registry password is a token from `ecr:GetAuthorizationToken` that
expires after 12 hours, so 017's "user name and secret" breaks half a day after it is pasted.
Dokploy users hit exactly this (Dokploy issue #2040; its PRs #2660 and #4141 log in at deploy
time only, so a restart or reschedule after 12 hours still fails, and #4632 broke the `AWS` user
name by lowercasing it).

The plan, built on 017 with no breaking change:

- **A second credential kind.** A host matching `<account>.dkr.ecr.<region>.amazonaws.com` (the
  region comes from the host) takes an AWS access key ID and secret instead of a user name and
  password, for an IAM user with `AmazonEC2ContainerRegistryReadOnly`. Kept write-only in
  `caravana/registries/<key>` as today; an additive migration adds `kind` (`static` | `ecr`) to
  `registries`.
- **Caravana mints the token.** One SigV4-signed call to `ecr:GetAuthorizationToken` returns the
  token and its expiry. Prefer a small standard-library signer tested against AWS's published
  vectors over the AWS SDK (constitution I); take the SDK only if the signer proves fragile.
- **Caravana refreshes it.** A background refresher, like 016's scheduler, renews each ECR token
  at about half its life (6 h), with backoff, and once at startup. Each new token goes through
  017's `SyncRegistryCopies`, so every private app's copy gets it without a restart.
- **Digest lookups** use user `AWS` (case kept) and the token as Basic auth, which 017's client
  already speaks.
- **Fail loudly.** A refresh that keeps failing near expiry goes on the needs-you list with AWS's
  error. "Test" checks the AWS keys by minting a token.
- **What stays safe.** Images already on the node are reused without a pull, so a lapse only
  affects copies that must pull.

Not needed: public ECR (`public.ecr.aws`) works anonymously today; Azure ACR (service principal)
and Google Artifact Registry (`_json_key`) use long-lived credentials that 017 already handles.

The alternative, for AWS-only hosts: AWS's `docker-credential-ecr-login` with an EC2 instance
role, as Nomad's docker credential helper. No keys are stored, but it needs the node-level work
above (Phase 3) and still leaves Caravana's own digest lookup needing credentials.

Check before building:
- in the lab, that a task restarted after its variable changed pulls with the new value;
- against a real ECR repository (needs an AWS account), the challenge and the manifest answer.

### Backups for more than Postgres (Phase 2.5)

Added 2026-10-11. Feature 010 backs up PostgreSQL only. Real apps also keep data in SQLite files
(Fhodor) and in plain volumes (Hollo's uploaded media). Next engines:

- `sqlite`: a consistent copy with `sqlite3 .backup` or `VACUUM INTO`, never a raw copy of a
  file in use
- `volume`: done in feature 016 (a tarball of each volume, taken while the service is stopped)
- MySQL/MariaDB and Redis, as 010 already planned

All use the same destination, retention, back up now and restore.

### Network isolation between apps (Phase 3)

Added 2026-10-15, found while documenting Umbral's trusted proxies. Every app's copies share
Nomad's one bridge on a host, and ufw allows routed traffic. So any container can reach any other
app directly: by its copy's address, or by its host port on `caravana0`, which arrives masqueraded
as the bridge gateway (172.26.64.1, lab-checked). An app that trusts `X-Forwarded-*` should trust
only `10.99.0.1`, the address Traefik's requests come from, never the gateway. Isolation (a network
per app, or firewall rules between copies) belongs with Phase 3's networking work. Until then the
docs say: apps on one host trust each other.

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
