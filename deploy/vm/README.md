# The gate host: a public Caravana on a VM

`deploy/dev/` is the local lab. This folder drives a real, public VM from your Mac over SSH, to
prove the gate with Let's Encrypt certificates and ports the internet can reach. It installs with
the product installer (`deploy/install/install.sh`, see `deploy/install/README.md`), from a
release built on your Mac. So the gate proves the same install users run.

## What you need

- **A VM**: Ubuntu 24.04 LTS, arm64 or amd64, 2 GB RAM, reachable over SSH as root or a user with
  passwordless sudo, with your SSH key.
- **Two DNS records** pointing at the VM's IP (or one wildcard):
  - the console, e.g. `caravana.example.com`
  - the app the gate deploys, e.g. `whoami.example.com`
- **The provider's firewall**, if it has one, allowing TCP 22, 80 and 443.
- On your Mac: Go, `ssh`, `scp`, `python3`.

```bash
cp deploy/vm/host.env.example deploy/vm/host.env    # gitignored; fill it in
```

## Commands

```bash
deploy/vm/host.sh up         # build a dev release, install or upgrade it (asks the admin's password the first time)
deploy/vm/host.sh prove      # the gate, ~15 minutes; PASS/FAIL per step, ends GATE PASSED/FAILED
deploy/vm/host.sh admin status | reset-password | create-token --name ci | revoke-token --name ci
deploy/vm/host.sh tunnel     # private access: Nomad :14646, Traefik dashboard :18081
eval "$(deploy/vm/host.sh env)"   # NOMAD_ADDR, NOMAD_TOKEN for the tunnel, CARAVANA_URL
deploy/vm/host.sh uninstall  # remove Caravana; Nomad, Traefik and the apps stay (--purge: data too)
deploy/vm/host.sh ssh        # a shell on the host; `host.sh ssh sudo caravana-nomad status` runs Nomad's CLI
```

`up` builds `0.0.0-dev.<time>` with `tools/release.sh`, copies it to the host, and runs
`install.sh --from` with the settings from `host.env`. Each `up` is a newer version, so it
exercises the installer's upgrade path, rollback included. The console is public at
`https://$CARAVANA_DOMAIN_CONSOLE`. Sign in there.

`prove` needs the admin. It makes its own API token `gate-<time>` and revokes it when the run
ends. The gate calls the API and the webhook through the public console domain, and Nomad
through the tunnel.

## Let's Encrypt: staging vs production

`CARAVANA_ACME_STAGING=1` uses Let's Encrypt's staging CA: no rate limits, but certificates
browsers don't trust. Use it while iterating; `prove` then reports `https-trusted` as a staging
certificate and the gate fails on purpose. Set it to `0` and run `up` again to prove the gate
with real certificates. Switching either way makes the installer stop Traefik and delete its
stored certificates (`changed:` lists `certificates`).

## Troubleshooting

- **The installer refuses**: it names the check. A domain that doesn't resolve to the host yet is
  the usual one: `dig +short caravana.example.com` should print the IP.
- **HTTPS fails right after `up`**: the first certificate takes a few seconds to a minute.
  `host.sh ssh sudo caravana-nomad alloc logs -job traefik` shows Traefik's ACME messages.
- **Port 80 blocked**: HTTP-01 needs it. Check the provider's firewall.
- **Caravana**: `host.sh ssh journalctl -u caravana -n 50`, and `host.sh ssh sudo caravana version`.
- **Webhook tokens never appear in logs**: Traefik's access log is off on purpose. Keep it off,
  or configure it to drop query strings.
