### Added
- **Requests: see each HTTP request Traefik served, per app.** The new Requests page (sidebar, and a tab on every
  app after Logs) lists the requests of one project › environment newest first: when, the service, the method,
  the host and path, the status, how long it took, the bytes and the client's address. Filter by status class
  (2xx to 5xx), method, path prefix, minimum duration and client; a histogram of the range is stacked by status;
  a row opens the Logs around its time. Monitoring's Errors chart links to the 5xx of its range (a click on the
  plot opens the minute you point at) and its apps table has a Requests link per service. Only the path is
  recorded: Traefik drops the query string and every header before the line exists, and Caravana stores
  nothing. The page says how far back it read. `GET /v1/requests` (API 0.52.0). See `docs/requests.md`.

### Changed
- **The Logs page accepts a window.** `/logs?from=…&to=…` (RFC 3339) reads exactly that window, paused, which
  is where a request's row leads. The range bar returns to the live ranges.

### Upgrade
- Rerun the installer on the first server: Traefik's job now writes its access log, so Traefik restarts once
  (a short gap on ports 80 and 443). Until then the Requests page says "Traefik isn't writing its access log
  yet" and the API answers `accessLog: false`.
