# Contract: the request, the updater and its status

The one interface between Caravana (user `caravana`, can write only `/var/lib/caravana`) and the root
updater. Both sides are tested against this file.

## The request (written by Caravana)

Path: `<CARAVANA_DATA_DIR>/update-request` (on a host `/var/lib/caravana/update-request`), mode 0600.
Written atomically: `update-request.tmp` then rename. Exactly two lines, LF-terminated:

```text
ID=3f6c0d2a9b7e41c58d0f2e6a1b9c7d40
VERSION=0.17.0
```

- `ID`: 32 lowercase hex characters (16 random bytes), the update run's ID in SQLite.
- `VERSION`: without a leading `v`, matching `^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`.
- Caravana removes the file when the run is final (any of `updated`, `rolled_back`, `failed`,
  `refused`, `lost`), and never writes a second request while one is open.

## The updater's directory (written by root)

`/var/lib/caravana-update/`, root:root 0755, made by the installer.

| File | Mode | What |
| --- | --- | --- |
| `updater` | 0644 | The installer's marker: one line, the format version `1`. Caravana takes its presence (a regular file) as "the updater is installed". |
| `status` | 0644 | The newest run's status (below). Absent before the first run. Written atomically (`status.tmp`, then `mv`). |
| `logs/<ID>.log` | 0600, dir 0700 | The installer's whole output for that run. The newest 10 are kept. |

### `status`

`KEY=value` lines, any order, read as text (Caravana never executes it):

```text
ID=3f6c0d2a9b7e41c58d0f2e6a1b9c7d40
VERSION=0.17.0
FROM=0.16.0
STATE=rolled_back
STARTED=2026-11-06T14:03:00Z
FINISHED=2026-11-06T14:05:12Z
REASON=Caravana 0.17.0 didn't answer its health check within 60 s; 0.16.0 runs again, with its data as it was
```

- `STATE`: `running`, then one of `updated`, `rolled_back`, `failed`, `refused`.
- `FROM`: the version `current` pointed at when the run started (empty when it couldn't be read).
- Times: RFC 3339 UTC, seconds. `FINISHED` is empty while running.
- `REASON`: empty for `running` and `updated`; else one line of printable ASCII (`LC_ALL=C tr -cd
  '[:print:]'`), at most 300 bytes. It is either one of the updater's own sentences (below) or the
  installer's last line starting `caravana install: `, without that prefix. Never more of the log.
- Caravana reads at most 4 KiB; an unknown key is ignored; a `status` whose `ID` isn't the open run's is
  ignored (it is an older run's).

## The updater (`/usr/local/libexec/caravana-update`)

Bash, run as root by `caravana-update.service` (oneshot) every minute from `caravana-update.timer`. The
whole body is a `main` function and the last line is `main "$@"; exit $?` (the installer rewrites this
file while it runs). Steps, in order; the first that applies ends the run:

1. Take `flock -n` on `/run/caravana-update.lock`; held: exit 0.
2. No `/var/lib/caravana/update-request`: exit 0 (nothing logged: this runs every minute).
3. The request is a symlink, not a regular file, or over 256 bytes: log "the update request isn't a
   plain file; ignored" to the journal, exit 0.
4. Read `ID` and `VERSION` with `head -c 256 | sed -n 's/^ID=//p'` (first match). An ID that isn't 32
   lowercase hex: log "the update request has no valid ID; ignored", exit 0 (no status: there's no ID to
   tie it to; Caravana times the run out).
5. `status` already has this `ID` with a final `STATE`: exit 0 (Caravana hasn't removed the request yet).
6. From here every refusal writes `STATE=refused` with the reason, and exit 0:
   - `VERSION` doesn't match the pattern: "The request's version isn't a release version."
   - not newer than `readlink /opt/caravana/current` (`sort -V`, the installer's `newer`):
     "caravana <current> runs here, and <version> isn't newer."
7. Write `STATE=running` (with `FROM`, `STARTED`).
8. Download into a new `mktemp -d` (0700): `<base>/v<version>/SHA256SUMS`, then `install.sh`, then
   `caravana_<version>_linux_<arch>.tar.gz` (`dpkg --print-architecture`), with `curl -fsSL --max-time
   300 --proto =https,http,file`. `<base>` is `https://github.com/Cosmic-Chimps/caravana/releases/download`,
   or `CARAVANA_UPDATE_RELEASES` from `/etc/caravana/update.env` when that file is owned by root and not
   group- or world-writable (read with `sed`, never sourced; must match `^(https?|file)://[A-Za-z0-9._:/-]+$`).
   A failed download: refused, "Couldn't download caravana <version>'s release files: it isn't a
   published release, or the server can't reach GitHub."
9. Both `install.sh` and the tarball must have a line in `SHA256SUMS` and match it (`sha256sum -c`), and
   `install.sh` must have the line `CARAVANA_VERSION=<version>`; else refused, "caravana <version>'s
   release files don't match their checksums; nothing was installed."
10. Run `bash <dir>/install.sh --from <dir> </dev/null >logs/<ID>.log 2>&1`. Then, by exit code:
    - 0 and `current` is `<version>`: `updated`;
    - 1 and `current` is `FROM` again and the log has "runs again": `rolled_back`;
    - 2: `refused` (the installer's preflight refused before changing anything);
    - anything else: `failed`.
    The reason (all but `updated`) is the log's last line starting `caravana install: ` without the
    prefix; with none, "The installer stopped with exit code <n>." Write the final status, remove the
    temporary directory, prune `logs/` to the newest 10, exit 0.

The updater never reads the request's bytes into anything but the two validated variables, never
prints them, and never writes outside `/var/lib/caravana-update`, its temporary directory and what the
installer itself writes.

## Units

`caravana-update.service`:

```ini
[Unit]
Description=Caravana's updater: runs an update the console asked for
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/libexec/caravana-update
TimeoutStartSec=30min
```

`caravana-update.timer`:

```ini
[Unit]
Description=Look for an update the console asked for, every minute

[Timer]
OnCalendar=minutely
AccuracySec=5s
Persistent=false

[Install]
WantedBy=timers.target
```

The installer installs the script (0755 root) and both units (0644) with `write_if_changed`, makes the
directory and its marker, `systemctl daemon-reload` and `enable --now caravana-update.timer`.
`--uninstall` runs `systemctl disable --now caravana-update.timer`, removes the script, both units and
`/var/lib/caravana-update`, and `daemon-reload`s.
