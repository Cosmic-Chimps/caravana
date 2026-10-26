# API contract: Domains (023), openapi 0.25.0

Every route needs a full API token or a session; deploy tokens are refused with 403
`token_scope` (default deny), except `deployApp`/`deployCompose` on the token's own app, which
may set the options and then receives the password once. Times are RFC 3339 UTC.

## Operations

| Operation | Route | Answer |
| --- | --- | --- |
| `listDomains` | `GET /v1/domains?app=&project=&fresh=` | 200 `{domains: [Domain]}` problems first, then live, then idle; `fresh=true` runs the checks now instead of reading the cache |
| `getDomain` | `GET /v1/domains/{host}` | 200 `Domain` |
| `checkDomain` | `POST /v1/domains/check` body `{host, app?}` | 200 `DomainCheck` (fresh; without `app`, a host not yet served: DNS and the record to create, the certificate line "issued after you add it"), `Cache-Control: no-store` |
| `rotateTeamPassword` | `POST /v1/apps/{id}/domains/{host}/team-password` | 200 `{host, user: "team", teamPassword}` once, `no-store`; 409 `not_team_only` |
| `getCertificateSettings` | `GET /v1/settings/certificates` | 200 `CertificateSettings` |
| `putCertificateSettings` | `PUT /v1/settings/certificates` body `{warnBeforeDays}` | 200 `CertificateSettings`; 422 on a value not in {7, 14, 30} |
| `listCertificates` | `GET /v1/certificates` | 200 `{certificates: [Certificate]}` problems first |

## Schemas

```yaml
Domain:
  host, app, project, environment, appName, service, port: string
  path: {type: string}                  # "" when none
  wwwRedirect: {type: boolean}
  teamOnly: {type: boolean}
  router: {type: string}                # <app>-<service> or <app>-<service>-<hash8>
  redirectOf: {type: string}            # for a redirect row: the host it redirects to
  state: {enum: [dns_elsewhere, cert_expiring, cert_pending, dns_none, self_signed, redirect, live, unknown, not_served]}
  dns: {status: {enum: [ok, elsewhere, none, unknown]}, seen: [string], expected: [string]}
  certificate: {status: {enum: [issued, pending, self, expiring, error, none]}, issuer: string, notAfter: date-time|null, renewsAt: date-time|null}
  checkedAt: {type: string, format: date-time, nullable: true}

DomainCheck:
  host: string
  record: {type: "A"|"AAAA", name: string, value: string}[]   # the records to create; empty when this host's address isn't known
  dns: Domain.dns
  certificate: Domain.certificate       # status none with "issued after you add it" for a host not served
  checkedAt: date-time

CertificateSettings:
  issuer: {enum: [letsencrypt, letsencrypt-staging, none, unknown]}   # from CARAVANA_ACME; unknown until the installer is rerun
  contactEmail: {type: string}          # read-only; "" when unknown
  warnBeforeDays: {type: integer, enum: [7, 14, 30]}
  edgeAddress: {type: string}

Certificate:
  host, app, service: string
  issuer: string                        # "Let's Encrypt", "Traefik's own", …
  status: Domain.certificate.status
  notAfter, renewsAt: date-time|null
```

`App.services[].domains[]` gains `path`, `wwwRedirect`, `teamOnly` (all optional).
`DeployResult` gains `teamPasswords: {<host>: <password>}` (present only on the deploy that
turned a domain's `teamOnly` on; `Cache-Control: no-store` on that answer).

## Codes

| Status | Code | When |
| --- | --- | --- |
| 404 | `domain_not_found` | `getDomain`, `rotateTeamPassword` with a host no app has |
| 409 | `domain_taken` | a deploy adds a host another app has (field `services[i].domains[j].host`, the app named) |
| 409 | `not_team_only` | `rotateTeamPassword` on a domain without the option |
| 409 | `app_stopped` | a rotate or an option change on a stopped app |
| 422 | `invalid_field` | `path` (shape, length), `wwwRedirect` (not an apex or `www.` host; the pair is another app's domain), `host` (the console's own host, a scheme, a path, a wildcard), `warnBeforeDays` |

## Existing schemas that change

- `AttentionItem.kind` gains `domain_dns_elsewhere` and `domain_cert_expiring`; the item gains
  `domain` (host), `addressSeen` and `expiresAt`.
- `NotificationEvent` gains `domain`, `addressSeen`, `expiresAt` (additive; `kind` as above).

## Console routes (same service methods)

`GET /domains`, `GET /parts/domains`, `POST /domains/check`, `POST /domains` (Add and deploy),
`POST /domains/{host}/options` (Save and deploy), `POST /domains/{host}/remove`,
`POST /domains/{host}/team-password`, `POST /domains/{host}/check` (Check again);
`GET /apps/{id}/domains` (the same template filtered), `GET /settings/certificates`,
`POST /settings/certificates`.
