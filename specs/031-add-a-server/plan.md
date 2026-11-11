# Implementation Plan: Add a server

**Feature**: `031-add-a-server` | **Spec**: [spec.md](spec.md) | **Research**: [research.md](research.md)
**Contracts**: [node.md](contracts/node.md), [api.md](contracts/api.md),
[page-servers.md](contracts/page-servers.md)

## Summary

A server joins with a command made in the console. The installer gains a `--join` role. Servers form a
WireGuard mesh whose addresses derive from one index per server. Nomad's RPC and raft get TLS from a CA
that Caravana holds in a Nomad Variable, and serf gets a gossip key. A root agent (the `caravana` binary,
`caravana node …`, on a 30 s timer) keeps each server's peers, role and certificate in line with what
Caravana wants. With three ready servers the two oldest joined ones become Nomad servers too. The
console gets Servers and Add a server, and the pieces that assumed one server are made to say which.

## Technical context

- Go 1.27, no CGO, no new module: WireGuard keys with `golang.org/x/crypto/curve25519` (the module is
  direct since 023), certificates with `crypto/x509`.
- New packages: `internal/mesh` (pure: addresses, keys, the `wg` config text), `internal/clusterca`
  (pure: the CA, signing a CSR, a key and CSR, checking a certificate), `internal/nodeagent` (the root
  agent; host access behind a struct the tests replace).
- Changed: `cmd/caravana` (`node …`, `server adopt-first`), `internal/store` (migration 0024),
  `internal/service` (servers, roles, attention, sampler and host per server), `internal/deploy` (nodes,
  the cluster's variables, host stats by node, volumes on one server), `internal/nomadjob` (spread),
  `internal/api` (+ `api/openapi.yaml` 0.34.0), `internal/ui` (Servers, Add a server, the server page,
  the deltas), `internal/notify` (`Server`), `deploy/install/*`, `tools/release.sh`,
  `deploy/dev/cluster-test.sh` (new), docs.
- The dev lab (`deploy/dev/`, one VM, no mesh, no TLS) doesn't change: Caravana must work without a
  cluster (US3.3), and `TestLab` proves it does.

## Constitution check

| Principle | How this plan stands |
| --- | --- |
| I. Small by default | One binary still: the agent is the same binary. No new service; `wireguard-tools` is a package. No new module. |
| II. One model | `Translate` stays pure; it gains `Spreads` for multi-copy groups. No new helper task or job. |
| III. Safe deploys | Unchanged. A spread is part of the job a deploy registers. |
| IV. Fail loudly | A join that can't finish names its open check; a pool that takes no apps says so; volumes on two servers can't happen (D9). |
| V. API-first | Every page reads `service`; `listServers`, `addServer`, `getServer`, `getServerJoin`, `removeServer` and the three node calls are in `openapi.yaml` in the same commit. |
| VI. Nomad-native | The official client; routing still by tags; Traefik's job gains a constraint; Nomad code stays in `deploy` and `nomadjob` (the agent renders Nomad's config, which is host setup, as the installer did). |
| VII. Secure by default | This is the multi-node clause: Nomad TLS, gossip encryption and an encrypted network, from the first install. The CA's key and the gossip key live in Nomad Variables. No token or key is logged (FR-015). |
| VIII. Plain, tested Go | Pure packages with table tests and goldens; the agent tested against a fake host; the three-VM test for what only machines can show. |

No violation, so no Complexity Tracking entry.

## Structure

```text
internal/mesh/            addresses.go keys.go config.go (+ tests, an import test)
internal/clusterca/       ca.go (+ tests)
internal/nodeagent/       agent.go state.go nomadhcl.go join.go sync.go up.go report.go validate.go
                          testdata/*.golden.hcl (+ tests with a fake host and an httptest console)
cmd/caravana/             node.go (the commands), server.go (adopt-first)
internal/store/           migrations/0024_servers.sql, servers.go
internal/deploy/          nodes.go (Nodes, HostStats by node), cluster.go (ClusterCA, GossipKey), volumes.go
internal/service/         servers.go serverroles.go serverchecks.go attention_servers.go (+ sampler, host)
internal/api/             servers.go nodecalls.go (+ scope walk, openapi sync)
internal/ui/              servers.go addserver.go templates/servers.html addserver.html (+ opsserver, ops)
deploy/install/           install.sh nomad.service caravana-mesh.service caravana-node.service
                          caravana-node.timer traefik.nomad.hcl (nomad.hcl.tpl deleted)
deploy/dev/cluster-test.sh
```

## Order of work

Five pieces, each handed to one implementing session and reviewed (diff, contract, tests) before the next:

1. **A. The pure packages and the agent** (`mesh`, `clusterca`, `nodeagent`, `cmd/caravana node`). No
   dependency on the rest. Tasks T001–T009.
2. **B. Store, service, API** for servers and the node calls (T010–T019). Needs A's `mesh` and
   `clusterca`.
3. **C. What assumed one server** in `deploy`, `nomadjob`, the sampler and `Host` (T020–T026). Needs B's
   `Nodes`.
4. **D. The host**: installer, units, release, the cluster test (T027–T034). Needs A's commands and B's
   calls; ends with `cluster-test.sh` passing on three VMs.
5. **E. The console** (T035–T043). Needs B and C. Then the designer's review and the critique.

Then docs, the changelog note, the checks of `CLAUDE.md` › Commands, and the lab (`TestConsoleLab`).

## Risks

- **The conversion of an existing host** (US3) is the one step that can hurt a real installation. It is
  two Nomad restarts with a recovery file in between (L2). The cluster test converts a host installed
  from the commit before this feature with an app answering throughout, and the installer stops with a
  clear message if Nomad has no leader after either restart, leaving the old `nomad.hcl` beside the new
  one (`nomad.hcl.before-mesh`).
- **A rolled-back Caravana** (030) must not take the agent away: the agent is its own copy of the binary
  (node.md §2), and an older Caravana answers 404 to its sync, which changes nothing.
- **Lima** is the only place the cluster test runs; a real three-VM proof at a provider is the
  maintainer's (`deploy/vm/host.sh` isn't extended here).
