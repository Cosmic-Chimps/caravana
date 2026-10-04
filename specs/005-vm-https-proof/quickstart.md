# Quickstart: Real HTTPS and Auto-Revert on a Cheap VM

Step 1 needs nothing but the repo. Steps 2–6 need a VM and a domain (research R13).

## 1. Offline checks

```bash
gofmt -l . && go vet ./... && go tool staticcheck ./... && go test -race ./...
shellcheck deploy/dev/*.sh deploy/vm/*.sh          # brew install shellcheck
deploy/dev/lab.sh up && eval "$(deploy/dev/lab.sh env)" \
  && CARAVANA_LAB=1 go test ./cmd/caravana -run TestLab -v -timeout 20m   # the refactored lab gate still passes
```

## 2. Prepare (by hand, once)

1. Create an Ubuntu 24.04 VM (e.g. Hetzner CAX11, arm64, 2 GB) with your SSH key; note its IP.
2. DNS: `whoami.<domain>` and `hooks.<domain>` (or `*.<domain>`) → the IP. Wait until
   `dig +short whoami.<domain>` prints it.
3. If the provider has a firewall, allow TCP 22, 80, 443.
4. `cp deploy/vm/host.env.example deploy/vm/host.env` and fill it in. Use
   `CARAVANA_ACME_STAGING=1` while iterating, `0` for the gate.

## 3. Set up the host

```bash
deploy/vm/host.sh up        # first run: ~5–10 min
deploy/vm/host.sh up        # again: "changed:" is empty
```

Expected: the summary lists the URLs; `curl -sI http://whoami.<domain>` is not yet routed (no
app), but `curl -s https://hooks.<domain>/v1/health` answers 404 and
`curl -s -X POST https://hooks.<domain>/v1/webhooks/x` answers 401 with a trusted certificate.

## 4. Operate it privately

```bash
deploy/vm/host.sh tunnel &            # Ctrl-C to close
eval "$(deploy/vm/host.sh env)"
curl -s "$CARAVANA_URL/v1/health"     # {"status":"ok"}
open http://127.0.0.1:14646/ui        # Nomad UI (token: $NOMAD_TOKEN)
open http://127.0.0.1:18081/dashboard/
```

## 5. Prove the Phase 1 gate

```bash
deploy/vm/host.sh prove    # ~12 min (the broken deploy waits for Nomad's 10-min progress deadline)
```

Expected: a `PASS` line per step (see [contracts/host-sh.md](contracts/host-sh.md)) and
`GATE PASSED`; with staging certificates, `https-trusted` says so and the gate isn't counted.

## 6. Reboot and port checks

```bash
deploy/vm/host.sh ssh sudo reboot
sleep 180 && curl -s https://whoami.<domain> | head -3     # answers again, trusted certificate
nmap -Pn -p- <ip>                                          # only 22, 80, 443 open (SC-004)
```
