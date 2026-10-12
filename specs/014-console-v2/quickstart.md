# Quickstart: validate Console v2

## Prerequisites

```bash
deploy/dev/lab.sh up
eval "$(deploy/dev/lab.sh env)"
go run ./cmd/caravana                                   # 127.0.0.1:7070
go run ./cmd/caravana admin init --email you@example.com   # once
TOKEN="$(go run ./cmd/caravana admin create-token --name qs)"
open http://127.0.0.1:7070/
```

## 1. Edit on Configure (US1, SC-001, SC-005)

1. New app › Image: `whoami`, image `traefik/whoami:latest`, port 80, domain
   `whoami.localhost`, health `/`. Wait for healthy.
2. Configure: add `GREETING=hi` to Environment, set Memory to 128 and ceiling to 256, add
   `whoami2.localhost` to Domains, save. Expected: you land on the new deployment; it becomes
   healthy; `curl -sk https://whoami2.localhost:8443/ | grep GREETING` is empty (whoami doesn't
   print env) but `GET /v1/apps/whoami` shows the env var, both domains and the memory.
3. Stale save: open Configure in two tabs, save in one, then save in the other. Expected: the
   second answers "This app changed since you opened the form" and deploys nothing.
4. Same through the API:
   ```bash
   REV=$(curl -s -H "Authorization: Bearer $TOKEN" localhost:7070/v1/apps/whoami | jq -r .revision)
   curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $TOKEN" \
     -H 'Content-Type: application/json' -H 'If-Match: "0000000000000000"' \
     -d "$(curl -s -H "Authorization: Bearer $TOKEN" localhost:7070/v1/apps/whoami | jq .app)" \
     localhost:7070/v1/apps/whoami/deployments          # 412
   ```
5. Invalid input: set Memory 512 and ceiling 256, save. Expected: 422, the message under the
   ceiling field, every typed value still there, no new deployment.

## 2. Compose on New app (US2, SC-002)

1. New app › Compose file: name `hollo`, paste `deploy/examples/hollo/compose.yaml`, Preview.
   Expected: two services, warnings naming Compose paths, overlay fields for `hollo` (domains,
   health) and the secret names the file asks for.
2. Fill the domain `hollo.localhost`, health `/`, the secret values, Create. Expected: the
   deployment page; it becomes healthy.
3. Compare with the API: `previewCompose` with the same file and `overlay.example.json` gives the
   same app (`GET /v1/apps/hollo` `.app`, ignoring secret values).
4. Name taken: New app › Compose with name `hollo` again. Expected: "taken" at the name field,
   nothing saved.

## 3. Deploy from Compose (US3)

1. Configure › Deploy from Compose on `hollo`; paste the file with a newer image tag for one
   service; Preview. Expected: the overlay is prefilled (domain, health, secret names with
   "empty keeps the current value"); no services added or removed.
2. Deploy. Expected: healthy; the other service's copy kept its start time (it wasn't
   replaced); secrets still work.

## 4. Usage per copy (US4, SC-004)

1. Open `whoami` › Running. Expected: CPU "N MHz of 100 MHz reserved", Memory "N MB of 256 MB
   ceiling, 128 reserved" per copy.
2. `curl -s -H "Authorization: Bearer $TOKEN" 'localhost:7070/v1/apps/whoami/copies?usage=true' | jq '.copies[].usage'`
   gives the same numbers.
3. Near the ceiling: deploy a service with `memoryMaxMB: 40` whose process holds ~38 MB (e.g.
   `busybox` running `dd if=/dev/zero bs=1M count=38 | sleep 3600`-style holder from the lab
   tests). Expected: warning mark and "close to its ceiling" within 10 s.

## 5. Needs you (US5, SC-003)

1. Deploy `whoami` with health path `/does-not-exist` from Configure. Expected: within 30 s the
   Apps home shows "whoami's deploy vN was reverted…" with a link to that deployment.
2. Fix the health path and save. Expected: the item disappears within 30 s.
3. Run a service that exits on start (busybox `false`). Expected: a "keeps restarting" item
   linking to its logs.
4. `curl -s -H "Authorization: Bearer $TOKEN" localhost:7070/v1/attention | jq` shows the same
   items.

## Automated

```bash
go test ./...
CARAVANA_LAB=1 go test ./cmd/caravana -run TestConsoleLab -v
CARAVANA_LAB=1 go test ./cmd/caravana -run TestLab -v -timeout 25m
tools/ui-css.sh check
```
