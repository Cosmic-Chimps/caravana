# Tasks: Private registries

**Input**: [plan.md](./plan.md), [private-registries.spec.md](./private-registries.spec.md),
[research.md](./research.md), [data-model.md](./data-model.md), [contracts/](./contracts/),
[quickstart.md](./quickstart.md)

**Scale note**: 16 tasks over about 30 files.

Read research.md before any task: the pull mechanism (R1) was proven in the lab and must be built
exactly that way.

The risky tasks:
- **T002**: credentialed auth in `internal/registry`. Public pulls must not change.
- **T009**: the per-task copies and their sync. No secret may reach the job, and no running app
  may restart.

Run the story phases in order: they share `api/openapi.yaml`, `internal/api/parity_test.go`,
`internal/ui/settings.go` and `cmd/caravana/gate_test.go`.

**Tests are required** (constitution VIII):
- Each task writes its tests next to the code, table-driven, using `testing` and `httptest` only.
- Each task leaves these passing: `go build ./...`, `go vet ./...`, `gofmt -l .` and
  `go test ./<its packages>`.
- An API change updates `api/openapi.yaml` in the same commit.
- A template or style change runs `tools/ui-css.sh build` and commits `app.css`.

## Phase 1: Setup

Nothing: no new module or tool.

## Phase 2: Foundational (blocks every story)

- [X] T001 [P] Any registry host, in internal/registry/reference.go and client.go (research R4):
  - `Parse` accepts any host the reference grammar allows. A first path part is a host when it
    has `.` or `:`, or is `localhost`.
  - `Registry` is the lowercased host; `index.docker.io` becomes `docker.io`. Docker Hub's
    `library/` rule is unchanged, and the pinned name keeps the host and port.
  - Add `Endpoint(host string) string`:
    - `docker.io` → `https://registry-1.docker.io`;
    - loopback hosts (`localhost`, `127.0.0.0/8`, `[::1]`, with or without a port) →
      `http://<host>`;
    - everything else → `https://<host>`.

    `Client.Endpoints` overrides it in tests.
  - A TLS handshake failure becomes `ErrUnavailable` with "<host> must serve the registry API
    over HTTPS". A 401 with no usable challenge from a host that isn't a registry (no
    `Docker-Distribution-Api-Version` header and no challenge) becomes `ErrUnavailable` with
    "<host> didn't answer as a container registry".
  - Update the existing Parse tests. The old "registry isn't supported yet" cases become valid
    hosts.
  - New tests: hosts with ports, uppercase hosts, IPv6 loopback, `localhost:5000`, the endpoint
    table, the TLS error, and a non-registry 401.
- [X] T002 [P] Credentials in the digest lookup (research R3, R5), in internal/registry/client.go:
  - Add `Client.Credentials func(ctx context.Context, host string) (user, pass string, ok bool, err error)`;
    nil means anonymous.
  - On a 401:
    - **`Bearer` challenge**: the token request carries `Authorization: Basic` when credentials
      exist.
    - **`Basic` challenge**: retry the request with Basic auth when credentials exist.
  - New `ErrUnauthorized` ("the registry refused the stored credentials"): credentials were sent,
    and the token or manifest request answered 401 or 403.
  - Without credentials, the "missing or private" message ends "add credentials for <host> in
    Settings › Registries", replacing "Caravana doesn't support registry credentials yet".
  - `Exists` goes through the same path.
  - Add `Test(ctx, host) error`: `GET <endpoint>/v2/` and the challenge flow (a token with no
    scope for Bearer); nil when the end answer is 200.
  - Credentials never appear in an error, a log or a URL.

  Tests against an `httptest` server that can answer:
  - a Bearer challenge with a token endpoint that checks Basic;
  - a Basic challenge;
  - wrong credentials;
  - anonymous public access, unchanged;
  - `Test` ok, refused and unreachable;
  - a sentinel password absent from every error string.
- [X] T003 [P] Store (data-model "Store"): add internal/store/migrations/0009_registries.sql and internal/store/registries.go with `Registry`, `RegistryTest`, `SaveRegistry`, `ListRegistries`, `GetRegistry`, `DeleteRegistry` and `SaveRegistryTest`. Tests: upsert keeps `created_at`, list order, delete of a missing host gives `ErrNotFound`, and test fields round-trip.

**⟶ Wait for T001–T003, then:**

- [X] T004 Deploy, the secret and the hook (data-model "Deploy"), in internal/deploy/registries.go (new):
  - `RegistryCredentials(ctx, host)` reads `caravana/registries/<key>` (key: host with `.` → `~` and `:` → `_`: Nomad paths allow neither);
    ok is false when the variable is absent.
  - `SaveRegistrySecret(ctx, host, user, pass)` and `DeleteRegistrySecret(ctx, host)`.
  - `TestRegistry(ctx, host)` calls `registry.Client.Test`.
  - In `deploy.New` (or where the client is built), set `registry.Client.Credentials` to
    `RegistryCredentials`.
  - `Resolve` and `CheckDigests` map `registry.ErrUnauthorized` to `*ImageError` of the new kind
    `ErrRegistryAuth`.

  Tests with the fakes: resolve with stored credentials, a refused secret gives `ErrRegistryAuth`,
  and there is no variable for a public image.

