# Installing Caravana

One command turns a fresh Ubuntu 24.04 VM into a Caravana host: Docker, Nomad with ACLs on,
Traefik with Let's Encrypt certificates, and Caravana under systemd.

## What you need

- **A VM**: Ubuntu 24.04 LTS, amd64 or arm64, 2 GB of memory, a public IP address. A fresh one:
  the installer refuses a machine that already runs Nomad or Docker.
- **Ports 22, 80 and 443** reachable from the internet. Open them in the provider's firewall if
  it has one.
- **A domain** for the console, with a DNS record pointing at the VM, e.g.
  `caravana.example.com`. To try it out you can skip it: the installer then uses
  `<ip-with-dashes>.sslip.io`, which resolves to the VM's IP address.

## Install

On the VM, as root or with sudo:

```bash
curl -fsSL https://github.com/Cosmic-Chimps/caravana/releases/latest/download/install.sh |
  sudo bash -s -- --email you@example.com --domain caravana.example.com
```

It checks the machine first and changes nothing if a check fails. Then it installs everything,
which takes a few minutes, and asks for the admin's password twice. It ends with the console's
address. Sign in there and deploy your first app.

| Option | Default | Meaning |
| --- | --- | --- |
| `--email` | (required the first time) | The admin's email; you sign in with it. |
| `--domain` | `<ip>.sslip.io` | The console's domain. It serves the console, the API and webhooks. |
| `--acme` | `production` | `staging`: Let's Encrypt's staging CA (no rate limits, untrusted certificates). `none`: Traefik's own certificate, for private networks. |
| `--acme-email` | `--email` | The Let's Encrypt account email (expiry notices). |
| `--from DIR` | download | Use a release's files from `DIR` (made with `tools/release.sh build`). |

Run from a script, without a terminal, the installer can't ask for a password. It then prints
the command that creates the admin:

```bash
sudo caravana admin init --email you@example.com
```

## What runs where

| Where | What |
| --- | --- |
| `https://<domain>/` | The console, the API (`/v1`) and webhooks (`/v1/webhooks/<app>`), through Traefik |
| `/opt/caravana/<version>/` | Caravana's binary and the files it was installed with; `current` points at the running one |
| `/var/lib/caravana/` | Caravana's data (one SQLite file) |
| `/etc/caravana/caravana.env` | Caravana's settings, including its own Nomad token (limited to what Caravana does) |
| `/var/lib/caravana-host/` | The installer's choices (`install.env`), Nomad's management token, Traefik's certificates |

From the internet only ports 22, 80 and 443 answer. Nomad, Caravana's own port and Traefik's
dashboard listen on loopback. Apps' ports are on a private interface, `caravana0`, and reach the
internet only through Traefik.

Apps on one host are not isolated from each other: any app's container can reach another app's
port directly. An app that believes `X-Forwarded-*` headers should believe them only from
`10.99.0.1`, the address Traefik's requests come from, never from the bridge gateway. A network
per app comes with Phase 3.

On the host:

```bash
sudo caravana version
sudo caravana admin status | reset-password | create-token --name ci
sudo caravana-nomad status          # Nomad's CLI with the management token
journalctl -u caravana              # Caravana's log
```

## Upgrade

Run the same command again, without options: it reuses the ones you gave the first time.

- A newer version replaces Caravana in place. It keeps the data, the admin, the API tokens, the
  certificates and every running app. Apps don't stop: Nomad runs them, not Caravana.
- If the new version doesn't answer its health check within a minute, the installer puts the
  previous version back, with its data as it was before the upgrade, and exits with an error.
- The same version changes nothing. An older version is refused.

Options given on a later run replace the recorded ones, for example a new `--domain`.

## Uninstall

```bash
curl -fsSL https://github.com/Cosmic-Chimps/caravana/releases/latest/download/install.sh |
  sudo bash -s -- --uninstall
```

This removes Caravana's service and binary and revokes its Nomad token. Nomad, Docker, Traefik
and every app keep running; the console's domain answers 502 until Caravana is installed again. Export the apps' jobspecs first (Configure › Export in the console,
or `GET /v1/apps/<id>/jobspec`) to manage them with plain Nomad. The data stays in
`/var/lib/caravana`, and installing again picks it up. Add `--purge` to delete it.

Removing the rest is up to you, because the apps depend on it:

```bash
sudo caravana-nomad job stop -purge <job>    # each app, and traefik
sudo systemctl disable --now nomad && sudo rm -rf /etc/nomad.d /opt/nomad /usr/local/bin/nomad
sudo apt-get purge docker.io
```

## Releases

`tools/release.sh build <version> <dir>` builds what a release publishes:

- `install.sh`, with its version set
- one tarball per architecture, with the binary and the host files from this folder
- `SHA256SUMS`

The installer checks the tarball against `SHA256SUMS` before using it. Pushing a `v<version>` tag
publishes a release (`.github/workflows/release.yml`). The command above downloads from GitHub,
so it works for others only once the repository is public.
