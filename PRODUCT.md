# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Stack

- Server-rendered Go: `html/template` + HTMX, every asset embedded in the binary with `embed`.
  No SPA, no Node, no runtime dependency (constitution, Technology Constraints).
- CSS: Tailwind CSS v4 **standalone CLI** (a single binary, no Node) with daisyUI 5's plugin
  files and the handoff's `theme/caravana.css`. The generated stylesheet is committed, so
  `go build` and CI work without the tool installed.
- The handoff's React + Vite + `lucide-react` instructions don't apply here. Its `.jsx` files are
  reference for layout, copy and behavior only.
- Open for 007: how the Tailwind binary gets pinned and checksum-verified (it can't use Go's
  `tool` directive), SSE vs. HTMX polling for live status, self-hosted IBM Plex font files, and
  Lucide icons as embedded SVG.

## Users

The primary user is a developer who self-hosts: an indie hacker, a small SaaS team or an agency,
running their apps on one or a few budget VMs (Hetzner, OVH, Contabo, a small EC2). On a single
node the same person is both the developer and the operator. They open the console to ship an
app, check whether a deploy worked, find out why something broke, and fix it. Most of the time
they're glancing at it between other work, not watching it.

API clients (the .NET Aspire `PublishToCaravana` integration, CI webhooks) are first-class users
of the same service layer, but they don't use the UI.

## Product Purpose

caravana is a self-hosted PaaS on HashiCorp Nomad: push an image (later a compose file or a repo)
and get a URL with HTTPS. It grows from one cheap VM to twenty without Kubernetes and without
rewriting app definitions. The console is the human front door. Success means the user deploys
without fear, sees right away whether things are healthy, and when they aren't, reads in plain
words what happened, what caravana already did about it, and the one action that fixes it.

## Positioning

Built on a real scheduler (Nomad) instead of Docker Swarm or Kubernetes, small enough for a 2 GB
VM, with no lock-in: any app can be exported as a plain Nomad job, and uninstalling caravana
leaves workloads running. Every deploy is pinned to a digest, health-checked, and reverts on its
own when it fails. The console reports what Nomad really did, translated into plain language.

## Operating Context

- The console is served by the caravana binary itself, at a domain on the user's own VM, behind
  the admin login (feature 006). Nomad and the API stay on loopback; only 22/80/443 are public.
- Deploys start from the console, from the API, or from a CI webhook. The console must show
  deploys it didn't start.
- Live state comes from Nomad: deployments, allocations, rollouts, logs.
- The design has two views in one shell. **Developer**: apps, deploys, config, logs, domains.
  **Operator**: servers, capacity, scheduling, platform.

## Capabilities and Constraints

- **Built (Phase 1):** image deploys pinned to a digest (Docker Hub, GHCR), blue/green rollouts
  with canaries hidden from Traefik until they're promoted, health checks with auto-revert,
  per-app webhook redeploys, HTTPS through Traefik and Let's Encrypt, single node.
- **Phase 2, in order:** 006 minimal auth (one admin login, API tokens), 007 HTMX UI v1 (app list
  and detail, deploy button, deployment history with live status, logs), 008 secrets, 009
  compose, 010 databases + backups, 011 rollback + export, 012 one-command install.
- **The design is a north star, built in phase order.** Each feature builds only the screens its
  phase can back with real data. 007 is the Developer view for what the engine does. 006 is a
  single-admin sign-in, not SSO or 2-step. Orgs, members, previews, multi-node Operator screens,
  pools, updates and git builds wait for Phase 3/4. No screen is ever mocked or shows placeholder
  data as if it were real.
- The UI calls only `internal/service`. No feature exists only in the UI (constitution V).
- Terminology: plain words in sentences ("copies", not "allocations"; "room for about 4 more
  apps", not "unreserved capacity"). Nomad terms (alloc, eval, node pool) appear only in mono
  detail lines. App, service, node and version names are literal and mono: `whoami`, `v24`.
- Secret values are never shown, only key names. Tokens are shown once.

## Brand Commitments

- **Name:** lowercase **caravana** in the UI wordmark and UI copy. Docs, README and code comments
  keep "Caravana". There's no logo: the wordmark is the word itself. Don't draw a mark.
- **Voice** (from the handoff's design-system README):
  - Open every overview with one headline sentence about the present: "3 apps healthy, 1 rollout
    in progress."
  - Explain failures with what happened and what caravana did. Never "Error · 0 s".
  - Actions start with a verb and say the outcome: "Revert to v22", "Raise to 2 GB". Never "OK"
    or "Submit".
  - Talk to the user in the second person ("needs you"). caravana is third person.
  - Calm, factual and short. Sentence case. No exclamation marks, no emoji.
- **Binding visual reference:** the Claude Design handoff "caravana console", Handoff 2,
  2026-10-04 (`design/console/`). It's high fidelity, with final copy.
  DESIGN.md will record it. An earlier light "field notebook" direction was rejected.
- **Originality:** no code, UI, copy, names or branding taken from Dokploy, Coolify or other PaaS
  projects (constitution, Development Workflow). It must not look like Dokploy.

## Evidence on Hand

- The design handoff in `design/console/`: the screens, components, tokens, theme mapping and copy.
- Sample data in its `data.js` (apps like `traveler-web`, servers like `hel-1`, `fsn-1`) is
  illustrative and can't be shown as real.
- The Phase 1 gate passed on 2026-10-04 on a real VM (`deploy/vm/host.sh prove`).
- There are no users, customers, testimonials, benchmarks, pricing or press. Don't make any up.

## Product Principles

1. **Show only what's true.** The console shows what the engine and Nomad really report. A
   screen ships when its phase can back it, not before.
2. **The UI is just another client.** Anything the console can do, the API can do, through the
   same service layer.
3. **Explain, don't just report.** Every problem comes with what happened, what caravana already
   did, and one action that fixes it.
4. **Calm until it matters.** Healthy is quiet. A problem is impossible to miss.
5. **Small by default.** The console ships inside the one binary and adds no services to run.

## Accessibility & Inclusion

- Status is never shown by color alone: every status has a shape as well as a color (● healthy,
  ◐ rolling, ◆ warning, ■ failed, ○ stopped), and the palette never relies on telling red from
  green.
- Motion respects reduced-motion settings. Keyboard focus is always visible.
- No formal WCAG level has been set yet.
