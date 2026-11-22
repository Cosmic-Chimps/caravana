# Feature 048: DNS providers, records that follow domains

CodeAlta task `s0ce` ("19 · Let Caravana create and remove DNS records through a provider key", raised by the
maintainer on 2026-10-28). `docs/plan.md` names it under Settings › DNS providers and Domain extras.

## Why

Every domain needs its DNS record made by hand where the domain's DNS is managed (Cloudflare for
`lacalabaza.xyz`), and the Add domain panel only checks it. With a key for the DNS provider, Caravana can create
and remove the records itself, so addresses that come and go (one per pull request, later) cost nothing to set
up or take down.

## User stories

### US1 (P1): a DNS provider for a zone

The owner opens Settings › DNS providers and adds a zone (`lacalabaza.xyz`) with a Cloudflare API token (scoped
to Zone › DNS › Edit for that zone) and the proxy setting for the records Caravana will make (Cloudflare's
orange cloud on or off). The token is kept only in the cluster's secret store; the page shows the zone, the
proxy setting, how many records Caravana made in it and the last test. Test lists the zone's records as the
provider has them now, saying which ones Caravana made. Edit changes the token or the proxy setting; Remove
forgets the zone and its token, and the records Caravana made stay at the provider (the page says so before
the owner confirms).

Acceptance: a zone added through the form, tested (the records listed, one marked as Caravana's), the proxy
setting changed, and removed; the token in no page, answer, error, log line or the SQLite file.

### US2 (P1): records follow domains

When a deploy adds a domain under a zone that has a provider, Caravana creates the record that points the
host at this installation (an A record, and an AAAA record when the host has a public IPv6 address) with the
zone's proxy setting, marked as Caravana's (`caravana:<app>` in the record's comment), before the job is
registered. When a deploy drops the domain, or the app is deleted, Caravana removes the record. The www pair of
a domain with the www redirect gets a record too.

Only records Caravana created are ever changed or removed: it keeps a table of them, and a hand-made record is
never touched. A host that already has a record at the provider pointing somewhere else refuses the deploy,
naming the domain's field; one that already points here is left alone, unmanaged.

Acceptance: an app deployed with a domain under the zone has its record at the provider with the comment and
the right address; a redeploy makes no provider call; a deploy without the domain removes the record; a deploy
with a host whose hand-made record points elsewhere is refused with `dns_record_exists` and the app keeps its
version; a host whose hand-made record points here deploys and the record is untouched; deleting the app
removes its records.

### US3 (P1): the Domains page says so

A domain whose record Caravana made says "record made by Caravana in `<zone>`" (with the proxy setting) in its
detail. The Add panel skips the "Point it at caravana" box for a host under a zone with a provider and says
Caravana will create the record; Check now for such a host says the record isn't there yet and will be made,
and Add stays the primary action.

Acceptance: the detail sentence on the Domains page and the Add panel's two shapes, through the forms, without
script.

### US4 (P2): the same through the API

`listDNSZones`, `putDNSZone`, `deleteDNSZone`, `testDNSZone` (the records listed), `listDNSRecords` (what
Caravana made), `Domain.record` and `DomainCheck.managedZone`. Deploy tokens may deploy a domain under a managed
zone for their own app (the record follows) and may not touch the zones.

## Refusals and errors

- A zone that isn't a lowercase domain name with a dot, or is an address: field error on `zone`.
- A token empty on create, or empty on edit (leave it out to keep the stored one): field error on `token`.
- A provider other than `cloudflare`: field error on `provider`.
- A deploy adding a host under a managed zone whose record at the provider points elsewhere (A, AAAA or
  CNAME): 409 `dns_record_exists` on `services[i].domains[j].host` (or `.wwwRedirect` for a pair), naming the
  host and what the record points at.
- The provider can't be reached while a deploy needs it: 502 `dns_provider_unavailable`; the provider refuses
  the token, or the zone isn't at the provider: 422 `dns_provider_refused`. Both name the field of the first
  domain that needed the provider. A deploy that adds no new host under a managed zone never calls the
  provider.
- This installation's public address isn't known (no `CARAVANA_PUBLIC_URL`, or its host doesn't resolve) while
  a deploy needs a record: 409 `dns_address_unknown`.
- A zone that doesn't exist in Caravana: 404 `dns_zone_not_found`.

A failed removal (the provider down when a domain is dropped or an app deleted) never fails the deploy or the
delete: the record is kept in the table and removed by a later pass.

## Out of scope

DNS-01 wildcard certificates through the provider (the same seam, later), other providers (Route 53,
DigitalOcean, deSEC…), records that follow a change of this installation's public address, CNAME records
(an edge CNAME is Phase 3), records on joined servers' addresses (one ingress today), an MCP tool, updating a
hand-made record to point here.
