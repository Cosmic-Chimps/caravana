# Data model (034)

## SQLite, migration `0027_byo_certificates.sql` (0026 is feature 033's)

`certificate_settings` gains (all about the custom issuer; empty = the installer's):

| Column | Type | |
|---|---|---|
| `acme_server` | TEXT NOT NULL DEFAULT '' | the directory URL |
| `acme_email` | TEXT NOT NULL DEFAULT '' | |
| `acme_eab_kid` | TEXT NOT NULL DEFAULT '' | the key ID (not the key) |
| `acme_eab_set` | INTEGER NOT NULL DEFAULT 0 | a key is stored in the variable |
| `acme_ca` | TEXT NOT NULL DEFAULT '' | the issuer's CA certificate, PEM (public) |
| `acme_updated_at` | TEXT | |

`domain_checks` gains `cert_fingerprint TEXT NOT NULL DEFAULT ''` (SHA-256 of the presented leaf, hex).

New table `uploaded_certificates`: `id` TEXT PRIMARY KEY (`crt_` + 12 hex), `names` TEXT (JSON array),
`subject`, `issuer`, `not_before`, `not_after` (RFC 3339, nine digits), `fingerprint`, `key_type`, `trusted`
INTEGER, `uploaded_at`. No key, no chain.

## Nomad Variables

`caravana/acme` and `caravana/certificates/<id>`: see `contracts/traefik.md`. Caravana's policy already has
`caravana/*`.

## Derived, never stored

- `issuerName`, `status` of an uploaded certificate, `domains`, `keptEarlier`, the attention items.
- Whether the ingress supports the feature: job `traefik`'s meta `caravana_certificates`, cached a minute.
