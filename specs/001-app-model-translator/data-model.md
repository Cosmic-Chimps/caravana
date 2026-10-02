# Data Model: App Model and Nomad Job Translator

Phase 1 output for [plan.md](plan.md). Types are Go; `internal/app` unless noted. Validation
rules reference [research.md R9](research.md#r9-name-and-value-rules). Error field paths are
listed in [contracts/translate.md](contracts/translate.md).

No state transitions: this feature has no lifecycle. Deployment status belongs to feature 003.

## App

One project; one Nomad job.

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `ID` | `string` | yes | DNS label. Job ID, and the `<appID>` in service and router names. |
| `Name` | `string` | no | Display name; job name. Empty means `ID`. No control characters. |
| `Source` | `Source` | yes | Only `SourceImage`. Validated, never read by the translator. |
| `Services` | `[]Service` | yes, ≥ 1 | Order is kept: task groups appear in this order. Names unique. |

## Source

| Field | Type | Notes |
| --- | --- | --- |
| `Kind` | `SourceKind` (`string`) | Constant `SourceImage = "image"`. Empty or anything else fails. |

The image itself lives on each `Service`. Compose and git kinds come back with features 009 and
Phase 4; they aren't declared yet (FR-003).

## Service

One container; one task group with one Docker task, both named `Name`.

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `Name` | `string` | yes | DNS label; `len(appID) + 1 + len(Name) ≤ 63`. |
| `Image` | `string` | yes | The user's reference (`traefik/whoami:latest`). Never written into the job: the deployment's digest is. No whitespace. Feature 003 resolves it. |
| `Command` | `string` | no | Docker `command`. Empty means the image's default. |
| `Args` | `[]string` | no | Docker `args`. Passed even without `Command`. |
| `Env` | `[]EnvVar` | no | Becomes the task `Env` map; none means nil. Names unique. |
| `Ports` | `[]Port` | no | Names unique. |
| `Domains` | `[]Domain` | no | Every domain uses the same `Port`. |
| `Replicas` | `*int` | no | nil means 1. Set means ≥ 1. |
| `Resources` | `Resources` | no | Zero fields mean the environment default. |
| `Health` | `*HealthCheck` | if `Ports` non-empty | nil means no check. Not allowed without ports. |

## EnvVar

| Field | Type | Notes |
| --- | --- | --- |
| `Name` | `string` | Non-empty, no `=`, no NUL. |
| `Value` | `string` | Any text. Plain config only; secrets come with feature 008. |

## Port

| Field | Type | Notes |
| --- | --- | --- |
| `Name` | `string` | `^[a-z][a-z0-9_]{0,62}$`. Becomes the Nomad port label. |
| `ContainerPort` | `int` | 1–65535. Becomes the dynamic port's `To`. |

## Domain

| Field | Type | Notes |
| --- | --- | --- |
| `Host` | `string` | Lowercase hostname; unique across the whole app. |
| `Port` | `string` | Required; must name a declared port of the same service. |

HTTPS is always on, so there is no TLS flag (HTTP-only domains are out of scope).

## HealthCheck

| Field | Type | Notes |
| --- | --- | --- |
| `Path` | `string` | Empty means a TCP check; set means an HTTP check, must start with `/`. |
| `Port` | `string` | Empty means: the domains' port; with no domains, the only port; several ports and no domains is an error. Set means it must be declared. |
| `Interval` | `time.Duration` | 0 means 10 s. Negative fails (app); under 1 s fails (nomadjob). |
| `Timeout` | `time.Duration` | 0 means 2 s. Negative fails (app); under 1 s, or ≥ the interval after defaults, fails (nomadjob). |

## Resources

| Field | Type | Notes |
| --- | --- | --- |
| `CPUMHz` | `int` | 0 means `Settings.DefaultCPUMHz`. Negative fails (app); 1 is Nomad's minimum. |
| `MemoryMB` | `int` | 0 means `Settings.DefaultMemoryMB`. Negative fails (app); under 10 fails (nomadjob). |

## Deployment

One go-live event (`internal/app/deployment.go`).

| Field | Type | Notes |
| --- | --- | --- |
| `ID` | `string` | Required. Written to job meta `caravana.deployment`; it changes on every deploy, so every deploy is a new job version. |
| `Trigger` | `Trigger` (`string`) | `TriggerManual = "manual"` or `TriggerWebhook = "webhook"`. Not written into the job. |
| `Digests` | `map[string]string` | Service name → `repo@sha256:<64 hex>`. Exactly one per service: missing and extra entries both fail. Extra keys are checked in sorted order, so the first error is deterministic. |

## FieldError and Warning

`internal/app/errors.go`.

| Type | Fields | Notes |
| --- | --- | --- |
| `FieldError` | `Field`, `Message string` | Implements `error`: `Error()` returns `"<field>: <message>"`. Returned as `*FieldError`; callers use `errors.As`. |
| `Warning` | `Field`, `Message string` | Supported input whose meaning changed. Defined now, never produced in Phase 1. |

## Settings (`internal/nomadjob`)

Per-installation values, supplied by the control plane's config (feature 002), never by an app.

| Field | Type | Lab value | Rule |
| --- | --- | --- | --- |
| `Entrypoint` | `string` | `websecure` | Required, `^[A-Za-z0-9_-]+$`. |
| `CertResolver` | `string` | `""` | Empty means Traefik's default certificate; otherwise same pattern as the entrypoint. |
| `DefaultCPUMHz` | `int` | `100` | ≥ 1. |
| `DefaultMemoryMB` | `int` | `64` | ≥ 10. |

## Result (`internal/nomadjob`)

| Field | Type | Notes |
| --- | --- | --- |
| `Job` | `*api.Job` | Ready to register. |
| `Warnings` | `[]app.Warning` | Always empty (nil) in Phase 1. |

## Mapping to the Nomad job

| Model | Nomad (`api.Job`) |
| --- | --- |
| `App.ID` | `ID` |
| `App.Name` (or `ID`) | `Name` |
| (constant) | `Type: "service"`, `Datacenters: ["*"]` |
| `App.ID`, `Deployment.ID` | `Meta{"caravana.app", "caravana.deployment"}` |
| (constant) | `Update{MaxParallel 1, HealthCheck "checks", MinHealthyTime 10s, HealthyDeadline 3m, ProgressDeadline 10m, AutoRevert true, AutoPromote true}` (blue/green: changed in feature 003, see its research R15) |
| `Service` | `TaskGroups[i]{Name, Count, Update{Canary = Count}, ShutdownDelay 5s, Networks, Services, Tasks}` |
| `Replicas` | `Count` (nil → 1) |
| `Ports` | `Networks[0]{Mode "bridge", DynamicPorts[{Label, To}]}`; empty `DynamicPorts` for workers |
| `Domains` / internal ports | `Services[0]{Name "<appID>-<service>", PortLabel, Provider "nomad", Tags}`; with domains also `CanaryTags ["caravana.canary=true"]` |
| `Health` | `Services[0].Checks[0]{Name "health", Type http/tcp, Path, PortLabel only if ≠ registration port, Interval, Timeout}` |
| `Name`, digest, `Command`, `Args` | `Tasks[0]{Name, Driver "docker", Config{image, command?, args?}}` |
| `Env` | `Tasks[0].Env` (nil when empty) |
| `Resources` + `Settings` defaults | `Tasks[0].Resources{CPU, MemoryMB}` |
