### Added
- **Caravana makes the DNS record for a domain you add, at Cloudflare.** Connect a zone in Settings › DNS providers
  with a Cloudflare API token (Zone › DNS › Edit for that zone) and, at the deploy that adds a domain under it,
  Caravana creates an A record (and an AAAA record when the installation has an IPv6 address) pointing at this
  installation, with the comment `caravana:<app>`; it removes the records when the domain goes or the app is deleted.
  It never changes a record you made: a host that already has a record pointing elsewhere refuses the deploy, naming
  the domain. A zone can proxy its records through Cloudflare (the orange cloud), and changing that brings the records
  Caravana made to the new setting. Test lists the zone's records, Remove forgets the zone and leaves the records at
  Cloudflare. The Domains page says which domains' records Caravana made, and its Add panel names the zones Caravana
  handles itself. The installation's public address comes from `CARAVANA_PUBLIC_URL`. API 0.54.0 (`listDNSZones`,
  `putDNSZone`, `deleteDNSZone`, `testDNSZone`, `listDNSRecords`; codes `dns_record_exists`, `dns_address_unknown`,
  `dns_provider_refused`, `dns_provider_unavailable`, `dns_zone_not_found`). See `docs/dns-providers.md`.
