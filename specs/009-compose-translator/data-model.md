# Data Model: Compose translator

## app.SourceKind (changed)

Gains `SourceCompose = "compose"`. `ValidateApp` accepts `image` and `compose`; both describe
services by prebuilt images, so nothing downstream reads the kind.

## app.Volume (new)

| Field | JSON | Rules |
| --- | --- | --- |
| `Name` | `name` | `^[a-z0-9][a-z0-9_-]{0,62}$`; unique across the app (one service per volume) |
| `Path` | `path` | absolute, cleaned, not `/` |
| `ReadOnly` | `readOnly`, omitted when false | |

`app.Service` gains `Volumes []Volume` (`volumes`, omitted when empty), after `Secrets`. A
service with a volume must have `Replicas` nil or 1. Errors: `services[i].volumes[j].name`,
`.path`, `services[i].replicas`.

## compose.Request (new, API input)

| Field | JSON | Rules |
| --- | --- | --- |
| `Compose` | `compose` | the file, required, at most 512 KiB |
| `Name` | `name` | optional display name; else the file's `name`; else none |
| `Services` | `services` | optional, `map[service name]Overlay`; each key must be a service in the file |

## compose.Overlay (new)

| Field | JSON | Becomes |
| --- | --- | --- |
| `Domains` | `domains: [{host, port?}]` | `Service.Domains`; `port` is a container port number, optional when the service has one port |
| `Health` | `health: {path?, port?, interval?, timeout?}` | `Service.Health`; `port` is a container port number |
| `Secrets` | `secrets: [{name, value?}]` | `Service.Secrets` (feature 008 semantics) |

Overlay error paths: `overlay.<service>.domains[k].port` etc.

## compose.Result

`Translate(appID string, req Request) (app.App, []app.Warning, error)`, pure. Errors are
`*app.FieldError` with Compose paths. Warnings are `app.Warning` with Compose paths.

## Nomad objects (in nomadjob / deploy)

| Object | Name | Who |
| --- | --- | --- |
| Internal registration | `<app>-<service>-internal` (alloc address, container port) | multi-service apps, each service with ports |
| Names sidecar task | `names` in every group | multi-service apps |
| Dynamic host volume | `<app>_<volume>` | each `app.Volume` |

## Update strategy per group

| Group | Canary | Replicas |
| --- | --- | --- |
| no volume | `count` (blue/green, as today) | any |
| with a volume | 0 (stop-then-start) | 1 |
