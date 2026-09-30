# Vision

**The PaaS that grows from one cheap VM to twenty, without ever making you touch Kubernetes.**

Push code or an image, get a URL with HTTPS, sleep well. Start on one box today, add a node with
one click tomorrow, and never rewrite your setup to grow.

## Problem

There is a missing middle between "one server with Docker" and "Kubernetes":
- Swarm-based PaaS tools (Dokploy, CapRover) hit Swarm's ceiling: basic scheduling, awkward
  multi-node volumes, an orchestrator in maintenance mode.
- Coolify manages servers independently; multi-server means islands, not one pool.
- Kubernetes is the wrong size: expensive in memory, money and attention.
- Polished platforms (Porter) run Kubernetes inside AWS/GCP/Azure, not on budget VMs.

## Who it's for

Indie hackers and small SaaS teams on budget VMs; agencies hosting many client projects; small
platform teams who found Kubernetes too heavy; Nomad users who lost Waypoint.
Not (yet) for: service mesh, multi-region failover, compliance-first teams.

## Principles

1. **Small by default.** One Go binary + SQLite, one-command install, fits on a 2 GB VM.
2. **Compose in, cluster out.** Users bring an image, compose file or repo; never write jobspecs,
   but can always export one.
3. **Growing is a button, not a migration.** One node to three changes nothing about app definitions.
4. **Secure without a manual.** WireGuard between nodes, Nomad TLS and ACLs, secrets in Nomad
   Variables, from the first install.
5. **Boring where it matters.** Databases pinned to a node with real backups; every deploy a
   versioned, revertable job.

## What makes us different

- A real scheduler: bin-packing, constraints, spreading replicas, node drain.
- Light enough for the smallest (including ARM) VMs.
- Schedules more than containers: workers, cron, builds on the same engine.
- Digest-pinned, health-checked deploys with automatic rollback.
- Cross-provider clusters over a WireGuard mesh.
- No lock-in: export any app as a plain Nomad job; uninstall us and workloads keep running.

## Roadmap

| Phase | Scope | Gate |
| --- | --- | --- |
| 1 · Engine (now) | Image deploys, webhook redeploys, digest pinning, Traefik + HTTPS, single node | whoami live by webhook |
| 2 · Compose | Compose translator, env and secrets, databases + backups, HTMX UI | A real compose app runs |
| 3 · Cluster | Add node over SSH, WireGuard mesh, placement and drain, cluster-wide routing | 3 nodes, survive 1 loss |
| 4 · Platform | Git sources + builds, templates, teams and roles, managed Cloud tier | — |

## Business model

Open core, per-server pricing: free self-hosted; paid managed control plane (users bring their
own VMs) and team/enterprise features. Nomad's BSL terms need legal review before a paid managed
offering; keep the orchestrator behind an interface.