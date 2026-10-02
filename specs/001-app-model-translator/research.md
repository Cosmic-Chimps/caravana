# Research: App Model and Nomad Job Translator

Phase 0 output for [plan.md](plan.md). Each entry: decision, rationale, alternatives considered.
Facts about Nomad were checked against the v2.0.7 source (commit `9dcbdc5e`), the version the
lab runs.

## R1. Nomad API client version

- **Decision**: `github.com/hashicorp/nomad/api v0.0.0-20260917172403-9dcbdc5e64ec`, the
  pseudo-version of the `v2.0.7` release commit.
- **Rationale**: the constitution mandates the official client (VI). The `api` module is never
  tagged on its own (`go list -m -versions` lists nothing), so a pseudo-version pinned to the
  release commit the lab runs is the honest choice; `@latest` would track `main`. It pulls in a
  few small indirect modules (`go-cleanhttp`, `go-rootcerts`, `cronexpr`, `gorilla/websocket`,
  `mapstructure`, `go-homedir`), none needing CGO. The commit message must justify the new
  dependency (constitution I).
- **Alternatives**: our own job structs (rejected: violates VI and drifts from Nomad); the
  latest `main` pseudo-version (rejected: types might not match the 2.0.7 server).

## R2. Pointers: `new(expr)` instead of a `ptr` helper

- **Decision**: build Nomad's pointer fields with Go 1.26+'s `new(expr)`, e.g.
  `Count: new(2)`, `Type: new("service")`, `MinHealthyTime: new(10 * time.Second)`. No helper.
- **Rationale**: verified on the local `go1.27.1`. It's standard Go, needs no helper to explain,
  and `go fix`'s modernizer rewrites `ptr`-style helpers into it anyway. `docs/go-guide.md`
  mentions a `ptr(v)` helper; that line gets updated.
- **Alternatives**: the sketch's `func ptr[T any](v T) *T` (fine, but redundant on Go 1.27).

## R3. "Field for field" comparison with `deploy/dev/whoami.json`

- **Decision**: the test decodes `deploy/dev/whoami.json` straight from the lab folder
  (`../../deploy/dev/whoami.json`; Go runs tests with the package folder as working directory)
  into `struct{ Job *api.Job }` with `DisallowUnknownFields`, then compares it with the
  translator's job using `reflect.DeepEqual`. On mismatch, it prints both as indented JSON.
- **Rationale**: decoding into the real Nomad type means every field the reference sets must be
  set identically, and every field it leaves out must be left out (nil or zero). A trial decode
  succeeded: every key in the file is a real `api.Job` field. Reading the lab's file itself
  means the lab and the test can't drift. `reflect.DeepEqual` follows pointers, so it compares
  values, not addresses.
- **Alternatives**: a `testdata/` copy (rejected: two files to keep in sync); comparing raw JSON
  text (rejected: the reference is hand-formatted and omits nulls, so whitespace and nulls would
  differ); `go-cmp` for nicer diffs (rejected: a new dependency for test output only).

## R4. Golden file format

- **Decision**: each golden file is the exact Nomad register body,
  `json.MarshalIndent(struct{ Job *api.Job }{job}, "", "  ")` plus a trailing newline, compared
  byte for byte. `go test ./internal/nomadjob -update` rewrites them.
- **Rationale**: the golden file is exactly what feature 003 will send, so it can be posted to
  the lab unchanged (quickstart). Marshaling `api.Job` prints every field, nulls included (about
  200 lines for whoami), which is verbose but honest. `encoding/json` sorts map keys (env,
  config, meta), which makes the bytes deterministic.
