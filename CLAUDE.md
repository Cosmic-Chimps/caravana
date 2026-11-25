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
  comes one gap after start (a timer alone). The pure `diffProblems` compares the list with `sent_problems`: a new item is opened,
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
  (`console_actions_test.go`). Both passed in the lab (`TestLab` on 2026-10-18; the QA pass ran it green again at
  v0.12.0 on 2026-10-24: `app-actions` 42 s, `console-app-actions` 31 s).

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
  in any log or summary, and 403 `token_scope` for everything else. It passed in the lab
  (`TestLab` on 2026-10-18, 48 s; green again in the QA pass at v0.12.0 on 2026-10-24 with the action's
  `61426ef`). Running the script against the in-process API found that it must send
  `If-Match` quoted (`"<revision>"`), as `getApp`'s ETag has it.

Feature 020.6 (`specs/020.6-terminal`, Phase 2.6) is implemented on branch `020.6-terminal`
(stacked on 020): an interactive shell in a running copy, in the console and over the API.
- The endpoint is `openTerminal`, `GET /v1/apps/{id}/copies/{copyId}/terminal?shell=auto|bash|sh&cols&rows`
  upgraded to a websocket (`internal/api/terminal.go`, `github.com/gorilla/websocket`, already a
  dependency since 020.5's deploytest, now in production code). Browser to Caravana: text frames
  `{"type":"input","data"}` and `{"type":"resize","cols","rows"}` (64 KiB at most, anything else
  closes with 1003); Caravana to browser: binary frames of the shell's output, then one text frame
  `{"type":"exit","code":N}` or `{"type":"exit","reason":"idle|limit|error","message"}` (a lost connection has nobody to tell: no frame, recorded as "connection lost") and a
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
  403). Both passed in the lab (`TestLab` on 2026-10-19; green again in the QA pass at v0.12.0 on
  2026-10-24: `terminal` 18.5 s, `console-terminal` 16.5 s).

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

Feature 021 (`specs/021-console-ia`, Phase 2.6) is implemented on branch `021-console-ia`: the console
in the design handoff's shape. It was built page by page; each page has a contract written by the
designer session (`contracts/pageN-*.md`, with its rulings) and was approved after a live review, an
impeccable critique (critique only, no detector, which gave only false positives on Go templates) and a
last look.
- Page 1: the shell, `/projects` and the app header (`AppStatus.services`, API 0.22.0).
- Page 2: the short Configure, plus Variables, Domains and Backups.
- Page 3: Settings with a grouped nav (`/settings/registries`, `backup-storage`, `notifications`, `tokens`).
- Page 4: the Apps home, with "Deploying now", the filter, kind words and "by <who>", and the stages strip
  in each history entry, including "(v0 again)".
- Page 4 records `deployments.token_name` (`Deployment.tokenName`, API 0.23.0) and `registered_at`
  (UI only), migrations 0015 and 0016.
- One set of who-words (`deployWho`) serves the rail, the entries and the lede. The console has one
  32px select.
- `TestConsoleLab` gained `console-domains` and `console-backup-daily`; `TestNoShipWords` keeps "ship"
  off every page.
- Ruling 12 (2026-11-04, `page2-app-pages.md` "Ruling 12: each domain once"): the Compose form of an
  app that exists has no Domains input. `composeDraft.keepDomains` carries each service's domains (the
  kept overlay's as written, else the app's) with their options, so a deploy from the form never drops
  a path, www redirect or Team only; domains are edited on the Domains page. Configure lists each domain
  once (the Domains panel, with Add domain for a kept-file app too). New app › Compose keeps the box.

Feature 022 (`specs/022-schedules`, Phase 2.6) is implemented on branch `022-schedules`: a
command on a timer in a fresh copy of one service of an app.
- A schedule (`schedules`, migration `0017_schedules.sql`) names an app, a service without
  volumes, a command (program and arguments, no shell), a five-field cron line in UTC
  (`internal/cronwords`: words ⇄ cron, `Validate`, `Next`), a stop-after limit, the run's
  `memoryMB` and `skipIfRunning`. Caravana keeps the clock (`service/scheduleclock.go`, the
  volume-backup `dueSlot` generalised with `armed_at`; every 10 s, `SetScheduleScanGap`) and
  registers **one one-off batch job per run** (`nomadjob.ScheduleRunJob`, built by
  `deploy.ScheduleRunJob` from the newest healthy deployment's digest; job ID
  `<app>_run-<runId>`; no Services, no ports; the `names` sidecar in multi-service apps). The
  run's secrets and registry credentials are a copy in the job's own variable
  `nomad/jobs/<jobId>`, written before `Register` and deleted at settle; a job can read only its
  own path (research R1), so a run can't read the app's. A slot whose app is stopped, undefined,
  without a healthy deployment or mid-rollout waits; one refused by skip-if-running or the cap of
  3 is a `skipped` row.
- Runs (`schedule_runs`) are written ahead as `starting`, flipped to `running`, and settled by a
  stateless watcher every 5 s (`service/schedulewatch.go`): the stop intent first (`owner`,
  `limit`, `app_stopped`, `schedule_removed` are written before the deregister), then out of
  memory, exit 0 (`ok`), a start failure (`could_not_start`), a stop from outside Caravana, exit n
  (`failed`), a lost job after a restart. Stop is `Jobs().Deregister` without purge (never
  `Allocations().Stop`, which replaces a batch allocation); the job is purged with its variable
  after the output is read. Output is the last 64 KiB of each stream (`deploy.ScheduleRunOutput`,
  `Logs` with a cut flag), masked of the service's secret values of 6+ bytes, kept for the last
  100 runs, returned only by the run's own read (`no-store`) and never in a list, item, event,
  notice or log line (tests search for sentinels). Stop app stops the app first, then its runs.
