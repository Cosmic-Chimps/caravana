# Tasks: Secret templates (053)

Each part ends with `go build ./... && go vet ./... && go tool staticcheck ./... && gofmt -l .` clean and the touched
packages' tests passing, plus the CGO-free cross-compile.

## Part A · Model

- [x] A1 `app.Secret.Template`/`Refs`, `WithoutValue`, `IsReference`; `secrettemplate.go` (`ParseTemplate`, `RenderTemplate`,
      `TemplateRefNames`, `validateSecretTemplate`), `validateSecrets` (plan D1–D3). Table tests: `$$`, adjacent
      placeholders, a value with `$` and `${x}` inserted literally, every parse error with its offset, every validation error,
      JSON unchanged without the fields.
- [x] A2 `compose` overlay: tests that a template reaches the app, errors name `services.<svc>.secrets[j]…`, and
      `WithoutSecretValues` keeps it.
- [x] A3 `secrets.Build` takes a template secret's value from the resolved sets.

## Part B · Service

- [x] B1 `referencesOf`, `resolveReferences` (render), `referencedBy`, `HasSecretReferences`, `placeholderSets` (plan D4).
- [x] B2 `SetSecretIf`/`SetSecretRefIf` clear a template; `CopyApp` missing secrets; provider `usedBy` (D7).
- [x] B3 Tests: deploy with a template (rendered, one read per provider, stored as written, sentinel in store/SQLite/
      record/job/log), two providers, static checks and every failure with its field, Refresh re-renders and is a no-op when
      unchanged, replacing, copy, provider in use.

## Part C · API, console, docs

- [x] C1 `api/openapi.yaml` 0.59.0 (`Secret.template`, `Secret.refs`); API test (deploy, read back, failures with code and
      field, refresh, sentinel in every answer).
- [x] C2 Console: Variables row, Compose form row, provider counts; tests (rows, no input, preview + deploy keeps it, Configure
      keeps it, no value on any page).
- [x] C3 `docs/secret-providers.md` Templates section, `changelog.d/053-secret-templates.md`, CLAUDE.md paragraph.

## Part D · Lab

- [x] D1 Gate step `secret-references`: `GATE_CONN` template, `printenv` in the copy, Refresh re-renders, missing path and
      bad template refusals (plan D10).
