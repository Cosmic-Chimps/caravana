# Implementation Plan: Update Caravana from the console

**Branch**: `alta/earthy-thistle-j2m8` | **Date**: 2026-11-06 | **Spec**: [spec.md](spec.md)
**Contracts**: [updater.md](contracts/updater.md) (host files and the root script), [api.md](contracts/api.md),
[page-updates.md](contracts/page-updates.md) (the designer's contract for the page)

## Summary

"Update to 0.17.0" writes a request (an ID and a version) into Caravana's data directory and a row in
SQLite. A root systemd timer from the installer runs `caravana-update` every minute; with a valid request
it downloads that release's `SHA256SUMS`, `install.sh` and tarball, checks them, and runs the installer,
which upgrades with 012's rollback. The updater writes its status in its own root-owned directory, which
Caravana reads (it can read, not write, outside `/var/lib/caravana`). Caravana follows the run across
its own restart, records the outcome, and turns a rollback or failure into a needs-you item, which the
notifier already sends. Optional automatic updates make the same request for patch releases at a set
hour.

## Technical Context

Go 1.27, SQLite (`modernc.org/sqlite`), the existing release check (027, `service/updates.go`), host
events and Activity (027), the needs-you list and notifier (014/018), bash for the updater (the
installer's conventions: `set -euo pipefail`, values read as text and never sourced, `envq`-style
validation), systemd timers. No new Go module, no runtime dependency, no Nomad call or policy change.

## Decisions (research)

- **R1 Timer, not a path unit.** A `.path` unit with `PathExists=` re-triggers while the file exists
  and hits systemd's start rate limit when a run is refused; a minutely timer is simple, and the same
  timer serves automatic updates (Caravana writes the request at the set hour). `OnCalendar=minutely`,
  `AccuracySec=5s`, `Persistent=false`. The oneshot service has `TimeoutStartSec=30min`.
- **R2 Two directories, one writer each.** The request lives in `/var/lib/caravana` (the only path
  Caravana can write; 0700 caravana, which root reads). The status and logs live in
  `/var/lib/caravana-update` (root, 0755; status 0644, logs 0600 root). Root never writes inside a
  directory Caravana owns, so a symlink planted by a compromised console can't redirect a root write.
  Caravana removes its own request file once the run is final; the updater skips a request whose ID
  already has a final status, so nothing runs twice.
- **R3 Automatic updates are Caravana's clock.** The setting lives in SQLite (the updater can't read it
  without a `sqlite3` on the host). At the set UTC hour Caravana writes an ordinary request with the
  trigger `automatic`; the updater can't tell them apart and needn't. A version that ever had a run is
  never requested automatically again, so a broken patch release isn't retried every night.
- **R4 Only the newest release seen.** Caravana requests only the release its check last saw
  (`release_check.latest_tag`) and only when it is newer than the running version. The updater checks
  again on its own (pattern, newer, published with checksums), since it can't trust the console.
- **R5 The installer runs as it does by hand.** `bash install.sh --from <dir>` after the updater has
  verified both files; the installer verifies the tarball again (its `--from` path) and reuses the
  recorded options (`/var/lib/caravana-host/install.env`). Its exit code says the outcome: 0 updated
  (and `current` points at the version), 1 with "runs again" in its output rolled back, 2 refused
  (preflight), anything else failed. The reason is the installer's last `caravana install: ` line.
- **R6 The updater replaces itself mid-run.** The new installer rewrites
  `/usr/local/libexec/caravana-update` while it runs; bash reads a script as it goes, so the script's
  whole body is one `main` function called on the last line (`main "$@"; exit $?`), parsed before it
  runs.
- **R7 A run that never ends.** `lost` after 5 minutes without a status for the request's ID (the
  timer is off, the updater is missing, the host was down) and after 45 minutes `running` (the service
  timeout is 30 minutes). Caravana removes the request file when it records `lost`.
- **R8 Tests without GitHub.** The updater reads one optional root-owned file,
  `/etc/caravana/update.env` (`CARAVANA_UPDATE_RELEASES=<base URL>`), only when it is owned by root and
  not writable by group or others; only `install-test.sh` writes it. Caravana gains two settings for
  tests and dev, like `CARAVANA_HOST_ROOT`: `CARAVANA_RELEASE_URL` (the latest-release address; https,
  or http on loopback) and `CARAVANA_UPDATER_DIR` (default `/var/lib/caravana-update`).
- **R9 The page across the restart.** While a run is open the run panel asks for itself every 5 s
  (`hx-trigger="every 5s"`; htmx ignores the failed requests while Caravana is down), and the page has
  `<noscript><meta http-equiv="refresh" content="15"></noscript>` in its head. The session is in SQLite,
  so the owner stays signed in across the update and across a rollback (the copied data has it).

