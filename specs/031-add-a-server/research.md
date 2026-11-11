# Research: Add a server

Lab, 2026-11-10: three Lima VMs (Ubuntu 24.04, 2 vCPU, 2 GB, arm64, Nomad 2.0.7, kernel 6.8, systemd 255,
wireguard-tools 1.0.20210914) on Lima's `user-v2` network, where VMs reach each other (192.168.104.x).
Everything marked "lab, observed" was done by hand there, on a first server installed from `main`
(d13c37c) and two more joined by hand. The scripts are not kept; the facts are.

## L1. Lima: `vzNAT` isolates guests, `lima:user-v2` doesn't

With `--network vzNAT` each VM gets 192.168.64.x and reaches the Mac and the internet, but ARP between two
guests fails (lab, observed: `192.168.64.3 dev lima0 FAILED`). With `--network lima:user-v2` the VMs
reach each other over ICMP, TCP and UDP (a `nc -u` datagram on 51820 arrived). `user-v2` needs no root
and no `socket_vmnet`. The cluster test uses it. One VM got the address `192.168.104.1`, the gateway's
address in Lima's `networks.yaml`; it still works.

## L2. A running single server converts in place, in two Nomad restarts

On the first server installed from `main`, with whoami (3 copies) and a two-service app running:

1. Create the WireGuard interface (10.98.0.1), write a CA, a server certificate and a gossip key, change
   `nomad.hcl` (`addresses`/`advertise` rpc and serf on 10.98.0.1, `tls { rpc = true, http = false,
   verify_server_hostname = true }`, `server { encrypt }`, `client { meta { caravana_ingress } }`) and
   restart Nomad. Leader after 4 s, every job, token and variable kept, whoami answered 80 of 80 calls
   made every half second (lab, observed).
2. But raft still lists the server at `127.0.0.1:4647`, and Nomad can't fix it by itself:
   `failed to reconcile: error removing server with duplicate ID …: need at least one voter in
   configuration`, repeated by autopilot every 10 s (lab, observed). A second server could never reach
   the leader. The fix is Nomad's outage recovery: stop Nomad, write
   `/opt/nomad/data/server/raft/peers.json` as `[{"id": "<data_dir/server/node-id>", "address":
   "10.98.0.1:4647", "non_voter": false}]`, start Nomad. It logs `found peers.json file, recovering Raft
   configuration` and deletes the file; `raft list-peers` then says `10.98.0.1:4647 leader`, state kept,
   whoami 80 of 80 (lab, observed).

A fresh install starts on the mesh address, so it never needs step 2.

On the first of the two restarts one copy (a group without ports) failed its network hook (`network
namespace already exists but was misconfigured`) and Nomad replaced it; the second restart failed none.
It looks like a Nomad restart hazard, not something this change causes; noted, not chased.

## L3. Nomad needs its interfaces before it starts

After a reboot Nomad started before `caravana0` and the mesh interface existed: `Failed to parse
network-interface: invalid interface name "caravana0"`, five tries, then systemd's start limit (`Start
request repeated too quickly`) left it failed until `reset-failed` (lab, observed, with interfaces made by
hand). So the mesh unit is `Before=nomad.service`, Nomad's unit `Requires=` and `After=` it, and Nomad's
unit gets `StartLimitIntervalSec=0` with `RestartSec=2`.

## L4. A client joins with a certificate and nothing else

A client with `tls { rpc = true }`, a certificate for `client.global.nomad` signed by the cluster CA and
`client { servers = ["10.98.0.1:4647"] }` registered and became `ready` in seconds. It needs no ACL token:
the certificate is the admission. Without the mesh peer it can't reach 4647 at all (Nomad's RPC listens on
the mesh address only).

Certificates as `nomad tls cert create` makes them, which ours mirror: ECDSA P-256; key usage Digital
Signature and Key Encipherment; extended key usage server auth and client auth on both kinds; SANs
`server.global.nomad` (or `client.global.nomad`), `localhost`, `127.0.0.1`, plus the address given.
`verify_server_hostname = true` is what stops a client certificate from acting as a server.

## L5. App ports route over the mesh with `caravana0` = 10.99.0.N

Each server keeps the `caravana0` dummy interface with its own /32 (10.99.0.1 on the first, as today;
10.99.0.2 on the second), `network_interface = "caravana0"`, and WireGuard's allowed addresses for a peer
include that /32. Traefik on the first server listed `http://10.99.0.2:26697` (and two more) for whoami's
copies on the second and answered 200 in 6 to 8 ms (lab, observed). The packet arrives on the mesh
interface, Nomad's port mapping (a DNAT rule on the destination address) sends it into the container, and
the reply goes back through the tunnel. Nothing on the first server moved: its ports stay on 10.99.0.1.

