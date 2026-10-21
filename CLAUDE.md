# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What we're building

Caravana: an open source, self-hosted PaaS (Heroku-style) on **HashiCorp Nomad**
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
gives the in-process Caravana a token made from `deploy/install/caravana-policy.hcl` (applied on every
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

Feature 017 is implemented on branch `017-private-registries` (stacked on 016): private images
deploy. `internal/registry` takes any registry host (`Parse` reads `host:port`, lowercases the
host and turns `index.docker.io` into `docker.io`; `Endpoint` is `https://<host>`, or plain
`http://` for loopback hosts only) and a `Client.Credentials` hook: a Bearer challenge sends the
token request with Basic auth, a Basic challenge retries with it, and `ErrUnauthorized` means the
registry refused credentials it was given. `Client.Test` runs the same flow against `/v2/`.
Credentials never appear in an error, a log or a URL. Other facts:
- The secret lives in the Nomad Variable `caravana/registries/<key>` (items `username`,
  `password`; the key writes `.` as `~` and `:` as `_`, since Nomad paths allow neither). SQLite
  holds only the host, the user name and the last test (migration `0009_registries.sql`), as the
  backup destination does.
- A service whose image is on a credentialed host gets a per-task copy of the credentials,
  `nomad/jobs/<app>/<svc>/<svc>` (items `registry`, `CARAVANA_REGISTRY_USERNAME`,
  `CARAVANA_REGISTRY_PASSWORD`), written by `ensureRegistryVariables` just before `Register`. The
  docker `auth` block interpolates the two names, and a `noop` template renders the copy into
  `secrets/registry.env` (`env = true`). `Translate` gets the credentialed hosts as an argument and
  stays pure. The job holds placeholders only, so no job, export, record or log has the password.
  The Postgres backup and restore jobs get the same `auth` and items.
- The trade-off, accepted and documented: `env = true` puts both values in the app's own container
  environment, so the console asks for a read-only token. Nomad's docker driver has no other
  per-job way, and a node-level credential helper waits for Phase 3's agent on each node (backlog).
- `SyncRegistryCopies(ctx, host)` runs after every save or removal: it rewrites each per-task copy
  (and backup and restore variable) whose registry is the host, with the new credentials or with
  the credential items emptied (Nomad refuses a variable with every item empty, so `registry`
  stays). It registers no job and restarts nothing; the new values reach a copy when its task next
  starts. A sync failure answers "saved, but apps already deployed still have the old credentials".
- A registry that turns the stored credentials down is `deploy.ErrRegistryAuth`, an `*ImageError`
  with the field, mapped to 422 `registry_auth_failed`. A missing image without credentials says
  "add credentials for <host> in Settings › Registries". The failed deploy in the console has one
  action, "Check the registry's credentials". The webhook and `RedeployVersion` give the same errors.
- API 0.14.0: `listRegistries`, `putRegistry` (a PUT without `password` keeps the secret),
  `deleteRegistry`, `testRegistry`, `Registry` schema (no password property). Console: Settings ›
  Registries (add, edit, Test, remove; forms work without JavaScript).
- The gate has a step `private-registry` (a `registry:2` job with htpasswd on `127.0.0.1:5000` and
  a skopeo copy of busybox into it: no credentials, wrong, right, then no-secret checks and
  removal). A test searches for a sentinel password in every page, answer, error, job, export,
  record and request log.

Feature 018 is implemented on branch `018-notifications` (stacked on 017): Caravana pushes what
the needs-you list derives, and how deploys end, to the owner. `internal/notify` is the pure
package: `Event`, `Channel`, `Render` (a ready `*http.Request` for `webhook`, `slack`, `discord`
and `ntfy`, with the exact bodies and headers of the spec's research R4), `Sign`/`Verify` for
webhooks (`Caravana-Signature: t=<unix>,v1=<hex>`, HMAC-SHA256 over `<t>.<body>`) and `Send` (10 s
timeout, 2xx is ok, and no error ever holds the request URL, since a Slack or Discord URL is a
secret). Other facts:
- Migration `0010_notifications.sql`: `notification_channels`, `sent_problems` and
  `notification_deliveries`. Deleting an app deletes its `sent_problems` rows; deliveries stay as
  history, 20 per channel (pending rows are never pruned).
- The notifier (`service/notifier.go`) checks `Service.Attention` every 30 s. The first check
  comes one gap after start (a timer alone, since `Subscribe` would keep the copies watcher
  running). The pure `diffProblems` compares the list with `sent_problems`: a new item is opened,
  a present one is seen, an item gone for `resolveAfter` since its last sighting is resolved, a
  younger one is kept (so a flapping problem sends one "opened"), the copy kinds are neither
  opened nor resolved while Nomad can't be read, and an app that no longer exists is dropped
  without "resolved". The key is `kind|app|service|deployment`, never the copy IDs.
  `store.ApplyProblemCheck` writes the table and the queue in one transaction. More than 10
  opened (or resolved) in one check become one summary per channel.
- Deploy hooks: `notifyDeploy(rec)` is called after the record write where a deployment ends
  (`finish` for the rollout, `fail` before register, and when it is superseded before it
  registers). It queues `deploy.succeeded` or `deploy.failed` and never blocks: an enqueue error
  is logged.
- The delivery queue and sender (`service/sender.go`): a loop woken by a 5 s timer, a `poke`
  after each enqueue and each finished worker takes due pending rows (20) and starts one worker
  goroutine per channel with something due, up to 4, so a slow receiver can't delay another
  channel. A failed send is retried after 30 s, then 2 min, then marked failed with the error
  kept. A removed or paused channel's pending rows are marked failed without sending. Pending rows
  survive a restart.
- Secrets of a channel live in the Nomad Variable `caravana/notifications/<id>` (`url` for Slack
  and Discord, `token` for ntfy, `signing_secret` for a webhook), written by
  `deploy.SaveNotificationSecret` and removed with the channel; SQLite holds the masked address
  only. A webhook's signing secret is 32 random bytes in hex, shown once on create and on rotate.
  No page, answer, error, log or delivery holds a secret (tests search for sentinels). The
  policy's `caravana/*` variables already cover it.
- Settings: `CARAVANA_PUBLIC_URL` (`-public-url`, optional, for the links in messages; the
  installer writes it from the console domain, so rerun it on an existing host) and
  `CARAVANA_NOTIFY_RESOLVE_AFTER` (`-notify-resolve-after`, default 5m, at least 10s), both set
  through `Service` setters from `cmd/caravana/main.go`.
- API 0.15.0: `listNotificationChannels`, `createNotificationChannel` (`signingSecret` in the
  answer once, for a webhook), `updateNotificationChannel`, `deleteNotificationChannel`,
  `testNotificationChannel`, `listNotificationDeliveries`, `rotateNotificationSecret` (409
  `not_a_webhook`), and the webhook message documented as a component for receivers. Console:
  Settings › Notifications (add, edit, Send a test, Pause or Resume, Remove, recent deliveries,
  the signing secret notice; forms work without JavaScript).
- `TestConsoleLab` has a step `console-notifications`: a receiver in the test process, a webhook
  channel added through the form, a test whose signature `notify.Verify` accepts, then a deploy
  of a tag that doesn't exist (`deploy.failed` and one `problem.opened`), a fixed deploy
  (`deploy.succeeded`, `problem.resolved`) and the channel removed. `startLab` sets
  `CARAVANA_NOTIFY_RESOLVE_AFTER=10s`; the notifier's 30 s gap is not configurable, so the step
  budgets for it and takes about two minutes more.

Feature 019 is implemented on branch `019-projects-environments` (stacked on 018): apps live in a
project and an environment (Default › production holds every app from before, with its ID).
- `app.Place{Project, Environment, Name}` and the pure `app.PlaceOf(id)` / `Place.ID()`: the ID is
  the bare name in Default › production and `<project>--<environment>--<name>` elsewhere. Names
  are lowercase labels without `--` (`app.ValidName`), so the parts read back and the two forms
  never collide. Every Nomad-facing name (job, router `<app>-<service>`, variables, volumes,
  backup jobs and objects, webhook path) still derives from the ID, so nothing existing moved and
  constitution VI needed no change. The 63-character rule on `<id>-<service>` still applies; New
  app keeps its 31-character name limit.
- Migration `0011_projects.sql`: `projects` and `environments` tables (Default and its
  production inserted), and `project`, `environment`, `app_name` columns on `apps` (filled from
  the ID, `UNIQUE(project, environment, app_name)`), beside the app document and not in it, so no
  `revision` changed and `Translate` is untouched. An old ID that `PlaceOf` refuses (it had a
  `--`) stays in Default › production under its whole ID and keeps deploying.
- `checkPlace` (service) runs before any path that creates an app (`CreateApp`, the first
  `Deploy` or `DeployCompose` of an ID, the console's New app, `createEnvironmentApp`, `CopyApp`):
  an unreadable ID is a field error on `id`; a project or environment that doesn't exist is a
  `*PlaceError` (404 `project_not_found` / `environment_not_found`, field `id`), so a typo never
  creates an app nobody can see. A client computes the ID and uses `deployApp` as before.
- One Nomad namespace, `default`, for everything (research R1, lab 2026-10-17): Traefik's Nomad
  provider watches `default` only; `namespaces=*` broke routing for every app, and a list needs
  Traefik redeployed (a gap on 80/443 and its ACME state) whenever an environment is added, plus a
  policy per namespace. Secrets are already isolated per job ID. Revisit with teams (Phase 4).
- `Service.CopyApp(appID, to)`: the stored app with domains removed (and its kept Compose file's
  overlay domains), inserted without a deploy; secret values were never stored, so the answer
  lists `missingSecrets` and a deploy is refused until they have values. No webhook token,
  backup or volume contents. `Service.PreviewPromotion` and `Promote(appID, to, cond, trigger)`
  share `planPromotion`: the source's newest deployment must be healthy with a recorded
  definition; the target (same name, another environment of the project) keeps env, secrets,
  domains, replicas, resources and backup and takes the source's image, command, args, ports,
  health and volumes (`promoted`, pure); a source-only service is added whole without domains; a
  target-only service refuses. It deploys the source's digests pinned (`CheckDigests`, as
  `RedeployVersion`), with the target's revision as the condition, and records the source
  deployment (`SourceApp`/`SourceEnvironment` on the record). A target with a kept Compose file
  loses it with the existing warning.
- `service.RefusalError{Field, Err}` carries the sentinel of a promote or copy refusal
  (`ErrAppNotInEnvironment`, `ErrPromotionRemovesService`, `ErrAppExists`) with the field at fault
  (`to`, `services.<name>`); `service.PlaceError{Field, Err}` does the same for a missing project
  or environment. Both are mapped in `internal/api`; `store.NotEmptyError` names the apps that
  block a delete. Default and its production can't be deleted (`ErrProtected`).
- API 0.16.0: the project and environment operations, `previewPromotion`, `promoteApp` (`If-Match`
  is the target's revision), `copyApp`, and `project`/`environment`/`appName` on apps,
  summaries, attention items, deployments and the webhook notification; `Deployment.promotedFrom`
  (`source` stays for a redeploy of the same app's version). See `specs/019-projects-environments/contracts/api.md`.
- Console (`internal/ui/projects.go`, `promote.go`, `places.go`): `/projects/new`,
  `/projects/{project}` (display name, environments, add and delete, delete project by typing its
  name), `POST /projects`, `/projects/{project}/environments[/{environment}/delete]`; New app and
  the Compose tab have a `place` select (`<project>/<environment>`) shown only when there is
  more than one environment; app pages have a breadcrumb and an Environments line (Copy to /
  Promote to); `GET /apps/{id}/promote?to=` previews and `POST` confirms with the preview's
  `revision` (412 draws the preview again); the Apps home groups by project and environment, as
  before while only Default › production exists. Forms work without JavaScript.
- Lab: the gate step `projects-promote` (`cmd/caravana/gate_projects_test.go`: project `lab019`,
  staging and production deployed by ID, copy, promote with a stale and a right `If-Match`, an
  added service, the `promotion_removes_service` refusal, `not_empty` deletes; its domains are
  reached with the host name as TLS server name, through 127.0.0.1:8443 in the lab and the host's
  IP on 443 on a real host, no DNS needed) and the console step `console-projects`
  (`console_projects_test.go`: the same through the forms, promoting into a copy never deployed).
  Both clean up with `t.Cleanup`.

Feature 019.5 (`specs/019.5-create-without-deploy`, added outside the plan's numbering so 020
stays Build from source) is implemented on branch `019.5-create-without-deploy` (stacked on 019): an app can be
created without deploying it, with a definition or with only a name.
- `Service.CreateAppOnly(ctx, app.App)` (`service/apps.go`): no deployment, no Nomad call.
  A definition goes through `deployer.Validate` (not the secret or backup checks, which the first
  deploy makes); no services means a stored app with `"services": []` and `source.kind` image
  (`checkPlace` still reads the ID). A secret with a value is a `*app.FieldError` on
  `services[i].secrets[j].value`: names are kept, values only ever travel with a deploy. An
  existing ID is `ErrAppExists`. No migration, `nomadjob` untouched, no new module.
- `app.App.Defined()` is `len(Services) > 0` (a method, not a field). `ErrAppNotDefined` is
  returned by `Redeploy` (so the webhook, Deploy now and an empty-body deploy) and
  `RedeployVersion` before anything is validated or recorded, so an app with zero services never
  reaches `Translate` or `Register`; `Deploy`/`DeployIf`/Compose with a definition work as for a
  new app. A defined app never deployed deploys as stored on its first webhook call
  (`WebhookRedeploy`'s unchanged check finds no healthy deployment). `CopyApp` of an undefined app
  copies the name and skips `Validate`. `statusOf` no longer says healthy when zero copies are
  wanted; Attention needs a deployment, so a never-deployed app is never an item.
- API 0.17.0: `createApp` and `createEnvironmentApp` take `deploy` (query, default true); with
  `false` the answer is 201 with the `AppRecord` (`ETag`, `Location: /v1/apps/{id}`) and the body
  is `NewApp` (only `id`, or `appName`, is needed); 409 `app_not_defined` on `deployApp` (empty
  body), `redeployDeployment` and `callWebhook`; `defined` on `AppRecord`, `AppSummary` and
  `CopyResult`. `App.services` no longer says `minItems: 1` in the schema (a read app may have
  none); a deploy still refuses an empty list.
- Console: New app's two submit buttons are `name="action"` (`deploy`, the default, and `create`),
  so both work without script; the image is optional for `create`, and a port or domain without
  an image is refused rather than dropped; `create` goes to `/apps/{id}?created=1` (the notice is
  only drawn while the app has no deployment). The Running tab's empty state is `firstDeploy`
  (Deploy now, Create a webhook token, the curl line; for an undefined app Configure, Deploy from
  Compose and the curl line with a body). Configure of an app with no services shows
  "Define the app" (`defineApp`, `POST /apps/{id}/define`: New app's fields, `DeployIf` with the
  page's revision, the service named like the app's name) instead of the per-service form, and
  its Secrets section says to define the app first. The list shows `never deployed` / `not
  defined yet`, and the Apps headline counts such apps as "waiting for a first deploy".
- `TestConsoleLab` has a step `console-create-only` (`console_create_only_test.go`): a name-only
  app (no job, webhook 409), defined through Configure's form and healthy, then a defined app
  created with `deploy=false` that its webhook deploys. It uses a worker with small reservations
  for the second app, since the form's service takes the default memory.

Feature 020.5 (`specs/020.5-app-actions`, Phase 2.6's first piece) is implemented on branch
`020.5-app-actions` (stacked on 019.5): Run a command in a running copy, and Stop, Start and
Restart per app.
- Run a command: `Service.RunCommand` takes the program and its arguments (1 to 64 items, each at
  most 4 KiB, no NUL), runs them with Nomad's alloc exec (`deploy.Exec`; no shell, no TTY, no
  stdin) in the copy's service task, 60 s timeout, each of stdout and stderr cut at 64 KiB, and
  returns a `CommandResult` (exit code, the two streams, `TimedOut`, `FailedToStart` with Nomad's
  reason in `Stderr`). The output exists only in that return value: it is never logged, stored,
  put in an error, a notice, a live event, a URL or a cache (research R2). Every run leaves an
  `app_actions` row with the first word of the command, the exit code or a reason in Caravana's
  words ("timed out after 1m0s", "failed to start"), the copy, the service and the trigger,
  never the arguments or the output. Tests search every store row, log line and page for a
  sentinel the command prints (`s3cret-sentinel-0205…`).
- Stop is `Deregister` without purge for the app's job and its backup periodic jobs
  (`deploy.StopApp`); Start reads the stopped job and registers it again with `Stop=false`
  (`deploy.StartApp`), so the same job and the same digests run with no tag lookup. `apps.stopped_at`
  (migration `0012_app_actions.sql`, with the `app_actions` table) is the owner's decision, so the
  app's `revision` doesn't move. `statusOfRecord` makes the state `stopped` over every other state,
  Attention ignores a stopped app, the scheduler skips its volume backups, and `ErrAppStopped` (409
  `app_stopped`) refuses every deploy path (`saveAndDeploy`, `Redeploy`, `RedeployVersion`,
  `Promote` into it, `BackUpNow`, `Restore`, and so the webhook). Stop, Start and Restart take the
  app's deploy lock with `TryLock`, so a deploy or a volume run in progress is `ErrAppBusy` (409
  `app_busy`), not a wait. Restart answers at once with a `running` action and a goroutine (lock held)
  restarts the live version's copies one at a time (`RestartCopy`, then `WaitCopyRestarted`: running
  again and passing its checks when the service has a health check, 2 minutes each); an interrupted
  one is settled as failed at the next start (`FailRunningActions`). `Copy.RequestedRestarts`
  keeps these restarts off the crash-loop rule.
- API 0.18.0: `runCommand` (`Cache-Control: no-store`), `stopApp`, `startApp`, `restartApp` (202),
  `listAppActions` (last 20), `AppStatus.state` `stopped`, `stoppedAt` on `AppRecord` and
  `AppSummary`, codes `app_stopped`, `app_not_stopped`, `app_busy`, `copy_not_running`. The policy
  `deploy/install/caravana-policy.hcl` gained `alloc-exec` (rerun the installer; without it the
  form says Nomad refused and to run the installer again).
- Console (`internal/ui/actions.go`): the app header has Restart (post), "Stop…" (a link to the
  confirmation page `GET /apps/{id}/stop`, then post) and, for a stopped app, Start; each answers
  303 to `/apps/{id}?did=stop|start|restart`, whose notice is only drawn while it is still true,
  and a refusal draws the Running tab again with the reason and its status (409, 502), never a
  redirect. The Running tab has a "Run a command" section outside the live regions (so a refresh
  never replaces what is being typed or the result), a form per live copy; `splitCommand` reads the
  line like a shell's words (quotes, backslash; no pipes, variables or globs: use `sh -c '...'`);
  `POST /apps/{id}/copies/{copyId}/run` answers 200 with the tab and the result panel (exit code,
  stdout and stderr in `<pre>`, cut and timed-out notes, "This output is shown once; it isn't
  kept."). The "Recent actions" list is its own live region (`/parts/apps/{id}/actions`, refreshed on
  the `actions` notice, which the app's stream now forwards) and shows the action, the program, the
  exit code or outcome, the trigger and when, never output or arguments. A stopped app is the idle
  ring "stopped" in lists and the header, the Apps headline counts it ("1 app stopped") and not
  as trouble, and the deploy form, Configure, secrets, backups and promotion say "The app is
  stopped: start it first." (the header's Start is the way out).
- The gate has a step `app-actions` (`gate_actions_test.go`: a two-copy busybox app with a domain;
  echo a sentinel, `sh -c 'exit 3'`, restart while the domain is called every 200 ms, stop, start,
  same job shape, delete) and `TestConsoleLab` a step `console-app-actions`
  (`console_actions_test.go`). Neither had been run in the lab when this was written.

Feature 020 (`specs/020-deploy-from-ci`, Phase 2.5's CI piece) is implemented on branch
`020-deploy-from-ci` (stacked on 020.5): deploy tokens, the origin of a deploy, and New app › "From
a GitHub repository". CI builds the image; the GitHub Action `Cosmic-Chimps/caravana-action`
(its own repo, cloned beside this one at `../caravana-action`, a composite action over
`caravana-deploy.sh` with curl and jq) deploys it through the public API.
- Deploy tokens: an API token with a scope, one app name in one project (migration
  `0013_deploy_from_ci.sql`: `api_tokens.scope_project`/`scope_app`, `deployments.origin`).
  `Auth.CreateScopedAPIToken` (the project must exist, the app is `app.ValidName` and needn't exist
  yet); `Auth.CheckAPIToken` now returns the `*TokenScope` (nil for a full token; a session has
  none). The API checks it in `authenticate` (`internal/api/scope.go`) after the 401 and before any
  handler: DEFAULT DENY, `scopeRules` lists the eleven operations a deploy token may use, each with
  how to find the app (path `{id}`, the body's `id`, path project plus the body's `appName` or the
  path's `{name}`); anything else, or another app, is 403 `token_scope` with "this token can only
  deploy app web of project shop". The body is read once and put back for the handler, a body that
  can't be read counts as out of scope. `TestScopedTokenWalksEveryRoute` tries every route of the
  table with a scoped token and checks it against `allowedForDeployTokens` (written out in the
  test, apart from `scopeRules`, so allowing a route takes two edits). Tokens are still SHA-256
  hashed and shown once.
- Origin: `app.Origin{Commit, Ref, Actor, Run, Repository}` with `Validate` (commit 7-64 hex, run
  `https://` only, the rest printable, each with its limit) and `Deployment.Origin`.
  `deployApp`, `deployCompose`, `promoteApp` and the webhook read the optional `Caravana-Origin-*`
  headers (`internal/api/origin.go`; 422 `invalid_field` naming the header, none required, the
  webhook reads them after its token) and put the origin in the request context with
  `service.WithOrigin`; `deployWarned` records it on the deployment, so every entry point shares
  it without a new argument. `notify.Event.Origin` carries it (the webhook JSON has the object, Slack,
  Discord and ntfy get a "From CI: abc1234 on main by ana, run <url>" line).
- API 0.19.0: `scope` on `createToken`/`listTokens` (schema `TokenScope`), `Deployment.origin`
  (schema `Origin`), the origin header parameters, response `TokenScope`, code `token_scope`.
- Console: Settings › API tokens has "Only for one app" (a project select and an app name; the new
  token's example reads that app); Configure has "Create a deploy token for this app" (answered
  200 with the token, like the webhook's); each history entry made with an origin says "from CI:
  `abc1234` on `main` by ana · run" (the run a link). New app has a third tab, "From a GitHub
  repository" (`/apps/new?from=github`, `githubapp.go`): name, place, an image on `ghcr.io`, port,
  domain, health path; `POST /apps/new/github` creates the app without deploying
  (`CreateAppOnly`), makes a token named `<app id>-ci` (a number is added when an active token has
  the name) scoped to the app's project and name, and answers 200 `no-store` with the token (once),
  the secret to add (`CARAVANA_TOKEN`) and the workflow to commit, with the app, the console
  address (`CARAVANA_PUBLIC_URL`, else the request's) and the image filled in. `Service.PublicURL`
  was added for it.
- The gate has a step `deploy-from-ci` (`gate_deploy_ci_test.go`): it runs the action's script from
  `$CARAVANA_ACTION_DIR` (default `../caravana-action` beside the repository, else the step skips
  with a note) with a deploy token for `lab020`/`web`: define and deploy, a new image, create
  production with the token and promote, a missing image, the origin on each deployment, no token
  in any log or summary, and 403 `token_scope` for everything else. It had not been run in the lab
  when this was written. Running the script against the in-process API found that it must send
  `If-Match` quoted (`"<revision>"`), as `getApp`'s ETag has it.

Feature 020.6 (`specs/020.6-terminal`, Phase 2.6) is implemented on branch `020.6-terminal`
(stacked on 020): an interactive shell in a running copy, in the console and over the API.
- The endpoint is `openTerminal`, `GET /v1/apps/{id}/copies/{copyId}/terminal?shell=auto|bash|sh&cols&rows`
  upgraded to a websocket (`internal/api/terminal.go`, `github.com/gorilla/websocket`, already a
  dependency since 020.5's deploytest, now in production code). Browser to Caravana: text frames
  `{"type":"input","data"}` and `{"type":"resize","cols","rows"}` (64 KiB at most, anything else
  closes with 1003); Caravana to browser: binary frames of the shell's output, then one text frame
  `{"type":"exit","code":N}` or `{"type":"exit","reason":"idle|limit|error|gone","message"}` and a
  normal close. Gorilla can't read on after a read timeout, so a client reads in one goroutine for
  the whole connection (the lab's `termClient` does).
- gorilla's upgrader needs `http.Hijacker`: `httpx`'s request-log recorder gained `Hijack()` (it
  logs 101). The `http.Server` deadlines set before the handler are cleared after the upgrade
  (`NetConn().SetDeadline(time.Time{})`); the session's own idle (15 min), limit (60 min) and
  Caravana's ping (30 s, pong within 60 s) end it. The credentials are checked again every 30 s
  (`credentialCheck`, the same as the copies stream's), so a revoked token or ended sign-in closes
  the terminal with `reason: "error"`.
- Who may open one: a session cookie whose `Origin` equals the console's own (scheme as the request
  arrived after trusted proxies, and Host; a missing or foreign Origin is 403 `cross_origin`, since
  `http.CrossOriginProtection` doesn't cover GET) or a full API token (no Origin check; no browser
  sends one with a token). A deploy token is 403 `token_scope` by the scope middleware's default
  deny. A sixth open terminal is 429 `too_many_terminals`. The trigger recorded is `console` for a
  cookie and `manual` for a token.
- `service.OpenTerminal` checks what `RunCommand` does (app, copy, stopped, `Stopping`), the limit of
  5, records an `app_actions` row `terminal` (migration `0014_terminal.sql` recreated the table
  with the wider CHECK; `program` is the shell asked for, `exit_code`, `error` in fixed words
  "idle for 15 minutes", "open for 60 minutes", "connection lost", "closed") and runs
  `deploy.OpenTerminal` (`Allocations().Exec` with a TTY) in the service's task. Input and output
  are never logged, stored or put in an error (tests search rows, the SQLite file and logs for
  `s3cret-sentinel-0206…`). The exit message "no shell found in this image" is for exit 126/127
  before anything was typed (an exec that can't find the program).
- Deviations from the contract: Nomad's exec can't set the environment, so `auto` exports
  `TERM=xterm-256color` inside its `sh -c` (bash and sh take the runtime's own); the exit frame
  carries `message` for the end reasons and for "no shell found", which the contract's first draft
  had for reasons only.
- The page is `GET /apps/{id}/copies/{copyId}/terminal` (`internal/ui/terminal.go`, `terminal.html`,
  `static/js/terminal.js`): shell picker, New session, Close. xterm.js 6.0.0, its stylesheet and
  `addon-fit` 0.11.0 are vendored (`tools/ui-vendor.versions`, `static/js/LICENSES.md`, checked by
  `tools/ui-vendor.sh check`). xterm makes `<style>` elements, so this one page has its own CSP,
  `style-src 'self' 'unsafe-inline'` and `connect-src 'self'` added to the console's policy
  (scripts stay `'self'`; no inline script, the page passes what the script needs in `data-`
  attributes). The Running tab has a Terminal link per running copy; without JavaScript the page
  says to use Run a command. API 0.20.0.
- The gate has a step `terminal` (`gate_terminal_test.go`: a busybox app, the gate's API token over
  a websocket, `echo $((6*7))`, resize then `stty size`, a sentinel, `exit 3`, the recorded action
  and no sentinel in any later answer; refusals for no credentials, a deploy token and `shell=zsh`)
  and `TestConsoleLab` a step `console-terminal` (`console_terminal_test.go`: the page's CSP and
  assets, the socket with the cookie and the console's Origin, a foreign Origin and no Origin both
  403). Neither had been run in the lab when this was written.

Feature 026 (`specs/026-publish-to-caravana`, Phase 2.75) has its Caravana side implemented on
branch `026-publish-to-caravana`; the publisher is its own repo, `Cosmic-Chimps/aspire-hosting-caravana`
(sibling `../aspire-hosting-caravana`, in progress), which sends Aspire's Compose output plus an
overlay to `POST /v1/apps/{id}/compose`. What real Aspire 13.6.1 output (the fixture in
`specs/026-publish-to-caravana/fixtures/`) needed here:
- `internal/compose` accepts `networks.*.driver` = `bridge` and `volumes.*.driver` = `local`, the
  defaults Aspire writes (`checkDrivers` in `fields.go`; any other driver is still refused, naming
  networks for a network and volumes for a volume). A file with networks gets one warning, on
  `networks`, not one per service.
- `app.Service.Entrypoint []string` (`entrypoint`, omitempty; validation: no empty item, at most 64
  items of 4096 bytes, no NUL): Compose's `services.*.entrypoint` (list, or the string split like a
  shell by compose-go) maps to it, `Translate` sets the docker driver's `entrypoint`, the HCL export
  writes it (golden `entrypoint`), Configure keeps it (`applyEdit` leaves what the form doesn't offer)
  and shows it read-only, and a promotion takes the source's (`"entrypoint"` in `Changed`). API
  0.21.0.
- Fixtures in `internal/compose/testdata/aspire/` (`aspire_test.go`): `generated.compose.yaml` is the
  file as Aspire writes it and fails naming `APISERVICE_IMAGE`; `resolved.compose.yaml` is what the
  publisher sends (variables resolved, dashboard dropped, volume renamed `seq-data`, secret values
  removed from the file and given in `resolved.overlay.json`) and becomes 4 services with a golden
  app (no secret value in it) and two warnings (networks, `depends_on`).
- Findings that need no Caravana change: Aspire's references are already `http://<service>:<port>`,
  which resolve by service name inside one app. The publisher resolves `${VAR}`, renames volumes
  (dots aren't allowed in a volume name) and moves values that contain a secret into the overlay.

Feature 026.5 (`specs/026.5-mcp-server`, Phase 2.75) is implemented on branch `026.5-mcp-server`:
`caravana mcp`, a Model Context Protocol server over stdio that runs where the agent is and calls
only the public API (no API change; openapi stays 0.21.0).
- `internal/mcpserver` is an API client only. A test fails if it imports `service`, `store`,
  `deploy` or Nomad. `github.com/modelcontextprotocol/go-sdk` v1.8.0 is a new direct dependency,
  the official SDK (plan R2), and `google/jsonschema-go` is now direct for the input schemas.
- Settings: `CARAVANA_URL` (https, or http for loopback only), and `CARAVANA_TOKEN` or
  `CARAVANA_TOKEN_FILE`. Exactly one token setting is allowed, there is never a flag for it, and
  its alphabet is checked. Redirects are not followed, each call has a 30 s timeout, and the
  token is redacted from every text.
- Tools: read tools always (`contracts/tools.md`), including `wait_for_deployment`, which polls
  every 5 s with progress. Action tools only with `--allow-actions`:
  - `deploy_image` reads the app, replaces one service's image in `record.app` and deploys with
    a quoted `If-Match`, retrying once on 412;
  - `promote_app` previews, then promotes with the preview's `targetRevision`.
  Never offered: delete, restore, Run a command, the terminal, tokens or secret-bearing settings,
  or a whole-definition deploy. No `Caravana-Origin-*` headers are sent.
- Each tool's answer is the API's body as it came; an API refusal is a tool error
  `Caravana refused: <code>: <message> (field …) (HTTP n)`. stderr gets one JSON line per call
  (tool, outcome, code, duration), never the token or an answer. A contract test checks every
  request against `api/openapi.yaml`, and the deploy body against the `App` schema.
- `tools/release.sh build` adds `caravana_<v>_darwin_{arm64,amd64}.tar.gz` (the binary alone) to
  each release. Setup for agents: `docs/mcp.md`.
- `TestConsoleLab` has a step `console-mcp` (`console_mcp_test.go`): read tools with a full token,
  then `deploy_image` and `wait_for_deployment` and `restart_app` with `--allow-actions`, a
  deploy token's `token_scope` refusals, a wrong token's 401, and no token in any stderr line or
  answer.

Product features go through Spec Kit (`/speckit-specify` → `/speckit-plan` → `/speckit-tasks`
→ `/speckit-implement`), one feature per git branch. Nomad runs in a Lima Linux VM
(`deploy/dev/`), because Nomad bridge networking needs Linux. From the Mac, apps are at
`https://<name>.localhost:8443`.

## Current phase: 2.5 — Running real apps day to day (single node)

Phases 1 and 2 are done (gates passed 2026-10-04 and 2026-10-11); 013–017 followed from running
real apps. Reordered with the maintainer on 2026-10-16. Scope right now, in the order of
`docs/plan.md`, nothing beyond it unless asked:
- 018 Notifications: push the "needs you" problems and deploy outcomes to the owner (signed
  webhooks, Slack/Discord, ntfy) (done, branch `018-notifications`)
- 019 Organization / Project / Environment: projects, environments with copy and promotion, app
  IDs `<project>--<environment>--<name>`, one Nomad namespace (done, branch
  `019-projects-environments`)
- 019.5 Create an app without deploying it: New app's "Create without deploying", `?deploy=false`,
  apps that have no definition yet (done, branch `019.5-create-without-deploy`)
- 020 Deploy from CI: CI builds the image, the GitHub Action `Cosmic-Chimps/caravana-action`
  (its own repo, cloned beside this one at `../caravana-action`) deploys, waits and promotes;
  per-app deploy tokens; New app › "From a GitHub repo". Building on the host moved to Phase 3.
  (Caravana's side done, branch `020-deploy-from-ci`.)

Gate to finish Phase 2.5: on the maintainer's host, an app built from a git repo deploys to a
staging environment, is promoted to production, and a failure during it reaches the owner as a
notification. Passed 2026-10-20 with the `lacalabaza` blog: staging → production by caravana-action,
a broken commit reverted on staging and reported to ntfy (see `docs/plan.md`).

After 020 comes Phase 2.6 (added 2026-10-18): the design handoff's Developer view, one feature
per screen (020.5 app actions and Run a command (done, branch `020.5-app-actions`), 020.6 terminal into a copy (done, branch `020.6-terminal`), 021 console IA, 022 Schedules, 023 Domains, 024
Logs, 025 Monitoring and capacity; also a sqlite backup engine and an "update available" notice),
ending with the API at 1.0, then Aspire in 2.75. From 2.6 on, `docs/plan.md` names each feature after the screen it builds
and ends Phase 4 with "The handoff, screen by screen".

Out of scope for now: multi-node, WireGuard, teams and roles, Aspire/Bella Baxter (Phase 2.75),
GPUs and binaries, Amazon ECR (backlog).

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
gh workflow run release.yml -f version=0.6.0   # release: renames Unreleased, commits, tags, builds, publishes
git tag v0.6.0 && git push origin v0.6.0        # also releases: the run renames Unreleased for the notes, then commits it to main if main is still the tag
tools/release.sh prepare 0.6.0     # what both run: "## Unreleased" becomes "## 0.6.0 - <today>"
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
