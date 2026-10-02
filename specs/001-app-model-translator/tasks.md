---

description: "Task list for feature 001: app model and Nomad job translator"
---

# Tasks: App Model and Nomad Job Translator

**Input**: Design documents from `specs/001-app-model-translator/`

**Prerequisites**: [plan.md](plan.md), [spec.md](spec.md), [research.md](research.md),
[data-model.md](data-model.md), [contracts/translate.md](contracts/translate.md),
[quickstart.md](quickstart.md)

**Tests**: Required (constitution VIII): table-driven tests, plus golden JSON in
`internal/nomadjob/testdata/`. In each story, write the test first and watch it fail.

**Organization**: by user story. US1 = the whoami job runs (MVP). US2 = bad input fails naming
the field. US3 = the other Phase 1 shapes.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: can run in parallel (different files, no dependency on an unfinished task)
- **[Story]**: US1, US2, US3 from spec.md

## Conventions for every task

- Pointer fields of Nomad types are built with `new(expr)` (research R2), e.g. `Count: new(2)`,
  `MinHealthyTime: new(10 * time.Second)`. No `ptr` helper.
- Errors are `*app.FieldError`, built with `app.NewFieldError(field, format, args...)`. Field
  paths are exactly as in the contract's error catalogue.
- No `panic`, no I/O, no logging, no globals except compiled `regexp` vars and the test `-update`
  flag.
- Keep `gofmt -l .` empty after every task.

---

## Phase 1: Setup

**Purpose**: dependency and folders

- [X] T001 Add the Nomad API client pinned to the v2.0.7 release commit: run
  `go get github.com/hashicorp/nomad/api@9dcbdc5e64ecc4ec63e42b225af729c87c87cf83` at the repo
  root and confirm `go.mod` gets `v0.0.0-20260917172403-9dcbdc5e64ec` (research R1). Don't run
  `go mod tidy` yet: nothing imports it, so tidy would remove it (T007 does it).
- [X] T002 Create folders `internal/app/` and `internal/nomadjob/testdata/`

---

## Phase 2: Foundational (blocks every story)

**Purpose**: the model types, the error type and the two function skeletons all stories build on

- [X] T003 [P] Create `internal/app/app.go`: package doc (every input normalizes into `App`;
  downstream code only sees this model; `Source` is never read after validation) and the types
  `App{ID, Name string; Source Source; Services []Service}`, `Source{Kind SourceKind}`,
  `type SourceKind string` with `const SourceImage SourceKind = "image"`,
  `Service{Name, Image, Command string; Args []string; Env []EnvVar; Ports []Port; Domains
  []Domain; Replicas *int; Resources Resources; Health *HealthCheck}`, `EnvVar{Name, Value
  string}`, `Port{Name string; ContainerPort int}`, `Domain{Host, Port string}`,
  `HealthCheck{Path, Port string; Interval, Timeout time.Duration}`, `Resources{CPUMHz, MemoryMB
  int}`. Doc comment on every type and field from data-model.md (what zero/nil means).
- [X] T004 [P] Create `internal/app/deployment.go`: `Deployment{ID string; Trigger Trigger;
  Digests map[string]string}`, `type Trigger string`, `const TriggerManual Trigger = "manual"`,
  `TriggerWebhook Trigger = "webhook"`. Doc says status, timing and the Nomad job version belong
  to feature 003.
- [X] T005 [P] Create `internal/app/errors.go`: `FieldError{Field, Message string}` with
  `func (e *FieldError) Error() string` returning `e.Field + ": " + e.Message`;
  `func NewFieldError(field, format string, args ...any) *FieldError` (uses `fmt.Sprintf`);
  `Warning{Field, Message string}` with a doc comment saying Phase 1 never produces one.
- [X] T006 Create `internal/app/validate.go` with `func Validate(a App, d Deployment) error`
  that returns `nil` for now, and a doc comment listing the check order from
  contracts/translate.md ("Validation order", step 2). Depends on T003–T005.
