# API contract: secret providers (037, API 0.40.0)

## Secret, extended

```json
{"name": "DATABASE_URL", "from": {"provider": "bella-prod", "path": "DATABASE_URL"}}
```
`Secret` gains `from` (`SecretRef`: `provider` string, required; `path` string, required, 1–512 bytes). `value` and
`from` together: 400 `invalid_field` on `services[i].secrets[j].from`. Reads return `from` as written. The Compose
overlay's secrets are the same schema.

## Errors on any deploy path

(`deployApp`, `deployCompose`, `redeployDeployment`, `promoteApp`, `callWebhook`, `refreshSecrets`, `installTemplate`)

| Case | Status | Code | Field |
|---|---|---|---|
| unknown provider, or not allowed for the app's place | 400 | `invalid_field` | `services[i].secrets[j].from.provider` |
| path not in the provider | 422 | `secret_not_found` | `services[i].secrets[j].from.path` |
| provider refused Caravana (key, device key, environment, envelope) | 422 | `secret_provider_refused` | `services[i].secrets[j].from` |
| provider down / timeout / 5xx | 502 | `secret_provider_unavailable` | `services[i].secrets[j].from` |

Messages name the provider and the path in Caravana's words, never a value or Bella's text.

## Providers

`SecretProvider` (response):
```json
{"name": "bella-prod", "kind": "bella",
 "bella": {"url": "https://api.bella-baxter.io", "project": "shop", "environment": "production"},
 "allowed": ["shop", "shop/production"],
 "usedBy": ["shop--production--web"],
 "lastTest": {"at": "…Z", "ok": true, "message": "12 secrets readable", "count": 12},
 "createdAt": "…Z", "updatedAt": "…Z"}
```
No credential property exists in any schema. `lastTest` is absent until tested.

- `GET /v1/secret-providers` → `listSecretProviders`: 200 `{"providers": [SecretProvider]}`, by name.
- `PUT /v1/secret-providers/{name}` → `putSecretProvider`: body
  `{"kind": "bella", "bella": {"url", "project", "environment"}, "allowed": [...], "apiKey"?: "bax-…", "privateKey"?: "-----BEGIN PRIVATE KEY-----…"}`.
  200 `SecretProvider` (201 when created). Credentials left out keep the stored ones; required the first time;
  an empty string is refused. Field errors (400 `invalid_field`): `name`, `kind`, `bella.url`, `bella.project`,
  `bella.environment`, `allowed[i]` (unknown project or environment), `apiKey`, `privateKey`. Nomad unreachable:
  502 `secret_store_unavailable`.
- `DELETE /v1/secret-providers/{name}` → `deleteSecretProvider`: 204; 404 `secret_provider_not_found`; 409
  `secret_provider_in_use` (message names the apps; `field` absent).
- `POST /v1/secret-providers/{name}/test` → `testSecretProvider`: 200 `SecretProvider` with `lastTest` (a failed
  test is still 200, `ok: false`, the reason in `message`); 404.

## Refresh

`POST /v1/apps/{id}/secrets/refresh` → `refreshSecrets`. Optional `Caravana-Origin-*` headers as on deploys.
- 200 `{"changed": false}`: every resolved value equals the one live; nothing deployed.
- 202 `{"changed": true, "deployment": Deployment}` with `Location` of the deployment; the deployment has
  `secretsRefreshed: true` and `source` = the live deployment's ID.
- 409 `app_not_defined`, `app_stopped`, `no_live_deployment`, `no_secret_references`; the deploy-path errors above.
- Deploy tokens of the app may call it (`scopeRules`, path `{id}`).

`Deployment` gains `secretsRefreshed` (boolean, present and true only for a refresh).

All provider operations: deploy tokens get 403 `token_scope` (default deny).
