# Tasks: Change the console's address from the console

## Phase A — service, store, Traefik route, redirect

- [x] T001 Migration `internal/store/migrations/0041_console_address.sql`: table `console_address_changes` (id TEXT
  PRIMARY KEY, domain, old_domain, redirect_days INTEGER, status with a CHECK of the nine states, checks TEXT JSON,
  reason, dns_record_created INTEGER, created_at, ready_at, switch_at, settled_at, redirect_until) and a partial unique
  index allowing one row with status in (checking, ready, switching). Store methods: insert, get, update status/checks,
  open change, newest change, old domains whose `redirect_until` is after a time; store tests.
- [x] T002 `deploy`: `ConsoleRouteSupported(ctx)` (the `traefik` job's task templates name
  `nomad/jobs/traefik/traefik`), `SetConsoleHosts(ctx, hosts []string)` (writes item `hosts`, space-separated, to that
  variable; an empty list deletes the variable). `deploytest` fakes for both.
- [x] T003 `deploy/install/traefik.nomad.hcl`: the `caravana.yml` template per plan D2 (variable first, then
  `var.console_domain`; one router per host `caravana`, `caravana-1`…; one service `caravana`; `change_mode = "noop"`;
  `{}` with no host). Comment why. `install.sh`'s `run_traefik` says "Traefik is restarted once for the console address
  (a few seconds without HTTP)" when the running job's spec lacks `nomad/jobs/traefik/traefik`.
- [x] T004 `service/consoleaddress.go`: `ConsoleAddress(ctx)`, `StartConsoleAddressChange(ctx, domain, days,
  createRecord)`, `ConsoleAddressChange(ctx, id)`, `CancelConsoleAddressChange`, `SwitchConsoleAddress`; the checks
  (plan D4) with seams for tests (route probe; `edge` already has Resolver/Dialer); the check goroutine on the process
  context; the hosts reconcile (plan D2) after every state change; typed errors mapped later in `internal/api`.
- [x] T005 The pass (every minute, `SetConsoleAddressPassGap` for tests) and start: `checking` → `check_failed`
  "Caravana restarted during the check"; `switching` whose domain is the public URL's host → `applied` (with
  `redirect_until`); `switching` over 5 minutes → `lost`; drop ended old domains from the route.
- [x] T006 Settle from the agent: `NodeSync` records `runsConsole` reports (time of the last one), sends
  `consoleAddress` to such an agent while a change is switching, sends `consoleURL` to every agent, and settles a
  switching change from a `failed`/`refused` outcome with its ID (an `applied` outcome is settled at start, or by the
  pass when the public URL matches; an `applied` whose public URL doesn't match is `failed` "Caravana restarted, but its
  public URL isn't https://<domain>").
- [x] T007 The outer handler (where `root` sends `/.well-known/` to the API): for a host in the old domains (cached,
  refreshed on change), `/v1/` and `/.well-known/` pass through, anything else 308 to the public URL + path + query.
  Tests.
- [x] T008 D12: an app domain equal to the current, the open change's or an old console domain is `domain_taken`
  (where the console's own domain is refused today). Tests.
- [x] T009 A `service` test: password sign-in at the new domain works with password sign-in off and passkeys only on
  the old domain (033's rule).

## Phase B — the agent

- [x] T010 `nodeagent`: `Report.RunsConsole`, `Report.ConsoleAddress`, `Answer.ConsoleURL`, `Answer.ConsoleAddress`,
  `State.ConsoleAddress`; validation per contracts/agent.md; table tests with crafted answers (newline, space, quote,
  `$(`, `/`, `:`, port, scheme, IP, uppercase, 254 bytes, bad ID) — each leaves both files and the unit untouched.
- [x] T011 Apply (contracts/agent.md §Apply) with the files under the agent's root (`a.path`) and `a.run` for
  systemctl, the health wait behind a seam; tests: applied, unhealthy → files back + `failed`, interrupted `running`,
  repeated ID not applied twice, the other lines of both files kept byte for byte, mode/owner kept.
- [x] T012 Follow (joined servers) with tests: loopback URL ignored, health 200 → node env rewritten, failure kept.
- [x] T013 Service side wired (T006) against the real types; `go test ./internal/nodeagent ./internal/service`.

## Phase C — API and console

- [x] T014 `internal/api`: the five operations, error mapping, `api/openapi.yaml` 0.57.0 (schemas and codes), the
  deploy-token scope walk lists them as denied, parity test.
- [x] T015 `internal/ui`: Operator › Console address per contracts/page.md; nav entry; tests (page, refusal at field,
  panel polling 286, switch/cancel posts, no-JS forms); `tools/ui-css.sh build` if classes are new.
- [x] T016 `docs/console-address.md` (operator doc: what changes, the window, passkeys, issuer, cluster, lab facts),
  `changelog.d/console-address.md` (Added, Upgrade: rerun the installer once).

## Phase D — lab

- [x] T017 (written, not run; `ONLY=console-address deploy/dev/install-test.sh` runs just it) `deploy/dev/install-test.sh` `check_console_address_host`: `/etc/hosts` names for `caravana.test` and
  `console2.caravana.test`; `nowhere.caravana.test` refused 422 with the env file and the route unchanged; the change to
  `console2.caravana.test` ready, switched, applied; env file, `install.env`, `/v1/console-address`, the issuer, the
  old domain's 308 for `/` and 200 for `/v1/health`, whoami answering throughout, Traefik's allocation unchanged; a
  rerun of the installer keeps the new domain.
- [x] T018 `TestConsoleLab` step `console-address` (the lab's Traefik has no console route: the page shows the
  sentence, the API answers `console_address_unsupported`).
- [ ] T019 CLAUDE.md entry; plan deviations; task `xbf5` complete.
