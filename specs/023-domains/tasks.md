# Tasks: Domains (023)

**Input**: `specs/023-domains/` (spec.md, plan.md, research.md, data-model.md,
contracts/api.md, contracts/domains-screen.md, quickstart.md)

**Tests**: required (constitution VIII): table tests beside the code, goldens for `Translate`
and the HCL export, fakes for the resolver and the TLS dialer, the API contract test against
`api/openapi.yaml`, sentinel searches for the password and the hash, a `TestConsoleLab` step
and a gate step.

**Order**: the lab spike first (its answers fix the tags), then the backend and the API, then
the console on real data, the installer and lab, docs, and the review cycle. A Sonnet agent
builds each phase; Opus reviews before the next.

## Phase 0: The lab spike (no story label)

- [x] T001 Run the research's six lab checks against the lab's Traefik (no Caravana change: a
  hand-registered Nomad job with the planned tags, and a throwaway Traefik job copy with a
  file-provider template over a variable): (1) middleware and router tags listed by
  `/api/http/middlewares` and `/api/http/routers` and the router `enabled`; (2) two routers on
  one registration without a `service` tag, and a `Host && PathPrefix` rule winning over the
  plain host router; (3) `redirectregex`'s permanent status code (301 or 308); (4) a file-provider
  middleware re-rendered from a changed variable without a restart, and a router naming a
  missing middleware disabled; (5) HTTP-01 through a basic-auth router (reasoned from Traefik's
  challenge handler; the real proof is the gate on a host); (6) Go's pure resolver answering
  `x.localhost`. Record each as "lab, observed" in `specs/023-domains/research.md` and adjust
  plan.md's tags if one differs. Clean every lab object up.

## Phase 1: Foundation (no story label; everything below needs it)

- [x] T002 [P] `internal/app/app.go`, `validate.go` + tests: `Domain.Path`, `WWWRedirect`,
  `TeamOnly` (`omitempty`), `Domain.HasOptions()`, `Domain.RedirectPair()`; validation per
  plan.md (path shape and length; `wwwRedirect` only on an apex or `www.` host; errors name
  `services[i].domains[j].<field>`); a test that an app without options marshals as before.
- [x] T003 [P] `internal/nomadjob/translate.go` (+ `domains.go` if cleaner) + goldens:
  `DomainRouterName(appID, service, host)` (8 hex of SHA-256), `routerTags` keeping only the
  hosts without options on the base router, `optionRouterTags` per domain with options (the
  rule with `PathPrefix` and the redirect pair's `|| Host`, `entrypoints`, `tls`,
  `certresolver`, `middlewares=<R>-auth@file,<R>-www`, the `redirectregex` tags with `[.]` and
  `$1`); goldens `domain-path`, `domain-www`, `domain-team`, `domain-all`; every existing golden
  unchanged (a test asserts it); `hcl.go`'s golden gains the cases; `deploy/export.go`'s header
  names the Team-only middleware file. Adjust to T001's findings.
- [x] T004 [P] `internal/edge/` (new, pure): `Resolver` and `Dialer` seams, the real system +
  public resolvers (2 s each, parallel) and the TLS dialer (3 s, SNI, hand verification),
  `Check(ctx, host, expected, resolverSet, warn) Facts`, `StateOf(facts, served, redirecting)
  State`, the classification tables (Traefik's default cert → pending/self; trusted → issued;
  within warn → expiring; dial failed → error; DNS ok/elsewhere/none/unknown with a stale AAAA
  as elsewhere); `edgetest` fakes. Tables for every row of the precedence.
- [x] T005 [P] `internal/store/migrations/0018_domains.sql` and `internal/store/domains.go` +
  tests: `domain_checks`, `team_passwords`, `certificate_settings` per data-model.md;
  `UpsertDomainCheck`, `ListDomainChecks`, `DeleteDomainCheck(host)`, `DeleteDomainChecks(appID)`;
  `SetTeamPassword`, `TeamPasswords`, `DeleteTeamPassword`, `DeleteTeamPasswords(appID)`;
  `CertificateSettings`, `SetWarnBeforeDays`; `DeleteApp` deletes both per-app tables' rows.
