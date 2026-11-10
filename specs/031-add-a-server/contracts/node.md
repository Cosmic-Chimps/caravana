# Contract: the server side of a server (addresses, files, the agent, the installer)

What a Caravana server has on its disk, what the root agent does, and what the installer's two roles do.
The calls the agent makes are in [api.md](api.md) › "Node calls". Facts marked L are in
[research.md](../research.md).

## 1. Addresses (package `internal/mesh`, pure)

Every server has an **index** N, 1 to 63. The first server is 1. Everything else derives from N; no
address ever comes from the console.

| What | Value | Where |
| --- | --- | --- |
| Mesh address | `10.98.0.N` | `caravana-mesh` (WireGuard, UDP 51820), `/32` |
| App ports | `10.99.0.N` | `caravana0` (dummy), `/32`; Nomad's `network_interface` |
| Containers | `172.26.64.0/20` for N = 1 (Nomad's default, unchanged), `10.97.(4N).0/22` for N ≥ 2 | Nomad's `bridge_network_subnet` |
| Routes through `caravana-mesh` | `10.98.0.0/24`, `10.99.0.0/24`, `10.97.0.0/16`, and `172.26.64.0/20` when N ≥ 2 | set by `up` |
| A peer's allowed addresses | its mesh `/32`, its app `/32`, its container subnet | derived from the peer's index |

`internal/mesh` exports: `MaxIndex = 63`, `Port = 51820`, `Of(index) (Addresses{Mesh, App netip.Addr;
Containers netip.Prefix}, error)`, `IndexOfApp(addr) (int, bool)` (10.99.0.N → N), `Routes(index)
[]netip.Prefix`, `NewKey() (private, public string, err)` (X25519 with `golang.org/x/crypto/curve25519`,
clamped, base64 as `wg` writes it), `PublicKey(private) (string, error)`, `ValidPublicKey(s) bool` (44
characters of base64 that decode to 32 bytes), `ParseEndpoint(s) (netip.AddrPort, error)` (a literal IP
and port, never a name; not loopback, unspecified, multicast or link-local), `Peer{Index int; PublicKey,
Endpoint string}`, and `Config(privateKey string, peers []Peer) string`: the text `wg setconf` reads
(`[Interface]` with `PrivateKey` and `ListenPort`, then per peer `PublicKey`, `Endpoint`, `AllowedIPs`,
`PersistentKeepalive = 25`), peers sorted by index. A test forbids `net/http`, `os/exec` and `os` here.

## 2. Files on a server

| Path | Mode | What |
| --- | --- | --- |
| `/etc/caravana-node/` | 0700 root | the agent's folder |
| `…/state.json` | 0600 | what the agent last applied (below); written to a temp file, then renamed |
| `…/mesh.key` | 0600 | WireGuard's private key; never leaves the server |
| `…/node.env` | 0600 | `CARAVANA_URL=`, `CARAVANA_NODE_TOKEN=`, `CARAVANA_INSECURE=0\|1`; read as text, never sourced |
| `…/gossip.key` | 0600 | Nomad servers only |
| `…/ca-key.pem` | 0600 | the first server only, between `init-first` and the installer storing it in Nomad; then shredded |
| `/etc/nomad.d/tls/ca.pem` | 0644 | the cluster CA's certificate |
| `/etc/nomad.d/tls/agent.pem`, `agent-key.pem` | 0644, 0600 | this server's Nomad certificate and key |
| `/etc/nomad.d/nomad.hcl` | 0600 | rendered by the agent (it holds the gossip key on a Nomad server) |
| `/usr/local/libexec/caravana-node` | 0755 | the agent: a copy of the release's `caravana` binary, so a rolled-back Caravana (030) never takes the agent's commands away |

`state.json`:

```json
{"version": 1, "index": 3, "name": "fsn-2", "pool": "default", "first": false,
 "role": "client", "servers": [1], "cpuTotalCompute": 0,
 "peers": [{"index": 1, "publicKey": "…", "endpoint": "203.0.113.4:51820"}]}
```

`role` is `client` or `server`. `servers` are the indexes of the Nomad servers. `first` is true on server
1 only: it is the ingress and keeps `bootstrap_expect = 1`.

## 3. The agent: `caravana node <command>` (package `internal/nodeagent`)

It runs as root (it refuses otherwise), from the timer or the installer. All host access goes through a
small struct the tests replace: a root folder for the paths above, a `Runner` (`Run(ctx, name, args...)
(stdout []byte, err error)` for `ip`, `wg`, `systemctl`, `ping`, `df`, `docker`, `nomad`, `uname`), an
`*http.Client` and a clock. No shell is ever invoked and no argument is built from the console's answer
except validated keys and endpoints, passed to `wg` through the config text on a file descriptor path
(`wg syncconf caravana-mesh <file>`, a root-only temp file removed afterwards), never on a command line.

| Command | Does |
| --- | --- |
| `init-first --address ADDR [--cpu-total-compute N]` | Server 1, offline. Makes what is missing: `mesh.key`; `state.json` (index 1, role server, first, name = the host name made a valid name, pool `default`); the CA (`ca.pem`, `ca-key.pem`) and this server's server certificate signed with it; `gossip.key`. Renders `nomad.hcl`. Runs `up`. A second run changes nothing. If `ca.pem` exists and `agent.pem` doesn't, it fails saying so. |
| `join --url URL --token TOKEN [--address ADDR] [--insecure] [--cpu-total-compute N]` | If `node.env` exists it has joined: render, `up`, exit 0, no call. Otherwise: make `mesh.key`, a Nomad key and CSR; gather the facts; `POST URL/v1/join`; validate the answer (§5); write `node.env`, `tls/`, `state.json`, `nomad.hcl`; `up`. On a refusal it prints the console's message and exits 1 having written nothing but `mesh.key`. |
| `up` | From `state.json`, no network needed: make `caravana0` (dummy) and `caravana-mesh` (wireguard) if missing, set their addresses, `wg setconf`, bring them up, replace the routes. Idempotent. Without `state.json`: exit 0, nothing. |
| `sync [--renew] [--checks]` | `--renew` renews the certificate now, whatever its age. `--checks` prints what the console reports, for the installer: one line `check <name> ok\|open <detail>` per check, then `state <joining\|ready>`. Without `node.env`: exit 0 ("not registered yet"). Gather the report, `POST /v1/node/sync`, validate, apply (§6). A console that can't be reached or answers 5xx: a line on stderr, exit 0, nothing changes. A 401: stderr says the server was removed from the cluster, exit 0, nothing changes. |
| `show FIELD` | Prints one of `public-key`, `name`, `index`, `role`, `first` and a newline. For the installer. |
| `leave` | Sends Nomad SIGTERM, on which it leaves the cluster (`leave_on_terminate = true` in `nomad.hcl`; a Nomad server goes out of raft, research L16), waits up to 30 s for it to exit, then stops and disables Nomad, deletes `caravana-mesh` and `caravana0`, removes `/etc/caravana-node` and `/etc/nomad.d/tls`. Refuses on the first server. |

The valid name of a host name: lowercase, the first label only, every character outside `[a-z0-9-]`
turned into `-`, dashes trimmed from the ends, at most 63; empty becomes `server-<index>`.

## 4. `nomad.hcl` (rendered by `nodeagent.RenderNomad(State, gossipKey) string`, golden tests)

One template, three forms. Lines marked F are the first server's only, S a Nomad server's, J a joined
server's.

```hcl
# Written by `caravana node`: the next sync rewrites it. Change it through Caravana.
name       = "fsn-2"                    # J (the first server keeps its host name)
datacenter = "dc1"
leave_on_terminate = true               # SIGTERM leaves the cluster; a stop sends SIGINT (L16)
data_dir   = "/opt/nomad/data"
bind_addr  = "127.0.0.1"

addresses {
  http = "127.0.0.1"
  rpc  = "10.98.0.3"                    # S
  serf = "10.98.0.3"                    # S
}

advertise {
  http = "127.0.0.1"
  rpc  = "10.98.0.3"
  serf = "10.98.0.3"
}

tls {
  http                   = false
  rpc                    = true
  ca_file                = "/etc/nomad.d/tls/ca.pem"
  cert_file              = "/etc/nomad.d/tls/agent.pem"
  key_file               = "/etc/nomad.d/tls/agent-key.pem"
  verify_server_hostname = true
}

server {                                # S
  enabled          = true
  bootstrap_expect = 1                  # F
  encrypt          = "<gossip key>"
  failover_heartbeat_ttl = "1m"         # S (research L14; Nomad's default is 5 minutes)
  heartbeat_grace = "30s"               # S (research L18; Nomad's default is 10 seconds)
  server_join {                         # S and not F, and F once there are other servers
    retry_join = ["10.98.0.1:4648"]     # every other Nomad server, by index
  }
}

client {
  enabled               = true
  cni_path              = "/opt/cni/bin"
  node_pool             = "default"
  network_interface     = "caravana0"
  bridge_network_subnet = "10.97.12.0/22"   # J
  servers               = ["10.98.0.1:4647"] # J and not S: every Nomad server (research L15)
  cpu_total_compute     = 4000               # only when given
  meta {                                     # F
    caravana_ingress = "true"
  }
  host_volume "traefik-acme" {               # F
    path      = "/var/lib/caravana-host/traefik"
    read_only = false
  }
}

acl { enabled = true }
plugin "docker" { config { allow_privileged = false } }
telemetry { publish_allocation_metrics = true }
```

The comments of today's `deploy/install/nomad.hcl.tpl` (why loopback, why `caravana0`, why the telemetry
line) move into the template. `nomad.hcl.tpl` is deleted and leaves the release's host files.