## L6. Service names work across servers when container subnets are routed

Each server's Nomad bridge gets its own subnet (`client { bridge_network_subnet }`): the first keeps
Nomad's default `172.26.64.0/20`, server N ≥ 2 gets `10.97.(4N).0/22`, and a peer's allowed addresses
include its subnet. With `front` on the first server (172.26.64.8) and `back` on the second (10.97.8.4),
the `names` sidecar wrote `10.97.8.4 back` into `front`'s `/etc/hosts` and `wget http://back:80/`
answered (lab, observed). Traffic leaves a server masqueraded as its mesh address, which the peer's
allowed addresses accept. `ufw default allow routed` (already the installer's) lets it through.

## L7. Caravana's reads reach copies on another server through the local agent

Every agent advertises `http = 127.0.0.1` and listens for HTTP on loopback only. Nomad's Go client first
dials the copy's node at its advertised HTTP address, which from the Caravana host is the local agent;
that agent forwards to the right node over RPC. Through Caravana's API on the first server, with its
scoped token (lab, observed): `listCopies?usage=true` returned CPU and memory for copies on the second
server, `getLogs` read their files, `runCommand` ran in one in 133 ms, `GET /v1/host` answered. The copy
already carries its node's name (`node`).

`deploy.HostStats` takes the first node that is `ready`, so with several servers it describes an
arbitrary one. It must take a node.

## L8. Without a spread rule, copies pile up on one server

A redeploy of whoami (3 copies) with two servers put all three on the second, emptier, server (lab,
observed). A job with `spread { attribute = "${node.unique.id}" weight = 100 }` and three servers put one
copy on each, and Traefik answered from all three (lab, observed).

## L9. Three Nomad servers, by promoting clients in place

A joined client becomes a server by getting a server certificate, the gossip key, `server { enabled =
true; encrypt; server_join { retry_join = ["10.98.0.1:4648"] } }` (no `bootstrap_expect`), RPC and serf on
its mesh address, and a restart. Its running copies kept running across the restart. Autopilot made it a
voter after its 10 s stabilization: after the second and third were promoted 20 s apart, `raft list-peers`
showed three voters (lab, observed).

## L10. Losing one server

- **A joined server (hard stop):** with a 3-copy spread app called every half second, 5 of 150 calls
  failed, between 7.7 s and 21.5 s after the stop (each a 2 s timeout to the dead copy). Nomad marked the
  node `down`, the copy `lost`, and started a replacement on another server within a minute. Autopilot
  removed the dead server from raft (`CleanupDeadServers`), leaving two voters; when the VM came back it
  rejoined and became a voter again (lab, observed). Copies don't move back.
- **The first server (leader, Caravana, Traefik):** a server on another VM became leader; copies on the
  other two kept running; `nomad` on the second answered with the management token. The console and
  public traffic were down, since both live on the first server. When it came back it rejoined as a
  follower, Caravana answered through its non-leader agent and the app answered (lab, observed).

Until Nomad notices (heartbeat), Traefik still sends a share of requests to the dead copy. A health check
in Traefik's own load balancer would shorten that; it belongs to "Proxy and routing".

## L11. Nomad reloads a renewed certificate on SIGHUP

Replacing an agent's certificate and key files and running `systemctl reload nomad` (SIGHUP) kept the
same process, logged `reloading HTTP server with new TLS configuration`, and the gauge
`nomad.agent.tls.cert.expiration_seconds` went from 31,535,630 to 2,591,999 (lab, observed). The raft
peers and the nodes stayed as they were. So a renewal needs no restart.

## L12. Volumes are created one by one, with no node

`deploy.EnsureVolumes` creates each dynamic host volume without a node, so Nomad chooses one per volume.
With several servers, two volumes of one service can land on different servers and the service can never
be placed (read in the code, not provoked). They must be created on one server.

## L13. Three small facts the installer relies on

- `nomad var put -force caravana/cluster/ca cert=@ca.pem key=@ca-key.pem` stores the files' contents, and
  `nomad var get -item key caravana/cluster/ca` gives back bytes equal to the file (lab, observed with
  `cmp`).
- With ACLs on, `GET /v1/status/leader` and `GET /v1/status/peers` answer 200 without a token
  (`["10.98.0.2:4647","10.98.0.3:4647","10.98.0.1:4647"]`); `GET /v1/agent/self` answers 403. So the
  agent can tell whether its Nomad knows a leader and is a raft peer with no token of its own.
- Adding the `meta.caravana_ingress` constraint to the running Traefik job is an in-place update: the job
  went to version 1 and the allocation kept its ID and kept running (lab, observed). So the rerun on an
  existing host doesn't restart Traefik for it.

## Decisions

| # | Decision | Why |
| --- | --- | --- |
| D1 | **A full WireGuard mesh**, interface `caravana-mesh`, UDP 51820, one index N (1 to 63) per server: mesh `10.98.0.N`, app ports `10.99.0.N` on `caravana0`, containers `172.26.64.0/20` (N = 1) or `10.97.(4N).0/22` | Hub-and-spoke would make the first server a single point for Nomad itself; L5 and L6 show the two extra ranges make routing and names work with nothing moved on the first server |
| D2 | **TLS on Nomad's RPC and raft, not on its HTTP API** (`rpc = true`, `http = false`, `verify_server_hostname = true`), plus gossip encryption | The HTTP API listens on loopback on every server; TLS there would change Caravana, Traefik's provider and the CLI for no gain. What crosses machines is RPC, raft and serf |
| D3 | **Caravana is the CA.** The CA key lives in the Nomad Variable `caravana/cluster/ca`, the gossip key in `caravana/cluster/gossip`; nodes send a CSR and keep their private key | Constitution VII (the control plane's secrets live in Nomad Variables); a private key never leaves its server |
| D4 | **A root agent on every server, `caravana node …`, run by a timer**, that asks Caravana for its state every 30 s and applies values, never commands or paths | The 030 pattern: the console never gets root. A compromised console can add a peer or change a role, which is no more than it can already do through Nomad; it can't run anything as root |
| D5 | **Nodes call the console's public address** (`/v1/join`, `/v1/node/*`), with a one-time join token and then a node token | It is the managed tier's shape (servers connect out to the control plane), and a join has to cross the internet anyway |
| D6 | **One ingress: Traefik stays on the first server**, by a constraint on `meta.caravana_ingress` | Let's Encrypt's HTTP-01 with a certificate store per server fails when the challenge reaches another server; shared certificates and a proxy per server are "Certificates" and "Proxy and routing" |
| D7 | **Nomad servers: the first alone until three servers are ready, then the first and the two oldest others**, promoted one at a time, and a removed server's place taken by the next oldest | L9; two voters tolerate nothing, so there is no two-server raft on purpose |
| D8 | **Copies of a service spread across servers** (`Spreads` on a group with more than one copy) | L8 |
| D9 | **All volumes of a service on one server** | L12 |
| D10 | **Removing a server is for one that is down**, or a join nobody used; moving work off a live server is "Servers and the server page" | A drain is its own feature with its own page |
| D11 | **Pools: a server joins a named pool, default `default`; apps run in `default`** | The handoff's field. Other pools are for build servers later; the page says a server outside `default` takes no apps |
| D12 | **The agent is the `caravana` binary**, not a shell script | It parses JSON, makes keys and CSRs and renders `nomad.hcl`: tested Go (constitution VIII) |

## Rejected

- **Nomad distributing peers** (a system job rendering a variable onto the host): needs a privileged
  writer on the host anyway, and a cluster whose certificates expired has no channel left to fix them.
- **`network_interface` = the mesh interface** on every server: the first server's running copies and
  their registrations are on 10.99.0.1; moving them needs a redeploy of everything.
- **TLS on the HTTP API with client certificates:** see D2.
- **Traefik on every server** in this feature: see D6.
- **Demoting a Nomad server back to a client** when the count falls under three: a raft removal plus a
  reconfiguration, for a state the next join repairs.
