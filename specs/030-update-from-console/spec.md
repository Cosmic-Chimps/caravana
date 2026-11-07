# Feature Specification: Update Caravana from the console

**Feature**: `030-update-from-console` | **Branch**: `alta/earthy-thistle-j2m8` | **Created**: 2026-11-06
**Input**: CodeAlta task `2026-11-04-update-caravana-from-the-console-c4tc` (the maintainer, 2026-11-04:
have the console update Caravana). Step 2 of `docs/plan.md` › "Updates from the console"; step 1, the
notice, is 027's Updates page.

## Context

The Updates page (027) shows a newer Caravana with its notes and the installer command to run over SSH,
and says "caravana doesn't apply updates from the console yet." Updating means rerunning the newer
release's installer as root, which already upgrades with rollback (012): it copies the data, starts the
new version, and puts the old binary and the copied data back when the new one fails its health check.

The console must never get root. Caravana runs as the `caravana` user with `NoNewPrivileges` and
`ProtectSystem=strict`; it can write only `/var/lib/caravana`. This stays true: the console records a
request that holds a version and nothing else, and a root systemd timer installed by the installer
picks it up, checks it, and runs that release's checksum-verified installer.

## User Scenarios & Testing

### User Story 1: Update to the newer release from the Updates page (P1)

The owner sees "caravana 0.16.0 → 0.17.0" on Updates, presses **Update to 0.17.0…**, reads what will
restart, and confirms. Within a minute the server's updater starts the release's installer. The console
stops for about a minute and comes back on 0.17.0; the page says it is running 0.17.0 and how long it
took, without the owner reloading. Apps keep answering throughout.

**Acceptance**:
1. Given a newer release and the updater installed, when the owner confirms, then a request with only
   the version is written, the page shows the run waiting, then running, then "caravana 0.17.0 is
   running", and `/v1/health` answers 0.17.0.
2. Given the run, then Past updates has "caravana 0.16.0 → 0.17.0" with who asked and how long, and
   Activity has one line for it.
3. Given the confirmation page, then it names the version, says the console stops for about a minute
   and apps keep running, names the Nomad and Traefik versions the release changes and what that
   restarts, and says the old version comes back if the new one fails its health check.

### User Story 2: A release that doesn't come up is rolled back, and the owner is told (P1)

The new version doesn't answer its health check. The installer puts the old version and its data back.
The page says "caravana 0.17.0 didn't start, so 0.16.0 runs again" with the installer's reason and
where the whole log is; the needs-you list has an item and every channel gets a message.

**Acceptance**:
1. Given a broken release, when the update runs, then the old version answers again with its data, the
   run is recorded as rolled back with the reason, a needs-you item opens, and `problem.opened` is sent.
2. Given that item, when a later update succeeds (from the console or by hand), then the item resolves.
3. A failed update is never retried by itself.

### User Story 3: Patch releases install by themselves at a set hour (P2)

On Updates › How this server updates, the owner turns on "Install patch releases (0.16.x) by
themselves" at 03:00 UTC. When 0.16.3 is out and the clock reaches 03:00 UTC, Caravana requests the
update itself; the run is the same as a manual one and is recorded as automatic. A minor release
(0.17.0) waits for the owner. Off by default.

### User Story 4: The console still can't run anything as root (P1)

The request is a file in Caravana's data directory with an ID and a version string. The updater runs
as root, validates the version against the installer's release pattern before using it, checks it is
newer than the running version, downloads only from the release download address, checks the
installer and the tarball against `SHA256SUMS`, and runs nothing without a valid request. No other
value from the console reaches a shell, a URL or a path.

**Acceptance**: a request with `VERSION=$(id)`, a path, a URL, an older version or a version that
isn't published is refused without running anything; a symlinked or oversized request is ignored.

### Edge Cases

- The updater isn't installed (a host installed before this feature, or a dev run): no Update button;
  the page says to run the newer installer once on the server, which adds the updater.
