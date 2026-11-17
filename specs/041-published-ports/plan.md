# Implementation Plan: Published ports

**Spec**: [spec.md](./spec.md) · **Research**: [research.md](./research.md) · **Tasks**: [tasks.md](./tasks.md)

## Summary

A port of a service may carry `published` (a fixed host port) and `protocol` (`tcp`, `udp`, `tcp+udp`). The
translator turns a published port into a Nomad static port on the host network `public`, which only the first
server's Nomad client declares (the node agent renders it from the default-route interface; the lab declares it
too). Such a group runs one copy and deploys stop-then-start, like a group with a volume. The port is open exactly
while the copy runs (Nomad's mapping); no firewall rule is written. Refusals name their field; a port another app
publishes is 409 `port_taken`; no server with the `public` network is 409 `no_public_network`. No migration (the
fields live in the app document, omitted when unset, so no revision moves), no new Go module, no Nomad policy change
(`node read` exists since 025). API **0.45.0**. Constitution **1.2.0** (VI).

## Constitution Check

- **I Small**: no module, no service, no root helper. ✓
- **II One model**: two fields of `app.Port`; image, Compose and Aspire normalize into them; `Translate` stays pure
  and golden-tested (`published`). ✓
- **III Safe deploys**: still a new job version per deploy, health-checked with auto-revert. A published group has
  no canary (Nomad can't hold one static port twice on one address), exactly as a group with a volume. Every
  service with a published port has a health check on a TCP port (refused otherwise). ✓
- **IV Fail loudly**: every refusal is a field error or a coded 409 with the field; the Compose file's `published`
  still warns unless the overlay publishes it. ✓
- **V API-first**: the schema, the codes and the overlay change in the same commit; the console reads and keeps. ✓
- **VI Nomad-native**: Nomad's own static ports and host networks; the export writes them. **Amended (1.2.0)**:
  networking is bridge mode with dynamic host ports, except a port the owner publishes, which is a static port on
  the host network `public`. ✓ after the amendment.
- **VII Secure**: only published ports are mapped on the public address; the refused list keeps SSH, HTTP(S),
  Nomad, WireGuard and Caravana's own ports out; no new privilege for Caravana (research R2). ✓
- **VIII Plain Go**: table tests, goldens, a lab step. ✓

No Complexity Tracking entry.

## Decisions

**D1 Fields.** In `internal/app`:
```go
type Port struct {
    Name          string `json:"name"`
    ContainerPort int    `json:"containerPort"`
    Protocol      string `json:"protocol,omitempty"` // "", "tcp", "udp", "tcp+udp"; "" means tcp
    Published     int    `json:"published,omitzero"` // 0: not published
}
const (ProtocolTCP = "tcp"; ProtocolUDP = "udp"; ProtocolTCPUDP = "tcp+udp")
func (p Port) SpeaksTCP() bool   // "", tcp, tcp+udp
func (p Port) IsPublished() bool // Published != 0
func (s Service) Publishes() bool
var ReservedPorts = []int{22, 80, 443, 4646, 4647, 4648, 7070, 7071, 8081, 51820} // with a comment per port
const MaxPublishedPorts = 16
```
No normalisation of `"tcp"` to `""` (the document holds what was sent).

**D2 Validation (pure, `app.ValidateApp` path, in `validate.go` beside the ports rules).** In order, each a
`*FieldError`:
- `services[i].ports[j].protocol`: one of the four values.
- `services[i].ports[j].published`: 1–65535 when set; not in `ReservedPorts` ("is reserved: <what uses it>"); not
  published twice in the app (the second occurrence's field, naming the first's service and port).
- `services[i].ports`: more than 16 published ports.
- `services[i].replicas`: above 1 while the service publishes ("a service with a published port runs one copy:
  one address can hold a fixed port once").
- `services[i].domains[j].port`: names a port that doesn't speak TCP.
- the health check's port (`healthPort` logic: `health.port`, else the domains' port, else the only port) doesn't
  speak TCP: field `services[i].health.port` when it was named, else `services[i].health`.

**D3 Translate.** `taskGroup`: a published port goes to `Networks[0].ReservedPorts` as
`api.Port{Label, Value: Published, To: ContainerPort, HostNetwork: PublicHostNetwork}`; the others stay in
`DynamicPorts`, both in the service's port order. A group that publishes gets `Update{Canary: 0, AutoPromote:
false}` (the volume rule; share one helper `stopThenStart(svc)` = volumes || publishes, with the comment moved).
`PublicHostNetwork = "public"` lives in `internal/mesh` (pure, imported by both `nomadjob` and `nodeagent`), with a
comment that renaming it moves every published service. `ScheduleRunJob` has no ports and stays unchanged (test it
with a publishing service). New golden `published` (one service: a TCP health port `http` dynamic, `turn`
`tcp+udp` published 3478; a second service with a domain, to show it keeps canaries) in JSON and HCL. The HCL writer
writes `port "turn" { static = 3478  to = 3478  host_network = "public" }`; the reflection test covers
`ReservedPorts` and `HostNetwork`. The export header gains a line when any port is published: the job needs a
Nomad client with `host_network "public"`.

**D4 Taken across apps.** `service.checkPortsTaken(ctx, a)` next to `checkDomainsTaken` (called at
`service/deploy.go` beside it, so every deploy path, Compose, promotion and template install go through it):
every stored app other than `a.ID`, stopped ones included, claims its published numbers; the first claim found is
`&PortTakenError{Field, Port, App}` (`Unwrap() → ErrPortTaken`), mapped in `internal/api` to 409 `port_taken` with
the field. No grandfathering (the feature is new).

**D5 The public network.** `deploy.(*Deployer)` gains `checkPublicNetwork(ctx, a) (node string, err)` run before
`EnsureVolumes` and `Register` when any service publishes: `Nodes().List`, then `Nodes().Info` of the ready,
eligible ones, keeping those whose `HostNetworks` has `public`. None → `*PublicNetworkError{Field}`
(`Unwrap() → ErrNoPublicNetwork`) on the first published port's field, message "no server offers published ports:
rerun the installer on the first server", mapped to 409 `no_public_network`. A Nomad read failure is the existing
`ErrNomadUnavailable`. `deploytest` gains nodes with host networks.

**D6 Volumes.** `EnsureVolumes` for a service that publishes: a volume to create goes to a node that offers
`public` (the first found by D5) instead of the pool; a service whose existing volumes are on a node without it is
refused with a field error on its first published port ("its volumes are on a server that offers no published
ports").

**D7 Promotion and copy.** `promoted()` takes the source's ports (as today) but sets each port's `Published` from
the target's port of the same name (0 when the target has none); it is reported in `Changed` only through the
existing `ports` entry. `CopyApp` sets every `Published` to 0 and clears the kept overlay's `Publish`. The comment
above `promoted` lists it with the target's other kept fields.

**D8 Compose.** `internal/compose`:
- `servicePorts`: `pc.Protocol` `""`/`tcp`/`udp` accepted (anything else still refused naming the field); a TCP and
  a UDP entry for one target make one port with `tcp+udp`; `expose` accepts `N`, `N/tcp`, `N/udp`. The warning for a
  file's `published`/`host_ip`/`mode: host` now says: `the host port isn't opened; to publish port %d on the
  server's address, add {"port": %d} to the overlay's publish list`.
- `Overlay.Publish []Publish` (`publish`), `Publish{Port int; Published int}` (`port`, `published,omitzero`;
  `published` 0 = the same number as `port`). Applied in `applyOverlay`: an unknown container port is a field error
  `services.<svc>.publish[k].port`; app validation errors on a published port are renamed to
  `services.<svc>.publish[k].published` like the backup ones.
- `Request.WithoutSecretValues` keeps `Publish` (no secret in it).

**D9 Console (read and keep).** `internal/ui`:
- Configure's service panel: a read-only "Published ports" block per publishing service, rows like
  `3478` · `tcp+udp` · `→ container 3478`, and one line: "Open on the address of server `<first server name>`
  while the copy runs. Set through the API or the Compose overlay." (server name from `ListServers` when managed;
  else "this server"). Same for a kept-file app.
- `applyEdit`/`merge.go`: a save of the service form keeps `Protocol` and `Published` of the ports it doesn't edit
  and of the port it edits (by name).
- The Compose form keeps the overlay's `publish` list per service (`keptPublish`, `keepPublish(rec)`, exactly like
  `keepIdentities`, including the "overlay is empty" test at `composeform.go:407`).
- No new page, no new script; `tools/ui-css.sh build` if classes change.

**D10 API 0.45.0.** `api/openapi.yaml`: `Port.protocol` (enum), `Port.published` (1–65535), the Compose overlay's
`publish`, codes `port_taken` and `no_public_network` in the error code list, the 409 responses on `deployApp`,
`deployCompose`, `promoteApp`, `installTemplate`, `createApp` (when it deploys). Parity and schema tests pass.

**D11 Node agent and installer.**
- `nodeagent.State.PublicInterface string` (`publicInterface`, omitempty). `Validate`: empty or
  `^[A-Za-z0-9_.-]{1,15}$`.
- `InitFirst` reads `ip -4 route show default` (through `a.run`), takes the `dev` word of the first line, and saves
  it in the state when it differs (a host with no default route keeps ""). Tests with the fake runner.
- `nomadTemplate`, inside `{{- if .First}}` of `client {}`: when `PublicInterface` is set,
  ```hcl
    # Published ports (spec 041): a service's fixed port is a static port on this network, the IPv4
    # address of the default-route interface. Only this server declares it, so published services run here.
    host_network "public" {
      interface = "eth0"
      cidr      = "0.0.0.0/0"
    }
  ```
  Goldens: `first.golden.hcl` and friends gain it (give the fixtures an interface); a joined server never renders it.
- `install.sh`: nothing new to write; the rerun's `restart_nomad_if_changed` restarts Nomad once because the config
  changed. Add one line to the step's output on that path is not needed. `deploy/install/` keeps `ufw` as is.
- `deploy/dev/install-test.sh`: after install, check `/etc/nomad.d/nomad.hcl` has `host_network "public"` and
  `nomad node status -self -json` lists `public`; deploy an echo service published on a free port through the API,
  reach it from `docker run --rm --network bridge busybox` on the host (TCP, forwarded through ufw as external
  traffic is), delete the app and see it closed.

**D12 Lab.** `deploy/dev/nomad.hcl` gains the same `host_network "public"` block (`interface = "eth0"`, the Lima
VM's). `lab.sh up` installs `deploy/dev/nomad.hcl` into `/etc/nomad.d/nomad.hcl` when it differs and restarts Nomad
(waiting until it answers), so an existing lab needs no `delete`; the comment in `lab.yaml` changes accordingly.

**D13 Docs.** `docs/published-ports.md` (what it is, the one-copy rule and the deploy gap, the refused list, where it
runs, the firewall note, calls: LiveKit's single-port mode, coturn's relay range). `docs/plan.md` gets the feature
line. `CLAUDE.md` gets the feature paragraph. `changelog.d/published-ports.md`.

## Tests

- `app`: validation table for every D2 rule and its field path; JSON of a port without the new fields is unchanged
  (revision test).
- `nomadjob`: golden `published` (JSON, HCL), stop-then-start for a publishing group, schedule run of a publishing
  service has no ports, HCL reflection, export header line.
- `compose`: udp ports, the tcp+udp merge, expose `/udp`, overlay `publish` (default number, explicit number,
  unknown port, a reserved number renamed to the overlay path), the new warning text, an existing fixture unchanged.
- `service`: `port_taken` (other app, stopped other app, the same app redeploying keeps its port), promotion keeps
  the target's numbers, copy drops them, `no_public_network` passes through.
- `deploy`: `checkPublicNetwork` (no node, a node without, a down node with it, one with it), volumes placed on the
  public node, existing volumes elsewhere refused.
- `nodeagent`: interface parse, validate, goldens, never on a joined server.
- `api`: codes and fields of both 409s, schema parity.
- `ui`: Configure shows the block and a save keeps the fields; the Compose form keeps `publish`.
- Lab gate step `published-ports` (`cmd/caravana/gate_published_test.go`): an echo service (`alpine/socat`, TCP
  health port `http`, a `tcp+udp` port published on 3478 or a free high port), healthy; from a busybox client app's
  copy (Run a command) `nc` over TCP and UDP to the server's address (read from the copy's allocation through Nomad)
  gets the echo; on a real host (`TestGate`) the test process also dials `<host IP>:<port>` itself, both protocols;
  refusals: reserved port, `port_taken` from a second app, `replicas: 2`, UDP-only health; `exportMatchesLive` for
  the app; delete it and the port no longer answers. Runs alone with `-run 'TestLab/^(api-needs-token|published-ports)$'`.
