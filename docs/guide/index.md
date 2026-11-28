# Caravana

Caravana is a self-hosted platform for running apps on servers you own. You give it a container
image, a Compose file or a Git repository, and it runs the app with HTTPS, health-checked deploys
you can roll back, secrets, volumes, database backups, logs and monitoring. It starts on one small
VM and can grow to a few servers without changing how you describe your apps.

It is open source (GNU AGPL v3) and built on HashiCorp Nomad. You never write a Nomad job, and you
can always export the one Caravana made for an app.

## Who it is for

Caravana is for people who run their own apps on budget VMs: indie developers, small teams,
agencies hosting a handful of client projects. You are comfortable with Docker and a shell, and you
want a console, an API and a deploy button instead of a pile of scripts. You do not need to know
Nomad.

## What you get

- **HTTPS for every domain.** Traefik serves your apps and gets certificates from Let's Encrypt, or
  from another ACME issuer, or from certificates you upload.
- **Deploys that check themselves.** Every deploy pins its images by digest, starts new copies next
  to the old ones, switches traffic only when the new ones pass their health check, and goes back to
  the old version by itself if they don't. Every earlier healthy version can be deployed again.
- **Secrets that are not stored with the app.** Values are write-only and live in Nomad's secret
  store, never in the app definition, the history or the logs. They can also come from a secret
  provider.
- **Volumes and backups.** Directories that keep their data across deploys, and scheduled backups of
  Postgres, MySQL, MariaDB, MongoDB and SQLite databases, of OpenSearch and Elasticsearch clusters and of whole volumes, to an S3-compatible
  bucket.
- **Logs, requests and monitoring.** Live output of every copy, the HTTP requests each app served,
  and memory, traffic and error charts against the same hour of the previous week.
- **Ways in.** The web console, an HTTP API with tokens, webhooks, a GitHub Action, a .NET Aspire
  publisher and an MCP server for coding agents. Everything the console does is also in the API.
- **More servers later.** Add a server with one command and the apps can spread across it, and
  three servers survive the loss of one.

## What it is not

Say it early, so you can decide:

- **It is not Kubernetes, and it is not a managed cloud.** You run the VM, and you keep it patched.
  Caravana shows which operating system updates are waiting but does not install them.
- **It runs on a VM or a physical server, not in a container.** The installer supports Ubuntu 24.04
  and 26.04 LTS and Debian 12 and 13, on amd64 and arm64, and refuses anything else before it
  changes the machine.
- **It runs container images.** There is no GPU scheduling and no way to run a plain binary
  without an image.
- **Apps on one server are not isolated from each other on the network.** Any app's container can
  reach another app's port directly. See [Install](install.md) for what that means for forwarded
  headers.
- **Caravana itself is not highly available.** Its data is one SQLite file on the first server, and
  all public traffic enters through Traefik on that server. Servers you add run apps; if the first
  one is lost, the console and the public addresses are down until it is back.
- **Compose support is a subset.** Attributes Caravana cannot honor are refused, naming the field,
  or produce a warning on the deploy. Nothing is dropped in silence. `build:` in a Compose file is
  refused: use [Build from Git](../building-from-git.md) instead.
- **Roles are simple.** There are owners, admins and members, with a role per project and
  optionally per environment. There is no single sign-on and no custom roles.
- **The API is not 1.0 yet.** Its version is 0.x.

## How it works

Caravana is one Go binary with a SQLite database. It runs under systemd on the first server and
holds the console, the API and the deploy logic. It does not run your containers: Nomad does, with
Docker, and Nomad keeps them running even if Caravana is stopped or uninstalled. Traefik, started by
Nomad, receives the traffic on ports 80 and 443 and sends each domain to the right app.

When you deploy, Caravana looks up the image's digest, turns the app definition into a Nomad job,
hands the job to Nomad and follows the rollout until it is healthy or has been reverted. The app
definition is the only thing you maintain. If you ever want to leave, **Export jobspec** gives you a
plain Nomad job for the app and for each version in its history, and uninstalling Caravana leaves your apps running.

## Where to go next

1. [Install](install.md) Caravana on a VM, which takes a few minutes.
2. Deploy [your first app](first-app.md) from the console.
3. Take [a tour of the console](console.md).

Then pick what you need:

- **Deploy:** [from an image](deploy/image.md), [from a Compose file](deploy/compose.md),
  [from a template](deploy/templates.md), [from Git](../building-from-git.md),
  [from CI](deploy/ci.md), [from a .NET Aspire AppHost](deploy/aspire.md), and
  [how a deploy works and how to roll back](deploy/rollback.md).
- **Configure an app:** [variables and secrets](configure/variables.md),
  [connect apps to each other](configure/connect-apps.md),
  [domains and HTTPS](configure/domains.md), [volumes](configure/volumes.md),
  [copies, memory and health checks](configure/resources.md).
- **Day to day:** [projects and environments](operate/projects.md), [logs](operate/logs.md),
  [backups](operate/backups.md), [monitoring](operate/monitoring.md) and
  [notifications](operate/notifications.md).
- **Run the host:** [upgrade](host/upgrade.md), [add a server](host/servers.md),
  [sign-in](host/sign-in.md) and [troubleshooting](host/troubleshooting.md).
- **Reference:** [settings](reference/settings.md), [the command line](reference/cli.md),
  [the HTTP API](reference/api.md) and [MCP for coding agents](../mcp.md).

What changed in each release is in the [release notes](../../CHANGELOG.md).
