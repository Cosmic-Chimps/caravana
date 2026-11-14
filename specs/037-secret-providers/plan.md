# Implementation Plan: Secret provider references, Bella Baxter first

**Spec**: [spec.md](./spec.md) · **Contracts**: [contracts/api.md](./contracts/api.md),
[contracts/console.md](./contracts/console.md) · **Tasks**: [tasks.md](./tasks.md)

## Summary

A secret may be a reference `from: {provider, path}`. Providers are a settings object (SQLite: name, kind,
non-secret settings, allowed places, last test; Nomad Variable `caravana/secret-providers/<name>`: credentials).
At deploy, under the app's lock and just before the sets are built, Caravana reads each provider once, checks
every path is there, and hands the values to `secrets.Build`, which writes them into the service's Nomad
Variable like typed values (008's previous-set restore unchanged). **Refresh secrets** redeploys the live version
with its digests pinned when a resolved value changed. Bella is reached through its Go SDK.

Migration **0030** (0027 stays reserved for spec 034). API **0.40.0**. One new Go module (Bella's SDK, plus its
Kiota packages), no runtime service, no Nomad policy change (`caravana/*` variables are already allowed), no
change to `nomadjob.Translate` (goldens untouched).

## Constitution Check

- **I Small**: no runtime service. New module `github.com/cosmic-chimps/bella-baxter-go` pinned at
  `v0.1.1-preview.146`, justified in the commit: "Bella's own SDK; we use what we build" (maintainer, 2026-11-13).
  It brings `microsoft/kiota-*`, `google/uuid`, and indirectly OpenTelemetry's API, `go-logr`, `xxhash`,
  `std-uritemplate`; `golang.org/x/crypto` is already ours. Pure Go, CGO-free cross-compile must still pass. ✓
- **II One model**: a reference is a field of `app.Secret`, so image, Compose overlay and Aspire inputs normalize
  into it; `Translate` never sees values or references. ✓
- **III Safe deploys**: a reference is declared on every deploy like a value, so each deploy still replaces the
  whole set; a refresh is a normal health-checked rollout of the live version with its digests pinned. ✓
- **IV Fail loudly**: every refusal names `services[i].secrets[j]…` and the provider; value-and-from is refused;
  a missing path fails the deploy, never deploys without the variable. ✓
- **V API-first**: provider CRUD, test and refresh are in the API and `api/openapi.yaml` in the same change; the
  console calls the same service methods. ✓
- **VI Nomad-native**: values go into the same `nomad/jobs/<app>/<service>` variables; Bella code stays in
  `internal/secrets/bella`, Nomad code in `deploy`. ✓
- **VII Secure**: the provider's credentials live in a Nomad Variable; a cold start or a restart never calls Bella
  (values are already in Nomad), only deploys and refreshes do; Bella down fails only new deploys. With ZKE,
  Caravana holds the private key: inherent to resolving at deploy, said on the Settings page. ✓
- **VIII Plain Go**: table tests, a fake Bella (`bellatest`) for the adapter's tests and the lab, a fake
  `secrets.Source` for the service's. ✓

No Complexity Tracking entry.

## Decisions

**D1 The reference is a typed field.** `app.Secret{Name, Value *string, From *SecretRef}`,
`SecretRef{Provider string "provider"; Path string "path"}` (`from,omitempty`). Static validation in
`validateSecrets`: value and from both set → `…secrets[j].from` "a secret has a value or a reference, not both";
provider is a lowercase label (`app.ValidName` rules) → `…from.provider`; path 1–512 bytes, no control byte →
`…from.path`. `app.App.WithoutSecretValues` and `compose.Request.WithoutSecretValues` keep `From`. No `field`
property yet: Bella's secrets are single values; a provider with fields (Vault) adds it then, a non-breaking change.

**D2 The source seam.** In `internal/secrets` (beside the existing `Provider`, which is the *store* and stays as it
is):
```go
// Source reads secret values from an outside provider (feature 037).
type Source interface {
    // Read returns the value of each path it has; a path it doesn't have is absent, not an error.
    Read(ctx context.Context, paths []string) (map[string]string, error)
    // Probe reads once and says how many secrets Caravana can read.
    Probe(ctx context.Context) (int, error)
}
var ErrSourceUnavailable = errors.New("secret provider unavailable") // down, timeout, 5xx
var ErrSourceRefused     = errors.New("secret provider refused")     // credentials, device key, wrong environment, bad envelope
// SourceError carries one of the two with a sentence in Caravana's words; it never wraps library text.
type SourceError struct{ Err error; Reason string }
```
`secrets.Build(a, current, resolved Sets)`: a secret with `From` takes `resolved[svc][name]` (absent is an
`*app.FieldError` on `…from.path`, defensive), size limit unchanged and counting resolved values.
`secretstest.Source` is the in-memory fake (values, `Down`, `Refused`, read counter).

**D3 Bella adapter.** Package `internal/secrets/bella`: `Config{URL, Project, Environment string; Auth Auth}` with
`type Auth interface{ options(*bellabaxter.Options) }` and `APIKey{Key, PrivateKey string}` as its one
implementation today (bella-baxter#979's grant can be a second). `New(cfg) (*Source, error)` calls
`bellabaxter.New(Options{BaxterURL, ApiKey, PrivateKeyPEM, Timeout: 10s, AppClient: "caravana"})` and maps its
construction errors to `ErrInvalidAPIKey` / `ErrInvalidPrivateKey` (fixed sentences). `Read` = `GetAllSecrets`
(one bulk call) filtered to `paths`; `Probe` = `GetAllSecrets`, `len`. Error classification (never the SDK's text):
`*bellabaxter.E2EEResponseError` → refused ("Bella's answer could not be decrypted with the private key" /
"Bella answered without encryption"); Kiota `*abstractions.ApiError` **or** `*models.ProblemDetails` (it embeds
`ApiError` by value, so `errors.As` must try both) by status: 401 → refused "Bella refused the API key"; 403 →
refused "Bella refused the read: this API key has no private key registered, or not this one"; 404 → refused
"this API key isn't for <project>/<environment>"; 500/502/503/other → unavailable "Bella answered <code>";
context deadline / network → unavailable "Bella can't be reached". The private key is required (always E2EE: the
Go SDK is the one SDK without it by default, and a ZKE-enforcing tenant refuses reads without it). URL: `https://`,
or `http://` for a loopback host only.

**D4 bellatest.** `internal/secrets/bella/bellatest`: an `httptest`-ready fake Bella. `New()` makes a key pair and
an API key (`bax-<32 hex>-<64 hex>`); handles `GET /api/v1/projects/{p}/environments/{e}/secrets`; checks
`X-Bella-Key-Id` and the HMAC signature (`METHOD\npath\nsortedquery\ntimestamp\nsha256hex(body)`), then that
`X-E2E-Public-Key` is the registered device key (else 403 problem `zke-required`), then encrypts
`{"environmentSlug","secrets",version}` with an ephemeral server P-256 key: ECDH, HKDF-SHA256 (32 zero bytes salt,
info `bella-e2ee-v1`), AES-256-GCM 12-byte nonce, envelope
`{"encrypted":true,"algorithm":"ECDH-P256-HKDF-SHA256-AES256GCM","serverPublicKey":<b64 SPKI>,"nonce","tag","ciphertext"}`
(tag split from the ciphertext). Knobs: `Set/Delete(name)`, `Down(bool)` (503), `Reads()`, `PrivateKeyPEM()`,
`APIKey()`, `URL()`. It is used by the adapter's tests and by the lab.

**D5 Providers in the store.** Migration `0030_secret_providers.sql`:
`secret_providers(name TEXT PRIMARY KEY, kind TEXT NOT NULL CHECK(kind IN ('bella')), settings TEXT NOT NULL
-- JSON {url, project, environment}, allowed TEXT NOT NULL DEFAULT '[]', created_at, updated_at, tested_at,
test_ok INTEGER, test_message TEXT, test_count INTEGER)` and `ALTER TABLE deployments ADD COLUMN refresh INTEGER NOT
NULL DEFAULT 0`. `store.SecretProvider` with `ListSecretProviders`, `GetSecretProvider`, `SaveSecretProvider`
(upsert), `DeleteSecretProvider`, `SaveSecretProviderTest`, `ClearSecretProviderTest`. `DeploymentRecord`
gains `Refresh bool` (`CreateDeployment` writes it).

**D6 Credentials in Nomad.** `deploy` gains `SaveSecretProviderCredentials(ctx, name, map[string]string)`,
`SecretProviderCredentials(ctx, name) (map[string]string, bool, error)` and `DeleteSecretProviderCredentials`,
on `caravana/secret-providers/<name>` (items `api_key`, `private_key`), shaped like the registry ones; failures
match `secrets.ErrUnavailable` as the other Variables calls do. Added to the `Deployer` interface and its fake.

**D7 Service: providers.** `service/secretproviders.go`: `SecretProviders`, `SaveSecretProvider(ctx, in)`
(`in` = name, kind, URL, project, environment, allowed, `APIKey *string`, `PrivateKey *string`; nil keeps,
required the first time, empty refused; validates by building the Bella source; writes Nomad first, then SQLite;
clears the last test when anything changed), `DeleteSecretProvider` (refused with `*ProviderInUseError{Apps}`
when any stored app references it; deletes the variable, then the row), `TestSecretProvider` (Probe, stores
ok/count or the reason). Allowed places: each `project` or `project/environment` must exist when saved (field
`allowed[i]`). Name: `app.ValidName`. The Bella source is built by an injectable factory
`s.sourceFor func(store.SecretProvider, creds map[string]string) (secrets.Source, error)` (`SetSecretSources`
from `cmd/caravana`; tests pass a `secretstest.Source`).

**D8 Service: resolution.** `checkSecrets` (before save) adds the static checks for each reference: the provider
exists (`*app.FieldError` on `…from.provider`, "no secret provider named X") and allows the app's place (same
field, "secret provider X can't be used by <project>/<environment>"). `deployWarned` calls
`resolveReferences(ctx, a)` right before `buildSecrets`: groups references by provider, repeats the static checks
(the provider may have changed), reads credentials, builds the source, `Read`s all its paths once. A path not
returned → `*ReferenceError{Field: …from.path, Provider, Path, Err: ErrSecretNotFound}`; a source error →
`*ReferenceError{Field: …from (of that provider's first reference), Provider, Err: the SourceError}`. The record
fails with `ReferenceError.Error()` (names provider and path, never a value). No reference: nothing is read.
Every other caller of `buildSecrets` (none outside deploy today) passes no resolved sets.

**D9 Refresh secrets.** `Service.RefreshSecrets(ctx, appID, trigger) (RefreshResult{Changed bool; Deployment
*store.DeploymentRecord}, error)`: defined (`ErrAppNotDefined`), not stopped (`ErrAppStopped`), a live version
(newest healthy with a definition, `ErrNoLiveDeployment`), at least one reference in it (`ErrNoSecretReferences`).
Resolve the live definition's references and build its sets with the store's current values; equal
(`Sets.Equal`) → `Changed: false`, nothing recorded. Otherwise `saveAndDeploy(live definition, Condition{},
trigger, made{file: live.Compose}, &pinned{digests, source: live.ID, refresh: true})`: the new record has
`Refresh: true` and `source`. (The live version becomes the stored app, as for a rollback; a stored definition
that never got healthy is replaced by what runs.)

**D10 Rollback, promote, copy.** `RedeployVersion` needs nothing new: its definition keeps `from`, and the deploy
resolves now. `CopyApp`'s `missingSecrets` leaves references out. Promotion: `promoted` keeps the target's secrets
as today; a source-only service carries its references; the target place is checked by the deploy.

**D11 API.** `internal/api/secretproviders.go` and `refreshSecrets` (contracts/api.md). Mapping: `*ReferenceError`
by its `Err` (`ErrSecretNotFound` 422 `secret_not_found`; `ErrSourceRefused` 422 `secret_provider_refused`;
`ErrSourceUnavailable` 502 `secret_provider_unavailable`), field = its `Field`; `*ProviderInUseError` 409
`secret_provider_in_use`; `ErrNoSecretReferences` 409 `no_secret_references`; unknown provider on the provider
routes 404 `secret_provider_not_found`. `refreshSecrets` is added to `scopeRules` (path `{id}`) and to the test's
`allowedForDeployTokens`; every provider route is denied by the default deny.

**D12 Console.** contracts/console.md: Settings › Secret providers, the Variables page's references and its
"Where secrets come from" panel with Refresh secrets, the history's "Refreshed secrets", the rollback note, and
read-only references in the Compose form (kept through the draft; `editSecrets` writes references into a kept
overlay).

**D13 Never shown.** Values exist only between `Read` and `Replace`. `ReferenceError`, `SourceError` and the
log never carry a value or library text. Sentinel `s3cret-sentinel-037…` set in the fake: searched in every API
answer, page, deployment record, SQLite file, server log, export and Nomad job.

**D14 Lab.** `startLab` runs a `bellatest` server on loopback for the steps. Gate step `secret-references`
(`gate_secret_refs_test.go`) and console step `console-secret-providers` (`console_secret_providers_test.go`),
per tasks.md Part C.

## Deviations (implementation, 2026-11-13)

- **D5:** `Refresh` is on `app.Deployment` (beside `Origin` and `TokenName`, which `CreateDeployment` already
  records from it), read as `rec.Deployment.Refresh`, not a new field of `DeploymentRecord`.
- **D6:** no `Deployer` interface or `deploytest` fake was added: the service holds the concrete
  `*deploy.Deployer` and its tests use the fake Nomad, which already serves Variables.
- **D7:** `service.BellaSource` is the factory `cmd/caravana` passes to `SetSecretSources`; it maps the
  adapter's `*bella.ConfigError` to field errors (`bella.url`, `bella.project`, `bella.environment`, `apiKey`,
  `privateKey`). The SDK is still imported only by `internal/secrets/bella` (plus `kiota-abstractions-go` for
  classifying its errors).
- **D8:** credentials missing from Nomad, or saved settings that no longer build a source, fail a deploy as a
  refused `*ReferenceError` (422 `secret_provider_refused`), not as a field error.
- **D11:** the openapi has a named `SecretProviderList`, and the new deploy-path codes are documented in the
  shared 422 and 502 responses, so every deploy operation inherits them.
- **D14:** the gate step is `secret-references`; the fake Bella is `gateTarget.Bella`, started by `startLab`,
  so the step is skipped on a real host.
