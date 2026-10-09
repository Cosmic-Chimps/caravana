# Research: One-command install

## R1. What the user runs

**Decision**: `deploy/install/install.sh`, a bash script. Each release publishes it with its
version baked in. The README command is:

```bash
curl -fsSL https://github.com/Cosmic-Chimps/caravana/releases/latest/download/install.sh | sudo bash -s -- --email you@example.com --domain caravana.example.com
```

The script downloads `caravana_<version>_linux_<arch>.tar.gz` and `SHA256SUMS` from its own
release and checks the checksum. The tarball holds the binary and the files the host needs
(`nomad.hcl.tpl`, `traefik.nomad.hcl`, the two ACL policies, `caravana.service`). The script
unpacks it to `/opt/caravana/<version>/`. `--from <dir>` uses a tarball and `SHA256SUMS` from a
local directory instead of downloading. The checksum is still checked. `host.sh up` and the lab
test use it.

**Rationale**: the host files stay real files: reviewable, shellchecked, and read by tests (the lab
test reads the policy). The script stays short. bash is on every Ubuntu.

**Alternatives considered**:

- Everything in heredocs in one script. The files would become strings nobody can lint or reuse.
- A `caravana install` Go subcommand. It would put apt, systemd and ufw calls in Go for no gain,
  and it couldn't install the things Caravana itself runs on.

## R2. Releases

**Decision**: `tools/release.sh build <version> <out-dir>` builds both architectures with
`-ldflags "-X main.version=<version>"`. It makes the two tarballs, `install.sh` with its version
set, and `SHA256SUMS`. `.github/workflows/release.yml` runs it on a `v*` tag push and publishes
with `gh release create`, which is preinstalled on GitHub's runners. CI runs `build` into a temp
directory on every push, so the script can't rot between releases.

**Rationale**: one script makes every artifact. The release, the rehearsal and the lab test all
install the same shape. There is no new tool (goreleaser) and no new module.

**Constraint found**: the repository is private. `releases/latest/download/...` needs
authentication until it is public, so the one-line command only works for others once the
maintainer makes the repository public and pushes a tag. Until then `--from` covers every test.
Nothing in the code depends on visibility.

## R3. Versions

**Decision**: `var version = "dev"` in `cmd/caravana`, set by ldflags. `caravana version` prints
it. `GET /v1/health` answers `{"status":"ok","version":"…"}` (`api/openapi.yaml` 0.9.1). The
installer compares the release's version with `/opt/caravana/current/caravana version` using
`sort -V`:

- older: refused before anything changes
- equal: no binary change
- newer: upgrade

Rehearsal and lab builds use `0.0.0-dev.<unix time>`, which sorts before any release and after
each other.

**Rationale**: the health answer lets the installer confirm that the version it just started is
the one answering, not a stale process.

## R4. Upgrade and putting the previous version back

**Decision**: versions live side by side in `/opt/caravana/<version>/`. `/opt/caravana/current`
is a symlink, and the two newest versions are kept. An upgrade runs these steps:

1. Stop `caravana`. Nomad, Traefik and the apps keep running.
2. Copy `/var/lib/caravana` to `/var/lib/caravana.pre-<old version>` (`cp -a`, one copy kept).
3. Point `current` at the new version, then start it.
4. Wait up to 60 s for `/v1/health` to answer with the new version.
5. On failure, stop it, put back the data copy and the symlink, start the old version, print the
   journal tail, and exit 1.

**Rationale**: a new version may migrate the store, and the old binary then refuses a store that
is too new (`ErrStoreTooNew`). So the data has to go back together with the binary. The store is
a few MB. Apps never stop, because Nomad runs them.

## R5. The console in public

**Decision**: Traefik's file provider gets one router, `caravana`: `Host(<console domain>)` on
`websecure` with the Let's Encrypt resolver, sent to `http://127.0.0.1:7070`. It replaces the
webhook-only route, because the main listener already serves `POST /v1/webhooks/{id}`. Caravana's
env gains `CARAVANA_TRUSTED_PROXIES=127.0.0.1,::1`, so it trusts Traefik's forwarded headers.
That makes the cookie `__Host-` and Secure, keeps sign-in limits per real client address, and
makes the console show https URLs. The env loses `CARAVANA_WEBHOOK_LISTEN`. The setting stays in
Caravana for setups that want a webhook-only listener, and the lab test still uses it.

**Checked**: Go's `CrossOriginProtection` compares Origin with Host. Traefik passes Host through,
so it needs no setting. The request log never writes query strings, and Traefik's access log stays
off.