- [x] T006 `internal/deploy/domains.go` + tests and deploytest: `SyncTeamPasswords(ctx,
  entries map[middleware]htpasswdLine)` writing `nomad/jobs/traefik` (a marker item when empty);
  `internal/config`: `CARAVANA_EDGE_ADDRESS` (`-edge-address`, default `127.0.0.1:443`),
  `CARAVANA_ACME`, `CARAVANA_ACME_EMAIL`; `go.mod`: `golang.org/x/crypto` direct (bcrypt, cost
  10; the commit justifies it per constitution I); `go mod tidy`.

## Phase 2: US1, every address with its state (P1)

Goal: `/v1/domains` and `/domains` list every domain with a derived, cached state.
Independent test: quickstart §1.

- [x] T007 [US1] `internal/service/domains.go` + tests: `DomainView`, `Domains(ctx,
  DomainFilter{App, Project})` from `StoredApps` + app statuses + `domain_checks` (sorted
  problems first; a redirect pair's redirecting host as a `redirect` row; `not_served` for a
  stopped or undeployed app; "two apps claim this address" said), `Domain(host)`,
  `CheckDomain(ctx, host, appID)` (fresh via `edge.Check`, upserts the row; for a host not
  served: DNS and the records to create, certificate "none"), `hostAddresses()` (the public
  URL's host resolved, IPv4 and IPv6, cached as the Add form's lookup is: move `hostIP` out of
  the UI), `ChangeDomains` notice.
- [x] T008 [US1] `internal/service/domainwatch.go` + tests: every stored domain every 15 min,
  every 60 s while a domains stream is open (`SetDomainWatchGaps` setters for the lab), at most 8
  probes at once, each result upserted, `ChangeDomains` published; stopped or undeployed apps
  not probed; the injectable resolver and dialer (`Service.SetEdge(edge.Resolver, edge.Dialer)`)
  for the lab.
- [ ] T009 [US1] `internal/api/domains.go` + `api/openapi.yaml` 0.25.0 + tests: `listDomains`
  (`?app=&project=&fresh=`), `getDomain`, `checkDomain` (`no-store`), the `Domain` and
  `DomainCheck` schemas, codes `domain_not_found`; `App.services[].domains[]` gains the three
  fields; the contract test; the scope walk expects 403 on the new routes.
- [ ] T010 [US1] Console: `internal/ui/domainspage.go`, `templates/domains.html`, the
  sidebar's Domains item (`globe` in `icons.go`, active on `/domains*`), routes `GET /domains`,
  `GET /parts/domains` (live on the `domains` notice; the stream marks the page as watched), the
  app's Domains page (`domains.go`) using the same template filtered with "All domains"; the
  head, filter, count line, banners (3 then "and N more"), the table of `<details>` rows per
  the contract (Domain with the lock icon and path, Status pill, Goes to, Certificate, the
  open link), the detail's left column per state with the facts grids, the footer, the empty
  state; "Check again" (`POST /domains/{host}/check`); `ui/parity_test.go` entries. Handler
  tests per state and a sentinel test that no hash or password appears.
- [ ] T011 [US1] Styles "Page 6 (023)" in `internal/ui/style/input.css` (the table columns,
  the detail grid, the switches, the banner, the record box, narrow rules), `tools/ui-css.sh
  build`, DESIGN.md's Domains section (the states' marks and words, the time rule for expiry).

## Phase 3: US2, per-domain options (P1)

Goal: Team only, the www redirect and a path, saved with a deploy, from the console and the API.
Independent test: quickstart §2.

- [x] T012 [US2] `internal/service/deploy.go`, `domains.go` + tests: on a deploy, for each
  Team-only domain without a hash generate the password (24 random bytes, base64url), bcrypt it,
  `SetTeamPassword`, and `SyncTeamPasswords` before `Register` (also after any deploy that
  drops the option or the domain: delete the row and sync); the deploy result carries
  `TeamPasswords map[host]password` once; `RotateTeamPassword(ctx, appID, host)` (new hash,
  sync, the password once; `ErrNotTeamOnly`); `ErrDomainTaken{App}` when a deploy adds a host
  another app has; `DeleteApp` drops the rows and syncs. Tests search every row, log line, job,
  export and answer for a password sentinel (only the one shown-once answer has it) and for the
  hash (only `team_passwords` and the variable).