- [X] T007 Create `internal/nomadjob/translate.go`: package doc (pure: no I/O, deterministic;
  the only place that knows Nomad's job shape), `Settings{Entrypoint, CertResolver string;
  DefaultCPUMHz, DefaultMemoryMB int}`, `Result{Job *api.Job; Warnings []app.Warning}`, and
  `func Translate(a app.App, d app.Deployment, s Settings) (Result, error)` that calls
  `app.Validate(a, d)`, returns `Result{}, err` on error, and otherwise returns
  `Result{Job: &api.Job{}}` for now. Import `github.com/hashicorp/nomad/api`. Then run
  `go mod tidy` and `go build ./...` (both must succeed; `go mod tidy -diff` must print nothing).

**Checkpoint**: `go build ./... && go vet ./...` pass; the packages exist and compile.

---

## Phase 3: User Story 1 - A routed web app becomes a job that runs (Priority: P1) 🎯 MVP

**Goal**: the whoami-equivalent input produces exactly `deploy/dev/whoami.json`, and that output
deploys healthy in the lab.

**Independent Test**: `go test ./internal/nomadjob -run 'TestTranslate/whoami|TestTranslateMatchesLabJob|TestTranslateDeterministic|TestTranslateNewDeploymentOnlyChangesMeta' -v`
passes, then `deploy/dev/lab.sh check internal/nomadjob/testdata/whoami.golden.json` prints OK.

### Tests for User Story 1

- [X] T008 [US1] Create `internal/nomadjob/translate_test.go` with: `var update =
  flag.Bool("update", false, "rewrite golden files")`; fixtures `whoamiApp() app.App` (ID and
  Name `whoami`, Source image, one service `web`, Image `traefik/whoami:latest`, Ports
  `[{http 80}]`, Domains `[{whoami.localhost http}]`, Replicas `new(2)`, Health `{Path: "/"}`, no
  resources, no env), `whoamiDeployment() app.Deployment` (ID `lab-manual`, Trigger manual,
  Digests `web` → `traefik/whoami@sha256:c4717a8d1f0134a7444e24f881160e033991f23027c6c5a9a3f8fd22e70d1d44`),
  `labSettings() Settings` (`websecure`, `""`, 100, 64); a helper `marshalJob(t, job) []byte`
  = `json.MarshalIndent(struct{ Job *api.Job }{job}, "", "  ")` plus `"\n"`; and a table test
  `TestTranslate` with cases `{name, app, deployment, settings}` (first case `whoami`) that
  translates, asserts `err == nil` and `len(res.Warnings) == 0`, then compares `marshalJob`
  bytes with `testdata/<name>.golden.json`, writing the file instead when `*update` is set.
  On mismatch, report the golden path and tell the reader to run with `-update` and review the
  diff.
- [X] T009 [US1] Add `TestTranslateMatchesLabJob` to `internal/nomadjob/translate_test.go`:
  open `../../deploy/dev/whoami.json`, decode with a `json.Decoder` with
  `DisallowUnknownFields()` into `struct{ Job *api.Job }`, translate the whoami fixtures, and
  compare with `reflect.DeepEqual(want.Job, res.Job)`. On mismatch, print both as indented JSON
  (research R3).
- [X] T010 [US1] Add to `internal/nomadjob/translate_test.go`: `TestTranslateDeterministic`
  (translate the whoami fixtures 100 times; every `marshalJob` result is byte-identical to the
  first) and `TestTranslateNewDeploymentOnlyChangesMeta` (translate with deployment IDs `d1` and
  `d2`; set the second job's `Meta["caravana.deployment"]` back to `d1` and assert
  `reflect.DeepEqual` with the first, and assert the original two values differ).
  Run the US1 tests and confirm they fail (the job is still empty).

### Implementation for User Story 1

- [X] T011 [US1] In `internal/nomadjob/translate.go`, build the job (unexported `func job(a
  app.App, d app.Deployment, s Settings) *api.Job`): `ID` = app ID, `Name` = app Name or ID when
  empty, `Type` `"service"`, `Datacenters []string{"*"}`, `Meta{"caravana.app": ID,
  "caravana.deployment": d.ID}`, `Update{MaxParallel 1, HealthCheck "checks", MinHealthyTime
  10s, HealthyDeadline 3m, ProgressDeadline 10m, AutoRevert true}`, and one task group per
  service in order. Name the durations as package constants. Wire it into `Translate`.
- [X] T012 [US1] In `internal/nomadjob/translate.go`, add `func taskGroup(...) *api.TaskGroup`:
  `Name` = service name, `Count` = `*Replicas` or 1 when nil, `ShutdownDelay` 5s, `Networks` =
  one `{Mode: "bridge", DynamicPorts: [{Label: port.Name, To: port.ContainerPort}]}` (leave
  `DynamicPorts` nil when there are no ports), `Services` from T013, `Tasks` = one task from
  T014.
- [X] T013 [US1] In `internal/nomadjob/translate.go`, add the service registration for a service
  with domains: `Name` and router name `<appID>-<service>`, `PortLabel` = the domains' port,
  `Provider "nomad"`, `Tags` in this order: `traefik.enable=true`,
  ``traefik.http.routers.<r>.rule=Host(`h1`) || Host(`h2`)`` (hosts in declared order, joined by
  ` || `), `traefik.http.routers.<r>.entrypoints=<Settings.Entrypoint>`,
  `traefik.http.routers.<r>.tls=true` (research R11). Add its health check: `Name "health"`,
  `Type "http"`, `Path`, `Interval` = health interval or 10s, `Timeout` = health timeout or 2s,
  and no `PortLabel` when the check's port equals the registration's port (research R10).
- [X] T014 [US1] In `internal/nomadjob/translate.go`, add the task: `Name` = service name,
  `Driver "docker"`, `Config map[string]any{"image": d.Digests[service]}`, `Resources{CPU:
  CPUMHz or Settings.DefaultCPUMHz, MemoryMB: MemoryMB or Settings.DefaultMemoryMB}`.
- [X] T015 [US1] Run `go test ./internal/nomadjob -run TestTranslate/whoami -update` to create
  `internal/nomadjob/testdata/whoami.golden.json`, check that its non-null fields match
  `deploy/dev/whoami.json`, then run every US1 test without `-update`: all pass.
- [X] T016 [P] [US1] In `deploy/dev/lab.sh`, let `check` take an optional job file (research
  R12): dispatch `check) shift; cmd_check "$@" ;;`; in `cmd_check`, use `${1:-$DIR/whoami.json}`
  and turn a relative path into an absolute one (`"$(cd "$(dirname "$f")" && pwd)/$(basename
  "$f")"`), because the VM mounts the repo at the same absolute path. Update the usage comment at
  the top of the script and the `check` line in `deploy/dev/README.md`.
