# Installing Caravana

One command turns a fresh Ubuntu 24.04 VM into a Caravana host: Docker, Nomad with ACLs on,
Traefik with Let's Encrypt certificates, and Caravana under systemd. The same installer turns more
VMs into servers of that host's cluster (see Adding a server).

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
| `--address` | the public IP (first server) | The address the other servers reach this one at, an IPv4 or IPv6 address. Only the first server needs it when it has no public IP to find. |
| `--first` | (the default) | This is the first server, the one that runs Caravana and Traefik. |
| `--from DIR` | download | Use a release's files from `DIR` (made with `tools/release.sh build`). |

`--join`, `--url`, `--insecure` and `--leave` are for the other servers: see Adding a server.

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
| `/usr/local/libexec/caravana-update` | The updater: a root script that runs an update the console asked for (see Updating) |
| `caravana-update.timer`, `caravana-update.service` | The timer starts the updater every minute; it does nothing unless a request is waiting |
| `/var/lib/caravana-update/` | The updater's status and logs. Only root writes here; Caravana reads the status |
| `/usr/local/libexec/caravana-node` | The server agent: a copy of the release's binary, so a rolled-back Caravana never takes it away. Runs on every server |
| `/etc/caravana-node/` | The agent's folder (root only): its WireGuard key, what it last applied (`state.json`), where the console is and the token it calls it with (`node.env`), Nomad's gossip key on a Nomad server |
| `/etc/nomad.d/nomad.hcl`, `/etc/nomad.d/tls/` | Nomad's config and certificates. The agent writes them; change them through Caravana, not by hand |
| `caravana-mesh.service` | Makes `caravana0` and the WireGuard interface `caravana-mesh` at boot, before Nomad starts (Nomad requires it) |
| `caravana-node.timer`, `caravana-node.service` | The timer starts the agent every 30 seconds: it asks the console for this server's peers, role and certificate and applies them |

Every server runs Docker, Nomad, the agent and the mesh. Only the first one runs Caravana, Traefik
and the updater, and so answers on 80 and 443.

From the internet only ports 22, 80 and 443 answer on the first server, and 22 on the others; all of
them also answer WireGuard on 51820/udp. Nomad, Caravana's own port and Traefik's dashboard listen on
loopback, and Nomad's RPC on the private mesh address. Apps' ports are on a private interface,
`caravana0`, and reach the internet only through Traefik.

