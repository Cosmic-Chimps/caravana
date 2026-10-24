# Data model: Domains (023)

Migration `internal/store/migrations/0018_domains.sql`. The app document changes only when an
option is set (three `omitempty` fields on `app.Domain`), so no existing `revision` moves.

## `app.Domain` (in the app document, not a table)

| Field | Type | Notes |
| --- | --- | --- |
| `host` | string | as today |
| `port` | string | as today, per service |
| `path` | string, omitempty | `/api`: the router's rule adds `PathPrefix`; the prefix is kept |
| `wwwRedirect` | bool, omitempty | this host is the target; its pair (`www.<h>` or `<h>`) redirects here and is listed as a `redirect` row |
| `teamOnly` | bool, omitempty | basic auth, user `team`; the password's hash is in `team_passwords` |

A domain with any of the three gets its own router `<app>-<service>-<hash8(host)>`.

## `domain_checks` (the cache the page and Attention read)

| Column | Type | Notes |
| --- | --- | --- |
| `host` | TEXT PK | |
| `app_id`, `service` | TEXT NOT NULL | copied from the app at check time |
| `state` | TEXT NOT NULL | `dns_elsewhere`, `cert_expiring`, `cert_pending`, `dns_none`, `self_signed`, `redirect`, `live`, `unknown`, `not_served` (a CHECK) |
| `dns_status` | TEXT NOT NULL | `ok`, `elsewhere`, `none`, `unknown` |
| `dns_seen` | TEXT | JSON array of addresses the resolvers answered |
| `dns_expected` | TEXT | JSON array of this host's addresses at check time |
| `cert_status` | TEXT NOT NULL | `issued`, `pending`, `self`, `expiring`, `error`, `none` (not probed: the app isn't served) |
| `cert_issuer` | TEXT | the leaf's issuer common name ("Let's Encrypt", "TRAEFIK DEFAULT CERT" drawn as "Traefik's own") |
| `cert_not_after` | TEXT | RFC 3339 UTC |
| `checked_at` | TEXT NOT NULL | |

Rows are upserted by the watcher and by `checkDomain`; `DeleteApp` and a domain's removal
delete them. Never a body, a key or raw resolver text.

## `team_passwords`

| Column | Type | Notes |
| --- | --- | --- |
| `host` | TEXT PK | one shared password per Team-only domain |
| `app_id` | TEXT NOT NULL | |
| `hash` | TEXT NOT NULL | bcrypt (cost 10), the htpasswd line's hash part; the password itself is never stored |
| `created_at` | TEXT NOT NULL | the last rotation |

The Nomad Variable `nomad/jobs/traefik` is rebuilt from this table on every change (items
`<router>-auth` = `team:<hash>`; a marker item when empty). Deleting the domain, turning the
option off or deleting the app deletes the row and rebuilds the variable.

## `certificate_settings`

| Column | Type | Notes |
| --- | --- | --- |
| `id` | INTEGER PK CHECK (id = 1) | one row, inserted by the migration |
| `warn_before_days` | INTEGER NOT NULL DEFAULT 14 | 7, 14 or 30 |
| `updated_at` | TEXT NOT NULL | |

The issuer (`CARAVANA_ACME`) and the contact email (`CARAVANA_ACME_EMAIL`) are read from the
environment, not stored.

## Derived, not stored

- `renewsAt` = `cert_not_after − 30 d`.
- The state word and mark per row; the banners (the first 3 problem rows); the count line.
- The Settings nav mark: ◆ when any `cert_expiring`, else the count of `issued`.
- The needs-you items `domain_dns_elsewhere` and `domain_cert_expiring`, from `domain_checks`
  joined with the stored apps (a domain no longer on any app has no item).

## `sent_problems` (no change)

Keys `domain_dns_elsewhere|<app>|<service>|<host>` and
`domain_cert_expiring|<app>|<service>|<host>`, the host as the fourth segment.