**Checkpoint**: `go test ./...` passes; deploys behave as before for public images.

## Phase 3: User Story 2 - Manage registry credentials (Priority: P1)

**Goal**: add, list, test, update and remove credentials from Settings and the API; the secret is
write-only.

**Independent test**: quickstart steps 3, 6 and 8 without a deploy: save, test (ok), save a wrong
secret, test (refused), fix it, remove it. The API never answers the secret.

- [X] T005 [US2] Service, in internal/service/registries.go (new):
  - `Registries(ctx)`, `SaveRegistry(ctx, host, username string, password *string)`,
    `DeleteRegistry(ctx, host)` and `TestRegistry(ctx, host)`, in the shape of
    `SetBackupDestination` (feature 010).
  - `SaveRegistry` validates:
    - the host, through `registry.Parse` of `<host>/x` and its `Registry`;
    - the user name is not empty;
    - the password is required on create (`password`: "is required the first time") and
      refused when present but empty.
  - After saving or deleting, it calls `deploy.SyncRegistryCopies`, which T009 adds; until then
    a no-op stub. A sync failure answers "saved, but apps already deployed still have the old
    credentials: <err>".
  - `TestRegistry` stores the outcome.

  Tests: each validation, the secret never stored in SQLite (raw read), test outcome recorded,
  delete of an unknown host.
- [ ] T006 [US2] API (contracts/api.md):
  - `listRegistries`, `putRegistry`, `deleteRegistry` and `testRegistry` in
    internal/api/registries.go, registered in api.go;
  - api/openapi.yaml 0.13.0 → 0.14.0, with the `Registry` schema and `registry_auth_failed`
    (mapped in T010);
  - the parity entries for the console routes T007 adds.

  Tests: each operation, password absent from every answer (sentinel), 404, and the 422 field
  errors.
- [ ] T007 [US2] Console, Settings › Registries (contracts/console.md):
  - internal/ui/settings.go and templates/settings.html;
  - routes in internal/ui/ui.go: `POST /settings/registries` (save), `POST /settings/registries/{host}/test`,
    and `POST /settings/registries/{host}/remove`, each `protected(requireSession(...))`;
  - the exact copy, `<details>` for Edit and Remove, field errors at their inputs, and the
    `id="registries"` anchor.

  Tests: list, add, edit keeps the secret when the field is empty, test notices, remove, no
  secret in the page (sentinel), and forms without JS.

**Checkpoint**: credentials can be managed end to end; nothing deploys with them yet.

## Phase 4: User Story 1 - Deploy a private image (Priority: P1) 🎯 MVP

**Goal**: private images deploy, redeploy and roll back, and the node pulls them with the stored
credentials.

**Independent test**: the gate step `private-registry` (research R7) and quickstart steps 2–7.

- [ ] T008 [P] [US1] Job shape (data-model "Job shape"), in internal/nomadjob/translate.go, backup.go and hcl.go:
  - Translation learns which registry hosts have credentials. Add an input field or argument, for
    example `Private map[string]bool` keyed by host, passed by deploy; `Translate` stays pure.
  - For a service whose image's registry is private:
    - the main task's `config.auth` interpolates `${CARAVANA_REGISTRY_USERNAME}` and
      `${CARAVANA_REGISTRY_PASSWORD}` with `server_address` = the host;
    - one template from `nomad/jobs/<app>/<service>/<service>` into `secrets/registry.env`,
      `env = true`, `change_mode = noop`. It renders the two keys by name, and only inside
      `{{ with nomadVar ... }}`. Make sure a missing key renders an empty value, not
      `<no value>`; use `{{ index .Items "KEY" }}` or a range over tuples, and test it with
      `text/template` as 016's tests do.
  - The backup `dump` task and the restore task get the same `auth` (their variable template
    already exports every item).
  - The export header names the variable path.
  - Goldens: a private service, a Compose app mixing private and public images, and a private
    Postgres with a backup.
- [ ] T009 [US1] Per-task copies and sync, in internal/deploy/registries.go, register.go and backups.go:
  - `ensureRegistryVariables(ctx, a)`, called from `Register` before register: for each service
    whose registry has credentials, write `nomad/jobs/<app>/<service>/<service>` with
    `registry=<host>` and the two credential items.
  - `SyncBackups` adds the two items to a private Postgres service's backup variable, and
    `Restore` adds them to the restore variable.
  - `SyncRegistryCopies(ctx, host)` replaces T005's stub. It lists `nomad/jobs/*` variables and
    rewrites those whose `registry` item is the host: with the stored credentials, or with the
    credential items empty when none are stored (research R2: one item must stay non-empty).
    It also rewrites backup and restore variables whose image is on that host.
  - Deploy passes the set of credentialed hosts to `Translate`.

  Tests:
  - a deploy writes the copy and the job has the `auth` placeholders;
  - no secret is in the registered job (sentinel);
  - the sync rewrites copies and doesn't register any job;
  - removal empties the copies;
  - a public app gets no copy.