## 5. Validating what the console answers

Applied to the join answer and every sync answer, before anything is written. One failure refuses the
whole answer: stderr names the field, nothing changes.

- The body is JSON of at most 256 KiB; unknown fields are ignored.
- `index`: 1 to 63; in a sync, equal to the one in `state.json`. `name`: a valid name; in a sync, equal.
- `pool`: a valid name. `role`: `client` or `server`; a server never goes back to `client` (refused).
- `peers`: indexes 1 to 63, none equal to its own, none twice; `publicKey` valid (§1) and not its own;
  `endpoint` parses (§1).
- `servers`: not empty, each its own index or a peer's.
- `gossipKey`: base64 of 16, 24 or 32 bytes; required when `role` is `server`.
- `ca`: one PEM certificate that is a CA; in a sync or a certificate answer it must equal `ca.pem`.
- `certificate`: one PEM certificate, signed by `ca.pem`, whose public key is the key the agent just
  made, valid now, with the DNS name `server.global.nomad` for a server and `client.global.nomad` for a
  client.
- `nodeToken`: `cvnd_` and 43 characters of `[A-Za-z0-9_-]`.

## 6. What `sync` applies, in this order

1. **Peers and servers.** If they differ from `state.json`: write it, `wg syncconf`. A changed `servers`
   list also re-renders `nomad.hcl` without restarting Nomad (a client learns its servers from the ones
   it knows; the file is for its next start).
