# Contract: API changes (`api/openapi.yaml` 0.5.0 → 0.6.0)

No new endpoint. Every endpoint that takes or returns an `App` gains `secrets` on each service:
`POST /v1/apps` (`createApp`), `POST /v1/apps/{id}/deployments` (`deployApp`),
`GET /v1/apps/{id}` and the app inside `GET /v1/apps`.

## `Service.secrets`

```yaml
secrets:
  type: array
  description: |
    The service's secrets, as environment variables. Each deploy replaces the whole list: a
    name you leave out is removed. Send `value` to set a secret; leave it out to keep the value
    it has now. Values are write-only: responses list names only.
  items:
    $ref: "#/components/schemas/Secret"

Secret:
  type: object
  additionalProperties: false
  required: [name]
  properties:
    name:
      type: string
      pattern: "^[A-Za-z_][A-Za-z0-9_]*$"
    value:
      type: string
      writeOnly: true
      description: UTF-8 text without NUL. Leave it out to keep the current value.
```

## Examples

Set two, on the first deploy:

```json
{"id": "blog", "source": {"kind": "image"}, "services": [{"name": "web", "image": "traefik/whoami",
  "secrets": [{"name": "DATABASE_URL", "value": "postgres://…"}, {"name": "API_KEY", "value": "k1"}]}]}
```

Rotate `API_KEY`, keep `DATABASE_URL`: `"secrets": [{"name": "DATABASE_URL"}, {"name": "API_KEY", "value": "k2"}]`.

Remove `API_KEY`: `"secrets": [{"name": "DATABASE_URL"}]`.

`GET /v1/apps/blog` then answers `"secrets": [{"name": "DATABASE_URL"}]`.

## Errors

| Case | Status | `code` | `field` |
| --- | --- | --- | --- |
| Bad name, duplicate, NUL or invalid UTF-8 value | 400 | `invalid_field` | `services[i].secrets[j].name` / `.value` |
| Keep with no stored value | 400 | `invalid_field` | `services[i].secrets[j].value` |
| Over 60 KiB in total | 400 | `invalid_field` | `services[i].secrets` |
| Store unreachable or refusing the token | 502 | `secret_store_unavailable` | none |

`secret_store_unavailable` joins the list of current codes in `Error.code`.
