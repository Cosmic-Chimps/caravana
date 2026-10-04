# Research: Real HTTPS and Auto-Revert on a Cheap VM

Decisions for feature 005. Versions as in the lab: Nomad 2.0.7, CNI 1.9.1, Traefik v3.7.13,
Ubuntu 24.04 LTS. No change to Caravana's own code is expected (spec FR-016); the work is
scripts, host configuration and one refactor of the lab test.

## R1. Shape: `deploy/vm/`, driven from the Mac over SSH

- **Decision**: a new `deploy/vm/` folder next to `deploy/dev/`. One entry script,
  `deploy/vm/host.sh`, with the same subcommand style as `lab.sh`: `up` (set up or update the
  host), `env` (print exports for a tunnel), `tunnel` (open it), `prove` (run the gate proof),
  `ssh`. Inputs come from `deploy/vm/host.env` (gitignored; `host.env.example` committed).
- **How it runs**: `up` cross-compiles Caravana for the host's architecture on the Mac
  (`CGO_ENABLED=0 GOOS=linux GOARCH=$(ssh … dpkg --print-architecture)`), copies the binary and
  the setup files with `scp`, and runs `deploy/vm/setup.sh` on the host with `sudo`. No agent,
  no provider API, no Terraform (spec Assumptions).
- **Rationale**: the same mental model as the lab, nothing to install on the host but what the
  host needs, and every step is a plain script a maintainer can read.

## R2. Reuse the lab's bootstrap

- **Decision**: `deploy/vm/setup.sh` writes the host's Nomad config to `/etc/nomad.d/nomad.hcl`
  first, then runs the lab's `deploy/dev/setup.sh` unchanged (Docker, Nomad 2.0.7 with checksum,
  CNI, bridge sysctls, the systemd unit, ACL bootstrap, the `lab-nomad` helper), then adds what
  only a public host needs (R3–R9). `deploy/dev/setup.sh` gets one change: its state directory
  becomes an environment variable with today's value as the default, so the VM can use
  `/var/lib/caravana-host` and the lab keeps `/var/lib/caravana-lab`.
- **Rationale**: one bootstrap for both, so the lab keeps rehearsing what the VM runs (and what
  012 will ship).

## R3. Nothing public but 22, 80, 443

- **Nomad**: the VM's `nomad.hcl` sets `bind_addr = "127.0.0.1"` (HTTP, RPC and Serf on loopback;
  fine for one node). The operator reaches the API through the tunnel (R10).
- **Caravana**: `CARAVANA_LISTEN=127.0.0.1:7070`, `CARAVANA_WEBHOOK_LISTEN=127.0.0.1:7071`;
  only Traefik's static route (R6) reaches the webhook listener.
- **Traefik**: the dashboard entrypoint moves to `127.0.0.1:8081`; `web` (80) and `websecure`
  (443) stay public.
- **Firewall**: `ufw` with default deny incoming, allow 22/80/443. It's the second layer: by
  then nothing else listens publicly anyway.

## R4. App ports must not bypass Traefik (spec FR-005a)

- **Problem**: in bridge mode Nomad's CNI `portmap` plugin installs DNAT rules for each dynamic
  host port. DNAT happens in `PREROUTING`, before `ufw`'s `INPUT` rules see the packet, so a
  firewall alone doesn't close `http://<vm-ip>:<port>`.
- **Decision**: make the address those ports are bound to a private one. Setup creates a dummy
  network interface `caravana0` with `10.99.0.1/32` (systemd-networkd `.netdev` + `.network`, so
  it survives reboots) and sets Nomad's client `network_interface = "caravana0"`. Nomad then
  binds every allocation port to `10.99.0.1`, the DNAT rules only match traffic addressed to
  `10.99.0.1`, and packets from the internet (addressed to the public IP) never match. Service
  registrations carry `10.99.0.1:<port>`, which Traefik (host network, same machine) reaches.
- **Why not loopback**: DNAT to `127.0.0.1` needs `route_localnet`, which loosens loopback
  protections; a dummy interface needs no kernel tweak.
- **Verification**: the lab can't prove it (its "public" side is the Mac), so the proof checks
  it on the host: it reads whoami's allocation port from Nomad and tries `<public-ip>:<port>`
  from the Mac, expecting a refusal or timeout (spec FR-013, SC-004). If Nomad 2.0.7 turns out
  to bind differently with a dummy interface, the fallback is a named `host_network` on the same
  address; both keep the rest of this plan unchanged.

## R5. Let's Encrypt with HTTP-01

- **Decision**: the VM's Traefik job (`deploy/vm/traefik.nomad.hcl`, HCL2 variables for the
  domain names, email and CA) adds a resolver named `letsencrypt`:
  `--certificatesresolvers.letsencrypt.acme.httpchallenge.entrypoint=web`,
  `…acme.email=${email}`, `…acme.storage=/acme/acme.json`, and for staging
  `…acme.caserver=https://acme-staging-v02.api.letsencrypt.org/directory` (spec FR-010).
  `web` redirects to `websecure` (`--entrypoints.web.http.redirections.entrypoint.to=websecure`);
  Traefik keeps serving the challenge path on `web` itself.
- **Persistence**: `acme.json` lives on a Nomad host volume `traefik-acme` at
  `/var/lib/caravana-host/traefik` (declared in the client config, mounted into the job), so
  restarts and reboots reuse certificates (spec edge case). Constitution IV's "volumes are
  explicit host volumes" is followed for infrastructure too.
