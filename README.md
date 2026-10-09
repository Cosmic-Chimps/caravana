# Caravana

A self-hosted PaaS on HashiCorp Nomad: bring an image or a Compose file and get an app with
HTTPS, health-checked deploys you can roll back, secrets, volumes and database backups. It starts
on one small VM. See [docs/vision.md](docs/vision.md) for where it's going.

## Install

On a fresh Ubuntu 24.04 VM (amd64 or arm64, 2 GB of memory, ports 22, 80 and 443 open), with a
domain pointing at it:

```bash
curl -fsSL https://github.com/Cosmic-Chimps/caravana/releases/latest/download/install.sh |
  sudo bash -s -- --email you@example.com --domain caravana.example.com
```

It installs Docker, Nomad (ACLs on), Traefik (Let's Encrypt) and Caravana. It asks for the admin's
password and ends with the console's address. Running it again upgrades. `--uninstall` removes
Caravana and leaves your apps running. Details: [deploy/install/README.md](deploy/install/README.md).

## Use

Sign in to the console to create apps, deploy, follow rollouts, read logs, set secrets and
back up databases. Everything the console does is also in the HTTP API
([api/openapi.yaml](api/openapi.yaml)), with API tokens made under Settings.

## Develop

Go 1.27+, no CGO. The Nomad lab runs in a Lima VM (`deploy/dev/lab.sh up`). The commands and
the state of each feature are in [CLAUDE.md](CLAUDE.md), and the rules every change follows are
in [.specify/memory/constitution.md](.specify/memory/constitution.md).
