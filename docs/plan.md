# Plan: from nothing to the vision

How we get from an empty repo to the roadmap in [vision.md](vision.md). Each numbered item
is a **Spec Kit feature**: `/speckit-specify` → `/speckit-plan` → `/speckit-tasks` →
`/speckit-implement`, one git branch per feature. The details go in each feature's spec. This file
only sets the order and the reason for it. From Phase 2.6 on, features are named after the
screens of the design handoff (`design/console/`), and "The handoff, screen by screen" after
Phase 4 says where each screen lands.

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

## Phase 2: Compose + UI (the "Heroku on your own VM" experience)

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

## Phase 2.5: Running real apps day to day

Reordered 2026-10-16 with the maintainer. After 017, what was missing on the maintainer's own host
was being told when something breaks, a way to group apps by project and environment, and
building from source. Aspire and Bella Baxter move to Phase 2.75.

| # | Feature | Notes |
| --- | --- | --- |
| 018 | **Notifications** | Done (specs/018-notifications). Pushes what the "needs you" list derives (failed or reverted deploys, out-of-memory kills, crash loops, unhealthy copies, failed backups and restores) and deploy outcomes to the owner. Channels are signed outgoing webhooks, Slack and Discord incoming webhooks, and ntfy, set in Settings › Notifications; their secrets live in Nomad Variables (`caravana/notifications/<id>`). A notifier diffs the list every 30 s against the problems already sent (so a flapping problem sends one "opened"), deploys queue their outcome, and a SQLite delivery queue retries twice before marking a message failed. API 0.15.0: `/v1/notifications/channels`. Email later. |
| 019 | **Organization / Project / Environment** | Done (specs/019-projects-environments). Apps live in a project and an environment; every existing app is in Default › production with its ID, so nothing moved. An app's ID is its name there and `<project>--<environment>--<name>` elsewhere, still the key of its job, router, variables, volumes and backups, so constitution VI's `<app>-<service>` router name needed no change. **One Nomad namespace, not one per environment** (lab finding, 2026-10-17): Traefik's Nomad provider watches `default` only, `namespaces=*` broke routing for every app, and a namespace list needs Traefik redeployed (a gap on 80/443 and its ACME state) each time an environment is added; secrets are already isolated per job, so namespaces wait for teams (Phase 4). Copy to environment makes the app elsewhere without deploying; promote deploys one environment's newest healthy deployment into another by digest, keeping the target's env, secrets, domains, memory, copies and backups, and refuses to delete a service. API 0.16.0, console pages for projects, copy and promote, migration 0011. The gate step `projects-promote` and the console step `console-projects` cover it. |
| 020 | **Build from source** | A git repo plus a Dockerfile, built as a Nomad batch job (rootless BuildKit), pushed to a registry from 017, then deployed by digest. Build once, deploy to staging, promote to production (019); failed builds notify (018). First test case: Phanpy. Buildpacks or Nixpacks later, if ever. Moved here from Phase 4. Drawn as `NewAppScreen` (the Git repository source: repository, branch, "Found a Dockerfile" check), `DeployScreen` (Queued, Build, Roll out, Live, the Build log tab, "What changed from v23"), Settings › Connections › Git providers (`SettingsScreen`), and the deploy's "pushed by" trigger. Autodeploy on push ships with it as the git twin of the image webhook. |

**Gate:** on the maintainer's host, an app built from a git repo deploys to a staging environment,
is promoted to production, and a failure during it reaches the owner as a notification.

## Phase 2.6: The Developer view, complete (single node)

Added 2026-10-18. From here on the plan follows the design handoff ("caravana console",
Handoff 2, 2026-10-04, `design/console/`, local only): every feature names the screen it builds,
and the index at the end of Phase 4 lists every screen and where it lands. The handoff draws the
whole Developer view; after 020 these are the screens it has that the console doesn't, one
feature each, in the order a single-host owner reaches for them. Nothing here needs a second
node, and the rule from 008 on holds: each feature ships its console part with it.

