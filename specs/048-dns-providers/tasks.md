# Tasks 048: DNS providers, records that follow domains

## Phase A: the seam, deploy, store, service, API

- [x] T001 `internal/dns` (D1): `Record`, `Provider`, the error sentinels and `*ProviderError`, `ZoneOf` (D4),
  `Cloudflare` over `net/http` with `NewCloudflare` and `CloudflareFactory`, `Factory`; `dnstest.Provider` (in
  memory, with knobs) and `dnstest.Server` (a fake Cloudflare over `httptest`: zones, records, a token to
  check, a call counter, seeded records). Tests: the client against the fake server for every call and every
  error class; the token in no error text; `ZoneOf` table; a test that the package imports nothing of Caravana
  but nothing (pure).
- [x] T002 `internal/deploy/dns.go` (D2): `DNSZoneVariablePath`, `DNSZoneToken`, `SaveDNSZoneToken`,
  `DeleteDNSZoneToken`; tests with the Nomad fake as `secretproviders_test.go` does.
- [x] T003 `internal/store` (D3): migration `0038_dns_providers.sql`, `dnszones.go` and `dnsrecords.go` with
  the functions named in D3; tests (save and read back, the test outcome, records by zone and app, the proxied
  update, `DeleteApp` leaves the rows).
- [x] T004 `internal/service` (D4–D7): `dnszones.go` (zones, save, delete, test, `ManagedZone`,
  `SetDNSProviders`), `dnsrecords.go` (`checkDNSRecords`, `ensureDNSRecords`, `pruneDNSRecords`,
  `removeDNSRecords`, the error types), `dnspass.go` (the pass, started in `Start`), the hooks in
  `saveAndDeploy`, `deployWarned` and `DeleteApp`; `DomainView.Record` and `DomainCheck.ManagedZone`. Tests with
  `dnstest.Provider`: every spec acceptance of US2 at the service level, the pass, the proxy update, and a
  sentinel test that the token is in no record, log line, error or the SQLite file.
- [x] T005 `internal/api` + `api/openapi.yaml` 0.54.0 (D8): `dns.go` with the five operations, the error
  mappings in `errors.go`, `Domain.record`, `DomainCheck.managedZone`; the parity, contract and scope-walk tests
  pass; a sentinel test that no answer holds the token.
- [x] T006 `cmd/caravana/main.go`: `svc.SetDNSProviders(dns.CloudflareFactory(""))`.

## Phase B: console and docs

- [x] T007 `internal/ui` (D9): `dnszones.go` (Settings › DNS providers: the entry, nav state, add, edit, test
  with the records listed, remove), the Domains page's managed fact, the Add panel's two shapes and the refusal
  at Host; templates; `tools/ui-css.sh build` if a class is new; tests per page state and a sentinel test.
- [x] T008 Docs (D11): `docs/dns-providers.md`, `docs/plan.md` rows, `changelog.d/048-dns-providers.md`,
  CLAUDE.md's Feature 048 paragraph.

## Phase C: lab

- [x] T009 `startLab`: the `dnstest.Server` and the factory through `serviceHook` (D10); the gate step
  `dns-records` (`gate_dns_records_test.go`, listed in `runGate` after `domains`) and the console step
  `console-dns-providers` (`console_dns_test.go`); both pass in the lab.
- [x] T010 Full checks: build, vet, staticcheck, `go test -race ./...`, gofmt, cross-compile, `tools/ui-css.sh
  check`, `tools/ui-vendor.sh check`, `tools/release.sh check`.
