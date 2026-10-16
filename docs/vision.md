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
platform teams who found Kubernetes too heavy; Nomad users who lost Waypoint; AI builders who
want to run inference (Ollama, vLLM, diffusion models) on their own GPU servers instead of
renting per-second GPUs.
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
- Schedules more than containers: workers, cron, builds, and plain binaries (a Go or Rust
  release artifact, no image needed) on the same engine. Binaries run under Nomad's isolated
  `exec` driver, never `raw_exec`, which has no isolation and runs as root.
- GPUs are a scheduling resource, not a DevOps project. With NVIDIA's device plugin on a node,
  an app asks for "1 GPU with at least 24 GB" and Nomad places it on a node that has one. Swarm
  handles GPUs poorly; Kubernetes needs the NVIDIA operator plus taints and tolerations.
- Digest-pinned, health-checked deploys with automatic rollback.
- Cross-provider clusters over a WireGuard mesh.
- No lock-in: export any app as a plain Nomad job; uninstall us and workloads keep running.

## Roadmap

| Phase | Scope | Gate |
| --- | --- | --- |
| 1 · Engine (done 2026-10-04) | Image deploys, webhook redeploys, digest pinning, Traefik + HTTPS, single node | whoami live by webhook |
| 2 · Compose (done 2026-10-11) | Compose translator, env and secrets, databases + backups, HTMX UI | A real compose app runs |
| 2.5 · Day to day (now) | Notifications, organization / project / environment, build from source (Dockerfile) | A git-built app goes staging → production, and a failure reaches the owner |
| 2.75 · Aspire | Aspire integration (`PublishToCaravana`), Bella Baxter secrets | Bella Baxter deploys through `PublishToCaravana` |
| 3 · Cluster | Add node over SSH, WireGuard mesh, placement and drain, cluster-wide routing | 3 nodes, survive 1 loss |
| 3.5 · GPU + binaries | GPU nodes (driver + NVIDIA device plugin at node bootstrap), GPU requests in the app model, binary sources (`exec` driver + artifact download), AI templates (Ollama, vLLM, ComfyUI) | An Ollama template serves a model on a GPU node; a plain binary app deploys and rolls back |
| 4 · Platform | More build sources, more templates, teams and roles, managed Cloud tier | — |

## Why Nomad (for the "but who uses it?" question)

- Roblox chose Nomad over Kubernetes, DC/OS and Swarm. Its 2021 case study describes 11,000+
  nodes in 20 clusters across bare metal and cloud, including GPU and Windows workloads.
- Cloudflare runs services on Nomad across its edge network.
- Nomad schedules containers, binaries, VMs and GPUs with one scheduler, in one binary.

Where the AI angle doesn't make us unique: dstack, SkyPilot and GPUStack already orchestrate
GPUs on your own servers. They're built for ML teams running training and inference. Our angle
is one PaaS where the web app, its database, its workers and its model server deploy the same
way, on the same small cluster.

## Business model

Open core, per-server pricing: free self-hosted; paid managed control plane (users bring their
own VMs) and team/enterprise features. Nomad's BSL terms need legal review before a paid managed
offering; keep the orchestrator behind an interface.