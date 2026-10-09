# Contract: the installer (`install.sh`)

```text
install.sh [--email ADDRESS] [--domain NAME] [--acme production|staging|none]
           [--acme-email ADDRESS] [--from DIR]
install.sh --uninstall [--purge]
```

| Option | Default | Meaning |
| --- | --- | --- |
| `--email` | recorded one | The admin's email. Required on the first install. |
| `--domain` | recorded one, else `<ip-with-dashes>.sslip.io` | The console domain. It serves the console, the API and webhooks. |
| `--acme` | `production` | `staging` is Let's Encrypt's staging CA. `none` is Traefik's default certificate and skips the DNS check (lab, private networks). |
| `--acme-email` | `--email` | The Let's Encrypt account email. |
| `--from` | download | A directory holding the tarball and `SHA256SUMS` of this release. |
| `--uninstall` | | Remove Caravana, keeping Nomad, Traefik and every app. |
| `--purge` | | With `--uninstall`, also delete Caravana's data, its config and its user. |

Output: `==> <step>` lines, then a last block with the console URL and the next step. Every
install run prints `changed: <names>`, which is empty when nothing changed.

Exit codes:

- 0: done
- 1: a step failed (the message names it; a failed upgrade has already put the previous version
  back)
- 2: refused before any change (preflight, older version, bad option)

# Contract: `caravana version` and health

- `caravana version` prints the version (`dev` for plain `go build`).
- `GET /v1/health` answers `200 {"status":"ok","version":"<version>"}`. `api/openapi.yaml` goes
  from 0.9.0 to 0.9.1.

# Contract: `deploy/vm/host.env`

- `CARAVANA_HOST`: the SSH target, as today.
- `CARAVANA_DOMAIN_CONSOLE`: new, replaces `CARAVANA_DOMAIN_WEBHOOK`.
- `CARAVANA_DOMAIN_APP`: the gate's whoami domain, as today.
- `CARAVANA_ADMIN_EMAIL`: new.
- `CARAVANA_ACME_EMAIL`: as today.
- `CARAVANA_ACME_STAGING`: as today (`1` becomes `--acme staging`).

`host.sh` keeps `up`, `env`, `tunnel` (Nomad and the Traefik dashboard), `admin`, `prove` and
`ssh`. It gains `uninstall`.