- [X] T017 [US1] Lab proof (quickstart §3, needs the Lima VM): `deploy/dev/lab.sh up`, then
  `deploy/dev/lab.sh check internal/nomadjob/testdata/whoami.golden.json`. Expect `OK: whoami
  answers over HTTPS through Traefik.` and a successful deployment with 2 healthy allocations in
  the Nomad UI. If the lab isn't available, record this task as not run; don't mark it done.

**Checkpoint**: MVP. The translator reproduces the lab job and it runs.

---

## Phase 4: User Story 2 - Bad input fails loudly and names the field (Priority: P2)

**Goal**: every rule in the contract's error catalogue returns `*app.FieldError` with the exact
field path and no job.

**Independent Test**: `go test ./internal/app -run TestValidate -v` and
`go test ./internal/nomadjob -run TestTranslateErrors -v` pass, one subtest per case.

### Tests for User Story 2

- [X] T018 [P] [US2] Create `internal/app/validate_test.go`: fixtures `validApp()` (app `shop`,
  service `web` with Image `ghcr.io/acme/shop:main`, Ports `[{http 8080}]`, Domains
  `[{shop.example.com http}]`, Health `{Path: "/healthz"}`; plus service `worker` with no ports)
  and `validDeployment()` (ID `d1`, Trigger manual, a valid digest for both services, e.g.
  `ghcr.io/acme/shop@sha256:` + 64 `a`). Table `TestValidate` with `{name; mutate func(a *App, d
  *Deployment); wantField string}`; each case builds fresh fixtures, applies `mutate`, calls
  `Validate`, and asserts with `errors.As` that the error is a `*FieldError` with
  `Field == wantField` and a non-empty `Message`; `wantField == ""` means `Validate` returns nil.
  Cases (index paths assume `web` is `services[0]`, `worker` is `services[1]`):
  - valid: the fixtures as-is; digest `localhost:5000/shop@sha256:…`; single-label host
    `localhost`; args without command; replicas `new(3)`
  - `id`: `""`, `"Shop"`, `"-shop"`, `"shop_1"`, 64 × `a`
  - `name`: `"bad\x00name"`, `"line\nbreak"`
  - `source.kind`: `""`, `"compose"`
  - `services`: no services
  - `services[0].name`: `""`, `"Web"`; `services[1].name`: duplicate `web`; long combination
    (app ID 40 chars, service name 30 chars)
  - `services[0].image`: `""`, `"ghcr.io/acme/shop main"`
  - `services[0].replicas`: `new(0)`, `new(-1)`
  - `services[0].resources.cpuMHz`: -1; `services[0].resources.memoryMB`: -1
  - `services[0].env[0].name`: `""`, `"A=B"`, `"A\x00"`; `services[0].env[1].name`: duplicate
  - `services[0].ports[0].name`: `""`, `"HTTP"`, `"my-port"`, `"1http"`;
    `services[0].ports[1].name`: duplicate; `services[0].ports[0].containerPort`: 0, 65536
  - `services[0].domains[0].host`: `""`, `"Shop.example.com"`, `"a b.com"`, ``"a`b.com"``,
    `"*.example.com"`, `"example.com."`, a 64-char label; `services[0].domains[1].host`: same host
    twice in one service; `services[1].domains[0].host`: host already used by `web`
  - `services[0].domains[0].port`: `""`, `"nope"`; `services[0].domains[1].port`: a second
    declared port (FR-027); `services[1].domains[0].port`: domain on the port-less `worker`
  - `services[0].health`: nil while ports exist; `services[1].health`: set on `worker`
  - `services[0].health.path`: `"healthz"`
  - `services[0].health.port`: `"nope"`; empty with two ports and no domains
  - `services[0].health.interval`: -1s; `services[0].health.timeout`: -1s
  - `deployment.id`: `""`; `deployment.trigger`: `""`, `"rollback"`
  - `deployment.digests[web]`: missing; `traefik/whoami:latest`; `…@sha256:abc` (short);
    uppercase hex; tag plus digest `ghcr.io/acme/shop:main@sha256:…`
  - `deployment.digests[db]`: extra key; `deployment.digests[a]`: extra keys `b` and `a` (sorted,
    so `a` is reported)
