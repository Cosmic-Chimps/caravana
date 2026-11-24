### Added
- **Network and disk throughput per server.** Each server's page (Operator › Servers) has a **Network and disk**
  section: bytes per second in and out of its public network interface, bytes read and written on the disk that
  holds Nomad's data and Docker, and how busy that disk is. **Monitoring › This host** draws two small charts of
  the same over the page's range. The server's agent reads the kernel's counters and Caravana works out the rates,
  so nothing new is installed or logged in to. A server without an agent, or with an older one, shows a dash and
  says why. `GET /v1/host` has the same numbers under `io`. See `docs/host-io.md`.

### Upgrade
- Rerun the installer on a server to get the numbers for it: an older agent reports no I/O, and the page says so.