## Security review

The question: can anything the console controls make root run something other than a checksum-verified
Caravana installer of a newer published release?

| What the console controls | Where it goes | Why it can't escalate |
| --- | --- | --- |
| The request file's bytes | Read by root with `head -c 256`, after checking it is a regular file and not a symlink; parsed with `sed -n 's/^KEY=//p'`, never sourced | `ID` must match `^[0-9a-f]{32}$` and `VERSION` `^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$` before either is used. Neither can hold a `/`, space, quote, `$` or `;`. An invalid request is logged to the journal by line number only (never its text) and left for Caravana to time out. |
| The version string | A path segment of the fixed download base and the tarball's name | The pattern leaves no `..` segment of its own and no `/`; the base is a constant (or the root-owned test file). |
| Which version | Must be newer than `/opt/caravana/current` (`sort -V`) | An attacker can only ask for a newer published release, which the owner could install by hand anyway. Downgrades are refused, here and by the installer. |
| The downloaded files | `SHA256SUMS` from the same release; both `install.sh` and the tarball must be listed and match | The same trust as the documented `curl … | sudo bash` install: GitHub's TLS and the release's assets. `install.sh`'s `CARAVANA_VERSION` must equal the version asked. |
| The status file | Written by root in its own directory | Caravana only reads it; the reason is one printable line, never the log, at most 300 characters. |

Also: the service unit `caravana.service` doesn't change (`NoNewPrivileges`, `ProtectSystem=strict`,
`ReadWritePaths=/var/lib/caravana`); the updater runs under its own unit, started only by its timer; the
timer runs nothing without a valid request; the API route needs a full token or a signed-in session
(cookie requests pass `http.CrossOriginProtection`), and deploy tokens are refused by the scope
middleware's default deny; the MCP server doesn't offer it.

## Constitution Check

| Principle | How this plan meets it |
| --- | --- |
| I. Small by Default | No Go module, no runtime dependency: a bash script and two systemd units from the installer. |
| II. One Model | Untouched: no app model or job change. |
| III. Safe, Reversible Deploys | Caravana's own upgrade keeps 012's rollback (data copied first, old binary back on a failed health check). Nothing about app deploys changes; apps keep running. |
| IV. Fail Loudly | Every refusal and outcome is recorded with its reason, shown on the page and, when it failed, on the needs-you list and as a message. A run never silently disappears (`lost`). |
| V. API-First | The console and `requestUpdate` call the same `Service.RequestUpdate`; `api/openapi.yaml` 0.33.0 in the same commit. |
| VI. Nomad-Native | No change. |
| VII. Secure by Default | The console never gets root (security review); the request holds a version and nothing else; downloads are checksum-verified. |
| VIII. Plain, Tested Go | Table tests for the request and outcome round trip, refusals, the watcher, automatic updates and the page; `install-test.sh` runs the whole thing on a VM; shellcheck on the script. |

No Complexity Tracking entry is needed.

## Project structure

```text
deploy/install/caravana-update.sh (new)       the root updater (contracts/updater.md)
deploy/install/caravana-update.service (new)  oneshot, root, TimeoutStartSec=30min
deploy/install/caravana-update.timer (new)    OnCalendar=minutely
deploy/install/install.sh                     install the three and the directory; --uninstall removes them
tools/release.sh                              host_files gains the three
deploy/dev/install-test.sh                    the update steps (quickstart.md)
internal/config/config.go                     CARAVANA_RELEASE_URL / -release-url, CARAVANA_UPDATER_DIR / -updater-dir
cmd/caravana/main.go                          wire both
internal/store/migrations/0023_update_runs.sql  update_runs; release_check gains auto_update, auto_update_hour
internal/store/updateruns.go (new)            insert, get, open run, list, settle; the settings
internal/service/updaterun.go (new)           RequestUpdate, the request file, reading the status, the watcher, automatic updates
internal/service/updates.go                   UpdatesView gains Updater, Run, Auto; history merges runs
internal/service/attention_update.go (new)    caravana_update_failed
internal/service/sentenceparts.go, activity.go  the kind's words; the run's Activity line
internal/api/updates.go, api/openapi.yaml     requestUpdate, listUpdateRuns, getUpdates/putUpdateSettings fields, codes; 0.33.0
internal/ui/opsupdates.go, templates/opsupdates.html, templates/opsupdatesconfirm.html (new), style
DESIGN.md, docs/plan.md, CHANGELOG.md, CLAUDE.md, deploy/install/README.md
```