2. **Role.** `client` → `server`: make a new key and CSR, `POST /v1/node/certificate`, validate, write
   the certificate, the key and `gossip.key`, write `state.json`, render `nomad.hcl`, `systemctl restart
   nomad`. Its copies keep running (L9).
3. **Renewal.** When the certificate ends in under 30 days, the answer says `renew`, or `--renew` was given: new key and CSR,
   `POST /v1/node/certificate`, validate, write both files (temp, then rename, key first), `systemctl
   reload nomad` (L11).

The report it sends (api.md) is gathered first: the agent's and Nomad's versions, Docker (`docker info`
answers), the disk under `/opt/nomad/data` (`df -Pm`), the kernel, memory, the certificate's end, the
role in `state.json`, whether the local Nomad knows a leader (`GET http://127.0.0.1:4646/v1/status/leader`,
which needs no token) and, for a server, whether `GET /v1/status/peers` lists its own `10.98.0.N:4647`;
and per peer one `ping -c 1 -W 2 10.98.0.X`, eight at a time.

## 7. Units (shipped in the release, installed by the installer)

- `caravana-mesh.service`: oneshot, `RemainAfterExit=yes`, `ExecStart=/usr/local/libexec/caravana-node
  node up`, `Before=nomad.service`, `After=network-pre.target`, `WantedBy=multi-user.target`.
- `nomad.service` (was a heredoc in `install.sh`; now a file, `deploy/install/nomad.service`): today's,
  plus `Requires=caravana-mesh.service`, `After=… caravana-mesh.service`, `RestartSec=2`,
  `StartLimitIntervalSec=0` (L3).
- `caravana-node.service`: oneshot, `ExecStart=/usr/local/libexec/caravana-node node sync`,
  `After=nomad.service`. `caravana-node.timer`: `OnBootSec=20s`, `OnUnitActiveSec=30s`,
  `AccuracySec=1s`.

## 8. The installer (`deploy/install/install.sh`)

