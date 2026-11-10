# Tasks: Add a server

Read [spec.md](spec.md), [plan.md](plan.md), [research.md](research.md) and the contracts first:
[node.md](contracts/node.md), [api.md](contracts/api.md), [page-servers.md](contracts/page-servers.md).
The phases run in order (A, B, C, D, E). Each Go phase ends with `go build ./... && go vet ./... &&
go tool staticcheck ./... && gofmt -l . && go test ./<packages touched>` green, and
`CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...`; the host phase with shellcheck.

## Phase A: the pure packages and the agent

- [x] T001 `internal/mesh`: `addresses.go` (`MaxIndex`, `Port`, `Of`, `IndexOfApp`, `Routes`),
  `keys.go` (`NewKey`, `PublicKey`, `ValidPublicKey`), `config.go` (`Peer`, `ParseEndpoint`, `Config`),
  exactly node.md §1. Table tests: every index's three values (1, 2, 63; 0 and 64 refused), a key made
  here gives the public key `wg pubkey` would (a fixed vector from RFC 7748 §6.1: Alice's private key
  `77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a` gives
  `8520f0098930a754748b7ddcb43ef75a0dbf3a0d26381af4eba4a98eaa9b4e6a`), endpoints refused (a name,
  loopback, `0.0.0.0`, multicast, no port), and a golden `Config`. An import test forbids `net/http`,
  `os`, `os/exec`.
