# Changelog

What changed in each release, for people who run Caravana. Newest first. A release's notes on
GitHub are its section here: `tools/release.sh notes <version>` prints it, and the release
workflow turns "Unreleased" into the version's section when it has none yet.

Each section has the changes that matter to a user, then **Upgrade** when an upgrade needs
more than rerunning the installer. Add to "Unreleased" as you go; a release renames it.

## Unreleased

### Added
- **Schedules**: a command on a timer in a fresh copy of an app. A schedule names an app and one
  of its services, a command (no shell implied: use `sh -c '…'`), when it runs (words such as
  "every day at 03:00" or "weekdays at 09:00", or a cron line), a stop-after limit (5 min to
  2 h), the memory of the run, and whether a run is skipped while the last one is still going.
  Each run starts a copy from the service's newest healthy deployment (the same image, variables
  and secret values), receives no traffic, and ends with the command; it is recorded with its
  exit code and the last 64 KiB of each output stream, with the service's secret values masked.
  Run now, Stop this run, Pause and Resume, Edit and Delete. A failed run (an exit code, the
  limit, couldn't start) is on the needs-you list and goes to the notification channels as a
  problem; three skipped runs in a row are a warning. Each service's backup appears in the same
  list as a built-in schedule. A service that mounts a volume can't have a schedule (its volume
  allows one copy). The Schedules page is in the sidebar; an app's Configure page lists its
  schedules.
- API 0.24.0: `listSchedules`, `createSchedule`, `getSchedule`, `updateSchedule`,
  `deleteSchedule`, `runSchedule`, `pauseSchedule`, `resumeSchedule`, `listScheduleRuns`,
  `getScheduleRun`, `stopScheduleRun` and `getScheduleRunOutput` (`text/plain`, never cached);
  `AttentionItem.kind` gains `schedule_failed` and `schedule_skipping` with `schedule` and
  `scheduleId`; the notification event gains `schedule` and `exitCode`, and documents `origin`,
  which it has sent since 0.8.1. Deploy tokens can't use any of it.
