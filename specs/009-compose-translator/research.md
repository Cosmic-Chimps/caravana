# Research: Compose translator

## R1. Loading a Compose file

**Decision**: `github.com/compose-spec/compose-go/v2` v2.16.1, `loader.LoadWithContext` over
`types.ConfigDetails{ConfigFiles: [{Filename: "compose.yaml", Content: src}]}` with:
`SetProjectName(appID, true)`, `ResolvePaths = false`, `SkipResolveEnvironment = true`,
`SkipResolveLabels = true`, `SkipInclude = true`, `SkipExtends = true`, validation and the
consistency check kept on. Variables come from a lookup over an empty environment that records
every name it was asked for without a default; any recorded name fails the translation, naming
the variable (FR-007). logrus, which compose-go logs warnings to, is sent to `io.Discard` once in
the package; its warnings (unset variable, obsolete `version`) are covered by our own rules.

**Rationale**: the official parser, so YAML anchors, short and long syntax, merge keys and
interpolation behave exactly as in `docker compose`. Loading from bytes with these options reads
no file and no OS variable (verified: `NOT_SET=leak` in the OS environment stayed empty). It is
pure Go and cross-compiles. The schema validation gives path-bearing errors
(`services.web.imagee`).

**Alternatives considered**: a hand-written YAML decoder (would get Compose semantics wrong);
`cli.ProjectFromOptions` (built around disk files and the OS environment).

## R2. Honored, warned, refused

**Decision**: `loader.WithSupportedAttributes(allowed, report)` with an explicit allowlist of
attribute paths. Every reported attribute is an error naming its path (FR-004), the first in
sorted order so the same file always gives the same error. Warned attributes are in the
allowlist and produce their warning from the loaded project. The defaults compose-go fills in
before the check (`ports.[].mode`, `ports.[].protocol`, `volumes.*.name`,
`volumes.[].volume`, `networks.*.name`) are allowlisted and their values checked afterwards
(`protocol` must be `tcp`).

| Path | Becomes | Note |
| --- | --- | --- |
| `name` | display name, when the overlay has none | |
| `version` | nothing | obsolete in Compose itself |
| `services.*.image` | `Image` | required |
| `services.*.command` | `Command` = first word, `Args` = the rest | |
| `services.*.environment` | `Env` | a name without a value is refused |
| `services.*.ports[]` | `Ports` (`target`; `name` if a valid port label, else `p<target>`) | `published`, `host_ip` → warning; `udp` refused |
| `services.*.expose[]` | `Ports`, internal | |
| `services.*.volumes[]` of type `volume` | `Volumes` | bind, tmpfs, image, npipe refused; `volume.nocopy` refused |
| `services.*.deploy.replicas`, `services.*.scale` | `Replicas` | |
| `services.*.deploy.resources.limits.memory`, `mem_limit` | `Resources.MemoryMB` | rounded up to whole MB |
| `services.*.deploy.resources.limits.cpus`, `cpus` | `Resources.CPUMHz` (×1000) | warning: Nomad shares CPU by MHz |
| `services.*.healthcheck` | TCP or overlay check | warning unless `disable: true` |
| `services.*.depends_on` | nothing | warning: services start together |
| `services.*.restart` | Nomad's restart policy | `no` and `on-failure` → warning |
| `services.*.container_name`, `hostname` | nothing | warning: Nomad names containers |
| `services.*.labels` | nothing | warning: routing comes from the overlay |
| `services.*.networks`, top-level `networks` | nothing | warning unless only `default`: all services of an app see each other |
| `services.*.pull_policy` | nothing | warning: every deploy pins the digest |
| `services.*.logging` | nothing | warning: Nomad keeps the logs |
| `services.*.stop_grace_period` | nothing | warning: Nomad's kill timeout applies |
| top-level `volumes.*` | the app's volumes | `driver`, `driver_opts`, `external`, `labels` refused |
| `x-*` anywhere | nothing | Compose ignores them too |

Everything else (`build`, `entrypoint`, `env_file`, `secrets`, `configs`, `user`,
`working_dir`, `privileged`, `cap_add`, `devices`, `extra_hosts`, `dns`, `profiles`, `include`,
`extends`, `network_mode`, `links`, ...) is refused, with a hint for the common ones.

**Rationale**: an allowlist fails closed. A field added to the Compose spec tomorrow is refused
until someone decides what it means here.

## R3. Services by name

**Decision**: in an app with two or more services:

- Each service with ports gets a second Nomad registration, `<app>-<service>-internal`,
  `provider = "nomad"`, `address_mode = "alloc"`, port = its first port, with
  `canary_tags = ["caravana.canary=true"]` and no checks. It answers with the allocation's
  bridge IP and the container port (lab: `172.26.64.4:8080`).
