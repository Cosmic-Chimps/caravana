# Tasks: Secret provider references, Bella Baxter first (037)

Each part ends with `go build ./... && go vet ./... && go tool staticcheck ./... && gofmt -l .` clean and the
touched packages' tests passing (`go test ./internal/...`), plus the CGO-free cross-compile
(`CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...`).

## Part A · Model, seam, Bella, store, service, API

- [x] A1 `app.Secret.From *SecretRef` and `validateSecrets` (plan D1); `WithoutSecretValues` keeps it in `app`
      and `compose`; table tests incl. value+from, bad provider, bad path; a test that an app without `from`
      keeps its revision (JSON unchanged).
- [x] A2 `secrets.Source`, `ErrSourceUnavailable`, `ErrSourceRefused`, `SourceError`; `Build(a, current,
      resolved)` and every caller; `secretstest.Source` fake (D2). Tests for Build with references.
- [x] A3 `go get github.com/cosmic-chimps/bella-baxter-go@v0.1.1-preview.146`; `internal/secrets/bella`
      adapter (D3) and `internal/secrets/bella/bellatest` fake (D4). Adapter tests against the fake: read, filter,
      missing path absent, 401/403/404/503, wrong device key (403), tampered envelope, plaintext answer,
      timeout, bad API key form, bad private key form (SEC1 refused), `http://` non-loopback refused; no error
      text holds a value or the API key (sentinels).
- [x] A4 Migration `0030_secret_providers.sql`, `store.SecretProvider` + CRUD + test fields,
      `DeploymentRecord.Refresh` (D5); store tests.
      Deviation: `Refresh` is a field of `app.Deployment` (`json:"refresh,omitempty"`, beside `Origin` and
      `TokenName`, which `CreateDeployment` already records from `d`), so the record reads
      `rec.Deployment.Refresh`; the plan's `DeploymentRecord.Refresh` would have changed `CreateDeployment`'s
      signature for ~100 callers. `refresh` is the last column of `deploymentColumns`.
- [x] A5 `deploy` credentials on `caravana/secret-providers/<name>` + `Deployer` interface + `deploytest` fake (D6).
      Deviation: there is no `Deployer` interface or `deploytest` fake to extend: the service holds the concrete
      `*deploy.Deployer`, and its tests run against the fake Nomad (`deploytest.Nomad`), which already serves
      Variables. The three calls are methods of `*deploy.Deployer` with tests there. Items: `api_key`,
      `private_key` (`deploy.SecretProviderAPIKey`, `deploy.SecretProviderPrivateKey`).
- [x] A6 `service/secretproviders.go` (D7): save/list/delete/test, `usedBy`, allowed places, factory
      `SetSecretSources`; tests with `secretstest.Source`.
      Deviation: the factory is `service.SourceFactory`, and `service.BellaSource` (the Bella one, which maps
      `bella.ConfigError` to field errors `bella.url`, `bella.project`, `bella.environment`, `apiKey`,
      `privateKey`) lives in the service package, so `cmd/caravana` passes `service.BellaSource`; the SDK is
      still imported only by `internal/secrets/bella`. Tests use `fakeSources` (service) or a `secretstest.Source`
      (api).
- [x] A7 Resolution (D8): `checkSecrets` static checks, `resolveReferences` in `deployWarned`, `ReferenceError`,
      `ErrSecretNotFound`; `CopyApp` missing secrets skip references. Tests: deploy with a reference writes the value
      into the store fake; missing path, refused, down each fail naming the field and provider and save/replace
      nothing new; not allowed is refused before save; Bella down while running doesn't touch the store's values;
      rollback resolves now.
      `ReferenceError{Field, Provider, Path, Err}` is in `service/secretrefs.go`; credentials that are missing or
      settings that no longer build are a refused `ReferenceError` (422), not a field error.
