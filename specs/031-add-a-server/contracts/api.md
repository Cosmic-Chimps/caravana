# Contract: API, store and service for servers

API 0.34.0. Migration `0024_servers.sql`. The host side is [node.md](node.md).

## 1. Store: `servers`

```sql
CREATE TABLE servers (
  idx            INTEGER PRIMARY KEY CHECK (idx BETWEEN 1 AND 63),  -- the index of node.md §1
  id             TEXT NOT NULL UNIQUE,          -- 26 lowercase base32, as other IDs; a join's handle
  name           TEXT NOT NULL DEFAULT '',      -- '' while a join that leaves the name to the machine waits
  pool           TEXT NOT NULL DEFAULT 'default',
  state          TEXT NOT NULL CHECK (state IN ('waiting', 'joining', 'ready')),
  first          INTEGER NOT NULL DEFAULT 0 CHECK (first IN (0, 1)),
  nomad_server   INTEGER NOT NULL DEFAULT 0 CHECK (nomad_server IN (0, 1)),  -- the role Caravana wants
  public_key     TEXT NOT NULL DEFAULT '',
  endpoint       TEXT NOT NULL DEFAULT '',      -- "ip:51820"
  join_hash      TEXT NOT NULL DEFAULT '',      -- SHA-256 hex of the join token; '' once used
  join_expires   TEXT NOT NULL DEFAULT '',
  token_hash     TEXT NOT NULL DEFAULT '',      -- SHA-256 hex of the node token
  created_at     TEXT NOT NULL,
  joined_at      TEXT NOT NULL DEFAULT '',
  ready_at       TEXT NOT NULL DEFAULT '',
  seen_at        TEXT NOT NULL DEFAULT '',      -- the agent's last sync
  report         TEXT NOT NULL DEFAULT '',      -- its last report, JSON
  cert_not_after TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE UNIQUE INDEX servers_name ON servers (name) WHERE name != '';
CREATE UNIQUE INDEX servers_join_hash ON servers (join_hash) WHERE join_hash != '';
CREATE UNIQUE INDEX servers_token_hash ON servers (token_hash) WHERE token_hash != '';
```

