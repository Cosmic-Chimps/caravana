# Contract: `nomadjob.Translate` and `app.Validate`

The interface this feature exposes to feature 003 (deploy flow) and, through it, to the API
error body. It's an in-process Go API: no HTTP endpoint, so `api/openapi.yaml` is unchanged.

## Signatures

```go
// package app
func Validate(a App, d Deployment) error // nil, or *FieldError

// package nomadjob
type Settings struct {
	Entrypoint      string
	CertResolver    string
	DefaultCPUMHz   int
	DefaultMemoryMB int
}

type Result struct {
	Job      *api.Job
	Warnings []app.Warning
}

func Translate(a app.App, d app.Deployment, s Settings) (Result, error) // error is *app.FieldError
```

Guarantees:

- **Pure**: no I/O, no clock, no randomness, no globals. It doesn't modify its inputs.
- **Deterministic**: equal inputs give `reflect.DeepEqual` results and byte-identical JSON.
- **All or nothing**: on error, `Result` is the zero value (`Job == nil`).
- **First error wins**: one `*app.FieldError`, from the order below.
- **Warnings**: always `len == 0` in this feature.

## Validation order

`Translate` checks in this order and stops at the first failure:

1. `Settings`: entrypoint, certResolver, defaultCPUMHz, defaultMemoryMB.
2. `app.Validate`:
   1. `id`, `name`, `source.kind`, `services` (at least one)
   2. each service in order: `name` (label, duplicate, combined length), `image`, `replicas`,
      `resources`, `env[]`, `ports[]`, `domains[]` (host, app-wide duplicate, port declared, same
      port), `health` (required, path, port, negative durations)
   3. `deployment.id`, `deployment.trigger`, a digest for each service in order, then extra
      digest keys in sorted order
3. Nomad limits, each service in order: resources after defaults, then health interval and
   timeout after defaults.

## Error catalogue

`Field` is the exact path the test asserts. `i`, `j` are zero-based indexes; `<svc>` is a
service name. Messages are indicative; tests assert `Field` and that the message is non-empty.

| Field | Fails when | FR |
| --- | --- | --- |
| `settings.entrypoint` | empty or bad characters | FR-019 |
| `settings.certResolver` | bad characters (empty is fine) | FR-019 |
| `settings.defaultCPUMHz` | < 1 | FR-019 |
| `settings.defaultMemoryMB` | < 10 | FR-019 |
| `id` | not a DNS label | FR-024 |
| `name` | contains a control character | — |
| `source.kind` | not `image` | FR-002 |
| `services` | empty | FR-001 |
| `services[i].name` | not a DNS label; duplicate; `<id>-<name>` > 63 chars | FR-024 |
| `services[i].image` | empty or contains whitespace | FR-020 |
| `services[i].replicas` | set to ≤ 0 | FR-032 |
| `services[i].resources.cpuMHz` | negative (any set value ≥ 1 already meets Nomad's 1 MHz minimum) | FR-032 |
| `services[i].resources.memoryMB` | negative (app); < 10 after defaults (nomadjob) | FR-032 |
| `services[i].env[j].name` | empty, contains `=` or NUL, duplicate | FR-033 |
| `services[i].ports[j].name` | bad label, duplicate | FR-025 |
| `services[i].ports[j].containerPort` | outside 1–65535 | FR-025 |
| `services[i].domains[j].host` | not a lowercase hostname; used earlier in the app | FR-028 |
| `services[i].domains[j].port` | empty or not declared; differs from `domains[0].port` | FR-026, FR-027 |
| `services[i].health` | nil while ports exist; set while no ports exist | FR-029, FR-030 |
| `services[i].health.path` | non-empty and not starting with `/` | FR-031 |
| `services[i].health.port` | not declared; empty with several ports and no domains | FR-030 |
| `services[i].health.interval` | negative (app); < 1 s after default (nomadjob) | FR-031 |
| `services[i].health.timeout` | negative (app); < 1 s or ≥ interval after defaults (nomadjob) | FR-031 |
| `deployment.id` | empty | FR-023 |
| `deployment.trigger` | not `manual` or `webhook` | FR-023 |
| `deployment.digests[<svc>]` | missing for a service; not `repo@sha256:<64 hex>`; key is not a service of the app | FR-021, FR-022 |

Domains need ports, so a service with domains and no ports fails at `domains[j].port` (the port
isn't declared), before the health check rules run.

## Consumers' obligations (features 002 and 003)

- The API request body uses the same camelCase names as these paths, so `field` points at it.
- The API maps `*app.FieldError` to `400` with `{"error": {"code": "invalid_field", "message",
  "field"}}`. The exact code is settled in feature 002.
- `Result.Warnings` goes into the deploy response as a JSON array (`[]`, not `null`).
