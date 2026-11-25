### Added
- **Drop the earlier certificates, from the console.** After you upload a certificate, a domain that already had a
  Let's Encrypt certificate keeps presenting it, and the uploaded certificate's row says "Still on their earlier
  certificate". Settings › Certificates now has a **Drop the earlier certificates** button for it: the agent on the
  first server removes from Traefik's store the ACME certificates whose every name an upload covers, Traefik restarts
  once (a few seconds without HTTPS), and the page says what was dropped. Only names are exchanged, never a
  certificate or a key. The API is `dropEarlierCertificates` (`POST /v1/certificates/drops`), with
  `listCertificateDrops` and `getCertificateDrop`.

### Changed
- **The installer drops covered certificates with Caravana's own agent.** Rerunning the installer still removes the
  ACME certificates an upload covers and says which, with the same rule as the button, and no longer needs
  `python3` for it.

### Upgrade
- The button needs the first server's agent to be up to date: rerun the installer on that server once. Until its
  agent reports that it holds Traefik's certificate store, the button answers that the installer must be run.
