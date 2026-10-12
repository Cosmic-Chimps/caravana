# Research: Memory per service

## R1. Reservation and ceiling in Nomad

**Decision**: a task's `Resources.MemoryMB` is the reservation and `Resources.MemoryMaxMB`
(`memory_max`) is the ceiling. Nomad's scheduler counts the reservation when it places
allocations. The Docker driver lets the container use up to the ceiling while the node has memory
spare, and the kernel kills it past the ceiling.

**Rationale**: it is Nomad's own model for this (memory oversubscription), and it needs no
change to the job's shape beyond one field.

`memory_max` only works when the cluster's scheduler configuration has
`MemoryOversubscriptionEnabled`. When it is off, Nomad accepts the job and answers with the
register warning *Memory oversubscription is not enabled; Task "g.t" memory_max value will be
ignored* (checked in the lab). `deploy.Register` already copies Nomad's warnings into the
deployment (`nomad: …`), so FR-010 needs no code, only a test.

## R2. Turning bursting on

**Decision**: the installer runs `nomad operator scheduler set-config
-memory-oversubscription=true` with the management token, after the ACL bootstrap, when
`get-config` says it is off, and records `scheduler` in `changed:`. `deploy/dev/lab.sh up` does
the same for the lab. Caravana's own scoped policy doesn't change: it never needs to read or
write the scheduler configuration, because Nomad's warning reports it.

**Alternatives considered**: `default_scheduler_config` in the server config. It only applies
when a cluster bootstraps, so it would miss every existing host, including the real one that
upgrades to `v0.1.1`.

## R3. The model and the defaults

**Decision**:

- `app.Resources.MemoryMB` (JSON `memoryMB`) keeps its name and now means the reservation.
- `app.Resources.MemoryMaxMB` (JSON `memoryMaxMB`, new) is the ceiling.
- Validation refuses `memoryMaxMB` below 10, and `memoryMB > memoryMaxMB` when both are set,
  naming `services[i].resources.memoryMB`.
- The translator computes:
  - reservation = `MemoryMB`, else `DefaultMemoryMB`, lowered to `MemoryMaxMB` when only the
    ceiling is set (FR-006)
  - ceiling = `MemoryMaxMB`, else the larger of the reservation and `DefaultMemoryMaxMB`
- `nomadjob.Settings.DefaultMemoryMaxMB` is new. The configuration gets
  `CARAVANA_DEFAULT_MEMORY_MAX_MB` (default 1024) and `CARAVANA_DEFAULT_MEMORY_MB`'s default moves
  from 64 to 256.
- The `names` sidecar and the backup and restore jobs keep their fixed small reservations,
  without a ceiling: they are Caravana's, with known needs.

**Rationale**: no client breaks. An app that set `memoryMB` could use exactly that before; now it
may use up to the default ceiling, which only loosens the limit. The translator stays pure.
Changing the defaults is configuration, so the golden files only gain `MemoryMaxMB`.

## R4. Compose

**Decision**:

- `mem_reservation` and `deploy.resources.reservations.memory` set `MemoryMB`.
- `mem_limit` and `deploy.resources.limits.memory` set `MemoryMaxMB`. Today `mem_limit` sets
  `MemoryMB`.
- The allowlist gains `services.*.mem_reservation`, `services.*.deploy.resources.reservations` and
  `services.*.deploy.resources.reservations.memory`. Other reservations (CPUs, devices) stay
  refused, naming the path.

**Rationale**: it is what the two Compose fields mean in Docker: a soft reservation and a hard
limit. Read that way, Hollo's example (`mem_limit: 768m`) gets a 768 MB ceiling and reserves
256 MB, which places more apps on one node.

## R5. Console and API

**Decision**: the definition panel's **Memory** row shows `256 MB reserved, up to 1024 MB`. A
value left at zero shows as `default`, as other rows do. `api/openapi.yaml` goes from 0.9.1 to
0.10.0: `Resources.memoryMaxMB` is added, and `memoryMB` is described as the reservation. It is
an additive change.

## R6. Release and the real upgrade

**Decision**: when the feature is merged, the maintainer tags `v0.1.1`. On the netcup host, the
same install command upgrades:

- Caravana's binary, with the data copied first
- the scheduler setting (`changed: scheduler`)

Hollo keeps running on its old job until its next deploy, which then gets the ceiling. The
runbook's upgrade line already describes this. The installer test in Lima covers the setting
on a fresh VM and on an upgrade.
