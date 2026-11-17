# Published ports: a fixed TCP or UDP port for apps that aren't HTTP

Caravana routes web traffic by host name through Traefik, on ports 80 and 443. Some apps need a port of their own:
a TURN server or LiveKit for calls, a game server, MQTT, SMTP, a database you reach from outside. A service can
**publish** a port: a fixed port on the public address of your server, open while the copy runs.

## Publish a port

Add `published` (and, when the app speaks UDP, `protocol`) to a port of the service, through the API, the Compose
overlay or Aspire's publisher:

```json
{
  "name": "turn",
  "image": "coturn/coturn:4.6",
  "ports": [
    { "name": "http", "containerPort": 8080 },
    { "name": "turn", "containerPort": 3478, "protocol": "tcp+udp", "published": 3478 }
  ],
  "health": { "port": "http", "path": "/healthz" }
}
```

- `published`: the port on the server, 1 to 65535. The port in the container stays `containerPort`.
- `protocol`: `tcp` (the default), `udp` or `tcp+udp`. Nomad opens both protocols for every port, so this says what
  your app speaks. It matters in two places: a **health check** and a **domain** need a port that speaks TCP.
  A service whose only port is UDP has nothing to check; give it a second, TCP port for the check, as above.
- At most 16 published ports per service.

From **Compose**, ports with a UDP protocol (`"3478:3478/udp"`) are read as they are, and a TCP and a UDP entry for one
container port become one port with `tcp+udp`. The file's `published` numbers are *not* opened by themselves: many
files carry them, and a deploy that publishes `80:80` is not what the author meant. Say what to open in the overlay:

```json
{ "services": { "turn": { "publish": [{ "port": 3478 }, { "port": 7882, "published": 17882 }] } } }
```

`port` is the container port; `published` defaults to the same number.

## What you get

- The port opens on **the address of the first server** (the one your DNS points at and where Traefik runs), over
  IPv4, **only while the copy runs**. Nomad adds the mapping when the copy starts and removes it when it stops, so
  stopping or deleting the app closes the port, and nothing else on the server is opened.
- **One copy, and a short gap on each deploy.** One address can hold a fixed port once, so a deploy stops the old copy
  before it starts the new one (the way a service with a volume deploys). The new copy is still health-checked and
  reverted if it fails. `replicas` above 1 is refused.
- Refused, naming the field: `22`, `80`, `443`, `4646`, `4647`, `4648`, `7070`, `7071`, `8081` and `51820` (SSH, Traefik,
  Nomad, Caravana and the mesh); a number published twice in an app; a port another app publishes (`409 port_taken`,
  stopped apps included: starting one again must not fail).
- A promotion keeps the **target's** published number for each port name, and Copy to leaves them out: an address
  belongs to an environment.
- Exporting the app writes `port "turn" { static = 3478  host_network = "public" }`, and the export's header says the job
  needs a Nomad client with that network.

## The firewall

Caravana writes **no firewall rule**. Nomad's port mapping is a DNAT rule applied before the host firewall's input
rules, and the installer already allows forwarded traffic for containers, so a `ufw` rule for the port would change
nothing and would have to outlive the copy. What keeps everything else closed is that only published ports are mapped
on the public address; every other app port stays on the private `caravana0` interface.

If something else on the server listens on the same port on all addresses, the copy's mapping comes first and that
process loses its outside traffic. Caravana can't see such a process; pick a free number.

## Hosts

The first server's Nomad client declares the host network `public` on the interface of its default route. The installer
and `caravana node` write it, so a host installed before this feature has it after you **rerun the installer** (Nomad
restarts once). Until then a deploy that publishes a port is refused with `409 no_public_network` and says to rerun it.

Joined servers don't declare it yet: published services run on the first server. Spreading them over servers (a proxy
on every server) is a later part of Phase 3.

## Calls: what fits

- **LiveKit** (MatrixRTC, Element Call) can carry all media on one UDP port plus one TCP fallback, with its HTTP and
  WebSocket API on a domain: two published ports and a domain. That fits.
- **coturn** relays on a port range. Each port is one published port (16 at most per service), so only a very small
  range fits; it will serve few concurrent calls. Port ranges are not part of this feature.
