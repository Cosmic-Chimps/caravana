# Data Model: Deploy Flow

What 003 adds or changes. The app model (feature 001) and the store's tables (feature 002) are
unchanged: **no migration, no translator golden-file change**.

## Configuration (additions)

| Setting | Env var | Flag | Default | Rule |
| --- | --- | --- | --- | --- |
| Nomad address | `NOMAD_ADDR` | `-nomad-addr` | `http://127.0.0.1:4646` | `http` or `https` URL with a host |
| Nomad token | `NOMAD_TOKEN` | — (env only) | empty (ACLs off) | held in `config.Secret`; prints as `[redacted]` |

## Image reference (`internal/registry`)

| Field | Example (`traefik/whoami:latest`) | Example (`ghcr.io/acme/shop@sha256:…`) |
| --- | --- | --- |
| `Name` (as written, no tag or digest) | `traefik/whoami` | `ghcr.io/acme/shop` |
| `Registry` (canonical host) | `docker.io` | `ghcr.io` |
| `Repository` (path on that registry) | `traefik/whoami` | `acme/shop` |
| `Tag` | `latest` | `` (dropped when a digest is present) |
| `Digest` | `` | `sha256:…` |
| `IgnoredTag` | `` | the tag, when both a tag and a digest were given (becomes a warning) |

- `nginx` → Registry `docker.io`, Repository `library/nginx`, Name `nginx`, Tag `latest`.
- **Pinned image** = `Name + "@" + digest`, e.g. `traefik/whoami@sha256:<64 hex>`. It goes into
  `app.Deployment.Digests[service]` and the job; it always matches `app`'s digest pattern.
- Parse errors (become 400 `invalid_field` on `services[i].image`): empty path part, uppercase
  in the path, bad tag, digest not `sha256:<64 lowercase hex>`, unsupported registry.

## Deployment lifecycle (as the deploy flow drives it)

Statuses and allowed changes are feature 002's (`internal/store`); 003 adds no status.

```text
                validated, app saved, record created
                              │
                           pending ──────────────► failed      registry error, Nomad unreachable,
                              │  │                             Nomad rejected, job_conflict,
                              │  └───────────────► superseded  a newer request came first (R9)
                 registered   │
                              ▼
                           running  (job version recorded in the same update)
              ┌───────────────┼───────────────┬──────────────┐
              ▼               ▼               ▼              ▼
           healthy          failed         reverted      superseded
        (successful)   (failed, no       (failed, Nomad  (Nomad cancelled it:
                        rollback)        rolled back)    newer version)
```

| Change | Trigger | Record update |
| --- | --- | --- |
| → `pending` | deploy validated, app saved | `CreateDeployment`: trigger, empty digests |
| `pending` → `failed` | resolution or registration failed | `error` = the client's error message |
| `pending` → `superseded` | newer request seen before registering | `error` = "replaced by deployment <id> before it was registered" |
| `pending` → `running` | Nomad accepted the job | `digests`, `jobVersion` (absent if not read back yet), `warnings` |
| `running` → `running` | version learned after registration (research R6) | `jobVersion` |
| `running` → `healthy` | Nomad deployment `successful` | — |
| `running` → `failed` | Nomad deployment `failed` without rollback, or `cancelled` for another reason | `error` = Nomad's description |
| `running` → `reverted` | Nomad deployment `failed` and rolling back | `error` = Nomad's description |
| `running` → `superseded` | Nomad deployment `cancelled` (newer version), or a newer version with no deployment for ours | `error` = Nomad's description |

On startup (R10): `running` → watched again; `pending` → `running` if Nomad has a job version
carrying this deployment ID, else `failed` ("Caravana stopped before registering this
deployment").

## Store changes (`internal/store`, no migration)

| Change | Why |
| --- | --- |
| `DeploymentUpdate.Digests map[string]string` (nil keeps) | Digests are only known after resolution, which happens after the record exists |
| `ListDeployments(ctx, appID, limit int)` | `GET …/deployments?limit=` (default 20, max 100); `limit` ≤ 0 means "all" for internal callers |
| `ListUnfinishedDeployments(ctx) ([]DeploymentRecord, error)` | resume after restart (`status IN ('pending','running')`, oldest first) |

## API resources (JSON, camelCase; schemas in [contracts/openapi.yaml](contracts/openapi.yaml))

- **App**: feature 001's model as sent, with `health.interval` / `health.timeout` as duration
  strings. Request body of `POST /v1/apps/{id}/deployments`; `id` may be omitted (taken from
  the path) and must equal the path's if present.
- **AppRecord** (`GET /v1/apps/{id}`): `{"app": App, "createdAt", "updatedAt"}`. Wrapped, so a
  client can send `app` back unchanged as a deploy body.
- **Deployment** (deploy response, list items, `GET …/deployments/{deploymentId}`):

  | Field | Type | Notes |
  | --- | --- | --- |
  | `id` | string | 26 lowercase base32 characters (`crypto/rand.Text`, lowercased) |
  | `appId` | string | |
  | `trigger` | `manual` \| `webhook` | the API always records `manual` |
  | `status` | `pending` \| `running` \| `healthy` \| `failed` \| `reverted` \| `superseded` | |
  | `jobVersion` | integer ≥ 0 | omitted until Nomad accepts the job |
  | `digests` | object, service name → pinned image | `{}` until resolved |
  | `warnings` | array of `{field, message}` | always present, may be empty |
  | `error` | string | omitted when empty |
  | `createdAt`, `updatedAt` | RFC 3339 date-time | |

- **DeploymentList**: `{"deployments": [Deployment, …]}`, newest first.

## Errors (additions to feature 002's codes)

| Code | Status | `field` | `Location` header |
| --- | --- | --- | --- |
| `image_not_found` | 422 | `services[i].image` | yes |
| `job_conflict` | 409 | — | yes |
| `registry_unavailable` | 502 | `services[i].image` | yes |
| `nomad_unavailable` | 502 | — | yes |
| `nomad_rejected` | 502 | — | yes |

Validation errors (400 `invalid_field`, `unknown_field`, …) and 404s never carry `Location`:
nothing was recorded.
