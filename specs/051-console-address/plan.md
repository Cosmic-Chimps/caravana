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

## Deviations (phase A)

- **D12 wording.** An app domain equal to a pending or old console domain is refused with the existing
  console-domain `*app.FieldError` (400 `invalid_field` on the domain's field), the same refusal the current
  domain already gets in `checkConsoleDomain`, not a 409 `domain_taken`: `DomainTakenError` carries the other
  *app's* ID and every page words it "is already a domain of <app>". `domain_taken` (409, field `domain`) is the
  answer the other way round: the change's new domain is an app's (`StartConsoleAddressChange`). The pending and
  old hosts come from an in-memory cache (`consoleRouted`, refreshed by every reconcile and by Start) so the
  synchronous app checks need no store read.
- **One host-name rule.** `mesh.ValidConsoleDomain` (lower case `validIssuerHost` without `localhost`) is the rule
  the service checks `domain` with and the agent will check the answer with (phase B), as `mesh.CleanIssuer` is for
  the issuer. The installer's `is_hostname` is the same rule in upper and lower case.
- **The agent is found from the stored reports.** `consoleAgentSeen` reads each server's last report and its
  `SeenAt` (a report with `runsConsole` heard from in the last 10 minutes), so a restart of Caravana doesn't forget
  it. No in-memory state. `ConsoleAddressView.CannotChange` never mentions the agent: its sentence is for an
  installation that can't change at all (no https public URL, no Traefik, Traefik job from before this feature);
  the missing agent is `Agent: false` and the switch's `ConsoleNoAgentError` (`errors.Is(err, ErrNoAgent)`, 409
  `no_agent`).
- **The variable is the route only once the feature was used.** A host that never had a change writes nothing
  (the Traefik job's `console_domain` rules). With a change open or applied the variable holds the hosts: the
  current one first, the open change's domain and, while it is *switching*, also its old domain (after the agent's
  restart the current host is the new one and the old one must keep answering until the change is settled), then
  each old domain whose window hasn't ended. After a cancelled, failed or lost change with nothing ever applied the
  variable is deleted again. After an applied change it always stays, even with 0 days, because the job's own
  `console_domain` is still the old domain until the installer runs again.
- **The route template** also falls back to `var.console_domain` when the variable has no `hosts` item, and renders
  `{}` with neither; `cmd/caravana/install_test.go` renders the template through Go's `text/template` with fakes of
  `nomadVarExists`, `nomadVar` and `split` (the priority test now looks for `{{ $host }}` in the rule).
- **A long reason from the agent isn't refused.** The report check validates the ID and the state only; a reason is
  cleaned to one printable line and cut at 300 bytes when it is kept (`cleanConsoleReason`), so an agent can't be
  stuck on a 422 it would repeat at every sync.
- **`redirectDays`** is an `int` the caller fills (`DefaultConsoleRedirectDays` = 30 when the owner said nothing),
  because 0 is a real choice ("Don't keep it").
- **The check goroutine** counts its poll and limit in real time (`time.After`, `time.Now`), not the service's
  clock, which tests move; `consoleAddr.poll` and `.limit` are the fields tests shorten.
- **Not done in phase A, for B and C.** `internal/api/nodecalls.go`'s `syncNode` answer doesn't carry `consoleURL`
  and `consoleAddress` yet (phase B adds them with the agent, with `api/openapi.yaml`); no route, no page, no
  `docs/console-address.md`.
- **R1, lab (2026-11-23, a throwaway job in the dev lab).** `nomadVar` yields a map of `dependency.NomadVarItem`,
  a struct that prints as its value: `{{ $hosts = index . "hosts" }}` then `eq $hosts ""` failed to render
  ("incompatible types for comparison: dependency.NomadVarItem and string") and the task was killed, which on a host
  would have stopped Traefik at the first change. The template reads `{{ with index . "hosts" }}{{ $hosts = printf
  "%s" . }}{{ end }}`; with it the file re-rendered with `noop` and no restart for a new list, and fell back to
  `console_domain` for a variable without `hosts` and for a deleted variable. The Go test's fake now returns struct
  items, so the old form fails there too.

## Deviations (phase B)

- **Where the agent's step is.** `applySync` step 1c, after the issuer (1b) and before the role, the renewal and the
  clean-up. The issuer first, because it follows the console's address by itself once Caravana runs under it (the
  e2e test sees the Nomad server restart for the new issuer at the sync after the change); before the role,
  because a promotion returns early and would delay the change by a sync; before the renewal and the clean-up,
  because a clean-up takes minutes and the console waits a few for the outcome. `applyConsole` returns the state
  and never an error, so a failure is an outcome or a line on stderr and the other steps run (a test checks a
  clean-up still runs after a failed change and that the state it saves keeps the outcome).
- **The domain is kept in `state.json`, not reported.** `ConsoleOutcome.Domain` (`json:"domain,omitempty"`) is
  stored so a request found `running` after a crash is settled: the report's member has `id`, `state` and `reason`
  only. The settling *finishes* the change when `caravana.env` already names the domain (it is written first):
  `CARAVANA_DOMAIN` is added to the installer's record, Caravana is restarted and waited for, and the outcome is
  `applied`, or `failed` when it doesn't answer (the old files can't be put back: their values weren't kept). One
  that doesn't name the domain was never changed: `failed` "The agent stopped during the change." This is a step
  beyond the contract's "applied if the env file names it and Caravana is healthy", which could report `applied`
  for a Caravana still running under the old address.
- **"Loopback" is the console's own listener.** Both `runsConsole` and follow use `http://127.0.0.1:7070` exactly
  (a trailing slash is ignored), not any loopback host, so a joined server pointed at another local port follows
  and the first server never does.
- **What refuses the whole answer.** An invalid `consoleURL` (not empty, not `https://host[:port]` by
  `mesh.CleanIssuer`) refuses the answer like a bad issuer, in `validateCommon`. `consoleAddress` is never refused
  at parse: a bad domain or an agent that doesn't run the console is the `refused` outcome, a bad ID is ignored
  with one line (the answer's other steps go on).
- **File rules.** Caravana's env file must have exactly one `CARAVANA_PUBLIC_URL=` line (else `refused`, files
  untouched); unreadable or unwritable files are `failed`; a missing `caravana.env` means this agent doesn't run
  the console (`refused`); a missing `install.env` is `failed` (the agent doesn't create the installer's record);
  every `CARAVANA_DOMAIN=` line is set and one is appended when there is none. A line's own ending (`\n`, `\r\n`)
  and everything else are kept byte for byte; the temporary file gets the original's mode, owner and group.
- **Health seam.** `Agent.Health` (nil: GET `http://127.0.0.1:7070/v1/health` through the agent's client, 200);
  the wait is 60 looks one second apart through `Agent.Sleep`, so tests don't wait.
- **API.** `syncNode`'s answer carries `consoleURL` and `consoleAddress`; `NodeReport` and `NodeSyncAnswer` in
  `api/openapi.yaml` list the new members; the version stays 0.56.0 (no test needed the bump).
