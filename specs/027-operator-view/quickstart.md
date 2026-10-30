# Quickstart: validating 027 (the Operator view)

Prerequisites: the lab running and cleared of leftover apps (the VM has 2 GB; the research found it
starved): `deploy/dev/lab.sh up`, then `eval "$(deploy/dev/lab.sh env)"`. Details of what each page
shows are in `contracts/page1-overview.md`, `page2-server.md` and `page3-updates.md`; the API is in
`contracts/api.md`.

## 1. Unit and package tests

```bash
go test ./internal/hostos/ ./internal/store/ ./internal/service/ ./internal/api/ ./internal/ui/
```
Expected: the `hostos` fixtures parse (a pending kernel, a `libc6` reboot, counts with and without the
security line, auto-upgrades on and off, a history with an unattended run and a manual run, a rotated
`.gz`, a `pro` JSON with a kernel); activity merges every source newest first and never shows command
output; the reboot and security rules open and resolve as data-model.md says; the release check honours
the setting and the ETag and notifies once.

## 2. Page 1, Overview (US1)

```bash
CARAVANA_LAB=1 go test ./cmd/caravana -run 'TestConsoleLab/console-ops-overview' -v
```
By hand at `http://127.0.0.1:7070/`: the toggle reaches `/ops`; Settings keeps the Operator sidebar;
the stats line, the server panel (spec line, meters, chips by app), the apps table across two places,
needs-you as on the home, Live activity with a deploy, a restart and a failed schedule run (a new deploy
appears without a reload), capacity. Restart Caravana: the activity is still there.

## 3. Page 2, the server (US2)

```bash
CARAVANA_LAB=1 go test ./cmd/caravana -run 'TestConsoleLab/console-ops-server' -v
```
By hand: `/ops/servers` redirects to the lab node's page; no action buttons; the numbers match
Monitoring's host section; Running here with a starting and a failed copy first; the facts show the lab's
real Nomad, Docker, Traefik and Caravana versions, kernel and "2 cores · … MHz in total" (arm); Recent
lists the lab's latest unattended-upgrades run.

## 4. Page 3, Updates (US3)

```bash
CARAVANA_LAB=1 go test ./cmd/caravana -run 'TestConsoleLab/console-ops-updates' -v
CARAVANA_LAB=1 go test ./cmd/caravana -run 'TestLab/os-updates' -v
```
The console step uses a fake host root (`SetHostRoot`) with a pending kernel and 14/9 counts and a fake
release server. Expected: the ■ banner "This server needs a reboot: kernel 6.8.0-149 is installed, but
6.8.0-147 is still running.", the needs-you item, How to apply's three steps with the reboot last, the
release row with its notes, the Updates nav mark ■; turning the release check off makes no request to the
fake server; "Check package names" twice within 10 minutes is refused. The gate step checks the reboot
item's `problem.opened` reaches a test receiver within one sampling period (the lab's 10 s gap) and
`problem.resolved` after the fake file is removed.

Optional, recorded if done: on the lab VM, `sudo apt install linux-image-<newer>` without rebooting, and
Updates shows the real case.

## 5. On an installed host

```bash
deploy/dev/install-test.sh
```
Expected after the install: `GET /v1/updates` answers `os.readable: true`, `os.distro.id: ubuntu`;
`GET /v1/host` has `node.kernel`. This proves the unit's sandbox lets Caravana read the files.

## 6. Checks before done

The repository's CI list (`CLAUDE.md`), `tools/ui-css.sh check`, `tools/ui-vendor.sh check`, and
`go test -race -timeout 30m ./...`.
