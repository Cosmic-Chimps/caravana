# Data model: Registry credentials per path

## Registry entry (`registries`, migration 0025)

| Field | Rule |
|---|---|
| `host` | lowercase, `:port` allowed, `docker.io` for Docker Hub (as 017) |
| `path` | `''` for a host-only entry; else lowercase image-path parts joined by `/`, no leading or trailing `/` |
| `username` | required |
| `created_at`, `updated_at` | as 017 |
| `last_test_at`, `last_test_ok`, `last_test_message` | as 017; a working path entry carries a note in the message |

Key: `(host, path)`. The entry's name everywhere else is `Key()`: `host`, or `host/path`.
Rows from before 0025 get `path = ''`.

## Entry secret (Nomad Variable)

`caravana/registries/<host key>[/<path with . as ~>]`, items `username`, `password`. Unchanged for host-only entries.

## Service pull login (Nomad Variable `nomad/jobs/<app>/<svc>/<svc>`)

Items `CARAVANA_REGISTRY_USERNAME`, `CARAVANA_REGISTRY_PASSWORD` and `registry`. `registry` now holds the entry the login came from (a host-only entry is the host, as before). Backup and restore variables keep only the two credential items.

## Matching

`entry(image) = first of candidates(host, repository) in the stored entries`, candidates from the most specific path down to the host. No match: no login, no auth block.
