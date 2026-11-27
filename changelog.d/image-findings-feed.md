### Changed
- **Live activity has one line per app for image findings, never one per package.** The Operator Overview's feed
  says "`uptime-kuma`: 9 fixable critical vulnerabilities found." when an app's first fixable findings appear and
  "Resolved: fixable vulnerabilities in `uptime-kuma`." when the last one goes, so a scan with dozens of findings no
  longer pushes everything else down. Lines a scan wrote per package before this release are no longer listed.
  Notifications and the needs-you items stay one per package.
