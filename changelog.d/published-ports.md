### Added
- **Published ports.** A service can publish a port: a fixed TCP or UDP port on your server's public address, for apps
  that aren't HTTP (a TURN server, LiveKit, a game server, MQTT). Set `published` and `protocol` (`tcp`, `udp` or
  `tcp+udp`) on a port through the API, or publish a port of a Compose file with the overlay's `publish` list (the
  file's own `published` numbers still only warn). The port is open while the copy runs and closed when it stops or the
  app is deleted; no firewall rule is written. Such a service runs one copy, and its deploys stop the old copy before
  starting the new one. Reserved ports (22, 80, 443, Nomad's, Caravana's and the mesh's), a port another app publishes
  (`409 port_taken`) and a health check or domain on a UDP-only port are refused naming the field. A promotion keeps
  the target's numbers and Copy to leaves them out. Configure shows them read-only. API 0.45.0. See
  `docs/published-ports.md`.

### Changed
- A Compose file's `ports` and `expose` now accept UDP (`"3478:3478/udp"`, `3478/udp`); a UDP port was refused before.
  The warning for a file's `published` port now says how to publish it.

### Upgrade
- Rerun the installer on the first server so its Nomad declares the host network that carries published ports (Nomad
  restarts once). Until you do, a deploy that publishes a port is refused with `409 no_public_network`.
