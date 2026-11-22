# Plan 048: DNS providers, records that follow domains

Spec: `spec.md`. Task `s0ce`. Branch: the worktree branch `alta/balmy-daisy-m7wj`. Numbered 048 because 047
(Requests) is on another unmerged branch; the API takes 0.53.0 for the same reason (047 took 0.52.0).

## Constitution check

- I Small by default: no new Go module (the Cloudflare client is `net/http` and `encoding/json` over the v4 API),
  no runtime dependency; a provider is optional and the installation works without one.
- II One model: no change to `app.App`. Nothing is read from `Source`. `nomadjob` is untouched.
- III Safe deploys: records are made before `Register`, as secrets and Team-only passwords are; a failure fails
  the deployment before anything is replaced; a record removal never fails a deploy or a delete (D6).
- IV Fail loudly: every refusal names a field (D5); nothing is dropped silently. A record that can't be removed
  now is kept in the table and tried again (D6), never forgotten.
- V API-first: `api/openapi.yaml` 0.53.0 in the same change; the console calls the same service methods.
- VI Nomad-native: untouched; the token's variable is Caravana's own under `caravana/*`, as the registries' are.
- VII Secure: the token lives only in the Nomad Variable `caravana/dns/<zone key>` (D2); it is never in SQLite, a
  page, an answer, an error, a log line, a job or a URL (the client sends it in a header and classifies errors by
  status, never by quoting a body). Sentinel tests in `dns`, `service`, `api` and `ui`.
- VIII Plain, tested Go: table tests, `dnstest` fakes, a sentinel per package, a gate step and a console step.

## Decisions

- **D1 The seam, `internal/dns`.** Pure of Caravana: it knows records and zones, nothing of apps.
  `Record{ID, Type, Name, Content, Proxied, Comment, TTL}`; `Provider` is `FindZone(ctx, zone) (zoneID, error)`,
  `Records(ctx, zoneID, name) ([]Record, error)` (`name` empty lists the zone), `Create(ctx, zoneID, Record)
  (Record, error)`, `Update(ctx, zoneID, Record) error` (the proxy setting of a record Caravana made),
  `Delete(ctx, zoneID, recordID) error` (a record already gone is not an error). Errors are the sentinels
  `ErrRefused` (401, 403: the token is wrong or lacks Zone › DNS › Edit), `ErrZoneNotFound` (no zone of that name
  for the token), `ErrUnavailable` (a network error, a timeout, a 5xx) and `ErrRecordNotFound`; a `*ProviderError`
  wraps them with the operation, never a body or the token. `Cloudflare{token, baseURL, client}` is the one
  implementation (`NewCloudflare(token, opts…)`): `GET /zones?name=`, `GET /zones/{id}/dns_records?name=`,
  `POST /zones/{id}/dns_records` (`ttl: 1`, `proxied`, `comment`), `PATCH …/{id}` (`proxied`), `DELETE …/{id}`;
  `Authorization: Bearer`; 10 s per call; redirects not followed. A `Factory` is `func(kind, token string)
  (Provider, error)`; `CloudflareFactory(baseURL)` is cmd's (base URL `https://api.cloudflare.com/client/v4`).
  `dnstest.Provider` is an in-memory fake with knobs (refuse, unavailable, a seeded hand-made record) and
  `dnstest.Server` a fake Cloudflare over `httptest` for the lab and the client's own tests.
