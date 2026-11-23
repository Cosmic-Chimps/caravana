# Implementation Plan: Change the console's address from the console

**Branch**: `alta/loyal-wombat-v69b` | **Spec**: [spec.md](spec.md) | **Contracts**: [api](contracts/api.md),
[agent](contracts/agent.md), [page](contracts/page.md)

## Summary

The owner checks a new domain on Operator › Console address; Caravana checks the name and DNS before recording
anything, then puts the domain beside the current one in Traefik's console route (a file Traefik watches, rendered
from a Nomad Variable Caravana writes) and waits for the route and its certificate. On Switch, the server agent that
runs beside Caravana (031, root) gets the request in its sync answer, rewrites the public URL and the installer's
domain, restarts Caravana and reports the outcome. The old domain stays routed for the redirect window: Caravana
redirects its pages and serves its `/v1/` and `/.well-known/`.

## Decisions

- **D1 The root side is the node agent, not a request file.** The agent is already root, on a 30 s timer, beside
  Caravana on the first server, and checks every value of the console's answer before it acts (`validate.go`). The
  request is a member of the sync answer (`consoleAddress {id, domain}`) and the outcome a member of the report, as
  045's clean-up is. No file in Caravana's data directory is read by root, so there is no file to craft; the crafted
  input is a crafted answer, refused by validation. 030's updater stays the updater. The agent writes exactly two
  lines of two fixed files and restarts exactly one unit.
- **D2 The console route follows a Nomad Variable, without restarting Traefik.** `traefik.nomad.hcl`'s
  `caravana.yml` template reads the item `hosts` of `nomad/jobs/traefik/traefik` (the job's own group path, which its
  workload identity reads with no policy line) and falls back to `var.console_domain` when there is none; one router
  per host (`caravana`, `caravana-1`, …, so each host gets its own certificate and a pending one can't hold up the
  current one's renewal), one service `caravana`; `change_mode = "noop"` as `team.yml`. Caravana writes the variable
  (its policy covers `nomad/jobs/*`). Caravana could already re-register Traefik's job, so no power moves; the route
  stays a static route in Traefik's configuration (constitution VI). A Traefik job from before this feature (its
  template doesn't name the variable) is detected by reading the job, and a change is refused with
  `console_address_unsupported`: rerun the installer once (Traefik restarts once, as for 023, 025 and 047).
  Caravana writes the variable only once a change exists; before that the job's variable rules, so nothing changes
  on a host that never uses the feature. The hosts are always: the public URL's host, then the open change's domain
  while it is checking, ready or switching, then each old domain whose window hasn't ended.
- **D3 The old domain is served, not only redirected.** For `redirectDays` (default 30, one of 0, 7, 30, 90) after the
  switch the old domain stays in the route and keeps its certificate (Traefik renews what a router names). Caravana's
  outer handler answers a request whose host is an old domain: `/v1/…` and `/.well-known/…` are served as before (CI
  sends a bearer token, which curl drops on a redirect to another host, and GitHub doesn't follow a webhook redirect;
  joined agents and Bella read discovery there), everything else is 308 to the same path and query on the new address.
  When the window ends a pass takes the domain out of the route. 0 days takes it out at the switch.
- **D4 Checks.** Synchronous, before anything is recorded: the host name (the installer's rule: dot-separated labels
  of letters, digits and dashes, none at either end, at least one dot, at most 253 bytes, lower-cased; not an IP; not
  the current domain; not an app's domain, `domain_taken`), and DNS with `edge.Check` against `hostAddresses` (must be
  `ok`; `*.localhost` is ok as everywhere). Asynchronous, after the route is written: the route (an HTTPS
  `GET /v1/health` through the edge address with the domain as SNI and Host answers 200 from Caravana) and the
  certificate (`edge.Check`'s certificate is `issued`/`expiring`; with `CARAVANA_ACME=none` Traefik's own is
  accepted), every 10 s for at most 5 minutes. The switch re-runs DNS, route and certificate once, without waiting.
- **D5 DNS records (048).** When DNS says `none` and a connected zone covers the domain, the refusal says so; a check
  with `createDNSRecord: true` makes an A per IPv4 and AAAA per IPv6 address of `hostAddresses` through the zone's
  provider with the comment `caravana:console`, then goes on without waiting for public resolvers (Let's Encrypt asks
  the authoritative servers). No `dns_records` row: no pass ever removes it.
- **D6 States.** A change is `checking` → `ready` | `check_failed`; `ready` → `switching` | `cancelled`;
  `checking`/`check_failed` → `cancelled`; `switching` → `applied` | `failed` | `refused` | `lost`. One change open
  (checking, ready, switching) at a time (partial unique index). `applied` is decided by Caravana itself at start
  (its public URL's host is the change's domain) or by the agent's report; `failed`/`refused` by the report; `lost`
  after 5 minutes switching. A `checking` change found at start is `check_failed` "Caravana restarted during the
  check".
- **D7 Which agent.** Only an agent that reports `runsConsole: true` (its node env URL is loopback and
  `/etc/caravana/caravana.env` exists) gets `consoleAddress`. None reporting in the last 10 minutes: the switch is
  409 `no_agent` (045's code).
- **D8 Cluster.** Every sync answer carries `consoleURL` (the public URL when it is https). A joined agent whose node
  env URL isn't loopback and differs switches to it after `GET <consoleURL>/v1/health` answers 200 (with its usual TLS
  setting). The old domain serves `/v1/` for the window, so a joined server that was off at the switch catches up
  within it; after it, rerun its installer with `--url`.
- **D9 sslip.io.** No special case; the page shows this server's `<a-b-c-d>.sslip.io` (from `hostAddresses`' IPv4)
  as the address to go back to.
