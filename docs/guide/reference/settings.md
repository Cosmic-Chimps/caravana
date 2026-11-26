# Settings

Caravana's server reads its settings from environment variables and command-line flags when it starts.
A flag wins over its environment variable, which wins over the built-in default. Each setting has both
forms: `CARAVANA_LISTEN` and `-listen` are the same setting. An invalid value stops Caravana at start
with an error that names the variable and the flag.

An environment variable that is set but empty counts as not set and gives the default.

## On an installed host

The installer writes the settings to `/etc/caravana/caravana.env`, a file only root and the `caravana`
user can read, and systemd gives them to Caravana at start. It contains these lines:

```text
CARAVANA_LISTEN=127.0.0.1:7070
CARAVANA_DATA_DIR=/var/lib/caravana
CARAVANA_CERT_RESOLVER=letsencrypt
CARAVANA_TRUSTED_PROXIES=127.0.0.1,::1
CARAVANA_PUBLIC_URL=https://caravana.example.com
CARAVANA_ACME=production
CARAVANA_ACME_EMAIL=ana@example.com
CARAVANA_EDGE_ADDRESS=127.0.0.1:443
CARAVANA_DOCKER_ROOT=/var/lib/docker
NOMAD_ADDR=http://127.0.0.1:4646
NOMAD_TOKEN=…
```

`CARAVANA_CERT_RESOLVER` is empty when you installed with `--acme none`. `NOMAD_TOKEN` is Caravana's own
Nomad token, limited to what Caravana does.

To apply a change, edit the file and restart Caravana:

```bash
sudo systemctl restart caravana
```

Apps keep running while Caravana restarts; the console is briefly unavailable.

The installer writes this file from its own values every time it runs, an upgrade included, so a line
you add by hand is lost the next time. The settings in the file follow the installer's options
(`--domain`, `--acme`, `--acme-email`: see [Install](../install.md)). To keep an extra setting across
upgrades, put it in a systemd drop-in that adds a second environment file, which systemd reads after
the first:

```ini
# sudo systemctl edit caravana
[Service]
EnvironmentFile=/etc/caravana/extra.env
```

*As of writing, the drop-in is standard systemd behavior and not something the installer tests.*

To change the console's domain, do not edit `CARAVANA_PUBLIC_URL`: use **Operator › Console address**,
which checks the new name, moves the route and updates the file for you
([Console address](../../console-address.md)).

## Listening

| Variable | Flag | Default | What it does |
| --- | --- | --- | --- |
| `CARAVANA_LISTEN` | `-listen` | `127.0.0.1:7070` | The address of the console and the API, as `host:port`. Loopback by default: Traefik is what faces the internet. |
| `CARAVANA_WEBHOOK_LISTEN` | `-webhook-listen` | empty (off) | An extra listener that serves only `POST /v1/webhooks/{id}`, so webhooks can be exposed while everything else stays private. It must not be the same address as `CARAVANA_LISTEN`. |
| `CARAVANA_TRUSTED_PROXIES` | `-trusted-proxies` | none | Comma-separated IP addresses or CIDR ranges of the proxies whose `X-Forwarded-For` and `X-Forwarded-Proto` headers Caravana believes. With Traefik on the same host the installer sets `127.0.0.1,::1`. It decides whether cookies are marked `Secure` and which address a failed sign-in is counted against. |

## Data

| Variable | Flag | Default | What it does |
| --- | --- | --- | --- |
| `CARAVANA_DATA_DIR` | `-data-dir` | `data` | The directory of the SQLite store and its lock file. The installer sets `/var/lib/caravana`. The `caravana admin` and `caravana server` commands read the same variable. |

## Nomad

| Variable | Flag | Default | What it does |
| --- | --- | --- | --- |
| `NOMAD_ADDR` | `-nomad-addr` | `http://127.0.0.1:4646` | Nomad's HTTP API, an `http://` or `https://` URL. |
| `NOMAD_TOKEN` | none | none | Nomad's ACL token. It comes from the environment only: a flag would show it in the process list. It is never logged. |

## Public address and certificates

| Variable | Flag | Default | What it does |
| --- | --- | --- | --- |
| `CARAVANA_PUBLIC_URL` | `-public-url` | empty | The console's own address, such as `https://caravana.example.com`, with a host and no query. Caravana uses it for the links in notifications, the links it makes for invitations, the workflow it shows for **From a GitHub repository**, and, when it is an `https://` address, as the issuer of workload identity tokens. Empty means messages have no links. |
| `CARAVANA_TRAEFIK_ENTRYPOINT` | `-traefik-entrypoint` | `websecure` | The name of Traefik's HTTPS entrypoint that app routers use. Letters, digits, `-` and `_`. |
| `CARAVANA_CERT_RESOLVER` | `-cert-resolver` | empty | The name of the Traefik certificate resolver that app routers use. Empty means Traefik's default certificate. |
| `CARAVANA_ACME` | `-acme` | empty | The certificate authority the installation uses: `production`, `staging`, `none` or empty for not said. Caravana words its certificate advice by it and shows it read-only in **Settings › Certificates**. It does not change Traefik. |
| `CARAVANA_ACME_EMAIL` | `-acme-email` | empty | The address given to the certificate authority, shown read-only. |
| `CARAVANA_EDGE_ADDRESS` | `-edge-address` | `127.0.0.1:443` | Where Traefik's HTTPS entrypoint answers, as seen from this machine. The domain checks connect here, naming the domain, to read the certificate it serves. |