- [x] T013 [US2] `internal/compose/compose.go`, `service.go` + tests: `compose.Domain` gains
  `path`, `wwwRedirect`, `teamOnly`; the Aspire fixtures unchanged; `internal/service`'s
  kept-file rewrite: `editDomains(request, …)` rewrites the overlay's domains (as `editSecrets`
  does) so the Domains page edits a kept-file app with `If-Match`.
- [ ] T014 [US2] API: `rotateTeamPassword` (`no-store`, 409 `not_team_only`, `app_stopped`),
  `DeployResult.teamPasswords` (`no-store` on that answer), codes `domain_taken` and the
  `invalid_field` fields, in `internal/api/domains.go`, `deployments.go` and `api/openapi.yaml`;
  tests including a deploy token setting `teamOnly` on its own app and getting the password once.
- [ ] T015 [US2] Console: the Options form in the row's detail (Force HTTPS locked with "On
  for every domain.", Team only (a shared password), the redirect switch worded per the host,
  Path, Save and deploy with `If-Match` (a 409 redraws), Remove… behind `<details>`, New
  password behind `<details>`), `POST /domains/{host}/options`, `/remove`, `/team-password`;
  the shown-once panel (page 3's) answered 200 `no-store` when Team only turns on or rotates;
  the stopped sentence; Configure's Deploy panel gains "Domains go to" when a service has
  several ports (`configure.go`, `edit.go`, `merge.go`). Handler tests; the sentinel test.

## Phase 4: US3, add a domain with its checks (P2)

Independent test: quickstart §3.

- [ ] T016 [US3] Console: the Add panel (`#add` on `/domains` and the app's page): Domain,
  Sends traffic to (app select, service select when several, the port as text "set in
  Configure"), Path, the record box (A and AAAA from `hostAddresses`, `@` or the first label,
  "this host's public IP address" when unknown), Check now (`action=check`, redraws with the
  DNS line's mark and reason and the certificate line), Add domain and deploy (`POST /domains`),
  refusals: another app's host naming it, the console's own host, a malformed host. Handler
  tests.

## Phase 5: US4, problems reach the owner (P2)

Independent test: quickstart §1's item and delivery.

- [ ] T017 [US4] `internal/service/attention.go`, `notifier.go`, `internal/notify` + tests:
  kinds `AttentionDomainDNSElsewhere` and `AttentionDomainCertExpiring` from `domain_checks`
  joined with the stored apps (key `kind|app|service|host`; `Domain`, `AddressSeen`,
  `ExpiresAt` on the item; `Detail` from the contract's sentences), `problemEvent`/`problemPath`
  (→ `/domains?open=<host>#domain-<host>`), `notify.Event` fields `domain`, `addressSeen`,
  `expiresAt` in the webhook JSON and the text renders; `api/openapi.yaml` `AttentionItem` and
  `NotificationEvent`; `internal/api/attention.go`. Tests: `attentionOf` tables, `diffProblems`
  (one opened, resolved when the state clears or the domain is removed), the renders.
- [ ] T018 [US4] Console: the needs-you rows for both kinds (`internal/ui/attention.go`, "Open
  domain"), the Apps headline words; handler tests.

## Phase 6: US5, certificates in Settings (P3)

Independent test: quickstart §4.

- [ ] T019 [US5] Service and API: `CertificateSettings()` (issuer from `CARAVANA_ACME`, the
  email, `warnBeforeDays`, `edgeAddress`), `SetWarnBeforeDays` (7, 14, 30), `Certificates()`
  (from `domain_checks`, problems first); `getCertificateSettings`, `putCertificateSettings`,
  `listCertificates` in `internal/api/certificates.go` and `api/openapi.yaml`; tests.
- [ ] T020 [US5] Console: `/settings/certificates` (`settings.go`, `settingsnav.go`,
  `templates/settings.html`): the head (the `acme=none` sentence), the banners, the certificates
  table, "How certificates are issued" with the read-only Issuer and Contact email rows and
  Edit holding Warn when alone, `POST /settings/certificates`; the nav entry under Connections
  after Backup storage with page 3's mark rule (◆ expiring, else the count); handler tests and
  the mark rule's table.

## Phase 7: Installer, lab, docs and the review cycle (no story label)

- [ ] T021 `deploy/install/traefik.nomad.hcl`: `--providers.file.directory=/local/dynamic`, the
  existing `caravana.yml` template moved there, a `team.yml` template ranging the variable
  `nomad/jobs/traefik` (`change_mode = "noop"`), per T001's observed syntax;
  `deploy/install/install.sh`: writes `CARAVANA_ACME`, `CARAVANA_ACME_EMAIL` and
  `CARAVANA_EDGE_ADDRESS` into Caravana's env, says Traefik restarts once on a rerun;
  `deploy/dev/traefik.nomad.hcl` and `deploy/dev/lab.sh up` the same (re-register the job);
  `deploy/dev/install-test.sh` checks the env lines and the file-provider middleware; shellcheck.
- [ ] T022 `cmd/caravana/lab_test.go`: `startLab` sets `CARAVANA_EDGE_ADDRESS=127.0.0.1:8443`
  and injects the lab resolver (`elsewhere.test` → `203.0.113.9`, this host → `203.0.113.1`,
  `*.invalid` NXDOMAIN, `*.localhost` loopback) through `Service.SetEdge`;
  `cmd/caravana/console_domains_states_test.go`, step `console-domains-states` after
  `console-schedules`, per quickstart §6: the states (self_signed, elsewhere, none, not_served,
  redirect), Team only's shown-once panel and curl 401/200, New password, the redirect's
  Location, the path, Check now, Settings' warn window, the needs-you item and the delivery, no
  password or hash on any page, answer, export, job or log, and the cleanup.
- [ ] T023 `cmd/caravana/gate_domains_test.go`, step `domains` in `runGate` after `schedules`:
  §1–§3 through the API with the gate's token, a deploy token refused on the seven routes and
  allowed to set `teamOnly` on its own app (the password once, absent from every later answer),
  `listCertificates`; on a real host (`host.sh prove`) the `live` state with Let's Encrypt.
- [ ] T024 [P] Docs: `CHANGELOG.md` Unreleased (Added; the Upgrade note: rerun the installer,
  Traefik restarts once, the three env lines), `docs/plan.md` row 023 (a router per domain with
  options under constitution 1.0.1; Force HTTPS global; the port per service; the certificate
  state from the handshake), `CLAUDE.md`'s "Where we are" paragraph for 023, `api/openapi.yaml`
  info text, `deploy/install/caravana-policy.hcl` unchanged (said in the commit).
- [ ] T025 Checks (the CI list, `go test -race` on ui/service/store/edge), `TestConsoleLab`,
  `deploy/dev/install-test.sh`, the review instance staged with the contract's review list, the
  designer's review, the impeccable critique agent (no detector), rulings recorded in the
  contract, fixes, the last look.

## Dependencies

- T001 → Phase 1 (T002–T005 parallel, T006 after T003) → US1 (T007 → T008; T009; T010 → T011)
  → US2 (T012 → T013, T014, T015) → US3 (T016) → US4 (T017 → T018) → US5 (T019 → T020) →
  Phase 7 (T021 and T024 any time after Phase 2; T022, T023 after US5; T025 last).

## Parallel examples

- Phase 1: T002, T003, T004, T005 at once (different packages).
- US1: T009 (API) beside T010 (console) once T007/T008 are in.
- Phase 7: T021 and T024 beside T022.

## MVP

US1 (T001–T011): every domain listed with its state on `/domains` and in the API. The options,
Add with checks, the alerts and Settings follow in that order.
