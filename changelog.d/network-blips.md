### Added
- **Copies survive a short network cut.** When a server stops answering, Nomad now keeps its copies running for
  five minutes (`disconnect.lostAfterSeconds`, 60 to 86400, settable per service through the API and the Compose
  overlay) instead of replacing them after about 45 seconds. A service without a volume or a published port gets
  replacements meanwhile and keeps its original copies when the server returns; a service with a volume or a
  published port waits for its server. A cut longer than the limit behaves as before. A service with a domain
  also gets a Traefik health check on its `health.path`, so the proxy stops sending to the unreachable copy
  within one interval. A service with a domain and only a TCP health check, or an HTTP check on another port,
  keeps today's behaviour, and setting `disconnect` on it is refused. See `docs/network-blips.md`. API 0.48.0.
- **A cut-off server reads "disconnected", not "down".** While Nomad keeps the copies of a server it stopped hearing,
  the console and the API (`state: "disconnected"`, `disconnectedSince`, `keptCopies`; a copy's `status:
  "disconnected"`) say so, the server takes no new work, and the needs-you list stays quiet. If the copies are lost
  after `lostAfterSeconds`, the server is down and the `server_down` item opens as before.

### Upgrade
- Apps get the disconnect policy and the Traefik health check at their next deploy; running apps don't change
  until then.
