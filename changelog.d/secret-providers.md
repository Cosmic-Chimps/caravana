### Added
- **Secrets from Bella Baxter.** A secret can name where its value lives instead of holding it:
  `{"name": "DATABASE_URL", "from": {"provider": "bella-prod", "path": "DATABASE_URL"}}`, or "From a
  provider" on the Variables page. At each deploy Caravana reads the value from Bella and puts it in the
  service's Nomad Variable, as it does with a typed value, so the app never talks to Bella and a restart
  never waits on it. The console shows where a value comes from, never the value.
- **Settings › Secret providers.** Connect a Bella Baxter environment with its URL, project, environment,
  an API key made for Caravana and the private key Bella showed with it, limit it to some projects or
  environments, and Test it ("12 secrets readable"). The keys stay in Nomad Variables.
- **Refresh secrets.** After a value changes in Bella, "Refresh secrets" on the Variables page (or
  `POST /v1/apps/{id}/secrets/refresh`) reads every reference again and rolls the running version out
  with the new values, same images; when nothing changed it says so and deploys nothing. An app's deploy
  token may call it.
- A deploy whose reference can't be read fails and names the secret and the provider: not in Bella (422
  `secret_not_found`), Bella refusing the keys (422 `secret_provider_refused`) or Bella down (502
  `secret_provider_unavailable`). Running copies keep the values they have.
- API 0.40.0: `from` on a secret, the provider operations (`listSecretProviders`, `putSecretProvider`,
  `deleteSecretProvider`, `testSecretProvider`), `refreshSecrets` and `secretsRefreshed` on a deployment.

### Changed
- Redeploying an old version reads its provider references again now: it gets today's values from
  Bella, not the ones it had.