- [x] T002 `internal/clusterca`: `New(now) (certPEM, keyPEM []byte, err)` (ECDSA P-256, CN "Caravana
  cluster CA", 10 years, CA, key usage cert sign and CRL sign), `NewKeyAndCSR() (keyPEM, csrPEM)`,
  `Sign(caCert, caKey, csrPEM []byte, kind Kind, meshIP netip.Addr, now) (certPEM, error)` (`Server` or
  `Client`; 90 days, not-before 5 minutes back; CN and first DNS name `server.global.nomad` or
  `client.global.nomad`, then `localhost`; IPs `127.0.0.1` and the mesh address; key usage digital
  signature and key encipherment; extended usage server and client auth, research L4; the CSR's
  signature checked, its subject and extensions ignored; only ECDSA P-256 and Ed25519 keys),
  `Check(caCert, certPEM, keyPEM []byte, kind Kind, now) error` (chains, matches the key, valid now, has
  the kind's DNS name), `NotAfter(certPEM)`. Tests: a signed certificate verifies against the CA with
  `x509.VerifyOptions{DNSName: "server.global.nomad"}`; a client certificate fails that name; a CSR with
  an RSA key, a bad signature or two blocks is refused; `Check` refuses another CA, another key, an
  expired one.
- [x] T003 `internal/nodeagent/state.go`: `State`, reading and writing `state.json` (temp file, rename,
  0600), `node.env` read as text (the installer's `env_value` rule) and written, the paths under a root
  folder, the valid name of a host name (node.md §3). Tests.
- [x] T004 `internal/nodeagent/nomadhcl.go`: `RenderNomad(State, gossipKey string) string` with
  `text/template`, node.md §4, the comments of `deploy/install/nomad.hcl.tpl` carried over. Goldens in
  `testdata/`: `first.golden.hcl`, `first-with-servers.golden.hcl`, `client.golden.hcl`,
  `server.golden.hcl`, `client-cpu.golden.hcl`. A test checks the first server's golden against today's
  template: the same `client` settings (`cni_path`, `network_interface`, the `traefik-acme` volume), the
  `acl`, `plugin "docker"` and `telemetry` blocks.
- [x] T005 `internal/nodeagent/validate.go`: node.md §5, each rule a table case; the error names the
  field and never quotes a key, a token or a certificate.
- [x] T006 `internal/nodeagent/up.go` and `report.go`: `Up` (the `ip` and `wg` calls of node.md §3,
  through the `Runner`; the config text goes to a 0600 temp file under the agent's folder, removed after
  `wg setconf`/`syncconf`), `Report` (node.md §6). Tests with a recording runner: the exact commands for
  index 1 and index 3, idempotent second run, no key in any argument.
- [x] T007 `internal/nodeagent/join.go`, `sync.go`, `initfirst.go`, `leave.go`: the commands of node.md
  §3 and §6. Tests against an `httptest` console: a join writes the five files with their modes and
  nothing on a refusal; a second `join` makes no call; `sync` applies new peers with `wg syncconf` and no
  Nomad restart; a promotion asks for a certificate, writes `gossip.key`, renders a server's `nomad.hcl`
  and restarts Nomad once; a renewal under 30 days writes key then certificate and reloads; a bad answer
  (each rule of §5) changes no file; a 401 and a 503 exit 0 and change nothing; `init-first` twice is one
  CA; a sentinel token, private key and gossip key appear in no stderr line.
- [x] T008 `cmd/caravana/node.go`: `caravana node init-first|join|up|sync|show|leave` with the flags of
  node.md §3, wired to the real host (root folder `/`, `exec.CommandContext` runner with a 30 s timeout,
  an HTTP client with a 20 s timeout that doesn't follow redirects and skips verification only with
  `--insecure` or `CARAVANA_INSECURE=1`). It refuses to run when not root (`CARAVANA_NODE_ROOT`, a folder,
  lifts that for tests only). Usage text in `caravana help`. `--token` is read from the flag (the
  installer passes it) and never echoed.
- [ ] T009 `cmd/caravana/server.go`: `caravana server adopt-first --name N --public-key K --address A`
  (opens the store beside a running server, as `admin` does; prints the node token and nothing else on
  stdout). The service method is T013's; until then a stub that fails the build is fine inside phase A's
  own commits only if phase B follows at once. Prefer: write it in phase B.

## Phase B: store, service, API

- [ ] T010 `internal/store/migrations/0024_servers.sql` and `store/servers.go`, api.md §1. Tests: the
  lowest free index (1, 2, then 2 again after a delete), `ErrClusterFull` at 63, `ConsumeJoin` wins once
  under two goroutines, an expired or used hash misses, the unique name.
- [ ] T011 `internal/deploy/nodes.go` (`Node`, `Nodes(ctx)`: `Nodes().List` with resources, the app
  address from `privateAddress`) and `cluster.go` (`ClusterCA`, `GossipKey`, `ErrNoCluster`), with
  `deploytest` fakes (nodes that can be added, marked down, removed; the two variables).
- [ ] T012 `internal/service/servers.go`: `ClusterStatus`, `CreateJoin` (the command of api.md §2),
  `Join`, `NodeSync`, `NodeCertificate`, `ListServers`, `Server`, `ServerJoin`, `RemoveServer`,
  `AdoptFirst`; `serverchecks.go` (the four checks, pure, table-tested); the `servers` change notice.
- [ ] T013 `internal/service/serverroles.go`: `wantedServers` (pure) and `reconcileRoles`. Table tests:
  two ready servers add nothing; three add one per call and only when the wanted ones report
  `serverRunning`; a fourth isn't added; a removed Nomad server's place is taken; the first is always
  one.
- [ ] T014 `internal/service/attention_servers.go`: `server_down`, `server_certificate_expiring` and the
  per-server `disk_nearly_full` (api.md §6) in `Attention`, `AttentionKinds`, `ProblemParts`,
  `diffProblems`' not-judged rule, `notify.Event.Server` and the four renderers.
  `TestEveryKindSaysTheSameWordsEverywhere` covers them.
- [ ] T015 `internal/api/servers.go` and `nodecalls.go`: the routes of api.md §4 and §5, the join limit
  (the sign-in limiter's store, its own key), node-token authentication, the error codes
  (`cluster_not_ready`, `cluster_full`, `server_exists`, `server_not_found`, `server_running`,
  `join_token_invalid`, `node_token_invalid`). The node calls are not behind `authenticate` and are not
  mounted on the webhook-only listener.
- [ ] T016 `api/openapi.yaml` 0.34.0: the eight operations, `Server`, `Cluster`, `ServerCheck`,
  `ServerJoin`, the node calls' schemas with a security scheme of their own, the codes,
  `AppVolume.server`, `getHost`'s `server` parameter, the two attention kinds and `server` on the webhook
  message. The route-sync test passes.
- [ ] T017 `TestScopedTokenWalksEveryRoute`: the five console operations denied to a deploy token; the
  node calls listed as not reachable with any API token.
- [ ] T018 Sentinel test (`service`, `api`): a join token, a node token, a CSR's key, the CA's key and
  the gossip key reach no log line, error, record (the SQLite file), list answer or notification;
  `addServer`'s answer is the only place the join token is.
- [ ] T019 `cmd/caravana/server.go` (T009) and `main.go` wiring.

## Phase C: what assumed one server

- [ ] T020 `deploy.HostStats(ctx, nodeID, dockerRoot)`; callers pass the node. Which node is Caravana's:
  api.md §6.
- [ ] T021 The sampler reads every ready node; `store.WriteMetricsTick` takes host rows per node;
  `service.Host(ctx, server)`; `HostView` gains `Server` and `OSRead`; `HostCopies(ctx, server)`.
  `Metrics`' room is the cluster's sum. Tests with two fake nodes.
- [ ] T022 `getHost?server=`, `Server`'s numbers in `ListServers` from the newest rows (never 0 for
  none).
- [x] T023 `nomadjob.Translate`: `Spreads` for a group with more than one copy; `nomadjob.HCL` writes
  the block; goldens for a multi-copy app change, single-copy ones don't; the reflection test passes.
- [ ] T024 `deploy.EnsureVolumes`: one server per service (api.md §6); `AppVolume.Server`. Test: two
  volumes of one service get one `NodeID`; a service with an existing volume puts a new one beside it.
- [ ] T025 `disk_nearly_full` per server (keys as api.md §6), with its level and the stale rule per
  server.
- [x] T026 `deploy/install/traefik.nomad.hcl`: the constraint (node.md §8). The lab's
  `deploy/dev/traefik.nomad.hcl` is unchanged.

## Phase D: the host

- [x] T027 `deploy/install/nomad.service`, `caravana-mesh.service`, `caravana-node.service`,
  `caravana-node.timer` (node.md §7), each with a comment header like `caravana.service`'s;
  `deploy/install/nomad.hcl.tpl` deleted.
- [x] T028 `install.sh`: options and their checks, `install.env`'s new lines, the roles' refusals
  (node.md §8). The header comment and `--help` say both roles.
- [x] T029 `install.sh --first`: steps 1 to 10 of node.md §8. The old `nomad.hcl` is kept once as
  `$STATE/nomad.hcl.before-mesh` (not in `/etc/nomad.d`, which Nomad reads whole). After each Nomad
  restart of the conversion, no leader within 2 minutes is a failure that says what was done and where
  the old file is.
- [x] T030 `install.sh --join`, `--leave`, and `--uninstall`'s additions.
- [x] T031 `tools/release.sh`: the host files (node.md §8). `deploy/install/README.md`: "What runs
  where" for both roles, "Adding a server", "Taking a server out", the ranges and the port to open.
- [ ] T032 `deploy/dev/install-test.sh`: the single-VM run still passes with the new first-server steps
  (its checks that read `nomad.hcl` or the Traefik job follow), plus: the mesh interface is up, Nomad's
  RPC answers on `10.98.0.1:4647` with TLS, `GET /v1/servers` lists one ready server, `ca-key.pem` is
  gone, and a rerun changes nothing.
- [ ] T033 `deploy/dev/cluster-test.sh` (quickstart below).
- [ ] T034 shellcheck (the repository's Docker way: `docker run --rm -v "$PWD:/mnt" koalaman/shellcheck
  …`) over the changed scripts.

## Phase E: the console

- [ ] T035 The nav item "Servers"; `/ops/servers` (the list, `servers.html`), `/parts/ops/servers`.
- [ ] T036 `/ops/servers/new`, `POST /ops/servers`, `/ops/servers/joins/{id}`,
  `/parts/ops/servers/joins/{id}`, `POST /ops/servers/joins/{id}/cancel` (`addserver.html`,
  `addserver.go`); `ops.js`'s 5 s refetch while a check is open and `data-copy`.
- [ ] T037 The server page for every server, the down banner, `GET` and `POST
  /ops/servers/{name}/remove`.
- [ ] T038 Overview's panel per server and headline; the Running tab's server; Monitoring's server
  select; Volumes' column; Updates' line; the two attention kinds' links.
- [ ] T039 `tools/ui-css.sh build` (and `check` passes); `DESIGN.md` gains the role marks ("facts, not
  statuses").
- [ ] T040 UI tests: each page with one server, three servers, a waiting join, a joining one, a down
  server, an unmanaged node, `cluster_not_ready`; forms without script; the token appears in the POST's
  answer and in no later page; the light and dark parity test lists the new routes; `TestNoShipWords`.
- [ ] T041 `TestConsoleLab` step `console-servers` (`cmd/caravana/console_servers_test.go`): the lab's
  one node is listed (unmanaged), Add a server shows `cluster_not_ready`'s sentence, `GET /v1/servers`
  agrees; no join is made (the lab has no mesh).
- [ ] T042 The designer's live review against page-servers.md, with staged data (a script like 030's
  `stage.sh`: three servers, a waiting join, a down one), then the impeccable critique, the rulings
  written into the contract, the fixes.
- [ ] T043 The last look.

## Closing

- [ ] T044 `docs/plan.md` (Phase 3's first row built, what changed from the row's text, the gate's first
  form), `CLAUDE.md` › "Where we are", `changelog.d/031-add-a-server.md` (Added, Changed, Upgrade: rerun
  the installer; Nomad restarts twice once; open UDP 51820 between servers), `docs/vision.md` unchanged.
- [ ] T045 Every check of `CLAUDE.md` › Commands, `go test -race ./...` (`-timeout 30m`), and
  `CARAVANA_LAB=1 go test ./cmd/caravana -run TestConsoleLab`.

## Quickstart: `deploy/dev/cluster-test.sh` (T033)

Three throwaway Lima VMs, `cv-cluster-1..3`, Ubuntu 24.04, 2 vCPU, 2 GB, `--network lima:user-v2`
(research L1), deleted at the end (`KEEP=1` keeps them). The release is built into
`.cache/cluster-test/<version>` under the repository, which the VMs see read-only at the same path. The
console's domain `caravana.test` and the apps' are resolved inside the VMs with `--resolve` or an
`/etc/hosts` line to server 1's address. About 25 minutes. It ends `CLUSTER TEST PASSED`.

1. **An existing host converts.** Build a release from `PRE_MESH_REF` (default `d13c37c`, the commit
   before this feature, checked out with `git worktree add --detach` into `.cache/cluster-test/pre`),
   install it on VM 1 (`--acme none --domain caravana.test`), make the admin and a token, deploy whoami
   (1 copy, a domain). Then install this tree's release while calling whoami every half second: no call
   fails; the installer says Nomad restarted twice; `raft list-peers` says `10.98.0.1:4647`;
   `$STATE/raft-mesh` exists; `ca-key.pem` doesn't; `nomad var get caravana/cluster/ca` has both items;
   `GET /v1/servers` lists one server, `ready`, `first`, `nomadServer`. A rerun says `changed:` and
   nothing after it.
2. **A refused join changes nothing.** On VM 2: a made-up token (`cvjn_` and 43 `x`) → the installer
   exits 1 after the console's 401, with the message; no `node.env`, no `nomad.hcl`. A bad `--url`, a bad
   token format: exit 2 with nothing installed.
3. **Join 2.** `POST /v1/servers` `{"name": "two"}` → the token. On VM 2: `install.sh --from … --join
   TOKEN --url https://caravana.test --insecure --address <VM 2's address>`. It prints the four checks
   and "This server joined as two." `GET /v1/servers`: two ready servers, one Nomad server,
   `survivesLoss` false. The same command again: exit 0, `changed:` empty. The token again on VM 3: 401.
4. **Join 3 and the raft.** The same for `three` on VM 3. Within 4 minutes: three servers with
   `nomadServer` true, `survivesLoss` true, and `raft list-peers` on VM 1 has three voters.
5. **Spread and names.** Deploy `spread` (whoami, 3 copies, a domain): `listCopies` has three different
   `node`s; 30 calls all 200. Deploy `pair` (busybox `front` calling `http://back/`, whoami `back`); put
   them on different servers if they aren't (stop the front copy with its server made ineligible through
   `caravana-nomad`, then eligible again) and read `front`'s log: it has whoami's answer.
6. **Volumes.** Deploy `vol` (busybox, two volumes): `listVolumes` names one `server` for both, and the
   copy's `node` is that server.
7. **Lose server 3.** `limactl stop -f`. Calling `spread` every half second for 90 s: at most 12 calls
   fail and none after the 60th second; `listCopies` shows three healthy copies on two servers;
   `GET /v1/servers` says `three` is `down`; `GET /v1/attention` has `server_down`; `DELETE
   /v1/servers/two` is 409 `server_running`. Start it again: within 3 minutes it is `ready` and a raft
   voter (its interfaces and Nomad came up by themselves after the boot, research L3).
8. **Lose server 1.** `limactl stop -f`. On VM 2, with the management token read from VM 1 earlier:
   `nomad operator raft list-peers` shows a leader that isn't server 1 within 60 s, and `spread`'s copies
   on servers 2 and 3 are running. Start VM 1: within 3 minutes `GET /v1/health` answers through
   Traefik, `spread` answers, three voters.
9. **A renewal.** On VM 2: `sudo caravana-node node sync --renew` (renew now, whatever the
   certificate's age), then: `agent.pem`'s serial changed, Nomad's `MainPID` didn't, the server is still
   `ready`, and `GET /v1/servers/two` has the new `certificateExpiresAt`.
10. **Remove.** `limactl stop -f` VM 3, wait for `down`, `DELETE /v1/servers/three` → 204; within a
    minute `wg show caravana-mesh peers` on VMs 1 and 2 has one peer; a fourth name can be added (201).
11. **Leave.** On VM 2: `install.sh --leave`: Nomad stopped, no `caravana-mesh`, no `/etc/caravana-node`;
    then `DELETE /v1/servers/two` → 204.