- Attention kinds `schedule_failed` (the newest ended run that isn't skipped or stopped failed)
  and `schedule_skipping` (3 skipped slots in a row, a warning), key `kind|app|service|scheduleID`;
  the notification stays `problem.opened/resolved` with `schedule` and `exitCode`. Backups are
  listed as built-in schedules `backup:<app>:<service>` (read-only; a stored schedule can't take
  the name `<service>-backup`).
- Time (the maintainer, 2026-10-23: "we should show UTC and localtime"): no timezone setting;
  every clock time is `<time datetime="…Z" class="cv-clock">14:15 UTC</time>` (`clockOf` in
  `ui/view.go`) and `static/js/localtime.js` (ours, listed in `tools/ui-vendor.versions` as
  `ours` with its checksum) appends "· 16:15 local" in the browser.
- API 0.24.0: the twelve schedule operations (`contracts/api.md`), `Caravana-Output-Cut` on the
  output read, codes `builtin_schedule`, `schedule_exists`, `run_going`, `run_not_running`,
  `output_not_kept`; deploy tokens denied by the default deny. Console: `/schedules` (list, the
  row's detail, filter, run page), `/schedules/new` and `/schedules/{id}/edit` (words with preset
  links and a Check preview), the sidebar's Schedules entry, a Schedules panel on Configure.
  `TestConsoleLab` has a step `console-schedules` and the gate a step `schedules` (with
  `RestartCaravana` during a run).

Feature 023 (`specs/023-domains`, Phase 2.6) is implemented on branch `023-domains` (stacked on 022):
every address with its state, per-domain options, Add with checks, and certificates.
- Model: `app.Domain` gained `path`, `wwwRedirect` and `teamOnly` (`omitempty`, so no existing
  `revision` moved; `validateDomains` names `services[i].domains[j].<field>`; `compose.Domain` has the
  same, and a kept-file app's Domains page edits the kept overlay and deploys with `If-Match`). A
  domain with any option gets its own router `<app>-<service>-<hash8(host)>`
  (`nomadjob.DomainRouterName`; constitution 1.0.1), a plain domain stays on `<app>-<service>`.
  `routerTags`/`optionRouterTags` write the tags: `Host(...) && PathPrefix(...)`, a `redirectregex`
  middleware `<router>-www` (the regex tolerates a port in Host: `(:[0-9]+)?`; `$1`/`$2` and `[.]`
  because Nomad interpolates `${...}`; 301 for GET, 308 otherwise), and `<router>-auth@file` for Team
  only. Force HTTPS is the installer's global `web` redirect, shown on and not switchable (research R4).
- `internal/edge` is the pure seam: `Resolver`, `Dialer`, `Check` (DNS from the system resolver plus
  `1.1.1.1` and `8.8.8.8`, 2 s each; the certificate from a handshake with `CARAVANA_EDGE_ADDRESS`, SNI
  = the host, verified by hand), `StateOf` (precedence: `not_served`, `dns_elsewhere`, `cert_expiring`,
  `cert_pending`, `dns_none`, `self_signed`, `redirect`, `live`, `unknown`; but a valid certificate
  (`Certificate.Valid`: issued or expiring) overrides an elsewhere DNS, since it proves the issuer
  reached this host, so a domain behind Cloudflare's proxy is `live` and only `dns_elsewhere` while
  it has no valid certificate; `DNS.Proxy` is `Cloudflare` when every foreign address is in its
  hard-coded ranges, `edge.ProxyOf`, recomputed from the stored addresses); `edgetest` has the fakes.
  `*.localhost` answers loopback without DNS. `Service.SetEdge`, `SetEdgeAddress`, `SetACME`,
  `SetDomainWatchGaps(idle, watched)`.
- Store (migration `0018_domains.sql`): `domain_checks` (the cache the page and Attention read),
  `team_passwords` (host, app, bcrypt hash; never the password) and `certificate_settings` (`warn_before_days`
  14 by default; the issuer and contact are env, read-only). The watcher (`service/domainwatch.go`)
  checks every domain every 15 min and every 60 s while a Domains page's stream is open
  (`MarkDomainsWatched`), 8 probes at once; `CheckDomain` checks fresh. `ErrDomainTaken` (409
  `domain_taken`), `ErrNotTeamOnly` (409 `not_team_only`).
- Team only: the password (24 random bytes, base64url) is shown once, in the deploy answer
  (`Deployment.teamPasswords`) or the console's 200 page, and by `RotateTeamPassword`. The hash is
  `golang.org/x/crypto/bcrypt` (the module is new and direct; cost 10) in `team_passwords`;
  `deploy.SyncTeamPasswords` rewrites the Nomad Variable `nomad/jobs/traefik` (one item
  `<router>-auth` = `team:<hash>`, a marker item `CARAVANA_TEAM` when empty) before `Register`, after
  a rotation and after a removal. Traefik's job reads it with no policy line (a job reads its own
  `nomad/jobs/<job>`) through the file provider, `--providers.file.directory=/local/dynamic` with
  `watch=true`: the template `local/dynamic/team.yml` (`change_mode = "noop"`) renders `{}` unless an
  `-auth` item exists. Lab, observed: `nomadVar` yields the items map itself (range over `.`); a template
  that fails to render kills the task; an `http:` with nothing under it is refused by the watcher and
  the old middlewares stay. A router naming a missing `@file` middleware is disabled (404), so the variable
  is written first.
- Attention kinds `domain_dns_elsewhere` and `domain_cert_expiring` (key `kind|app|service|host`,
  read from `domain_checks`), and `notify.Event` gained `domain`, `addressSeen` and `expiresAt`.
- API 0.25.0: `listDomains`, `getDomain`, `checkDomain`, `rotateTeamPassword`,
  `getCertificateSettings`, `putCertificateSettings`, `listCertificates`, the domain options,
  `Deployment.teamPasswords`; deploy tokens are denied by the default deny (a scoped `deployApp` may
  turn Team only on for its own app). Console: `/domains` (rows with the state, options as switches,
  Save and deploy, Remove, New password, the Add panel with the record and Check now),
  `/parts/domains`, the app's Domains page on the same rows, the sidebar's Domains entry,
  Settings › Certificates, Configure's "Domains go to".
- Installer: `deploy/install/traefik.nomad.hcl` reads the directory; `install.sh` writes
  `CARAVANA_ACME`, `CARAVANA_ACME_EMAIL` and `CARAVANA_EDGE_ADDRESS=127.0.0.1:443` into Caravana's env,
  and a rerun against an older Traefik job says it restarts it once (rerun the installer on an existing
  host). `caravana-policy.hcl` is unchanged. `deploy/dev/traefik.nomad.hcl` is the same for the lab
  (`lab.sh up` re-registers it); `install-test.sh` checks the env lines and `team.yml` in Traefik's
  allocation.
- Lab: `startLab` gives the in-process Caravana `CARAVANA_EDGE_ADDRESS=127.0.0.1:8443`, fake DNS through
  `serviceHook` (`elsewhere.test` to `203.0.113.9`, the console's host to `203.0.113.1`, the rest
  NXDOMAIN) with the real TLS dialer, and watcher gaps of 60 s and 10 s. The `console-domains-states`
  step and the gate's `domains` step are listed in `specs/023-domains/tasks.md` (T022, T023).

Feature 024 (`specs/024-logs`, Phase 2.6) is implemented on branch `024-logs` (stacked on `023-domains`):
the output of every copy of every service of one project › environment on one page, read live from
Nomad and never stored. No migration, no new module, no policy change (`read-fs` since 016).
- Source (research R1): a page view lists each copy's `alloc/logs` files (`ls`) and reads them
  backwards with `readat` in 64 KiB chunks from the newest file into `n-1`, until the oldest timed line
  read is before the range's start, the stream's budget (1 MiB; the page's 16 MiB split evenly beyond
  16 streams) is spent, a deadline (5 s; `logReadDeadline`) passes, or no file is left. At most 2 page
  reads at once per Caravana (`logSem`, a semaphore), 8 Nomad calls at once inside one read. The
  coverage (the oldest time reached per stream, budget or deadline or end) goes with the answer: the
  page says "read from <time>" and "not all read in time". A ring buffer of lines was rejected
  (research R1): app output, and any secret in it, at rest in SQLite, and a follower on every copy
  forever. `getLogs?follow=true` stays the per-app live read; the environment has no follow in the API.
- `internal/logline` is pure (a test forbids network imports) and reads each line: `StripANSI`
  (CSI, OSC, DCS/SOS/PM/APC, C1, C0 but tab, DEL; the text after the last mid-line `\r`; Nomad strips
  nothing, so this is ours), `TimeOf(s, Bounds)` (JSON fields, klog, the start of the line or after
  one short token or inside a leading `[`, Go's `log`, Redis, syslog, the Common Log Format bracket
  after three words, `time=`/`ts=` in the first six logfmt pairs; no zone is UTC; a time outside the
  bounds, the copy's start minus a minute to the file's `ModTime`, is refused and the line is
  `estimated`; a date inside a message is never the line's time), plus the per-file skew check in
  the service (the file's last timed line against its `ModTime`, over a minute either way marks the
  whole file estimated: it catches a container in a west-of-UTC zone, which the bounds miss),
  `LevelOf(text, prev)` (order: JSON `level`/`lvl`/`severity`/`LogLevel`/`@l`/`log.level` with pino
  numbers, klog/Rails/.NET prefixes and the sentences `panic:` and `Traceback`, `level=` among the first
  six pairs, the first four words against the table, a continuation (indent, `at `, `Caused by:`,
  Postgres `DETAIL:`...) takes `prev`, else **info on either stream**, designer's ruling 1), `Fold`
  (consecutive equal texts without the time span, per stream, `Repeat` = N-1, the newest line's time and
  id), `Parse` and `LineID` (`<copy>.<stream>.<file>.<offset>`, `ParseLineID`). An estimated line takes
  the older timed line's time in its stream, never "now".
- Nomad facts (research R8, lab, observed): `readat` of a missing or purged file answers **200 with the
  error text as the body**, so `deploy.ReadLogAt` checks a chunk shorter than asked with `Stat` (404 is
  `ErrLogRotated`) and never quotes an unexpected body in an error (it may be the app's output); a
  frame's `Offset` is unreliable for a file's short last frame (start + 65,536), so every id and `at`
  offset comes from Caravana's own `readat` offsets, and a Live line has no id; files close under their
  size and never split a line; a restart keeps appending to the newest file; dead copies stay listed and
  readable until Nomad's GC (`GCMaxAllocs` 50, disk over 80 %), at least minutes; a CPU-hungry neighbour
  made a 1 MiB `readat` take up to 6.9 s, which is why there is a deadline. `deploy.LogFiles`,
  `deploy.ReadLogAt` and `deploy.LogCopies` (every copy Nomad lists, any version, terminal ones `Gone`;
  `Copies` keeps its `listed` filter for every other page); `deploytest` has a fake file system.
- Store: `store.ListDeploymentsIn(project, environment, from, to)` (created in range, or ended in range
  for a terminal status), read-only.
- Service: `ReadLogs(EnvLogQuery)` returns `LogRead` (lines newest first, `Coverage`, `Copies` with
  counts and `Gone`, `Dropped`, `From`/`To`; `Histogram()` is 30 bins of `count`, `errors`,
  `estimated` and `read`, false for bins before the coverage); `LogDeploys` gives the deploy rows
  (two per deployment: started, then live/failed and rolled back/replaced, with `deployWho`'s words,
  never filtered by level or search); `LogsAround(copy, at, n)` (both streams of that copy, every level,
  the anchor's id; `ErrLogRotated` is 404 `log_rotated`) and `LogsAroundTime` (a binary search of the
  stream's files by time, about 12 probes of 4 KiB for a 10 MiB file, a stream with no timed line
  falls back to offsets); `DownloadLogs(q, w)` (the k-way merge oldest first, streamed; 8 MiB per
  stream, 64 MiB per download, one at a time: `ErrDownloadBusy`, 409 `download_busy`; the first line
  `# newest last`, an estimated time with a trailing `~`, `# deploy …` and `# cut: …` lines); the
  page read and the download share one stream loop. `WatchLogs(project, environment)` follows the
  place's running copies while a page with Live is open and sends the `logs` notice (`ChangeLogs`: the
  app, never a line) at most every 2 s; the per-app `Logs` read now goes through the same file walk, so
  its lines have an id, a level, a time and a source (`line`, `estimated`, `received`) and a stripped
  `text`.
- API 0.26.0: `listLogs` (`GET /v1/logs`), `getLogsAround` (`/v1/logs/around?copy&at`), `downloadLogs`
  (`text/plain`, `Content-Disposition`), every answer `no-store`, deploy tokens denied by the default
  deny (the scope walk lists them), `LogLine` of `getLogs` gains `id`, `level`, `timeSource`, codes
  `log_rotated` and `download_busy`. Console (`internal/ui/logs.go`, `templates/logs.html`):
  `/logs` (the range bar and Live/Paused, one GET form for the filters, the rail with copy counts,
  the SVG histogram, the stream with folded rows, deploy rows and the open row's strip), `/logs?copy&at`
  (around), `/parts/logs` (the stream and the histogram only, on the `logs` notice; the filters, the rail
  and the open row sit outside the swapped region), `/logs/download`, the app's Logs tab as the same page
  filtered to the app (with "All logs"), the sidebar's Logs item. The service's hue (a stable hash of
  `<app>/<service>` over six) is the console's one identity color and the histogram its one SVG chart (the
  CSP forbids `style`, so bars are `rect` attributes and classes), both recorded in `DESIGN.md`; an
  estimated time is fg-4 with a leading `~`. Forms and links work without JavaScript; Live needs it.
- Secrets rule: a line's text is never written to the store, the server log, an error, a notice or a
  live event; the `logs` notice has no text, Download and every API answer are `no-store`, control
  bytes are stripped so a file can't drive a terminal. Tests search for sentinels.
- Lab: `TestConsoleLab` has a step `console-logs` (`console_logs_test.go`) and the gate a step `logs`
  (`gate_logs_test.go`), per `specs/024-logs/quickstart.md` §6. Both pass in the lab (2026-10-25): the
  console step in about a minute inside the full run, the gate step in 41 s. The gate step found that an
  untimed stream was pinned to the copy's start and that the start isn't a lower bound (a container's
  clock runs ahead of Nomad's start event); since then an untimed line is placed by byte offset between
  its file's write bounds, and a timed line up to a minute before the start is trusted.

Feature 025 (`specs/025-monitoring`, Phase 2.6) is implemented on branch `025-monitoring` (stacked on
`024-logs`): requests, errors, the slowest 5 % and memory per service against the same hour of the
last 7 days, the host's capacity and room for more apps, and a disk item on the needs-you list. New
migration 0019, no new Go module (our own parser of Traefik's text format; `expfmt` would bring three).
- Sources and the sampler (`service/sampler.go`, plan "Decisions carried"): `runSampler` is started
  in `Service.Start` with the other watchers and ticks at the next whole minute plus 2 s
  (`SetMetricsSampleGap`, 10 s in the lab). Each tick has 20 s and reads each source on its own, so a
  failed one leaves its columns NULL (a gap, never 0): Traefik's Prometheus text on loopback
  (`CARAVANA_TRAEFIK_METRICS_URL`, default `http://127.0.0.1:8081/metrics`, loopback only, empty turns the
  HTTP numbers off; states `ok`, `off` (404), `unreachable`, `disabled`), Nomad's allocation list with
  resources (reservations, restarts, exit events), 014's per-copy `Allocations().Stats` for usage, and
  the node's stats (states `ok`, `denied`, `unreachable`). The page's notices come from the last outcome of
  each. One transaction per tick writes the minute rows, folds the 10-minute slot that just ended and,
  every 10 minutes, prunes; then `ChangeMetrics` (the live notice `metrics`, no number). The previous
  Traefik snapshot, the restart counts and the last scrape time live in memory: a restart loses them, so
  its first minute has CPU and memory and no HTTP numbers.
- `internal/metrics` is pure (a test forbids `net/http` outside `real.go`, and `nomad`, `service`,
  `store`): `ParseText` (the 0.0.4 format, only the named families), `Delta` (a counter that went
  down is a reset and the delta is its value; a delta is kept only when the last scrape was 50-90 s
  before; `process_start_time_seconds` counts as a restart only when it moves by more than 10 s,
  because it drifts 1-4 s in one process, lab, observed), `TraefikMinute` (5xx from `http` and `sse` only,
  because a grpc `code` is a gRPC status; buckets from `http` and `grpc` only, so SSE counts as requests
  and never in latency; websocket never appears in Traefik's service metrics), `Layout`/`Buckets`/`Merge`/
  `Quantile` (per-bucket counts stored, Prometheus' interpolation with a running maximum on read,
  over the last bound is "over 10 s", bounds 5 ms to 10 s), `Fold` and `Points` (counters and buckets
  summed, gauges the median, a point with no row is a gap), `Band`/`Baseline` (min, max and median of
  the same UTC 10-minute slot on the previous 1-7 days; `building` under 24 h of history, `partial`
  with 1-6 days, `full`), `Judge`/`Since`/`DeployNear` (slower: p95 over a 10-minute window of at
  least 20 requests above 1.5 times the band's high and 100 ms over it; more errors: over 1 %, 5 or
  more, above the band; quieter: under half the band's low; memory over 90 % of the limit; restarting:
  2 or more; rollouts skip the HTTP rules, stopped apps aren't judged), `Room` (N = min of memory and
  CPU left over the yardstick app, the one whose memory reservation is the median) and
  `DiskState`/`DiskRun`/`Stale` (opens at 85 %, resolves below 83 %).
- Nomad reads (`internal/deploy`): `host.go` `HostStats` (`Nodes().Info` cached 10 minutes, `Stats`; the
  two watched filesystems are the `DiskStats` entry that matches `AllocDirStats` (Nomad's data) and the one
  with the longest mount that prefixes `CARAVANA_DOCKER_ROOT`, one row with `holds` when it's one disk;
  a 403 is `ErrNodeReadDenied`); `reserved.go` `Reservations` (per copy `max(sum of prestart non-sidecar
  tasks, sum of main and sidecar tasks)` from the job's task lifecycles, cached per job version: 1780 MHz
  and 1344 MB, exactly Nomad's `allocated` gauges, where a plain sum said 1930/1440); `usage.go` memory is
  `Usage - Cache` (else RSS), and a copy with `Timestamp` 0 or no measured memory is NULL (Nomad answers a
  stopped copy with one zeroed entry per task, which 014's test read as 0); `deploytest` has the node and
  allocation fakes.
- Store: migration `0019_metrics.sql` (`metrics_series`, `metrics_layouts`, `metrics_samples` with
  `res` 60 for 48 h and 600 for 8 days overlapping, `metrics_host`, `metrics_disks`, `metrics_state`
  whose `started_at` is written once), `store.WriteMetricsTick`, `MinuteRows`, `SlotRows`, `HostRows`,
  `DiskRows`; `DeleteApp` takes the app's series. 3-8 MB for 20 services.
- Service reads: `Metrics`/`AppMetrics` (`service/metrics.go`: the place check, points, bands, totals,
  issues, the deploy clause from healthy deployments' end within 15 minutes, sort `unusual`/`traffic`/
  `memory`), `Host` (`service/host.go`, from the newest stored rows, stale after 5 minutes). Restarts
  use one rule, `crashesInHour` (3 is a needs-you `restarting`, 2 a monitoring issue). The attention kind
  `disk_nearly_full` is host-level (key `disk_nearly_full|||<mount>`, level failed from 95 %), derived on
  every read from the stored disk rows, and `diffProblems` doesn't judge it (so never sends "resolved")
  when the newest host sample is over 5 minutes old; `notify.Event` carries `mount`, `holds`,
  `usedPercent`, `freeBytes`.
- API 0.27.0: `getMetrics`, `getAppMetrics`, `getHost` (read from the store, never 502: `sources` says what
  couldn't be read; a gap is `null`), the attention kind and the webhook's five properties; deploy
  tokens denied by the default deny. Console: `/monitoring` (`ui/monitoring.go`, `monitoringchart.go`,
  `monitoringhost.go`: the issues panel, four inline-SVG charts with the band and deploy markers and a
  hidden table, the apps table, the host section with meters whose reserved part is hatched, the 7-day
  memory chart, a footer that says websocket isn't counted), `/parts/monitoring`, `/parts/monitoring/host`
  and `/parts/platform` swapped on the `metrics` notice, `static/js/monitoring.js` (the hover crosshair;
  the page reads without it), the Apps home's Platform panel above Activity, the sidebar's Monitoring
  item. The Running tab's memory is the new one, and a copy with no measurement shows "—". Ruling 9
  (text is never fg-4) is in `DESIGN.md`.
- Installer: both `traefik.nomad.hcl` files gain `--metrics.prometheus=true` and the buckets (Traefik
  restarts once on a rerun, and `install.sh` says so), the policy gains `node { policy = "read" }`,
  `caravana.env` gains `CARAVANA_DOCKER_ROOT` from `docker info` (`-docker-root`, default
  `/var/lib/docker`), `internal/config` checks both new settings, and `install-test.sh` checks the
  policy line, the env line and a 200 from `127.0.0.1:8081/metrics`.
- Not in 025: a host reservation, per-copy history, the access log, owner-set thresholds, network and
  disk I/O, an MCP tool, several servers (Phase 3).
- Lab: `TestConsoleLab` has a step `console-monitoring` and the gate a step `monitoring`, per
  `specs/025-monitoring/quickstart.md` §6 (`startLab` points the Traefik URL at `127.0.0.1:8088/metrics`
  and the gap at 10 s). They passed in the lab before 025 merged on 2026-10-26 (`specs/025-monitoring/tasks.md` T018 and
  T019 are ticked); T020 (the seeded review data and the disk fill on `install-test.sh`'s VM) is the
  other lab piece, also done.
  The lab's checks are in `research.md` R8 ("lab, observed"). Gotchas: the lab VM has about 4 GB, so
  clear the review apps before a lab step, or a deploy fails for lack of memory; and
  `TestRestartMidRolloutRestoresSecrets`'s fix belongs to 024, not to this feature.

Feature 027 (`specs/027-operator-view`, Phase 2.6, moved from Phase 3) is implemented on branch
`027-operator-view` (stacked on `025-monitoring`): the Operator view for the one host. A
Developer/Operator toggle under the brand (the mode cookie; Settings and the app pages keep the view
the owner was last in; `/` and sign-in still land on the Developer home) and three pages, each
built in the designer's method (contract, live review, critique, last look) and approved: Overview at
`/ops` (9210661), the server page at `/ops/servers/<name>` (4344394) and Updates at `/ops/updates`
(624489a). It reads and never applies: nothing installs, restarts or reboots, and no vulnerability is
named. New migration 0020 (`release_check`, `host_events`, `problem_events`, `host_marks`), no new Go
module, no Nomad policy change.
- `internal/hostos` is pure (reads through an `fs.FS`; only `real.go` touches the host): apt's
  `history.log` and its rotated `.1.gz` (`ReadHistory`, runs and who made them), the
  `reboot-required` marker with its packages, installed kernels, Ubuntu's `updates-available` counts
  and unattended-upgrades' settings (`ReadState`), pro's `security-status` JSON (`ParseSecurityStatus`,
  package names and a kernel flag) and release order (`version.go`). The sampler (025) reads the files
  on each tick (`service/hostosstate.go`, `noteOS`); `pro` runs only when the owner asks (Check package
  names: one at a time, 120 s, once per 10 minutes) and only on a host that is Ubuntu with `pro`.
- Service: `service/activity.go` (`Activity`: the feed in Caravana's words from deployments, actions,
  backups and restores, schedule runs, problem events and host events; first version ever recorded is a
  baseline, not an event), `updates.go` (`Updates`, the release check, `CheckRelease`, `CheckPackages`,
  settings), `attention_os.go` (`os_reboot_required`, and `os_security_updates` at once when automatic
  updates are off, else after security updates have waited 24 hours; never judged from a list older than 48 hours), `hostnode.go` (the node block of `Host`,
  boot events from uptime, Caravana's version events at start) and `HostCopies` (every copy on the
  node with its use and reservation). The release check asks GitHub's latest release once a day with an
  ETag (and on Check now, rate limited) and reads the release's installer and Traefik job for the pins;
  it is on by default, can be turned off on Updates (then no request is made) and sends nothing about the
  server. A new release is one `caravana.release` message to every channel that isn't paused.
- Settings: `CARAVANA_HOST_ROOT` (`-host-root`) points the OS file reads at a folder; it is for dev and
  tests only (the lab and `install-test.sh` use fixtures; a host leaves it unset and reads `/`). The
  unit's `ProtectSystem=strict` still allows the reads.
- Console (`internal/ui/ops.go`, `opsserver.go`, `opsupdates.go`, `static/js/ops.js`): one live stream
  (`/live?watch=ops`) and parts per region (`/parts/ops/server`, `/apps`, `/activity`,
  `/parts/ops/servers/<name>`, `/parts/ops/updates`), the Updates nav mark in the shell (■ reboot, ◆
  security, else the count); `POST /ops/updates/check` (`what=release|packages`) and
  `/ops/updates/settings` redirect with 303 and draw a refusal on the page. Forms work without
  JavaScript.
- Cross-cutting changes the review asked for: a copy is `starting` until Nomad decides its health (the shared copy
  status: in a rollout from the job's active deployment, outside one from its checks), so the Running tab
  and the Operator pages agree; the needs-you wording has **one source**, `service.ProblemParts`
  (`sentenceparts.go`), which the home, the Operator pages, the feed and the notifications draw, with
  names, kernels, packages and versions in mono (`TestEveryKindSaysTheSameWordsEverywhere` walks
  `service.AttentionKinds`); one vCPU formatter (two decimals, whole totals); dates read "Oct 22" and
  the local time leaves out its date when it is the UTC date; nav marks wear the hue.
- API 0.29.0: `getHost`'s `node`, `listActivity`, `getUpdates`, `putUpdateSettings`, `checkUpdates`,
  the attention kinds `os_reboot_required` and `os_security_updates`, and the `caravana.release`
  notification (the webhook message has a `release` object). Deploy tokens are denied by the default deny.
- Tests: a sentinel per package that can read the view (`operator_sentinel_test.go` in `service`, `api`
  and `ui`: a secret, a command's output, pro's text and an apt command line never reach Activity,
  Updates, Host, the pages, the notifications, the log or the SQLite file; release notes are public, so
  only the log is checked for them). `TestConsoleLab` has the steps `console-ops-overview`,
  `console-ops-server` and `console-ops-updates` (`console_ops_test.go`, `console_ops_updates_test.go`:
  a fake host root with a pending kernel, a stand-in release server, a recorded `pro` answer) and the
  gate has a step `os-updates` (`gate_os_updates_test.go`: `problem.opened` for the reboot, then
  `problem.resolved` after the marker is removed, one `caravana.release`). `deploy/dev/install-test.sh`
  checks `GET /v1/updates` and `GET /v1/host` inside the unit's sandbox.
- **Merge condition, met on 2026-10-31:** the whole `TestConsoleLab` passes on a quiet machine (30 of 30
  steps). `console-logs` is flaky when the machine is busy: the copy-status change isn't the cause, since
  it passed 3 of 3 on an idle one. `install-test.sh` passes on a throwaway VM, with `pro` running inside
  the unit's sandbox.
- Not in 027: applying updates from the console (Phase 3), several servers (the server list, Move work
  off, cluster-wide Updates), and the Activity, Logs and Proxy pages of the Operator view (Phase 3).

Feature 028 (`specs/028-light-theme`, Phase 2.6, from a CodeAlta task) is implemented on the worktree branch
`alta/fluent-topaz-6qk8`: a light theme, the same look with the same rules. The designer's contract
(`contracts/light-theme.md`, with `cr.py`, its oklch-to-WCAG calculator) decided every value.
- Every colour is a token in `internal/ui/style/theme.css`, declared once as `light-dark(<light>, <dark>)`
  (the light value first: the function's order)
  (the daisyUI slots point at the ramp with `var()`), and `:root`'s `color-scheme` picks the side: dark by
  default, light under `prefers-color-scheme: light` unless `data-cv-theme="dark"`, and `data-cv-theme="light"`
  forces light. The pinned Tailwind build keeps `light-dark()` as written. The direct colours `input.css`
  had are tokens now (`focus`, the washes, `hue-0…5`, `tick`, `ping`, `overlay-shade`, `hatch-fill`,
  `terminal`); the `#000` stops in `mask-image` and `terminal.js`'s hex palette are the two named
  exceptions (the terminal stays dark in both themes). The three hard-coded `color-scheme: dark` are gone.
- The forced choice is the cookie `caravana_theme` (`httpx.ThemeCookie`/`SetThemeCookie`, the view cookie's
  attributes; "Match the system" deletes it), read into `pageData.Theme` by `s.page(r, title)` (so
  `s.message` and `s.serverError` take the request) and written by `base.html` as `data-cv-theme` on
  `<html>` with a one-word `color-scheme` meta (`dark light` without it): no flash, and the sign-in page
  follows it too. Settings › This browser › Appearance (`/settings/appearance`, a last nav group) has
  Match the system (console.js adds "(now dark)"), Light and Dark; the post answers 303 to `?saved=1`.
  No API change.
- `internal/ui/theme_test.go` recomputes the contract's contrast ratios from `theme.css` for both palettes,
  checks every token has both values and that no colour is written outside the tokens; `DESIGN.md` has the
  Token-Only Color Rule and "depth is distance from the panel face". `TestConsoleLab` has a step
  `console-appearance`. The designer's review, the critique's rulings and the last look are the contract's
  sections 8 to 10 (approved 2026-11-04); every `TestConsoleLab` step passed on the build that day, after
  `console-domains-states` lost a stale expectation for a lab without `CARAVANA_PUBLIC_URL`.

Feature 029 (`specs/029-sqlite-backups`, from a CodeAlta task) is implemented on the worktree branch
`alta/cozy-kiln-6m49`: the backup engine `sqlite`, `backup: {engine: sqlite, file: /data/app.db}`.
- `file` is the database's path as the service sees it, inside one of its writable volumes
  (`app.Service.SQLiteFile()` gives the volume and the path in it; `validateSQLiteBackup`).
- A sqlite-backed service gets two idle poststart sidecars (`nomadjob/sqlite.go`; constitution 1.1.1
  names them): `caravana-sqlite` (`alpine/sqlite` pinned by digest, `CARAVANA_SQLITE_IMAGE`, the db's
  volume at `/caravana-volumes/<v>`) and `caravana-upload` (the rclone image). Caravana runs the
  scripts in them with alloc exec (`deploy/sqliterun.go`): a backup is `VACUUM INTO` plus
  `PRAGMA quick_check` into `/alloc/data/caravana/sqlite/<request>.db`, then gzip and `rclone rcat` to
  `<BACKUP_PATH>/<ts>.sqlite.gz` with retention, the service never stopped.
- The destination's credentials go on the exec's standard input only (`deploy.Exec` takes a reader):
  no variable, file or argument holds them.
- A restore downloads and stages the checked file on the db's volume
  (`.caravana-restore/<request>.db`) while the service runs, then writes a `restore-sqlite` request
  (`FILE=<volume>/<path>`) and restarts the copy: `VolumeScript` swaps the file (owner and mode kept,
  `-wal`/`-shm`/`-journal` moved aside, everything put back on failure).
- Runs share 016's claim, IDs, scheduler and records (`service/sqlitebackup.go`); at start a running
  sqlite backup is failed, a sqlite restore resumes only if its request was written (`VolumeRequestIs`).
- Docker facts (research D1–D5): root's SQLite gives created `-wal`/`-shm` to the db's owner, and
  `sqlite3` on a missing path creates an empty file (the copy script checks first).
- Migration 0022 (engine CHECK), API 0.32.0 (`BackupPolicy.file`, engine `sqlite`), console (Compose
  form engine and Database file, Backups tab "A SQLite file instead"), gate step
  `sqlite-backup-restore`.

Feature 030 (`specs/030-update-from-console`, from a CodeAlta task) is implemented on the worktree branch
`alta/earthy-thistle-j2m8`: Caravana updates itself from Operator › Updates, and the console still never gets
root. The contracts are `contracts/updater.md` (the files both sides read), `api.md` and `page-updates.md`.
- The request: `RequestUpdate` (`service/updaterun.go`) writes a row in `update_runs` (migration 0023) and
  `<data dir>/update-request` with only `ID=<32 hex>` and `VERSION=`. It refuses a dev build, a host without the
  updater, an open run, and any version but the newest release seen that is newer than the running one.
- The updater: `deploy/install/caravana-update.sh` → `/usr/local/libexec/caravana-update`, root, run every minute
  by `caravana-update.timer`. It reads the request as text with size and symlink checks, validates both values,
  checks the version is newer than `/opt/caravana/current`, downloads `SHA256SUMS`, `install.sh` and the tarball,
  verifies both and runs `install.sh --from`. It writes `status` (`running`, then `updated`, `rolled_back`,
  `failed` or `refused`, with a one-line reason) and a root-only log in `/var/lib/caravana-update`, a folder
  Caravana reads and never writes. Root never writes inside `/var/lib/caravana`; Caravana removes its own request
  when the run is final. The script's body is one `main` call, since the installer rewrites it mid-run.
- The watcher reads the status every 5 s while a run is open and once at start, so a run is followed across
  Caravana's restart (and a rollback's copied data still holds the row). `lost` comes after 5 minutes without a
  start or 45 minutes running. A rolled-back, failed, refused or lost run is the needs-you kind
  `caravana_update_failed` until a later update succeeds.
- Automatic updates (`release_check.auto_update`, `auto_update_hour`, off by default) request a patch release
  of the running version at the set UTC hour, and never a version that already had a run.
- For tests and dev only: `CARAVANA_RELEASE_URL` (`-release-url`) and `CARAVANA_UPDATER_DIR` (`-updater-dir`), and
  a root-owned `/etc/caravana/update.env` (`CARAVANA_UPDATE_RELEASES`) that only `install-test.sh` writes.
- API 0.33.0: `requestUpdate` (`POST /v1/updates/runs`), `listUpdateRuns`, `caravana.updater`/`run`/`autoUpdate`
  on `getUpdates`, `autoUpdate`/`autoUpdateHour` on `putUpdateSettings`, codes `update_unavailable`,
  `update_running`, `update_not_offered`.
- Console: the caravana row's **Update to X…**, the confirmation page `/ops/updates/caravana?version=`, the run
  panel (`#run`, polled from `/parts/ops/updates/run` while open, a `noscript` refresh without script), Automatic
  caravana updates in the release-check form, and update runs in Past updates.
- `install-test.sh` updates a VM through the API (releases `0.0.<time>` served inside the VM). It also checks
  that a crafted or symlinked request runs nothing and that a broken release is rolled back with the item. It
  passed on 2026-11-06.

Feature 031 (`specs/031-add-a-server`, Phase 3's first feature, from a CodeAlta task) is implemented on the
worktree branch `alta/genuine-skiff-rmbk`: a server joins from a command made in the console, and three
servers survive the loss of one. The contracts are `contracts/node.md` (addresses, files, the agent, the
installer), `api.md` and `page-servers.md`; `research.md` holds what three Lima VMs showed (L1 to L18).
- Addresses (`internal/mesh`, pure): every server has an index N (1 to 63; the first is 1) and everything
  derives from it: mesh `10.98.0.N` on `caravana-mesh` (WireGuard, UDP 51820), app ports `10.99.0.N` on
  `caravana0`, containers `172.26.64.0/20` (N = 1, Nomad's default) or `10.97.(4N).0/22`. A peer's allowed
  addresses are those three, so Traefik reaches a copy on any server and service names resolve across
  servers. No address ever comes from the console.
- The agent: `caravana node init-first|join|up|sync|show|leave` (`internal/nodeagent`), root, the same
  binary copied to `/usr/local/libexec/caravana-node` (so a rolled-back Caravana never takes it away), run by
  `caravana-node.timer` every 30 s; `caravana-mesh.service` runs `node up` before Nomad at boot. It renders
  `nomad.hcl` (goldens; `deploy/install/nomad.hcl.tpl` is gone), keeps `state.json`, and applies the
  console's answer only after checking every value (`validate.go`): it takes no command, path or address
  from it. Files in `/etc/caravana-node` (0700) and `/etc/nomad.d/tls`.
- Nomad: TLS on RPC and raft only (`http = false`: the HTTP API stays on loopback), `verify_server_hostname`,
  gossip encryption. Caravana is the CA (`internal/clusterca`): the CA's key is in the Nomad Variable
  `caravana/cluster/ca`, the gossip key in `caravana/cluster/gossip`; a server sends a CSR and keeps its key.
  Certificates last 90 days and the agent renews them at 30 with `systemctl reload nomad` (no restart).
- Roles (`service/serverroles.go`): one Nomad server until three servers are ready, then the first and the
  two oldest others, promoted one at a time (a client gets a server certificate, the gossip key and one
  Nomad restart; its copies keep running). Every Nomad server has `failover_heartbeat_ttl = "1m"`: with
  Nomad's 5 minutes a lost leader's copies stayed registered for five minutes (L14). It also has
  `heartbeat_grace = "30s"`: with Nomad's 10 seconds a healthy server whose heartbeat was on its way to the
  lost one was called down and its copies moved (L18); a lost server is called down after about 45 seconds. A Nomad server's own
  client lists no servers (L15).
- Store and service: migration `0024_servers.sql` (`servers`: index, name, pool, state `waiting`, `joining`
  or `ready`, hashes of the join token and the node token, the agent's last report). `CreateJoin` (token
  `cvjn_…`, once, one hour, shown once), `Join` (a client certificate for the CSR, a node token `cvnd_…`),
  `NodeSync` (the four checks: connected, docker, network, ready; a down server doesn't hold up a join),
  `NodeCertificate`, `ListServers` (rows joined with Nomad's nodes by app address; a node without a row is
  `managed: false`), `RemoveServer` (a waiting join, or a server Nomad says is down), `AdoptFirst`
  (`caravana server adopt-first`, called by the installer). The names `new` and `joins` are reserved.
- What assumed one server: `nomadjob.Translate` spreads a multi-copy group over `${node.unique.id}`;
  `deploy.EnsureVolumes` puts all volumes of a service on one server; `deploy.HostStats` takes a node and the
  sampler writes rows per server; `Host(ctx, server)` and `HostCopies(ctx, server)`; the room for more apps
  is the cluster's sum; `disk_nearly_full` is per server (the key of the server Caravana runs on is
  unchanged); Traefik's job has a constraint on `meta.caravana_ingress`, so it runs on the first server
  only (one ingress: shared certificates and a proxy per server are later Phase 3 features).
- Needs-you kinds `server_down` and `server_certificate_expiring`; the OS items name their server once
  there is more than one.
- API 0.34.0: `listServers`, `getServer`, `addServer`, `getServerJoin`, `removeServer`, the node calls
  `joinServer`, `syncNode`, `renewNodeCertificate` (their own credentials, outside `authenticate`; a failed
  join counts against the address like a failed sign-in), `getHost?server=` with `server` and `osRead`,
  `AppVolume.server`. Deploy tokens are denied by the default deny.
- Console: Operator › Servers (`/ops/servers`, the list), Add a server (`/ops/servers/new`, then the
  command and the live checks; the token is in that one answer only), a page per server with Remove for a
  down one, and the per-server pieces on Overview, Running, Monitoring, Volumes and Updates.
- Two facts every Nomad restart depends on: the installer writes `/etc/systemd/network/10-caravana-veth.link`
  (`MACAddressPolicy=none`), because systemd rewrote a container veth's MAC after the CNI plugin recorded
  it and Nomad then replaced the copy at its next restart (L17; copies made before the file can still be
  replaced once); and every server has `leave_on_terminate = true`, with `caravana node leave` sending
  SIGTERM so a retiring Nomad server leaves raft (L16; a stop or restart sends SIGINT and leaves nothing).
- Installer: `--first` (the default), `--join TOKEN --url URL`, `--address`, `--insecure`, `--leave`. A host
  installed before this feature is converted by the next run: Nomad restarts twice, once only (the second
  moves its raft address with `peers.json`, L2), with the old config kept as
  `/var/lib/caravana-host/nomad.hcl.before-mesh`.
- Tests: `deploy/dev/install-test.sh` checks the first server's network, certificates and registration;
  `deploy/dev/cluster-test.sh` runs three Lima VMs on `lima:user-v2` (`vzNAT` isolates guests): the
  conversion of a host installed from `d13c37c`, two joins from API tokens, three raft voters, the spread,
  names across servers, volumes on one server, the loss of server 3 and of server 1, a renewal, a removal
  and `--leave`. It passed on 2026-11-10 (0 calls failed during the conversion, 11 while a server was lost);
  the five runs before it each failed one step and found a real problem (L14, L16, L17, L18). It takes 20
  to 40 minutes and keeps its VMs with `KEEP=1`. `TestConsoleLab` has a step `console-servers` (the lab is
  one node with no mesh: listed as unmanaged, and Add a server says to run the installer first). The whole
  `TestConsoleLab` passed on 2026-11-10 (33 steps, 26 minutes), after three steps that had been failing on
  `main` since 021's ruling 12, 029 and 030 were brought up to date; the lab stops at its first failure, so
  a stale step hides every step after it.
- Not in 031: moving work off a live server, a proxy on every server, shared certificates, updating joined
  servers from the console, reading their OS updates, SSH as a way to add a server, Caravana's own high
  availability (its data is one SQLite file on the first server).

Feature 032 (`specs/032-registry-paths`, registry entries per path, from CodeAlta task 7239) is implemented on the worktree branch
`alta/arctic-palm-fhmv`: private-registry credentials may be limited to a path on a host.
- An entry is a host or `host/path` (`ghcr.io`, `ghcr.io/acme`). The path is lowercase repository segments
  without a leading or trailing `/` (`service.canonicalRegistryEntry`; a bad one is a field error `path`,
  shown at the console's registry input). `internal/registry/entry.go` is the pure rule: an image
  (host, repository) uses the most specific entry whose path is a whole-segment prefix of the repository
  (`EntryCandidates`, `MatchEntry`; `ghcr.io/acme` matches `acme` and `acme/x`, never `acme-old/x`), the
  host-only entry is the fallback, no match is anonymous. Existing rows and variables are host-only
  entries, so nothing moved and the translator's goldens are unchanged.
- `Client.Credentials` is `func(ctx, host, repository)` and `Client.Test(ctx, host, repository)` only passes
  the repository to the hook (the request is still `GET /v2/`, so a test of a path entry checks the login
  only; the stored message says the first deploy shows which images it reads). `deploy.pullCredentials`
  tries `EntryCandidates` in order.
- Variable path: `caravana/registries/<host key>[/<path with "." as "~">]` (`RegistryVariablePath(entry)`;
  host keys unchanged). The per-task copy's `registry` item holds the ENTRY; the docker `auth` block's
  `server_address` is still the host. `nomadjob.PrivateRegistry` returns the matching entry from the set of
  entries (`private` is now the set of entries, `Registry.Key()`).
- `SyncRegistryCopies(ctx, entry, private)` (`private` after the change) rewrites a copy only where the
  entry that covers its image (read from its job; the old "item == entry" rule when the job or image isn't
  there) changed or has new credentials: saving one prefix leaves the others and the host entry's copies
  untouched, adding a prefix moves the copies under it off the host entry, removing one moves them back (or
  empties them). Backup and restore variables follow the image of their dump or restore task and never get
  a `registry` item.
- Store: migration `0025_registry_paths.sql` (`registries` keyed by `(host, path)`, `path` `''` for a host
  entry); `store.Registry.Path` and `Key()`; `GetRegistry`, `DeleteRegistry`, `SaveRegistryTest`,
  `ClearRegistryTest` take `(host, path)`.
- API 0.35.0: optional `?path=` on `putRegistry`, `deleteRegistry` and `testRegistry` (route unchanged),
  `Registry.path`, 422 `invalid_field` on `path`. Console: the Registry field takes `host/path`, rows show
  the entry, Test and Remove are `POST /settings/registries/test` and `/remove` with the entry in the
  hidden `host` field, "used by" counts the apps each entry covers.
- The gate's `private-registry` step has two htpasswd users, four images (`gate`, `teama`, `teamb`,
  `other`) and a per-path part (wrong host entry, two path entries, two-service app healthy, `other/` is 422
  `registry_auth_failed`, saving teamb leaves teama's copy's ModifyIndex alone, deleting teama moves its
  copy to the host entry). It passed in the lab on 2026-11-11 (53 s; run alone with
  `-run 'TestLab/^private-registry$'`).

Feature 033 (`specs/033-passkeys`, from a CodeAlta task) is implemented on the worktree branch `alta/ready-tabor-hzqw`:
the admin may sign in with a WebAuthn passkey instead of the password, and may turn the password off. Library:
`github.com/go-webauthn/webauthn` v0.18.2 (new module, justified in the commit; no metadata service is configured, so
nothing is fetched). The contracts are `contracts/api.md` and `contracts/console.md`; `plan.md` R1–R13 are the decisions.
- Store: migration 0026 (`passkeys`; `admin.password_signin`, `admin.webauthn_handle`; `sessions.verified_at`). Only public
  material is stored (public key, counter, flags); the admin's 32-byte WebAuthn user handle is made at the first
  registration and set only when NULL.
- Ceremonies (`service/ceremonies.go`, `passkeys.go`): in memory in `Auth`, single use, 5 minutes, 8 per address and 512
  in all, and `confirm` and `register` ceremonies are bound to the session that began them. User verification and a
  resident key are required, attestation is `none`. A sign-in checks the user handle is the admin's and the credential's
  `rp_id` is the page's domain; a sign count that doesn't rise (`CloneWarning`) is a failure. Every failure of a sign-in or
  a confirmation is counted in the sign-in limit (`beginAttempt` first, so a locked address is refused before anything is
  parsed); asking for options is not. Log events name our passkey ID only, never a credential ID, key, challenge or the
  library's error text.
- Relying party (`ui/passkeys.go`, `passkeyPlace`): the RP ID is the request's host without port, lower case; the origin
  is the scheme (`proxies.HTTPS`) and `r.Host`. Passkeys are available only at a plain ASCII domain name (not an IP),
  over https or at `localhost`/`*.localhost`, and, when `CARAVANA_PUBLIC_URL` is set, only at its host or a localhost
  name. The console domain (what password sign-in is judged against) is the public URL's host, else the request's.
- Password sign-in: `SignIn(ctx, email, password, consoleDomain, address)` and `VerifyPassword` refuse with
  `ErrPasswordSignInOff`, without checking or counting, when it is off **and** a passkey exists for the console domain
  (`""` means any passkey); with none the password still works (a domain change never locks the owner out).
  `ResetPassword` turns it on. `RemovePasskey` refuses the last passkey of its own domain while it is off
  (`ErrLastPasskey`, `*PasskeyDomainError`); `RemovePasskeyUnchecked` is the CLI's.
- Confirm window: a session's `verified_at` (set at sign-in, by `VerifyPassword` and by a passkey confirm) must be within
  10 minutes to add a passkey; a session from before the migration has none.
- API 0.36.0: `listPasskeys`, `updatePasskey`, `deletePasskey`, `getSignIn`, `putSignIn` (`/v1/account/…`; codes
  `passkey_not_found`, `last_passkey`, `no_passkey`; deploy tokens are denied by the default deny). The ceremonies are
  console routes only; `api/parity_test.go` keeps them in the named set `consoleOnly` with their reasons.
- Console: Settings › Sign-in (`/settings/sign-in`, the Security group's first entry, icon `fingerprint`), `/login/passkey`,
  `/login/passkey/options`, `/settings/sign-in/verify[/options]`, `/settings/passkeys[/options|/{id}|/{id}/remove]` and
  `POST /settings/sign-in` (`password=on|off`). The JSON routes answer `no-store` and `{"error":{"code","message"}}`; a signed-out
  script gets 401 JSON, not a redirect. The finishes are form posts: 303 on success, the page with one sentence on failure,
  and the credential, ceremony and library errors are never drawn, logged or put in a redirect.
- Script: `static/js/passkeys.js` is ours (`ours` in `tools/ui-vendor.versions`), loaded by `/login` and Settings › Sign-in
  only; no inline script and the CSP is unchanged. Its blocks (`data-passkey-signin|confirm|add`) start `hidden` and are shown
  only when the browser supports passkeys and the server said `data-available="true"`.
- CLI: `caravana admin passkeys list|remove --id`, `reset-password` says "Password sign-in is on again." when it was off, and
  `status` has a `sign-in:` line.
- Tests: `internal/webauthntest` is a software authenticator (ES256, `none` attestation) whose knobs make the failures
  (counter, UV flag, user handle, RP ID and origin it signs for) and whose `PrivateKeySentinel()` leak tests search for.
  `TestConsoleLab` has a step `console-passkeys` (`console_passkeys_test.go`, before `console-appearance`) at
  `http://localhost:<port>`; it ends the lab console's session with `reset-password` and signs it in again on the way out, and
  counts four failures (never five) so the lab's address is not locked for 15 minutes. It passed in the lab on
  2026-11-12 (run with `-run 'TestConsoleLab/^console-(sign-in|passkeys|appearance)$'`), and a headless Chrome with its
  virtual authenticator (CDP `WebAuthn`) added a passkey, signed in with it and with password sign-in off at
  `http://localhost:7091`. A credential ID is not secret: it is in the options answers (`allowCredentials`,
  `excludeCredentials`), never in a page or a log line.

Feature 034 (`specs/034-byo-certificates`, CodeAlta task `k83w`, worktree branch `alta/quick-baker-rvch`): bring your own
certificates, another ACME issuer or an uploaded certificate. `research.md` R1–R10 are the lab's facts (2026-11-12 and 11-24, a
second Traefik and a Pebble ACME server requiring EAB); operator doc `docs/certificates.md`. Migration 0046, API 0.63.0 (merged after 055 and 057), no new
module, constitution unchanged (certificates reach Traefik through its file provider, like 023's Team only).
- Traefik's static configuration is a file both jobs render into `secrets/traefik.yml` (`--configFile` is the only argument:
  Traefik reads a file or flags, never both, and an EAB key can't be a flag). It reads the Nomad Variable `caravana/acme`
  (items `server`, `email`, `eab_kid`, `eab_hmac`, `ca`, `storage`, always all six) with `change_mode = "restart"`, so a new
  issuer restarts Traefik's task in place, and `caravana/certificates/<id>` (`certificate`, `key`, `names`) into
  `secrets/dynamic/certificates.yml` (`noop`, the file provider's watch). The provider's directory moved from `local/dynamic` to
  `secrets/dynamic` (with 023's `team.yml` and 051's `caravana.yml`): Nomad's fs API can't read `secrets/`. The job-scoped
  policy `traefik-read` gains those two paths and must be applied before the job (without it the task waits at
  "Missing: nomad.var.get"). The job's meta `caravana_certificates = "1"` is how Caravana knows (`deploy.IngressFeatures`), and
  `ProxyAccessLog` takes it too (the access log of 047 is in the file now). The resolver keeps the name `letsencrypt`; each
  issuer has its own store file `acme-<12 hex of SHA-256(server + "\n" + key ID)>.json`, the installer's stays `acme.json`.
- `internal/certs` (standard library only): `Parse` (the key matches, the chain is complete, dates, DNS names, RSA 2048+ or
  ECDSA, not encrypted; every refusal a `*certs.Error` on `certificate` or `key` with no key bytes), `Covers`/`SameNames`,
  `CheckDirectory` (one GET, no redirect, never registers: an EAB key can be single-use), `ValidServer`, `NormalizeHMAC`,
  `NormalizeCAPEM`; `certstest` makes CAs and leaves.
- Service: `SetCertificateIssuer`/`ResetCertificateIssuer` (`certissuer.go`; the variable before SQLite, a stored HMAC key kept
  only for its key ID, the issuer's CA added to the domain checks' roots), `UploadCertificate` and friends
  (`uploadedcerts.go`; same names replace, 30 at most, variable before row with rollback, covered hosts checked again after 15 s),
  a host presenting an upload is judged by the upload's dates (`judgeUploaded`, matched by the leaf's SHA-256, stored as
  `domain_checks.cert_fingerprint`) and names it (`Uploaded`); `keptEarlier` lists covered hosts on another certificate with
  `alsoAsks`. Needs-you `certificate_expiring` (one per upload, key `certificate_expiring|||<id>`) replaces
  `domain_cert_expiring` for those hosts. `IssuerUnavailable`/`UploadUnavailable`: `installed_without_issuer` (`--acme none`),
  `traefik_outdated`, `nomad_unreachable`.
- API 0.63.0: `putCertificateIssuer`, `deleteCertificateIssuer`, `listUploadedCertificates`, `uploadCertificate` (201),
  `getUploadedCertificate`, `deleteUploadedCertificate`, the settings' `issuerName`, `acme`, `issuerUnavailable` and
  `uploadUnavailable`, `fingerprint` and `uploaded` on a certificate, the attention kind and the webhook's `certificate` and
  `names`, codes `issuer_unavailable`, `certificates_unavailable`, `too_many_certificates`, `certificate_not_found`; the two
  forms answer field errors with 422 like `putCertificateSettings`. Admin only; deploy tokens are denied by the default deny.
- Console (`internal/ui/certificates.go`, `templates/settings.html`): Settings › Certificates has the Uploaded certificates
  section (`POST /settings/certificates/upload`, `/uploaded/{id}/remove`; a refusal draws the certificate box back without any
  key block, the key box never) and Change issuer (`POST /settings/certificates/issuer`, `?preset=google|zerossl|digicert#issuer`
  fills the URL, the HMAC field is never filled and empty keeps it, the CA is sent only when typed or "remove" is ticked).
  Domains name the issuer through `Service.IssuerName` (SQLite and `CARAVANA_ACME` only, no Nomad read per render). No
  designer pass.
- Lab facts: Traefik asks ACME for every name of a router when one isn't covered, so a router mixing covered and uncovered
  hosts gets one ACME certificate for all, and an exact name beats a wildcard in Traefik's store (R4, R5). The same happens
  to a router added in the seconds before Traefik picks an upload up. The installer drops ACME certificates whose every name
  an upload covers (`drop_covered_acme_certificates`, python3) and says Traefik restarts once for 034.
- Tests: `cmd/caravana/traefik_conf_test.go` renders both jobs' templates; the gate step `certificates`
  (`gate_certificates_test.go`, lab only: a Pebble job requiring EAB with 20-day certificates, Caravana restarted with
  `CARAVANA_CERT_RESOLVER=letsencrypt` through the lab's `SetEnv`, the issuer's refusals, a Pebble certificate and its renewal
  after a Traefik restart, a private-CA wildcard serving two hosts with no ACME order, `certificate_expiring`, the fallback,
  no HMAC or key in any answer, log or data file) passed on 2026-11-24 (74 s, `-run 'TestLab/^(api-needs-token|certificates)$'`).
  `TestConsoleLab` has a step `console-certificates` (`console_certificates_test.go`: a refused upload drawn at `key` with no key
  back, a private-CA wildcard for `*.cert034.localhost` uploaded through the form, Traefik serving it before the app's domain is
  added, the Domains row saying "uploaded:", Settings' "Serves", Remove), which passed the same day (28 s, `-run
  'TestConsoleLab/^console-(sign-in|certificates)$'`); its host is under `.localhost` so the row is live without DNS.
  The lab's Traefik must be the 034 job: `deploy/dev/lab.sh up` from this branch. Lima forwards the VM's port 14000 to the
  Mac's localhost, which the in-process Caravana's directory check uses. `install-test.sh` has `check_certificates_host`
  (Traefik's policy, a private-CA upload served on 443, a covered fake ACME certificate dropped by a rerun, removal) and
  `check_old_traefik_host`; `ONLY=certificates` runs just them in about 4 minutes. The whole run passed on 2026-11-25 on
  Ubuntu 24.04 (24 min), after a fix: every rerun said Traefik restarts, because `grep -q` under `pipefail` can fail a
  pipeline through SIGPIPE, so the installer reads the job before grepping it.

Feature 035 (`specs/035-app-templates`, from a CodeAlta task) is implemented on the worktree branch
`alta/fresh-kestrel-ng5c`: New app › From a template installs a known app from a few answers. A template is
a manifest plus a Compose file with `{{placeholders}}`; installing it is a normal Compose deploy of a new app.
- `internal/templates` is pure (no network, no disk but an `fs.FS`, randomness only from an `io.Reader`): the
  format (`ParseCatalog` names template and field for every rule of `contracts/template-format.md`), the inputs
  (`domain`, `email`, `text` with a pattern, `secret`; every plain value must fit the Compose-safe set
  `^[A-Za-z0-9._@+:/=-]{1,253}$`), generated secrets (`Generate`, `crypto/rand` in production, rejection
  sampling), `Render` (plain values in the Compose text, any value in the overlay's string leaves, a secret-valued
  name only in a service's `secrets[].value`, so no secret is ever in the file Caravana keeps and shows),
  `Check` (the rendered file through `compose.Translate`, **any warning is an error**, then `nomadjob.Check` and
  `Translate`; no floating tag, no `POSTGRES_PASSWORD` in plain env), `ReadSource`/`Sums` for the catalog
  repository's layout, and the embedded catalog (`internal/templates/catalog/`, made by `caravana template
  bundle`; a test checks its sum and runs `Check` on every template).
- The catalog is its own repository, `Cosmic-Chimps/caravana-templates` (cloned beside this one): sources in
  `templates/<name>/`, CI running `caravana template check .`, a release workflow that uploads `catalog.json`
  and `SHA256SUMS`, and `lab/install-all.sh`. Six templates: `uptime-kuma`, `hollo`, `postgres` (its password is a
  secret input: a generated one is never shown, so nobody could log into Adminer), `adminer`, `umbral`,
  `codemagic-patch` (`lab: false`: it needs a private image). Health paths were found by running the images
  (Uptime Kuma's `/` redirects, so `/robots.txt`; Hollo `/nodeinfo/2.1`). No template sets `requires`.
- `Service.InstallTemplate` checks the place and the inputs, makes the generated secrets, renders and calls
  `DeployComposeIfResult` with `Condition{IfNew: true}`, then records the template and its version beside the
  app (`apps.template_name/_version`, migration 0028, not in the revision), also when the deploy fails after the
  app was saved. A refusal at a domain host that came from an input is renamed to `inputs.<name>`.
  `TemplateInstallOptions{WithoutBackups}` leaves the template's backup policies out and adds one warning per
  service on `services.<svc>.backup`: the owner's explicit choice on an installation with no backup
  destination, since a backup without one is refused as for any deploy.
- The catalog in use is the stored verified bundle when newer than the built-in one, else the built-in one.
  A check (daily with jitter, first a minute after start, and Check now, not within 10 minutes of the last)
  reuses the release check's `get`, `checkFailure` and too-soon/disabled refusals: `SHA256SUMS` first,
  `catalog.json` only when its sum changed, verified, parsed and run through `CheckCatalog`. The request has
  the User-Agent only. Off (`template_catalog.checking`) means no request. `CARAVANA_TEMPLATES_URL` is for tests
  and the lab.
- API 0.37.0 (033 took 0.36.0): `listTemplates`, `getTemplate`, `getTemplateLogo`, `previewTemplate`,
  `installTemplate`, `checkTemplates`, `putTemplateSettings`, `AppRecord.template`/`AppSummary.template`, codes
  `template_not_found` and `template_unavailable`; a field error is 400 as everywhere. Deploy tokens are
  denied by the default deny. MCP: `list_templates` and `get_template` (no install tool: it would carry secret
  answers through an agent). The API's JSON encoder writes a zero number under `omitempty`, so
  `app.Backup.Keep` and compose's `Port`, `Interval` and `Timeout` are `omitzero`.
- Console (`internal/ui/templatespage.go`, `templates/template.html`, the grid in `newapp.html`): the fourth
  tab (links and a GET form, no script), tags, the footer with Check now and the switch, the logo route (served
  as an image with a CSP that runs nothing), the page of one template (what it makes, the passwords, the
  notes, the inputs, Preview, Create; when there is a backup and no destination a notice and the checkbox "Install
  without backups for now"), the installed notice and notes on the Deployments tab (the plain answers travel in
  `?v_<input>=`, and each is checked again before use; never a secret), and Configure's "Template · title
  version". A typed secret input is read from the post, handed to the service and dropped; the form is drawn
  without it. Sentinel tests in `service`, `api` and `ui` search every answer, page, address, kept file, Nomad
  job, log and the SQLite file for a generated secret and a typed one.
- Lab: `TestConsoleLab` has a step `console-templates` (Uptime Kuma with only a domain, healthy, volume
  backup policy on, `template` on the record, answers through Traefik, the same domain refused at its input) and
  the gate a step `templates` (Hollo through the API: both services healthy, one password in two services'
  Nomad Variables and in no answer, Back up now of Postgres succeeds). Both passed in the lab on 2026-11-12
  (74 s each, run alone). A stale Nomad Variable `caravana/registries/ghcr~io` left by an earlier review made
  every `ghcr.io` pull in the lab 403 (the pull reads the Variable whether or not SQLite has the entry); it was
  deleted. The lab VM has 2 GiB.
- Not in 035: templates from other catalogs or with signatures, a template editor, logos (the console draws a
  monogram tile), the Continuwuity template (waits for tasks 07 and 09), installing from MCP.

Feature 036 (`specs/036-scheduled-restores`, from CodeAlta task `sgnc`) is implemented on the worktree branch
`alta/simple-ithaca-nr74`: a backup can be pinned under a label, and a service can restore the pinned backup on a
schedule (the public Umbral demo that starts each day from the same state). The contracts are `contracts/api.md` and
`contracts/console.md`; `plan.md` D1–D10 are the decisions.
- Pins are labels on backup rows (`backups.pin`, `pinned_at`, migration 0029, a unique partial index per
  `(app, service, label)`): `store.PinBackup` moves a label and returns the previous holder, `UnpinBackup`,
  `PinnedBackup`, `PinnedBackups`. The code says pin and label, never `pinned` alone (`service/deploy.go` has a type
  `pinned` for image digests). `restores` was rebuilt in the same migration with the status `skipped`, a `trigger`
  (`owner`, `schedule`; old rows read `owner`) and the `pin` a scheduled run asked for; `LatestBackupRuns` ignores
  skipped restores, so a skipped slot never hides a failure.
- Retention of every engine is told the pinned timestamps, as unix seconds separated by spaces, and removes them
  from the candidates before counting `keep`. Postgres: item `BACKUP_PINNED` of the backup job's variable
  (`SyncBackups` writes it, `Deployer.SetBackupPins` rewrites only that item); volume: item `PINNED` of each backup
  request (`volumeRequestKeys`); sqlite: the sixth argument of `SQLiteUploadScript`. A list that isn't digits and
  spaces deletes nothing and says so. Each script is tested under a real `sh` with a fake `rclone` (the Postgres
  upload script got its first test, with a fake GNU `stat`). The script text is part of every translated job, so
  the volume, sqlite and backup goldens changed, and an app gets the pin-aware retention at its next deploy.
- `service.PinBackup` / `UnpinBackup` (`backuppins.go`) take the app's deploy lock with `TryLock` (`ErrAppBusy`, 409
  `app_busy`) and refuse while a backup or restore of the service runs (`ErrBackupRunning`), so no retention runs
  with a pin list older than the store's; the store is written first, then the Postgres job's variable, and put
  back when Nomad refuses (502). Volume and sqlite runs read the pins in their `prepare*` step, under the claim,
  and refuse the run when they can't be read.
- The policy field is `app.Backup.Restore{Schedule, From}` (`omitempty`, so no existing `revision` moved), validated
  for every engine (`<field>.restore.schedule` is five-field cron that `cronexpr` parses, `.from` is
  `app.ValidLabel`); a Compose overlay reuses `*app.Backup`, so its errors read `services.<svc>.backup.restore.*`.
- The restore clock (`service/restoreclock.go`) runs inside the volume-backup scheduler's pass, before the backups
  (`SetBackupScanGap` shortens the 30 s gap for the lab). Each due slot restores the pinned backup through
  `restoreBackup`, the body `Restore` shares, recorded with trigger `schedule` and the label; a refusal (no pin,
  expired pin, engine changed, no copy, no destination, Nomad's error) is a failed record, a busy service is a
  `skipped` one that isn't tried again, and a stopped app leaves nothing and waits. The slots count from the later
  of the newest scheduled restore (any outcome) and the app's last save, so saving a new schedule never runs a slot
  that already passed (`store.SetClock` lets tests save an app at a fake time); a slot missed while Caravana was
  down runs once if it is under an hour late (`dueSlot`). `pg_restore` runs with `--single-transaction`, so a
  restore that fails partway leaves the database as it was. A deploy whose `from` has no pinned backup warns on
  `services[i].backup.restore.from`; `restore_failed` is unchanged and its detail is the record's message.
- API 0.39.0: `pinBackup` (PUT) and `unpinBackup` (DELETE) on `/v1/apps/{id}/backups/{backupId}/pin`, `Backup.pin` /
  `pinnedAt`, `Restore.trigger` (required) / `pin` and the status `skipped`, `BackupPolicy.restore`; deploy tokens
  are denied by the default deny. No new table or module.
- Console (`internal/ui/backups.go`, `templates/app.html`): "Pin…" and "Unpin" on a run (`pinned golden` in its meta
  line; a pinned run is always listed), "7 backups, plus 1 pinned", the line "Restores golden every day at 04:00
  UTC · last: …" with a note while no backup carries the label, "Restore on a schedule…" with "Stop restoring"
  (`POST /apps/{id}/backups/restore-schedule`, which calls the new `Service.SetRestoreSchedule`, since
  `SetBackupPolicy` refuses a service that has a policy), restores that say "by you" or "on schedule from golden",
  and the two fields of the Compose form (kept through the draft). Forms work without JavaScript.
- Lab: the gate's `volume-backup-restore` and `sqlite-backup-restore` steps pin a backup as `golden`, change the
  data, deploy a restore schedule two minutes ahead (`gate_restore_schedule_test.go`) and wait for the clock's
  restore; the sqlite step also shows the pinned backup outliving five more backups with `keep: 2` (record and
  object). `startLab` sets `SetBackupScanGap(10 s)`. Both passed on 2026-11-13 (359 s and 250 s). A sqlite backup
  right after a restore can lose its exec output (Nomad reports the exit first; `main` does it too, CodeAlta task
  `phpv`), so that step may need a second run. The Aspire side is `WithCaravanaScheduledRestore` on branch
  `scheduled-restore` of `../aspire-hosting-caravana` (not pushed).

Feature 037 (`specs/037-secret-providers`, from CodeAlta task `ptfn`, the 2026-11-11 secrets talk) is implemented
on the worktree branch `alta/lucid-reprise-qgp5`: a secret may be a reference to a secret provider, Bella Baxter
first. `plan.md` D1–D14 are the decisions, with a Deviations section.
- `app.Secret.From *SecretRef{Provider, Path}` (`from`, omitempty, so no revision moved); a value and a `from`
  together are a field error on `…secrets[j].from`. `WithoutSecretValues` (app and compose) and `withSecretNames`
  keep `from`. A Compose overlay takes it too.
- `internal/secrets/source.go` is the outside-provider seam: `Source` (`Read(ctx, paths)`, `Probe`),
  `ErrSourceUnavailable`, `ErrSourceRefused`, `SourceError` (a sentence in Caravana's words, never a library's
  text); `Build(a, current, resolved)`. `secretstest.Source` is the fake. The existing `Provider` is still the
  Nomad-Variables store.
- `internal/secrets/bella` is the one importer of Bella's Go SDK, `github.com/cosmic-chimps/bella-baxter-go`
  `v0.1.1-preview.146` (new module, "Bella's own SDK; we use what we build"; it brings the Kiota packages and the
  OpenTelemetry API). It always presents the private key (the SDK falls back to `BELLA_BAXTER_PRIVATE_KEY`
  otherwise), reads one environment with one bulk call, and classifies errors by type and status only (Kiota's
  404/502 come as `*models.ProblemDetails`, which embeds `ApiError` by value: match `abstractions.ApiErrorable`).
  `Auth` is pluggable (`APIKey` today). `bellatest` is a fake Bella speaking the SDK's HMAC and E2EE envelope.
- Providers: migration `0030_secret_providers.sql` (`secret_providers`: name, kind `bella`, settings JSON, allowed
  places, last test; `deployments.refresh`). Credentials in the Nomad Variable `caravana/secret-providers/<name>`
  (`api_key`, `private_key`). `service/secretproviders.go` (save, list with `usedBy`, delete refused while used,
  test), `SetSecretSources(service.BellaSource)` in `cmd/caravana`.
- Resolution (`service/secretrefs.go`): `checkSecrets` checks each reference's provider exists and allows the app's
  place before anything is saved (400 on `…from.provider`); `deployWarned` reads each provider once under the app's
  lock, before the sets are replaced: a missing path is 422 `secret_not_found` (`…from.path`), a refusal 422
  `secret_provider_refused`, the provider down 502 `secret_provider_unavailable` (`…from`), via
  `*service.ReferenceError`. Running copies and restarts never call Bella. Rollback reads today's values.
- Refresh secrets (`service/secretrefresh.go`): re-reads the live version's references; changed → a redeploy of the
  live version with its digests pinned, recorded `Refresh` with `source`; unchanged → nothing.
  `ErrNoSecretReferences` (409 `no_secret_references`).
- API 0.40.0: `from`, `listSecretProviders`, `putSecretProvider`, `deleteSecretProvider`, `testSecretProvider`,
  `refreshSecrets` (deploy tokens may call it; every provider route is denied to them), `secretsRefreshed`.
  Console: Settings › Secret providers (`/settings/secret-providers`, keys write-only), the Variables page's
  "From a provider", reference rows, the "Where secrets come from" panel and Refresh secrets, "Refreshed secrets"
  in the history, the rollback note, read-only references in the Compose form. `docs/secret-providers.md`.
- Lab: `startLab` runs a `bellatest` server on loopback (`gateTarget.Bella`, nil on a real host, where the steps
  skip). The gate step `secret-references` and the console step `console-secret-providers` passed on 2026-11-13
  (50 s and 53 s, run alone). A check against a real Bella environment is the maintainer's.
- Not in 037: Bella's change webhook (it will call Refresh), workload-identity auth (task 06), other providers, an
  MCP refresh tool, emitting references from the Aspire publisher.

Feature 038 (`specs/038-workload-identity`, from CodeAlta task `j37f`) is implemented on the worktree branch
`alta/hushed-apricot-zvbx` (stacked on 036): a service may ask for Nomad workload identities, and the cluster
publishes its issuer, so an app proves who it is (to Bella Baxter's Nomad attestor, Bella's spec 081) with no
credential stored in Caravana. Operator doc: `docs/workload-identity.md`. No migration, no module, no policy change.
- Model: `app.Service.Identities []app.Identity{Name, Audience, TTLSeconds}` (`identities`, omitempty, so no
  revision moved; at most 4; name `^[a-z][a-z0-9_]{0,29}$`, not `default`, `vault_*` or `consul_*`; audience 1–4
  printable values; TTL 60–86400, 0 = 3600). `nomadjob.workloadIdentities` gives the service's task (and a schedule
  run's) one `identity` block each with `file = true` (golden `identity`, written by the HCL export). `env` isn't
  offered: Nomad doesn't refresh the variable on renewal. The Compose overlay takes `identities` and the console's
  Compose form carries them (`keepIdentities`, like `keepDomains`); a promotion keeps the target's (an audience
  belongs to an environment); Configure shows them read-only.
- Lab, observed (research R3, R4): the token is `/secrets/nomad_<name>.jwt` (`NOMAD_SECRETS_DIR=/secrets`), RS256,
  `aud` a string, `exp − iat` the TTL, `nomad_job_id` the app ID, `nomad_task` the service; Nomad rewrites it at
  about half the TTL; without an issuer it has no `iss` (Bella refuses it), and Nomad's openid-configuration is 404.
  The file can be empty for a moment right after the copy starts.
- Issuer: `Service.ClusterIssuer()` is the public URL when it is `https://host[:port]` (`mesh.CleanIssuer`, the one
  rule the console and the agent share). The node sync answer carries `issuer`, chosen by the pure `issuerFor` so
  Nomad servers take a new one one at a time (in-memory `issuerInFlight`, 2 minutes); the agent stores it
  (`State.Issuer`, reported back as `Report.Issuer`), renders `oidc_issuer` in `server {}` only and restarts Nomad
  on a server (a client restarts nothing). An answer **without** an issuer keeps the one a server has, so a
  rolled-back update never restarts every server at once. `node init-first --issuer` (the installer passes
  `https://<domain>`) renders it on a fresh host from the start.
- Discovery: `GET /.well-known/openid-configuration` and `/.well-known/jwks.json` on the main listener (`root` sends
  `/.well-known/` to the API), no credentials, passing Nomad's documents through (`deploy.NomadDiscovery`, 5 s, 64
  KiB, JSON only, `Cache-Control: public, max-age=60`); 404 `issuer_not_set` without an issuer, 502
  `nomad_unavailable` with a fixed message. API 0.41.0 (0.40.0 is 037's).
- Tests: the gate step `workload-identity` (`gate_identity_test.go`: signature against Nomad's JWKS, claims, a
  renewal, the token in no answer, log or data file, export with no diff; passed in the lab on 2026-11-14, 33 s) and
  `install-test.sh`'s `check_issuer_host` (passed 2026-11-14: a host from before the feature took the issuer with one
  Nomad restart, whoami 0 failures, and a copy's default identity still read its secret). Aspire: `WithCaravanaIdentity` / `WithCaravanaBellaIdentity` on branch
  `workload-identity` of `../aspire-hosting-caravana` (not pushed). The end-to-end `bella spiffe agent` against a
  real Bella needs an https issuer Bella can reach, so it is the maintainer's check on a real host.

Supported systems (CodeAlta task `rfhb`, 2026-11-14, on the worktree branch `alta/tropical-equinox-nf37`): the
installer accepts Ubuntu 24.04 LTS, Ubuntu 26.04 LTS, Debian 12 and Debian 13 (amd64 and arm64) and refuses
anything else before changing anything, naming the four (`SUPPORTED_OS` in `install.sh`; `/etc/os-release` is
read as text). The same packages serve every one (`iptables` and `iputils-ping` were added to the list: Debian's
images come without them, the CNI plugins and ufw need the first and the agent's network check pings with the
second), and the preflight refuses plainly when curl is missing for the public-IP lookup. Ubuntu 26.04 confines `wg` with an
AppArmor profile that reads only `/etc/wireguard/**` (lab, observed: `wg setconf` of a file in `/etc/caravana-node`
failed with "fopen: Permission denied"), so the agent's temporary WireGuard config is written there. The Updates page on a
host without Ubuntu's `updates-available` file counts with `apt list --upgradable` (`hostos.Apt`, run by the
sampler inside Caravana's sandbox when `hostos.AptStamp` changes: the apt lists, the dpkg status or apt's
`update-success-stamp`, whose mtime is `checkedAt`), names the host's system in its words (`hostos.Distro.Name`:
"Debian updates") and still says package names need Ubuntu's `pro`. `install-test.sh` takes `IMAGE=` (Lima's
`ubuntu-24.04`, `ubuntu-26.04`, `debian-12`, `debian-13`), reads the VM's distro ID, waits for the counts to be
known, writes and removes the reboot marker as root and refuses a fake Rocky 9 by name; `install-test-all.sh` runs the
four, one after the other. All four passed on 2026-11-15 (about 15 minutes each; Debian's counts came through
`apt list` inside the unit's sandbox, and its package names were refused plainly for want of `pro`). API 0.41.1
(text only).

Feature 039 (`specs/039-backup-schedule-never`, the Umbral demo's nightly reset) is implemented on branch
`039-backup-schedule-never`: `backup.schedule: "never"` takes no automatic backups, for every engine, and keeps Back
up now, pinning, retention of the backups taken by hand and `restore: {schedule, from}`. `app.BackupScheduleNever` and
`Backup.Automatic()`; an empty schedule still means `0 3 * * *`, and `ScheduleOrDefault` keeps returning the stored
word, so anything that turns it into a time checks `Automatic()` first (the volume and sqlite scheduler, the built-in
schedule's `NextRunAt` and words, `service.NoAutomaticBackups` for the console). `restore.schedule: "never"` is refused
naming `…backup.restore.schedule`, and a bad schedule's message mentions `never`. A Postgres backup job stays periodic
and enabled with the cron line `nomadjob.neverSpec` (`0 0 30 2 *`, a day no month has), because Back up now is
`Jobs().PeriodicForce`, which Nomad refuses for a disabled periodic job; lab check on Nomad 2.0.7 (2026-11-15): the job
registers, no child launches by itself in 90 s, and `PeriodicForce` starts a child that completes. No attention rule
judges a missed scheduled backup, so nothing changed there (a test keeps it so). API 0.42.0, console words on the
Backups tab, Configure and Schedules, and a Schedule select in the Backups tab's "no backup" forms. No migration, no
module, no policy change.

Feature 040 (`specs/040-schedule-in-copy`, CodeAlta task `5f39`, from the Umbral session) is implemented on the worktree
branch `alta/kind-chinchilla-zkvg`: a schedule may run its command in the service's running copy (`target: copy`) instead of a
fresh one, so a service with volumes can have a timed maintenance command.
- Model: `schedules.target` (`fresh`, the default and 022 unchanged, or `copy`) and `all_copies`; a run keeps its own `target` and
  `copies` (JSON array of copy IDs, `[]` for fresh), migration 0031. `fresh` still refuses a service with volumes; `copy` allows
  one. Every refused combination is a field error: `allCopies` with `fresh`, a `stopAfterSeconds` outside 60/120/300 (0 means 60)
  for `copy`, `memoryMB` other than 0, `skipIfRunning: false` (a copy schedule always skips a slot while its run goes).
- A copy run (`service/schedulecopy.go`) picks the oldest healthy copy (`pickCopies`: Nomad's create time, then the ID; every
  healthy copy with `allCopies`, one after the other, as one run row), writes the row ahead (`claimRun`, so 022's overlap, skip
  streak and `schedule_failed` stay one rule) and runs the execs in a goroutine, off the clock's pass. It registers no Nomad job.
  The run stays in `s.starting` (the watcher skips it) and has a cancel function in `s.copyRuns` (guarded by `runMu`): Stop, an
  app stop and a schedule's removal write the stop intent first and then cancel the exec. The limit is one deadline for the whole
  run (`runLimit`, so the lab's shortcut works). A copy run the watcher finds open that this process isn't running was left by a
  process that ended: it is settled `failed` "lost while Caravana was restarting" (or by its stop reason), with no Nomad call.
- Output is never kept (020.5's R2): the exec's streams are dropped as they come back, so `output_kept` stays 0, `stdout`/`stderr`
  stay NULL, `GET …/output` is 404 `output_not_kept`, and no log line, error, notice or page holds them (a sentinel test searches
  rows, the SQLite file, the log, the answers and the feed). Each copy gets an `app_actions` row `run` with trigger
  `app.TriggerSchedule` ("schedule", "on a schedule" in the console and the feed; not a deployment trigger). Outcomes: exit 0
  `ok`, "exit N" (with `allCopies` "exit N in copy 1a2b3c4d", the first failure; the others still run), the limit
  `stopped_at_limit`, an exec Nomad can't start `could_not_start`, a vanished copy or Nomad down `failed` in `execFailure`'s words.
- The clock: a copy schedule's slot while the app is stopped is a `skipped` row "the app is stopped" (`skipSlot`), where a fresh
  schedule's slot waits as before; a slot with no healthy copy (`ErrNoRunningCopy`) or a deploy going waits like 022's, until it
  is an hour old. `schedule_skipping` is never raised for a stopped app.
- API 0.43.0: `target`, `allCopies`, `copies`, `schedule` as an action trigger, `runSchedule` can be 409 `no_running_copy`.
  Console: New schedule has "Run in" (radios) and "In every copy"; Stop after is one select with 1, 2, 5 min and 30 min to 2 h (a
  wrong choice for the target is a field error at it), the running copy's form doesn't send Memory or the skip box (the hints say
  so), the list and detail say "in the running copy", and the run page says where it ran, the exit code, and "This run was in the
  running copy: its output isn't kept." "Usual" (0) is the first Stop after choice and what a new form selects: 30 min for a
  new copy, 1 min for the running copy.
- Lab: the gate step `schedule-in-copy` (`gate_schedule_copy_test.go`: a busybox service with a volume; `purge` every minute
  appends to a file on the volume, prints a sentinel on both streams that its command doesn't hold whole and exits 3, `slow`
  sleeps 100 with a 120 s limit; the run is `target: copy`, in that copy, `failed` exit 3, `outputKept` false, the output read is
  404 `output_not_kept`, the file grew, `schedule_failed` is on `/v1/attention`, `listAppActions` has a `run` with trigger
  `schedule`, `slow`'s next slot is `skipped`, and after Stop app the next slot is `skipped` "the app is stopped"; the sentinel
  is in no answer, log or data file) and the console step `console-schedule-in-copy` (`console_schedule_copy_test.go`: the form
  with Run in = the running copy and the Usual limit, Run now, the run page). Both passed in the lab on 2026-11-16 (gate step
  161–195 s over three runs, console step 27 s; run the console step with `-run
  'TestConsoleLab/^console-(sign-in|schedule-in-copy)$'`). The lab stops every run at 40 s (`SetScheduleLimitForTests`), so the
  gate step lifts it with `gateTarget.SetScheduleLimit` while it runs; `scheduleLimit` is atomic for that. One earlier run saw
  an exit 3 read as 0 once (Docker's exec-inspect race, as for Run a command) and did not repeat.

Service DNS (CodeAlta task `dn53`, worktree branch `alta/subtle-barque-48jk`): a service may set its resolvers,
`app.Service.DNS *app.DNS{Servers, Searches, Options}` (`dns`, omitempty, so no revision moved; at most 3 IP-literal
servers without a zone, 6 lowercase search domains, 8 options like `ndots:2`, no duplicates, an empty object refused;
`app.CheckDNS` takes the three field paths so Compose names its own). `Translate` puts it on the group network's `dns`
block (Nomad writes the copy's resolv.conf; no file mount), and so does `ScheduleRunJob`; the HCL export writes it
(golden `dns`). Compose's `dns`, `dns_search` and `dns_opt` map to it (errors name `services.web.dns[0]`); the overlay
has no `dns`. A promotion takes the source's (`dns` in `Changed`); Configure shows it read-only. The names sidecar's
`/etc/hosts` still resolves the app's services beside it. API 0.44.0 (`ServiceDNS`). Gate step `service-dns`
(`gate_dns_test.go`: a hostname server refused at `services.web.dns[0]`, the job's dns block, resolv.conf, names beside
it, export) passed in the lab on 2026-11-16 (37 s).

Feature 041 (`specs/041-published-ports`, CodeAlta task `dfn8`, worktree branch `alta/swift-tiger-phd5`): a service's port may
be published, a fixed TCP or UDP port on the first server's public address (a TURN server, LiveKit, a game, MQTT). Constitution
1.2.0 amends VI for it. `plan.md` D1–D13 are the decisions; `research.md` R1 and R2 are the lab facts.
- Model: `app.Port{Protocol, Published}` (`protocol` omitempty, `published` omitzero, so no revision moved; `""` means tcp,
  stored as sent), `Port.SpeaksTCP`, `Service.Publishes`, `app.ReservedPorts` (22, 80, 443, 4646–4648, 7070, 7071, 8081, 51820),
  `MaxPublishedPorts` 16 (`internal/app/published.go`). Refused naming the field: unknown protocol, number out of range or
  reserved, a number published twice in the app, more than 16, `replicas` above 1, a domain or the health check (named or
  implied) on a port that doesn't speak TCP. `mesh.PublicHostNetwork = "public"`.
- Nomad (lab, observed, 2.0.7): Nomad maps **both** tcp and udp for every port, so `protocol` only says what the app speaks.
  `Translate` puts a published port in the group network's `ReservedPorts` (`Value`, `To`, `HostNetwork: "public"`), the HCL
  export writes `port "x" { static = N  to = M  host_network = "public" }` and its header names the network. A group that
  publishes deploys stop-then-start like one with a volume (`stopThenStart`). The mapping lives exactly as long as the copy,
  so Caravana writes **no firewall rule**: the mapping is a DNAT rule before ufw's input rules (research R2).
- Flow: `service.checkPortsTaken` (next to `checkDomainsTaken`; every other stored app, stopped ones too, claims its numbers;
  `PortTakenError` → 409 `port_taken` with the field), `deploy.checkPublicNetwork` in `Register` before `ensureVolumes` (the
  ready, eligible nodes whose `Nodes().Info` has `HostNetworks["public"]`; none → `PublicNetworkError` → 409
  `no_public_network` on the first published port, and the failed deployment is recorded like any `Register` failure, so
  the app is stored and holds its number). A publishing service's new volumes go to a public node; volumes that exist
  elsewhere refuse on the port's field. A promotion keeps the target's published number per port name (the source's
  `Changed` ignores it) and a service new to the target arrives unpublished; Copy to clears every number and the kept
  overlay's `publish`.
- Compose: `ports` and `expose` take udp (`3478/udp`); a tcp and a udp entry for one container port make one `tcp+udp` port (tcp
  stays `""`, so a file without udp makes the same app as before). The file's `published` only warns (the message now names the
  overlay entry to add); `Overlay.Publish []Publish{Port, Published}` publishes (`published` 0 = the same number), an unknown
  container port is `services.<svc>.publish[k].port` and a model refusal of the number is renamed to
  `services.<svc>.publish[k].published` (`compose.PublishedInOverlay`, also for the 409s in `service.inFile`).
- Console: Configure shows a service's published ports read-only (`publishedRows`: "Published ports", "Open on the address of
  server <name>", "Set through the API or the Compose overlay"; `publishHost` reads the managed ingress server), a save keeps
  them (`applyEdit` only touches `ContainerPort` of a single port), and the Compose form keeps the overlay's list
  (`keepPublish`, like `keepIdentities`). No editor in the console.
- Hosts: `nodeagent.State.PublicInterface` (`publicInterface`, omitempty, validated), set by `InitFirst` from `ip -4 route show
  default` (the first line's `dev`; no route keeps what the state has), rendered inside the first server's `client {}` as
  `host_network "public" { interface = ...; cidr = "0.0.0.0/0" }` (`first-public` and `client-public` goldens; the older goldens
  are unchanged and a joined server never renders it). `install.sh` needs no change: its rerun runs `init-first` and restarts
  Nomad once when the config changed. `deploy/dev/nomad.hcl` declares the same network on `eth0`, and `lab.sh up` installs a
  changed `nomad.hcl` into an existing lab VM and restarts Nomad (no `delete`).
- API 0.45.0: `Port.protocol`, `Port.published`, the overlay's `publish`, codes `port_taken` and `no_public_network` (409, with the
  field). Deploy tokens may deploy them for their own app. `docs/published-ports.md`.
- Tests: the gate step `published-ports` (`gate_published_test.go`: refusals, an `alpine/socat` echo on a `tcp+udp` port
  published as 34478 deploying healthy with the static port on `public`, TCP and UDP answered from another copy at the address
  Nomad recorded (and, on a real host, from the test process through the firewall), `port_taken` for a second app, the export
  with no diff, and the port closed after the app is deleted) passed in the lab (36 s, run alone with
  `-run 'TestLab/^(api-needs-token|published-ports)$'`). `install-test.sh` has `check_published_host` (not run in this session).
  The lab VM must have the network: run `deploy/dev/lab.sh up` once from a branch that has the new `nomad.hcl`.
- Not in 041: port ranges (coturn's relay range needs one published port each), published ports on joined servers (a proxy per
  server, Phase 3), IPv6, a console editor, a firewall rule.

Feature 042 (`specs/042-placement-and-drain`, CodeAlta task `jcfq`, worktree branch `alta/stout-canyon-xdrv`): server
labels, placement rules per service, and moving work off a server. `plan.md` D1–D14 are the decisions; `research.md`
R1–R4 the lab facts (dynamic node metadata reaches Nomad's servers 8–10 s after the client takes it; dotted meta keys
work in constraints; `!=` passes on a node without the key; why Nomad's own `node drain` isn't used).
- Labels: `servers.labels` (JSON, migration `0032_placement_drain.sql`) written to the node as dynamic metadata
  `caravana.label.<name>` plus `caravana.server=<name>` on every managed node (`deploy.SetNodeLabels`, one write that also
  unsets stale keys, then a wait of up to `Timing.LabelWait` 15 s for the server-side record; `ErrLabelsPending` isn't a
  failure). `service/serverlabels.go` reconciles at start, when a server becomes ready and every 5 minutes
  (`SetLabelReconcileGap`); `labelMu` serializes a save and the reconcile. `ServerView.Labels`, `LabelsApplied`.
- Placement: `app.Service.Placement{Require map, Prefer []Preference{Label, Value, Weight}, Avoid []string}` (omitempty;
  limits 8/8/16; label names `^[a-z][a-z0-9-]{0,31}$`, values `^[A-Za-z0-9._:/-]{1,64}$`; `app.CheckLabels` for a server's
  16). `nomadjob.placementBlocks`: constraints (require sorted, then `${meta.caravana.server} != name` per avoid, sorted)
  and affinities (prefer in order, weight 50 by default) on the group and on a schedule run's group; the HCL export
  writes them (golden `placement`). `deploy.checkPlacement` in `Register`, only for apps with rules: `Jobs().Plan`, and a
  group filtered by Caravana's constraints alone (no `DimensionExhausted`, no `NodesExhausted`) is `*PlacementError` →
  409 `placement_impossible` on `services[i].placement.require.<label>` or `.avoid`; lack of room is still no refusal.
  Nomad adds its own implied constraints to a job it reads back, so tests look only at `${meta.caravana.` ones. A
  promotion takes the source's rules (`placement` in `Changed`); Copy to keeps them. The Compose overlay takes
  `placement`.
- Move work off (`service/servermove.go`): `MoveWorkOff(name, deadline)` (1 min–2 h, default 15 min) refuses a server
  that isn't ready, one with a move going (`server_moving`) and a cluster with no other node that is ready and
  eligible (`no_other_server`); then `SetNodeEligible(false)`, a `server_moves` row and a goroutine: each round lists the
  node's copies (`AllCopies`), `classifyCopies` (pure) sorts them into movable and stays (volumes "its data is on this
  server", a published port, `traefik` "the proxy runs here", helper jobs with `_` "a run that ends on its own", a
  service whose rules allow no other server, anything else "not deployed by Caravana"), and each movable copy is moved
  under its app's lock with `deploy.MoveCopy` (`Allocations().Stop`, Nomad's stop-and-reschedule) and
  `WaitCopyMoved` (the replacement is the running copy whose `PreviousAllocation` is the old one, on another node,
  checks passing; `copyWait` 5 min). End states `done`, `incomplete` (the deadline; needs-you `server_move_incomplete`
  while the node is still ineligible), `cancelled`; a process shutdown leaves the row open and `resumeMoves` goes on
  at start. `AcceptWork` cancels a move (waits for its row to settle) and makes the node eligible. The node stays
  ineligible after `done`. `PreviewMoveOff` is the confirmation page's read.
- Needs-you: `copies_unplaced` (a service with rules whose job has queued copies, from `Jobs().List` summaries,
  `deploy.QueuedCopies`; not judged while Nomad can't be read) and `server_move_incomplete`.
- API 0.46.0: `putServerLabels`, `drainServer` (202), `undrainServer`, `Server.labels/labelsApplied/move`,
  `Service.placement`, the overlay's `placement`, codes `placement_impossible`, `server_moving`, `server_not_ready`,
  `no_other_server`. The policy gains `node { policy = "write" }` (rerun the installer).
- Console: the server page's Labels row (chips, one text input, `hx-preserve`), Move work off… with
  `/ops/servers/{name}/move` (what moves, what stays and why, the one-copy note, a deadline select), the move panel
  (first in the body, refreshed on `servers`), Accept work again; the list's labels; Configure's Placement panel per
  service (sentences, and the form Require/Prefer/Never on posting to `/apps/{id}/services/{svc}/placement` as a
  deploy with the page's revision; a kept-file app is read-only and the Compose form keeps the overlay's rules).
  `docs/placement-and-drain.md`.
- Lab: the gate step `placement` (the refusal naming the field and no job, avoid and prefer healthy with the blocks on
  the group, export with no diff, the refusal over a running app leaves its version, the one-server refusals) passed
  2026-11-17 (17 s, `-run 'TestLab/^(api-needs-token|placement)$'`); the console step `console-placement` (Configure's
  form: an impossible rule drawn at Require, a preference saved and cleared). `deploy/dev/cluster-test.sh` gained steps
  6b (labels on two, a required label places on two only, an impossible rule is 409 naming the field, prefer lands on
  two, avoid keeps copies off three, removing the label makes a redeploy refused while the copies keep running) and 6c
  (a two-copy app and a volume app pinned to two; drain two while `spread.test` is called every half second; the
  movable copies leave, vol2 and req are listed as staying with their reasons, Accept work again). It passed on
  2026-11-17 (17 min; 0 calls failed during the move). Its first run found research R5: a new volume of a service
  with rules was created on any node and the copy stayed queued, so `ensureVolumes` now creates it with the
  service's placement constraints (Nomad picks a node that fits) and refuses on `services[i].placement` a volume
  that already sits on a server the rules forbid (`nomadjob.PlacementConstraints`, `nomadjob.AllowedOn`). A copy
  that didn't move can fail a check for a moment while the VMs place the others, so the step waits for health.
- Not in 042: node pools, other constraint operators, placement by resources, moving a volume, Nomad's own drain, a
  scheduler settings page, labels from the installer.

Nomad's event stream (CodeAlta task `f6r6`, worktree branch `alta/balmy-bunting-7wtt`): one subscription replaces
the fast polls. No API, page, migration, module or policy change (the token's `read-job` and `node` rules cover the
topics).
- `deploy.FollowEvents` reads the topics Job, Allocation, Deployment and Node, takes only job IDs out of the payloads
  (`EventBatch{Copies, Deployments, Rollouts, Jobs, Nodes, Resync}`), coalesces what is queued, resumes from the last
  index plus one, backs off after a failure, and renews each connection after `Timing.StreamLifetime` (10 min; the
  client swallows Nomad's heartbeats, so a dead connection would go unseen). Any connect that isn't a renewal hands
  out `Resync`. `Rollouts` leaves out Nomad's per-copy health updates (type `AllocationUpdated`) unless they carry
  a final status. Nomad replays its buffer (100 events) on index 0 too (lab, observed).
- `service/events.go` turns batches into notices (copies; deployments only for an app whose copies didn't change in
  the same batch, since both regions refresh on either; servers) and wakes the watchers through `wakeHub`
  (`wake.go`, the closed-channel broadcast; a waiter takes its channel before it reads). A Resync wakes everything
  and publishes copies and deployments for every app. `deploy.WatchCopies` and the subscriber-driven watcher are gone;
  `deploy.Watch` became `RolloutOutcome` (one plain read) and the loop is `Service.awaitRollout`.
- Safety nets: a rollout is read at least every 30 s (`rolloutResync`), schedule runs every 30 s
  (`scheduleWatchGap`, plus the earliest due time from `runDue`: a limit, a start deadline, a stop's grace), backups
  every 2 min (`backupPoll`), a Logs place's copies every 30 s (and on a copies wake, at most once per 2 s). The lab
  no longer shortens the run watcher.
- Lab, 2026-11-18, through a counting proxy: 20 apps with a Postgres backup policy, one page open, 180 s idle went from
  297 to 77 Nomad requests; three deploys with a page open from 435 to 221 (the stream adds about 80 KB per deploy of
  allocation payloads). A killed copy reached the Running tab's stream 0.4 s after `docker kill`; a Nomad restart
  logged the loss once, reconnected with a Resync 7 s later and the next deploy was followed normally.

Feature 043 (`specs/043-autoscaling`, CodeAlta task `97ks`, worktree branch `alta/flowing-sequoia-942b`): a service
adds and removes copies from Caravana's own metrics. `plan.md` D1–D13 are the decisions; `research.md` R1–R3 the lab
facts. Operator doc: `docs/autoscaling.md`.
- Model: `app.Service.Scale{Min, Max, Target{CPU | RequestsPerCopy | P95}, CooldownSeconds}` (`scale`, omitempty, so no
  revision moved; 1–20 copies, cpu 10–200 % of the reservation, requests 1–100000 a second per copy, p95 10–60000 ms,
  cooldown 60–3600 s, 0 = 300). `Replicas` is then the starting count and must lie in `min`–`max`. Refused naming the
  field: a service with volumes or a published port (`services[i].scale`), a requests or p95 target without a domain
  (`services[i].scale.target`). `Service.CountFor(current)` is the count a deploy registers. The Compose overlay takes
  `scale`; a promotion keeps the target's own (a service new to the target arrives without a proxy-measured one, like
  its domains); Copy to keeps it.
- Nomad (lab, observed, research R1–R2): `Jobs().Scale` makes a **new job version** and a short Nomad deployment, and
  updates the running copies **in place** to it; a register with `Count N, Canary N` keeps a scaled count blue/green.
  `submit-job` covers scaling: no policy change.
- The rule is pure, `metrics.DecideScale` (`internal/metrics/scale.go`): a count outside min–max goes to the bound;
  inside the cooldown nothing; only rows since the last change count; up when the newest 3 rows with a value (at least
  2) aggregate over the target; down when the newest 5 rows all have a value and the load on one copy fewer stays under
  80 % of the target with no row over it (p95: under half the target); one step at a time. `OverTargetAtMax` is the
  needs-you rule.
- The pass (`service/autoscale.go`) runs after each `sampleTick` (its own 30 s budget): per app that isn't stopped,
  `TryLock` the app's lock, skip a deployment pending or running and a Nomad deployment still going
  (`deploy.ScaleState`), write `service_counts` from Nomad, then at most one step per app with `deploy.Scale`, recorded
  as an app action `scale` (migration 0033 rebuilt `app_actions`: `from_count`, `to_count`, `reason`, `job_version`,
  `base_version`; trigger `app.TriggerAutoscale`). A refused step is a failed action and the cooldown counts from it.
  The reason is numbers only, worded by `scaleReason` from the samples × the sampler gap.
- A deploy keeps the count: `Register` translates a copy of the app whose scaled services have `Replicas =
  CountFor(the group's count now)` and returns `Registered.Counts`, which the service stores (`ReplaceServiceCounts`).
- Versions: `Deployer.VersionAliases` (set to `store.ScaleVersions`) maps a scale's job version to the version of the
  deployment it scaled in `Copies`, `AllCopies` and `LogCopies` (after `listed`), so the live version, the Running tab,
  Restart and the API's `Copy.version` read as deployments. `statusOf` takes the stored counts: a scaled service wants
  the count it has now.
- Needs-you `scale_at_max` (`service/attention_scale.go`, a warning, key `scale_at_max|app|service|`).
- API 0.47.0: `ServiceScale`, `ServiceScaleTarget`, the overlay's `scale`, the action `scale` with `fromCount`,
  `toCount`, `reason`, trigger `autoscale`. No new route or code. Console: Configure's Scaling panel
  (`internal/ui/scale.go`, `POST /apps/{id}/services/{svc}/scale` as a deploy on the page's revision, Stop scaling keeps
  the current count as `replicas`, read-only for a kept Compose file, `keepScale`), the per-service form leaves a scaled
  service's copies alone, Recent actions and the feed word the steps, Monitoring shows "2 copies (1–3)" and the place's
  last steps, the Running tab says how a service scales.
- Lab: the gate step `autoscale` (`gate_autoscale_test.go`: a whoami service with `requestsPerCopy: 5`, ~25 requests a
  second, 1 → 3 in about 2 minutes with `scale_at_max`, a deploy at 3 keeps 3 with 3 canaries and the live version stays
  the deployment's, back to 1 about 2 minutes after the load stops, exactly four steps, the count never outside 1–3,
  no call failed) passed on 2026-11-18 (301–328 s, `-run 'TestLab/^(api-needs-token|autoscale)$'`; 0 of 3623 calls
  failed). Research R5 (task 6rn5, lab, verified): Nomad registers a copy 0.7–2.6 s before its task starts and a
  scaled, moved or rescheduled copy has no canary tags, so Traefik routed to it early (0 to 31 502s of ~4000 before the
  fix). Every router of a service now ends with `<app>-<service>-retry` (`nomadjob.RetryMiddleware`,
  `retry.attempts=3`, in the service's tags): Traefik retries only a request that never reached the copy, on the next
  copy. An app gets it at its next deploy.
- Not in 043: scale to zero, steps over one, several targets or memory, scheduled scaling, services with volumes,
  Nomad's own autoscaler.

Network blips (CodeAlta task `ctbb`, worktree branch `alta/breezy-cuckoo-wt94`): a server cut off for a minute no longer
costs a rebuild of its copies. Operator doc: `docs/network-blips.md`.
- Every app task group has Nomad's `disconnect` block (`nomadjob.disconnectStrategy`): `lost_after` is
  `app.Service.Disconnect.LostAfterSeconds` (60–86400, default 300), `replace` is true unless the service has a volume
  or a published port (`stopThenStart`), `reconcile = keep_original`. A service with a domain gets a Traefik health
  check on its `health.path` (`healthCheckTags`), since Nomad keeps the registration of a copy it keeps and Traefik
  would send traffic to it; a service with a domain the proxy can't check (TCP check, another port) gets no block and
  refuses an explicit `disconnect`. Backup, restore and schedule run jobs have none.
- Nomad (verified in its source): a node that misses its heartbeats is `disconnected`, not `down`, while it has a
  copy whose `lost_after` hasn't expired; it stays `disconnected` until it reconnects, and the scheduler marks the
  copies `lost` after `lost_after`. `deploy.Node` keeps `Down()` (down or disconnected: unreachable) and gains `KeptCopies`
  (client status `unknown`, desired `run`), `DisconnectedSince` (the record's `StatusUpdatedAt`, read fresh, since
  `nodeInfo` caches 10 minutes) and `Holding()`; they are read only for a disconnected stub, and a failed read leaves them
  zero (the safe side: down).
- A copy with client status `unknown` is the copy status `disconnected` (`deploy.CopyDisconnected`, not terminal;
  `Copy.Reachable()` is false for it). It is no needs-you copy item and no crash (`consideredCopy`), is not healthy for
  the app's status (degraded or not running, not a failure), is refused by Run a command, the terminal and Restart, is
  skipped by a move, has no usage read and makes its service's CPU and memory samples a gap (so autoscaling waits), and
  is a `disconnected` phase on a server page. Volume runs already took only healthy or unhealthy copies.
- A server is `disconnected` (`ServerView.State`, `DisconnectedSince`, `KeptCopies`, via `nodeStateOf`) while its
  node `Holding()`: no work accepted, not counted ready, and **no `server_down` item**. Once the copies are lost it is
  `down` and the item opens as before. Remove stays allowed for both (it uses `Down()`).
- API 0.48.0: `Server.state` `disconnected`, `disconnectedSince`, `keptCopies`, `Copy.status` and the host node's state
  `disconnected`. Console: the servers list and page say "disconnected since <clock>, N copies kept running there" in the
  warning colour (the banner takes a `Clock`), never "down".
- Lab: `deploy/dev/cluster-test.sh` step 6d cuts server 3's mesh link for two minutes (the node is disconnected, its
  copies kept, the stateless app keeps its count, no `server_down`, the volume app's task isn't restarted, at most 15
  failed calls) and step 7 now waits `lost_after` before expecting down. Steps 1–7 passed on 2026-11-18: the blip cost 4
  failed calls of ~230, losing server 3 for good 3 (031's run: 11). Each cut has a second short window when the
  replacement registers: Traefik rebuilds the load balancer and counts the unreachable copy up until its next check
  (2 calls at 75–80 s, both runs), so steps 6d and 7 allow 4 late failures. Steps 8–11 haven't run to the end with the
  change: the runs that got that far stopped on the network (partial Nomad downloads), a stalled `limactl shell` and,
  in step 8, a boot ordering cycle between `caravana-mesh.service` and cloud-init that dropped `ssh.socket` (task `c6wd`,
  fixed below).

Boot ordering (CodeAlta task `c6wd`, worktree branch `alta/glowing-baker-ttss`): `caravana-mesh.service` has
`DefaultDependencies=no` (after `local-fs.target`, `systemd-modules-load.service` and `network-pre.target`; before Nomad,
systemd-networkd and `shutdown.target`). With default dependencies it came after `sysinit.target`, and cloud-init
(`After=systemd-networkd-wait-online.service`, `Before=sysinit.target`) closed a loop through networkd on every boot of a
cloud-init host since 031; systemd broke it by dropping a job that changed per boot (`ssh.socket`, `docker.socket`,
`cloud-init.service`) and left the system `degraded`. A control on a bare Ubuntu 24.04 Lima VM (2026-11-19): the old
ordering logged 2 cycle lines per boot, the new one 0 with the system `running`. A rerun of the installer rewrites the
unit and reloads systemd. `install-test.sh` checks the rewrite and a reboot (ssh, no `ordering cycle` in `journalctl -b`,
`systemd-analyze verify`, the mesh, Nomad, the console and whoami), and `cluster-test.sh` checks the journal after its
reboots in steps 7 and 8.

Feature 044 (`specs/044-image-scanning`, CodeAlta task `z57f`, worktree branch `alta/gallant-helm-7qwv`): the image
scanner, an add-on the owner installs, finds known vulnerabilities in the images apps run. `plan.md` D1–D18 are the
decisions (D15–D18 are the maintainer's changes of the same day); `research.md` R1–R5 the lab facts. Operator doc:
`docs/image-scanning.md`. Constitution 1.2.1 names its helpers.
- Scanner: Grype v0.117.0 pinned by digest (`CARAVANA_SCANNER_IMAGE`, `nomadjob.DefaultScannerImage`), chosen over
  Trivy for privacy and supply chain (R1: Trivy's telemetry is on by default, its March 2026 releases were
  compromised). Its database is 3.5 GB on disk and a 204 MB download a day; a scan peaks at about 477 MB.
- The add-on (D15): table `addons`, `service/addons.go`; until `PUT /v1/addons/image-scanner` nothing exists (no image,
  volume, download, queue, gate or needs-you item). Remove stops the queue and a running scan, deletes the volume and
  every result (`ForgetScans`, which also drops the kind's `sent_problems`, so no "resolved" is sent).
- Jobs (`nomadjob/scan.go`, pure, goldens `scan*`): one batch job `_scan-<run>` per image (`_` so no app's ID or
  prefix matches), constrained to the node of the host volume `caravana-scanner` (one database per installation,
  D16), Grype's update check off, `GRYPE_DB_UPDATE_CHECK_MAX_FREQUENCY=24h`, an optional mirror (`databaseUrl` →
  `GRYPE_DB_UPDATE_URL`). A registry scan writes the findings (`vulns.Template`) and the CycloneDX SBOM
  (`/cache/sbom/<hex>.cdx.json`) in one run; the daily rescan reads the SBOM (no pull, no credentials, D17); install
  runs `db update`. A private image's credentials are the job's own variable (`GRYPE_REGISTRY_AUTH_*`), purged with
  the job. Scans run on Docker's bridge; only a registry scan of an image on a loopback registry gets the host's
  network, and then on the group and in the docker config (`network_mode = "host"`): lab, observed, the group's mode
  alone leaves the container on the bridge (R5).
- `internal/vulns` (pure) parses the result: one severity scale (critical…info), a stable finding ID (16 hex of
  SHA-256 over check and digest), CVE, EPSS, KEV, a remediation sentence, the scanner's version and the database's
  date. `deploy/scan.go` starts, reads, classifies (fixed classes from Grype's stderr, never its text) and purges.
- Service (`scanner.go`, `scanroom.go`, `scanreads.go`): one runner, one scan at a time, fed by healthy deploys
  (`finish`), the daily pass (`daily_hour`, 04:00 UTC), the owner, the install and the gate; skipped when the
  volume's server has under 512 MB unreserved memory or 4 GiB free disk; 20-minute limit; restart recovery; results
  of digests no live deployment runs are pruned. Migration `0034_image_scans.sql` (`addons`, `image_scans`,
  `image_findings` with `first_seen`/`last_seen`, `scan_runs`, `scan_settings`).
- Gate (`gate.go`, D9): with `refuseFixableCritical` on, `deployWarned` scans each new image (waiting at most half the
  deploy's remaining budget) and refuses a fixable critical with `*ImageVulnerableError` → 422 `image_vulnerable` on
  `services[i].image`; a scan that can't be made is a warning.
- Needs-you `image_vulnerable` (`attention_scan.go`): one item per app, service and package with a fixable finding at
  or above `notifyLevel` (critical, or high), key `image_vulnerable|<app>|<service>|<package>`; `notify.Event` gains
  package, versions, advisory, severity and image.
- API 0.49.0: `/v1/addons[/{name}]`, `/v1/apps/{id}/scan`, `/v1/apps/{id}/deployments/{deploymentId}/scan`,
  `/v1/scans` (list, request), `/v1/scans/runs`, `/v1/scans/settings`; codes `image_vulnerable`,
  `scanner_not_installed`, `addon_busy`, `addon_not_found`; deploy tokens denied by the default deny. MCP read tool
  `get_app_scan`. Console: Operator › Security (`/ops/security`: install, remove, settings, images, runs) and the
  Deployments tab's Security panel plus a count line per history entry. No OCSF export (fields kept for one).
- Lab: the gate step `image-scan` (install, zlib CVE-2022-37434 on `alpine:3.14.0`, items and notifications,
  `alpine:3.14.10` resolves them, the daily SBOM rescan finds a dropped finding again via
  `Service.DropFindingForTests`, a private image with a sentinel password, the gate's 422, removal; 283 s) and the
  console step `console-security` (136 s) passed on 2026-11-19. Each installs and removes the add-on, so each
  downloads the database (~2 min); the lab VM needs about 4 GB free disk for it.

Feature 045 (`specs/045-reclaim-space`, CodeAlta task `p535`, worktree branch `alta/sturdy-erebus-k2fw`): a server's page says
what could go, unused images are removed every day, and a volume no app declares is deleted by typing its name. `plan.md` D1–D7
are the decisions and its Deviations sections (phases A to E) the changes; operator doc: `docs/reclaim-space.md`. No policy
change, no new module, constitution unchanged (the archive job is a helper job the plan names).
- Why: Nomad's docker driver counts the images it pulled in memory only, so after a Nomad restart (which Caravana does on
  purpose) old app images stay for good; Docker's anonymous volumes (a container is removed without `RemoveVolumes`) and an app's
  dropped volumes stay too. Lab, observed 2026-11-20: 7 images of which 3 in use, 154 leftover Docker volumes (91 MB).
- The agent (`internal/nodeagent/space.go`, `contracts/agent.md`): `Report.Space` (every image with its digests, tags, size and
  whether a container uses it, at most 1000; Docker's unused volumes; the build cache; the size of each Nomad host volume
  directory, measured with `du` at most every 15 minutes and kept in `state.json`; `lastClean`) and `Answer.Clean{id, keep,
  dockerVolumes}`, validated like the rest of 031's answer (32 hex ID, at most 4096 `sha256:` digests; `maxAnswer` is 512 KiB and
  the sync body 4 MiB). The console sends only an ID and the digests to KEEP, never a path, a container or an image to remove.
  `clean` removes every image no container uses whose digests are all outside `keep` (`removable`, pure; no `--force`; the image
  is checked with `image inspect` when `image rm` fails, since Docker 29 fails on the second name of an image the first name
  already removed), prunes the build cache and, only when asked, Docker's volumes; the ID is written to `state.json` first, so a
  crash never repeats a run. `caravana node space` prints the report.
- The keep rule (`service/space.go`, `keepFrom`, pure): the digests of each app's newest N healthy deployments with a recorded
  definition (N is `cleanup_settings.keep_deployments`, default 3, 1–10), every deployment pending or running, every stored app's
  current definition (a stopped app starts without a pull) and the helper images, plus 044's scanner image while the image
  scanner is installed (`keepDigests`; it runs only during a scan, so no task holds it); at most 4096 (`ErrKeepListTooLong`).
- Clean-ups (`service/cleanups.go`, migration 0035: `cleanup_settings`, `image_cleanups`, `volume_deletions`): `RequestCleanup`
  needs a managed server whose report is under 10 minutes old (`ErrNoAgent`) and no open one (`ErrCleanupRunning`, a partial
  unique index); the sync answer carries `clean` until the report names its ID (`done` or `failed`); `cleanupPass` (every
  minute) fails a `requested` one after 10 minutes and a `running` one after 30 (`lost`) and, once a day from 04:00 UTC while
  `automatic` is on (the default; the hour isn't a setting), requests one per ready managed server that reports its images,
  trigger `schedule`, never with Docker's volumes. The feed has "Cleaned up server one: removed 7 images, freed 1.2 GB".
- Orphan volumes (`service/orphans.go`): a Nomad host volume no stored app's `nomadjob.VolumeName` equals and that isn't
  Caravana's own (`nomadjob.OwnVolume`: 044's `caravana-scanner`, which only the add-on's Remove deletes) (`classifyOrphans`,
  pure), listed under its server with the app (or "its app was deleted"), the size the agent measured (null until then), when
  the app was last deployed with it and whether a copy mounts it. `DeleteOrphanVolume` checks the typed name (`*app.FieldError`
  on `confirm`), `ErrVolumeNotFound`, `ErrVolumeNotOrphan`, `ErrVolumeInUse` (a reverted deploy leaves the live version
  mounting it) and, with `backUpFirst`, the backup destination; then deletes through Nomad. With `backUpFirst` it answers 202
  `archiving` and a goroutine runs `deploy.ArchiveVolume`: the one-off batch job `<volume>_archive` (`nomadjob/archive.go`, golden;
  mounts the volume read-only, `tar | rclone rcat` to `<bucket>/<prefix>/<app>/volumes/<ts>/<volume>.tar.gz`, prints
  `caravana-archive: ok object=… bytes=…`; the destination's keys only in the job's own Nomad Variable), and the volume is deleted
  only after the object is there. The job and variable are removed whatever happened, and `failInterruptedDeletions` removes
  what a killed process left. The deploy warning for a dropped volume names the server.
- API 0.50.0: `getServerSpace`, `cleanServer` (202), `listServerCleanups`, `getServerCleanup`, `deleteOrphanVolume` (`POST
  /v1/volumes/{name}/deletions`, 200 or 202), `getVolumeDeletion`, `getCleanupSettings`, `putCleanupSettings`, codes `no_agent`,
  `cleanup_running`, `keep_list_too_long`, `volume_not_found`, `volume_not_orphan`, `volume_in_use`, `deletion_running`,
  `cleanup_not_found`, `deletion_not_found`; deploy tokens are denied by the default deny; MCP has one read tool,
  `get_server_space`. Go's `ServeMux` conflicts between `/v1/servers/{name}/space|cleanups` (and the console's
  `/ops/servers/{name}/clean`, `/parts/ops/servers/{name}/space`) and 031's `joins/{id}` routes, so one pattern takes both and
  dispatches (`serverSubRoutes`, `showServerSub`, `partServerSub`).
- Console (`internal/ui/opsspace.go`): the server page's Space section (`id="space"`, its own part, refreshed on the `servers`
  notice): a line per kind (each "—" with the reason without an agent), the reclaimable total, the last clean-up, Clean up now…
  (`/ops/servers/{name}/clean`, the Docker-volumes box), the orphans with Delete… (`.../volumes/{volume}/delete`: the name typed,
  Back up first when the destination is set) and the settings form (`POST /ops/cleanup/settings`); Overview's server panels
  have "Reclaimable 1.5 GB"; the disk item links to `#space`. Forms work without JavaScript.
- Lab: the gate step `space` (`gate_space_test.go`: the lab's node is unmanaged, so `managed: false`, no images, `no_agent`; an
  app with two volumes, a broken deploy dropping one reverted at Nomad's 10 minute progress deadline so the orphan is `inUse` and
  its deletion `volume_in_use`, the refusals, a delete with `backUpFirst` against the gate's bucket job whose object holds the
  volume's file, the other volume's data intact, the feed, the settings) passed on 2026-11-20 (692 s); `console-space`
  (`console_space_test.go`) passed (81 s). `deploy/dev/install-test.sh` `check_space_host` (four alpine versions, Nomad restarted,
  Clean up now removes the first and keeps the three newest, Traefik's and whoami's images, a redeploy of the second version
  pulls nothing) passed on 2026-11-20 on Ubuntu 24.04 (1501 s); its first run found the Docker 29 `image rm` bug above. A killed
  gate run leaves `gate-space` and its volumes in the lab: `lab-nomad job stop -purge gate-space` and `volume delete`.
- Not in 045: other disk users (logs, Nomad's own data, old backups on the host), an editor for the daily hour, a registry cache
  prune, a clean-up without the agent.

Feature 046 (`specs/046-database-dumps`, CodeAlta task `qgmm`, worktree branch `alta/balmy-elbrus-rj6w`): `backup: {engine: mysql}`
(the official `mysql` and `mariadb` images) and `backup: {engine: mongodb}` (`mongo`) dump a running database like
`postgres` does, without stopping it. `plan.md` D1–D14 are the decisions and its Deviations sections the changes; operator
doc: `docs/database-backups.md`. No new module, no policy change, constitution unchanged (the jobs are 010's helper jobs).
- Model: `app.DumpEngine(engine)` / `Backup.Dumps()` say "a Nomad job that dumps over the service's port" (postgres, mysql,
  mongodb); every place that meant that tests it, and places that mean Postgres itself keep the name. `app.FindDumpLogin` is
  the one login rule (validation, the deploy's credential read and the defaults), `DumpLoginHint` words the refusal on
  `services[i].backup`, `BackupPasswordNames` feeds the plain-env warning and the template check. mysql reads
  `MARIADB_ROOT_PASSWORD`, `MYSQL_ROOT_PASSWORD` (user root), else a `MARIADB_USER`/`MYSQL_USER` pair; mongodb reads
  `MONGO_INITDB_ROOT_USERNAME` and `_PASSWORD`; `*_FILE` variables are not read.
- Jobs (`nomadjob/dumps.go`, pure, goldens `dump-*`): one recipe per engine behind the same `dump` + `upload` and `download` +
  `restore` groups. Objects are `.dump` (postgres), `.sql.gz` (mysql; the upload task gzips, so the image needs only its client)
  and `.archive.gz` (mongodump compresses itself); the upload task has `BACKUP_EXT` and `BACKUP_GZIP` (the Postgres goldens
  changed for it) and retention lists only the engine's extension, pins matching `<ts><ext>`. The login is in the job's own
  Nomad Variable (`DB_USER`, `DB_PASSWORD`; Postgres keeps `PG*`) and reaches the tools through a file in the task's secrets
  directory, never argv: a `[client]` option file for `--defaults-extra-file` (MySQL 8 deprecates `MYSQL_PWD`) and a
  `--config` YAML for the Mongo tools (100.3+, in the mongo 4.4+ images). Scripts write with `printf` (dash's `echo` reads
  backslashes) and refuse a value with a line break. mysql dumps every database the login sees but the four built-in ones
  (`--databases --single-transaction --routines --events --triggers --no-tablespaces`; no database gives a one-comment dump),
  restores per table (`DROP TABLE IF EXISTS`, tables made since stay, not one transaction: MySQL DDL commits); mongodb restores
  with `--drop` and leaves `admin.*` and `config.*` alone. A missing tool fails the run with a sentence naming it ("the image
  has neither mariadb-dump nor mysqldump", "the image has no mongodump"): Caravana doesn't look inside an image before it runs.
- Store and API: migration 0036 widens the engine CHECK of `backups` and `restores`; records of every dump engine carry their
  engine, and an expired object maps to its record by the digits before the first dot. API 0.51.0: the two engines in every
  engine enum (policy, backup, restore, overlay), no new route or code.
- Console: the Compose form's engine select ("MySQL or MariaDB (the service keeps running)", "MongoDB (the service keeps running)"),
  the Backups tab's "Back up daily" for an image whose last path segment is `postgres`, `mysql`, `mariadb` or `mongo` with a port
  (`dumpEngineOfImage`; a row and a gap say "PostgreSQL", "MySQL", "MariaDB" or "MongoDB", `dumpName`), built-in schedules read
  `mysqldump`/`mongodump`, any dump engine and a `mongo` image make an app a database on the Apps home. A sentinel test in `ui` searches every page,
  record and log for a secret and a plain-env password (Configure draws plain variables by design, so it is checked for the secret only).
- Lab: the gate steps `mysql-backup-restore` (MariaDB 11.4) and `mongodb-backup-restore` (MongoDB 8.0), added by Phase C in
  `cmd/caravana/gate_dumps_test.go` after `sqlite-backup-restore`: a timer run, Back up now while a writer inserts every 200 ms,
  A → back up → B → restore → A, and no password in any job, export, answer, log or SQLite file. Both passed in the lab on
  2026-11-20 (127 s and 119 s, each run alone with `-run 'TestLab/^(api-needs-token|mysql-backup-restore)$'`); no script
  needed a change against the real tools. Not covered by the lab: an empty MySQL server (its dump is one SQL comment).
- Not in 046: point-in-time recovery, replica sets and shards beyond the node the service reaches, `*_FILE` credentials,
  Percona XtraBackup, Redis.

Email and Telegram notifications (CodeAlta task `c6v8`, worktree branch `alta/cosmic-phoenix-4c2k`): two more channel kinds beside
`webhook`, `slack`, `discord` and `ntfy`, on the same queue, retries, deliveries, Send a test, Pause and Edit.
- `internal/notify`: `Render` handles `telegram` (`POST <Bot API>/bot<token>/sendMessage`, JSON `chat_id` always a string, plain text,
  previews off); an email channel has no URL, so `RenderMail` (pure; multipart/alternative, quoted-printable UTF-8, goldens
  `email-*.golden.txt`) and `SendMail` (`net/smtp`, one 10 s deadline, `starttls`/`tls`/`none`, AUTH PLAIN only over TLS or to
  localhost, no new module) replace `Render` + `Send`. `notify.Deliver` picks the pair and blanks the channel's secret out of an
  error; the sender and Send a test both call it. The bot token is in the URL's path, so `Send` still names only the host.
  `notify/smtptest` is a fake SMTP server (STARTTLS, implicit TLS, AUTH, refusals, hang) for the tests of every package.
- Settings are JSON in the new column `notification_channels.settings` (migration 0037: the table is rebuilt for the wider CHECK
  **together with `notification_deliveries`**, because dropping the old channels table would cascade-delete every delivery);
  the SMTP password (`password`) and the bot token (`token`) live only in the Nomad Variable `caravana/notifications/<id>`.
  `service.EmailSettings`/`TelegramSettings` are validated naming `email.host`, `email.to[i]`, `telegram.chatId`, `token`...;
  `none` security only for localhost or a loopback address; shown address `ana@x.com, ben@x.com +1 more via smtp.x.com:587` or `chat -100…`.
- API 0.52.0: `email`, `telegram`, `password` on create and patch, `NotificationEmail`/`NotificationTelegram`, `hasToken` also means
  an SMTP password. Console: the add form draws every kind's rows and the stylesheet hides the others with `:has()` on the Kind
  select (`.cv-nf-url|ntfy|email|telegram`), so it works without script; the handler reads only the chosen kind's inputs.
- Lab: `TestConsoleLab` step `console-email-telegram` (`console_email_test.go`: an email channel through the form against a
  fake SMTP server in the test process, a Telegram test to a recorded fake Bot API, a failed deploy mailed once, no secret in
  any page or list) passed on 2026-11-20 (8 s, `-run 'TestConsoleLab/^console-(sign-in|email-telegram)$'`). Run it with the
  main checkout's `deploy/dev/lab.sh env`: a worktree has no `deploy/dev/.data/env`.

Feature 047 (`specs/047-requests`, CodeAlta task `5k6z`, worktree branch `alta/hopeful-glen-8f72`): Requests, each HTTP
request Traefik served, per app, read from Traefik's access log and never stored. `plan.md` D1–D13 are the decisions and its
Deviations sections (phases A and B) the changes; operator doc: `docs/requests.md`. No migration, no new module, no policy
change, constitution unchanged.
- The log: both Traefik jobs write `--accesslog=true` as JSON to **stdout** with only the fields the page needs (`StartUTC`,
  router, service, method, host, path, status, duration, bytes, client, TLS version, retries, entry point), no header and no
  query string (`headers` and `queryparameters` `defaultmode=drop`: the sentinel `?token=s3cret-sentinel-047` gave `"RequestPath":"/"`
  in the lab). Traefik's own log moved to stderr, so Nomad's `alloc/logs/traefik.stdout.<n>` holds access lines only; the job's
  `logs { max_files = 10, max_file_size = 20 }` keeps about 200 MB. `install.sh` says Traefik restarts once when the running job
  lacks `accesslog`.
- `internal/accesslog` is pure (a test forbids network and Nomad imports): `Parse` (a line with `StartUTC` and `DownstreamStatus`,
  else skipped), `Class` (`1xx`…`5xx`, `other`), `Owners`/`OwnersOf` (Traefik's service is `<app ID>-<service>`, so it is resolved
  against the stored apps; `caravana@file` and a deleted app's service belong to nobody) and `ServiceOf` for the live follower.
  `deploy.ProxyCopy` (the running allocation of job `traefik`; `ErrNoProxy`) and `ProxyAccessLog` (the job's arguments).
- The read (`service/requests.go`): `ReadRequests` walks Traefik's stdout files backwards with 024's `walkLogStream`, under 024's
  `logSem` and deadline, with a budget of 8 MiB (about 20,000 requests); each line is parsed, resolved to its owner, filtered
  (status classes, method, path prefix, minimum duration, client) and sorted newest first by `StartUTC`, since the file is in
  completion order. `RequestRead` has `AccessLog` (false when the job doesn't write the log: nothing is read), `Total`, `Coverage`,
  `Dropped`, `Services` (the rail's counts, after every filter but the app and service) and `Histogram()` (30 bins by class).
  The read goes on into the allocations before the last Traefik restart while Nomad keeps them (`deploy.ProxyCopies`),
  and when Nomad already collected what came before, the coverage names the oldest allocation reached. `WatchRequests` follows Traefik's stdout
  while a Live page is open and sends the `requests` notice (the app ID, never a line), at most every 2 s per place.
- API 0.53.0 (0.52.0 is email and Telegram): `listRequests` (`GET /v1/requests`, `no-store`), 422 `invalid_field` naming the field, 404 as the logs', 503
  `proxy_unavailable`; deploy tokens are denied by the default deny.
- Console (`internal/ui/requests.go`, `templates/requests.html`, no script of its own): `/requests`, `/parts/requests` (the
  histogram and the table, on the `requests` notice; 503 with the notice's sentence when Traefik isn't running, which htmx doesn't
  swap), `/apps/{id}/requests` (the tab after Logs, the same page filtered to the app with "All requests"; its stream is
  `/live/apps/{id}?watch=requests&live=1`), the sidebar's Requests item (icon `arrow-left-right`). The filters are links (status
  chips: from no filter the first click turns that class off, the last class on can't be turned off) and one GET form (method,
  minimum duration, path prefix, client). A row is a link to Logs around its time for its service. The histogram is the logs' SVG
  stacked by class. Only the path is drawn, as text; control characters are dropped from it.
- The Logs page gained a window: `/logs?from=&to=` (RFC 3339, both) reads exactly that window, paused, with no Live button
  (`windowOf` serves both pages); a request's row and Monitoring's Errors chart link to it. Monitoring: the Errors chart's
  caption "See the requests that failed" (`/requests?status=5xx` over the chart's range), a click on its plot opens the point's
  minute (`data-click-href`, `monitoring.js`), its hidden table's rows are the same links, and the apps table has a Requests link
  beside the Logs one.
- Tests: sentinel tests in `service`, `api` and `ui` search every page, answer, error, log and the stored apps for
  `s3cret-sentinel-047`.
- Lab: `TestConsoleLab` has a step `console-requests` (`console_requests_test.go`: a busybox app with a domain and two CGI
  scripts, the three requests through Traefik with `?token=<sentinel>`, the app's Requests tab, the 5xx chip and the path and
  method filters, the workspace page, Logs around a row, Monitoring's link) and the gate a step `requests`
  (`gate_requests_test.go`: `listRequests` with the gate's token, every filter, the 422 refusals naming their field, a deploy
  token's 403, and the sentinel in no answer, log line or data file). Both passed in the lab on 2026-11-20 (32 s each, run
  alone with `-run 'TestConsoleLab/^console-(sign-in|requests)$'` and `-run 'TestLab/^(api-needs-token|requests)$'`).
  busybox's httpd answers a POST to a non-CGI path with 501, so the POST goes to a CGI script. Traefik's log keeps earlier
  runs' requests, and an ID made again gets its old requests back, so each run uses an app ID of its own. `install-test.sh`'s
  `check_requests_host` (the `accesslog` arguments in Traefik's job and one `GET /v1/requests`) is written and shellchecked,
  not run.
- Not in 047: long-term analytics, geo lookup of clients, storing the log, requests no app's router took, Traefik's
  `forwardedHeaders` trust list (behind a proxy the client is the proxy's address), a download, a per-copy attribution.

Feature 048 (`specs/048-dns-providers`, CodeAlta task `s0ce`, worktree branch `alta/balmy-daisy-m7wj`): a zone may be
connected to Cloudflare, and Caravana then makes the DNS record of each domain an app adds under it. `plan.md` D1–D11 are
the decisions and its Deviations sections (phases A and B) the changes; operator doc: `docs/dns-providers.md`. No new
module (the Cloudflare client is `net/http`), no policy change (the token's variable is under `caravana/*`), constitution
unchanged.
- The seam (`internal/dns`, pure of Caravana): `Record`, `Provider` (`FindZone`, `Records`, `Create`, `Update` for the proxy
  setting, `Delete`), the sentinels `ErrRefused`, `ErrZoneNotFound`, `ErrUnavailable`, `ErrRecordNotFound` (no error holds a
  body, a token or a URL), `ZoneOf` (the longest zone that is the host or a parent), `Cloudflare` over `net/http` and
  `CloudflareFactory`; `dnstest` has an in-memory `Provider` (knobs: refuse, unavailable, seeded hand-made records, a token
  to want) and a fake Cloudflare over `httptest` (`Server`).
- The token: Nomad Variable `caravana/dns/<zone key>` (`deploy.DNSZoneVariablePath`, item `token`; `.` is `~` in the key);
  SQLite (migration 0038: `dns_zones`, `dns_records`) holds the zone, the proxy setting, the provider's zone ID and the last
  test, and one row per record Caravana made, keyed by the provider's record ID.
- Flow (`service/dnsrecords.go`): `checkDNSRecords` is the last of `saveAndDeploy`'s checks (the only one that leaves the
  process): a host under a zone with no row of its app, whose record at the provider is an A, AAAA or CNAME pointing
  elsewhere, is `*DNSRecordExistsError` (409 `dns_record_exists`, on the domain's field); a record that already points here is
  left alone; one with our comment is adopted. `ensureDNSRecords` makes the missing records (an A per IPv4 and an AAAA per
  IPv6 address of `hostAddresses`, TTL automatic, comment `caravana:<app>`, one row written right after each create) in
  `deployWarned` before `Register`; no public address is `ErrHostAddressUnknown` (409 `dns_address_unknown`); a provider
  that refuses or can't be reached is `*DNSProviderError` (422 `dns_provider_refused`, 502 `dns_provider_unavailable`).
  `pruneDNSRecords` removes the rows of hosts the app dropped after a good `Register`, `DeleteApp` removes the app's records,
  and the pass (`service/dnspass.go`, at start and every 10 minutes, `SetDNSPassGap`) removes the rest and retries failures; it
  never creates a record. A failure to remove never fails a deploy or a delete.
- Zones (`service/dnszones.go`): `SaveDNSZone` (token first, then the row; a changed token or proxy setting forgets the last
  test; a changed proxy setting is applied to Caravana's records, and a partial failure comes back with the saved zone as
  "saved, but the old setting stays on N records; the next save tries again"), `DeleteDNSZone` (the records stay at Cloudflare),
  `TestDNSZone` (an outcome, never an error; the zone's records are in the answer only), `ManagedZone`.
  `Service.SetDNSProviders(dns.CloudflareFactory(""))` is called from `cmd/caravana/main.go`.
- API 0.54.0: `listDNSZones`, `putDNSZone`, `deleteDNSZone`, `testDNSZone`, `listDNSRecords`, `Domain.record`,
  `DomainCheck.managedZone`, the five codes above and `dns_zone_not_found`; deploy tokens are denied by the default deny.
- Console (`internal/ui/dnszones.go`, `dns_test.go`): Settings › DNS providers (`/settings/dns`, entry `dns`, icon `globe`,
  after Certificates; the row's mark and the nav's count-or-failed follow `dnsZonesState`): add, Edit (token optional), Test
  (answers 200 with the zone's records under its row), Remove (the records stay at Cloudflare). The page reuses the secret
  providers' inputs (`provider-fields`) and its token field never has a value. The Domains page says "DNS record · made by
  Caravana in <zone> (Cloudflare, direct|proxied)" in a managed row's detail, the Add panel names the zones and, after Check now
  for a host under one with no record, says Caravana creates it; the refusals are drawn at the Domain field
  (`dns_record_exists`) or as the form's notice (`refuseDeploy` words the provider and address errors for every deploy form).
  A proxied domain shows as pointing elsewhere until its certificate is issued (023's rule). Forms work without JavaScript.
- Lab: `startLab` runs a `dnstest.Server` (a fake Cloudflare on loopback, `gateTarget.DNS`, nil on a real host, where the
  steps skip) and gives the in-process Caravana `dns.CloudflareFactory(<its URL>)`. The gate step `dns-records`
  (`gate_dns_records_test.go`: the zone with a sentinel token, Test, five deploys covering create, no call on a redeploy,
  `dns_record_exists`, a hand-made record pointing here left alone, prune, the proxy update, `dns_provider_refused`, the
  delete, the zone's removal leaving the record at the fake, the token in no answer, log, job, export or SQLite file) and
  the console step `console-dns-providers` (`console_dns_test.go`) passed in the lab on 2026-11-21 (78 s and 16 s, each
  run alone with `CARAVANA_PUBLIC_URL=https://console.lab.test`, which the lab's resolver answers with 203.0.113.1).
  Without it the gate step checks the `dns_address_unknown` refusal and skips, and the console step skips the Add domain
  parts.
- Not in 048: DNS-01 and wildcard certificates, other DNS providers, records that follow a change of the installation's
  address, CNAME records.

Feature 049 (`specs/049-previews`, CodeAlta task `rg2u`, worktree branch `alta/dreamy-glow-f7vh`): a preview is an environment
named `pr-<n>` (`app.PreviewNumber`; the prefix is reserved), opened and closed by a deploy token, with its own copy of the app.
`plan.md` D1–D12 are the decisions; operator doc: `docs/previews.md`. Migration 0039 (merged as 0039: 048 took 0038), no new module, no policy change.
- Deploy tokens: `scopeRules` are functions of the request and the scope, and gained three: create `pr-<n>` in the token's
  project, delete a `pr-<n>` environment of it and delete its own app inside one. `closePreview`, `getPreviews` and
  `putPreviewSettings` stay denied to them.
- Per project (`projects.preview_limit` 5, `preview_expire_days` 7, `preview_password_hash`): the limit is checked in the same
  transaction as the insert (`*PreviewLimitError`, 409 `preview_limit`), expiry is computed from the newest deploy (never
  stored) and a reaper (`SetPreviewReapGap`, 5 minutes) closes the expired with `ClosePreview` (every app by `DeleteApp`, then
  the environment). The preview password is one bcrypt hash: the deploy path turns `teamOnly` on for every domain of a preview
  (one warning) and the Team-only rows take the project's hash; changing it rewrites them with no deploy, removing it is 409
  `previews_open` while a preview is open, and `rotateTeamPassword` there is 409 `preview_password`.
- API 0.55.0 (0.54.0 is 048): `getPreviews`, `putPreviewSettings`, `closePreview`, `Environment.preview`.
- Console: the handoff's Previews page (task `64ph`, built through the designer cycle, `contracts/previews-screen.md` with its
  rulings and last look): `/previews` and `/parts/previews` (`internal/ui/previewspage.go`, `templates/previews.html`), the
  sidebar's Previews item after Projects. The open tab lists every project's open previews problems first, one aligned row each
  (`#142` and the branch when the origin names one, never a `refs/pull/…` ref; the who-words without the ref; the console's
  status word; app chips; the address, a link when healthy; reserved memory; Open, Logs, Close…; one line: the failed
  deployment's error, the rollout, or "If nothing is deployed to it, it closes <clock>."). `service.PreviewApp.Summary` carries
  the app summary for `appStatusView` (Go only, no API change). The setup tab is the settings form's one place (limit, days,
  password; refusals at their field, the password never drawn back; the post's hidden `from=previews` returns there); the
  project settings page keeps one sentence and a link, and each preview row's "preview · expires <clock>" and Close preview…
  (`?from=previews` brings the confirmation back to the Previews page).
- Lab: gate step `previews` (`gate_previews_test.go`, after `deploy-from-ci`), which also runs caravana-action's preview mode when
  `../caravana-action` has it (`CARAVANA_ACTION_DIR` points at it from a worktree; its preview mode is `0a99ae4` on the
  action's unpushed branch `preview-mode`). It passed in the lab on 2026-11-22 (111 s, run with
  `-run 'TestLab/^(api-needs-token|previews)$'`). The action prints `::add-mask::<value>` for each preview secret, so a
  leak check skips those lines, as `deploy-from-ci` does for the token.

Feature 050 (`specs/050-build-from-git`, CodeAlta task `c9fd`, worktree branch `alta/solid-zagros-wbqx`): an app may be built from a Git
repository on the host. `plan.md` D1–D12 are the decisions and its Deviations sections (phases B and D) the changes; operator doc:
`docs/building-from-git.md`. Migration 0040, API 0.56.0, constitution 1.2.2 (names the build job and the build cache), no new module
(`golang.org/x/crypto/ssh` makes the keys), no policy change (builds use jobs, `nomad/jobs/*` and `caravana/*` variables, host volumes,
`read-fs` and `read-logs`, all in the policy already).
- Model: `app.Service.Build *app.Build{Repository, Branch, Path, Dockerfile, Access, HostKey}` (`omitempty`, so no revision moved;
  `Defaults()` is written into the stored app), the service's `image` is the repository builds push to (no tag or digest; each build is
  `<image>:git-<commit12>`), one built service per app (`App.BuiltService`), `ParseRepository` is the one address rule, the `caravana-`
  volume prefix is reserved. Compose's `build:` is still refused, a promotion takes the source's `build`.
- Jobs: `nomadjob.BuildJob` is pure (goldens `build-*`; ID `<app>_build-<id>`): the `source` task (`alpine/git`, clones with a deploy key
  or a credential file read from the job's own variable, `StrictHostKeyChecking=yes` over the built-in github.com and gitlab.com keys or
  `hostKey`) and the `build` task (rootless BuildKit, `seccomp`/`apparmor` unconfined, pushes `<image>:git-<commit>` and writes the
  digest), a cache volume `<app>_caravana-build` (uid 1000, emptied past its size), the proxy's server avoided once there is another, the
  host network only for a loopback repository or registry. The scripts are tested under a real `sh` with fakes. The private key, a token
  and the push credentials are in no job, only in `nomad/jobs/<job>`, and the log is masked of them.
- Flow: a built service's deploy (`pin == nil`) is recorded with its build (`store.CreateDeploymentWithBuild`, status `pending`, build
  `queued`), answers 202, and a goroutine on the process context builds, then runs `deployRecorded` with the pushed digest pinned
  (`Deployer.ResolveWith`). The newest deploy cancels the build in progress, at most `concurrency` builds run at once (the rest are
  `queued`), a timeout and a restart fail it ("Caravana restarted during the build"), a failure is `*service.BuildError` ("build failed:
  <reason>" and the last 20 lines; nothing is registered, so the running version stays) and `notifyDeploy` sends `deploy.failed`. The
  origin is the CI headers' when set, else the commit, branch and author. `checkBuild` runs before anything is saved:
  `ErrGitAccessMissing` (409), `ErrGitHostKeyUnknown` and `ErrBuildRegistryMissing` (422), each a `*RefusalError` with its field.
  `Redeploy`, the webhook and the console's Deploy build; a rollback, a promotion and Refresh secrets (pinned) never do.
- Git access (`service/gitaccess.go`): the Nomad Variable `caravana/builds/<app>/<service>` (`kind`, `private_key`, `public_key` or
  `username`, `token`); `NewDeployKey` (ed25519), `SetGitToken`, `RemoveGitAccess`, `GitAccess` (never the secrets). The push webhook
  (`service.PushEvent`, pure) reads GitHub, Gitea, Forgejo and GitLab bodies and `X-Gitlab-Token`; a push elsewhere, a deletion, a ping
  or the commit that is live is `WebhookResult.Skipped`. Settings: `BuildSettings` (memory 2048 MB, CPU 2000 MHz, timeout 30 min, one
  build at once, cache 5120 MB), `CARAVANA_BUILDER_IMAGE` and `CARAVANA_GIT_IMAGE`.
- API 0.56.0: `getDeploymentBuild`, `getGitAccess`, `createDeployKey`, `putGitToken`, `deleteGitAccess`, `getBuildSettings`,
  `putBuildSettings`, `Service.build`, `Deployment.build`, `WebhookResult.skipped`, codes `git_access_missing`, `git_host_key_unknown`,
  `build_registry_missing`, `build_not_found`, `git_access_not_found`. Deploy tokens may deploy a built app; the new routes are denied
  to them.
- Console (no designer pass, the existing parts and `DESIGN.md`'s rules; `internal/ui/build.go`, `gitaccess.go`, `buildpanel.go`,
  `gitform.go`, `nextsteps.go`, `buildsettings.go`): New app's fifth tab **Build from Git** (`/apps/new?from=git`, `POST /apps/new/git`,
  plain post; "Build and deploy" is for public access, a key or token is refused at `access` with "choose Create, then add access"; the
  service is named like the app); the app's page has a **Next steps** panel outside the live regions while nothing is deployed (the Git
  access, the push webhook, Build and deploy); Configure's **Build** panel per built service (a form posted to
  `/apps/{id}/services/{service}/build` with the page's revision, a deploy that builds, and the **Git access** section: the public key with
  its fingerprint, New key…, Add or Replace token…, Remove access; the token input never holds a value and the forms carry `from=next|
  configure`); the history entry's **Build** stage first in the strip (queued, "building 12 s", "built in 41 s", failed, cancelled; the
  strip is `grid-auto-flow: column`), its commit line and **Build log** link; the build page `/apps/{id}/deployments/{deploymentId}/build`
  with its region `/parts/apps/{id}/deployments/{deploymentId}/build` (refreshed on the `deployments` notice and every 5 s while the
  build is going, a `286` once it is over so htmx stops, a `noscript` refresh without script); Deploy on a built app says "Build and deploy
  the newest commit of <branch>"; Settings › Builds (`/settings/builds`, Connections group after Registries). The refusals of a built
  service are `buildRefusal`; `fieldMessage` names the build fields. The SSH host key has no field on New app's tab: it is in Configure's
  Build panel.
- Tests: `service`, `api` and `ui` have sentinel tests for a token, a private key and push credentials (rows, log, answers, pages, jobs); `internal/ui/build_test.go` plays Git and BuildKit with `deploytest.PlayBuilds`. The lab steps are the gate's
  `build-from-git` (`cmd/caravana/gate_build_test.go`: a public build over smart HTTP, a push webhook, a broken Dockerfile that
  leaves v2 live, a deploy key over SSH, the private key in no answer, log, job, export or SQLite file, the delete) and
  `TestConsoleLab`'s `console-build` (`console_build_test.go`), sharing `lab_git_test.go`'s Git server and registry jobs; they passed
  in the lab on 2026-11-23 (125 s and 39 s, each run alone; the steps lower the build settings for the 2 GiB VM). A shallow clone
  needs smart HTTP (research R3).
- Not in 050: Compose `build:`, more than one built service, polling a repository, build arguments and secrets, multi-architecture
  images, a registry run by Caravana, Git providers as installation-wide accounts, a pool of build servers.

Feature 051 (`specs/051-console-address`, CodeAlta task `xbf5`, worktree branch `alta/loyal-wombat-v69b`): Operator › Console
address changes the console's domain without SSH, and the console still never gets root. `plan.md` D1–D12 are the decisions and
its Deviations sections (phases A–C, R1) the changes; operator doc: `docs/console-address.md`. Migration 0041, API 0.57.0, no new
module, no policy change, constitution unchanged (the console route stays a static route of Traefik's file provider).
- The route: `traefik.nomad.hcl`'s `caravana.yml` template reads the item `hosts` of the Nomad Variable `nomad/jobs/traefik/traefik`
  (the job's own group path: no policy line), one router per host (`caravana`, `caravana-1`…, so each host has its own certificate),
  `change_mode = "noop"`, and falls back to `var.console_domain`. Caravana writes it (`deploy.SetConsoleHosts`) only once a change
  exists: the current host, the change being checked or switched (and while switching the old one), then each old domain whose
  window hasn't ended. A change never restarts Traefik. Lab, observed (R1): `nomadVar` yields struct items, so `eq $item ""` fails
  to render and kills the task; the template reads `{{ with index . "hosts" }}{{ $hosts = printf "%s" . }}{{ end }}`, and the Go
  test's fake returns struct items. A Traefik job from before 051 is detected (`deploy.ConsoleRouteSupported`) and refused with
  `console_address_unsupported`: rerun the installer once (Traefik restarts once).
- Checks (`service/consoleaddress.go`, `consolechecks.go`): before anything is recorded, the host name (`mesh.ValidConsoleDomain`,
  the installer's rule in lower case, no IP), not an app's (`domain_taken`), DNS via `edge.Check` against `hostAddresses`
  (422 `domain_not_here`; with a 048 zone and no record, `createDNSRecord` makes A/AAAA records commented `caravana:console`, no
  `dns_records` row). Then the domain joins the route and a goroutine waits up to 5 minutes for the route (`GET /v1/health`
  through the edge with the domain as SNI and Host) and a valid certificate (Traefik's own with `CARAVANA_ACME=none`). One change
  open at a time (`checking`, `ready`, `switching`; then `applied`, `failed`, `refused`, `lost`, `check_failed`, `cancelled`).
- The switch (D1): the server agent that `runsConsole` (its node env URL is `http://127.0.0.1:7070` and `/etc/caravana/caravana.env`
  exists) gets `consoleAddress {id, domain}` in its sync answer while a change is switching, checks both as text, saves `running`
  in `state.json`, rewrites only the `CARAVANA_PUBLIC_URL=` line of `caravana.env` and the `CARAVANA_DOMAIN=` line of
  `/var/lib/caravana-host/install.env` (temp file and rename, mode and owner kept), restarts `caravana` and waits 60 s for health;
  unhealthy puts both files back and reports `failed` (`internal/nodeagent/consoleaddress.go`). No request file exists for root to
  read. Caravana settles `applied` at start when its public URL's host is the domain, `lost` after 5 minutes. Joined agents get
  `consoleURL` in every answer and switch their node env URL once `/v1/health` answers there. The issuer follows the public URL
  (038's `issuerFor`, one Nomad restart per server).
- Old domains: for `redirectDays` (0, 7, 30 by default, 90) the old domain stays routed and keeps its certificate; `cmd/caravana`'s
  `root` serves `/v1/` and `/.well-known/` there (CI tokens and Git hosts' webhooks don't follow a redirect to another host) and
  answers anything else 308 to the same path on the new address (`Service.ConsoleRedirect`); the pass (every minute) drops ended
  windows from the route. An app domain equal to a pending or old console domain is refused like the console's own.
- Passkeys: 033's rule already keeps password sign-in on at a domain with no passkey (`consoleaddress_signin_test.go`).
- API 0.57.0: `getConsoleAddress`, `startConsoleAddressChange` (202), `getConsoleAddressChange`, `cancelConsoleAddressChange`,
  `switchConsoleAddress` (202), codes `domain_not_here`, `console_address_change_open`, `console_address_unsupported`,
  `console_address_check_failed`, `console_address_change_not_found`, `console_address_not_cancellable`,
  `console_address_not_ready` (and 045's `no_agent`); the node sync gained `runsConsole`, `consoleAddress` and `consoleURL`. Deploy
  tokens are denied by the default deny. Console (`internal/ui/consoleaddress.go`, `templates/opsconsoleaddress.html`):
  `/ops/console-address` after Updates (now, what depends on it, the form, the change panel polled from
  `/parts/ops/console-address` every 3 s while checking or switching, 286 once not), posts answer 303. No designer pass.
- Lab: `TestConsoleLab` step `console-address` (the lab's Traefik has no console route: the sentence and 409
  `console_address_unsupported`). `install-test.sh`'s `check_console_address_host` (`ONLY=console-address` runs just it on a fresh
  VM, about 4 minutes): `/etc/hosts` names `caravana.test` and `console2.caravana.test`, `nowhere.caravana.test` refused with
  nothing changed, a crafted file runs nothing, the change ready with Traefik's allocation unchanged, the switch (env files, 308 on
  the old domain's pages, 200 on its `/v1/health`, the issuer in discovery and `nomad.hcl`, whoami answering, Traefik never
  restarted), password sign-in at the new domain, and a rerun of the installer keeping it. It passed on 2026-11-24 on Ubuntu 24.04.
  A valid Let's Encrypt certificate on a real domain is the maintainer's check on a real host (the VM uses `--acme none`).
- Not in 051: moving the console to another server, a console on several servers, removing a DNS record made for an old domain,
  a "stop redirecting now" button.

Feature 052 (`specs/052-backup-destinations`, worktree branch `alta/robust-monsoon-cn34`): several named backup destinations and a
second copy of every backup. `plan.md` D1–D15 are the decisions and its Deviations sections (phases A to D) the changes; operator doc:
`docs/backup-destinations.md`. Migration 0042, API 0.58.0, constitution 1.2.3 (names the copy and check jobs), no new module, no policy change.
- Destinations: table `backup_destinations` (name, endpoint, region, bucket, prefix, access key ID, `secret_path`, `is_default` with a
  partial unique index, the last test); the secret key only in a Nomad Variable (`caravana/backup-destinations/<name>`, item
  `secretAccessKey`). The migration makes the old row `main`, the default, with its key left at `caravana/backup-destination`, and
  `backups.destination`/`restores.destination` say `main` on old rows. `service.ListBackupDestinations` (with `UsedBy`),
  `SaveBackupDestination` (the first is the default; `default: false` on the default is a field error), `SetDefaultBackupDestination`,
  `DeleteBackupDestination` (`ErrDestinationNotFound`, `ErrDestinationIsDefault`, `*DestinationInUseError`), `TestBackupDestination`.
  `GetBackupDestination`/`SetBackupDestination` still mean the default (the old route, 045's archive, 035's templates).
- Policy: `app.Backup.Destination`, `CopyTo` (at most 2, distinct, never the destination) and `CopyKeep` (`destination`, `copyTo`,
  `copyKeep`; omitempty/omitzero, so no revision moved). `backupPlaces` resolves policy, then `projects.backup_destination`, then the
  default; `checkBackups` refuses unknown names naming `services[i].backup.destination` or `.copyTo[k]`. `Service.BackupPlaces` exports
  the resolution for the console, `SetBackupPlaces` is a deploy with those three fields changed (like `SetRestoreSchedule`, kept Compose
  file through the overlay), `UpdateProject{BackupDestination}` sets a project's. `repointBackups` points the jobs of every affected
  service at a new destination from their next run (`Deployer.PointBackups`), without a deploy.
- Copies (`service/backupcopies.go`, `nomadjob/backupcopy.go`): a succeeded backup queues a `pending` row in `backup_copies` per `copyTo`
  name; `copyPass` (end of every `recordBackups`) settles `running` rows and starts `pending` ones, two at a time, as the batch job
  `<app>_<service>_copy-<id>` (rclone `copyto`/`copy`, a size check, then the target's retention with pins kept). The job's keys are in
  its own Nomad Variable. A restart keeps the queue. `store.BackupRecord.Gone()` (deleted from its own destination and every copy)
  replaces `Expired` for liveness. `_backup-check-<id>` (`nomadjob/backupcheck.go`, modes `test` and `locate`) tests a destination and
  finds which place still holds an object.
- Restore: `Service.Restore(ctx, appID, backupID, from)`; the places are the backup's destination and its live copies, `locate` picks
  the first that has it, `ErrBackupMissing` (409 `backup_missing`) when none, a `from` outside them is a field error on `from`. The
  restore record keeps its `destination`. Needs-you `backup_copy_failed` (a warning; key `…|<app>|<service>|<dest>`), and
  `backup_failed` and the notifications name the destination.
- API 0.58.0: `listBackupDestinations`, `putBackupDestination`, `deleteBackupDestination`, `testBackupDestination`,
  `BackupPolicy.destination/copyTo/copyKeep`, `Backup.destination/copies/expired`, `Restore.destination`, the restore's `{from}`,
  `Project.backupDestination`, codes `destination_not_found`, `destination_is_default`, `destination_in_use`, `backup_missing`; deploy
  tokens are denied by the default deny.
- Console: Settings › Backup storage is a list (`internal/ui/destinations.go`: `POST /settings/backups` for add and edit, `/test`,
  `/default`, `/remove`; the nav state follows the destinations), the project page has "Backups go to" (`POST
  /projects/{project}/backups`, shown with two destinations or when the project has one), the Backups tab has the policy line, "Where
  backups go…" (`POST /apps/{id}/backups/places`, shown with more than one destination or a policy that names one), a line per backup
  (`backupWhere`) and "Restore from" with more than one place, and the Compose form's backup part carries `backupDestination`,
  `backupCopyTo` and `backupCopyKeep`. Forms work without JavaScript; the key input never holds a value. Sentinel tests in `service`,
  `api` and `ui` (`TestDestinationKeyNeverShows`).
- Lab (2026-11-24, each run alone): the gate step `backup-copies` (`gate_backup_copies_test.go`: offsite added through the API, Test of main and
  offsite ok, a destination with a missing bucket fails Test with no key in its message and is removed, a Postgres service with
  `copyTo: ["offsite"]`, Back up now, the backup and its copy succeed with the object under `/data/offsite/`, the data changed, the object
  deleted from main **through the bucket's S3 API**, a restore with no `from` reads offsite and brings the data back, removing offsite while
  used is 409 `destination_in_use`, and the key in no answer, log line, job (the copy and check jobs are read while they live), export or
  SQLite file) passed in 60.6 s with `-run 'TestLab/^(api-needs-token|backup-copies)$'`; the console step `console-backup-destinations`
  (`console_backup_destinations_test.go`: offsite through the Settings form and Test, a busybox volume app whose "Where backups go…" sets
  Also copy to offsite, Back up now, the row says "in main · copied to offsite", the restore form offers both places, the app deleted and
  offsite removed) passed in 132.9 s with `-run 'TestConsoleLab/^console-(sign-in|backup-destinations)$'`; the existing `backup-restore`
  (48.7 s) and `volume-backup-restore` (335.3 s) passed unchanged with `-run 'TestLab/^(api-needs-token|backup-restore|volume-backup-restore)$'`.
  The gate's bucket job now makes `/data/offsite` beside `/data/backups`. Lab fact: `rclone serve s3` caches its listings, so a file removed
  under it with `rm` is still listed and sized (the check job finds it) while a download gets 404; delete through the S3 API instead, and
  write the endpoint in a connection string quoted (`endpoint='http://127.0.0.1:9000'`), or rclone can't parse it.
- Not in 052: other kinds of destination, encryption, moving backups between destinations, renaming a destination, a third copy,
  provider-side replication, per-copy pinning.

Feature 053 (`specs/053-secret-templates`, from the Aspire publisher's need, branch `053-secret-templates`): a secret's value may be
built from text and references to a secret provider, so a password kept in Bella Baxter can sit inside a connection string
(`ConnectionStrings__postgres = Host=postgres;Username=banana;Password=<db password>`). `plan.md` D1-D10 are the decisions. No
migration, no new module, no policy change, `Translate` untouched. Operator doc: the Templates section of `docs/secret-providers.md`.
- Model: `app.Secret.Template string` (`template`) and `Refs map[string]SecretRef` (`refs`), both `omitempty` (no `revision` moved). A
  secret has a Value, a From or a Template, only one; none keeps the current value. `Refs` needs at least one entry with a template and
  is refused without one. `${name}` inserts `Refs[name]`, `$$` is a literal `$`, any other `$` (lone, `${`
  without `}`, bad name) is a field error on `services[i].secrets[j].template` with the byte offset. Names `^[A-Za-z_][A-Za-z0-9_]{0,63}$`;
  every `${name}` needs a ref and every ref must be used (`…refs.<name>`); template 1-4096 bytes of UTF-8 without NUL, at most 16 refs;
  each ref is checked like a `from` (`validateSecretRefValue`: `…refs.<name>.provider`, `.path`). The pure parser and renderer are
  `internal/app/secrettemplate.go` (`ParseTemplate`, `RenderTemplate` in one pass, so a value with `$` or `${x}` arrives as typed,
  `TemplateRefNames`, `TemplateError`). `Secret.IsReference()` is From or Template; `WithoutValue` keeps both fields (cloning `Refs`).
- Resolution: `referencesOf` lists a template's refs as references (field `services[i].secrets[j].refs.<name>`, in the text's order of
  first use), so `checkReferences` and the one read per provider cover them; after the reads `resolveReferences` renders each template
  (same UTF-8/NUL check on the result) into the resolved sets, and `secrets.Build` takes a template secret's value from them as it does
  a `from`'s. Errors keep 037's codes with the ref's field: 400 `…refs.<name>.provider`, 422 `secret_not_found` `…refs.<name>.path`,
  422 `secret_provider_refused` / 502 `secret_provider_unavailable` `…refs.<name>`. Refresh secrets needed no change (it compares the
  resolved sets); `SetSecretIf` and `SetSecretRefIf` clear a template; `CopyApp` doesn't count one as missing; a provider's `usedBy`
  counts a template's providers.
- API 0.59.0: `Secret.template` and `Secret.refs`, no new route or code. Console: the Variables row and the Compose form's kept secrets
  read "template with N references: provider/path, …" read-only (`variableRow.Refs`, `draftSecret.Template/Refs`, no input), kept
  through a Compose deploy and a Configure save; the provider's row counts each ref; no template editor.
- Tests: table tests in `app` (every parse and validation error), `compose` (overlay), `secrets.Build`, `service` (`secrettemplate_test.go`:
  two providers, every failure with its field, Refresh re-render and no-op, replacing, copy, sentinel `s3cret-sentinel-053` in store, SQLite
  file, record, job, log), `api` (answers, contract schema) and `ui`. The gate step `secret-references` has `GATE_CONN`
  (`Host=db;Password=${pw};Note=$$5`): `printenv` in the copy shows it rendered, Refresh re-renders it, a ref to a missing path is 422 on
  `…refs.pw.path`. It passed in the lab on 2026-11-24 (59 s, `-run 'TestLab/^(api-needs-token|secret-references)$'`). The Aspire side
  (the publisher sending `template` and `refs`) is its own repo.
- Not in 053: a template editor in the console, other functions than `${name}` (defaults, encodings), templates for plain env,
  references across services, Bella's change webhook, an MCP tool.

Feature 054 (`specs/054-people-and-roles`, worktree branch `alta/dewy-quince-q5ss`, phases A to E): an installation has several people, each
with their own sign-in, tokens and role. `plan.md` D1–D12 are the decisions and its Deviations sections (phases B to D) the changes; operator
doc: `docs/people.md`. Migration 0043, API 0.60.0 (0.59.0 is 053, secret templates), no new module, no policy change, constitution unchanged.
- Store (`store/people.go`): `people` (email, `org_role` owner/admin/member, `state` invited/active, password hash, `invite_hash`, `ends_at`),
  `project_roles` (view/deploy/maintain, `ends_at`) and `environment_roles` (none/view/deploy/maintain); the one `admin` row became the first
  owner (same email, hash, passkeys, sessions, tokens), `sessions`, `api_tokens` and `passkeys` belong to a person, and `deployments`,
  `app_actions`, `backups` and `restores` gained `actor` (the person's email). Foreign keys are on.
- `Principal{PersonID, Email, OrgRole, Roles, Scope, TokenName}` is loaded fresh at every request (`Auth.CheckSession`, `CheckAPIToken`), so a
  role change or a removal applies to the next call; a person past `ends_at` is refused as one who isn't there. `service.Need`
  (`NeedSignedIn/Admin/Owner/App/Place/Project`), `Target` and the pure `Principal.Allow` give a `*NotAllowedError` ("your role in shop ›
  production is view; this needs deploy", with the `Field` for a deploy that changes more than images); `Service.Allow` reads the principal
  from the context and a context without one is Caravana itself (watchers, notifier, CLI): allowed, not filtered. `LevelAt` takes the
  environment's override over the project role; a preview (`pr-<n>`) takes the project role and its configure level is deploy
  (`ConfigureLevel`).
- One decision, two default-deny tables: `internal/api/access.go` (`accessRules`, 169, keyed like `scopeRules`) runs after a deploy token's
  scope, and `internal/ui/access.go` (`consoleAccess`, 211 plus `openRoutes`) is applied by `requireSession`/`requirePart`/
  `requireSessionJSON` by `r.Pattern`. A route with no rule is refused for everyone, owners too; a test fails for a route without one and
  a walk (`TestRolesWalkEveryRoute` ~1740 requests, `TestConsoleRolesWalkEveryRoute` ~1800) tries every route with an owner, an admin, a member
  and an ended guest against a table written apart from the rules. Schedules by ID are judged in the service by their app
  (`allowSchedule`); a followed log stream and a terminal re-run the route's rule at every re-check, so a role taken away ends them.
- Rights: view reads; deploy deploys (a body below maintain may change only `image`s: `app.ImagesOnlyChange`), redeploys, restarts, starts, runs a
  schedule, backs up now, refreshes secrets, promotes into the place; maintain changes the setup, stops, deletes, runs a command and opens
  a terminal, restores and pins, makes tokens and Git access, makes apps and changes the project; the installation's pages are admin.
  Lists filter (`visibility`: apps, projects, recent deployments, attention, previews, schedules, domains); reads of one place refuse below
  view; an app a person can't see is "not found". Admins manage members only, owners anyone; the last owner stays (`last_owner`).
- People (`service/people.go`): `InvitePerson` (token `cvin_`, SHA-256 stored, 7 days, shown once; Caravana sends no mail), `ReinvitePerson`,
  `AcceptInvite` (a wrong token counts against the address like a wrong password, 429 when locked), `UpdatePerson`, `RemovePerson`
  (credentials go in one transaction), `People`, `Person`, `Me`, plus `InviteFor` (the invite page names who the link is for) and
  `Auth.StartSessionFor` (a session for the person `AcceptInvite` returned; nothing else calls it).
- API 0.59.0: `getMe`, `listPeople`, `invitePerson`, `getPerson`, `updatePerson`, `removePerson`, `reinvitePerson`, public `acceptInvite`, `actor`
  on Deployment, AppAction, Backup and Restore, codes `not_allowed` (403, with `field`), `person_not_found`, `email_taken`, `invite_invalid`,
  `not_invited`, `last_owner`; `listTokens`/`createToken`/`revokeToken` and the passkey and sign-in operations act on the caller's own.
  `Me` is a full object (an `allOf` over `Person` rejects `token`: `additionalProperties: false`). A deploy token is outside 020's
  allowlist for `getMe`. MCP: a read tool `get_me`.
- Console (`internal/ui/people.go`, `templates/people.html`, `invite.html`): Settings › People (list, Invite with a role per project and an
  override per environment, the link once at 200 `no-store`, a person's page, New invite link, Remove… with a confirmation), `/invite?token=`
  (email, password and confirmation, a session, 303 to `/apps`; `POST /invite` is in `consoleOnly`), and what each role sees: the
  Developer/Operator toggle, installation Settings, the host panels (Platform, Monitoring's host) for admins only; a member's Settings is
  account, sign-in, tokens and appearance; New app, Deploy, Redeploy, Restart/Start, Stop, Run a command, Terminal, Promote/Copy, Delete,
  Configure's forms (read-only with "Your role here is to look"), Variables, Domains, Backups, Schedules and a project's settings are
  drawn for what the role allows, in the parts too (flags in the views: `Caps`, `NoDeploy`, `NoMaintain`, `NoConfigure`, `ReadOnly`);
  `/logs`, `/requests` and `/monitoring` with no place send a member to their first visible place. Live notices: an app's only to a person who
  may see it, the installation's (servers, host, updates, scans, the console's address, the sampler's metrics) to admins; the metrics notice
  also reaches a member on the Monitoring page's own stream. A refusal inside any handler (`ErrNotAllowed`) is the 403 page.
- Who-words: `mine()` says "you" for the viewer's own email before `deployWho` runs, so the one set of words needs no viewer: "by you", "by
  ana@example.com", "through the API by ci (ana@example.com)"; rows with no actor keep the old words (the Previews page doesn't say "you").
- Lab (2026-11-24, each run alone): the gate step `people-roles` (`gate_people_test.go`: lab053 with staging and production and lab053b, an admin
  and a member (deploy in staging, view in production) who accept their invites, sign in through `POST /login` and make their tokens with
  that session; listApps shows the member two apps, an images-only deploy to staging is healthy and production, an env change
  (`services[0].env`), registries, servers and another project are `not_allowed`; the member's deploy token deploys staging and is refused
  production; the admin lists people and deploys production; `actor` on each deployment; `caravana mcp` with the member's token: `get_me`,
  `list_apps`, `deploy_image` refused on production and working on staging; a person whose end date passed and a member whose role
  ended; removal makes the token, the deploy token and the session 401 at the next call) passed in 122.1 s, and the console step
  `console-people` (`console_people_test.go`: the forms, the link once, `/invite`, only lab053c on the member's Apps, no Deploy form and a 403
  POST on production, no Operator toggle or installation Settings, removal sends the next page to `/login`) in 47.8 s
  (`-run 'TestLab/^(api-needs-token|people-roles)$'` and `-run 'TestConsoleLab/^console-(sign-in|people)$'`). The steps Phase A and C
  touched were run again: `console-passkeys` and `console-appearance` (1.6 s with `console-sign-in`), and `deploy-from-ci` (49.7 s) and
  `previews` (110.3 s), whose tokens now go through their owner's role.
- Review fixes (2026-11-24): in a preview (`pr-<n>`) a person below maintain, and any deploy token, may define or change an app only with
  secret references (each `from` and each template ref: provider and path) that the app of the same name already has in a non-preview
  environment of the project (`allowPreviewReferences` in `service/allow.go`, asked by `allowSave`, `CreateApp`, `CreateAppOnly`, `CopyApp` and
  `Promote`); anything else is 403 `not_allowed` naming the `…from` or `…refs.<name>` field, because a provider allowed for a project is open to
  every app there. `docs/people.md` says plainly that deploy rights are in effect access to an app's secrets and view includes logs.
  The store's DSN escapes the database path (a `#` or `?` in it opened another file). Foreign keys are enforced now, which older migrations
  never did: orphan rows from before (17 declared keys in 15 tables) are kept, not repaired, and every write still works on them
  (`TestOrphansFromBeforeForeignKeysAreKept`; see the plan's Deviations, review fixes); `PRAGMA foreign_key_check` lists them.
- Not in 053: single sign-on, custom roles, teams, per-project Nomad namespaces, sending the invite by mail, a roles page per
  app, and an invite for someone who already has an account.

Feature 055 (`specs/055-host-io`, branch `055-host-io`, stacked on 054's work): network and disk throughput per server, read by the node agent
from `/proc` (the maintainer's decision, 2026-11-24; a Beszel add-on went to the backlog). `plan.md` D1–D13 are the decisions; operator doc:
`docs/host-io.md`. Migration 0044, API 0.61.0, no new module, no policy change, no new route (so no access-table work), constitution unchanged.
- The agent (`internal/nodeagent/io.go`): `Report.IO *IOReport` (`io`, `omitempty`, so an old console ignores it): `{at (Unix ms), net[{name, kind
  "default"|"mesh", rxBytes, txBytes}], disks[{name, holds ["nomad","docker"], readBytes, writeBytes, busyMs}]}`, cumulative since boot. The
  parsers are pure (`parseNetDev`, `parseDiskstats`, `parseMountinfo`, `deviceOf`); files are read under the agent's `Root`
  (`/proc/net/dev`, `/proc/diskstats`, `/proc/self/mountinfo`; fixtures in `testdata/proc/`). Interfaces: the default route's (`State.PublicInterface`
  or `ip -4 route show default`) and `caravana-mesh`, never `lo`, `veth*`, `docker*`, `cni*`, `br-*`, `nomad`, `caravana0`. Devices: the ones holding
  `/opt/nomad/data` and Docker's root (`docker info --format {{.DockerRootDir}}`, `/var/lib/docker` otherwise), by the longest mount point and its
  `major:minor`, one entry with both `holds` when it is one disk; loop, RAM and zram devices and mounts with no `diskstats` line (btrfs) are skipped.
  The agent sends bytes (sectors × 512), not sectors; a counter over 2^63 leaves its entry out. A failed read leaves the block out; the sync never fails.
- The console (`service/hostio.go`): `NodeIO` decodes leniently (`UnmarshalJSON` marks a block that doesn't read `Invalid`), `checkIO` refuses a block
  by a reason (names ≤ 64 printable bytes, ≤ 4 interfaces and devices, known kinds and holds, non-negative counters, no twins, the block's time within
  10 minutes of the console's) and `NodeSync` drops it with one log line; the io block is not stored in the server's report. `ioRates(prev, cur)`
  makes rates only when the samples are 20 to 120 s apart; a counter that went down is a reset of that interface or device alone; busy % = Δbusy/Δt capped
  at 100. `hostIOState` (memory only, by server index) keeps the previous block and the newest rates, fresh for 3 minutes; after a restart the first
  rates come with the second report; a removed server is forgotten. Deviation from the brief: the **network headline is the default-route interface's**,
  not a sum over interfaces (the mesh's bytes leave on the default interface again, so a sum counted them twice); the mesh is listed beside it. Disk read
  and write are summed over devices, busy is the busiest device's.
- Reasons (`HostIO.Reason`, `""` when known): `no_agent` (a node with no server row: the lab's), `agent_too_old` (syncs, no usable block), `stale` (no sync for
  3 minutes, with `Since`), and `waiting` (one sample so far, a gap too long, a reset, or Caravana just started; an addition to the brief's three).
  A number that isn't known is `null` in the API, NULL in the database and "—" on the pages, never 0.
- Storage: migration `0044_host_io.sql` adds `net_rx_bps`, `net_tx_bps`, `disk_read_bps`, `disk_write_bps`, `disk_busy_pct` (nullable INTEGER) to `metrics_host`
  (`TestMigration0019OnAStoreWith0018Data` skips 0044 too, since 0044 alters 0019's table). The sampler's `addHostRows` writes a node's fresh rates by
  its server row (`serverNode.Index`, set by `buildServerDirectory`), `foldHosts` takes the median of each. `Service.HostIOSeries(server, range)` reads
  them back into points of the range's step.
- API 0.61.0: `getHost.io` (schema `HostIO`: `reason`, `since`, `at`, `network{rxBytesPerSec, txBytesPerSec, interfaces[]}`,
  `disk{readBytesPerSec, writeBytesPerSec, busyPercent, devices[]}`). Console: the server page's **Network and disk** section (`internal/ui/opsio.go`,
  `srv-io`: rows in B/s, KB/s, MB/s by `humanBytes`, names in mono, "—" rows with one sentence per reason, drawn with the Space section's classes) and
  Monitoring's host section: two charts under the memory chart, network (in solid, out dashed) and disk (read solid, write dashed) over the page's range
  (`internal/ui/monitoringio.go`, the existing plot helpers and a hidden table), or the line that says why there are none. No new CSS.
- Tests: parsers and report building on fixtures, `checkIO`, `ioRates`, `hostIOState`, the sampler writing values and NULLs, the fold, `HostIOSeries`, the store
  round trip and the migration over a 0043 store, `getHost.io` through a real agent join (`internal/api/host_io_test.go`), and the pages for known data and each
  reason (`internal/ui/opsio_test.go`). `deploy/dev/install-test.sh` has `check_host_io` (after two syncs `GET /v1/host?server=<first>` has non-null rates): written and
  shellchecked, **not run**. The lab's node is unmanaged, so a `TestConsoleLab` step could only show `no_agent`, which the UI tests cover.
- Not in 055: alerts or needs-you items for I/O, per-container I/O, IOPS, latency, a threshold setting, a monitoring add-on. Rerun the installer on a server to get an
  agent that reports I/O.

Feature 056 (`specs/056-drop-earlier-certificates`, CodeAlta task `69pt`, worktree branch `alta/hushed-mistral-bf5s`, phases A to C, a follow-up of 034's
research R5): Settings › Certificates drops the ACME certificates an upload covers, so a host that kept its earlier certificate presents the upload, and
the console still never gets root. `plan.md` D1–D12 are the decisions; operator doc: the section "When a domain still shows its earlier certificate" of
`docs/certificates.md`. Migration 0047, API 0.64.0 (merged after 055 and 057; 034 is 0046 and 0.63.0), no new module, no policy change (Traefik is restarted with `alloc-lifecycle`, in the policy since 016).
- The agent does it (D1, `internal/nodeagent/certdrop.go`): Traefik's store is the folder `/var/lib/caravana-host/traefik` of the first server. The sync answer's
  `certificateDrop {id, names}` is an ID and names, nothing else: the ID must be 32 hex digits (else ignored with a stderr line), the names 1–1000 lowercase host
  names or `*.` and one (`nodeagent.ValidDropName`; else the outcome is `refused`). `DropCovered` removes a stored certificate when every name (`domain.main`,
  `sans`, lower-cased, and each a host name) is covered by `certs.Covers`, keeping every other JSON member as it was; it runs over `acme.json` and
  `acme-<12 hex>.json` (regular files only, at most 32 MiB), checks all files before writing any, and rewrites a changed one through a temporary file and a
  rename with its mode and owner (`rewriteFile`). The outcome (`running`, `dropped`, `nothing`, `failed`, `refused`; `dropped` lists each certificate as its
  names joined with `+`) is kept in `state.json` and in the report beside `traefikStore` (the folder exists). A run found `running` goes again and says
  `dropped`, with no names when nothing is left, since its files may be rewritten already. After a run that dropped, `Sync` posts the report once more at once,
  so Traefik is restarted within a second. Nothing of a certificate, key or account is printed, logged or reported (sentinel tests).
- The same rule is `caravana node drop-certificates --names a,b,*.c [--dry-run]` (prints the dropped names on one line), which `install.sh`'s
  `drop_covered_acme_certificates` calls instead of its Python (so the installer no longer needs `python3`; it passes only the names that look like host names).
- The record (`service/certificatedrops.go`, migration 0045 `certificate_drops`, one open drop in all): `RequestCertificateDrop` (admin) needs an upload with a
  name the agent accepts (`ErrNoUploadedCertificates`), the first server's ready, fresh agent saying `traefikStore` (else `*DropNoAgentError`, `ErrNoAgent`, "run
  the installer on server X") and no open drop (`ErrCertificateDropRunning`). `certificateDropHandoff` in `NodeSync` hands only the first server's agent the
  request (names read at that moment), marks `requested` as `running`, and settles from the report: `dropped` → `restarting`, `nothing` → `done` (its store
  never rewritten: `rewritten_at` NULL, which is how the console tells it from a restart-only `done`), `failed` and `refused` → `failed` with the agent's
  sentence. `validateReport` checks the block (32 hex ID, one of five states, at most 100 entries of host names joined with `+`, a printable reason of at most
  300 bytes). Restarting runs in the background (`ProxyCopy`, `RestartCopy`): the drop is `done` when Traefik's task started after `rewritten_at`, whoever
  restarted it, and the covered hosts are checked again. A pass every minute (`SetCertificateDropPassGap`) fails a request nobody picked up in 10 minutes,
  loses a run with no outcome after 10, and retries a restart that didn't finish and fails it after 10 with what the owner is left with. A status change
  publishes `ChangeServers`.
- API 0.64.0: `dropEarlierCertificates` (`POST /v1/certificates/drops`, 202, `Location`), `listCertificateDrops`, `getCertificateDrop`, schema `CertificateDrop`, the
  sync report's `traefikStore` and `certificateDrop` and the answer's `certificateDrop`, codes `no_uploaded_certificates`, `certificate_drop_running`,
  `certificate_drop_not_found` (and `no_agent`, with no field). Admin only; deploy tokens are denied by the default deny.
- Console (`internal/ui/certdrop.go`): under the uploaded certificates, while a host is on its earlier certificate and no drop is open, the form
  `POST /settings/certificates/drop` ("Drop the earlier certificates", "Traefik restarts once: a few seconds without HTTPS.") answers 303 to
  `/settings/certificates?drop=<id>#drop`; a refusal draws the page again with 409 and its sentence at the form. The panel `#drop` (the drop the address names, else the
  newest while it is open or ended within the hour) is read again from `/parts/settings/certificates/drop` every 3 s while open and answers 286 once it isn't, with a
  `noscript` refresh. Forms work without JavaScript; both routes are admin rules.
- Tests: `nodeagent` (the rule's table, the folder walk, the outcomes, a run again, the second post, no leak), `cmd/caravana`, `store`, `service`
  (every refusal, the hand-off, each settling, the restart, the pass's deadlines, no PEM through a report), `api` (the real agent end to end against a store with
  sentinels), `ui` (`certdrop_test.go`, the walk and parity tables). `deploy/dev/install-test.sh`'s `check_certificates_host` has a console drop after its rerun
  drop, which passed on 2026-11-25 on Ubuntu 24.04 (`ONLY=certificates`, 3 min 17 s).
- Not in 056: dropping a certificate that also names a host no upload covers (Traefik would order it again, 034 R4), choosing which to drop, any other change of
  Traefik's store, a store on a server that isn't the ingress.

Feature 057 (`specs/057-audit-log`, CodeAlta task `jf6s`, worktree branch `alta/mossy-cutter-pg2p`): an audit log of who changed what, one entry per
change, readable by owners and admins. `plan.md` D1–D14 are the decisions and its Deviations sections (phases A to D) the changes; operator doc:
`docs/audit-log.md`. Migration 0045 and API 0.62.0 (merged after 055, which took 0044 and 0.61.0). No new module, no policy change, constitution unchanged.
- `internal/audit` is pure (a test forbids `net/http`, `store`, `service`): `Entry` (actor, door, address, action, operation, resource, changes,
  record, related, fallback), `Event` (an entry with the store's ID and time), the closed lists `Actions()` and `Kinds()` (a note outside them is
  logged as an error), `Diff(before, after)` (JSON walk, lists of objects keyed by `name|host|id|label|path`, paths like
  `services[web].domains[new.example.com].path`, values cut at 200 bytes, at most 100 changes) and its redaction (a leaf whose key is or ends with
  `password`, `token`, `secret`, `secretAccessKey`, `privateKey`, `apiKey`, `signingSecret`, `hash` or `value` shows `set`/`changed`/`removed`, no
  `from`/`to`; a value or item added whole shows only `{…}`/`[…]`, but a list of named items added or removed whole lists each item
  by its name, so a service's first domain still names its host), and the CSV writer (`SafeCell` quotes a leading `= + - @`, tab or CR).
- Store: `audit_events` has no foreign key (entries outlive apps and people), `audit_settings` (`retention_days` 90, CHECK 30–3650);
  `InsertAuditEvent`, `ListAuditEvents` (newest first, `before` cursor on the ID, at most 500), `ExportAuditEvents` (callback, oldest first, at most
  100 000), `PruneAuditEvents`; no update or delete of one entry exists. The prune runs from `Service.Start` (`SetAuditPruneGap`).
- The recorder (`service/audit.go`): the service notes with `s.audit(ctx, entry)` (`a.audit` in `Auth`) once a change is stored, never failing it (a
  failed write is logged). The API (every non-GET route but `auditExempt`) and the console (every POST) open an `AuditScope` with `Service.BeginAudit`
  (door, address, `METHOD /pattern`): the first note is the entry, later notes about the same resource add changes, others are `related`;
  `Finish(status, fallback)` writes it. The API's scope writes a **fallback** entry (`changed`, `fallback: true`, a warning in the log) for a write
  that succeeded and noted nothing, from the resource of the route's access rule (`SetFallback`); `NoChange()` says a request changed nothing. The
  console's scope calls **`NoFallback()`**: it has no route-to-resource rule, so a post nothing noted leaves no entry, and its coverage is the
  service's notes alone. Actor (`actorFor`): a principal is a `person`, a `token` or a `deploy_token`; with none the door says `webhook` (the app),
  `host` (the CLI sets `WithAuditDoor`) or `caravana`; a failed sign-in is `anonymous` with the typed email only when it reads as one address.
  `TestEveryWriteRouteLeavesOneEntry` walks every non-GET API route with stubbed handlers; the exceptions (`syncNode`, `renewNodeCertificate` and the
  previews sent as POST) are named in `auditExempt` and in the test, and a test fails when the two differ.
- What is noted: every deploy path through `deployWarned` (`noteDeploy`: `created`, `deployed`, `redeployed`, `rolled back`, `promoted`,
  `refreshed secrets`, with the deployment as the record), Configure's edits as `updated`, app actions, backups, restores, pins, schedules, projects,
  environments, previews (closing one is a single entry listing its apps as `related`), domains, Git access, every installation setting and
  provider, servers, updates, templates, add-ons, console address, people, tokens, passkeys, sign-in, and the CLI (`host`). Clock-driven runs add
  nothing (D14); what Caravana decides for the owner has Caravana as actor. A diff reads views that give every stored secret the value `\x00stored`
  (`audit_views.go`, `audit_installation.go`), so a replaced secret is `changed` and a kept one no change.
- API 0.62.0: `listAuditEvents` (`GET /v1/audit`, filters `actor`, `action`, `kind`, `resource`, `project`, `environment`, `from`, `to`, `before`,
  `limit`; 422 `invalid_field` naming the parameter), `exportAuditEvents` (`/v1/audit/export`, `from` and `to` required, CSV, at most 100 000 rows),
  `getAuditSettings`, `putAuditSettings` (owners); owners and admins read, members 403 `not_allowed`, deploy tokens 403 `token_scope`; every answer
  `no-store`.
- Console (`internal/ui/audit.go`, `templates/audit.html`): Settings › Audit log (`/settings/audit`, Security group after API tokens, admins and
  owners only): one GET form of filters (UTC days), entries newest first with the actor in words, the door, the action, the resource (linked to the app
  and the record while they exist), the address and the changes in a `<details>`; "Older" by cursor keeping the filters; `/settings/audit/export` (its
  own small GET form, `exportFrom`/`exportTo`, defaults to the last 7 days, the other filters come along as hidden inputs; a refusal is drawn at the
  form at 422); `POST /settings/audit` is the owners' retention (303 to `?saved=1`, a field error drawn at the input; an admin sees it read-only). Every
  page and the export are `no-store`. Forms work without JavaScript. `consoleAccess` has an `owner()` rule for the post, and the roles walk expects an
  admin to be refused it (`ownerOnly`).
- Tests: sentinel tests in `audit`, `store`, `service`, `api` and `ui` (`TestAuditNeverDrawsASecret` puts a registry password and a secret through
  console forms and searches the page, the CSV and the log). Not in 057: entries for reads, a per-app audit tab, an MCP tool, streaming to syslog or a
  SIEM, signing or chaining entries, entries for the agents' routine calls or for clock-driven runs.

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
ending with the API at 1.0. Phase 2.75 (Aspire: 026 `PublishToCaravana` and 026.5 `caravana mcp`)
was done out of order and passed its gate on 2026-10-21 with Frunchy. Bella Baxter won't run on
Caravana; its secrets provider (was 027) is in the backlog. Phase 2.6 resumes with 021. From 2.6 on, `docs/plan.md` names each feature after the screen it builds
and ends Phase 4 with "The handoff, screen by screen".

Phase 3 (Cluster) has started with 031 Add a server, built from a CodeAlta task before Phase 2.6's API
1.0; its gate is three servers that survive the loss of one (`deploy/dev/cluster-test.sh` is its first form).

Out of scope for now: the rest of Phase 3 unless asked (moving work off a server, a proxy per server,
cluster certificates and updates), single sign-on and per-project Nomad namespaces (053 built people and roles), secrets from Bella Baxter (backlog),
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
go run ./cmd/caravana admin init --email you@example.com      # the first owner (asks for a password); works while the server runs
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
# Release notes: each branch adds changelog.d/<name>.md (### Added|Changed|Fixed|Security|Upgrade bullets,
# changelog.d/README.md); CHANGELOG.md holds released sections only and never gets "## Unreleased" back.
tools/release.sh unreleased        # what the next release will say, gathered from changelog.d/
tools/release.sh check             # fails on a bad note file or a "## Unreleased" section (CI runs it)
bash tools/changelog-test.sh       # tests notes, unreleased, prepare, commit and check (CI runs it)
gh workflow run release.yml -f version=0.6.0   # release: gathers changelog.d/, commits, tags, builds, publishes
git tag v0.6.0 && git push origin v0.6.0        # also releases: the run gathers the tag's notes, then commits the section and the deleted notes to main if main is still the tag
tools/release.sh prepare 0.6.0     # what both run: changelog.d/*.md become "## 0.6.0 - <today>" and are deleted
tools/release.sh commit 0.6.0      # then both commit CHANGELOG.md and the deletions (git add -A), so no note is gathered twice
deploy/dev/install-test.sh         # the installer on a throwaway Lima VM: install, rerun, upgrade, rollback, uninstall (~20 min; IMAGE=debian-12|debian-13|ubuntu-26.04)
deploy/dev/install-test-all.sh     # the same on all four supported images, one after the other (before a release)
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