- [ ] T010 [US1] Errors and the fix action:
  - `deploy.ErrRegistryAuth` → 422 `registry_auth_failed`, with `field` from the `ImageError`, in
    internal/api/api.go (`mapError`), with tests;
  - in the console, the failed-deploy sentence for this error, and its one fix action "Check the
    registry's credentials" linking to `/settings#registries` (internal/ui/view.go, where the
    failed-deploy actions are), with tests;
  - the webhook and `RedeployVersion` paths give the same error (service tests).
- [ ] T011 [US1] Gate step `private-registry`, in cmd/caravana/gate_test.go, after `rollback-export`, per research R7:
  - the `gate-registry` job, with an `httpd:2-alpine` prestart task that writes htpasswd from
    env, and `registry:2` on host network `127.0.0.1:5000`;
  - a skopeo batch job that copies `busybox:1.37` to `127.0.0.1:5000/gate/busybox:v1` with
    `--dest-creds` and `--dest-tls-verify=false`;
  - the sequence: no credentials, then a wrong secret, then the right one (test, deploy healthy),
    then the no-secret checks, then remove.
  - Per-run user and password, from `g.run`.
  - Clean up the jobs, the variables and the app.
  - `go vet ./cmd/caravana` must pass. I run the lab.

**Checkpoint**: the MVP: private images deploy.

## Phase 5: User Story 3 - Images from other registries (Priority: P2)

- [ ] T012 [US3] Coverage: an end-to-end deploy test (service level, with an `httptest` registry as `Client.Endpoints`) of an image from a host with a port, showing the pinned name keeps `host:port`. Also check the console's add-form hint and the API's host canonicalization (`Index.Docker.IO` → `docker.io`). Anything T001 missed gets fixed here.

## Phase 6: Polish & cross-cutting

- [ ] T013 [P] Sentinel sweep (SC-002): one test per path that handles credentials. Use a sentinel password and assert it is absent from:
  - the Settings page;
  - `listRegistries`, and the `putRegistry` and `testRegistry` answers;
  - every deploy error (refused, missing);
  - the registered job JSON (fake);
  - the export;
  - the deployment record;
  - the request log.

  Extend existing tests where they exist.
- [ ] T014 [P] Docs:
  - CHANGELOG.md "Unreleased" gains Added (private registries, any registry host, Test) and
    Changed (the "missing or private" message points at Settings). No Upgrade step is needed.
  - CLAUDE.md "Where we are" gains a 017 paragraph.
  - docs/plan.md:
    - the backlog item "Private registry credentials" becomes done as 017;
    - Phase 2.5 renumbers Aspire to 018 and Bella Baxter to 019;
    - a backlog item "Node-level registry credentials (Phase 3)" records research R1's
      trade-off.
- [ ] T015 Full check (CLAUDE.md "Commands"): `go mod tidy -diff`, build, vet, staticcheck, gofmt, `go test -race ./...`, the amd64 and arm64 CGO-free builds, `tools/ui-css.sh check`, `tools/ui-vendor.sh check`, shellcheck.
- [ ] T016 Lab validation: `CARAVANA_LAB=1 go test ./cmd/caravana -run TestLab -v -timeout 25m` (alone in the lab), then quickstart "By hand with GHCR" on the maintainer's host after the release (SC-001, SC-005). Record the results here.

## Dependencies & Execution Order

- **Foundation**: (T001 ∥ T002 ∥ T003), then T004. T002 builds on T001's endpoint, so run them
  one after another if one agent does both.
- **US2**: T005, then T006, then T007.
- **US1**: T008 can run beside US2. Then T009 (needs T005 and T008), then T010, then T011.
- **US3**: T012 after the foundation; by default it runs after US1.
- **Polish**: T013 and T014, then T015, then T016.

## Parallel examples

```text
Foundation: T001 (registry/reference.go) ∥ T003 (store)
US1:        T008 (nomadjob, pure)        ∥ T005–T007 (US2)
Polish:     T013 (tests)                 ∥ T014 (docs)
```

## Implementation strategy

- **MVP**: Foundation, US2 and US1 (T001–T011). Private GHCR images deploy, which is SC-001 and
  unblocks Phase 2.5's gate.
- **Release**: v0.5.0 after T016's automated part.
- **Delegation**: each phase goes to one Sonnet agent with this file, the spec, plan, research,
  data model and contracts. Opus reviews each diff and runs the lab before the next phase.