- [X] T019 [P] [US2] Add `TestTranslateErrors` to `internal/nomadjob/translate_test.go`: a table
  using the whoami fixtures plus a mutation of app, deployment or settings, asserting `errors.As`
  `*app.FieldError`, the exact `Field`, and `res.Job == nil`. Cases:
  - `settings.entrypoint`: `""`, `"web secure"`, `"a,b"`; `settings.certResolver`: `"le tls"`;
    `settings.defaultCPUMHz`: 0; `settings.defaultMemoryMB`: 9
  - `services[0].resources.memoryMB`: 5 (below Nomad's 10)
  - `services[0].health.interval`: 500ms; `services[0].health.timeout`: 500ms; timeout 10s with
    the default 10s interval; timeout 3s with interval 2s
  - `deployment.digests[web]`: `traefik/whoami:latest` (proves `app.Validate` runs)
  - settings checked first: bad entrypoint and a bad digest together → `settings.entrypoint`
  Run both tables and confirm they fail.

### Implementation for User Story 2

- [X] T020 [US2] Implement `Validate` in `internal/app/validate.go` exactly in the contract's
  order, stopping at the first error. Package-level compiled patterns from research R9:
  `labelRE` `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`, `portNameRE` `^[a-z][a-z0-9_]{0,62}$`,
  `digestRE` `^(?:[a-zA-Z0-9.-]+(?::[0-9]+)?/)?[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*@sha256:[a-f0-9]{64}$`.
  Hosts: split on `.`, every label matches `labelRE`, total length ≤ 253, no empty label. Track
  hosts in a `map[string]bool` across services to catch app-wide duplicates. The combined
  `<id>-<name>` length check is on `services[i].name`. Extra digest keys are checked by sorting
  them with `slices.Sorted(maps.Keys(d.Digests))`. Small unexported helpers (`validateService`,
  `validateDomains`, `validateHealth`, `validateDeployment`) keep each function short.
- [X] T021 [US2] In `internal/nomadjob/translate.go`, before calling `app.Validate`, validate
  `Settings` (entrypoint required and `^[A-Za-z0-9_-]+$`; certResolver empty or the same
  pattern; defaultCPUMHz ≥ 1; defaultMemoryMB ≥ 10, as named constants `minCPUMHz`,
  `minMemoryMB`). After `app.Validate`, check Nomad limits per service in order, with defaults
  applied: memory ≥ 10 MB; check interval and timeout ≥ 1s (`minCheckDuration`); timeout <
  interval. Run all tests: US1 and US2 pass.

**Checkpoint**: US1 and US2 pass independently; bad input never produces a job.

---

## Phase 5: User Story 3 - The other Phase 1 shapes translate correctly (Priority: P3)

**Goal**: golden output for every shape Phase 1 needs.

**Independent Test**: `go test ./internal/nomadjob -run TestTranslate -v` passes for all eight
golden cases.

### Tests for User Story 3

- [X] T022 [US3] Add these cases to the `TestTranslate` table in
  `internal/nomadjob/translate_test.go` (digests: `<image repo>@sha256:` + 64 hex chars, a
  different repeated character per app; lab settings unless stated):
  - `minimal-web`: app `blog`, service `web`, Image `ghcr.io/acme/blog:main`, Ports
    `[{http 8080}]`, Domains `[{blog.example.com http}]`, Health `{}` (TCP), Replicas nil, no
    resources
  - `multi-domain`: app `shop`, service `web`, Ports `[{http 3000} {metrics 9100}]`, Domains
    `[{shop.example.com http} {www.shop.example.com http}]`, Health `{Path: "/healthz", Port:
    "metrics"}`
  - `worker`: app `jobs`, service `worker`, Command `/app/worker`, Args `["--queue",
    "default"]`, Env `[{QUEUE_URL redis://queue:6379} {LOG_LEVEL info}]`, no ports, no health
  - `internal-service`: app `api`, service `grpc`, Ports `[{grpc 50051}]`, Health `{}`, no
    domains
  - `letsencrypt`: the whoami fixtures with `CertResolver: "letsencrypt"`
  - `custom-resources`: the whoami fixtures with Resources `{500 256}` and Health `{Path: "/",
    Interval: 30s, Timeout: 5s}`
  - `multi-service`: app `blog` with the `minimal-web` service followed by the `worker` service
  Also extend `TestTranslateDeterministic` to loop over the `worker` case too, since it has env.
  Run and confirm the new cases fail (no golden files yet).

### Implementation for User Story 3

- [X] T023 [US3] Extend the builders in `internal/nomadjob/translate.go`:
  - a service with ports and no domains gets an internal registration `<appID>-<service>` with
    `PortLabel` = the check's port and no `Tags` (nil)
  - the check's port: `Health.Port` if set, else the domains' port, else the only port
  - a service with no ports gets no `Services` and no check
  - `Type "tcp"` and no `Path` when `Health.Path` is empty; the check's `PortLabel` is set only
    when it differs from the registration's `PortLabel`
  - when `Settings.CertResolver` is set, append
    `traefik.http.routers.<r>.tls.certresolver=<resolver>` after the `tls=true` tag
  - `Config["command"]` only when `Command` is set; `Config["args"]` (as `[]string`) only when
    `Args` is non-empty
  - task `Env` = a map built from `Env`, nil when there are none
- [X] T024 [US3] Run `go test ./internal/nomadjob -update`, then review each new file in
  `internal/nomadjob/testdata/` against the spec: `multi-domain` has one router whose rule is
  both hosts in order and a check `PortLabel` of `metrics`; `worker` has no `Services`, has
  command, args and an env map; `internal-service` has a registration with null `Tags`;
  `letsencrypt` ends its tags with `tls.certresolver=letsencrypt`; `custom-resources` has CPU
  500, MemoryMB 256, interval 30s, timeout 5s; `multi-service` has groups `web` then `worker`;
  `minimal-web` has Count 1, CPU 100, MemoryMB 64 and a `tcp` check. Confirm `whoami.golden.json`
  is unchanged. Then run every test without `-update`: all pass.

**Checkpoint**: all three stories pass.

---

## Phase 6: Polish & Cross-Cutting Concerns

- [X] T025 Delete the proof of concept: `git rm -r scratchpad/`. Confirm nothing references it
  (`grep -rn scratchpad --include='*.go' .` prints nothing). FR-035.
- [X] T026 [P] Update `docs/go-guide.md`: in the C# → Go table, replace "The Nomad API uses
  pointers everywhere, hence the `ptr(v)` helper." with a note that Go 1.26+ `new(expr)` builds
  a pointer to a value (`new(2)`, `new("service")`). In `CLAUDE.md`: under "Working with the
  maintainer", change "pointer helpers like `ptr[T]`" to "`new(expr)` for pointer fields"; under
  "Where we are", say feature 001 is implemented (app model in `internal/app`, translator in
  `internal/nomadjob`), `scratchpad/` is gone, and next is feature 002.
- [X] T027 Run every CI check from quickstart §2 at the repo root: `gofmt -l .` (empty),
  `go mod tidy -diff` (empty), `go vet ./...`, `go tool staticcheck ./...`, `go test -race ./...`,
  `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...`,
  `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...`. Fix anything they report.
- [X] T028 Write the maintainer summary in the final report (not a file): what was built, and a
  one-line explanation of each Go idiom used, as listed in plan.md "Implementation notes": 
  `new(expr)`, `*int` as optional, `error` and `errors.As`, implicit interfaces, table tests with
  `t.Run`, `testdata/` and `-update`, `reflect.DeepEqual`, `regexp.MustCompile` in package vars.
  Include the commit message for the new dependency from plan.md (constitution I). Commit only
  when the maintainer asks.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (T001–T002)**: none.
- **Foundational (T003–T007)**: after Setup. Blocks all stories.
- **US1 (T008–T017)**: after Foundational.
- **US2 (T018–T021)**: after Foundational. Independent of US1 in principle, but T019 and T021
  edit files US1 also edits (`translate_test.go`, `translate.go`) and reuse US1's whoami
  fixtures, so in practice run it after US1.
- **US3 (T022–T024)**: after US1 (extends its builders and golden table). Doesn't need US2.
- **Polish (T025–T028)**: after the stories you're shipping; T027 last but one, T028 last.

### Within each story

Tests first (and failing), then implementation, then the checkpoint run. T015 needs T011–T014.
T017 needs T015 and T016.

### Parallel Opportunities

- T003, T004, T005: three different new files.
- T016 (`lab.sh`) alongside any US1 Go task.
- T018 (`internal/app/validate_test.go`) alongside T019 (`internal/nomadjob/translate_test.go`).
- T026 (docs) alongside T025 or T027.

## Parallel Example: Foundational and User Story 2

```text
Together: T003 app.go, T004 deployment.go, T005 errors.go
Together: T018 validate_test.go table, T019 TestTranslateErrors table
```

## Implementation Strategy

### MVP first (US1)

1. Phases 1 and 2.
2. US1 through T015: the translator reproduces `deploy/dev/whoami.json`.
3. T016–T017: the output deploys in the lab. **Stop and check**: this is the feature's main
   acceptance test.

### Incremental delivery

1. US1: the lab job, proven in the lab.
2. US2: loud failures. After this, the translator is safe to hand to feature 003.
3. US3: the remaining shapes as goldens.
4. Polish: delete `scratchpad/`, docs, all CI checks, summary.

## Notes

- Golden updates are only accepted after reading the diff; never run `-update` to make a failing
  test pass without understanding why it failed.
- If the whoami field-for-field test (T009) fails, the translator is wrong, not the lab file:
  `deploy/dev/whoami.json` is the known-good reference.
- Commit after each phase when the maintainer asks; the dependency commit carries the
  justification from plan.md.
