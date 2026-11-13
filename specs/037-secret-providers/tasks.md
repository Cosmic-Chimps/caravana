# Tasks: Secret provider references, Bella Baxter first (037)

Each part ends with `go build ./... && go vet ./... && go tool staticcheck ./... && gofmt -l .` clean and the
touched packages' tests passing (`go test ./internal/...`), plus the CGO-free cross-compile
(`CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...`).

## Part A · Model, seam, Bella, store, service, API

- [ ] A1 `app.Secret.From *SecretRef` and `validateSecrets` (plan D1); `WithoutSecretValues` keeps it in `app`
      and `compose`; table tests incl. value+from, bad provider, bad path; a test that an app without `from`
      keeps its revision (JSON unchanged).
- [ ] A2 `secrets.Source`, `ErrSourceUnavailable`, `ErrSourceRefused`, `SourceError`; `Build(a, current,
      resolved)` and every caller; `secretstest.Source` fake (D2). Tests for Build with references.
- [ ] A3 `go get github.com/cosmic-chimps/bella-baxter-go@v0.1.1-preview.146`; `internal/secrets/bella`
      adapter (D3) and `internal/secrets/bella/bellatest` fake (D4). Adapter tests against the fake: read, filter,
      missing path absent, 401/403/404/503, wrong device key (403), tampered envelope, plaintext answer,
      timeout, bad API key form, bad private key form (SEC1 refused), `http://` non-loopback refused; no error
      text holds a value or the API key (sentinels).
- [ ] A4 Migration `0030_secret_providers.sql`, `store.SecretProvider` + CRUD + test fields,
      `DeploymentRecord.Refresh` (D5); store tests.
- [ ] A5 `deploy` credentials on `caravana/secret-providers/<name>` + `Deployer` interface + `deploytest` fake (D6).
- [ ] A6 `service/secretproviders.go` (D7): save/list/delete/test, `usedBy`, allowed places, factory
      `SetSecretSources`; tests with `secretstest.Source`.
- [ ] A7 Resolution (D8): `checkSecrets` static checks, `resolveReferences` in `deployWarned`, `ReferenceError`,
      `ErrSecretNotFound`; `CopyApp` missing secrets skip references. Tests: deploy with a reference writes the value
      into the store fake; missing path, refused, down each fail naming the field and provider and save/replace
      nothing new; not allowed is refused before save; Bella down while running doesn't touch the store's values;
      rollback resolves now.
- [ ] A8 `RefreshSecrets` (D9): changed / unchanged / refusals; record `Refresh` + `source`.
- [ ] A9 API (D11) + `api/openapi.yaml` 0.40.0 (`SecretRef`, `SecretProvider`, the five operations, the codes,
      `Deployment.secretsRefreshed`); scope rule for `refreshSecrets` and `allowedForDeployTokens`; contract and
      parity tests pass; sentinel test (a fake source whose value is `s3cret-sentinel-037-api`) over every answer
      and the request log.
- [ ] A10 `cmd/caravana` wires `SetSecretSources` with the Bella adapter.

## Part B · Console

- [ ] B1 Settings › Secret providers page, form, Test, Remove (contracts/console.md); nav entry; tests incl. no
      credential echoed into the form or page after a refused save.
- [ ] B2 Variables page: reference rows, Add "From a provider", the "Where secrets come from" panel, Refresh
      secrets; kept-file apps write references into the overlay (`editSecrets`).
- [ ] B3 Deployments tab: "Refreshed secrets" kind words; the rollback note. Compose form: read-only references kept
      through the draft.
- [ ] B4 Sentinel test over every page touched (`s3cret-sentinel-037-ui`); `tools/ui-css.sh build` and `check`;
      parity test lists the new console routes.

## Part C · Lab, docs

- [ ] C1 `startLab` runs a `bellatest` server on 127.0.0.1 (one per lab run).
- [ ] C2 Gate step `secret-references` (`cmd/caravana/gate_secret_refs_test.go`): provider via the API (Test ok),
      a busybox app with one reference and one typed secret; deploy; `printenv` through Run a command shows the
      value; change it in the fake; refresh → new deployment and the new value; refresh again → `changed: false`;
      fake down → restart the app, `printenv` still shows the value; a deploy fails 502
      `secret_provider_unavailable` naming the field; a project not allowed → 400 naming the field; delete the
      provider while used → 409; the sentinel in no answer, record, export, job or the SQLite file; cleanup.
- [ ] C3 Console step `console-secret-providers` (`cmd/caravana/console_secret_providers_test.go`): add through
      the form, Test, add a reference on the Variables page, Refresh secrets (unchanged notice, then changed after
      the fake changes), Remove refused while used, then removed after the app is deleted.
- [ ] C4 `docs/secret-providers.md` (setup with Bella: make an API key for Caravana in Bella's console, the private
      key, allowed places, Refresh, rollback reads today's values, Bella down, `BELLA_DEBUG` logs only headers
      masked and failed bodies), `changelog.d/secret-providers.md`, CLAUDE.md "Where we are" paragraph.
