# Research: Published ports

## R1 Nomad static ports on a host network (lab, observed 2026-11-16, Nomad 2.0.7, caravana-lab)

A hand-written job with a bridge-mode group and `port "turn" { static = 3478, to = 3478, host_network = "public" }`
on a node whose client declares `host_network "public" { interface = "eth0", cidr = "0.0.0.0/0" }`:

- The CNI portmap rules DNAT `192.168.5.15:3478` (eth0's address) to the container for **both `tcp` and `udp`**
  (`CNI-DN-…` rules for `-p tcp` and `-p udp`). Nomad maps both protocols for every port, dynamic ones included,
  so `protocol` can't limit what is mapped; it records what the app speaks.
- A socat echo answered `pong-udp` and `pong-tcp` on `192.168.5.15:3478`.
- After `job stop -purge`, no rule mentions 3478: the port closes with the copy.
- The node's JSON lists `HostNetworks: {"public": {"Name": "public", "Interface": "eth0", "CIDR": "0.0.0.0/0"}}`,
  which `Nodes().Info` returns; a pre-deploy check can read it.
- Nomad refuses to place a group needing a host network a node lacks, so declaring `public` only on some servers
  pins published services to them with no constraint in the job.

## R2 The firewall

The installer's comment on `network_interface` (feature 012) already records that Nomad's port mappings are DNAT
rules a host firewall can't block: after DNAT the packet is forwarded to the container, so it never reaches ufw's
INPUT rules, and the installer sets `ufw default allow routed` (containers need forwarding). The CNI `firewall`
plugin also accepts forwarded traffic to the container. So:

- Writing ufw rules would change nothing about reachability, and would need root (Caravana runs unprivileged; a
  rule left behind by a failed close would stay open with nothing behind it).
- What keeps every other port closed is that only published ports are mapped on the public interface's address;
  dynamic ports stay on `caravana0`.

**Decision**: no firewall change. "Caravana opens the port and closes it" is Nomad's mapping, which lives exactly as
long as the copy. `install-test.sh` checks the path through ufw from a container on the host's Docker bridge
(forwarded, as external traffic is), and `TestGate` from the Mac.

## R3 Several servers (Phase 3)

The question from the task: pin the app to a node, or open the port on every node? A static port answers only on
the server where the copy runs, and the owner points DNS at one address. The model is the port's `published`
number; *where* is the set of servers that declare the `public` host network. Today that is the first server
only (the one with `meta.caravana_ingress`, where Traefik runs). A later "proxy per server" feature may declare it
on more servers and relax the one-copy rule, without changing the model.

## R4 Port ranges and calls (desk research, not run)

- **LiveKit** (MatrixRTC / Element Call) can multiplex all media on one UDP port (`rtc.udp_port`, e.g. 7882) plus
  one TCP fallback (`rtc.tcp_port`, 7881) and its HTTP/WebSocket API (7880, a domain through Traefik). That fits:
  two published ports and a domain.
- **coturn** relays on `min-port`–`max-port`. Each relay port would be one published port (16 at most per service),
  so only a very small range fits; it serves few concurrent calls. Not promised.
- Ranges are out of scope; nothing in the model blocks a later `publishedEnd`.

## R5 Compose compatibility

Today `"8080:8080"` warns and is ignored, and Aspire's output and many files carry such entries. Honoring the file's
`published` would make those deploys expose ports publicly, refuse `"80:80"` and drop canaries. So the file's
`published` stays a warning, and publishing takes an explicit overlay entry (`publish: [{port}]`), like domains.
A UDP protocol in the file was refused; it is now honored (internal traffic by service name works for UDP).