- Every clock time the console shows (a schedule's next run, a run's start, a backup's time, a
  deployment's Live time) reads in UTC with the viewer's local time beside it ("14:15 UTC ·
  16:15 local"), added in the browser; without JavaScript the UTC time stands. Schedules and
  backups are written in UTC; there is no timezone setting.

### Changed
- The Backups tab's run and next-backup times are drawn as clock times (UTC, local beside them).

**Upgrade**: rerun nothing. The scoped Nomad policy needs no new capability; a schedule run's job
is one more batch job while it runs and is purged after.

## 0.12.0 - 2026-10-22

### Added
- API 0.23.0: `Deployment.tokenName` is the name of the API token that made a deploy (`deployApp`,
  `deployCompose`, `promoteApp`, `redeployDeployment`), as it was then. It is absent for a deploy from
  the console, by the webhook, and for deployments from before. Deployments also keep when Nomad
  accepted their job, which the console reads.
- The Apps home shows **Deploying now**: a card for each app with a rollout in progress, with what it
  carries (who pushed it, a promotion, a redeploy), its copies as chips (each with the version it runs,
  the new version's first; starting and stopping ones marked) and, under them, how many new copies are
  healthy. It updates live and is gone when the rollout ends. While an app rolls out its Version cell
  reads `v5 → v6`.
- A filter on the Apps home, once there are more than 5 apps: by name, project, environment, image or
  address, by state and by kind. "Needs you" is the apps with an item in the needs-you list. An app that
  isn't a plain image app has its kind (compose, database, volume) after its name.
- The Activity rail, a deployment's entry and the app's summary say who deployed in the same words:
  "pushed to main by ana · 4e0a77a", "promoted from staging", "through the API by ci-github", "by the
  webhook", "from the console" ("Last deployed 26 minutes ago, promoted from staging."). An entry's run
  link is on the same line.
- Deployments in the history have a stages strip: Queued, Roll out and Live, with the wait, the
  rollout's progress ("1 of 2 new copies healthy") or duration, and the time it went live. The panel is
  drawn for a rollout in progress, the newest deployment and any that failed or was reverted; older
  ones get the same stages as one line. A reverted deployment's raw line from Nomad is prefixed "Nomad
  said:". After a rollback the app and its entry say what the running version repeats: "v3 (v1 again)
  keeps running."
- API 0.22.0: `AppStatus.services` lists, for each service of an app, its wanted and healthy
  copies (`name`, `wantedCopies`, `healthyCopies`), in the app's service order. It is absent for
  an app with no services.
- A Projects page (`/projects`) lists every project with its environments and app count, each row
  a link to the project. "New project" is on it.
- "Back up daily" on the Backups tab: a service with a database image (PostgreSQL, with a port) or a
  volume and no backup gets a row offering it. One click sets the backup (every day at 03:00 UTC,
  the last 7) and deploys; an app that keeps its Compose file keeps it, with the backup in its
  overlay.
- Variables & secrets (`/apps/{id}/variables`) and Domains (`/apps/{id}/domains`), two pages of
  Configure. Variables lists every secret (a lock and ten dots, never a value) and variable of the
  app, sets or replaces one (a secret with "Keep as a secret"), edits a service's variables as a
  `.env` text, and removes a secret. Domains lists the app's domains with a link to open each,
  adds one (with the DNS record to create) and removes one. Each change is a deploy, refused with
  the page again if the app changed since the page was drawn.

### Changed
- The console has one select, 32px tall with the field's 13px mono (it was 38px and 14px); the filter's
  and the Logs toolbar's buttons match it.
- Settings is one page per entry under a grouped nav: Account (`/settings`), Registries
  (`/settings/registries`), Backup storage (`/settings/backup-storage`), Notifications
  (`/settings/notifications`) and API tokens (`/settings/tokens`). The nav shows each entry's count, or a
  mark when something is breaking (a refused registry, a failed backup or a missing destination while a
  service has a backup policy, a failed delivery, a token unused for 90 days); the rows mark more (a paused
  channel, a full token without an end date). Registries say how many apps use each and "changed · not tested
  since" after a save; Notifications and API tokens lead with what needs a look, and an ntfy topic is shown
  by its host only, like a Slack URL. A failed backup says which service of which app, and why.
  `/settings/account` and `/account` redirect to `/settings`; every form posts where it did, and
  answers with the entry's own page. Links to `/settings#registries` and the like now land on Account:
  use the new addresses.
- The console's sidebar and top bar follow the design. The sidebar links Home, Projects and
  Settings (the pages that exist), with the host's initial, the host and the version at the top
  and the account row at the foot, where Sign out is now an icon button. The top bar's
  breadcrumbs read `project / environment` and "New app" is on every page.
- An app's header has its actions in the title row: Restart, Stop… and Deploy (a link to the
  deploy form), or Start when the app is stopped. A "← Home" link sits above the name, and the
  Running tab has a status mark when the app isn't healthy.
- An app with several services names each one and its copies, such as "2 services running v2:
  hollo and postgres, 1 copy each", instead of "2 copies running". A service short of its own
  copies now makes the app degraded even when another has a surplus.
- An app whose newest deployment failed or was reverted shows a "deploy failed" pill even while an
  older version still runs, and the Apps list sorts it first. Its summary and the needs-you list
  say the same sentence, "v3 failed its health checks. v2 keeps running." (or "No copies are
  running."). "0 of 1 copies healthy" now says so when copies exist and none is healthy.
- Configure is a short form: per service, Copies & size (copies, CPU, memory, ceiling) and Shipping
  (image, port and the health check), a read-only "Also set" for what the form leaves alone, then
  Domains and Secrets panels that link to their pages. The environment, the domains and the secrets
  left the form, and saving it keeps them as they are. The registry notice shows when an image
  can't be pulled for want of credentials.
- On the Variables page "Keep as a secret" is ticked by default.
- The Backups tab is redrawn: a row for each backed-up service that opens to its last 30 days (one
  bar per day, each a link to its run), its last ten runs with Restore…, "Restore from the last
  backup" and "Back up now", and the volume's host path and size; the restores follow. The
  console's fields are 32px tall (the sign-in's stays 42px) and a select is 38px.
- The app header offers only what can work: Restart and Stop… need a live version, and "Promote
  to <environment>" needs a healthy newest deployment with its definition recorded; the line is
  labeled "Other environments". On narrow screens the back link also names the project and
  environment.

## 0.11.0 - 2026-10-21

### Fixed
- An app can no longer take the console's own domain. Giving an app the domain in
  `CARAVANA_PUBLIC_URL` made Traefik hold two routes for it, and the console answered 502 while
  that app rolled out. Every way of saving or deploying an app (the API, the console's forms,
  Compose, Promote) now refuses it, naming the domain, and so does a redeploy or webhook of an
  app that already has it. The console's own route in Traefik also now has top priority, so no
  app's route can win over it.
  - **Upgrade:** rerun the installer on an existing host to put the route change in place. If an
    app uses the console's domain today, give it another one first (Configure).

## 0.10.0 - 2026-10-21

### Added
- Coding agents can read and act on an installation: `caravana mcp` is an MCP server that an
  agent such as Claude Code starts on your machine. Give it the console's address and an API
  token (`CARAVANA_URL`, and `CARAVANA_TOKEN` or `CARAVANA_TOKEN_FILE`). The agent can then answer
  "is umbral healthy?" or "why did the last deploy fail?" itself.
  - It reads projects, apps, deployments, copies, logs, volumes, backups, recent actions and the
    needs-you list, and it can wait for a deployment to end.
  - With `--allow-actions` it can also redeploy, deploy a new image to a service, roll back,
    promote, restart, stop, start and back up now.
  - A deploy token keeps it to one app. It never offers deleting, restoring, Run a command, the
    terminal, or any setting that holds a secret, and it never logs the token or an answer.
  - Setup: [docs/mcp.md](docs/mcp.md).
- Releases include the `caravana` binary for macOS (`darwin_arm64` and `darwin_amd64`), for
  running `caravana mcp` where the agent is.

## 0.9.1 - 2026-10-19

### Fixed
- The terminal opens on a host behind HTTPS. It said "Couldn't connect" every time: Traefik
  marks a websocket request as `wss`, not `https`, so Caravana read the request as plain HTTP,
  looked for the wrong session cookie and answered 401. Caravana now takes `wss` from a trusted
  proxy as HTTPS. When the session is the reason a terminal can't connect, the page now says so.

## 0.9.0 - 2026-10-19

### Added
- A service may set its `entrypoint`: the program and its first arguments, replacing the
  image's own, with `command` and `args` after it. A Compose file's `entrypoint` (list or
  string form) is now honored instead of refused. Configure shows it beside the command and
  keeps it when you save, a promotion takes the source's, and the exported jobspec writes it.
  Nothing to do when upgrading beyond rerunning the installer.
- API 0.21.0: `Service.entrypoint` (an array of strings, at most 64 items of at most 4096
  bytes, none empty), and `entrypoint` among the parts a promotion preview can list as changed.
- A terminal in a running app: the Running tab has a Terminal link for each copy, and it opens
  an interactive shell in that copy, drawn in the page (it needs JavaScript; Run a command stays
  for the cases that don't). Pick the shell: Automatic starts bash when the image has it and sh
  otherwise, or choose bash or sh. Colors, the arrow keys, history, resizing the window and
  programs like `top` or an editor work. Nothing typed or printed is kept: not in the logs, not
  in the app's history, not in a notification. What is recorded is that a terminal was opened,
  its shell, how long it lasted and how it ended. A terminal closes after 15 minutes without
  input, or after 60 minutes, and at most 5 are open at once. Only a signed-in console or a
  full API token can open one; a deploy token can't, and neither can another website (the
  request must come from the console's own address). It replaces SSH for looking around inside
  an app. Nothing to do when upgrading beyond rerunning the installer: Nomad's `alloc-exec`
  came with 0.8.0.
- API 0.20.0: `openTerminal` (`GET /v1/apps/{id}/copies/{copyId}/terminal`, a websocket; its
  messages are described in the operation). A new code, `too_many_terminals` (429).

### Changed
- A Compose file as Aspire generates it is accepted: `networks.*.driver: bridge` and
  `volumes.*.driver: local` (the defaults, which Aspire writes out) no longer fail. Another
  driver is still refused, and a refused network driver now says it is about networks, not
  volumes. Networks are warned about only when a file declares more than one ("networks don't
  separate services here", once, on `networks`), since that is when someone meant to keep services
  apart; a single network, like the one Aspire always writes, joins every service already and
  gets no warning. It used to be one warning per service plus one for the file.

## 0.8.1 - 2026-10-19

### Added
- Deploy from CI. Your GitHub workflow builds the image and a new step,
  `Cosmic-Chimps/caravana-action@v1`, deploys it: it waits until the app is healthy, prints the
  deploy's warnings, fails the workflow with the reason when the deploy fails or is reverted, and
  can promote to another environment afterwards. The action is its own repository; Caravana
  never builds an image.
- Deploy tokens: a token that can deploy only one app (its name, in every environment of one
  project). If it leaks it can deploy and promote that app, read its deployments and create it,
  and nothing else: not other apps, not Settings, not tokens. Make one in Settings › API tokens
  ("Only for one app") or on an app's Configure tab ("Create a deploy token for this app"). Like
  every token it is shown once.
- Where a deploy came from: a deploy started by the action records the commit, the branch or tag,
  who pushed and the workflow run. The history says "from CI" with a link to the run, and Slack,
  Discord, ntfy and webhook notifications of that deploy carry it too.
- New app › "From a GitHub repository": create the app without deploying it, get a deploy token
  for it, and copy the workflow and the repository secret to add. Your next push deploys it.
- API 0.19.0: `createToken` takes a `scope` (`{project, app}`) and `listTokens` shows it; a deploy
  token gets 403 `token_scope` for anything outside its app. `deployApp`, `deployCompose`,
  `promoteApp` and the webhook accept optional `Caravana-Origin-Commit`, `-Ref`, `-Actor`, `-Run`
  and `-Repository` headers (422 naming one that breaks its rule), and `Deployment` gains
  `origin`.

### Fixed
- Volume backups and restores failed with "the copy didn't pick up the request" once Nomad had
  been restarted on the host (as rerunning the installer does) after the app's copy started:
  Nomad then stops handing new requests to the copy's backup task until the copy restarts. A
  backup or restore now restarts the copy anyway when the request isn't picked up, which hands
  it over, so it works again without restarting the app by hand.
- A "Back up now" or a restore from the console no longer fails with an error when you leave the
  page while it starts; it goes on to its end.

## 0.8.0 - 2026-10-19

### Added
- The console's sidebar shows which Caravana it is, under the host name: `v0.7.0` for a release,
  `dev` for a build of your own.
- Run a command in a running app: the Running tab has a "Run a command" form for each copy. Type
  a program and its arguments (quotes work like in a shell; for pipes or variables run
  `sh -c '...'`) and the page answers with what it printed, what it wrote to stderr and its exit
  code. A command that runs longer than a minute is stopped and the answer says so; output past
  64 KB is cut and says so. The output is shown once, on that answer, and is kept nowhere: not in
  the logs, not in the app's history, not in a notification, not when you reload. What is
  recorded is that a command ran, its first word (not its arguments) and its exit code. This
  replaces SSH and Nomad's CLI for things like resetting an app's admin credential.
- Stop, Start and Restart for an app, in the app's header. Stop asks first and says what it does:
  every copy stops and the app stops answering, while its definition, secrets, volumes, webhook
  token and history stay. A stopped app shows as stopped (a ring mark) on the Apps page, the
  Apps page doesn't count it as trouble, and anything that would deploy it (the deploy form,
  Configure, the webhook, a promotion into it, a redeploy of a version) is refused with "The app
  is stopped: start it first." Its scheduled backups are skipped, not failed. Start runs exactly
  what ran before, with no new image lookup. Restart restarts the copies one at a time, waiting
  for each to be running (and healthy) before the next, so an app with two copies keeps
  answering (a request that reaches a copy in the second it restarts can still fail); restarts
  you ask for never count as "keeps restarting". Stop, Start and Restart are
  refused while a deploy or a volume backup of the app is running.
- A list of recent actions on the Running tab: restarts, stops, starts and commands, with how
  each ended and whether it came from the console or the API.
- API 0.18.0: `runCommand` (`POST /v1/apps/{id}/copies/{copyId}/run`, answered with
  `Cache-Control: no-store`), `stopApp`, `startApp`, `restartApp` (202, it goes on in the
  background) and `listAppActions`. `AppStatus.state` gains `stopped`, `AppRecord` and
  `AppSummary` gain `stoppedAt`. New codes `app_stopped`, `app_not_stopped`, `app_busy` and
  `copy_not_running` (all 409).

### Fixed
- "Keeps restarting" no longer stays on the Apps page after the app has settled. A copy's
  restarts add up for as long as it runs, so three restarts in the morning kept the warning up
  all day. Now a copy counts only while it has restarted within the last hour, as failed copies
  already did, and the warning clears by itself an hour after the last restart (a notification
  channel then gets "resolved").

### Upgrade
- Rerun the installer: Caravana's Nomad token gains `alloc-exec`, the permission "Run a command"
  needs. Until you do, the form says Nomad refused the command and to run the installer again;
  everything else works as before. Stop, Start and Restart need no new permission.

## 0.7.0 - 2026-10-18

### Added
- Create an app without deploying it: New app has a second button, "Create without deploying".
  Give it a name and the image your CI will push (it doesn't have to exist yet) and nothing is
  deployed, nothing fails and nothing reaches your notifications. The app's page says it is
  waiting for its first deploy and shows how to start it: Deploy now, a webhook token (the first
  call of the webhook deploys the app as it is stored), or the API call. So CI can be set up
  before its first build.
- A name is enough: create an app with only its name and let your automation fill in the rest.
  The app is listed as "not defined yet", can have a webhook token, and its page offers Define it
  in Configure (an image, a port, a domain and a health check, saved and deployed in one step),
  Deploy from Compose, and the API call with a body. Until it has a definition, Deploy now and
  the webhook answer that there is nothing to deploy and record nothing, because the webhook
  never chooses an image. Secret values can't be given when creating without deploying, only
  with a deploy; copying such an app to another environment copies its name.
- The Apps page counts apps that were never deployed as "waiting for a first deploy", not as
  something that needs you.
- API 0.17.0: `createApp` and `createEnvironmentApp` take `?deploy=false`: 201 with the app (its
  `revision` as `ETag`, its URL as `Location`) and no deployment, and the body may hold only an
  `id` (or `appName`) and a `name`. `AppRecord` and `AppSummary` gain `defined`. New code
  `app_not_defined` (409) on `deployApp` with an empty body, `redeployDeployment` and the
  webhook call.

## 0.6.0 - 2026-10-18

### Added
- Notifications: Caravana now tells you when something needs you and when a deploy ends, even
  when nobody has the console open. Settings › Notifications adds channels of four kinds: ntfy
  (a topic, with an optional access token), Slack and Discord (an incoming webhook URL), and a
  plain webhook that gets signed JSON. Each channel picks what it hears: problems, failed
  deploys, successful deploys. "Send a test" checks a channel, Pause and Resume stop and restart
  it, and each channel lists its last 20 deliveries.
- What is sent: a message when a problem on the Apps page's "needs you" list opens (failed or
  reverted deploys, out-of-memory kills, crash loops, unhealthy copies, failed backups and
  restores, no backup destination) and another when it has been gone for 5 minutes. A problem
  that flaps sends one "opened", not many. More than 10 new problems at once arrive as one
  summary. A deploy that ends sends its outcome, the version, how it was started (console,
  API or webhook) and the reason when it failed.
- Messages are queued, so a receiver that is down or slow never holds up deploys: a failed
  delivery is tried again after 30 seconds and after 2 minutes, then marked failed with its
  reason. Messages queued before a restart are still sent after it.
- Webhooks are signed: each message carries `Caravana-Signature: t=<unix seconds>,v1=<hex>`
  (HMAC-SHA256 of `<t>.<body>` with the channel's signing secret), plus `Caravana-Event` and
  `Caravana-Delivery`. The secret is shown once when the channel is added or the secret is
  replaced (the old one stops working at once). Receivers should refuse a `t` older than 5
  minutes. The message's fields are in `api/openapi.yaml`.
- Messages link to the console page about the problem or deploy when `CARAVANA_PUBLIC_URL` is
  set (the installer sets it from the console domain).
- Secrets of a channel (a Slack or Discord URL, an ntfy token, a webhook's signing secret) are
  write-only: kept in Nomad Variables, never shown again, never in a log, an error or a page.
- Settings: `CARAVANA_PUBLIC_URL` (`-public-url`) is the console's address for links;
  `CARAVANA_NOTIFY_RESOLVE_AFTER` (`-notify-resolve-after`, default 5m, at least 10s) is how
  long a problem must be gone before "resolved" is sent.
- API 0.15.0: `GET` and `POST /v1/notifications/channels`, `PATCH` and `DELETE`
  `/v1/notifications/channels/{id}`, `POST .../{id}/test`, `GET .../{id}/deliveries` and
  `POST .../{id}/secret`; a 409 `not_a_webhook`.
- Projects and environments: apps now live in a project and an environment, such as a project
  `shop` with `staging` and `production`, each with its own copy of every app: its own env,
  secrets, domains, memory, copies and backups. New project (and Add environment on the
  project's page) are in the console, and New app has a "Project and environment" choice. The
  Apps page groups apps by project and environment once you add the first one; until then it
  looks as it did. An app's page shows where it is, and lists the project's other environments.
- Copy to environment: "Copy to production" makes the same app in another environment without
  deploying it. The copy has the app's definition but no domains, and none of its secret values
  (they were never stored): its Configure page lists the secrets that need a value before the
  first deploy. Webhook tokens, backups and volume contents are not copied.
- Promote: "Promote to production" shows what would change (image and digest per service, the
  parts of the service's shape that are replaced, services added) and deploys the staging
  version to production by digest, so production runs exactly the image staging ran. The target
  keeps its own env, secrets, domains, memory, copies and backups. It refuses, with the reason,
  when staging's newest deployment isn't healthy, when its image is gone from the registry, or
  when production has a service staging doesn't (promoting would delete it). The history says
  "Promoted from staging" and links to the deployment it came from, and notifications name the
  project, the environment and the promotion.
- The app ID of an app outside Default › production is `<project>--<environment>--<name>`, for
  example `shop--staging--web`; names are lowercase labels and never contain `--`. The ID is
  still the Nomad job, router, secrets path and volume prefix, so a client can compute it and
  deploy with the same `deployApp` as before. The whole ID plus a service name must fit in 63
  characters (the console's New app also keeps names to 31).
- API 0.16.0: `listProjects`, `createProject`, `getProject`, `updateProject`, `deleteProject`,
  `createEnvironment`, `deleteEnvironment`, `listEnvironmentApps`, `createEnvironmentApp`,
  `getEnvironmentApp`, `previewPromotion`, `promoteApp` and `copyApp`. Apps, summaries, needs-you
  items, deployments and webhook messages gain `project`, `environment` and `appName`;
  `listApps` takes `?project=` and `?environment=`; a promotion's deployment has `promotedFrom`.
  New codes: `project_not_found`, `environment_not_found`, `project_exists`, `environment_exists`,
  `not_empty`, `protected`, `app_not_in_environment` and `promotion_removes_service`.

### Security
- An app created with `POST /v1/apps` and secret values in its body kept those values in
  Caravana's SQLite database, beside the names. Secret values now live only in Nomad Variables,
  whichever way an app is created, and the new version removes any such value from the database
  when it starts. Apps made in the console, by a deploy or from Compose were not affected. If you
  created apps with secrets through `POST /v1/apps`, consider changing those secrets, since older
  copies of the database (backups of `/var/lib/caravana`) may still hold them.

### Upgrade
- Rerun the installer so `CARAVANA_PUBLIC_URL` is set to your console domain; without it,
  messages still go out, just without links. The store gains its notification tables when the
  new version starts, and no new Nomad capability is needed.
- Nothing moves on upgrade: every app you have is in the project Default, environment
  `production`, with the ID it has now, so no job, secret, volume, backup, address or webhook
  URL changes and nothing is redeployed. The store gains its project tables when the new version
  starts. Default and its production environment can't be deleted (your existing apps live
  there); Default can be renamed for display. All environments share Nomad's `default`
  namespace: a namespace per environment would need Traefik restarted each time one is added,
  and gives nothing until there are teams.

## 0.5.0 - 2026-10-16

### Added
- Private registries: Settings › Registries keeps a user name and a password or token per
  registry (GitHub's `ghcr.io`, GitLab, Quay, Docker Hub, or your own). Deploys, redeploys,
  webhooks and rollbacks of images from that registry then work. "Test" checks the credentials
  against the registry and shows when they last worked. The password is write-only: it is never
  shown again, never in a log, a page, an export or a deployment record.
- Images from any registry host now deploy, including hosts with a port (`registry.example.com:5000`)
  and your own self-hosted ones. A host on `localhost` or another loopback address is reached over
  plain HTTP; every other host must serve its registry over HTTPS.
- Changing or removing a registry's credentials updates the apps that use them. Nothing restarts;
  the new credentials are used the next time Nomad starts a copy of the app (a deploy, or a copy
  that restarts).
- API 0.14.0: `GET /v1/registries`, `PUT` and `DELETE /v1/registries/{host}`,
  `POST /v1/registries/{host}/test`; a 422 `registry_auth_failed` when a registry turns the stored
  credentials down.

### Changed
- When an image is missing or private, the message now says "add credentials for <host> in
  Settings › Registries", instead of saying credentials aren't supported.
- A deploy that fails because the registry refused the stored credentials says so, with one action
  that opens Settings › Registries.

### Security
- To let Nomad pull a private image, the credentials reach that app's own container as the
  environment variables `CARAVANA_REGISTRY_USERNAME` and `CARAVANA_REGISTRY_PASSWORD`. Only
  apps whose image comes from a registry you added get them, and the job Nomad stores holds
  placeholders, not the password. Use a token that can only read images (for GitHub, a
  personal access token with `read:packages`), never your account password.

### Upgrade
- Nothing to do beyond rerunning the installer: the store gains its registries table when the new
  version starts, and no new Nomad capability is needed.

## 0.4.0 - 2026-10-16

### Added
- Volume backups: a service's backup can now use the engine `volume`, for apps that keep their
  data in files (SQLite, an embedded MariaDB, uploads) rather than in Postgres. On its schedule,
  or with "Back up now", the service stops, its volumes are archived to your backup destination,
  and it starts again. The stop is usually a few seconds, and the Backups tab shows how long the
  last one took.
- Restore a volume backup from the Backups tab. The service stops, its volumes get back exactly
  the backup's files (files added since are deleted), and it starts again. If anything fails
  along the way, the volumes are left as they were.
- The Backups tab opens with a Volumes section: each volume's mount, where it lives on the host,
  its size, and its last backup or "not backed up".
- API 0.13.0: `GET /v1/apps/{id}/volumes`; backups and restores say their `engine` and
  `stoppedSeconds`; a 409 `no_running_copy` when there is no running copy to back up or restore.

### Changed
- Every service with a volume gains a small task, `caravana-volumes`, that runs before the service
  starts (it measures the volumes, and does backups and restores). It reserves 32 MB while the
  service runs.
- Restarts Caravana asks for no longer count towards "keeps restarting" on the needs-you list.

### Upgrade
- Rerun the installer: Caravana's Nomad policy gains `alloc-lifecycle` and `read-fs`.
- Then deploy each app that has a volume once, so it gains the `caravana-volumes` task. Until
  then its volumes show "not measured yet" and can't be backed up with the `volume` engine.

## 0.3.0 - 2026-10-15

### Added
- An app deployed from a Compose file keeps the file, exactly as you wrote it (comments
  included), with the app and with every deployment. Secret values are never kept.
- Configure edits that file: change it and its domains, health checks, secrets and backup, then
  deploy (Preview is there for a look first). The definition below it is read-only, so the file
  is the one thing you edit.
- Apps made from Compose before this release ask for their file once on Configure. Paste it and
  deploy; from then on it is kept.
- Redeploying a past version brings back the file it was deployed from, and each history entry
  made from a file offers "Download Compose file".
- API 0.12.0: `GET /v1/apps/{id}/compose` (with the app's revision as `ETag`, so a client can
  edit and send it back with `If-Match`), `GET /v1/apps/{id}/deployments/{deploymentId}/compose`,
  and `composeKept` on apps and deployments.

### Changed
- Setting or removing a secret, the webhook and redeploys keep the file. Deploying a plain
  definition instead (`POST /v1/apps/{id}/deployments` with a body) drops it, and the deployment
  says so in its warnings.
- An app's top bar no longer has a Deploy button. Each tab's Deploy is its own form's button,
  so pressing it deploys what that page shows.

### Upgrade
- Rerun the installer. The store gains a column for the kept file (migration 0007) when the new
  version starts; nothing else to do.

## 0.2.0 - 2026-10-14

### Added
- Configure is an edit form: image, environment, port, domains, copies, health check, CPU and
  memory per service. Saving deploys, and is refused when the app changed since you opened the
  form.
- New app takes a pasted Compose file, with a preview before anything is created; Configure can
  deploy an existing app from a Compose file.
- The Running tab shows CPU and memory per copy, read from Nomad while the tab is open.
- The Apps home opens with a "needs you" list: failed or reverted deploys, out-of-memory kills,
  crash loops, unhealthy copies, failed backups and restores, and no backup destination.
- API 0.11.0: `getApp` sends the revision as `ETag`; deploys take `If-Match` (412
  `app_changed`) and Compose creates take `If-None-Match: *`; `listCopies?usage=true`;
  `GET /v1/attention`.

### Upgrade
- Rerun the installer: it turns on Nomad's allocation metrics, without which the Running tab
  has no usage to show.

## 0.1.1 - 2026-10-12

### Added
- Memory per service: a reservation (`memoryMB`, default 256) and a ceiling it may burst to
  (`memoryMaxMB`, default 1024 or the reservation if larger). From Compose, `mem_reservation`
  is the reservation and `mem_limit` the ceiling. API 0.10.0.

### Upgrade
- Rerun the installer: it turns on Nomad's memory oversubscription. Without it Nomad ignores the
  ceiling, and the deployment's warnings say so.

## 0.1.0 - 2026-10-11

The first release: a single-node PaaS on Nomad, installed with one command.

### Added
- One-command install on a fresh Ubuntu 24.04 VM: Docker, Nomad with ACLs, Traefik with Let's
  Encrypt, and Caravana under systemd. Rerunning the installer upgrades (and rolls back if the
  new version fails), `--uninstall` leaves your apps running.
- Deploy an image or a Compose file. Tags are pinned to digests, every deploy is health-checked
  and blue/green, and a failed one reverts on its own.
- Redeploy by webhook, with a per-app token.
- Secrets per service, write-only, kept in Nomad Variables.
- PostgreSQL backups on a schedule to S3-compatible storage, with restore.
- Redeploy any past version, and export any app as a plain Nomad jobspec.
- The web console (apps, deployments with live progress, configure, logs, backups) and the HTTP
  API (`api/openapi.yaml`), behind one admin login and API tokens.
