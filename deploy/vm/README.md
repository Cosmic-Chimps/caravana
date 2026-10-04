# A public Caravana host on a VM

`deploy/dev/` is the local lab; this folder runs the same Caravana on a real, public VM, with
Let's Encrypt certificates and a webhook the internet can reach. It's the Phase 1 rehearsal
(feature 005), not the product installer (plan item 012).

## What you need

- **A VM**: Ubuntu 24.04 LTS, arm64 or amd64, 2 GB RAM (e.g. Hetzner CAX11), reachable over
  SSH as root or a user with passwordless sudo, with your SSH key.
- **A domain** with two DNS records pointing at the VM's IP (or one wildcard):
  - the app the proof deploys, e.g. `whoami.example.com`
  - the webhook host, e.g. `hooks.example.com`
- **The provider's firewall**, if it has one, allowing TCP 22, 80, 443.
- On your Mac: Go, `ssh`, `scp`, `python3`.

```bash
cp deploy/vm/host.env.example deploy/vm/host.env    # gitignored; fill it in
```

## Commands

```bash
deploy/vm/host.sh up       # set up or update the host; a second run prints an empty "changed:"
deploy/vm/host.sh tunnel   # private access: API :17070, Nomad :14646, Traefik dashboard :18081
eval "$(deploy/vm/host.sh env)"            # NOMAD_ADDR, NOMAD_TOKEN, CARAVANA_URL for the tunnel
deploy/vm/host.sh admin init --email you@example.com   # once per host: the one admin (asks for a password)
deploy/vm/host.sh admin status | reset-password | create-token --name ci | revoke-token --name ci
deploy/vm/host.sh prove    # the Phase 1 gate, ~12 minutes; prints PASS/FAIL per step
deploy/vm/host.sh ssh      # a shell on the host; `host.sh ssh sudo lab-nomad status` runs Nomad's CLI
```

`admin` runs `caravana admin <command>` on the host over SSH as the `caravana` user, on the
service's data directory; it works while Caravana runs. Every API route except `/v1/health` and
the webhook call needs an API token or a signed-in session, so create the admin once, right
after the first `up`: `host.sh admin init --email you@example.com`. Then sign in at `/login`
through the tunnel (`http://127.0.0.1:17070/login`), or make a token with
`host.sh admin create-token --name laptop` (printed once). `prove` needs that admin: it stops
and tells you if there is none, makes its own token `gate-<time>`, and revokes it when the run
ends.

`up` builds Caravana for the host's architecture, copies it with the files here, and runs
`setup.sh` on the host, which reuses the lab's `deploy/dev/setup.sh` for Docker, CNI and Nomad.

## What runs where

| Port | Bound to | Public |
| --- | --- | --- |
| 22 | all | yes (SSH) |
| 80 | Traefik | yes: Let's Encrypt challenges, redirects to HTTPS |
| 443 | Traefik | yes: apps, and `/v1/webhooks/*` on the webhook host |
| 4646–4648 | 127.0.0.1 (Nomad) | no |
| 7070 | 127.0.0.1 (Caravana API) | no: use the tunnel |
| 7071 | 127.0.0.1 (Caravana webhook listener) | only through Traefik's webhook route |
| 8081 | 127.0.0.1 (Traefik dashboard) | no: use the tunnel |
| 20000–32000 | 10.99.0.1 (`caravana0`, allocation ports) | no |

App ports live on a private dummy interface because Nomad's port mappings are DNAT rules a
host firewall can't block; `ufw` (deny incoming except 22/80/443) is the second layer.

Caravana runs as the `caravana` user under systemd (`/etc/caravana/caravana.env`, data in
`/var/lib/caravana`) with its own Nomad token, limited to submitting and reading jobs.

## Let's Encrypt: staging vs production

`CARAVANA_ACME_STAGING=1` uses Let's Encrypt's staging CA: no rate limits, but certificates
browsers don't trust. Use it while iterating; `prove` then reports the `https-trusted` step as
"staging certificate" and the gate fails on purpose. Set it to `0` and run `up` again to prove
the gate with real certificates (Traefik keeps them in `/var/lib/caravana-host/traefik`).
Switching either way makes `up` stop Traefik and delete its stored certificates (`changed:`
lists `certificates`), because Traefik would otherwise keep serving the old CA's certificates
until they expire.

## Troubleshooting

- **`up` warns a name doesn't resolve to the host**: fix the DNS record; certificates can't be
  issued until it points at the VM. `dig +short whoami.example.com` should print the IP.
- **HTTPS fails right after `up`**: the first certificate takes a few seconds to a minute.
  `host.sh ssh sudo lab-nomad alloc logs -job traefik` shows Traefik's ACME messages.
- **Port 80 blocked**: HTTP-01 needs it. Check the provider's firewall.
- **Caravana**: `host.sh ssh journalctl -u caravana -n 50`; its health is
  `curl $CARAVANA_URL/v1/health` through the tunnel.
- **Nomad**: `host.sh ssh sudo lab-nomad status` (the management token is in
  `/var/lib/caravana-host/bootstrap.token`, root only).
- **Webhook tokens never appear in logs**: Traefik's access log is off on purpose; keep it off,
  or configure it to drop query strings.