The Traefik route goes live after the admin step (R6), so the first visitor never meets a console
without an admin.

## R6. The admin

**Decision**: with a terminal (`/dev/tty` readable, which works under `curl | bash`), the installer
runs `caravana admin init --email <email> </dev/tty`, which asks for the password twice. Without
one, it prints `sudo caravana admin init --email <email>`. A rerun skips the step when
`caravana admin status` says an admin exists.

`/usr/local/bin/caravana` is a small wrapper. It requires root, reads `/etc/caravana/caravana.env`,
and runs `/opt/caravana/current/caravana` as the `caravana` user with `setpriv`. So
`sudo caravana admin …` just works on the right data. systemd runs the binary directly.

## R7. Preflight

**Decision**: these checks run before any change, in this order, and the first failure stops the
run with a sentence naming it:

1. root
2. Ubuntu 24.04 (`/etc/os-release`)
3. amd64 or arm64
4. MemTotal ≥ 1.8 GB (a "2 GB" VM reports about 1.9)
5. ports 80 and 443 free, or held by our Traefik on a rerun (`ss -ltnp`)
6. no Nomad this installer didn't install (no `/var/lib/caravana-host/install.env`), and no Docker
   other than Ubuntu's `docker.io` (it would conflict)
7. the console domain resolves to the public IP

The public IP comes from `https://checkip.amazonaws.com`. Without `--domain`, the domain is
`<ip with dashes>.sslip.io`.

`--acme none` uses Traefik's default certificate and skips the DNS check. It is for the lab test
and private networks. `--acme staging` uses Let's Encrypt's staging CA.

## R8. Rerunning

**Decision**: the installer records its inputs in `/var/lib/caravana-host/install.env`:

- version
- domain
- ACME mode and email
- admin email

A rerun without options reuses them, so the same one-line command upgrades. Options given on a
rerun replace the recorded ones. Each step writes only what changed (the `write_if_changed`
helper from `deploy/vm/setup.sh`), and the run ends with `changed: <list>`, empty when nothing
changed. An interrupted run is finished by the next one, because every step checks before it
acts.

## R9. Uninstall

**Decision**: `install.sh --uninstall` does this:

- stops and disables `caravana`
- removes its unit, the wrapper and `/opt/caravana`
- revokes Caravana's Nomad token
- runs the Traefik job again without the `caravana` route

`--purge` also deletes `/var/lib/caravana`, `/etc/caravana` and the `caravana` user. Nomad,
Docker, Traefik, the firewall and every app stay. The README says how to remove those by hand.

## R10. Where the files go, and the rehearsal

**Decision**: `deploy/vm/` keeps only `host.sh`, `host.env.example` and its README.

- `setup.sh`'s logic moves into `install.sh`.
- `nomad.hcl.tpl`, `traefik.nomad.hcl`, `caravana-policy.hcl` and `caravana.service` move to
  `deploy/install/`, with a copy of `traefik-policy.hcl`. The lab keeps `deploy/dev/` for itself.
- The Nomad and CNI installation copies `deploy/dev/setup.sh`'s pinned versions and checksum
  checks. A test fails when the two pins differ.

`host.sh up` builds a dev release with `tools/release.sh`, copies it, and runs
`install.sh --from` over `ssh -t`, so the admin prompt works. `host.env` changes:
`CARAVANA_DOMAIN_CONSOLE` replaces `CARAVANA_DOMAIN_WEBHOOK`, and it gains
`CARAVANA_ADMIN_EMAIL`. `host.sh admin` becomes `ssh -t sudo caravana admin`. `prove` points
`GATE_API` and `GATE_WEBHOOK` at `https://<console domain>`, so the gate goes through the public
path. The `webhook-host-only-webhook` step runs only when the target has a separate webhook-only
listener (the lab).

## R11. Testing the installer without a public VM

**Decision**: `deploy/dev/install-test.sh` creates a throwaway Lima VM from Ubuntu 24.04 (no lab
provisioning) and runs these checks:

1. `install.sh --from … --acme none --email …` with the password piped. It checks that the console
   answers over HTTPS inside the VM and that sign-in works.
2. The same command again prints an empty `changed:`.
3. A newer dev build upgrades. A whoami deployed before keeps answering during the upgrade.
4. A build that fails health is rolled back.
5. An older build is refused.
6. `--uninstall` leaves whoami answering.

At the end it deletes the VM.

**Rationale**: this tests every installer path except Let's Encrypt and the public ports, which
`host.sh prove` tests on a real VM at the Phase 2 gate.
