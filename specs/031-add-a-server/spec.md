# Feature Specification: Add a server

**Feature**: `031-add-a-server` | **Branch**: `alta/genuine-skiff-rmbk` | **Created**: 2026-11-10
**Input**: CodeAlta task `2026-11-05-add-a-server-join-command-wireguard-nomad-tls-nmsv`. The first
feature of Phase 3 (`docs/plan.md` › "Add a server", drawn as `AddServerScreen`, with the list from
`ServersScreen`). The maintainer chose (2026-11-05) the managed tier's shape: Caravana is the control
plane and the customer's own servers run Nomad, so this join path is also that tier's foundation.

## Context

Caravana runs on one server: the installer (012) sets up Nomad as server and client, Traefik and Caravana
on one VM. Nomad listens on loopback, app ports are on the private `caravana0` interface, and nothing
between machines exists. This feature lets the owner add servers from the console: a command with a
one-time token, an encrypted private network between servers, Nomad's own traffic authenticated with
certificates Caravana issues and renews, and three Nomad servers once there are three machines.

The console still never gets root (030's rule). Every server runs a small root agent, the `caravana`
binary on a timer, that asks Caravana what its state should be and applies values. It never runs a
command or writes a path that came from the console.

## User Scenarios & Testing

### User Story 1: Add a second server from the console (P1)

The owner opens Operator › Servers › Add server, names the server (or leaves the name to the machine),
and gets a command to run on the new machine as root. The token in it works once and for one hour. They
run it. The page shows the checks as they pass: connected, Docker running with its free disk, reaches the
other servers on the private network, ready to take work. Then the server is in the list with its CPU,
memory and disk, and new copies can be placed on it.

**Acceptance**:
1. Given an installed first server, when the owner makes a join command, then the answer holds the token
   once, the command, and when the token expires; the list shows the server as waiting.
2. Given the command run on a fresh Ubuntu 24.04 machine, then the machine installs Docker, Nomad and
   WireGuard, calls in with the token, gets its addresses, the CA's certificate and its own certificate
   (for a key it made and kept), joins the private network and registers with Nomad as a client.
3. Given the join, then each of the four checks turns true on the page without a reload, in order, and
   the server becomes `ready`; every other server has added it as a peer within a minute.
4. Given a token used once, expired, or never issued, then the join call is refused with one message
   that doesn't say which, and nothing is installed beyond packages.
5. Given the command run again on a server that already joined, then it changes nothing and exits 0.

### User Story 2: Three servers survive the loss of one (P1)

With three servers ready, Caravana makes the two oldest joined servers Nomad servers as well, one after
the other, so the scheduler has three voters. A service with several copies has them on different
servers. When one server is lost, its copies are replaced on the others, apps with copies elsewhere keep
answering, and the scheduler still has a leader, whichever server was lost.

**Acceptance**:
1. Given three ready servers, then within a few minutes all three run a Nomad server and the Servers
   page says the cluster survives the loss of one server.
2. Given a service with 3 copies and three servers, then each server runs one copy.
3. Given a joined server lost, then the app answers again within 90 seconds and all calls succeed after
   that, the lost copy runs on another server, and the list shows the server as down, with a needs-you
   item and a notification.
4. Given the first server lost, then Nomad on the others elects a leader and their copies keep running;
   when it comes back the console answers and the cluster is whole again. (The console and public
   traffic live on the first server, so both are down meanwhile; the page says so before it happens.)

### User Story 3: An existing single server keeps working and becomes server 1 (P1)

The owner of a host installed before this feature reruns the installer (or updates from the console). The
host gets the private network's interface, Nomad's certificates and gossip encryption, and is registered
as the first server. Every app keeps answering, and every job, secret, token and volume is as it was.

**Acceptance**:
1. Given a host installed by the release before this feature, with an app running, when the new
   installer runs, then the app keeps answering (a copy whose network Nomad can't verify after its
   restart is replaced, which is a few seconds for an app with one copy: research L17), Nomad's raft
   address is the private one, Nomad's RPC uses TLS, and Servers lists the host as ready.
2. Given a rerun, then nothing changes.
3. Given a host that hasn't been rerun (or a dev setup with no private network), then Servers lists what
   Nomad knows and Add server says to run the installer again first; nothing else in the console changes.

### User Story 4: What assumed one server still tells the truth (P2)

Apps reach each other by name across servers. An app's volumes are on one server and the page says which.
The Running tab names the server of each copy. Monitoring's host numbers, the disk warning and the server
page work per server. Public traffic enters through the first server.

**Acceptance**:
1. Given a two-service app with its services on different servers, then one reaches the other by name.
2. Given a service with two volumes, then both are created on the same server, its copy runs there, and
   the Volumes list names the server.
3. Given several servers, then each has its own CPU, memory and disk numbers, its own server page, and a
   disk that fills on any of them is a needs-you item naming the server.
4. Given a domain, then it is served by Traefik on the first server whichever server the copies are on.

### User Story 5: Certificates renew themselves (P2)

Each server's Nomad certificate lasts 90 days. With 30 days left its agent makes a new key, sends a CSR,
gets a new certificate and reloads Nomad without a restart. If a certificate gets within 14 days of its
end, the owner is told.

**Acceptance**:
1. Given a certificate with less than 30 days left, when the agent next runs, then the server has a new
   one, Nomad's process is the same and the node stays ready.
2. Given a server whose certificate has under 14 days left, then there is a needs-you item naming it.

### User Story 6: Take a dead server out (P3)

A server that is down, or a join nobody ran, can be removed from the list. Its token stops working and
the other servers drop it as a peer. A server that is running can't be removed here.

### Edge cases

- The join call's public address isn't the one other servers should use (NAT, a private network at the
  provider): `--address` on the join command says which.
- The new machine already has Nomad or Docker it didn't get from Caravana, isn't Ubuntu 24.04, has under
  2 GB, or already uses one of the private ranges: refused before anything changes.
- UDP 51820 is closed between two servers: the "reaches the others" check stays open and names the
  servers it can't reach.
- The console is served with Traefik's own certificate (`--acme none`, tests): the join command says the
  certificate isn't checked, and the page warns.
- A server joins a pool other than `default`: it takes no apps, and the page says so.
- Two servers ready, not three: one Nomad server; the page says a third server makes the cluster survive
  a loss.
- A Nomad server is removed while three or more servers remain: the next oldest becomes a Nomad server.
- 63 servers exist: the next join is refused with `cluster_full`.
- The agent can't reach the console: it keeps the state it has; nothing on the server changes.

## Requirements

### Functional

- **FR-001** The installer has two roles. `--first` (the default, today's install) sets up Nomad as
  server and client, Traefik, Caravana, the private network's interface and the cluster's CA.
  `--join TOKEN --url URL` sets up Docker, Nomad as a client and the private network, and no Caravana
  service and no Traefik. A rerun reuses the recorded role.
- **FR-002** The console makes a join token: random, shown once, stored as a SHA-256 hash, valid one
  hour, used once. With it go a name (optional: the machine's host name then) and a pool.
- **FR-003** Servers form a full WireGuard mesh. Nomad's RPC, raft and serf, the app ports and the
  container subnets travel on it and nothing else is opened between servers but UDP 51820.
- **FR-004** Nomad's RPC and raft use TLS with certificates from the cluster's CA, with
  `verify_server_hostname`; serf uses the gossip key. Both from the first install, and on an existing
  host from the next installer run.
- **FR-005** Caravana signs a certificate only for a CSR, only for a server it knows, and only of the
  kind (client or server) that server should be. Private keys never leave their server. The CA's key and
  the gossip key live in Nomad Variables under `caravana/cluster/`.
- **FR-006** A root agent on every server asks Caravana for its state at most every 30 s and applies it:
  peers, role, certificate. It validates every value (keys, addresses inside the fixed ranges, the role,
  a certificate that chains to the CA it has and matches its own key) and takes no command, path or file
  name from the answer.
- **FR-007** One Nomad server until three servers are ready; then the first server and the two oldest
  others, promoted one at a time. A removed Nomad server is replaced by the next oldest ready server.
- **FR-008** A service with more than one copy spreads its copies across servers.
- **FR-009** All volumes of one service are created on the same server.
- **FR-010** Traefik runs on the first server only, and routes to copies on any server.
- **FR-011** The console has Servers (`/ops/servers`: the list, Add server), Add a server
  (`/ops/servers/new`: the command and the checks, live) and a server page for every server. The
  Operator Overview, the Running tab, Monitoring's host section, Volumes and Updates say which server
  they describe once there is more than one.
- **FR-012** Needs-you gains `server_down` and `server_certificate_expiring`, with notifications, and
  `disk_nearly_full` names its server.
- **FR-013** A server can be removed when Nomad says it is down or it never joined; otherwise the
  removal is refused, naming why.
- **FR-014** Everything in the console is in the API (`api/openapi.yaml`), and a deploy token can use
  none of it. The join and node calls have their own credentials and are documented there too.
- **FR-015** No page, answer, log line, error, record or notification holds a join token, a node token,
  a private key, the CA's key or the gossip key, beyond the one answer that shows a join token. Tests
  search for sentinels.

### Non-functional

- **NFR-001** No new runtime dependency and no new Go module (`golang.org/x/crypto` is already direct).
  `wireguard-tools` is a package the installer adds.
- **NFR-002** A converted single server needs at most two Nomad restarts. Apps keep answering, but for a
  copy made before the installer's veth link file and replaced at the restart (research L17): at most
  ten seconds for an app with one copy.
- **NFR-003** A join, from the command to `ready`, takes under five minutes on a fresh VM with a normal
  connection, most of it package installs.

## Success criteria

- **SC-001** On three throwaway VMs, the second and third join from commands made by the API, a 3-copy
  app has a copy on each, and the app answers after any one of the servers is stopped
  (`deploy/dev/cluster-test.sh`, the Phase 3 gate's first form).
- **SC-002** A host installed by the release before this feature is converted with its app answering.

## Out of scope (each has its Phase 3 feature)

- Moving work off a live server, "Take work again" (Servers and the server page).
- A proxy on every server, shared and wildcard certificates, Traefik's own health check per route (Proxy
  and routing, Certificates). So the first server is still the only way in.
- Updating joined servers from the console; reading their OS updates (Updates). A joined server is
  updated by running the newer installer on it.
- SSH access as a way to add a server, placement rules, pools that add servers by themselves.
- Caravana's own high availability: its data is one SQLite file on the first server.
- Network isolation between apps (backlog): a container can reach any other container, now on any server.

## Decisions taken as defaults (the maintainer hasn't ruled on them)

1. One ingress, the first server (research D6).
2. The agent polls the console's public address (D5).
3. No two-server raft (D7).
4. Remove only a down server (D10).
5. Pools are accepted and written, and apps stay in `default` (D11).
6. Certificates last 90 days and renew at 30; the CA lasts 10 years and has no rotation yet.
7. The cluster test is its own script, `deploy/dev/cluster-test.sh`, not more VMs in `install-test.sh`.