- **Alternatives**: strip nulls and zeros to make goldens look like `whoami.json` (rejected: a
  pruning step can hide a meaningful zero and tests something other than what's sent).

## R5. Where validation lives

- **Decision**: two layers, both returning the same error type.
  - `app.Validate(a App, d Deployment) error`: rules about the model that hold for any
    orchestrator (names, ports, domains, hosts, health check presence and ports, env names,
    replicas, negative values, digest format, deployment ID and trigger).
  - `nomadjob.Translate` first validates `Settings`, then calls `app.Validate`, then checks Nomad
    limits after applying defaults: CPU ≥ 1 MHz and memory ≥ 10 MB (`structs.MinResources`),
    check interval and timeout ≥ 1 s (`minCheckInterval`, `minCheckTimeout`), timeout shorter
    than interval.
- **Rationale**: constitution VI keeps Nomad-specific knowledge in `nomadjob`. The defaults
  (10 s / 2 s checks, environment CPU and memory) live in `nomadjob`, so rules that depend on
  them have to as well.
- **Alternatives**: all rules in `app` (rejected: Nomad limits leak into the model); all rules in
  `nomadjob` (rejected: compose input in 009 must reuse the model rules without Nomad).

## R6. Error type and field paths

- **Decision**: `app.FieldError{Field, Message string}`, whose `Error()` is
  `"<field>: <message>"`. Callers get the field with `errors.As`. Paths use camelCase names,
  list items by index and map entries by key: `services[0].domains[1].port`,
  `services[0].env[2].name`, `deployment.digests[web]`, `settings.entrypoint`. The full path
  list is in [contracts/translate.md](contracts/translate.md). Validation returns the **first**
  error, in a fixed order.
- **Rationale**: the API error body (constitution V) has one `field`, so one error is enough.
  camelCase matches common OpenAPI style and what the .NET client expects, and **feature 002
  must use the same JSON names** so `field` points at the request body. Index paths stay correct
  even when names are duplicated, which is exactly when they're needed.
- **Alternatives**: collecting all errors (rejected for now: a single-error API body, and more
  code for a first feature); name-based paths like `services[web]` (rejected: ambiguous for
  duplicate or empty names); `snake_case` (rejected: less common for this API's clients).

## R7. "Not set" vs zero

- **Decision**: `Replicas *int` (nil means 1, explicit 0 or less is an error). `Health
  *HealthCheck` (nil means no check). Everything else uses Go zero values as "not set":
  `Resources` 0 means the environment default, health `Interval`/`Timeout` 0 means 10 s / 2 s,
  empty `Command` means the image default.
- **Rationale**: the clarification needs replicas to tell omitted from 0, and a pointer is the
  plain Go way (C#: `int?`). Other fields don't need it: 0 CPU or a 0 s interval is never valid,
  so 0 can safely mean "use the default".
- **Alternatives**: `int` with 0 meaning default (rejected by the clarification); a separate
  `ReplicasSet bool` (rejected: clumsier than a pointer).

## R8. Env vars as an ordered list

- **Decision**: `Env []EnvVar` with `Name`, `Value`, turned into the task's `Env` map. No env
  means a nil map.
- **Rationale**: FR-033 rejects duplicate names, but a Go map can't hold duplicates, so the rule
  could never be tested. Also, Go's JSON decoder silently keeps the last of two duplicate
  object keys. A list keeps the input as written, gives index paths (`env[2].name`), and leaves
  ordering to `encoding/json`'s sorted map output.
- **Alternatives**: `map[string]string` (rejected: silently drops duplicates, which violates
  constitution IV).

## R9. Name and value rules

| Thing | Rule | Why |
| --- | --- | --- |
| App ID, service name | `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$` | RFC 1123 label; Nomad's service-name rule (`services.go`) is the same, case-insensitive |
| `<appID>-<service>` | ≤ 63 chars | it becomes a Nomad service name and Traefik router name |
| Port name | `^[a-z][a-z0-9_]{0,62}$` | Nomad has no label rule, but exposes `NOMAD_PORT_<label>` env vars, where `-` breaks shells |
| Container port | 1–65535 | |
| Host | lowercase labels of `[a-z0-9-]`, no leading/trailing `-`, each 1–63, total ≤ 253, no trailing dot, no wildcard | also stops backticks, spaces and `\|\|` from reaching the Traefik rule |
| Digest | `^(<registry host>[:port]/)?<path>@sha256:<64 lowercase hex>$`, path components `[a-z0-9]+` joined by `.`, `_`, `__` or `-` | the distribution reference grammar, minus tags: a reference with both tag and digest is rejected so nothing looks floating |
| Env name | non-empty, no `=`, no NUL | what `execve` env strings can carry |
| Display name | no control characters | Nomad rejects NUL in job names; other control characters would break logs and UI |
| Health path | starts with `/` | |
| Entrypoint, cert resolver | `^[A-Za-z0-9_-]+$` (resolver may be empty) | they end up inside Traefik tag values; a `,` would mean a list |
| Default CPU / memory | ≥ 1 MHz / ≥ 10 MB | Nomad's minimums |

## R10. Health check port in the output

- **Decision**: the service registration's `PortLabel` is the domains' port, or with no domains
  the check's port. The check's own `PortLabel` is set **only** when it differs from the
  registration's port.
- **Rationale**: `whoami.json` has no `PortLabel` on its check (Nomad then uses the service's
  port), and the "field for field" test needs that omission. A check on a different port than
  the routed one (e.g. a `metrics` port) still works because its label is set explicitly.

## R11. Router tags

- **Decision**: in this order, for router `r = <appID>-<service>`:
  `traefik.enable=true`, `traefik.http.routers.r.rule=Host(`a`) || Host(`b`)`,
  `traefik.http.routers.r.entrypoints=<entrypoint>`, `traefik.http.routers.r.tls=true`, and
  when a resolver is set, `traefik.http.routers.r.tls.certresolver=<resolver>`.
- **Rationale**: the first four match `whoami.json` exactly. The lab's Traefik runs with
  `providers.nomad.exposedByDefault=false`, so an internal registration with no tags is never
  routed. No `traefik.http.services.*` tags: with one router and one Nomad service, the
  provider links them itself (proven by the lab).

## R12. Submitting the output to the lab

- **Decision**: `deploy/dev/lab.sh check [job.json]`: the existing check gains an optional file
  argument (default `whoami.json`). The quickstart runs it with the whoami golden file.
- **Rationale**: the VM mounts the repo at the same path, so `lab-nomad job run -json <file>`
  works for any file in the repo. It reuses the existing wait-and-curl loop. This is a small
  script change, allowed without its own Spec Kit feature.
- **Alternatives**: `curl` to `/v1/jobs` from the Mac (works, but repeats the wait loop by hand).
