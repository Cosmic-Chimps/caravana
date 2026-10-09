# Data model: One-command install

## Release (GitHub release `v<version>`)

| Asset | Content |
| --- | --- |
| `install.sh` | the installer, with `CARAVANA_VERSION=<version>` set |
| `caravana_<version>_linux_amd64.tar.gz` | `caravana` (built with `-X main.version=<version>`), `nomad.hcl.tpl`, `traefik.nomad.hcl`, `caravana-policy.hcl`, `traefik-policy.hcl`, `caravana.service` |
| `caravana_<version>_linux_arm64.tar.gz` | the same, for arm64 |
| `SHA256SUMS` | `sha256sum` lines for the two tarballs and `install.sh` |

## Installed host (files the installer owns)

| Path | Mode | Meaning |
| --- | --- | --- |
| `/var/lib/caravana-host/install.env` | 0600 root | `CARAVANA_VERSION`, `CARAVANA_DOMAIN`, `CARAVANA_ACME` (`production`, `staging`, `none`), `CARAVANA_ACME_EMAIL`, `CARAVANA_ADMIN_EMAIL`. A rerun without options reuses them. Its presence means "this installer owns Nomad here". |
| `/var/lib/caravana-host/bootstrap.token` | 0600 root | Nomad's management token (as today) |
| `/var/lib/caravana-host/traefik/` | 0700 | Traefik's ACME storage (as today) |
| `/opt/caravana/<version>/` | 0755 | an unpacked release; the two newest are kept |
| `/opt/caravana/current` | symlink | the version systemd runs |
| `/usr/local/bin/caravana` | 0755 | wrapper: root only, runs `current/caravana` as `caravana` with `/etc/caravana/caravana.env` |
| `/etc/caravana/caravana.env` | 0600 caravana | `CARAVANA_LISTEN=127.0.0.1:7070`, `CARAVANA_DATA_DIR`, `CARAVANA_CERT_RESOLVER`, `CARAVANA_TRUSTED_PROXIES=127.0.0.1,::1`, `NOMAD_ADDR`, `NOMAD_TOKEN` (created once) |
| `/var/lib/caravana/` | 0700 caravana | Caravana's data (`caravana.db`) |
| `/var/lib/caravana.pre-<version>/` | 0700 | the data before the last upgrade, one kept |

## Version order

Versions compare with `sort -V`. A release is `X.Y.Z`. Dev builds are `0.0.0-dev.<unix time>`,
older than any release and ordered among themselves.
