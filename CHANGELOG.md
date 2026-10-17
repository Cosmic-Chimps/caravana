# Changelog

What changed in each release, for people who run Caravana. Newest first. A release's notes on
GitHub are its section here: `tools/release.sh notes <version>` prints it, and the release
workflow refuses a tag whose version has no section.

Each section has the changes that matter to a user, then **Upgrade** when an upgrade needs
more than rerunning the installer. Add to "Unreleased" as you go; a release renames it.

## Unreleased

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

### Upgrade
- Rerun the installer so `CARAVANA_PUBLIC_URL` is set to your console domain; without it,
  messages still go out, just without links. The store gains its notification tables when the
  new version starts, and no new Nomad capability is needed.

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