- **D10 Passkeys and sessions.** 033's rule already keeps password sign-in on at a domain with no passkey; a test
  proves it for the switch. The page says passkeys stay with the old domain and that the owner signs in again.
- **D11 Issuer.** It follows the public URL (038's `issuerFor`, one Nomad server at a time). The page warns that what
  trusts the old issuer needs the new one.
- **D12 Apps.** An app's domain equal to the current, pending or an old console domain is `domain_taken`.

## Constitution check

- I Small: no new module, no new runtime dependency. II: no app model change. III: deploys untouched. IV: every
  refusal names `domain`. V: API and page share `service.ConsoleAddress*`; openapi 0.57.0 in the same commits.
  VI: the console route stays a static route of Traefik's file provider. VII: no secret involved; root acts only on
  validated values. VIII: table tests, agent unit tests, install-test.

## Phases

- **A — service, store, Traefik route, redirect** (Go, no UI): migration `0041_console_address.sql`,
  `store` CRUD, `deploy.ConsoleRoute` (read support from the Traefik job, write `hosts`), `service/consoleaddress.go`
  (checks, change lifecycle, the pass, settle at start and from reports), the outer redirect handler, D12, both
  `traefik.nomad.hcl` changes (install only; the dev lab has no console route), installer message.
- **B — the agent**: `Answer.ConsoleAddress`, `Answer.ConsoleURL`, `Report.RunsConsole`, `Report.ConsoleAddress`,
  `State.ConsoleAddress`, apply on the console's server, URL follow on joined servers; service sends/reads them.
- **C — API and console**: the five operations (openapi 0.57.0, scope walk), Operator › Console address,
  `docs/console-address.md`, `changelog.d/console-address.md`.
- **D — lab**: `install-test.sh` `check_console_address_host`; `TestConsoleLab` step `console-address` (the lab's
  refusal and the page), CLAUDE.md.

## Research to do in the lab

- R1 The template: how `nomadVar` exposes one item (`index . "hosts"`), `split`, and that a `noop` re-render is
  picked up by the file provider with no restart. Verified on install-test's VM in phase D (the dev lab's Traefik has
  no console route).
