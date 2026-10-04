# Local Nomad lab

A Linux VM on your Mac with Docker, single-node Nomad 2.0.7 (ACLs on) and Traefik v3. Nomad
needs Linux for bridge networking, so it can't run directly on macOS.

Needs: `brew install lima`.

```bash
deploy/dev/lab.sh up        # first run creates the VM (a few minutes), later runs just start it
deploy/dev/lab.sh check     # deploys whoami.json by hand and calls https://whoami.localhost:8443
deploy/dev/lab.sh check internal/nomadjob/testdata/whoami.golden.json   # same, with the translator's output
eval "$(deploy/dev/lab.sh env)"   # NOMAD_ADDR + NOMAD_TOKEN for tools on the Mac
deploy/dev/lab.sh stop | delete | shell
```

## Caravana's own admin and token

The API refuses requests without credentials (feature 006), so after `lab.sh up`, with Caravana
running on the Mac (`go run ./cmd/caravana`), create the admin once and a token for `curl`:

```bash
go run ./cmd/caravana admin init --email you@example.com     # asks for a password (12+ characters)
TOKEN="$(go run ./cmd/caravana admin create-token --name local)"
curl -s -H "Authorization: Bearer $TOKEN" localhost:7070/v1/tokens
```

The CLI works while the server runs. In a browser, sign in at <http://127.0.0.1:7070/login>.
Caravana's data (`./data` by default) is separate from the lab's, so `lab.sh delete` doesn't
touch the admin.

| Where | What |
| --- | --- |
| http://127.0.0.1:4646/ui | Nomad UI; sign in with `NOMAD_TOKEN` from `deploy/dev/.data/env` |
| http://127.0.0.1:8088/dashboard/ | Traefik dashboard (8081 inside the VM) |
| https://&lt;name&gt;.localhost:8443 | Apps routed by Traefik (self-signed cert, so `curl -k`) |

Ports 80/443 inside the VM appear as 8080/8443 on the Mac, because macOS only lets root bind
ports below 1024 on `127.0.0.1`.

## Files

- `lab.yaml`: the Lima VM (2 CPUs, 2 GiB, Ubuntu LTS).
- `setup.sh`: installs Docker, CNI plugins and Nomad, and bootstraps ACLs. It runs on every boot
  and is safe to repeat. It's a prototype of the one-command installer (plan item 012).
- `nomad.hcl`: Nomad agent config. It's copied in when the VM is created, so after changing it
  run `lab.sh delete` then `lab.sh up`.
- `traefik.nomad.hcl` and `traefik-policy.hcl`: Traefik as a Nomad system job. It reads services
  with a workload identity token limited by the policy.
- `whoami.json`: a hand-written job in Nomad API JSON, the shape Caravana's translator must
  produce. Feature 001 uses it as a golden file.

The Nomad management token never leaves the VM except in `deploy/dev/.data/env`, which is
gitignored and readable only by you.