- [x] A8 `RefreshSecrets` (D9): changed / unchanged / refusals; record `Refresh` + `source`.
- [x] A9 API (D11) + `api/openapi.yaml` 0.40.0 (`SecretRef`, `SecretProvider`, the five operations, the codes,
      `Deployment.secretsRefreshed`); scope rule for `refreshSecrets` and `allowedForDeployTokens`; contract and
      parity tests pass; sentinel test (a fake source whose value is `s3cret-sentinel-037-api`) over every answer
      and the request log.
      The list answer is the named schema `SecretProviderList` (the plan's contract had an inline object);
      `ImageNotFound` (422) and `BadGateway` (502) responses document the new codes for every deploy path.
- [x] A10 `cmd/caravana` wires `SetSecretSources` with the Bella adapter.

## Part B · Console

- [x] B1 Settings › Secret providers page, form, Test, Remove (contracts/console.md); nav entry; tests incl. no
      credential echoed into the form or page after a refused save.
      `internal/ui/secretproviders.go`, the `settings-secret-providers` template, route `/settings/secret-providers`
      (+ `/test`, `/remove`), nav entry in Connections after Registries, `secretProvidersState` (■ failed test, ● ok,
      ○ untested; the nav counts providers). Deviations: the icon is `key` (`key-round` isn't vendored); a row shows
      "tested 2 hours ago" (relative, as Registries does) instead of the contract's absolute "Nov 13"; the row's second
      line is "Bella Baxter · project/environment · host" and a third line is the allowed places; a refusal that
      names no field (Nomad down, provider in use) is the page's alert at 502/409.
- [x] B2 Variables page: reference rows, Add "From a provider", the "Where secrets come from" panel, Refresh
      secrets; kept-file apps write references into the overlay (`editSecrets`).
      `internal/ui/variables.go` + `secret-sources` template; `POST /apps/{id}/secrets/refresh` (changed → 303 to the
      deployment; unchanged → 303 to `?refresh=unchanged` so a reload doesn't post again; refusals draw the page).
      Part A fixes needed here: `service.SetSecretRefIf` (new; a reference replaces a typed value and the reverse:
      `SetSecretIf` now clears `From`), `withSecretNames` kept `Name` only, which dropped a reference from a kept
      overlay on every later secret edit (now `WithoutValue()`), `service.ProviderAllows` (exported for the select).
      Every console deploy path now says a `*service.ReferenceError` in Caravana's words (422 for a missing path or a
      refusal, 502 for a provider that is down): `referenceRefusal` in `refuseDeploy`. Behaviour to know: a refused add
      leaves the definition saved with the reference (as any deploy that fails after the save), so the page says "The
      changes are saved; the failed attempt is on the Deployments tab."; a path the provider lacks sits at the Path input.
- [x] B3 Deployments tab: "Refreshed secrets" kind words; the rollback note. Compose form: read-only references kept
      through the draft.
      History entry: "Refreshed secrets (v2's version) · deployment abc" in place of "Redeploy of abc"; the Deploying-now
      card says the same in `changeLine`; "Redeploy this version" adds "Reads secrets from providers again now, not as
      they were." when the version's definition has a reference. Compose form: `draftSecret.From`, read-only row with no
      input, kept by `draftOf`, `draftFrom` and `keepStored` (a reference has no input to post, so the stored one is what
      the deploy keeps).
- [x] B4 Sentinel test over every page touched (`s3cret-sentinel-037-ui`); `tools/ui-css.sh build` and `check`;
      parity test lists the new console routes.
      `TestProviderPagesHoldNoSecret` (every page and jobspec, after a refresh, a refused test and a provider down) plus
      the sentinel checks in each B1–B3 test; `consoleParity` lists the five routes (and `listSecretProviders` on every
      Settings page and the Variables page, which read it). `app.css` regenerated.

## Part C · Lab, docs

- [x] C1 `startLab` runs a `bellatest` server on 127.0.0.1 (one per lab run).
- [x] C2 Gate step `secret-references` (`cmd/caravana/gate_secret_refs_test.go`): provider via the API (Test ok),
      a busybox app with one reference and one typed secret; deploy; `printenv` through Run a command shows the
      value; change it in the fake; refresh → new deployment and the new value; refresh again → `changed: false`;
      fake down → restart the app, `printenv` still shows the value; a deploy fails 502
      `secret_provider_unavailable` naming the field; a project not allowed → 400 naming the field; delete the
      provider while used → 409; the sentinel in no answer, record, export, job or the SQLite file; cleanup.
      Passed in the lab on 2026-11-13 (50 s, twice, run alone with `-run 'TestLab/^secret-references$'`). The
      printenv check waits up to 90 s for the value: right after a rollout the old copy may still be listed or an
      exec may lose its output (task `phpv`), which made one read come back empty.
- [x] C3 Console step `console-secret-providers` (`cmd/caravana/console_secret_providers_test.go`): add through
      the form, Test, add a reference on the Variables page, Refresh secrets (unchanged notice, then changed after
      the fake changes), Remove refused while used, then removed after the app is deleted.
      Passed in the lab on 2026-11-13 (53 s, with `-run 'TestConsoleLab/^console-(sign-in|secret-providers)$'`).
- [x] C4 `docs/secret-providers.md` (setup with Bella: make an API key for Caravana in Bella's console, the private
      key, allowed places, Refresh, rollback reads today's values, Bella down, `BELLA_DEBUG` logs only headers
      masked and failed bodies), `changelog.d/secret-providers.md`, CLAUDE.md "Where we are" paragraph.
