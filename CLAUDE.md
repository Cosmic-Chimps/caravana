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
`internal/ui` serves `/login`, the settings page (password, API tokens) and `/static` on the main
listener, never on the webhook-only one. The stylesheet is generated, not hand-written:
`tools/ui-css.sh build|check` runs the pinned Tailwind v4 standalone CLI with daisyUI 5 over
`internal/ui/style/` and the templates into the committed `internal/ui/static/app.css` (CI runs
`check`); styles follow `DESIGN.md`. The gate has a first step `api-needs-token`; `host.sh admin`
runs the CLI on the VM and `prove` makes and revokes its own token. For local work:
`go run ./cmd/caravana`, then `go run ./cmd/caravana admin init --email you@example.com`, then
sign in at `http://127.0.0.1:7070/login`. Features 001–006 are merged into `main`. Feature 007 is
implemented on branch `007-htmx-ui-v1`: the web console, `internal/ui` calling only
`internal/service`. Pages: Apps (list, headline, activity), New app, and per app Running,
Deployments (deploy form, history with rollout progress), Configure (definition, webhook token,
delete) and Logs; `/settings` is the old `/account` (which redirects). Live updates are
server-sent events carrying only notices ("deployments changed"); the page refetches that region
from a `/parts/...` route, so everything also works as plain HTML and forms without JavaScript.
htmx and its SSE extension are vendored, not fetched at run time: `tools/ui-vendor.sh fetch`
downloads the pinned versions (`tools/ui-vendor.versions`) and verifies their checksums,
`tools/ui-vendor.sh check` verifies the committed files offline (CI runs it). The API gained
`listApps`, `createApp`, `deleteApp`, `listRecentDeployments`, `listCopies` and `getLogs`
(`api/openapi.yaml` 0.5.0), and Caravana's scoped Nomad policy gained `read-logs`. `TestLab` runs
the console steps (`cmd/caravana/console_lab_test.go`; `TestConsoleLab` runs only those, in about
a minute) before the gate steps, and the gate has a step `console-reads` that catches a missing
`read-logs`. For local work the console is at `http://127.0.0.1:7070/`. Feature 007 is merged
into `main`. Feature 008 is implemented on branch `008-secrets`: each service has `secrets`
(`{name, value?}`; no value means keep the current one), values are write-only and live only in
Nomad Variables at `nomad/jobs/<app>/<service>`, which only that service's tasks can read, and
the stored app, responses, logs and the Nomad job hold names only. `internal/secrets` is the
seam (`Provider`, `Sets`, `Build`, `ErrUnavailable`; `secretstest` has the in-memory store and
the shared contract suite `TestProvider`); the Nomad store is `deploy.Variables`. A deploy
replaces the sets just before `Register`; the replaced sets are kept at
`nomad/jobs/<app>/caravana/previous`, owned by the deployment, and once it ends they are
dropped (healthy) or put back (failed, reverted), also across restarts. The task's env
template is `noop`, so values reach copies only through a rollout. Configure has a Secrets
section (set, remove, each a deploy); the API is `deployApp` with `secrets`
(`api/openapi.yaml` 0.6.0, new code `secret_store_unavailable`). The scoped policy gained
`variables` on `nomad/jobs/*` (run `host.sh up` on an existing host), and the gate has a step
`secrets-render`. Feature 009 is implemented on branch `009-compose-translator` (stacked on 008):
`POST /v1/apps/{id}/compose` deploys an app from a Compose file plus a JSON overlay (per service:
domains, health, secrets) and `POST /v1/apps/{id}/compose/preview` shows the app it becomes
(`api/openapi.yaml` 0.7.0). `internal/compose` is the only importer of `compose-go` and is pure:
it loads from bytes with no disk and no OS environment, variables only take the file's defaults,
and an allowlist of Compose attribute paths (`fields.go`) decides what is honored, warned
(`depends_on`, a `healthcheck` command, published ports, ...) or refused, naming the path
(`services.web.build`). Two capabilities came with it for every input: services of a
multi-service app reach each other by name (each group gets a `names` prestart sidecar, busybox
pinned by digest via `CARAVANA_NAMES_IMAGE`, that rewrites the group's `/etc/hosts` from the
`<app>-<service>-internal` alloc-address registrations, skipping canaries), and `Service.Volumes`
become Nomad dynamic host volumes `<app>_<volume>` (`mkdir` plugin), created by
`deploy.EnsureVolumes` before register and deleted with the app; a group with a volume runs one
copy and deploys stop-then-start. The scoped policy gained `host-volume-create/-delete` and
`host_volume "*"` write with `"traefik-acme"` deny (Nomad allows no `_` in these names), and the
gate has a step `compose-app`. Feature 010 is implemented on branch
`010-databases-backups` (stacked on 009): a service with `backup: {engine: postgres, schedule?,
keep?}` gets a Nomad periodic batch job `<app>_<service>_backup` (pg_dump with the service's
pinned image, then rclone uploads to the installation's S3-compatible destination and deletes
dumps beyond `keep`); `<app>_<service>_restore` is a one-off batch job running `pg_restore
--clean`. Job shapes are pure in `nomadjob/backup.go`; `deploy/backups.go` writes each job's own
Nomad Variable (credentials from the service's `POSTGRES_*` env or secrets, plus
`RCLONE_CONFIG_DEST_*`), registers the jobs and reads the runs' outcome from their output; a
watcher in `service/backups.go` records runs and restores in SQLite (`0005_backups.sql`). The
destination's secret key is in the Nomad Variable `caravana/backup-destination`. API: destination
GET/PUT, list, back up now, restore (`api/openapi.yaml` 0.8.0); console: Settings › Backup
destination and a Backups tab. The policy gained variables `caravana/*`, and the gate has a step
`backup-restore` (A, back up, B, restore, A) against an `rclone serve s3` bucket job. `TestLab`
gives the in-process Caravana a token made from `deploy/vm/caravana-policy.hcl` (applied on every
run), not the lab's management token, so a missing capability fails in the lab. Feature 011 is
implemented on branch `011-rollback-export`: every deployment records the app definition it
deployed (`deployments.definition`, secret values left out, migration `0006_rollback.sql`) and
its `source` for a redeploy. `Service.RedeployVersion` deploys a past healthy deployment again:
`Deployer.CheckDigests` (a registry `HEAD` per pinned digest, `registry.Client.Exists`) refuses a
deleted image first, then the normal deploy runs with those digests pinned (no tag lookup), and
the definition becomes the stored app. Any deploy that drops a secret warns that its value is
deleted. Export is `nomadjob.HCL`, a small pure writer for the fields `Translate` sets (golden
`*.golden.nomad.hcl`; a reflection test fails naming any field it doesn't write), behind a
header naming what the job needs (Traefik, host volumes, secret variable paths); there is no HCL
module. API 0.9.0: `redeployDeployment`, `exportApp`, `exportDeployment` (`text/plain`
downloads), `Deployment.source`/`definitionRecorded`, codes `deployment_not_healthy`,
`definition_not_recorded`, `no_live_deployment`. Console: each history entry offers "Redeploy
this version" and "Export jobspec"; Configure has Export. The gate has a step `rollback-export`,
and `exportMatchesLive` parses an export with Nomad and plans it against the live job (diff
`None`) for whoami, the secrets app and the compose app. Stored times now keep nine fraction
digits so they sort as text. Feature 012 is implemented on branch `012-one-command-install`:
`deploy/install/install.sh` turns a fresh Ubuntu 24.04 VM into a Caravana host (preflight that
refuses before changing anything; Docker, Nomad with ACLs and CNI at the lab's pins with
checksums; `caravana0`, ufw, Traefik with one static route, the console domain to
`127.0.0.1:7070`; Caravana under systemd from `/opt/caravana/<version>` via the `current` link,
`CARAVANA_TRUSTED_PROXIES=127.0.0.1,::1`; the admin via `/dev/tty`). Rerunning it upgrades (the
data is copied first and put back with the old binary if the new one fails health); an older
version is refused; `--uninstall [--purge]` keeps Nomad, Traefik and the apps. The host files
live in `deploy/install/` and ship in each release, which `tools/release.sh build` makes
(tarballs per arch, `install.sh` with its version, `SHA256SUMS`) and `.github/workflows/release.yml`
publishes on a `v*` tag. Builds carry a version (`caravana version`, `/v1/health` answers it,
`api/openapi.yaml` 0.9.1). On a host: `sudo caravana admin …`, `sudo caravana-nomad …`.
`deploy/vm/host.sh up` builds a dev release and runs the installer over SSH (`setup.sh` is gone),
and `prove` goes through the public console domain. `deploy/dev/install-test.sh` runs every
installer path on a throwaway Lima VM. The Phase 2 gate passed on 2026-10-11: Hollo (two services,
Postgres on a volume, backed up to R2 and restored) on a netcup VPS installed from release `v0.1.0`.
`deploy/examples/hollo/` holds its files and a runbook. Feature 013 (memory per service, branch
`013-memory-per-service`, release `v0.1.1`): a service reserves `memoryMB` (default 256,
`CARAVANA_DEFAULT_MEMORY_MB`) and may burst to `memoryMaxMB` (default the larger of the
reservation and 1024, `CARAVANA_DEFAULT_MEMORY_MAX_MB`) as Nomad's `memory_max`. Compose's
`mem_reservation` is the reservation and `mem_limit` the ceiling. The installer and `lab.sh up`
turn on Nomad's memory oversubscription (`operator scheduler set-config`). Without it, Nomad
ignores the ceiling and its register warning lands in the deployment's warnings. Feature 014
(Console v2, the plan's 012.5, branch `014-console-v2`): every stored app has a `revision` (the
first 16 hex digits of the SHA-256 of its stored document; `getApp` sends it as `ETag`), and
`deployApp`/`deployCompose` take `If-Match` (412 `app_changed`) and `deployCompose` takes
`If-None-Match: *` (409 `app_exists`); in the service that is `Condition` on `DeployIf` /
`DeployComposeIf`, and `store.SaveAppIf` checks and writes in one transaction. Configure is now
an edit form per service (`internal/ui/edit.go` parses, `merge.go` lays it over the stored app so
command, args, secrets, volumes and backup are kept; env values are shown). New app has a
Compose tab and Configure has "Deploy from Compose" (`composeform.go`, `composepage.go`): preview
first, overlay with domains, health, secrets (never echoed) and backup. The Running tab shows CPU
and memory per copy from Nomad's allocation stats (`deploy.Usage`, `listCopies?usage=true`),
read every 5 s only while the tab's stream has `?tab=running`; Nomad collects them only with
`telemetry { publish_allocation_metrics = true }`, which the installer's `nomad.hcl` now sets
(rerun the installer on an existing host). The Apps home opens with a "needs you" list
(`service.Attention`, `GET /v1/attention`), derived on every read: failed or reverted deploys,
out-of-memory kills, crash loops (restarts plus failed copies of the last hour reach 3),
unhealthy copies, failed backups and restores, no backup destination. API 0.11.0. The gate has a
step `console-v2-reads`. Feature 015 is implemented on branch `015-compose-file-edit` (stacked on
014): an app deployed from Compose keeps the file as typed, comments included, on the app
(`apps.compose`) and on each deployment (`deployments.compose`), both JSON of `compose.Request`
with every secret value removed (`Request.WithoutSecretValues`; migration `0007_compose_file.sql`),
and the app's `revision` covers it, so a comment-only edit is a change. `store.SaveApp`,
`SaveAppIf`, `InsertApp` and `CreateDeployment` take the file (nil for none); in the service,
`made{file, warnings}` says what a deploy was made from. Configure of a kept-file app shows the
Compose form first (`draftOf` fills it from the kept request; preview, then deploy with
`If-Match`) and the definition read-only ("This app is edited through its Compose file."; the
per-field save answers 409). An app made from Compose before this feature is asked for its file
once ("paste it once": empty editor, overlay prefilled from the app, per-field form still
editable). Rollback (`RedeployVersion`) makes the version's file the kept one, and each history
entry made from a file offers "Download Compose file". Secret edits (`editSecrets` rewrites the
overlay's secret names), `Redeploy` and the webhook keep the file and record it on the new
deployment. A deploy from a definition (`deployApp` with a body, `Deploy`) drops the file with the
warning "This app's Compose file was dropped: it was deployed from a definition, not a file." API
0.12.0: `getCompose` (with the revision as `ETag`), `getDeploymentCompose`, `composeKept` on
`AppRecord`, `AppSummary` and `Deployment`, code `compose_not_kept` (404). No page, download or
API read holds a secret value (tests search for `s3cret-sentinel-015`). `TestConsoleLab` has a step
`console-compose-file-edit` (create from a file, edit, roll back, download). Feature 016 is
implemented on branch `016-volume-backups`: a service's backup may use the engine `volume`.
The service stops, its volumes are archived to the backup destination, and it starts again. Restore
works the same way, all or nothing across the service's volumes, and files added since the backup
are deleted. It runs inside the app's own copy: every service with volumes gets a
`caravana-volumes` prestart task (the pinned rclone image, `nomadjob.VolumeScript`, always exits 0)
that measures its volumes at each start. Caravana writes a request (`REQUEST`, `ACTION`, `OBJECT`,
`TS`, `NOT_AFTER`) into the task's variable `nomad/jobs/<app>/<service>/caravana-volumes`, waits
until `local/request.env` shows it, then restarts the copy's tasks (`Allocations().Restart`, all
tasks). The service stops cleanly, the task does the run and writes
`/alloc/data/caravana/<request>.result`, and the service starts again: same copy, same job version,
no Nomad deployment. Other facts:
- The destination's keys render only into `secrets/destination.env`, which the fs API can't read.
- Restarted tasks' output isn't kept, which is why the outcome goes in a file.
- Scaling to 0 was rejected: two job versions per backup, and auto-revert could stick at count 0
  (research L1–L11).
- Caravana schedules volume backups itself (`hashicorp/cronexpr`, now a direct dependency), and
  holds the app's deploy lock for the whole run, so deploys and deletes wait. A second run is
  refused (`ErrBackupRunning`), and runs left running at a restart are settled at start.
- `Copy.RequestedRestarts` keeps these restarts off the crash-loop rule.
- Migration `0008_volume_backups.sql`: `engine`, `stopped_ms`, `volumes` on backups and restores,
  and `volume_sizes`.
- The policy gained `alloc-lifecycle` and `read-fs`: rerun the installer, then redeploy each volume
  app once so it gains the task.
- API 0.13.0: `listVolumes` (`AppVolume`), `engine` and `stoppedSeconds` on backups and restores,
  `volumes` on backups, 409 `no_running_copy`.
- The Backups tab opens with a Volumes section (mount, host path, size, last backup).
- The gate has a step `volume-backup-restore`, and `TestConsoleLab` a step `console-volumes`.

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
  from Nomad allocations. It calls only `internal/service`. Live updates are SSE (decided).
  (done, branch `007-htmx-ui-v1`)
- 008 Secrets: `secrets.Provider`, Nomad Variables, declarative replace on every deploy
  (done, branch `008-secrets`)
- 009 Compose translator (`compose-go`): loud errors, warnings for changed meaning, explicit
  host volumes (done, branch `009-compose-translator`)
- 010 Databases + backups: pinned to a node with a host volume; backups as periodic batch jobs to
  S3-compatible storage (done for PostgreSQL, branch `010-databases-backups`)
- 011 Rollback + export: redeploy any past version; export any app as a plain Nomad jobspec
  (done, branch `011-rollback-export`)
- 012 One-command install (done, branch `012-one-command-install`)
- 012.5 Console v2: edit the definition, paste Compose, CPU and memory per copy, "needs you"
  (done as feature 014, branch `014-console-v2`)

Gate to finish Phase 2: a real multi-service compose app runs, with a database on a host volume
and a backup that restores. Passed 2026-10-11 (Hollo on netcup, see `docs/plan.md`).

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
tools/ui-vendor.sh fetch           # re-download the pinned htmx + SSE extension into internal/ui/static/js (after bumping tools/ui-vendor.versions)
tools/ui-vendor.sh check           # fails if the committed scripts don't match their pinned checksums (CI runs it)
open http://127.0.0.1:7070/        # the console, after `go run ./cmd/caravana` and `admin init`; sign in
```

```bash
eval "$(deploy/dev/lab.sh env)"                               # Caravana reads NOMAD_ADDR / NOMAD_TOKEN
CARAVANA_LAB=1 go test ./cmd/caravana -run TestLab -v -timeout 20m   # opt-in end-to-end test: console steps, then the gate (~12 min)
CARAVANA_LAB=1 go test ./cmd/caravana -run TestConsoleLab -v   # only the console steps (~1 min)
```

```bash
deploy/dev/lab.sh up               # start (or create) the local Nomad lab
deploy/dev/lab.sh check            # deploy whoami.json by hand and call it over HTTPS
eval "$(deploy/dev/lab.sh env)"    # NOMAD_ADDR + NOMAD_TOKEN for Caravana on the Mac
deploy/dev/lab.sh shell            # inside the VM: sudo lab-nomad <cmd> (management token)
```

```bash
tools/release.sh build 0.0.0-dev.1 /tmp/rel   # a release: tarballs per arch, install.sh, SHA256SUMS
tools/release.sh notes 0.3.0       # that version's CHANGELOG.md section: the release's notes (a v* tag without one fails)
deploy/dev/install-test.sh         # the installer on a throwaway Lima VM: install, rerun, upgrade, rollback, uninstall (~10 min)
deploy/vm/host.sh up               # build a dev release and install/upgrade it on the public VM (settings in deploy/vm/host.env)
deploy/vm/host.sh prove            # the gate against the VM (~15 min), ends GATE PASSED/FAILED
deploy/vm/host.sh admin status     # sudo caravana admin <cmd> on the VM
deploy/vm/host.sh tunnel           # private access to the VM's Nomad and Traefik dashboard
eval "$(deploy/vm/host.sh env)"    # NOMAD_ADDR, NOMAD_TOKEN through the tunnel, CARAVANA_URL
shellcheck deploy/dev/*.sh deploy/vm/*.sh deploy/install/*.sh deploy/examples/*/*.sh tools/*.sh
```

CI (`.github/workflows/ci.yml`) runs all of these plus `go test -race` and `go mod tidy -diff`.
Run them before saying a change is done.

## Working with the maintainer

The maintainer comes from C#/.NET and is learning Go. When introducing a Go idiom that differs
from C# (interfaces satisfied implicitly, error returns instead of exceptions, goroutines and
channels, `new(expr)` for pointer fields), add a one-line explanation in your summary. Prefer
clear, plain Go over clever Go.
