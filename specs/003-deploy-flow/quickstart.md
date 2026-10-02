# Quickstart: Deploy Flow

How to prove feature 003 works. Steps 1–2 need no network and no lab; steps 3–5 need the lab.
Endpoints and bodies are in [contracts/openapi.yaml](contracts/openapi.yaml).

## 1. CI checks (no network, no lab)

```bash
go build ./...
go vet ./...
go tool staticcheck ./...
go test -race ./...
gofmt -l .                                         # prints nothing
go mod tidy -diff                                  # prints nothing: no new module
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...
```

Expected: all pass. Lab tests show as skipped. These cover registry resolution against
`registrytest` (token handshake, index vs manifest, 404, 401, 429, 500); rollout following
against `deploytest` (successful, failed, failed with rollback, cancelled by a newer version,
Nomad down mid-watch); service idempotency and newer-wins; and API bodies checked against
the contract.

## 2. Focused runs while developing

```bash
go test ./internal/registry -run TestResolve -v
go test ./internal/deploy   -run TestWatch -v
go test ./internal/service  -run 'TestDeploy/(same payload twice|newer wins)' -race -v
go test ./internal/api      -run TestContract -v
```

## 3. Deploy whoami in the lab (US1, US2)

```bash
deploy/dev/lab.sh up
limactl shell --workdir / caravana-lab sudo lab-nomad job stop -purge whoami   # drop the hand-deployed job, if any
eval "$(deploy/dev/lab.sh env)"                                     # NOMAD_ADDR, NOMAD_TOKEN
go run ./cmd/caravana                                               # in another terminal
```

```bash
cat > /tmp/whoami.json <<'EOF'
{
  "source": {"kind": "image"},
  "services": [{
    "name": "web",
    "image": "traefik/whoami:latest",
    "ports": [{"name": "http", "containerPort": 80}],
    "domains": [{"host": "whoami.localhost", "port": "http"}],
    "health": {"path": "/", "interval": "10s", "timeout": "2s"}
  }]
}
EOF
curl -si -X POST localhost:7070/v1/apps/whoami/deployments \
  -H 'Content-Type: application/json; charset=UTF-8' --data @/tmp/whoami.json
```

Expected: `202 Accepted` within 10 s, `Location: /v1/apps/whoami/deployments/<id>`, a body with
`status: running`, a `jobVersion` and `digests.web` = `traefik/whoami@sha256:…`.

```bash
curl -s localhost:7070/v1/apps/whoami/deployments/<id>     # repeat until status is final
curl -sk https://whoami.localhost:8443                      # answers once healthy
limactl shell --workdir / caravana-lab sudo lab-nomad job inspect whoami | grep image   # a digest, no tag
```

Send the same body again: a new deployment ID, `jobVersion` one higher, and
`GET /v1/apps/whoami` shows the same app with the same `updatedAt`.

## 4. A broken deploy reverts (US3)

Change `containerPort` to `81` in `/tmp/whoami.json` and deploy it. Nomad starts the new
version as canaries beside the running one, and Traefik doesn't route to them (research R15).
The canaries turn unhealthy at the healthy deadline (3 minutes), but Nomad keeps rescheduling
them and only fails the deployment at the progress deadline (10 minutes). All that time
`curl -sk https://whoami.localhost:8443` keeps answering from the previous version. The deployment ends `reverted`, and its `error` says Nomad is
rolling back to the previous job version.

## 5. Failures are loud (US4)

| Try | Expected |
| --- | --- |
| `"image": "traefik/whoami:does-not-exist"` | 422 `image_not_found`, `field: services[0].image`, `Location` set; the record is `failed`; Nomad's job version is unchanged |
| `"image": "quay.io/x/y:1"` | 400 `invalid_field`, `field: services[0].image`, no record |
| `"interval": 10` | 400 `invalid_field`, `field: services[0].health.interval` |
| body `"id": "other"` on `/v1/apps/whoami/...` | 400 `invalid_field`, `field: id` |
| `NOMAD_ADDR=http://127.0.0.1:1` | 502 `nomad_unavailable`, the record is `failed` |
| `deploy/dev/lab.sh check`, then deploy `whoami` | 409 `job_conflict` naming the job; the hand-deployed job is untouched |
| `GET /v1/apps/whoami/deployments?limit=101` | 400 `invalid_field`, `field: limit` |

## 6. Opt-in lab test

```bash
eval "$(deploy/dev/lab.sh env)"
CARAVANA_LAB=1 go test ./cmd/caravana -run TestLab -v -timeout 20m
```

Takes about 12 minutes. Runs steps 3–5 automatically (whoami healthy over HTTPS, same payload twice, wrong port ends
`reverted`, missing tag fails before registering). It also confirms the Nomad description
strings `internal/deploy` matches on (research R8).
