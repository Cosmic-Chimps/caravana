# Quickstart: Webhook Redeploy

How to prove feature 004 works. Step 1 needs no network and no lab; steps 2–4 use the lab.
Endpoints and bodies are in [contracts/openapi.yaml](contracts/openapi.yaml).

## 1. CI checks (no network, no lab)

```bash
gofmt -l . && go mod tidy -diff && go vet ./... && go tool staticcheck ./...
go test -race ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./... && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...
```

Expected: all pass; `go mod tidy -diff` prints nothing (no new module).

## 2. Run Caravana with the webhook-only listener

```bash
deploy/dev/lab.sh up
eval "$(deploy/dev/lab.sh env)"
go run ./cmd/caravana -webhook-listen 127.0.0.1:7071
```

Deploy whoami through the main API as in feature 003's quickstart §3 (`POST
localhost:7070/v1/apps/whoami/deployments` with `/tmp/whoami.json`) and wait for `healthy`.

## 3. Create a token and call the webhook

```bash
created=$(curl -s -X POST localhost:7070/v1/apps/whoami/webhook)
echo "$created"   # {"token":"cvwh_…","path":"/v1/webhooks/whoami?token=cvwh_…","createdAt":"…"}
token=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])' <<<"$created")

curl -si -X POST "localhost:7071/v1/webhooks/whoami?token=$token"                # URL-only sender
curl -si -X POST localhost:7071/v1/webhooks/whoami -H "Authorization: Bearer $token"
```

Expected: 200 with `"changed": false` and the live deployment, because `latest` hasn't moved;
no new job version in Nomad. (A moved tag gives 202 and a new deployment with trigger
`webhook`; the lab can't move Docker Hub's tag, so the offline tests and §4 cover that.)

| Try | Expected |
| --- | --- |
| no token, a wrong token, `/v1/webhooks/ghost` | 401 `unauthorized`, `WWW-Authenticate: Bearer`, the same body each time |
| `GET localhost:7071/v1/health` | 404: the webhook listener serves nothing else |
| `GET localhost:7070/v1/apps/whoami/webhook` | `{"enabled": true, "createdAt": "…"}`, never the token |
| `POST …/webhook` again, then the old token | old token 401, new one works |
| `DELETE localhost:7070/v1/apps/whoami/webhook`, then any token | 401 |
| the server log | method, path, status, duration; never `cvwh_` |

## 4. Opt-in lab test

```bash
eval "$(deploy/dev/lab.sh env)"
CARAVANA_LAB=1 go test ./cmd/caravana -run TestLab -v -timeout 20m
```

Feature 003's checks plus the webhook phase (FR-016): a webhook call right after the first API
deploy, before that rollout is healthy, redeploys (202), its deployment reaches `healthy` and
the API's ends `superseded`; a second call answers 200 unchanged with no new job version; the
webhook listener answers 404 for health.