- **Caravana**: `CARAVANA_CERT_RESOLVER=letsencrypt`, so every app router gets
  `tls.certresolver=letsencrypt` (feature 001's setting); nothing else changes in Caravana.
- **Access log**: not enabled (Traefik's default), so no request URL with `?token=` is ever
  written (spec FR-009). The VM job says so in a comment, so nobody turns it on casually.

## R6. The static webhook route (clarification Q1)

- **Decision**: Traefik's file provider, with the file rendered by the job's `template` block:

  ```yaml
  http:
    routers:
      caravana-webhook:
        rule: Host(`<webhook-host>`) && PathPrefix(`/v1/webhooks/`)
        entryPoints: [websecure]
        tls: {certResolver: letsencrypt}
        service: caravana-webhook
    services:
      caravana-webhook:
        loadBalancer:
          servers: [{url: "http://127.0.0.1:7071"}]
  ```

  Traefik runs in host network mode, so `127.0.0.1` is the host's loopback. Anything else on the
  webhook host has no router and gets Traefik's 404.
- **Constitution VI**: recorded in the plan's Complexity Tracking as the one route not made from
  service tags, for the control plane only.

## R7. Caravana as a system service

- **Decision**: a `caravana` system user; the binary at `/usr/local/bin/caravana`; data in
  `/var/lib/caravana` (0700, owned by the user); settings in `/etc/caravana/caravana.env`
  (0600, root) loaded by `EnvironmentFile=`; a systemd unit `caravana.service` with
  `Restart=on-failure`, `After=nomad.service`, and basic hardening (`NoNewPrivileges`,
  `ProtectSystem=strict`, `ReadWritePaths=/var/lib/caravana`, `PrivateTmp`).
- **Nomad token**: not the management token. Setup creates an ACL policy `caravana` (namespace
  `default`: `submit-job`, `read-job`, `list-jobs`) and a client token for it, written only to
  `caravana.env`. A cold start never waits on anything else (plan item 012).
- **Upgrades**: `up` copies the new binary to a temp path, compares it with the installed one,
  and only if different moves it into place and restarts the service; Caravana's resume
  (feature 003) picks up unfinished deployments.

## R8. Idempotency

- **Decision**: every file the host setup writes goes through a `write_if_changed` helper (write
  to a temp file, `cmp`, move only if different, report whether it changed); services restart
  only when one of their files changed; `nomad job run` of an unchanged Traefik job is a no-op
  in Nomad (no new version); ACL policy apply is idempotent; the Caravana token is created only
  when `caravana.env` has none.
- **Proof of idempotency** (spec SC-005): `host.sh up` prints a summary line "changed: <list>",
  empty on a second run; the quickstart checks it, and the Traefik job's version is compared
  before and after.

## R9. CPU total on ARM VMs

- **Decision**: the generated `nomad.hcl` sets `cpu_total_compute` only when `lscpu` reports no
  MHz (the lab's finding): `nproc × 2000`. Other hosts let Nomad fingerprint.

## R10. Private access: the tunnel

- **Decision**: `host.sh tunnel` runs `ssh -N -L 17070:127.0.0.1:7070 -L 14646:127.0.0.1:4646
  -L 18081:127.0.0.1:8081 <host>`; `host.sh env` prints `NOMAD_ADDR=http://127.0.0.1:14646`,
  `NOMAD_TOKEN=<management token read over ssh>`, `CARAVANA_URL=http://127.0.0.1:17070`. Local
  ports differ from the lab's so both can run at once.

## R11. One set of gate checks (clarification Q2)

- **Decision**: move the gate steps from `cmd/caravana/lab_test.go` into
  `cmd/caravana/gate_test.go` as `runGate(t, gateTarget)`, where `gateTarget` holds the API URL,
  webhook URL, app host and URL, an HTTP client for the app (the lab's skips verification and
  dials `127.0.0.1:8443`; the VM's uses the system roots), the Nomad client, and for the VM the
  public IP for port checks. Each step is a `t.Run` subtest, and a failed step stops the rest.
- `TestLab` (`CARAVANA_LAB=1`) starts Caravana in-process and calls `runGate` with the lab
  target, as today. `TestGate` (`CARAVANA_GATE=1`) builds the target from env (`GATE_API`,
  `GATE_WEBHOOK`, `GATE_APP_HOST`, `GATE_PUBLIC_IP`, `NOMAD_ADDR`, `NOMAD_TOKEN`) and adds the
  VM-only steps: certificate publicly trusted (and "staging" reported as such), redirect from
  HTTP, only 22/80/443 answer, whoami's allocation port closed, nothing but the webhook on the
  webhook host.
- `host.sh prove` opens the tunnel, exports those variables, runs `go test ./cmd/caravana -run
  TestGate -v -timeout 20m`, and turns the subtest lines into "PASS step" / "FAIL step: why".

## R12. CI

- **Decision**: add a `shellcheck` step for `deploy/dev/*.sh` and `deploy/vm/*.sh` to
  `.github/workflows/ci.yml` (shellcheck is preinstalled on GitHub's Ubuntu runners), and fix
  whatever it reports in the lab scripts. Locally: `brew install shellcheck`. The Go refactor is
  covered by the existing checks; `TestGate` and `TestLab` skip without their env vars.

## R13. What the operator must do by hand (documented, not scripted)

- Create the VM (Ubuntu 24.04, SSH key login) and note its IP.
- Create DNS records: `whoami.<domain>` and `<webhook-host>` (or a wildcard) → the IP.
- Allow 22/80/443 in the provider's firewall if it has one.
- Fill in `deploy/vm/host.env`.
- `host.sh up` checks each DNS name resolves to the host and warns otherwise (spec edge case).
