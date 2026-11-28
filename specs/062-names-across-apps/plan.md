# 062 Names across apps: apps of one environment reach each other by name

CodeAlta task `2xg2` (2026-11-26). The maintainer installed Postgres from its template and had no hostname to give the app that
should use it: service names resolved only inside one app (009), and only apps with two or more services had the names sidecar.
The network already lets any app reach any other app's port, but only at the copy's bridge address, which changes on every
restart. Nomad's own service catalog has no DNS (Consul would, and the constitution excludes it), so Caravana keeps resolving
names itself, with the same sidecar, now for the whole project › environment.

## Research (lab, 2026-11-27, Nomad 2.0.7 in the Lima VM)

- **R1** A job's template can list every registration of its namespace with `nomadServices` and read each with `nomadService`,
  using only the task's default workload identity: no policy line, for Caravana's token or the jobs.
- **R2** `nomadServices` gives each registration name once with the union of its tags; `scratch` is reset on every render;
  `regexMatch` and `regexReplaceAll` are available. Four jobs in two places and a consumer: the consumer's file held the names of
  its own place, not the other place's, a name two jobs claimed resolved to neither, and a name equal to the consumer's own name
  was left out. Purging one of the two claimants made the shared name resolve to the other within seconds, with no redeploy.

## Decisions

- **D1 Names.** Inside an app each service with ports keeps its bare name (009, Compose-like). Across the place each service with
  ports claims `<app>-<service>`, and an app with one service that has ports also claims `<app>`: a Postgres app named `postgres`
  with that one service is reached at `postgres` (or `postgres-postgres`). The PostgreSQL template's app also has Adminer, whose
  `expose` is a port, so it is reached at `postgres-postgres` only. `<app>` is the name part of the ID; an ID from before
  019 that `PlaceOf` refuses is its own name, in Default › production. A service without ports claims nothing.
- **D2 Collisions.** A name two services of the place claim resolves to neither, for every app, so a new app can't quietly take a
  name another uses. In an app's own copies its bare names win over another app's claim. An app's own claims never collide with
  each other (`<app>-<service>` differs per service; `<app>` is claimed only by a lone service). The rule is one pure function,
  `app.Addresses`, which the console uses; the job writes the same rule into its template.
- **D3 Registrations.** Every service with ports gets its internal registration (`<id>-<service>-internal`, alloc address, first
  port), with the tags `caravana-place=<project>/<environment>` and `caravana-name=<name>` per claim; the template reads names
  from the tags and never parses an ID. Canary tags are the same tags plus the canary tag (a canary's tags replace its tags).
  An internal name over 63 characters, now possible for one-service apps, keeps what fits and ends with 8 hex of its SHA-256;
  names that fit are unchanged, so no registration made before moves.
- **D4 The sidecar.** Every group of every app gets it, last of its tasks, and so does every schedule run: a worker without
  ports still reaches its database. The task brief said "every app with ports"; a consumer without ports is the common case
  (a worker, a migration), so it is every app. In the group of a service that is itself called `names`, the sidecar is
  `caravana-names` instead of refusing an app that deployed before (009 refused the name only in multi-service apps).
- **D5 The template.** Own services first by their bare names (as 009), then two passes over the place's registrations: the
  first marks each claimed name with its registration, or with `""` when a second claims it; the second writes each name whose
  mark is its own registration, unless it is one of the app's own bare names. Canaries are left out. `Translate` stays pure:
  what other apps exist is read live, so a new, moved or deleted app shows up without redeploying the others.
- **D6 Cost.** The sidecar reserves 16 MB and 20 MHz per group, as before (busybox `sleep`, under 1 MB resident). Single-service
  apps now pay it too: 20 one-service apps reserve 320 MB more. Each sidecar's template watches the namespace's list and the
  registrations of its place (blocking queries Nomad serves from memory). A per-node resolver would save the reservation but is a
  new moving part on every node (a DNS server, its port, `resolv.conf` of every copy); the sidecar stays unless the lab or a host
  shows a problem.
- **D7 API.** `getApp` and `getEnvironmentApp` answer `addresses` on the `AppRecord`: per service with ports, its container
  ports, the names other apps of the place reach it at, its own name for a multi-service app, and the conflicts (each name with
  the other app and service that claim it). The console's view counts the stored apps of the place that are defined and not
  stopped (a stopped app is deregistered). No new route; deploy tokens see it as they see `getApp`.
- **D8 Console.** Configure › Overview and the Running tab say, per service with ports, "Other apps in <project> › <environment>
  reach it at `postgres:5432`" (each name, with the first port) and, for a multi-service app, "Its own services reach it at
  `postgres:5432`"; a conflict says the name answers to nothing because the other app claims it too, on both apps. A template's
  installed notice says the address. The designer reviews the wording.
- **D9 Upgrade.** Apps get the registrations and the sidecar at their next deploy; until both apps of a pair are deployed again,
  the names between them don't resolve. The changelog's Upgrade section says so.
