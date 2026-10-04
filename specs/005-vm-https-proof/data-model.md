# Data Model: Real HTTPS and Auto-Revert on a Cheap VM

No change to Caravana's data (apps, deployments, webhook tokens). This feature's "data" is the
host configuration and the layout of the host.

## Host configuration (`deploy/vm/host.env`, gitignored)

| Variable | Example | Rule |
| --- | --- | --- |
| `CARAVANA_HOST` | `root@203.0.113.10` | SSH target; root or a sudo user with key login |
| `CARAVANA_DOMAIN_APP` | `whoami.example.com` | the proof's app host; must resolve to the host |
| `CARAVANA_DOMAIN_WEBHOOK` | `hooks.example.com` | the public webhook host; must resolve to the host |
| `CARAVANA_ACME_EMAIL` | `ops@example.com` | Let's Encrypt account email |
| `CARAVANA_ACME_STAGING` | `0` | `1` uses Let's Encrypt's staging CA (test runs) |

Same values → `host.sh up` changes nothing (research R8).

## Host layout (after `up`)

| Path / thing | Owner, mode | What |
| --- | --- | --- |
| `/etc/nomad.d/nomad.hcl` | root 0644 | `bind_addr 127.0.0.1`, `network_interface "caravana0"`, host volume `traefik-acme`, ACLs on, `cpu_total_compute` when needed |
| `/var/lib/caravana-host/` | root 0700 | Nomad bootstrap token, Traefik's `acme.json` (host volume) |
| `caravana0` (dummy interface) | systemd-networkd | `10.99.0.1/32`; every allocation port binds here (R4) |
| `ufw` | — | default deny incoming; allow 22, 80, 443 |
| Nomad job `traefik` | — | ports 80/443 public, dashboard 127.0.0.1:8081, `letsencrypt` resolver, file-provider route for the webhook host, access log off |
| ACL policy `traefik-read`, `caravana` | — | Traefik reads services; Caravana submits and reads jobs |
| `caravana` user | — | runs the service |
| `/usr/local/bin/caravana` | root 0755 | the binary `up` built |
| `/etc/caravana/caravana.env` | root 0600 | `CARAVANA_LISTEN=127.0.0.1:7070`, `CARAVANA_WEBHOOK_LISTEN=127.0.0.1:7071`, `CARAVANA_DATA_DIR=/var/lib/caravana`, `CARAVANA_CERT_RESOLVER=letsencrypt`, `NOMAD_ADDR=http://127.0.0.1:4646`, `NOMAD_TOKEN=<caravana policy token>` |
| `/var/lib/caravana/` | caravana 0700 | Caravana's SQLite store |
| `caravana.service` | systemd | `After=nomad.service`, `Restart=on-failure`, hardened |

## Ports

| Port | Bound to | Public |
| --- | --- | --- |
| 22 | all | yes (SSH) |
| 80 | all (Traefik `web`) | yes: HTTP-01 challenges and redirects to HTTPS |
| 443 | all (Traefik `websecure`) | yes: apps and the webhook host |
| 4646–4648 | 127.0.0.1 (Nomad) | no |
| 7070 | 127.0.0.1 (Caravana API) | no |
| 7071 | 127.0.0.1 (webhook listener; reached by Traefik's static route) | only via 443 on the webhook host, `/v1/webhooks/` only |
| 8081 | 127.0.0.1 (Traefik dashboard) | no |
| 20000–32000 | 10.99.0.1 (allocation ports) | no |

## Gate target (`cmd/caravana/gate_test.go`)

| Field | Lab | VM |
| --- | --- | --- |
| API URL | in-process `http://127.0.0.1:<free>` | `http://127.0.0.1:17070` (tunnel) |
| Webhook URL | in-process webhook listener | `https://<webhook-host>` |
| App host / URL | `whoami.localhost` / `https://whoami.localhost:8443` | `whoami.<domain>` / `https://whoami.<domain>` |
| App client | skips verification, dials `127.0.0.1:8443` | system roots, normal DNS |
| Nomad | lab `NOMAD_ADDR`/`NOMAD_TOKEN` | tunnel `NOMAD_ADDR`/`NOMAD_TOKEN` |
| Public checks (ports, cert trust, redirect) | skipped | run, against `GATE_PUBLIC_IP` |