- **D2 The token's variable.** `caravana/dns/<zone key>` with one item `token`; the zone key writes `.` as `~`
  (`deploy.DNSZoneVariablePath`; the registries' rule). `deploy.DNSZoneToken`, `SaveDNSZoneToken`,
  `DeleteDNSZoneToken`, next to `secretproviders.go`. A zone name is a lowercase hostname with at least one dot
  and not an IP (`service.validZone`, reusing `validCheckHost`).
- **D3 Store.** Migration `0038_dns_providers.sql` (merged as 0038: main took 0037 for email and Telegram): `dns_zones` (`zone` PK, `provider` CHECK `cloudflare`,
  `proxied`, `zone_id` (the provider's, learned by a test or the first record; `''` until then), `created_at`,
  `updated_at`, `tested_at`, `test_ok`, `test_message`, `test_count`) and `dns_records` (`host`, `type` (`A`,
  `AAAA`), PK of the two; `zone`, `app_id`, `record_id` (the provider's), `content`, `proxied`, `created_at`).
  `store.DeleteApp` leaves `dns_records` alone: the record at the provider still exists and the service removes
  it first (D6). `store.DNSZone`, `DNSRecord`, `SaveDNSZone`, `ListDNSZones`, `GetDNSZone`, `DeleteDNSZone`,
  `SaveDNSZoneTest`, `ClearDNSZoneTest`, `SetDNSZoneID`, `InsertDNSRecord`, `ListDNSRecords` (all, by zone, by
  app), `DeleteDNSRecord`, `SetDNSRecordsProxied(zone, proxied)`.
- **D4 Which zone covers a host.** `dns.ZoneOf(host, zones)` (pure): the longest zone equal to the host or a
  suffix after a dot. Records are made for every host of the app's domains and the www pairs of domains with
  the www redirect (the same hosts `checkDomainsTaken` claims). The record name is the full host; the content is
  this installation's public addresses (`hostAddresses`: an A per IPv4, an AAAA per IPv6); no address is
  `ErrHostAddressUnknown` (409 `dns_address_unknown`). The comment is `caravana:<app id>`.
- **D5 The deploy flow.** In `saveAndDeploy`, after `checkDomainsTaken`, `checkDNSRecords(ctx, a)`: for each host
  of `a` under a managed zone that has no row of `a`, read the provider's records for the host: an A, AAAA or
  CNAME record that isn't ours (no row) and doesn't point at one of this installation's addresses is a
  `*DNSRecordExistsError{Field, Host, Zone, Content}` → 409 `dns_record_exists`; one that already points here is
  left alone (no row: hand-made); one with our comment and content but no row is adopted (a row is written). A
  provider that refuses is 422 `dns_provider_refused` (`ErrDNSProviderRefused`, with the field), one that can't
  be reached 502 `dns_provider_unavailable` (`ErrDNSProviderUnavailable`); a zone unknown to the provider is the
  refused error with that sentence. An app whose managed hosts all have rows makes no provider call. Then in
  `deployWarned`, after `ensureTeamPasswords` and before `Register`, `ensureDNSRecords(ctx, a)` creates the
  missing records (one row per record, written right after each create, so a failure half-way leaves what was
  made owned) and fails the deploy with the same errors. After a successful `Register`, `pruneDNSRecords(ctx,
  &a)` removes this app's rows whose host is no longer one of its hosts (a failure is logged, as
  `pruneTeamPasswords` does). `DeleteApp` calls `removeDNSRecords(ctx, id)` before `store.DeleteApp` (failures
  logged).
- **D6 The pass.** `service/dnspass.go`: at start and every 10 minutes (`SetDNSPassGap`), each `dns_records` row
  whose app no longer exists, or whose host is no longer one of its app's hosts, is deleted at the provider and
  dropped; a failure is logged once and tried again next time. Removing a zone (D8) drops its rows first, so the
  pass never touches a zone without a provider. The pass never creates records.
- **D7 Zones (the service).** `service/dnszones.go`: `DNSZoneInfo{store.DNSZone, Records int}`; `DNSZones`,
  `DNSZone`, `SaveDNSZone(ctx, DNSZoneInput{Zone, Provider, Proxied, Token *string})` (the token to the variable
  first, then the row; a changed token or proxy setting forgets the last test; a changed proxy setting updates
  Caravana's records of the zone through `Provider.Update`, and a failure there comes back with the saved zone
  as "saved, but N records keep the old setting; the next save tries again", as `SaveRegistry` does for copies),
  `DeleteDNSZone` (the rows of the zone dropped, the token deleted, the row deleted; the records stay at the
  provider), `TestDNSZone(ctx, zone) (DNSZoneTest, error)`: `FindZone` (stores `zone_id`), then `Records` of the
  whole zone, each marked `Managed` when a row has its provider ID; the outcome stored as `LastTest` ("12
  records, 2 made by Caravana"; a refusal or an unreachable provider is an outcome with OK false, in Caravana's
  words), the records in the answer only. `ManagedZone(ctx, host) (store.DNSZone, bool, error)` for the pages.
  `Service.SetDNSProviders(dns.Factory)`; without one, no zone can be saved or tested and deploys under a zone
  fail refused.
- **D8 API 0.54.0** (merged as 0.54.0: 0.52.0 and 0.53.0 went to email and Telegram and to 047). `GET /v1/dns/zones` (`listDNSZones`, `DNSZoneList`), `PUT /v1/dns/zones/{zone}`
  (`putDNSZone`, body `DNSZoneRequest{provider, proxied, token?}`, 201 created / 200 replaced, `DNSZone`),
  `DELETE` (`deleteDNSZone`, 204; the doc says the records Caravana made stay at the provider),
  `POST /v1/dns/zones/{zone}/test` (`testDNSZone`, `DNSZoneTest{zone, lastTest, records[]}`), `GET
  /v1/dns/records` (`listDNSRecords`, `DNSRecordList`: host, type, zone, app, content, proxied, createdAt).
  `Domain.record` (`DNSManagedRecord{zone, provider, proxied, createdAt}`, null for none), `DomainCheck.managedZone`
  (`""` for none). Codes `dns_record_exists` (409), `dns_address_unknown` (409), `dns_provider_refused` (422),
  `dns_provider_unavailable` (502), `dns_zone_not_found` (404). Deploy tokens: every `/v1/dns/*` route denied by the
  default deny; `deployApp` and `deployCompose` for their own app create records as a side effect.
- **D9 Console.** Settings › DNS providers (`/settings/dns`, entry `dns`, the Connections group after
  Certificates, icon `globe` if the icon set has it, else `link`): the add form (Zone, Cloudflare API token
  (password), "Proxy through Cloudflare" checkbox), one row per zone (zone, "Cloudflare · proxied" or
  "Cloudflare · direct", "3 records made by Caravana", the last test's state and time, Test, Edit (token
  optional, proxy setting), Remove in a `<details>` that says the records stay at Cloudflare). `POST
  /settings/dns` saves (303 to `?dns=saved`), `POST /settings/dns/test` answers 200 with the page and the zone's
  records listed under its row ("as of now": type, name, content, proxied, "made by Caravana"), `POST
  /settings/dns/remove` (303 to `?dns=removed`). The Domains page: a managed row's detail gets the fact "DNS
  record · made by Caravana in `<zone>` (Cloudflare, proxied)"; the Add panel, when any zone is managed, says
  under the "Point it at caravana" box which zones Caravana handles itself; after Check now for a host under a
  managed zone the box is replaced by "Caravana creates the record in `<zone>` at Cloudflare when you add the
  domain." and the DNS line reads "no record yet: Caravana will create it" (idle mark, Add primary); the
  `dns_record_exists` refusal is drawn at the Host field: "`<host>` already has a record at Cloudflare pointing
  at `<content>`. Remove it there, or point it here by hand." Forms work without JavaScript.
- **D10 Lab.** `startLab` runs a `dnstest.Server` on loopback and gives the in-process Caravana
  `dns.CloudflareFactory(server.URL)` through `serviceHook` (`gateTarget.DNS`, nil on a real host, where the
  steps skip). The gate step `dns-records` and the console step `console-dns-providers` (spec acceptance). The
  steps need this installation's public address (`CARAVANA_PUBLIC_URL`, which the lab's resolver answers with
  203.0.113.1): without it the gate step checks the `dns_address_unknown` refusal and skips the rest with a
  note.
- **D11 Docs.** `docs/dns-providers.md` (the token's scope, what is created, what is never touched, removal,
  the proxy setting and certificates), `docs/plan.md` (the Domain extras and Certificates rows name 048),
  `changelog.d/048-dns-providers.md`, CLAUDE.md's Feature 048 paragraph.

## Lab

The gate step `dns-records` (`cmd/caravana/gate_dns_records_test.go`): zone `lab.test` saved with a sentinel token
(`cf-s3cret-sentinel-048-…`); Test lists the fake's seeded records; an app deployed with `web.lab.test` has an A
record `203.0.113.1` with the comment `caravana:<app>` at the fake, a row, `Domain.record` set, and a redeploy
makes no provider call (the fake counts); a deploy adding `taken.lab.test` (seeded, pointing at 203.0.113.9) is
409 `dns_record_exists` on `services[0].domains[1].host` and the app keeps its version; `here.lab.test` (seeded,
pointing here) deploys with no row and the record untouched; a deploy without `web.lab.test` removes its record;
the zone's proxy setting flipped on updates the remaining records; a wrong token makes Test say refused and a
deploy adding a host 422; deleting the app removes its records; removing the zone forgets the rows and leaves the
records at the fake; the sentinel is in no answer, page, log, job, export or the SQLite file.

The console step `console-dns-providers` (`console_dns_test.go`): the Settings page through the forms (add,
Test with the records listed, edit the proxy setting, remove), the Domains page's fact and the Add panel's two
shapes.

## Deviations

(Filled in as the phases land.)

### Phase A (the seam, deploy, store, service, API)

- **`dns_records` is keyed by the provider's record ID**, not `(host, type)`: a host may have two A records when
  this installation has two IPv4 addresses. So the store takes record IDs: `DeleteDNSRecord(recordID)` and
  `SetDNSRecordProxied(recordID, proxied)` (one row at a time, since a zone's records change one by one and a
  failure leaves the others), replacing `SetDNSRecordsProxied(zone, …)`; `ListDNSRecordsInZone`, `ListDNSRecordsOfApp`
  and `DeleteDNSRecordsInZone` are the by-zone and by-app reads and the removal of a zone's rows.
- **Cloudflare classes**: 401/403 and any other 4xx are `ErrRefused` (a 400 for a record the provider won't take has no
  better class); 404 is `ErrZoneNotFound` on the zone lookup, the listing and Create, `ErrRecordNotFound` on Update,
  and nothing on Delete; 429, 5xx, a redirect, a network error and an answer that isn't JSON are `ErrUnavailable`.
  A 2xx with `success: false` is `ErrRefused`.
- **Where the check runs**: after `checkBackups`, the last of `saveAndDeploy`'s checks, not right after
  `checkDomainsTaken`, since it is the only one that leaves the process. Its plan travels in `made.dns`; `ensureDNSRecords`
  uses it, or computes it when the deploy didn't come through `saveAndDeploy` (redeploy, webhook, rollback), and skips
  any record the app has a row for already, so two deploys of one app never make a record twice.
- **Adoption** (a row for a record with no row of this app): a record that has a row of another app (left by a failed
  removal), or this app's comment and an address of this installation, becomes this app's row at ensure time;
  a hand-made record that points here stays unmanaged.
- **Errors**: `*DNSProviderError` also has `Zone` and `Words`; it carries the zone's "saved, but N records keep the old
  setting; the next save tries again" too (API 422 or 502 by its class), and the proxy setting is reconciled on every
  save that finds a row that differs, not only the one that changed it, which is what "the next save tries again" needs.
  There is no `ErrDNSZoneNotFound`: an unknown zone is `store.ErrNotFound`, mapped to 404 `dns_zone_not_found` by the
  zone routes. `SaveDNSZone` without `SetDNSProviders` is a field error on `provider` (no new code); a test without one
  is an outcome.
- **API schemas** added to the plan's list: `DNSZoneTestOutcome`, `DNSProviderRecord` (the records of a test),
  `DNSRecordMade` (the items of `listDNSRecords`) and `DNSManagedRecord` (`Domain.record`); `DNSZoneTest.zone` is the
  zone's name. `Domain.record` is also set on a www pair's row. `testDNSZone` is sent `no-store`.
- `dnstest.Provider.Factory()` and `WantToken` (a client built with another token refuses) stand in for the plan's
  knobs. Not touched, as agreed: `internal/ui`, `parity_test.go`'s `settingsReads` (Phase B adds `listDNSZones`).

### Phase B (console and docs)

- **Files**: `internal/ui/dnszones.go` (the page, its handlers and `dnsZonesState`'s facts), `dns_test.go`, the `settings-dns` and
  `dns-fields` templates in `settings.html`, `domainsadd.go`/`domainspage.go`/`domains.html` for the Domains page. The zone form
  reuses the secret providers' `provider-fields` and `provider-input` templates (the Zone and token inputs are
  `providerFieldView`s; the proxy checkbox is its own setting row), so the token input has no value by the same code.
- **Routes**: `GET /settings/dns` (`?dns=saved|removed`, `?edit=<zone>`), `POST /settings/dns` (add, and edit with `edit=1`),
  `POST /settings/dns/test`, `POST /settings/dns/remove`. The zone travels in the form's hidden or read-only `zone` field.
- **One new CSS class set**, `.cv-dnsrecs` (the records table under a tested zone's row), token colours only; no table class of
  the Settings pages existed (`.cv-sectable` lives in the Updates container).
- **`refuseDeploy` words the DNS errors for every deploy form**: `*DNSRecordExistsError` (409), `*DNSProviderError` (422 or 502 by
  its class) and `ErrHostAddressUnknown` (409), each ending "Nothing was deployed." The Add panel catches
  `*DNSRecordExistsError` first and draws it at the Domain field (its helper answers 422, not the API's 409, as it does for the
  other Host refusals).
- **Check now under a managed zone** replaces the record box with the sentence in every case, but the DNS line becomes "DNS record ·
  none yet: Caravana will create it" only when the check found no record (`DNSNone`). A record that points elsewhere keeps its
  own failed line, because the add would be refused, and one that points here keeps the passed line.
- **`api/parity_test.go`**: `listDNSZones` joined `settingsReads` (every Settings page reads the zones for its nav) and the Domains
  routes, and the four new console routes are mapped to `putDNSZone`, `testDNSZone` and `deleteDNSZone`.
- A `saved, but N records keep the old setting` answer is drawn as the page with a notice and the error's class (422 or 502), not
  a redirect, so the sentence is not lost; the service's own wording ("1 records") is shown as it comes.
- The lab steps are still Phase C's.

### Phase C (lab steps)

- **Files**: `cmd/caravana/gate_dns_records_test.go` (step `dns-records`, after `domains` in `runGate`),
  `console_dns_test.go` (step `console-dns-providers`, after `console-secret-providers`), and `startLab`/`gateTarget` in
  `lab_test.go`/`gate_test.go` (`gateTarget.DNS`, a `dnstest.Server` whose factory reaches Caravana through `serviceHook`).
  Nothing outside `cmd/caravana` changed, and no step needed a fix in the service, store, deploy, dns, api or ui packages.
- **Passed 2026-11-21** with `CARAVANA_PUBLIC_URL=https://console.lab.test`: `dns-records` in 78 s (run alone with
  `-run 'TestLab/^(api-needs-token|dns-records)$'`; five deploys), `console-dns-providers` in 16 s (with `console-sign-in`:
  `-run 'TestConsoleLab/^console-(sign-in|dns-providers)$'`).
- **Without `CARAVANA_PUBLIC_URL`** the gate step checks the 409 `dns_address_unknown` refusal (its message says to set
  `CARAVANA_PUBLIC_URL`, and no record is made) and skips with a note; the console step runs everything but the Add domain
  and Remove-with-a-record parts and logs a note (0.1 s and 0.5 s).
- **Differences from the plan's list**: the redeploy "makes no provider call" check uses an empty-body deploy, the wrong-token
  deploy adds `new.lab.test` to an app that already has rows, and the zone-removal check needs a record, so the step deletes
  the app (records removed), deploys it again with `web.lab.test`, and then removes the zone (rows gone, the record stays at
  the fake, the app's later deletion makes no provider call, and the Nomad variable `caravana/dns/lab~test` is gone). The
  gate reads no console pages; the token's absence from pages is the console step's check, and the gate's covers answers,
  the log, the Nomad job and the SQLite files. The fake accepts no token until a step calls `SetToken`, and each step puts its
  own sentinel there.
- **One test bug fixed on the way**: the console step first waited on a `status` field that `getApp` doesn't have; it now
  waits on the newest deployment, as the 037 step does.
