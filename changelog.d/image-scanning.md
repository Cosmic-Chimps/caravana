### Added
- **Image scanning, an add-on.** Operator › Security installs the image scanner (Grype, pinned by digest; about
  3.5 GB of disk on one server and about 204 MB downloaded a day, nothing until you install it). Installed, it
  scans each image a healthy deploy brings and, every day, every image a running app uses, from a kept SBOM so the
  rescan pulls nothing. Findings show on the app's Deployments tab (Security panel, counts on each history entry)
  and on Operator › Security, with advisory links, fixed versions, EPSS and "known exploited". A package with a
  fixable critical (or high, a setting) is a needs-you item `image_vulnerable` and a notification. An optional gate
  refuses to deploy an image with a fixable critical (422 `image_vulnerable`). Nothing about your servers or apps
  is sent anywhere; a mirror of the database can be set. API 0.49.0 (`/v1/addons`, `/v1/scans`,
  `/v1/apps/{id}/scan`), MCP tool `get_app_scan`. See `docs/image-scanning.md`.