- A development build (`dev`, `0.0.0-dev.*`): no Update button (027's "development build" sentence).
- A run is already open: a second request is refused (409 `update_running`).
- The version asked isn't the newest release Caravana has seen, or isn't newer than the running one:
  refused (409 `update_not_offered`).
- The updater doesn't pick the request up within 5 minutes (the timer is off, the host was down): the
  run is recorded as not started, with the reason, and the request file is removed.
- The updater stops mid-run without an outcome (killed, power loss): after 45 minutes the run is
  recorded as not finished.
- Caravana restarts during the run (it does, by design): the new process reads the updater's status
  and carries on; a rolled-back process has the request in its copied data and does the same.
- Release check off: the Update button still shows a release seen before it was turned off; automatic
  updates don't run while the check is off (they need it to know a release exists).
- GitHub unreachable during the run: the updater refuses with "couldn't download …"; nothing changes.

## Requirements

### Functional

- **FR-001** The console and the API record an update request for one version: a row in SQLite and the
  file `<data dir>/update-request` holding `ID=<32 hex>` and `VERSION=<version>`, written atomically.
- **FR-002** Caravana refuses a request on a development build, without the updater, while a run is
  open, or for a version that isn't the newest release seen and newer than the running one.
- **FR-003** The installer installs `/usr/local/libexec/caravana-update`, `caravana-update.service` (a
  root oneshot) and `caravana-update.timer` (every minute), and the updater's directory
  `/var/lib/caravana-update` (root, 0755) with its marker file; `--uninstall` removes all of them.
- **FR-004** The updater, as root: ignores a request that isn't a plain regular file of at most 256
  bytes; reads `ID` and `VERSION` as text (never sourced); refuses an ID that isn't 32 lowercase hex or a
  version that doesn't match `^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`, or isn't newer than
  `/opt/caravana/current` (`sort -V`); downloads `SHA256SUMS`, `install.sh` and the arch's tarball from
  `https://github.com/Cosmic-Chimps/caravana/releases/download/v<version>/`; checks both files against
  `SHA256SUMS` and that the installer's `CARAVANA_VERSION` is the version; runs
  `bash install.sh --from <dir>`; writes its status before and after; keeps the whole log root-only.
- **FR-005** The updater's status file says `running`, then `updated`, `rolled_back`, `failed` or
  `refused`, with the request's ID, the versions, the times and a one-line reason (printable, at most
  300 characters, never the log).
- **FR-006** Caravana follows an open run every 5 seconds: it records the updater's states, removes the
  request file once the run is final, records `lost` with a reason when nothing started within 5
  minutes or nothing finished within 45, and publishes the change to the page's live stream.
- **FR-007** A rolled back, failed, refused or lost run is a needs-you item `caravana_update_failed`
  (failed level for `failed`, warning otherwise) until a later run succeeds or the running version is
  at least the run's version; through the existing problem events it is a notification. A successful
  run is one Activity line and one Past updates row; the version change it explains isn't listed again.
- **FR-008** Automatic updates: off by default; when on, at the set UTC hour, Caravana requests the
  newest release when it is a patch release of the running version (same major and minor, no suffix on
  either), the release check is on, the updater is installed, no run is open and no run was ever made for
  that version. Recorded with the trigger `automatic`.
- **FR-009** API (0.33.0): `requestUpdate` (`POST /v1/updates/runs`, body `{version}`, 202 with the
  run), `listUpdateRuns` (`GET /v1/updates/runs`, last 20), `getUpdates` gains the updater's state, the
  run to show and the automatic setting, `putUpdateSettings` gains `autoUpdate` and `autoUpdateHour`.
  Codes `update_unavailable`, `update_running`, `update_not_offered`. Deploy tokens are denied by the
  default deny. The MCP server offers none of it.
- **FR-010** The Updates page follows `contracts/page-updates.md`: the Update button and its
  confirmation page, the run panel that keeps itself current while a run is open (with and without
  script), the outcome, the automatic setting, Past updates, and How to apply's new words.
- **FR-011** OS packages and reboots stay by hand, as in 027.

### Key Entities

- **Update run**: ID, version, from-version, trigger (`console`, `api`, `automatic`), token name,
  requested, started, finished, state (`requested`, `running`, `updated`, `rolled_back`, `failed`,
  `refused`, `lost`), reason.
- **Update settings**: automatic on/off and the hour (0–23 UTC), beside the release check's setting.

## Success Criteria

- **SC-001** `install-test.sh` updates a VM from the API: request, the timer runs it, the new version
  answers, the run reads `updated`; a broken release is rolled back and the run reads `rolled_back`
  with a needs-you item; a crafted request runs nothing.
- **SC-002** The page states the outcome after the restart without the owner reloading (script) or
  within 15 seconds (no script).
- **SC-003** No path from the console to a root shell: the security review in `plan.md` holds, and the
  service unit is unchanged (`NoNewPrivileges`, `ProtectSystem=strict`, one writable path).
