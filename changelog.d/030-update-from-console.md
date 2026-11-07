### Added
- **Update Caravana from the console.** When a newer release is out, Operator › Updates has **Update to
  X…**: a confirmation page says what restarts (the console for about a minute, and Nomad or Traefik when
  the release changes their versions; apps keep running), and the update starts within a minute. The page
  follows it while the console restarts and says how it went: updated, rolled back with the reason, or
  failed. A rolled-back or failed update is on the needs-you list and sent to every notification channel.
  The console still can't run anything as root: it only writes a request holding a version, and a root
  timer from the installer (`caravana-update.timer`) checks the version, downloads that release's
  installer and binary, verifies them against the release's checksums and runs the installer, which puts
  the old version and its data back if the new one fails its health check. **Automatic caravana updates**
  (on Updates, off by default) install patch releases (0.18.x) at a set UTC hour, and never retry a version
  that failed. API 0.33.0 (`requestUpdate`, `listUpdateRuns`, the updater's state and the automatic
  setting on `getUpdates` and `putUpdateSettings`, the needs-you kind `caravana_update_failed`).

### Upgrade
- To update from the console, rerun the installer of this release once on the server: it adds the updater
  (`/usr/local/libexec/caravana-update`, `caravana-update.service` and `.timer`, and
  `/var/lib/caravana-update`). Until then the Updates page says to.