- Each group gets a task `names` (lifecycle prestart, sidecar), image `Settings.NamesImage`
  (busybox pinned by digest), 20 MHz and 16 MB. Its template renders, per other service with
  ports, one line `<ip> <service>` for every registration not tagged `caravana.canary=true`,
  into `local/names`. On change, its script writes the saved original hosts file plus that file
  over `/etc/hosts`. Its own command does the same once at start, after saving the original
  into `local/base`, then waits.
- Because the internal registration adds `-internal` to the name, multi-service apps need
  `len(app)+len(service)+10 <= 63` (checked in `nomadjob.Check`, naming the service).

**Rationale**: verified in the lab. During a canary rollout the dependents keep the old
address; after promotion `/etc/hosts` showed the new address within seconds and the app task
had zero restarts. A dependency that isn't registered yet simply doesn't resolve, and nothing
fails. The task must not be named `hosts`, because its directory would collide with the
allocation's `hosts` file. The script writes in place (`cat >`), which keeps Docker's bind mount.

**Alternatives considered**: `extra_hosts` with template env (stale after the first redeploy:
Nomad writes the allocation's hosts file once); bind-mounting a hosts file (`Duplicate mount
point: /etc/hosts`); Consul DNS (constitution VI).

**Limit**: allocation IPs are routable on one node. Multi-node (Phase 3) needs routes across the
WireGuard mesh, or a switch to host addresses with published ports.

## R4. Ports and health

**Decision**: port labels come from the Compose port's `name` if it is a valid label, else
`p<target>`. The overlay's domains and health check name the port by its container port number.
With one port, it can be left out. A service with ports and no overlay health check gets
`HealthCheck{}` (TCP on the first port). A Compose healthcheck that this replaces gives a
warning.

**Rationale**: Nomad's native checks are HTTP or TCP only. A `CMD` test can't run as a Nomad
check, and parsing `curl` lines would be clever, not plain.

## R5. Volumes

**Decision**: `app.Service.Volumes []Volume{Name, Path, ReadOnly}`. Volume names are
`^[a-z0-9][a-z0-9_-]{0,62}$`, each mounted by one service, and the path must be absolute. Each
becomes a Nomad dynamic host volume named `<app>_<volume>`. App IDs never contain `_`, so the
first `_` always separates app from volume. It is created with plugin `mkdir`, parameter
`mode: "0777"`, capability single-node-single-writer / file-system. `deploy.Register` first
ensures every volume exists and is `ready` (create if absent, poll until ready within the
deploy's budget). The group's volume request uses the same access and attachment mode; the
task mounts it at `Path`. A group with a volume gets `Canary: 0` (stop-then-start), and the
model refuses `Replicas` > 1.

Delete: after the job is purged, `DeleteVolumes` lists host volumes with prefix `<app>_` and
deletes each. It retries "in use" until the purged allocations let go, for up to 60 s.

A volume removed from the definition stays until the app is deleted, and the deploy warns about
it.

**Rationale**: verified in the lab. Create takes about 10 s to `ready`; data survives a purge
and rerun; a canary can't be placed on a single-writer volume (it hangs at "requires manual
promotion"); canary 0 stops the old allocation before starting the new one. `0777` because
mkdir makes a root-owned 0700 directory, and non-root images (unlike Docker named volumes,
which copy the image's ownership) couldn't write to it.

**Alternatives considered**: static `host_volume` blocks in the client config (need a client
restart for every new volume); single-node-multi-writer (two database processes on one
directory corrupt it); keeping volumes on app delete (data nobody can see or remove).

## R6. Scoped policy

**Decision**: the `default` namespace gains `host-volume-create` (implies read) and
`host-volume-delete`. A top-level `host_volume "*" { policy = "write" }` lets Caravana's jobs
mount the volumes it created, and `host_volume "traefik-acme" { policy = "deny" }` keeps them off the
host's own `traefik-acme`. (The first draft was `"*_*"`; Nomad allows no `_` in host volume names, so
the shipped policy in `deploy/install/caravana-policy.hcl` is the wildcard plus the deny, commit 139e3e0.) The gate's `compose-app` step
proves it on a real host.

## R7. API

**Decision**: `POST /v1/apps/{id}/compose` (`deployCompose`, 202, a Deployment) and
`POST /v1/apps/{id}/compose/preview` (`previewCompose`, 200, `{app, warnings}`). Body:
`{compose: string, name?: string, services?: {<name>: {domains?, health?, secrets?}}}`. Errors
use the Compose path as `field` (`services.web.build`), or `overlay.<name>.<field>` for the
overlay. The file is at most 512 KiB inside the existing 1 MiB body limit.

**Rationale**: JSON keeps the contract generator-friendly for the Aspire client. Preview makes
the translation visible without risk. Two operations instead of a `oneOf` on `deployApp`, which
generated clients handle badly.