Caravana's servers use these private ranges between them, on every server: `10.98.0.N` for the mesh,
`10.99.0.N` for app ports (`caravana0`) and `10.97.0.0/16` for the containers of servers after the
first (the first keeps Nomad's `172.26.64.0/20`). N is the server's number, 1 to 63, given by the
console. The installer refuses a host that already has an address or a route inside
`10.97.0.0/16`, `10.98.0.0/24` or `10.99.0.0/24` on another interface.

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

## Adding a server

In the console, Servers › Add a server makes a one-hour, single-use join token and shows the command
to run on the new VM (Ubuntu 24.04, 2 GB, Docker and Nomad not installed yet):

```bash
curl -fsSL https://github.com/Cosmic-Chimps/caravana/releases/download/vX.Y.Z/install.sh |
  sudo bash -s -- --join cvjn_... --url https://caravana.example.com
```

The VM needs to reach the console's address over HTTPS, and **UDP 51820 must be open between the
servers** (in the provider's firewall too: the installer opens it on the host itself). Each server
must be reachable by the others at one address, which the console sees on the join call; give
`--address IP` when it isn't the one the VM calls from.

The installer installs Docker, Nomad and the agent, joins (the agent sends the token, its WireGuard
public key and a certificate request; the console answers with the peers and a certificate signed by
the cluster's CA, and no private key ever leaves the server), starts Nomad and then waits, up to three
minutes, for the console to confirm four checks, which it prints as they pass: connected, Docker,
network (every other server answers on the private network) and ready. It ends with "This server
joined as NAME." or, on a timeout, the checks still open (exit 1); the agent keeps trying every 30
seconds either way.

- A console on Traefik's own certificate (`--acme none`) needs `--insecure`, which the console's
  command adds by itself in that case.
- The token goes only to the agent's command line, once. It isn't written anywhere and a rerun
  doesn't need it: run the same command again to upgrade the server with a newer release; a server
  that joined already makes no call.
- With three servers ready, the console makes the first and the two oldest others Nomad servers
  (three voters survive losing one); the agent promotes the server when the console says so and Nomad
  restarts once on it, keeping its copies.
- A host already running Caravana from before servers existed converts when you run this release's
  installer on it: Nomad restarts twice, once only, to move to the private network and to correct its
  raft peer address (its jobs, tokens and variables are kept, apps keep running). Its old Nomad config
  is kept once as `/var/lib/caravana-host/nomad.hcl.before-mesh`. If Nomad has no leader within two
  minutes after either restart, the installer stops and says what it did and where that file is.

## Taking a server out

A server that is down (or a join nobody used) is removed in the console: Servers › the server ›
Remove. A server that is running is taken out on the server first:

```bash
sudo bash install.sh --leave        # the install.sh of the release it joined with
```

That stops the agent and Nomad, deletes `caravana0` and the mesh interface, removes the agent, its
units, its certificates and Nomad's config, and keeps Docker, Nomad's binary and the packages. Remove
the server in the console too, or it stays listed as down. Containers the apps left running are not
touched (`docker ps`). The first server can't leave: its `--uninstall` removes Caravana as before.

## Upgrade

Run the same command again, without options: it reuses the ones you gave the first time.

- A newer version replaces Caravana in place. It keeps the data, the admin, the API tokens, the
  certificates and every running app. Apps don't stop: Nomad runs them, not Caravana.
- If the new version doesn't answer its health check within a minute, the installer puts the
  previous version back, with its data as it was before the upgrade, and exits with an error.
- The same version changes nothing. An older version is refused.

Options given on a later run replace the recorded ones, for example a new `--domain`.

## Updating

Two ways, with the same installer and the same rollback.

**From the console.** The Updates page (Operator view) shows a newer release with an **Update** button. The
console is not root and never gets to be: pressing the button writes a request that holds a version
and nothing else (`/var/lib/caravana/update-request`). A root timer, `caravana-update.timer`, looks
for it every minute and starts the updater, which:

1. ignores anything that isn't a plain, small file with a valid request ID and a version it can
   read as `X.Y.Z`, and refuses a version that isn't newer than the one running;
2. downloads that release's `SHA256SUMS`, `install.sh` and tarball from the release's address
   on GitHub, and refuses to go on unless the installer and the tarball match `SHA256SUMS` and the
   installer is for that version;
3. runs that installer, which does the upgrade above, with its rollback.

The console is down for about a minute while the new version starts, and comes back on it. The
Updates page says how it went: updated, rolled back (the old version runs again, with its data as it
was), failed or refused. A rolled back or failed update also shows on the needs-you list and is sent
to your notification channels. Apps keep running throughout.

The updater's status and the installer's whole output are kept in `/var/lib/caravana-update/`, which
only root can write. `status` is the newest run; `logs/<id>.log` is that run's output, root-only,
and the newest 10 are kept.

```bash
sudo cat /var/lib/caravana-update/status
sudo less /var/lib/caravana-update/logs/<id>.log
journalctl -u caravana-update         # what the updater itself said
systemctl status caravana-update.timer
```

A host installed before the updater existed doesn't have it: run the newer release's installer by
hand once (below), and later updates can start from the console.

**By hand.** Rerun the installer of the newer release over SSH, as under Upgrade. The Updates page
shows the exact command.

The updater reads one optional file, `/etc/caravana/update.env`, only when root owns it and nobody
else can write it. Its `CARAVANA_UPDATE_RELEASES=<address>` line replaces GitHub's download address.
It exists for the installer's own tests; leave it out.

## Uninstall

```bash
curl -fsSL https://github.com/Cosmic-Chimps/caravana/releases/latest/download/install.sh |
  sudo bash -s -- --uninstall
```

This is for the first server (on a server that joined, `--leave` is the command: see Taking a server
out). It removes Caravana's service and binary, the updater and its timer, the agent's timer and its
`node.env`, and revokes its Nomad token. Nomad, the private network, Docker, Traefik and every app keep
running, and so do the agent and its units, which Nomad needs. The console's domain answers 502 until
Caravana is installed again; other servers can't sync until then, and keep running what they run.
Export the apps' jobspecs first (Configure › Export in the console, or `GET /v1/apps/<id>/jobspec`) to
manage them with plain Nomad. The data stays in `/var/lib/caravana`, and installing again picks it up
(and registers the server again). Add `--purge` to delete it.

Removing the rest is up to you, because the apps depend on it:

```bash
sudo caravana-nomad job stop -purge <job>    # each app, and traefik
sudo systemctl disable --now nomad caravana-mesh && sudo rm -rf /etc/nomad.d /opt/nomad /usr/local/bin/nomad
sudo rm -rf /etc/caravana-node /usr/local/libexec/caravana-node /etc/systemd/system/{nomad,caravana-mesh,caravana-node}.service
sudo apt-get purge docker.io
```

## Releases

`tools/release.sh build <version> <dir>` builds what a release publishes:

- `install.sh`, with its version set
- one tarball per architecture, with the binary and the host files from this folder (the updater and
  its two systemd units, and the units of Nomad, the private network and the server agent, are among them)
- `SHA256SUMS`

The installer checks the tarball against `SHA256SUMS` before using it. Pushing a `v<version>` tag
publishes a release (`.github/workflows/release.yml`). The command above downloads from GitHub,
so it works for others only once the repository is public.