`store/servers.go`: `ListServers`, `ServerByName`, `ServerByID`, `ServerByJoinHash`,
`ServerByTokenHash`, `InsertServer` (takes the lowest free index in the same transaction;
`ErrClusterFull`), `ConsumeJoin` (one `UPDATE … WHERE join_hash = ? AND state = 'waiting' AND
join_expires > ?` that sets the name, key, endpoint, token hash, `joined_at`, state `joining` and clears
`join_hash`; 0 rows is `ErrNotFound`, so two calls with one token can't both win), `SaveServerReport`,
`SetServerReady`, `SetNomadServer`, `DeleteServer`, `DeleteExpiredJoins(now)`.

## 2. Service (`internal/service/servers.go`, `serverroles.go`)

Seams, set from `cmd/caravana/main.go`, faked in tests: the deployer gains `Nodes(ctx) ([]deploy.Node,
error)` (every Nomad node: ID, name, status, eligible, draining, pool, app address, totals),
`ClusterCA(ctx) (certPEM, keyPEM []byte, err)` and `GossipKey(ctx) (string, error)` (the variables
`caravana/cluster/ca` items `cert`, `key`, and `caravana/cluster/gossip` item `key`; missing is
`deploy.ErrNoCluster`).

- `ClusterStatus(ctx)`: can servers be added? Yes when a first server row exists and the CA is readable.
  Otherwise `ErrClusterNotReady` (409 `cluster_not_ready`): "Run the installer again on this server
  first: it sets up the private network and Nomad's certificates."
- `CreateJoin(ctx, name, pool, fallbackURL) (ServerJoin, error)` (`fallbackURL` is the request's own
  scheme and host, which only the API can see; used when `CARAVANA_PUBLIC_URL` isn't set): `name` empty or a valid name (lowercase label, at
  most 63) not used by a server or a Nomad node (`ErrServerExists`, 409 `server_exists`, field `name`);
  `pool` empty (then `default`) or a valid name. Deletes expired joins first. Makes the token (`cvjn_` and
  32 random bytes base64url), stores its hash and `now + 1 h`, returns the server, the token, the command
  and `expiresAt`.
- The command: `curl -fsSL <releases>/v<version>/install.sh | sudo bash -s -- --join <token> --url
  <public URL>`, plus ` --insecure` when the console's certificate mode is `none`. `<releases>` is
  `https://github.com/Cosmic-Chimps/caravana/releases/download`. A dev build (version `dev` or one that
  isn't a release) has no download: the command is `sudo bash install.sh --from <the release folder>
  --join <token> --url <public URL>` and `commandNote` says the installer has to be copied to the server.
  The public URL is `CARAVANA_PUBLIC_URL`, else the request's own scheme and host.
- `Join(ctx, JoinRequest, remoteAddr)`: hashes the token and calls `ConsumeJoin`; a miss is
  `ErrJoinToken` (401 `join_token_invalid`, one message: "This join token isn't valid: it was used,
  expired, or never existed. Make a new command in the console."). Validates the public key
  (`mesh.ValidPublicKey`, not another server's), the CSR (PEM, one request, its signature checks, ECDSA
  P-256 or Ed25519), the facts' sizes. The name is the join's, else the valid name of `facts.hostname`
  (node.md §3), with `-2`, `-3`… added while taken. The endpoint is `request.address` when given (an IP
  literal), else `remoteAddr`'s address, with port 51820. Signs a **client** certificate, makes the node
  token (`cvnd_` and 32 random bytes base64url), and answers the join answer (§4). The server is
  `joining`. A failure after `ConsumeJoin` puts nothing back: the owner makes a new command and the
  half-joined row is removed with Remove.
- `NodeSync(ctx, nodeToken, report)`: unknown token is `ErrNodeToken` (401, uniform). Saves the report,
  `seen_at`, `cert_not_after`. Evaluates the checks (§3); when all four pass, `SetServerReady` and the
  change notice `servers`. Then `reconcileRoles` (below). Answers the sync answer.
- `NodeCertificate(ctx, nodeToken, csrPEM)`: signs a certificate of the kind the server should be now
  (`nomad_server` → server), for its mesh address.
- `reconcileRoles` (pure core `wantedServers(servers []ServerFacts) []int`, table-tested): the Nomad
  servers are those already wanted, minus removed ones; the first server always. With fewer than three
  ready servers nothing is added. With three or more, while fewer than three are wanted **and** every
  wanted one reports `serverRunning`, the oldest ready server that isn't one (by `joined_at`) is added:
  one per call, so they are promoted one at a time.
- `ListServers(ctx) (ServersView, error)` and `Server(ctx, name)`: the rows joined with Nomad's nodes by
  app address (`mesh.IndexOfApp`), plus Nomad nodes that have no row (`managed: false`; the lab, or a
  host not rerun), plus per-server numbers from the newest stored samples (§6). When Nomad can't be read
  the rows are still listed, with `nomad: "unreachable"` and no judgement of `down`.
- `RemoveServer(ctx, nameOrID)`: a `waiting` row always; a `joining` or `ready` row only when Nomad has
  no node for it or says it is `down` (`ErrServerRunning`, 409 `server_running`: "<name> is running. Stop
  it, or run the installer with --leave on it, then remove it."); the first server never
  (`ErrProtected`). Deletes the row, so its node token stops working and it leaves everyone's peers at
  their next sync; a removed Nomad server's place is taken by `reconcileRoles`.
- `AdoptFirst(ctx, name, publicKey, address) (nodeToken string, err error)` (the CLI's `caravana server
  adopt-first`): inserts index 1 (`first`, `nomad_server`, state `ready`) when no first server exists, or
  rotates the first server's node token and updates its key and endpoint when one does; prints the token.

State shown: `waiting`, `joining`, and for a `ready` row what Nomad says: `ready`, `down` (the node is
down, disconnected or gone), or `ready` with `acceptsWork: false` (ineligible or draining).

## 3. The four checks of a joining server

| `name` | Passes when | `detail` |
| --- | --- | --- |
| `connected` | the join call was answered | "Connected to the cluster" |
| `docker` | the last report says Docker answers | "Docker running, 160 GB disk free" (the disk under Nomad's data, from the report) |
| `network` | the last report's `reach` has every other `ready` or `joining` server, all `ok` | "Reaches hel-1 and fsn-1 on the private network"; open: "Can't reach fsn-1 yet" with the names |
| `ready` | Nomad has a node at this server's app address that is `ready` and eligible | "Ready to take work" |

They are reported in this order, and one that follows an open one is open too. A lone joining server
with no other server but the first still needs `network` to reach the first.

## 4. Node calls (their own credentials; not behind `authenticate`)

Served on the main listener only. Each answer is `Cache-Control: no-store`. Request bodies at most
64 KiB, JSON. Errors have the usual shape.

**`POST /v1/join`** (`joinServer`). No header credentials; the token is in the body. Failed tries are
limited per address like sign-ins (5 per 15 minutes; 429 `too_many_attempts`).

```json
{"token": "cvjn_…", "publicKey": "…", "csr": "-----BEGIN CERTIFICATE REQUEST-----…",
 "address": "203.0.113.9",
 "facts": {"hostname": "fsn-2", "arch": "arm64", "memoryMb": 3916, "diskFreeMb": 160000,
           "diskTotalMb": 190000, "kernel": "6.8.0-134-generic", "docker": "27.5.1",
           "nomad": "2.0.7", "agent": "0.18.0"}}
```

200:

```json
{"name": "fsn-2", "index": 3, "pool": "default", "role": "client", "servers": [1],
 "ca": "-----BEGIN CERTIFICATE-----…", "certificate": "-----BEGIN CERTIFICATE-----…",
 "nodeToken": "cvnd_…",
 "peers": [{"index": 1, "name": "hel-1", "publicKey": "…", "endpoint": "203.0.113.4:51820"}]}
```

401 `join_token_invalid`, 409 `cluster_not_ready`, 422 `invalid_field` (naming `publicKey`, `csr`,
`address`, `facts.…`), 429.

**`POST /v1/node/sync`** (`syncNode`), `Authorization: Bearer cvnd_…`:

```json
{"agent": "0.18.0", "nomad": "2.0.7", "docker": "27.5.1", "dockerRunning": true,
 "diskFreeMb": 160000, "diskTotalMb": 190000, "memoryMb": 3916, "kernel": "6.8.0-134-generic",
 "role": "client", "nomadLeader": true, "serverRunning": false,
 "certificateNotAfter": "2027-02-08T10:00:00Z",
 "reach": [{"index": 1, "ok": true}, {"index": 2, "ok": false}]}
```

200:

```json
{"name": "fsn-2", "index": 3, "pool": "default", "role": "server", "servers": [1, 2, 3],
 "peers": [{"index": 1, "name": "hel-1", "publicKey": "…", "endpoint": "203.0.113.4:51820"}],
 "gossipKey": "…", "renew": false, "state": "joining",
 "checks": [{"name": "connected", "ok": true, "detail": "Connected to the cluster"}]}
```

`gossipKey` only when `role` is `server`. `peers` are every other server that is `joining` or `ready`.
`renew` is true when the certificate the server reported ends in under 30 days. 401 `node_token_invalid`
for any token that isn't a server's.

**`POST /v1/node/certificate`** (`renewNodeCertificate`), same credentials: `{"csr": "…"}` → 200
`{"certificate": "…", "ca": "…"}`; 422 `invalid_field` `csr`.

A node token is accepted nowhere else, and an API token, a session or a deploy token is refused on the
three node calls (401).

## 5. Console calls (session or full API token; a deploy token gets 403 `token_scope` by default deny)

| Operation | Call | Answer |
| --- | --- | --- |
| `listServers` | `GET /v1/servers` | 200 `{"cluster": Cluster, "servers": [Server]}` |
| `getServer` | `GET /v1/servers/{name}` | 200 `Server`; 404 `server_not_found` |
| `addServer` | `POST /v1/servers` `{"name": "", "pool": ""}` | 201 `{"server": Server, "token": "cvjn_…", "command": "…", "commandNote": "", "expiresAt": "…"}`, `no-store`; 409 `cluster_not_ready`, `server_exists`, `cluster_full`; 422 `invalid_field` |
| `getServerJoin` | `GET /v1/servers/joins/{id}` | 200 `Server` (by the join's ID, which exists before the name does); 404 |
| `removeServer` | `DELETE /v1/servers/{name}` (a name or a join's ID: OpenAPI allows one parameter name per path) | 204; 404; 409 `server_running`, `protected` |

`getHost` gains `?server=<name>` (default: the server Caravana runs on; 404 `server_not_found`), and
`listVolumes`' `AppVolume` gains `server`. `Copy.node` exists already.

```json
// Server
{"id": "…", "name": "fsn-2", "index": 3, "pool": "default", "state": "ready", "managed": true,
 "first": false, "ingress": false, "controlPlane": false, "nomadServer": true, "acceptsWork": true,
 "address": "203.0.113.9", "privateAddress": "10.98.0.3",
 "createdAt": "…", "joinedAt": "…", "readyAt": "…", "seenAt": "…", "expiresAt": "",
 "checks": [{"name": "connected", "ok": true, "detail": "…"}],
 "agent": {"caravana": "0.18.0", "nomad": "2.0.7", "docker": "27.5.1", "kernel": "…"},
 "certificateExpiresAt": "…",
 "memory": {"used": 480, "reserved": 144, "total": 1958}, "cpu": {"used": 60, "reserved": 220, "total": 4000, "cores": 2},
 "disks": [ … as getHost … ], "copies": 4, "unreachable": []}

// Cluster
{"servers": 3, "ready": 3, "nomadServers": 3, "survivesLoss": true,
 "memoryMb": 5874, "cpuCores": 6, "canAdd": true, "cannotAddReason": "",
 "nomad": "ok"}
```

`memory`, `cpu` and `disks` are `null` for a server with no sample yet (never 0). `survivesLoss` is true
when three Nomad servers are wanted and each reports `serverRunning`. `expiresAt` is set while `waiting`.

## 6. What assumed one server (Go)

- `deploy.HostStats(ctx, nodeID, dockerRoot)` takes the node. `deploy.Nodes` lists them. The sampler
  reads every `ready` node each tick (the Docker root is the Caravana host's setting, used for every
  server; a server whose Docker root differs shows the disk that holds Nomad's data only) and
  `WriteMetricsTick` writes a host row and its disk rows per node (the tables are keyed by node already).
  `service.Host(ctx, server)` reads one node's rows; the OS block (027) is filled for the server
  Caravana runs on only, and `HostView.osRead` says whether it was.
- Which node is "the server Caravana runs on": the first server's row when there is one, else the only
  Nomad node, else the first `ready` one (today's behaviour).
- `disk_nearly_full`: one item per server and mount. The key stays `disk_nearly_full|||<mount>` for the
  server Caravana runs on, and is `disk_nearly_full||<server>|<mount>` for the others; `ProblemParts`
  names the server when there is more than one.
- `server_down`: a `ready` row whose Nomad node is down, disconnected or gone; key
  `server_down|||<name>`; not judged while Nomad can't be read (as the copy kinds). Sentence: "<name> is
  down. Its copies are being replaced on other servers; what has a volume there waits for it."
  `server_certificate_expiring`: a `joining` or `ready` row whose `cert_not_after` is under 14 days away;
  key `server_certificate_expiring|||<name>`. Sentence: "<name>'s Nomad certificate ends <date>. Its
  agent couldn't renew it; run the installer on it again." Both in `AttentionKinds`, `ProblemParts`, the
  notifier's event (`notify.Event.Server`) and every renderer.
- `nomadjob.Translate`: a group with more than one copy gets `Spreads: [{Attribute:
  "${node.unique.id}", Weight: 100}]`; `nomadjob.HCL` writes it; goldens change for multi-copy apps only.
- `deploy.EnsureVolumes`: per service, the node of a volume that exists decides; otherwise the first
  volume is created in pool `default` with no node, and the rest of that service's volumes with its
  `NodeID`. `AppVolume.Server` is the node's name.
- `HostCopies(ctx, server)` lists the copies on that server.