New options: `--first` (the default), `--join TOKEN`, `--url URL`, `--address ADDR` (the address other
servers reach this one at; default: the public IP for `--first`, the address the join call comes from
for `--join`), `--insecure` (don't check the console's certificate: only what the console's command
says, for a console on Traefik's own certificate), `--leave`. `install.env` records `CARAVANA_ROLE`
(`first` or `join`), `CARAVANA_URL`, `CARAVANA_INSECURE`, `CARAVANA_ADDRESS`; never a token. A rerun
without options reuses them. `--join` on a first server and `--first` on a joined one are refused (exit
2), as is `--uninstall` on a joined server (it says `--leave`).

Checked values, before anything changes (exit 2): `--join` is `cvjn_` and 43 characters of
`[A-Za-z0-9_-]`; `--url` is `https://` and a host name (with an optional port), or `http://` for
`127.0.0.1` and `localhost` only; `--address` is an IPv4 or IPv6 literal. Both roles refuse a host that
already has an address or a route inside `10.97.0.0/16`, `10.98.0.0/24` or `10.99.0.0/24` on an
interface that isn't `caravana0`, `caravana-mesh` or `nomad`.

**`--first`**, in order (new steps in bold):

1. Preflight, release files, packages (**plus `wireguard-tools`**), Nomad's binary, CNI, sysctl, **the
   veth link file** (`/etc/systemd/network/10-caravana-veth.link`, `MACAddressPolicy=none`, research
   L17; both roles), `caravana0` through systemd-networkd as today.
2. **Install the agent** (`/usr/local/libexec/caravana-node`, from the release's `caravana`) **and the
   units** of §7. Remember whether `/opt/nomad/data/server/raft` existed.
3. **`caravana-node node init-first --address ADDR`** (with `--cpu-total-compute` when `lscpu` has no
   MHz, as today).
4. Restart Nomad when its binary, unit, `nomad.hcl` or `tls/` changed (the checksum now covers them).
5. **Once per host, when raft existed before this run and `$STATE/raft-mesh` doesn't:** stop Nomad, write
   `peers.json` (L2) from `data_dir/server/node-id` and `10.98.0.1:4647`, start Nomad, and say so
   ("Nomad restarts twice, once"). Then write the marker. A fresh host writes the marker directly.
6. Leader, ACL bootstrap, the CLI wrapper, the firewall (**plus `51820/udp` and `allow in on
   caravana-mesh`**), the scheduler setting, the policies.
7. **Hand the cluster's secrets to Nomad:** when `ca-key.pem` is on disk, `nomad var put -force
   caravana/cluster/ca cert=@ca.pem key=@ca-key.pem`, read it back, compare, then `shred -u ca-key.pem`;
   `caravana/cluster/gossip key=@gossip.key` when the variable is missing. A failure here stops the
   install (exit 1) with the key still on disk.
8. Caravana's user, settings and service, the updater, start, the admin, as today.
9. **Register the first server:** without `node.env`, `sudo caravana server adopt-first --name NAME
   --public-key KEY --address ADDR` (as the `caravana` user, on the store, like `admin init`) prints a
   node token once; the installer writes `node.env` (`CARAVANA_URL=http://127.0.0.1:7070`).
10. **Enable `caravana-node.timer`.** Traefik (its job now has the ingress constraint), the route check.

**`--join`**: preflight (root, Ubuntu 24.04, the CPU, 2 GB, a versioned installer, no Nomad or Docker
that isn't Caravana's, the values above; without `node.env`, `--join` and `--url` are required), record
the options, the release files, packages, Nomad's binary, CNI and sysctl, the agent and units,
`caravana-node node join …`, start Nomad, the firewall (22, `51820/udp`, `in on caravana-mesh`; not 80 or
443), the timer. Then it runs `caravana-node node sync` every 5 s for up to 3 minutes and prints each
check as the console reports it passed (the sync answer's `checks`), ending "This server joined as
NAME." or, on a timeout, the checks still open and exit 1. No Caravana user, service, env file, updater,
CLI wrapper, Traefik or admin.

**`--leave`** (a joined server): disable the timer and the mesh unit, `caravana-node node leave`, remove
the units and the agent, keep Docker and the packages, and say to remove the server in the console.

**`--uninstall`** (the first server) also disables `caravana-node.timer` and removes `node.env`: without
Caravana there is nothing to sync with. The mesh, Nomad and the apps stay.

`deploy/install/traefik.nomad.hcl` gains, in the job:

```hcl
constraint {
  attribute = "${meta.caravana_ingress}"
  value     = "true"
}
```

`tools/release.sh`: the host files lose `nomad.hcl.tpl` and gain `nomad.service`,
`caravana-mesh.service`, `caravana-node.service`, `caravana-node.timer`.

## 9. What a compromised console can and can't do through the agent

It can hand a server a peer of its choosing, promote a client to a Nomad server, or withhold a renewal.
With its Nomad token it can already run containers on every server and read every secret, so none of that
is new reach. It cannot make the agent run a program, write outside the files of §2, choose an address
(they derive from indexes), or read a private key (none is ever sent). The CA's key is readable by
Caravana's Nomad token: someone with that token **and** a place on the private network could mint a
server certificate. That is accepted and written here; rotating the CA is not built.