## Defaults for apps

These apply to a service that does not set its own value.

| Variable | Flag | Default | What it does |
| --- | --- | --- | --- |
| `CARAVANA_DEFAULT_CPU_MHZ` | `-default-cpu-mhz` | `100` | CPU reserved per service, in MHz. At least 1. |
| `CARAVANA_DEFAULT_MEMORY_MB` | `-default-memory-mb` | `256` | Memory reserved per service, in MB. At least 10. |
| `CARAVANA_DEFAULT_MEMORY_MAX_MB` | `-default-memory-max-mb` | `1024` | The memory a service may use while the server has it spare, in MB. At least the default reservation. |
| `CARAVANA_VOLUME_BACKUP_TIMEOUT` | `-volume-backup-timeout` | `30m` | The longest a volume backup or restore may take, as a duration such as `30m`. At least one minute. The service stays stopped for as long as the run lasts. |

Copies, memory and the rest of a service's resources are in [Copies, memory and health
checks](../configure/resources.md).

## Helper images

Caravana runs small helper containers for some features. Each is built in, pinned by digest. A setting
replaces one, and must be an image pinned by digest (`repository@sha256:…`) with no tag; empty keeps the
built-in one.

| Variable | Flag | What it is |
| --- | --- | --- |
| `CARAVANA_NAMES_IMAGE` | `-names-image` | The sidecar that lets the services of an app reach each other by name. |
| `CARAVANA_BACKUP_IMAGE` | `-backup-image` | The image that uploads and downloads backups (rclone). |
| `CARAVANA_SQLITE_IMAGE` | `-sqlite-image` | The sidecar that makes and checks copies of a SQLite database. |
| `CARAVANA_SCANNER_IMAGE` | `-scanner-image` | The vulnerability scanner of the optional image scanner. |
| `CARAVANA_BUILDER_IMAGE` | `-builder-image` | The rootless BuildKit that builds an image from a Git repository. |
| `CARAVANA_GIT_IMAGE` | `-git-image` | The image that clones a Git repository for a build. Built in: `alpine/git`. |

## Notifications

| Variable | Flag | Default | What it does |
| --- | --- | --- | --- |
| `CARAVANA_NOTIFY_RESOLVE_AFTER` | `-notify-resolve-after` | `5m` | How long a problem must be gone from the needs-you list before a notification says it is resolved, so a problem that flaps sends one message. A duration, at least `10s`. |

The channels themselves are set in **Settings › Notifications**: see
[Notifications](../operate/notifications.md).

## Monitoring and the host

| Variable | Flag | Default | What it does |
| --- | --- | --- | --- |
| `CARAVANA_TRAEFIK_METRICS_URL` | `-traefik-metrics-url` | `http://127.0.0.1:8081/metrics` | Where Traefik serves its Prometheus numbers, an `http://` URL on this machine's loopback. Monitoring reads request counts and latencies from it. Turn the request numbers off by giving the flag an empty value, `-traefik-metrics-url=`; an empty environment variable gives the default. |
| `CARAVANA_DOCKER_ROOT` | `-docker-root` | `/var/lib/docker` | The directory Docker keeps its images and layers in, as `docker info` says, an absolute path. Monitoring watches the disk it is on. The installer writes it. |

## For development and tests

You should not need these on a real host.

| Variable | Flag | Default | What it does |
| --- | --- | --- | --- |
| `CARAVANA_HOST_ROOT` | `-host-root` | empty (`/`) | Read the host's operating system files (apt's history, the reboot marker) under this absolute path instead of `/`. The files are only read. |
| `CARAVANA_RELEASE_URL` | `-release-url` | empty (GitHub's) | The address of the "latest release" answer the release check asks. `https://`, or `http://` on loopback. |
| `CARAVANA_TEMPLATES_URL` | `-templates-url` | empty (the catalog repository's latest release) | The directory the template catalog check reads `SHA256SUMS` and `catalog.v2.json` from, falling back to `catalog.json`. `https://`, or `http://` on loopback. |
| `CARAVANA_UPDATER_DIR` | `-updater-dir` | `/var/lib/caravana-update` | The directory the root updater writes its status and logs in. An absolute path. Caravana only reads it. |

## Settings of other commands

Some variables are read by `caravana` subcommands, not by the server:

- `CARAVANA_URL`, `CARAVANA_TOKEN` and `CARAVANA_TOKEN_FILE` configure `caravana mcp`, which runs on a
  developer's machine and talks to the API. They are described in [MCP for coding
  agents](../../mcp.md).
- `CARAVANA_INSECURE=1` makes `caravana node` skip checking the console's certificate, as `--insecure`
  does. See [Servers](../host/servers.md).
- `CARAVANA_DATA_DIR` is also read by `caravana admin` and `caravana server`.

See [The caravana command](cli.md).

## Next

- [Install](../install.md) for the installer's options
- [The caravana command](cli.md)
