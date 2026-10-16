# Quickstart: validate private registries

Shapes and copy: [contracts/api.md](./contracts/api.md), [contracts/console.md](./contracts/console.md).

## Automated

```bash
go test ./internal/registry ./internal/nomadjob ./internal/deploy ./internal/store ./internal/service ./internal/api ./internal/ui
eval "$(deploy/dev/lab.sh env)"
CARAVANA_LAB=1 go test ./cmd/caravana -run TestLab -v -timeout 25m   # gate step private-registry
```

The gate step builds its own private registry on the node (research R7), so it needs nothing
outside the lab.

## By hand with GHCR (US1, SC-001)

1. Push a private image, for example `ghcr.io/<you>/umbral:latest`. Create a GitHub token with
   `read:packages` only.
2. Deploy it without credentials. Expected: the deploy fails, saying the image is missing or
   private and to add credentials for `ghcr.io` in Settings › Registries.
3. Settings › Registries: add `ghcr.io`, your user name, and the token. Press Test. Expected:
   "ghcr.io accepted the credentials."
4. Deploy again. Expected: healthy, with the image pinned by digest.
5. Check that no secret leaks:

   ```bash
   curl -s -H "Authorization: Bearer $TOKEN" localhost:7070/v1/registries | grep -c <token>   # 0
   nomad job inspect -json <app> | grep -c <token>                                            # 0
   ```
6. Edit with a wrong token, then Test. Expected: refused with the registry's message. Deploy.
   Expected: 422 `registry_auth_failed`, naming `ghcr.io`.
7. Fix it, then trigger the webhook. Expected: the redeploy works.
8. Remove the credentials. Expected: the app keeps running.

## Other registries (US3)

- `quay.io/prometheus/busybox:latest`, which is public, deploys with no credentials.
- A self-hosted `registry.example.com:5000` with credentials deploys, and the pinned image keeps
  `:5000`.