| # | Feature | Drawn as | Notes |
| --- | --- | --- | --- |
| 021 | **Console IA: the handoff's shape** | `AppScreen` Configure, `Shell` sidebar, `SettingsScreen` nav, `DevHome` "Shipping now" | Put the build back on the handoff's information architecture. Configure becomes the short form the handoff draws (copies and size, shipping, the domains list, secret names) and Variables and secrets, Domains and Backups become their own app pages; Settings gets the grouped left nav (Connections, Security, People) with counts and warning marks per entry; the Apps home gets the "Shipping now" card for a rollout in progress; the deployment entry gains the handoff's stages (Queued, Roll out, Live; Build from 020). Stop, Start and Restart arrive as app actions (the handoff has Restart; Stop is the one addition): Nomad job stop without purge, and the task restart 016 uses. Cheap things ride along: a kind word per row on the Apps table (`NowScreen` shows `compose`, `database`), an app filter on the Apps home (open question 2 of the 007 review), and the deployment entry naming which token or person deployed (`DeployList` says "by ana"). |
| 022 | **Schedules** | `SchedulesScreen` | A command on a timer in a fresh copy of an app: the live version's image, variables and secrets, no traffic, as a Nomad periodic batch job (the shape backups already use). Presets ("every day at 03:00") over a cron line, a stop-after limit, skip a run if the last one is still going, Run now, Pause and Resume, the last 14 runs as marks, the logs of the last run, failed runs on the needs-you list and in notifications. Backups appear in the list as built-in schedules. Settles the installation's timezone (the handoff shows local time; backups are UTC today). |
| 023 | **Domains** | `DomainsScreen`, `AddDomainPanel` | One page for every address: status (live, getting certificate, DNS points elsewhere, certificate expiring), the app and port it goes to, the certificate's renewal date, and per-domain options drawn as switches: Force HTTPS, www redirect, Team only (basic auth until there are accounts), path or port. Add a domain shows the record to create (an A record to this host; the edge CNAME is Phase 3) and checks DNS, certificate and reachability. Every option is a Traefik middleware on the existing router, so constitution VI's router name is untouched; path and header routing between services stays in the backlog. The issuer settings from `CertsScreen` (Let's Encrypt contact, warn before expiry) come here; its cluster parts wait for Phase 3. |
| 024 | **Logs** | `LogsScreen` (Developer mode) | Logs across an environment, newest first, with levels read from the line (error, warn, info, debug), a search box, a time range, a histogram with deploy markers, repeated lines folded, "only this service", "lines around this", Download, and deploys as rows in the stream. ANSI sequences stripped or rendered. Backed by Nomad's log API as today; retention stays Nomad's, and "send them somewhere longer" is a Settings connection for later. |
| 025 | **Monitoring and capacity** | `MonitoringScreen`, the Platform panel on `DevHome`, the Capacity panel of `NowScreen` for one host | Requests, errors and slowest 5 % per app from Traefik's metrics, memory per app, each compared with the same hour over the last 7 days (the shaded band) with deploy markers, and a table of apps sorted "unusual first". The host's own capacity: memory reserved against used, disk, "room for about 4 more apps like whoami", with "disk nearly full" on the needs-you list. History in a small SQLite ring buffer fed by Nomad's allocation and node stats and Traefik's metrics endpoint on loopback. |

Also in this phase, with no screen of their own: "Start from an example" on New app, fed by
`deploy/examples/` (the handoff has no templates screen; one to ask for), Telegram and email
channels in Notifications (`NotifySettings` draws Email), and 2-step sign-in for the one admin
(`LoginScreen` draws the code step).

**Gate:** on the maintainer's host, each of the five pages shows real data for the apps already
there, a schedule runs and fails visibly, and Configure for a two-service Compose app fits on
two screens.

## Phase 2.75: Aspire + Bella Baxter

- 026: Aspire compose fixtures, and service discovery env (`services__<name>__<endpoint>__0`)
  rendered from Nomad services. `PublishToCaravana` targets a project and environment (019).
- 027: Bella Baxter secrets provider (workload identity auth) and the secret-change webhook.
  Drawn as `VariablesScreen`'s "Where secrets come from" panel with one external source, not the
  handoff's provider gallery: the constitution keeps Caravana whole without any provider and names
  Bella Baxter first.
- The `PublishToCaravana` integration (separate C# repo) is generated from `api/openapi.yaml`.

**Gate:** Bella Baxter deploys to Caravana through `PublishToCaravana`.

## Phase 3: Cluster

Add a node over SSH, WireGuard mesh, Nomad TLS (Bella PKI or self-generated), placement and
drain in the UI, cluster-wide routing. The handoff's Operator view is this phase, screen by
screen. Features are numbered when they are specified, in about this order:

| Feature | Drawn as | Notes |
| --- | --- | --- |
| Add a server | `AddServerScreen` | A join command with a one-time token (or SSH access), the checks as they pass (connected, Docker and disk, reaches the others on the private network, ready to take work), the server named and put in a pool. The installer from 012 becomes the join path. |
| Servers and the server page | `ServersScreen`, `NodeScreen` | The list with CPU, memory and disk meters (used and reserved) and copies per server; the server page with what runs here, "Move work off" (a drain that says what moves and what a volume pins), "Take work again", the agent versions, and what happened on this server. |
| Overview, the Operator home | `NowScreen` | The cluster headline, a panel per node with its copies, the apps table with a node column, needs-you, the live activity feed from Nomad's event stream, capacity for the cluster. The Developer and Operator toggle in the sidebar arrives here. |
| Activity | `ActivityScreen` | One row per operation (deploys, restarts and moves, servers, schedules, failures) with its steps, trigger and outcome, by day. |
| Placement | `PlacementScreen`, `SchedulerScreen` | Why a copy can't be placed, with the free memory per server and the ways to fix it (use less, add a server, undo); the scheduler's settings in plain words (spread or pack, priorities per environment, crash and server-loss rules); placement rules per app (only on, never two on one server, servers with a label). Nomad affinity, spread and constraints behind the words. |
| Proxy and routing | `ProxyScreen` | A proxy per server with req/s, p95 and errors, take one out of rotation, routes with healthy copies per domain, and the defaults for every route (HTTP to HTTPS, timeout, upload size, requests per visitor, health check, slow start). |
| Certificates | `CertsScreen` | The cluster parts: the cluster CA and the server and client certificates Caravana renews (Nomad TLS), and wildcard certificates through a DNS provider (`SettingsScreen` › DNS providers), which previews need later. |
| Updates | `UpdatesScreen` | The backlog's "Updates from the console", cluster-wide: available updates (Caravana, Nomad, OS security fixes), the plan server by server with what stops, an update window, past runs. |
| Operator logs | `LogsScreen` (Operator mode) | 024 plus the scheduler, Docker and Caravana sources per server. |
| Capacity | `CapacityScreen` | 025 across servers: memory by server and project, "sizes worth changing" from 30-day peaks, what still fits per size, the reserved-memory trend and the date it runs out. |
| A shell into a copy | `NodeScreen` has "Open shell" for a server only | Nomad alloc exec over a websocket; the no-JavaScript first step is "Run a command" with captured output. Exposing a TCP port (a ufw rule beside Traefik) belongs here too. |

**Gate:** 3 nodes that survive the loss of 1. Node pools that add servers at a provider by
themselves (`PoolsScreen`) and previews wait for Phase 4.

## Phase 3.5: GPU + binaries

**On demand:** not part of the MVP, and built when users ask for it. Phase 3 comes first: it
proves the core promise (one VM to twenty). Consul Connect versus native discovery over
WireGuard is decided at the start of Phase 3; Nomad namespaces per project or environment wait
for teams (Phase 4, see 019), so app IDs stay unique across the installation.

- GPU nodes: node bootstrap installs the NVIDIA driver, the container toolkit and Nomad's NVIDIA
  device plugin (not part of the Nomad download), then checks that the GPU fingerprint shows up.
- App model: a GPU request per service (count, vendor, minimum memory) becomes a Nomad `device`
  block. No GPU node that fits means a clear error, not a job stuck pending.
- Binary sources: a release artifact URL plus checksum becomes a Nomad `artifact` + `exec` task.
  Drawn as `NewAppScreen`'s Binary source (download URL, checksum, command, arguments) with the
  Sandboxed and Java isolation choices. The handoff's third choice, "Full host access", is not
  built: `raw_exec` is never enabled (`docs/vision.md`).
- Templates: Ollama, vLLM and ComfyUI with a GPU request and a model cache on a host volume.

Needs a real GPU box to test (a rented GPU VM by the hour is enough). Nothing in Phases 1–3
changes for this. The pure translator only gains new fields.

**Gate:** an Ollama template serves a model on a GPU node, and a plain binary app deploys and
rolls back.

## Phase 4: Platform

More build sources (buildpacks, monorepos), app templates, teams and roles, managed Cloud tier
(needs legal review of Nomad's BSL first). Git sources with a Dockerfile moved to Phase 2.5 (020).
Drawn as:

- `MembersScreen`: organization roles (owner, admin, member), a role per project with an
  override per environment (view, deploy, maintain), guests whose access ends, invites. Nomad
  namespaces per project arrive with this (019's finding).
- `SsoSettings`, `AuditSettings`, `SshSettings`: single sign-on with join-automatically and
  require-SSO, the 90-day audit log with diffs, SSH keys placed on every server.
- The org switcher and `NewOrgScreen`: an organization on the shared servers with a memory
  limit, or on a pool of its own.
- `PreviewsScreen`: a preview per pull request on a wildcard domain, which apps get their own
  copy and which share, starting data (empty, seed, a copy of staging), sleep when idle, delete
  after the PR closes, a limit per project.
- `PoolsScreen`: pools by server type and region, minimum and maximum, servers added by
  themselves at the provider, labels for placement rules.
- `BackupsScreen` extras: restore to any second, weekly restore tests, "not backed up" gaps with
  "safe to lose", download.
- `VariablesScreen`'s grid: one row per name and one column per environment, shared variables,
  "not shipped yet" changes, edit as `.env`.

### The handoff, screen by screen

Where each of the handoff's 26 screens lands. "Built" means the console has it in the handoff's
sense, even where the layout differs (007 and 014 record the differences).

| Screen | Built | Lands in |
| --- | --- | --- |
| `LoginScreen` | password sign-in | 2-step code in 2.6; SSO and reset link in Phase 4 |
| `DevHome` | Apps home: headline, needs-you, activity | "Shipping now" card in 021; the Platform panel in 025 |
| `DevHome` › `ProjectScreen` | project page, copy, promote (019) | environment switcher and "Deploys in staging" rail in 021 |
| `AppScreen` | Running, Deployments, Configure, Logs, Backups | Configure's short form, Restart and Stop in 021 |
| `DeployScreen` | a progress line in the deployment entry | 020 (Build) and 021 (stages) |
| `NewAppScreen` | image and Compose sources | git in 020, examples in 2.6, binary in 3.5 |
| `VariablesScreen` | per service, inside Configure | its own app page in 021; Bella Baxter source in 027; the cross-environment grid in Phase 4 |
| `LogsScreen` | per app: copy, stream, pause, wrap | 024; Operator mode in Phase 3 |
| `DomainsScreen` | domains inside Configure | 023 |
| `SchedulesScreen` | backups only | 022 |
| `BackupsScreen` | per app: volumes, back up now, restore | its own app page in 021; the extras in Phase 4 |
| `MonitoringScreen` | CPU and memory per copy, live only | 025 |
| `PreviewsScreen` | — | Phase 4 |
| `PlacementScreen` | — | Phase 3 |
| `SettingsScreen` | one page: password, tokens, destination, registries, notifications | the nav in 021; git providers in 020; DNS providers, SSH keys, SSO, audit in Phase 3 and 4 |
| `MembersScreen` | — | Phase 4 |
| `NewOrgScreen`, org switcher | — | Phase 4 |
| `NowScreen` | — | Phase 3 (its Capacity panel for one host in 025) |
| `ServerScreens` (list, server, add) | — | Phase 3 |
| `CapacityScreen` | — | Phase 3 (one host in 025) |
| `ActivityScreen` | — | Phase 3 |
| `SchedulerScreen` | — | Phase 3 |
| `PoolsScreen` | — | Phase 4 |
| `ProxyScreen` | — | Phase 3 |
| `CertsScreen` | Let's Encrypt through Traefik | issuer settings in 023; cluster certificates in Phase 3 |
| `UpdatesScreen` | — | Phase 3 (the notice from the backlog's "Updates from the console" may come earlier) |

Not drawn by the handoff and to ask for: templates ("Start from an example"), Stop as an action,
a shell or "run a command" into a copy, exposing a TCP port, a light theme.

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
Other PaaS tools hit exactly this: logging in at deploy time only still fails on a restart or
reschedule after 12 hours, and lowercasing the user name breaks `AWS`.

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

### What other PaaS tools have that we don't (ranked 2026-10-17)

Added 2026-10-17, from comparing the live console, the design handoff in `design/console/` and
`internal/app` against the self-hosted PaaS tools people come from. Not gaps: digest-pinned
deploys with auto-revert, rollback by recorded definition, promotion by digest, the needs-you
list with signed notifications, volume backups inside the app's copy, export as a Nomad job,
`If-Match` on edits and the kept Compose file. Those stay the pitch.

Ranked by day-to-day value for the single-host owner against cost and fit with the Nomad model.
"Drawn" names the handoff screen that already covers it. Scheduled on 2026-10-18: the Phase column
names the feature (Phase 2.6 is the handoff's Developer view, screen by screen).

| # | Gap | Elsewhere | How it fits Caravana | Drawn | Phase |
| --- | --- | --- | --- | --- | --- |
| 1 | Build from source | Git providers, autodeploy on push, build server, concurrent-build limit | Feature 020. Ship "autodeploy on push" with it, as the git twin of the image webhook | `NewAppScreen` | 2.5 (020) |
| 2 | Stop, start, restart | Deploy, Reload, Rebuild, Stop per service; bulk actions per environment | Nomad `job stop` without purge, and the task restart 016 already uses. The design has the ○ stopped mark; `docs/design-feedback-007.md` 3.4 notes "no Restart yet" | `AppScreen` (partly) | 2.6 (021) |
| 3 | Scheduled tasks | A cron plus a command run in the service's container, with a run history | Nomad periodic batch jobs, which backups already are. The design feedback's "workload kinds: web, worker, scheduled" (7.1) and a Runs tab | `SchedulesScreen` | 2.6 (022) |
| 4 | Domain extras | Path, strip path, redirects, basic auth, custom middlewares, "Validate DNS", DNS providers that create records | Basic auth and a www or apex redirect are Traefik tags. A DNS check (the A record points at this host) and the certificate state are reads. Path and header routing stays in "Routing by path and header" above | `DomainsScreen` | 2.6 (023) |
| 5 | Monitoring with history | CPU, memory, disk, block and network I/O per app and per server, with a time range | Caravana reads CPU and memory only while the Running tab is open. A small ring buffer in SQLite from Nomad's node and allocation stats, host disk and memory on the Apps page ("room for about 4 more apps"), and "disk nearly full" on the needs-you list | `MonitoringScreen`, `CapacityScreen` | 2.6 (025) |
| 6 | Templates | About a hundred one-click apps, each a Compose file with a form | Compose plus an overlay already is a template. Start with "Start from an example" on New app, fed by `deploy/examples/` | none | 2.6 (New app, no screen) |
| 7 | More notification kinds | Email, Telegram, Teams, Gotify, Resend, Lark | Telegram is one HTTPS call with a bot token; email needs SMTP settings. Both are 018 channels | `SettingsScreen` | 2.6 (Notifications) |
| 8 | A shell into a copy | "Open Terminal" per container | Nomad alloc exec over a websocket. A terminal needs scripted UI; the no-JavaScript first step is "Run a command" with captured output | none | 3 |
| 9 | Databases as a kind | Postgres, MySQL, MariaDB, Mongo, Redis with generated credentials, an internal connection URL, an optional external port | A database stays a service. Missing: dump engines beyond Postgres ("Backups for more than Postgres" above), the internal address on the app page, and exposing a TCP port, which needs a ufw rule beside Traefik | partly | 2.5 (backups), 3 (TCP ports) |
| 10 | Preview deployments | A pull request gets its own deploy on a wildcard domain | An environment per pull request on top of 019 and 020 | `PreviewsScreen` | 4 |
| 11 | Account security | 2FA, passkeys, a Sessions page, invitations, custom roles and an audit log (enterprise) | Teams are Phase 4. 2FA or a passkey for the one admin, a sessions list with "sign out everywhere" and an audit trail keyed by token name are cheap now | `LoginScreen`, `MembersScreen` | 2.6 (2-step for the admin), 4 (teams) |
| 12 | Shared variables | Project-level env referenced as `${{project.VAR}}` | Variables shared by an environment would help the staging and production split from 019 | `VariablesScreen` | 4 (`VariablesScreen` grid) |
| 13 | Requests log | Traefik's access log, filterable, with a cleanup schedule | Traefik's JSON access log, shown as per-app request counts and status mix. Watch disk use | none | 2.6 (025 reads Traefik's metrics), 3 (the log itself) |
| 14 | Updates and host upkeep | "Check for updates", a cleanup button, Let's Encrypt email in settings | "Updates from the console" above. Nomad's docker driver removes unused images by default, so a daily image cleanup is covered; 020's build cache will need its own pruning | `UpdatesScreen` | 3 (Updates) |
| 15 | List ergonomics | Filter, type and status selects, tags, bulk select | The Apps page has no filter. The design feedback (5.2) already asks at what size one appears | none | 2.6 (021) |

Not to copy: the Traefik file editor, the Docker containers, images, networks and events pages
and per-app Traefik config (the Caravana twin is the Operator view and a read-only view of jobs
Caravana didn't create); Swarm-isms (Reload, Rebuild, a replicas box applied by a redeploy,
"Clear deployments", since the history is a ledger and edits are deploys); the AI assistant and
Import.

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
