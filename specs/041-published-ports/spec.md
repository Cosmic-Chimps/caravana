# Feature Specification: Published ports (a fixed TCP or UDP port for apps that aren't HTTP)

**Feature Branch**: `alta/swift-tiger-phd5` (worktree; CodeAlta task `dfn8`)
**Created**: 2026-11-16
**Status**: Draft → Implemented
**Input**: CodeAlta task `dfn8` (raised 2026-10-26 while planning Continuwuity): "A service's port may be published:
a fixed host port, tcp or udp, on the host's public address, Nomad's static port instead of a dynamic one."

## Why

Caravana routes only HTTP, by host name, through Traefik on 80 and 443. Some apps need a port of their own: a TURN
server or LiveKit for Matrix calls (UDP), a game server, SMTP, MQTT, a database reached from outside. Today
Compose's `ports.*.published` is accepted with a warning and ignored, and every app port is on the private
`caravana0` interface.

## User Scenarios & Testing

### User Story 1 — Publish a UDP and a TCP port (Priority: P1)

The owner deploys a service whose port `turn` (container port 3478, protocol `tcp+udp`) is published as 3478.
After the deploy, `<server address>:3478` answers over TCP and over UDP from the internet. Deleting the app (or
removing `published`) closes the port.

**Independent test**: deploy a socat echo service with one published port through `deployApp`; a client outside
the copy gets the echo over TCP and UDP; delete the app and the port no longer answers.

**Acceptance scenarios**
1. Given a service with `ports: [{name: turn, containerPort: 3478, protocol: "tcp+udp", published: 3478}]` and a
   health check on a TCP port, when it is deployed, then the job's group has a static port 3478 on the host network
   `public` and the deploy ends healthy.
2. Given that app, when it is deleted, then nothing answers on 3478.
3. Given the same port with `published` removed, when it is deployed, then nothing answers on 3478 and the port
   is a dynamic one again.

### User Story 2 — Refusals that name the field (Priority: P1)

1. `published: 22` (or 80, 443, 4646, 4647, 4648, 7070, 7071, 8081, 51820) → 400 `invalid_field` on
   `services[i].ports[j].published`.
2. A port another app publishes → 409 `port_taken` on `services[i].ports[j].published`, naming the other app.
3. Two services of one app publishing the same port → 400 on the second one's field.
4. `replicas: 2` on a service with a published port → 400 on `services[i].replicas` ("runs one copy").
5. A health check whose port is UDP only → 400 on `services[i].health.port` (or `services[i].health` when the
   port was implied). A domain on a UDP-only port → 400 on `services[i].domains[j].port`.
6. No server offers published ports (a host not yet upgraded) → 409 `no_public_network` on the first published
   port's field, telling the owner to rerun the installer on the first server.

### User Story 3 — Compose (Priority: P2)

A Compose file with `ports: ["3478:3478/udp", "3478:3478/tcp"]` and the overlay
`publish: [{port: 3478}]` deploys one port `p3478` with protocol `tcp+udp`, published as 3478. Without the
overlay entry the file's `published` stays a warning (as today, with a message that names the overlay's `publish`),
so an existing Compose file with `"8080:8080"` changes nothing. A `publish` entry for a port the file doesn't have
is refused on `services.<svc>.publish[k].port`.

### User Story 4 — Console (Priority: P3)

Configure shows each service's published ports read-only ("3478 tcp+udp → 3478, on the address of server
`<name>`"), keeps them on a save of the service form, and the Compose form keeps the overlay's `publish` list
through a deploy from the form.

### Edge cases

- **One copy, stop then start.** A static port can be held by one copy per address, so a canary can't run beside
  the old copy: such a group deploys like a group with a volume (stop, then start; still health-checked and
  reverted). The deploy has a short gap; this is said in the docs.
- **Promotion and copy.** A published port is an address, and addresses belong to an environment: a promotion keeps
  the target's `published` per port name (a source-only port arrives unpublished), and Copy to drops every
  `published`, as it drops domains.
- **Stopped apps keep their ports.** A stopped app still holds its published port for `port_taken`: starting it
  again must not fail.
- **Loopback in Compose.** `"127.0.0.1:5432:5432"` is never published (the overlay is the only way to publish).
- **IPv4 only.** The `public` host network is the IPv4 address of the server's default-route interface.
- **Another process on the host** listening on the same port on all addresses loses external traffic to the
  copy (Nomad's mapping comes first). Caravana can't see such processes; the refused list covers its own.
- **Port ranges** (TURN's relay range) are not in this feature: each published port is one Nomad reservation.
  LiveKit's single-port UDP mode fits; coturn fits only with a few relay ports (research R4).

## Requirements

- **FR-001** `app.Port` gains `protocol` (`tcp`, the default when empty, `udp` or `tcp+udp`) and `published`
  (0 = not published, else 1–65535). Both are omitted from the document when unset, so no revision moves.
- **FR-002** A published port becomes a Nomad static port on the host network `public`, mapped to the container
  port. Nomad maps TCP and UDP for every port; `protocol` says what the app speaks and decides what a health check
  or a domain may use.
- **FR-003** Refused (field errors, constitution IV): a reserved port (22, 80, 443, 4646, 4647, 4648, 7070, 7071,
  8081, 51820), a duplicate inside the app, more than 16 published ports in one service, `replicas` above 1, a
  health check or a domain on a UDP-only port, an unknown protocol.
- **FR-004** A port another stored app publishes is refused with 409 `port_taken`.
- **FR-005** The host network `public` is declared by the first server's Nomad client only (the server whose
  address the owner points DNS at, where Traefik runs). A service with a published port therefore runs there.
  A deploy when no ready server declares it is refused with 409 `no_public_network`.
- **FR-006** A service with volumes and a published port gets its volumes on a server that offers published ports;
  volumes already elsewhere refuse the deploy on the published port's field.
- **FR-007** The port is open exactly while the copy runs: Nomad adds the mapping when the copy starts and removes
  it when the copy stops. No firewall rule is written (research R2).
- **FR-008** Compose: `protocol: udp` is honored on `ports` and `expose` (`/udp`); a TCP and a UDP entry for one
  container port make one `tcp+udp` port; the overlay's `publish` list publishes; the file's `published` alone warns.
- **FR-009** API 0.45.0: `Port.protocol`, `Port.published`, overlay `publish`, codes `port_taken` and
  `no_public_network`. The export writes the static port and names the `public` host network in its header.
- **FR-010** The installer renders the host network on the first server (the next run restarts Nomad once, as any
  config change does); the lab declares it too.

## Success Criteria

- **SC-001** In the lab, an echo service published on a TCP+UDP port answers both protocols from another copy,
  and stops answering after the app is deleted (gate step `published-ports`).
- **SC-002** On a real host (`TestGate` through `host.sh prove`), the same answers come from the Mac over the
  public address, through the host's firewall.
- **SC-003** Every refusal of User Story 2 names its field, in the lab and in unit tests.
- **SC-004** An existing app and an existing Compose file deploy exactly as before (golden and revision tests).

## Out of scope

Port ranges; publishing on joined servers (a proxy per server, Phase 3); IPv6; a console editor for published
ports (API, Compose overlay and Aspire first; the console shows and keeps them).
