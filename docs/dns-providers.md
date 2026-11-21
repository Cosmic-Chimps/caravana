# DNS records made for you (Cloudflare)

When you add a domain to an app, someone has to make its DNS record. Without a DNS provider that someone is you:
the Add panel shows the record, you make it where you manage DNS, and Caravana's checks tell you when it points
here. With a **DNS provider** connected, Caravana makes the record itself, at the deploy that adds the domain, and
removes it when the domain goes.

Cloudflare is the first provider. This page is about the records Caravana makes for domains. Certificates are
unchanged: Traefik still gets them from Let's Encrypt, domain by domain.

## Connect a zone

1. In Cloudflare, make an **API token for Caravana alone**: *Zone › DNS › Edit*, limited to the one zone
   (`example.com`). It needs nothing else.
2. In Caravana, open **Settings › DNS providers** and add a zone:
   - **Zone**: the domain Cloudflare serves, in lower case (`example.com`). Every domain under it, such as
     `shop.example.com`, gets its record from Caravana. When two zones fit a host (`example.com` and
     `eu.example.com`), the longer one wins.
   - **Cloudflare API token**: the token from step 1. Caravana keeps it in the Nomad Variable
     `caravana/dns/<zone>`, never in its database, a page, an answer or a log.
   - **Proxy through Cloudflare**: the orange cloud. It applies to the records Caravana makes.
3. Press **Test**. It reads the zone's records and says how many there are and how many Caravana made, and lists
   them (type, name, content, proxied, and the app a record of Caravana's was made for). The records are read at
   that moment and kept nowhere. A refused token or an unreachable Cloudflare is said in words, and the row turns
   to the failed mark.

Caravana needs to know **its own public address** to make a record. It reads it from `CARAVANA_PUBLIC_URL` (the
installer sets it from the console domain). Without one a deploy that needs a record is refused with
`dns_address_unknown`.

## What Caravana creates

For every host of the app's domains that is under a connected zone, and for the `www` pair of a domain with the
www redirect:

- an **A** record for each IPv4 address of this installation, and an **AAAA** record for each IPv6 address;
- TTL automatic;
- the comment `caravana:<app id>`, so you can tell at Cloudflare which app a record belongs to;
- the zone's proxy setting.

The records are made before the new version is registered, as secrets are, so a failure fails the deployment before
anything is replaced. A deploy of an app whose hosts already have their records makes no call to Cloudflare.

A domain with a record of Caravana's has the fact **DNS record · made by Caravana in `example.com` (Cloudflare,
direct)** on the Domains page, and the Add panel names the zones Caravana handles itself ("Under `example.com`
Caravana creates the record itself."). After **Check now** for such a host with no record yet, the panel says
"Caravana creates the record in `example.com` at Cloudflare when you add the domain."

When a deploy drops a domain, Caravana removes the records it made for it. Deleting an app removes its records too.
A removal that fails is logged and tried again every ten minutes; it never fails a deploy or a delete.

## What Caravana never touches

- **Records you made.** Caravana only changes and removes records it made itself, which it knows from its own table.
- **A host that already has a record pointing elsewhere** (an A, AAAA or CNAME that doesn't point at this
  installation) **refuses the deploy**, naming the domain's field: "`taken.example.com` already has a record at
  Cloudflare pointing at `198.51.100.9`. Remove it there, or point it here by hand." Nothing is changed. A hand-made
  record that already points here is left alone and not claimed.
- **Other zones.** A host under no connected zone is handled as before: you make its record.

## The proxy setting

**Proxy through Cloudflare** is per zone. Turning it on or off in a zone's **Edit** form brings the records Caravana
made in that zone to the new setting, through Cloudflare. If Cloudflare can't take some of them the zone is saved
and the page says "saved, but the old setting stays on N records; the next save tries again".

A proxied domain's DNS answers are Cloudflare's addresses, not this host's. Until Let's Encrypt has issued its
certificate the Domains page shows it as pointing elsewhere, and says that it is Cloudflare's proxy. Once the
certificate is issued it shows as live, as any domain behind the proxy does.

## Removing a zone

**Remove** forgets the zone and its token. The records Caravana made in it **stay at Cloudflare**: remove them there
if you no longer want them. Domains under the zone stop getting records, and Caravana stops tracking the ones it made.

## The API

| Operation | What it does |
|---|---|
| `listDNSZones` (`GET /v1/dns/zones`) | the zones, with how many records Caravana made in each and the last test |
| `putDNSZone` (`PUT /v1/dns/zones/{zone}`) | creates (201) or replaces (200) a zone; a body without `token` keeps the stored one |
| `deleteDNSZone` (`DELETE /v1/dns/zones/{zone}`) | forgets the zone (204); its records stay at the provider |
| `testDNSZone` (`POST /v1/dns/zones/{zone}/test`) | reads the zone and lists its records now |
| `listDNSRecords` (`GET /v1/dns/records`) | the records Caravana made: host, type, zone, app, content, proxied |

`Domain.record` says whether Caravana made a domain's record, and `DomainCheck.managedZone` names the zone that
covers a host. Deploy tokens can't use these routes; a deploy token's own `deployApp` creates records for its app as
a side effect.

| Code | Status | Meaning |
|---|---|---|
| `dns_record_exists` | 409 | the host has a record pointing elsewhere; the field names the domain |
| `dns_address_unknown` | 409 | Caravana can't tell its own public address (`CARAVANA_PUBLIC_URL`) |
| `dns_provider_refused` | 422 | Cloudflare refused the token, or has no such zone for it |
| `dns_provider_unavailable` | 502 | Cloudflare couldn't be reached |
| `dns_zone_not_found` | 404 | no such zone in Caravana |

## Not in this feature

DNS-01 and wildcard certificates, other DNS providers, records that follow a change of the installation's address
(a record is made once, at the deploy that adds the host), and CNAME records.
