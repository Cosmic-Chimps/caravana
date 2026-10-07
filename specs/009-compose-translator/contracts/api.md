# Contract: API (`api/openapi.yaml` 0.6.0 → 0.7.0)

## `POST /v1/apps/{id}/compose` (`deployCompose`)

Deploys the app from a Compose file and an overlay. It is an upsert by app ID, like `deployApp`.
202 with a `Deployment` and `Location`. Warnings in the deployment carry Compose paths.

```json
{
  "compose": "services:\n  web:\n    image: traefik/whoami\n    ports: [\"80\"]\n  worker:\n    image: busybox:1.37\n    command: sleep 3600\n",
  "name": "Blog",
  "services": {
    "web": {
      "domains": [{"host": "blog.example.com"}],
      "health": {"path": "/"},
      "secrets": [{"name": "API_KEY", "value": "…"}]
    }
  }
}
```

## `POST /v1/apps/{id}/compose/preview` (`previewCompose`)

The same body. 200 with `{"app": App, "warnings": [Warning]}`. Saves and deploys nothing. Secret
values in the answer are never echoed: secrets list names only.

## Errors

| Case | Status | `code` | `field` |
| --- | --- | --- | --- |
| Refused attribute | 400 | `invalid_field` | `services.web.build` |
| Bad YAML / schema | 400 | `invalid_field` | `compose` (the message has the parser's path and line) |
| Variable without a value | 400 | `invalid_field` | `compose` (the message names the variable) |
| Overlay for an unknown service | 400 | `invalid_field` | `services.api` (the overlay key) |
| Overlay field | 400 | `invalid_field` | `services.web.domains[0].port` |
| Model rules after translation | 400 | `invalid_field` | the Compose path of the service, e.g. `services.db.deploy.replicas` |

Overlay fields are reported under `services.<name>...` because that is where they sit in the
request body.

## Schema additions

- `App.source.kind` enum: `[image, compose]`.
- `Service.volumes`: `[{name, path, readOnly?}]`.
- `ComposeRequest`, `ComposeOverlay`, `ComposePreview`.
