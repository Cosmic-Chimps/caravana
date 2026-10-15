# Quickstart: validate "Keep and edit the Compose file"

Shapes and messages are in [contracts/api.md](./contracts/api.md) and
[contracts/console.md](./contracts/console.md); this guide only runs them.

## Prerequisites

```bash
deploy/dev/lab.sh up
eval "$(deploy/dev/lab.sh env)"
go run ./cmd/caravana                                      # 127.0.0.1:7070
go run ./cmd/caravana admin init --email you@example.com   # once
TOKEN="$(go run ./cmd/caravana admin create-token --name qs)"
open http://127.0.0.1:7070/
```

A test file, `$CLAUDE_JOB_DIR/tmp/kuma.compose.yaml` or anywhere outside the repo:

```yaml
# Uptime check for the lab
services:
  web:
    image: traefik/whoami:v1.10   # pinned on purpose
    ports: ["80"]
```

## 1. The file is kept and edited on Configure (US1, SC-001)

1. New app › Compose file: name `kuma`, paste the file, Preview, domain `kuma.localhost`,
   health `/`, secret `TOKEN` with a value `s3cret-sentinel`, Deploy. Wait for healthy.
2. Configure. Expected: the Compose file section shows the file with both comments; overlay
   shows `kuma.localhost`, health `/`, secret `TOKEN` marked as set with an empty input; the
   Definition below is read-only under "This app is edited through its Compose file."
3. Change the tag to `v1.11`, Preview (nothing saved: the history has no new entry), Deploy.
   Expected: a new deployment, healthy; `GET /v1/apps/kuma` shows `traefik/whoami:v1.11`;
   Configure shows the edited file. Time from opening Configure to Deploy is under a minute.
4. Stale: open Configure in two tabs; in one, edit only a comment and deploy; in the other, deploy.
   Expected: the second is refused as stale, nothing deployed, its text still in the editor.

## 2. The API sees the same (US4, FR-008)

```bash
H="Authorization: Bearer $TOKEN"
curl -si -H "$H" localhost:7070/v1/apps/kuma/compose | sed -n '1,/^\r$/p' | grep -i etag
curl -s  -H "$H" localhost:7070/v1/apps/kuma/compose | jq .                  # compose, name, services
curl -s  -H "$H" localhost:7070/v1/apps/kuma | jq .composeKept               # true
curl -s  -H "$H" localhost:7070/v1/apps/kuma/deployments | jq '.[].composeKept'
```

Round trip: send the answer back with `If-Match` and get 202; `TOKEN` keeps its value (the
running copy still has it).

```bash
REV=$(curl -s -H "$H" localhost:7070/v1/apps/kuma | jq -r .revision)
curl -s -H "$H" localhost:7070/v1/apps/kuma/compose \
  | curl -s -o /dev/null -w '%{http_code}\n' -H "$H" -H 'Content-Type: application/json' \
      -H "If-Match: \"$REV\"" -d @- localhost:7070/v1/apps/kuma/compose          # 202
```

## 3. Rollback brings the file back (US3, SC-002)

1. Deployments: on the first deployment (tag `v1.10`), Redeploy this version. Wait for healthy.
2. Configure. Expected: the file says `v1.10`, with its own comments.
3. History: each Compose deployment offers "Download Compose file"; the download of the first is
   byte-identical to the file pasted in step 1.1 (`cmp`).

## 4. Everything else keeps the file (US4, SC-004)

1. Configure › Secrets: set `OTHER=x`. Expected: `GET /v1/apps/kuma/compose` has the same
   `compose` text and `services.web.secrets` lists `OTHER` and `TOKEN`, names only.
2. Deployments › Deploy (redeploy), and the webhook (create a token on Configure, `curl -X POST`
   it). Expected: the `compose` text is unchanged after each.
3. Deploy a definition:

   ```bash
   curl -s -H "$H" localhost:7070/v1/apps/kuma | jq .app \
     | curl -s -H "$H" -H 'Content-Type: application/json' -d @- \
         localhost:7070/v1/apps/kuma/deployments | jq .warnings
   ```

   Expected: the warning "This app's Compose file was dropped: it was deployed from a definition,
   not a file."; `getCompose` answers 404 `compose_not_kept`.

## 5. An app whose file wasn't kept (US2, SC-005)

After 4.3 `kuma` is in the same state as a Compose app made before this feature (source
`compose`, no file).

1. Configure. Expected: "This app's Compose file wasn't kept. Paste it once to edit it here from
   now on.", an empty editor, overlay prefilled (domain, health, secret names), and the per-field
   form editable.
2. Paste the file, Preview, Deploy. Expected: Configure now shows the file and the read-only
   Definition.

On the maintainer's host: do 5.1–5.2 for Uptime Kuma (SC-005).

## 6. No secret value anywhere (SC-003)

```bash
for p in /v1/apps/kuma/compose /v1/apps/kuma/deployments /v1/apps/kuma; do
  curl -s -H "$H" "localhost:7070$p" | grep -c s3cret-sentinel; done          # 0 each
```

Also the Configure page source, the Deploy from Compose page and every downloaded file: no
`s3cret-sentinel`.

## 7. Automated checks

```bash
go test ./internal/store ./internal/compose ./internal/service ./internal/api ./internal/ui
CARAVANA_LAB=1 go test ./cmd/caravana -run TestConsoleLab -v     # includes compose-file-edit
```

Plus the full list in CLAUDE.md (vet, staticcheck, gofmt, race, cross-compile, ui-css check).
