# Implementation Plan: Domains

**Branch**: `023-domains` (stacked on `022-schedules`) | **Date**: 2026-10-24 |
**Spec**: [spec.md](./spec.md) | **Page contract**: [contracts/domains-screen.md](./contracts/domains-screen.md) |
**API contract**: [contracts/api.md](./contracts/api.md) | **Research**: [research.md](./research.md)

## Summary

One page for every address (`/domains`, and the same rows on each app's Domains page), each
with a state Caravana derives from two checks it runs itself: the host's DNS records against
this host's public addresses, and a TLS handshake to Traefik's own edge with the host as server
name (the certificate's issuer and expiry; Traefik's default certificate means pending or "its
own"). Per-domain options (`teamOnly`, `wwwRedirect`, `path`) live on `app.Domain`; a domain
with any option gets its own router `<app>-<service>-<hash8(host)>` (constitution 1.0.1), and
domains without options keep the service's router, so no existing job or golden changes. Team
only is basic auth whose bcrypt hash lives in the Nomad Variable `nomad/jobs/traefik`, rendered
by Traefik's file provider; the password is shown once. Two needs-you kinds, Settings ›
Certificates with the warn window, API 0.25.0, migration 0018.

## Technical Context

Go 1.27. **One new module**: `golang.org/x/crypto` (bcrypt for the Team-only hash; Traefik
accepts bcrypt htpasswd lines; the stdlib has no bcrypt). Storage: migration `0018_domains.sql`
(`domain_checks`, `team_passwords`, `certificate_settings`). Nomad: no policy change (the
variable path `nomad/jobs/*` is already writable; Traefik's task reads its own job's variable).
**Installer and lab**: Traefik's job gains a file-provider directory and a template that renders
the Team-only middlewares from the variable (one Traefik restart on existing hosts); the
installer writes `CARAVANA_ACME` and `CARAVANA_ACME_EMAIL` into Caravana's env. New settings:
`CARAVANA_EDGE_ADDRESS` (default `127.0.0.1:443`; the lab `127.0.0.1:8443`), `CARAVANA_ACME`,
`CARAVANA_ACME_EMAIL`. Testing: pure classification tables, `Translate` goldens per option, the
HCL export golden, fakes for the resolver and the TLS dialer, service, API (contract), UI, a
`TestConsoleLab` step `console-domains-states` and a gate step `domains`.

## Constitution Check

| Principle | Assessment |
| --- | --- |
| I. Small | PASS with one justified module (`x/crypto`, Complexity Tracking). No runtime service: the checks are Caravana's own dials with short timeouts. |
| II. One Model | PASS. `app.Domain` gains three optional fields; `Translate` stays pure (the per-domain router is a function of the domain's options). A Compose app's overlay carries the same fields. |
| III. Safe Deploys | PASS. An option change is a normal deploy with `If-Match`; the variable for Team only is written before `Register` and the middleware name is stable, so a router never names a missing middleware. |
| IV. Fail Loudly | PASS. Every refused option names its field; a check that fails says "couldn't check", never a wrong state; a host two apps claim is said. |
| V. API-First | PASS. Seven operations in openapi 0.25.0 with the console on the same service methods; options travel on `deployApp`/`deployCompose`; deploy tokens denied by default deny. |
| VI. Nomad-Native | PASS under 1.0.1: routing still only through service tags; the service's router stays `<app>-<service>`, a domain with options gets `<app>-<service>-<hash8>`. The export writes the extra routers and the middleware tags; the header names the Traefik file-provider middleware a Team-only domain needs. |
| VII. Secure | PASS. The password is generated, shown once, never stored; its bcrypt hash is in SQLite (as token hashes are) and in `nomad/jobs/traefik`, never in a job, export, dashboard, record, log or answer (sentinel tests). The TLS probe sends nothing after the handshake; the DNS probe asks only resolvers; the edge address is fixed, never derived from a host. |
| VIII. Plain Go | PASS. Pure state table, pure tag builder, table tests; fakes through small interfaces for the resolver and the dialer (a second implementation: the tests). |

## Decisions carried (and where the research and the contract differed)

- **Options** (the contract over research R4's richer model): `Path` (a prefix, kept; no strip
  flag), `WWWRedirect bool` (the domain is the target; its pair, `www.<h>` or `<h>`, redirects
  here and is listed as a `redirect` row), `TeamOnly bool` (one shared password per domain,
  user `team`). More users and a strip option are later features.
- **The per-domain router** is named `<app>-<service>-` + the first 8 hex of SHA-256 of the
  host, on the same Nomad registration (no `service` tag); its rule is ``Host(`h`)`` (``&&
  PathPrefix(`/p`)`` with a path; ``|| Host(`o`)`` with a redirect pair), `entrypoints`, `tls`
  and `certresolver` as the base router, `middlewares=<R>-auth@file,<R>-www` in that order.
  Tags: `traefik.http.middlewares.<R>-www.redirectregex.regex=^https://<o with [.]>(:[0-9]+)?/(.*)`,
  `.replacement=https://<h>$1/$2`, `.permanent=true` (the lab spike: Traefik matches the regex
  against `https://<Host header><URI>`, and the Host header may carry a port; a GET answers
  301, other methods 308). `$1`, `$2` and `[.]` because Nomad interpolates `${…}` in tags. The
  base router's rule drops the hosts that moved to their own routers.
- **Team only**: `team_passwords(host, app_id, hash, created_at)` in SQLite (the hash only; a
  bcrypt verifier, as API tokens are SHA-256 verifiers); `deploy.SyncTeamPasswords` rewrites the
  variable `nomad/jobs/traefik` (one item per middleware, `<R>-auth` = `team:<hash>`) from the
  table before `Register` of an app with a Team-only domain, after a rotation, and after a
  remove or app delete; Traefik's template renders it into `/local/dynamic/team.yml`
  (`http.middlewares.<name>.basicAuth.users`, `removeHeader`, `realm`). A missing middleware
  disables the router (fails closed, 404). Rotation writes the new hash and the variable with no
  deploy. The password: 24 bytes of randomness, base64url, shown once in the console's 200
  answer and once in the API's deploy answer (`teamPassword`) or rotate answer.
- **Certificate state** (R2): `edge.Probe` dials `CARAVANA_EDGE_ADDRESS` with SNI = host,
  `InsecureSkipVerify` for the handshake, then verifies by hand: names, chain against system
  roots. Classes: `issued` (trusted, names match), `pending` (Traefik's default cert, resolver
  set), `self` (default cert, no resolver), `expiring` (issued, `NotAfter − now ≤ warn window`),
  `error` (dial failed or a chain that doesn't verify and isn't Traefik's). `renewsAt` =
  `NotAfter − 30 d`.
- **DNS** (R3): the system resolver plus `1.1.1.1:53` and `8.8.8.8:53` in parallel, 2 s each;
  `ok` when every answering resolver includes one of this host's addresses (AAAA included), `elsewhere`
  when any answered addresses that don't (a stale AAAA counts), `none` on NXDOMAIN from all,
  `unknown` when none answered or this host's addresses aren't known. This host's addresses:
  `CARAVANA_PUBLIC_URL`'s host resolved (IPv4 and IPv6), cached as the Add form's `hostIP` is
  (moved into the shared helper). `*.localhost` → loopback → `ok` in the lab.
- **State precedence** (the contract): `not_served` (app stopped or no healthy deployment) →
  `dns_elsewhere` → `cert_expiring` → `cert_pending` → `dns_none` → `self_signed` → `redirect`
  (the pair's redirecting host) → `live` → `unknown`. One pure function, table-tested.
- **Cache and watcher**: `domain_checks(host PK, app_id, service, state, dns_status, dns_seen
  JSON, dns_expected JSON, cert_status, cert_issuer, cert_not_after, checked_at)`. A watcher
  (`domainwatch.go`) checks every stored domain every 15 min, and every 60 s while a `/domains`
  or an app's Domains stream is open (the Running tab's usage precedent), at most 8 probes at
  once; `checkDomain` runs fresh and writes the row. `Attention` reads the table only.
- **Issuer settings** (R5): `CARAVANA_ACME` (`production|staging|none`) and
  `CARAVANA_ACME_EMAIL` from the env, read-only; `certificate_settings(id=1, warn_before_days)`
  default 14 (7, 14, 30). The Settings nav: ◆ when any `cert_expiring`, else the count of
  `issued`.
- **Kept-file apps**: `compose.Domain` gains `path`, `wwwRedirect`, `teamOnly`; the Domains
  page edits the kept request's overlay (as `editSecrets` rewrites secret names) and deploys
  with `If-Match`; the page no longer refuses a kept-file app.
- **Port**: unchanged, per service; Configure's Deploy panel gains "Domains go to" when a
  service has several ports (sets `Domains[].Port` for all its domains).
- **Lab checks first** (research's list): a short spike in the lab before the translator is
  finalized: the middleware and router tags listed by Traefik, two routers on one registration,
  the redirect's status code, a file-provider middleware re-rendered from a changed variable,
  HTTP-01 through a basic-auth router. Their outcomes go into research.md as "lab, observed".

## Design

### `internal/app`
- `Domain{Host, Port, Path, WWWRedirect, TeamOnly}` (the three new, `omitempty`); `Domain.HasOptions()`;
  `Domain.RedirectPair() string` (`www.<h>` ↔ `<h>`). `validateDomains`: `Path` starts with `/`,
  ≤ 200 bytes, no backtick, space, `||`, `&&`; `WWWRedirect` only when the host is an apex (one
  dot) or starts with `www.`, and the pair isn't a domain of this or any app (the service checks
  across apps); errors name `services[i].domains[j].<field>`.

### `internal/nomadjob`
- `routerTags` splits: the base router gets the hosts without options; each domain with options
  gets `optionRouterTags(app, svc, d, s)` (name `DomainRouterName(appID, svc, host)`, the rule,
  the middleware tags). Goldens: `domain-path.golden.json`, `domain-www.golden.json`,
  `domain-team.golden.json`, `domain-all.golden.json`; the existing goldens unchanged. `HCL`
  writes the tags as strings already; its golden gains the four cases; the export header names
  the file-provider middleware for a Team-only domain.

### `internal/edge` (new, pure seams)
- `Resolver` interface (`LookupIPAddr`), `Dialer` interface (a TLS dial), `Check(ctx, host,
  expected []netip.Addr, resolverSet bool, warn time.Duration) Facts` with `DNS{Status, Seen,
  Expected}` and `Certificate{Status, Issuer, NotAfter, RenewsAt}`; `StateOf(facts, appServed,
  redirecting bool) State`. The real resolver set (system + two public) and the TLS dialer live
  here; tests use fakes. No Nomad import.

### `internal/store` (migration `0018_domains.sql`)
- `domain_checks`, `team_passwords`, `certificate_settings` per [data-model.md](./data-model.md);
  `UpsertDomainCheck`, `ListDomainChecks`, `DeleteDomainChecks(appID)`; `SetTeamPassword`,
  `TeamPasswords()`, `DeleteTeamPassword(host)`; `CertificateSettings`, `SetWarnBeforeDays`.
  `DeleteApp` deletes both per-app tables' rows.

### `internal/deploy`
- `SyncTeamPasswords(ctx, entries map[string]string)` writes `nomad/jobs/traefik` (items
  `<R>-auth` = `team:<hash>`; an empty set keeps a marker item, as 017 does); called by the
  service before `Register` and after rotate/remove. deploytest records the variable.
- `ExportHeader` lists the Team-only middleware file.

### `internal/service`
- `Domains(ctx, filter) []DomainView` (stored apps + statuses + the cached facts), `Domain(host)`,
  `CheckDomain(ctx, host, appID?)` (fresh; for a host not yet served: DNS plus "issued after you
  add it"), `RotateTeamPassword(ctx, appID, host) (password, error)`, `CertificateSettings`,
  `SetWarnBeforeDays`, `Certificates()`; the watcher `watchDomains` (15 min / 60 s while
  watched, 8 parallel, `ChangeDomains` notice); `deployWarned` → when a domain turns Team only
  without a password, generate one, store the hash, sync the variable, return the password once
  (`DeployResult.TeamPasswords map[host]password`); on remove/delete, drop the row and sync.
  Attention kinds `domain_dns_elsewhere`, `domain_cert_expiring` from `domain_checks`, key
  `kind|app|service|host`, `notify.Event` gains `domain`, `addressSeen`, `expiresAt`.
  `ErrDomainTaken{App}` (409 `domain_taken`), `ErrNotTeamOnly` (409 `not_team_only`).

### API 0.25.0 (`internal/api/domains.go`)
Per [contracts/api.md](./contracts/api.md): `listDomains`, `getDomain`, `checkDomain`,
`rotateTeamPassword`, `getCertificateSettings`, `putCertificateSettings`, `listCertificates`;
`Domain` options on `App.services[].domains[]`; `DeployResult.teamPasswords` (shown once);
`AttentionItem.kind` gains the two kinds with `domain`; `NotificationEvent` gains `domain`,
`addressSeen`, `expiresAt`. Deploy tokens: denied by the default deny; the scope walk covers the
new routes; `deployApp` with `teamOnly` on its own app is allowed and its answer carries the
password once (the token holder can already deploy anything about the app).

### Console (`internal/ui/domainspage.go`, `templates/domains.html`, `settings.html`)
Per the page contract: `/domains` (head, filter, count line, banners, the table of `<details>`
rows, the detail per state, the Options form with switches, Save and deploy, Remove…, New
password, the Add panel with the record box and Check now), `/parts/domains` on the `domains`
notice, the app's Domains page using the same template filtered to the app with "All domains",
the sidebar's Domains item (`globe`), Settings › Certificates (the table, the read-only issuer
rows, Edit with Warn when), Configure's "Domains go to" select, the needs-you rows for the two
kinds, the clock helper for expiry times. Styles "Page 6 (023)", `tools/ui-css.sh build`,
DESIGN.md.

### Installer and lab
- `deploy/install/traefik.nomad.hcl`: `--providers.file.directory=/local/dynamic` (the existing
  `caravana.yml` template moves into it), a second template `team.yml` ranging the variable
  `nomad/jobs/traefik` (`change_mode = "noop"`; the file provider watches the directory). The
  installer writes `CARAVANA_ACME`, `CARAVANA_ACME_EMAIL` and `CARAVANA_EDGE_ADDRESS` into
  Caravana's env; rerunning it restarts Traefik once (said in the output and the CHANGELOG's
  Upgrade note). `deploy/dev/traefik.nomad.hcl` the same; `lab.sh up` re-registers it; `startLab`
  sets `CARAVANA_EDGE_ADDRESS=127.0.0.1:8443` and injects the lab resolver.
- `TestConsoleLab` step `console-domains-states` and the gate step `domains` per
  [quickstart.md](./quickstart.md) §6.

## Complexity Tracking

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| A new Go module, `golang.org/x/crypto` (bcrypt) | Traefik's basic auth takes htpasswd hashes; bcrypt is the one strong format it accepts, and the stdlib has none | apr1-MD5 (40 lines of stdlib) is weak against guessing; SHA-1 htpasswd likewise. `x/crypto` is already an indirect dependency of the module graph. |
