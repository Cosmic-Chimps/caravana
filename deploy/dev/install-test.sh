#!/usr/bin/env bash
# Tests the installer (deploy/install/install.sh) end to end on a throwaway Lima VM: a fresh
# Ubuntu 24.04, nothing else. It installs, reruns, upgrades with an app answering throughout,
# rolls back a release that doesn't come up, updates from the API through the root updater (a
# crafted request runs nothing, a release is installed, a broken one is rolled back), refuses an
# older one, and uninstalls with the app still answering. Feature 031's first-server steps are in it:
# the private network (caravana0, the WireGuard mesh), Nomad's RPC on the mesh address with TLS, the
# cluster's CA and gossip key in Nomad Variables with no key left on disk, the server registered with
# Caravana, the refusals of both roles, and what --uninstall leaves. Then it deletes the VM. About 20
# minutes. Adding a server and converting an older host need more machines: deploy/dev/cluster-test.sh.
#
#   deploy/dev/install-test.sh          run it (KEEP=1 keeps the VM for a look afterwards)
#
# Let's Encrypt and the public ports need a real VM: deploy/vm/host.sh prove covers those.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
NAME=caravana-install-test
DOMAIN=caravana.test
APP=whoami.test
EMAIL=admin@example.com
PASSWORD="an install-test password"
# Under the home folder, which Lima mounts read-only at the same path inside the VM.
WORK="$ROOT/.cache/install-test"

say() { echo "--- $*"; }
die() {
  printf 'FAIL: %b\n' "$*" >&2
  exit 1
}
vm() { limactl shell --workdir / "$NAME" "$@"; }
# https GET inside the VM, with DOMAIN or APP resolved to the VM itself (Traefik's own certificate).
vm_curl() { vm curl -ksS --max-time 5 --resolve "$DOMAIN:443:127.0.0.1" --resolve "$APP:443:127.0.0.1" "$@"; }

# check_domains_host: Caravana's env file has the three lines feature 023 added, and Traefik's job
# reads a directory with the Team only template, whose file exists in the running allocation.
check_domains_host() {
  local env spec alloc
  env="$(vm sudo cat /etc/caravana/caravana.env)"
  grep -q '^CARAVANA_ACME=none$' <<<"$env" || die "the env file has no CARAVANA_ACME=none:\n$(grep -v NOMAD_TOKEN <<<"$env")"
  grep -q "^CARAVANA_ACME_EMAIL=$EMAIL$" <<<"$env" || die "the env file has no CARAVANA_ACME_EMAIL"
  grep -q '^CARAVANA_EDGE_ADDRESS=127.0.0.1:443$' <<<"$env" || die "the env file has no CARAVANA_EDGE_ADDRESS"
  spec="$(vm sudo caravana-nomad job inspect traefik)"
  grep -q 'providers.file.directory=/local/dynamic' <<<"$spec" || die "Traefik doesn't read /local/dynamic"
  grep -q 'local/dynamic/team.yml' <<<"$spec" || die "Traefik's job has no team.yml template"
  alloc="$(vm sudo caravana-nomad job allocs -t '{{range .}}{{if eq .ClientStatus "running"}}{{.ID}} {{end}}{{end}}' traefik | awk '{print $1}')"
  [ -n "$alloc" ] || die "Traefik has no running allocation"
  vm sudo caravana-nomad alloc fs "$alloc" traefik/local/dynamic/team.yml >/dev/null || die "team.yml isn't in Traefik's allocation"
}

# check_monitoring_host: what feature 025 needs from the host. Caravana's env file names Docker's
# root as Docker says it, Caravana's Nomad policy lets it read nodes, and Traefik's job has the
# two metrics arguments and serves its numbers on the loopback entrypoint.
check_monitoring_host() {
  local env spec policy root code
  env="$(vm sudo cat /etc/caravana/caravana.env)"
  root="$(vm sudo docker info --format '{{.DockerRootDir}}')"
  [ -n "$root" ] || die "docker info didn't say where Docker's root is"
  grep -qx "CARAVANA_DOCKER_ROOT=$root" <<<"$env" ||
    die "the env file should say CARAVANA_DOCKER_ROOT=$root:\n$(grep -v NOMAD_TOKEN <<<"$env")"
  policy="$(vm sudo caravana-nomad acl policy info caravana | tr -d '\n')"
  grep -Eq 'node \{ *policy = "read" *\}' <<<"$policy" || die "Caravana's Nomad policy has no node { policy = \"read\" }"
  spec="$(vm sudo caravana-nomad job inspect traefik)"
  grep -q -- '--metrics.prometheus=true' <<<"$spec" || die "Traefik's job doesn't turn on its metrics"
  grep -q -- '--metrics.prometheus.buckets=0.005,0.01,0.025,0.05,0.1,0.25,0.5,1,2.5,5,10' <<<"$spec" ||
    die "Traefik's job doesn't have the finer buckets"
  local end=$((SECONDS + 60))
  until code="$(vm curl -sS --max-time 5 -o /dev/null -w '%{http_code}' http://127.0.0.1:8081/metrics 2>/dev/null)" && [ "$code" = 200 ]; do
    [ "$SECONDS" -lt "$end" ] || die "http://127.0.0.1:8081/metrics answers $code, not 200"
    sleep 2
  done
}

# check_updates_host MODE: what feature 027's Updates page needs from the host, through the API: the unit's
# sandbox (ProtectSystem=strict, ProtectHome, PrivateTmp) lets Caravana read the host's files, so
# GET /v1/updates says os.readable with Ubuntu, and GET /v1/host has the node's kernel (the sampler's
# first tick comes within a minute). With MODE "packages" it also asks for the package names, which
# runs `pro security-status` inside that sandbox: the check is accepted (202) and ends with no error. A
# VM without a pro command (409) skips that part with a note.
check_updates_host() {
  local mode="$1" up host code end
  up="$(api -fsS "https://$DOMAIN/v1/updates")" || die "GET /v1/updates failed"
  grep -q '"os":{"readable":true,"distro":{"id":"ubuntu",' <<<"$up" ||
    die "GET /v1/updates should say os.readable with Ubuntu (does the unit's sandbox stop the reads?):\n$up"
  end=$((SECONDS + 150))
  until host="$(api -fsS "https://$DOMAIN/v1/host")" && grep -q '"kernel":"[^"][^"]*"' <<<"$host"; do
    [ "$SECONDS" -lt "$end" ] || die "GET /v1/host has no node.kernel after 150 s:\n$host"
    sleep 3
  done
  [ "$mode" = packages ] || return 0
  code="$(api -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' "https://$DOMAIN/v1/updates/check" -d '{"what":"packages"}')"
  if [ "$code" = 409 ]; then
    echo "    (this VM has no pro command: Check package names is skipped)"
    return 0
  fi
  [ "$code" = 202 ] || die "POST /v1/updates/check for the package names answered $code, not 202"
  end=$((SECONDS + 150))
  until up="$(api -fsS "https://$DOMAIN/v1/updates")" && grep -q '"packagesCheckedAt":"' <<<"$up"; do
    [ "$SECONDS" -lt "$end" ] || die "the package check didn't end within 150 s:\n$up"
    sleep 3
  done
  grep -q '"packagesError":""' <<<"$up" ||
    die "the package check failed inside the unit's sandbox (pro needs HOME, which is the data directory, and may write elsewhere):\n$up"
}

# jsonq PYTHON_EXPRESSION: the value of an expression over the JSON on standard input (d is the document).
jsonq() { python3 -c 'import json,sys; d=json.load(sys.stdin); print(eval(sys.argv[1]))' "$1"; }

# check_mesh_host: what feature 031 makes of the first server (contracts/node.md): the two private
# interfaces with their addresses and routes, Nomad's config written by the agent (root only) and
# its RPC on the mesh address with TLS, the cluster's secrets in Nomad Variables and no CA key left on
# disk, the units, the firewall's two rules, and node.env, which never holds anything but the console's
# loopback address and the node token. The rpc-tls.py file is written by main.
check_mesh_host() {
  local route out
  grep -q ' 10.99.0.1/32 ' < <(vm ip -4 -o addr show dev caravana0) || die "caravana0 should have 10.99.0.1/32"
  vm grep -q '^MACAddressPolicy=none$' /etc/systemd/network/10-caravana-veth.link || die "the veth link file isn't there (research L17)"
  grep -q ' 10.98.0.1/32 ' < <(vm ip -4 -o addr show dev caravana-mesh) || die "caravana-mesh should have 10.98.0.1/32"
  for route in 10.97.0.0/16 10.98.0.0/24 10.99.0.0/24; do
    grep -q 'dev caravana-mesh' < <(vm ip -4 route show "$route") || die "no route $route through caravana-mesh"
  done
  [ "$(vm sudo wg show caravana-mesh listen-port)" = 51820 ] || die "the mesh should listen on 51820"
  [ "$(vm sudo stat -c '%a %U' /etc/nomad.d/nomad.hcl)" = "600 root" ] || die "nomad.hcl should be root 0600 (it holds the gossip key)"
  [ "$(vm sudo stat -c '%a' /etc/nomad.d/tls/agent-key.pem)" = 600 ] || die "agent-key.pem should be 0600"
  vm sudo grep -q 'Written by' /etc/nomad.d/nomad.hcl || die "nomad.hcl wasn't written by the agent"
  vm sudo grep -q 'rpc  = "10.98.0.1"' /etc/nomad.d/nomad.hcl || die "nomad.hcl should put Nomad's RPC on 10.98.0.1"
  [ "$(vm sudo stat -c '%a' /etc/caravana-node)" = 700 ] || die "/etc/caravana-node should be 0700"
  out="$(vm sudo caravana-nomad operator raft list-peers)"
  grep -Eq '10\.98\.0\.1:4647 +leader' <<<"$out" || die "raft should list this server at 10.98.0.1:4647 as the leader:\n$out"
  ! grep -Eq '^(0\.0\.0\.0|\*|\[::\]):(4647|4648)$' < <(vm sudo ss -ltnH | awk '{print $4}') || die "Nomad's RPC or serf listens on every address"
  out="$(vm sudo python3 "$WORK/rpc-tls.py")" || die "Nomad's RPC on 10.98.0.1:4647 doesn't answer a TLS handshake against the cluster's CA:\n$out"
  grep -q 'TLSv1.3 server.global.nomad' <<<"$out" || die "unexpected RPC handshake: $out"
  vm sudo test ! -e /etc/caravana-node/ca-key.pem || die "the CA's key is still on disk"
  vm sudo test -f /var/lib/caravana-host/raft-mesh || die "no raft-mesh marker"
  vm sudo test ! -e /var/lib/caravana-host/nomad.hcl.before-mesh || die "a fresh host has no config to keep"
  grep -q 'BEGIN' < <(vm sudo caravana-nomad var get -item key caravana/cluster/ca | head -1) || die "the CA's key isn't in the Nomad Variable caravana/cluster/ca"
  grep -q 'BEGIN CERTIFICATE' < <(vm sudo caravana-nomad var get -item cert caravana/cluster/ca | head -1) || die "the CA's certificate isn't in caravana/cluster/ca"
  [ "$(vm sudo caravana-nomad var get -item key caravana/cluster/gossip | wc -c)" -ge 24 ] || die "the gossip key isn't in caravana/cluster/gossip"
  vm systemctl is-active --quiet caravana-mesh || die "caravana-mesh.service isn't active"
  vm systemctl is-enabled --quiet caravana-mesh || die "caravana-mesh.service isn't enabled"
  vm systemctl is-active --quiet caravana-node.timer || die "caravana-node.timer isn't active"
  vm systemctl is-enabled --quiet caravana-node.timer || die "caravana-node.timer isn't enabled"
  grep -q '^51820/udp .*ALLOW' < <(vm sudo ufw status) || die "ufw should allow 51820/udp"
  grep -q 'on caravana-mesh .*ALLOW' < <(vm sudo ufw status) || die "ufw should allow everything in on caravana-mesh"
  [ "$(vm sudo stat -c '%a %U' /etc/caravana-node/node.env)" = "600 root" ] || die "node.env should be root 0600"
  vm sudo grep -qx 'CARAVANA_URL=http://127.0.0.1:7070' /etc/caravana-node/node.env || die "node.env should name the console on loopback"
  vm sudo grep -Eqx 'CARAVANA_NODE_TOKEN=cvnd_[A-Za-z0-9_-]{43}' /etc/caravana-node/node.env || die "node.env should hold a node token"
}

# check_servers_api: the first server is registered with Caravana and listed as the one ready server.
check_servers_api() {
  local out
  out="$(api -fsS "https://$DOMAIN/v1/servers")" || die "GET /v1/servers failed"
  [ "$(jsonq 'len(d["servers"])' <<<"$out")" = 1 ] || die "GET /v1/servers should list one server:\n$out"
  [ "$(jsonq 'd["servers"][0]["state"]' <<<"$out")" = ready ] || die "the first server should be ready:\n$out"
  [ "$(jsonq 'd["servers"][0]["first"]' <<<"$out")" = True ] || die "the server should be the first:\n$out"
  [ "$(jsonq 'd["servers"][0]["nomadServer"]' <<<"$out")" = True ] || die "the first server should be a Nomad server:\n$out"
  [ "$(jsonq 'd["servers"][0]["index"]' <<<"$out")" = 1 ] || die "the first server's index should be 1:\n$out"
  [ "$(jsonq 'd["servers"][0]["privateAddress"]' <<<"$out")" = 10.98.0.1 ] || die "the first server's private address should be 10.98.0.1:\n$out"
  [ "$(jsonq 'd["cluster"]["survivesLoss"]' <<<"$out")" = False ] || die "one server can't survive a loss:\n$out"
  ! grep -q 'cvnd_' <<<"$out" || die "the servers list holds a node token"
}

# refuses LABEL EXPECTED ARGS...: the installer exits 2 and says EXPECTED.
refuses() {
  local label="$1" expected="$2" out code
  shift 2
  set +e
  out="$(vm sudo bash "$WORK/$V1/install.sh" --from "$WORK/$V1" "$@" 2>&1)"
  code=$?
  set -e
  [ "$code" = 2 ] || die "$label: install.sh should exit 2, got $code:\n$out"
  grep -qF -- "$expected" <<<"$out" || die "$label: the refusal should say '$expected':\n$out"
}

cleanup() {
  if [ "${KEEP:-0}" = 1 ]; then
    echo "kept the VM: limactl shell $NAME"
  else
    limactl delete --force "$NAME" >/dev/null 2>&1 || true
  fi
}

# release VERSION: build a release into $WORK/VERSION.
release() { "$ROOT/tools/release.sh" build "$1" "$WORK/$1" >/dev/null; }

# broken VERSION DONOR: a release that doesn't come up: its installer and its agent work (feature 031
# runs the release's own binary as the agent before Caravana starts, so a binary that can't run at all
# would stop the install before anything was replaced), but its Caravana is the binary of DONOR, which
# answers its health check with DONOR's version, never VERSION's. Checksums made to match.
broken() {
  local v="$1" donor="$2" arch dir
  release "$v"
  for arch in amd64 arm64; do
    dir="$(mktemp -d)"
    tar -xzf "$WORK/$v/caravana_${v}_linux_${arch}.tar.gz" -C "$dir"
    mkdir "$dir/donor"
    tar -xzf "$WORK/$donor/caravana_${donor}_linux_${arch}.tar.gz" -C "$dir/donor"
    cp "$dir/donor/caravana_${donor}_linux_${arch}/caravana" "$dir/caravana_${v}_linux_${arch}/caravana"
    COPYFILE_DISABLE=1 tar --no-xattrs --no-mac-metadata -C "$dir" -czf "$WORK/$v/caravana_${v}_linux_${arch}.tar.gz" "caravana_${v}_linux_${arch}"
    rm -rf "$dir"
  done
  (cd "$WORK/$v" && shasum -a 256 install.sh "caravana_${v}_linux_amd64.tar.gz" "caravana_${v}_linux_arm64.tar.gz" >SHA256SUMS)
}

# install VERSION [ARGS]: run that release's installer in the VM; prints its output.
install() {
  local v="$1"
  shift
  vm sudo bash "$WORK/$v/install.sh" --from "$WORK/$v" "$@"
}

# app_answers: whoami answers through Traefik.
app_answers() { grep -q '^200$' < <(vm_curl -o /dev/null -w '%{http_code}' "https://$APP/"); }

# watch_app FILE: count whoami's failed answers, one try every half second, until FILE is gone.
watch_app() {
  local flag="$1" fails=0
  while [ -e "$flag" ]; do
    app_answers || fails=$((fails + 1))
    sleep 0.5
  done
  echo "$fails"
}

api() { vm_curl -H "Authorization: Bearer $TOKEN" "$@"; }

# ---- feature 030: updating from the API, through the root updater -----------------------------

# A real release version for each of the three releases the update steps use. Caravana calls a
# 0.0.0-dev.* build a development build and refuses to update it, so these are plain 0.0.N.
SERVE_PORT=8099
RELEASE_URL="http://127.0.0.1:$SERVE_PORT/repos/x/releases/latest"

# serve_release VERSION: lay a release's files out as GitHub serves them: download/v<version>/<file>.
serve_release() {
  local v="$1" dir="$WORK/served/download/v$1"
  mkdir -p "$dir"
  cp "$WORK/$v/install.sh" "$WORK/$v/SHA256SUMS" "$WORK/$v/caravana_${v}_linux_amd64.tar.gz" \
    "$WORK/$v/caravana_${v}_linux_arm64.tar.gz" "$dir/"
}

# serve_latest VERSION: make the fake "latest release" answer name VERSION.
serve_latest() {
  mkdir -p "$WORK/served/repos/x/releases"
  printf '{"tag_name":"v%s","html_url":"https://github.com/Cosmic-Chimps/caravana/releases/tag/v%s","body":"A release for the installer test.","published_at":"2026-11-06T12:00:00Z","draft":false,"prerelease":false}\n' \
    "$1" "$1" >"$WORK/served/repos/x/releases/latest"
}

# start_server: serve $WORK/served inside the VM on loopback, so the updater and Caravana's release
# check have a "GitHub" to ask. Extensionless files (the fake latest release) are sent as JSON.
start_server() {
  cat >"$WORK/serve.py" <<'PY'
import functools
import http.server
import sys

handler = http.server.SimpleHTTPRequestHandler
handler.extensions_map = {**handler.extensions_map, "": "application/json"}
http.server.ThreadingHTTPServer(
    ("127.0.0.1", int(sys.argv[2])), functools.partial(handler, directory=sys.argv[1])
).serve_forever()
PY
  vm sh -c "setsid python3 '$WORK/serve.py' '$WORK/served' $SERVE_PORT >/dev/null 2>&1 </dev/null &"
  local end=$((SECONDS + 15))
  until vm curl -fsS --max-time 2 -o /dev/null "$RELEASE_URL"; do
    [ "$SECONDS" -lt "$end" ] || die "the test release server doesn't answer on $RELEASE_URL"
    sleep 1
  done
}

# point_at_server: Caravana asks the test server for the latest release (a systemd drop-in, so the
# unit is the one the installer wrote), and the updater downloads from it (its root-owned test file).
point_at_server() {
  printf '[Service]\nEnvironment=CARAVANA_RELEASE_URL=%s\n' "$RELEASE_URL" >"$WORK/test.conf"
  printf 'CARAVANA_UPDATE_RELEASES=http://127.0.0.1:%s/download\n' "$SERVE_PORT" >"$WORK/update.env"
  vm sudo install -D -m 0644 -o root -g root "$WORK/test.conf" /etc/systemd/system/caravana.service.d/test.conf
  vm sudo install -D -m 0644 -o root -g root "$WORK/update.env" /etc/caravana/update.env
  vm sudo systemctl daemon-reload
}

# run_field FIELD: one string field of "run" in GET /v1/updates, or nothing (Caravana may be down).
run_field() {
  api -fsS "https://$DOMAIN/v1/updates" 2>/dev/null | grep -o '"run":{[^}]*}' | grep -o "\"$1\":\"[^\"]*\"" | head -1 | cut -d'"' -f4 || true
}

# wait_run STATE SECONDS: wait until the run in GET /v1/updates is in STATE.
wait_run() {
  local want="$1" end=$((SECONDS + $2)) got=""
  until [ "$got" = "$want" ]; do
    [ "$SECONDS" -lt "$end" ] || die "the update run should be $want within $2 s; last seen: ${got:-nothing}\n$(api -sS "https://$DOMAIN/v1/updates" 2>&1 | head -c 1500)"
    sleep 5
    got="$(run_field state)"
  done
}

# wait_health VERSION SECONDS: wait until Caravana's health check answers VERSION.
wait_health() {
  local end=$((SECONDS + $2))
  until grep -q "\"version\":\"$1\"" < <(vm_curl "https://$DOMAIN/v1/health" 2>/dev/null); do
    [ "$SECONDS" -lt "$end" ] || die "Caravana should answer $1 within $2 s"
    sleep 3
  done
}

# wait_status ID STATE SECONDS: wait until the updater's status file is for ID and in STATE. It
# leaves the file in UPDATER_STATUS.
UPDATER_STATUS=""
wait_status() {
  local end=$((SECONDS + $3))
  until UPDATER_STATUS="$(vm sudo cat /var/lib/caravana-update/status 2>/dev/null)" &&
    grep -qx "ID=$1" <<<"$UPDATER_STATUS" && grep -qx "STATE=$2" <<<"$UPDATER_STATUS"; do
    [ "$SECONDS" -lt "$end" ] || die "the updater's status should be $2 for $1 within $3 s:\n$UPDATER_STATUS"
    sleep 3
  done
}

# post_run VERSION: POST /v1/updates/runs. Leaves the status in POST_CODE and the answer in POST_BODY.
POST_CODE="" POST_BODY=""
post_run() {
  local resp
  resp="$(api -X POST -H 'Content-Type: application/json' -w '\n%{http_code}' "https://$DOMAIN/v1/updates/runs" -d "{\"version\":\"$1\"}")" || resp=$'\n000'
  POST_CODE="${resp##*$'\n'}"
  POST_BODY="${resp%$'\n'*}"
}

# request_update VERSION: check for releases, then ask for the update until Caravana accepts it. The
# check runs on its own; until it has seen the release the answer is 409 update_not_offered.
request_update() {
  local end=$((SECONDS + 150))
  while :; do
    api -o /dev/null -X POST -H 'Content-Type: application/json' "https://$DOMAIN/v1/updates/check" -d '{"what":"release"}' || true
    post_run "$1"
    [ "$POST_CODE" = 202 ] && return 0
    if [ "$POST_CODE" != 409 ] || ! grep -q '"code":"update_not_offered"' <<<"$POST_BODY" || [ "$SECONDS" -ge "$end" ]; then
      die "POST /v1/updates/runs for $1 answered $POST_CODE: $POST_BODY"
    fi
    sleep 10
  done
}

# check_console_update U1 U2 UBAD: the console's update path, end to end, on the VM that runs the
# earlier steps. U1 and U2 are good releases, UBAD one whose Caravana exits at once.
check_console_update() {
  local u1="$1" u2="$2" ubad="$3" out id i
  local flag="$WORK/watching-update" fails

  say "update: the test release server, and Caravana and the updater pointed at it"
  serve_release "$u1"
  serve_release "$u2"
  serve_latest "$u2"
  start_server
  point_at_server

  say "update: install $u1 by hand; it brings the updater and its timer"
  out="$(install "$u1")" || { echo "$out"; die "install $u1"; }
  wait_health "$u1" 60
  [ "$(vm sudo cat /var/lib/caravana-update/updater)" = 1 ] || die "/var/lib/caravana-update/updater should say 1"
  vm sudo test -x /usr/local/libexec/caravana-update || die "the updater script isn't installed and executable"
  [ "$(vm stat -c '%U %a' /usr/local/libexec/caravana-update)" = "root 755" ] || die "the updater script should be root 0755"
  [ "$(vm stat -c '%U %a' /var/lib/caravana-update)" = "root 755" ] || die "/var/lib/caravana-update should be root 0755"
  [ "$(vm sudo stat -c '%U %a' /var/lib/caravana-update/logs)" = "root 700" ] || die "/var/lib/caravana-update/logs should be root 0700"
  vm systemctl is-active --quiet caravana-update.timer || die "caravana-update.timer isn't active"
  vm systemctl is-enabled --quiet caravana-update.timer || die "caravana-update.timer isn't enabled"
  grep -q '"installed":true' < <(api -fsS "https://$DOMAIN/v1/updates") || die "GET /v1/updates should say the updater is installed"

  say "update: a crafted request runs nothing"
  id="$(printf '%032x' "$(date +%s)")"
  printf 'ID=%s\nVERSION=%s\n' "$id" '0.0.1;touch /tmp/pwned' >"$WORK/request"
  vm sudo install -m 0600 -o caravana -g caravana "$WORK/request" /var/lib/caravana/update-request
  wait_status "$id" refused 120
  grep -q '^REASON=The request.s version isn.t a release version\.$' <<<"$UPDATER_STATUS" ||
    die "a crafted version should be refused as not a release version:\n$UPDATER_STATUS"
  grep -q '^VERSION=$' <<<"$UPDATER_STATUS" || die "the status shouldn't repeat the request's version:\n$UPDATER_STATUS"
  [ "$(vm readlink /opt/caravana/current)" = "$u1" ] || die "a refused request changed what runs"
  vm test ! -e /tmp/pwned || die "the crafted version ran something"
  vm sudo rm -f /var/lib/caravana/update-request

  # A symlink to a file root can read: no new status, and the journal says it isn't a plain file.
  local before
  before="$(vm sudo cat /var/lib/caravana-update/status)"
  vm sudo ln -s /etc/shadow /var/lib/caravana/update-request
  local end=$((SECONDS + 120)) journal=""
  until journal="$(vm sudo journalctl -u caravana-update -o cat --since=-3min)" && grep -q "isn't a plain file" <<<"$journal"; do
    [ "$SECONDS" -lt "$end" ] || die "the updater should log that a symlinked request isn't a plain file:\n$journal"
    sleep 5
  done
  [ "$(vm sudo cat /var/lib/caravana-update/status)" = "$before" ] || die "a symlinked request changed the status"
  ! grep -q '^root:' <<<"$journal" || die "the updater printed a line of /etc/shadow"
  vm sudo rm -f /var/lib/caravana/update-request

  say "update: from the API to $u2, with whoami answering throughout"
  touch "$flag"
  watch_app "$flag" >"$WORK/fails" &
  request_update "$u2"
  id="$(grep -o '"id":"[0-9a-f]\{32\}"' <<<"$POST_BODY" | head -1 | cut -d'"' -f4)"
  [ -n "$id" ] || die "the 202 answer has no run id: $POST_BODY"
  grep -q '"state":"requested"' <<<"$POST_BODY" || die "the new run should be requested: $POST_BODY"
  post_run "$u2"
  { [ "$POST_CODE" = 409 ] && grep -q '"code":"update_running"' <<<"$POST_BODY"; } ||
    die "a second request at once should be 409 update_running, got $POST_CODE: $POST_BODY"
  wait_health "$u2" 240
  wait_run updated 120
  rm -f "$flag"
  wait
  fails="$(cat "$WORK/fails")"
  [ "$fails" = 0 ] || die "whoami failed $fails times during the update"
  [ "$(vm readlink /opt/caravana/current)" = "$u2" ] || die "current should point at $u2"
  i=0
  until vm sudo test ! -e /var/lib/caravana/update-request; do
    i=$((i + 1))
    [ "$i" -lt 10 ] || die "Caravana didn't remove the update request once the run ended"
    sleep 3
  done
  out="$(vm sudo cat /var/lib/caravana-update/status)"
  if ! { grep -qx "ID=$id" <<<"$out" && grep -qx "STATE=updated" <<<"$out" && grep -qx "FROM=$u1" <<<"$out" && grep -qx "VERSION=$u2" <<<"$out"; }; then
    die "the updater's status should say $u1 updated to $u2 for run $id:\n$out"
  fi
  vm sudo test -s "/var/lib/caravana-update/logs/$id.log" || die "the installer's log of run $id is missing"
  [ "$(vm sudo stat -c '%a' "/var/lib/caravana-update/logs/$id.log")" = 600 ] || die "the log should be 0600"
  api -fsS -o /dev/null "https://$DOMAIN/v1/tokens" || die "the API token didn't survive the update"

  say "update: a broken $ubad is rolled back to $u2, and the owner is told"
  serve_release "$ubad"
  serve_latest "$ubad"
  # Check now runs at most once per 10 minutes, which the first update's check may still hold.
  # Caravana's own first check comes a minute after it starts, whatever that limit says, so a
  # restart makes it see $ubad within request_update's wait.
  vm sudo systemctl restart caravana
  wait_health "$u2" 60
  request_update "$ubad"
  id="$(grep -o '"id":"[0-9a-f]\{32\}"' <<<"$POST_BODY" | head -1 | cut -d'"' -f4)"
  wait_status "$id" rolled_back 360
  grep -q "^REASON=.*$u2 runs again" <<<"$UPDATER_STATUS" || die "the rolled back status should say $u2 runs again:\n$UPDATER_STATUS"
  wait_health "$u2" 120
  wait_run rolled_back 120
  [ -n "$(run_field reason)" ] || die "the rolled back run should have a reason"
  api -fsS -o /dev/null "https://$DOMAIN/v1/tokens" || die "the data didn't come back with $u2"
  out="$(api -fsS "https://$DOMAIN/v1/attention")"
  grep -q '"kind":"caravana_update_failed"' <<<"$out" || die "GET /v1/attention should have a caravana_update_failed item:\n$out"
  vm sudo test ! -e /var/lib/caravana/update-request || die "the rolled back run's request is still there"

  say "update: put the test wiring away"
  vm sudo rm -rf /etc/systemd/system/caravana.service.d /etc/caravana/update.env
  vm sudo systemctl daemon-reload
  vm sudo pkill -f '[s]erve\.py' || true
}

V1="" ADDR=""
main() {
  command -v limactl >/dev/null || die "Lima isn't installed"
  trap cleanup EXIT
  rm -rf "$WORK" && mkdir -p "$WORK"
  local base v1 v2 bad old out u1 u2 ubad
  base="$(date +%s)"
  old="0.0.0-dev.$((base - 1))" v1="0.0.0-dev.$base" v2="0.0.0-dev.$((base + 1))" bad="0.0.0-dev.$((base + 2))"
  V1="$v1"
  # Release versions for the update steps (0.0.N is newer than every 0.0.0-dev.*).
  u1="0.0.$base" u2="0.0.$((base + 1))" ubad="0.0.$((base + 2))"
  say "building releases $old, $v1, $v2 and a broken $bad"
  release "$old"
  release "$v1"
  release "$v2"
  broken "$bad" "$old"
  say "building the releases for the update steps: $u1, $u2 and a broken $ubad"
  release "$u1"
  release "$u2"
  broken "$ubad" "$u1"

  say "a fresh Ubuntu 24.04 VM"
  limactl delete --force "$NAME" >/dev/null 2>&1 || true
  limactl start --name "$NAME" --tty=false --cpus 2 --memory 2 --containerd none template:ubuntu-24.04 >/dev/null
  # The address other servers would reach this one at (--address), so the run needs no lookup of a public IP.
  ADDR="$(vm ip -4 -o route get 1.1.1.1 | sed -n 's/.* src \([0-9.]*\).*/\1/p')"
  [ -n "$ADDR" ] || die "can't find the VM's address"
  # What check_mesh_host asks of Nomad's RPC: a TLS handshake with the cluster's CA and no plain RPC.
  cat >"$WORK/rpc-tls.py" <<'PY'
import socket, ssl, sys

ctx = ssl.create_default_context(cafile="/etc/nomad.d/tls/ca.pem")
ctx.load_cert_chain("/etc/nomad.d/tls/agent.pem", "/etc/nomad.d/tls/agent-key.pem")
s = socket.create_connection(("10.98.0.1", 4647), 5)
s.sendall(b"\x04")  # Nomad's RPC type byte for "TLS follows"
t = ctx.wrap_socket(s, server_hostname="server.global.nomad")
print(t.version(), t.getpeercert()["subjectAltName"][0][1])
t.close()

plain = socket.create_connection(("10.98.0.1", 4647), 5)
plain.settimeout(5)
plain.sendall(b"\x01" + b"\x82\xa6Method\xabStatus.Ping\xa3Seq\x01")  # Nomad's plain RPC type, then a request
try:
    if plain.recv(100):
        sys.exit("plain RPC was answered")
except ConnectionError:
    pass
PY

  say "the installer refuses a version it wasn't built with"
  set +e
  vm sudo bash "$ROOT/deploy/install/install.sh" --email "$EMAIL" >/dev/null 2>&1
  [ $? = 2 ] || die "an install.sh without a version should exit 2"
  set -e

  say "the installer refuses a domain or email that isn't plain, and changes nothing"
  local bad_arg path
  # shellcheck disable=SC2016 # the $( ) is the point: it must reach the installer as text
  for bad_arg in "--domain=bad domain.test" '--domain=a$(id).test' "--domain=-a.test" "--email=a b@example.com" '--email=a$(id)@example.com' "--acme-email=a;b@example.com"; do
    set +e
    out="$(vm sudo bash "$WORK/$v1/install.sh" --from "$WORK/$v1" --email "$EMAIL" --domain "$DOMAIN" --acme none "${bad_arg%%=*}" "${bad_arg#*=}" 2>&1)"
    [ $? = 2 ] || { set -e; die "install.sh should refuse (exit 2) with ${bad_arg%%=*} '${bad_arg#*=}':\n$out"; }
    set -e
    grep -q -- "${bad_arg%%=*} is not" <<<"$out" || die "the refusal should name ${bad_arg%%=*}:\n$out"
  done
  say "the installer refuses the options of the other role and values that aren't plain, and changes nothing (feature 031)"
  local good_token
  good_token="cvjn_$(printf 'x%.0s' $(seq 43))"
  refuses "--join without --url" "--url is required" --join "$good_token"
  refuses "--url without --join, on a first server" "--url and --insecure are for --join" --email "$EMAIL" --url https://caravana.test
  refuses "--insecure on a first server" "--url and --insecure are for --join" --email "$EMAIL" --insecure
  refuses "a short join token" "--join is not a join token" --join cvjn_short --url https://caravana.test
  refuses "a join token with a bad character" "--join is not a join token" --join "cvjn_$(printf 'x%.0s' $(seq 42))!" --url https://caravana.test
  refuses "an http console" "--url must be https://" --join "$good_token" --url http://caravana.test
  refuses "a console with a path" "--url must be https://" --join "$good_token" --url https://caravana.test/console
  # shellcheck disable=SC2016 # the $( ) is the point: it must reach the installer as text
  refuses "a console that isn't a host name" "--url must be https://" --join "$good_token" --url 'https://a$(id).test'
  refuses "an address that isn't one" "--address is not" --join "$good_token" --url https://caravana.test --address 999.1.1.1
  refuses "a loopback address" "--address is not" --email "$EMAIL" --address 127.0.0.1
  refuses "--first with --join" "--first and --join" --first --join "$good_token" --url https://caravana.test
  refuses "--first options with --join" "are for the first server" --join "$good_token" --url https://caravana.test --email "$EMAIL"
  refuses "--leave with --uninstall" "not both" --leave --uninstall
  refuses "a stray join token" "looks like a join token" "$good_token"
  out="$(vm sudo bash "$WORK/$v1/install.sh" --from "$WORK/$v1" --leave)" || die "--leave on a host that never installed anything should exit 0"
  grep -q "nothing to leave" <<<"$out" || die "--leave on a fresh host should say there is nothing to leave:\n$out"
  ! grep -q "$good_token" <<<"$(vm sudo bash "$WORK/$v1/install.sh" --from "$WORK/$v1" --join "$good_token" --url https://caravana.test --email "$EMAIL" 2>&1 || true)" ||
    die "a refusal printed the join token"
  for path in /etc/caravana /var/lib/caravana-host /opt/caravana /etc/nomad.d /etc/caravana-node /usr/local/libexec/caravana-node; do
    vm test ! -e "$path" || die "a refused install changed the host: $path exists"
  done

  say "install $v1"
  out="$(install "$v1" --email "$EMAIL" --domain "$DOMAIN" --acme none --address "$ADDR")" || { echo "$out"; die "install"; }
  ! grep -q 'cvnd_' <<<"$out" || die "the installer printed a node token"
  grep -q "Create the admin: sudo caravana admin init --email $EMAIL" <<<"$out" ||
    die "without a terminal the installer should print the admin command:\n$out"
  grep -q "\"version\":\"$v1\"" < <(vm_curl "https://$DOMAIN/v1/health") || die "the console doesn't answer over HTTPS with $v1"

  say "the admin, and signing in through Traefik"
  printf '%s\n' "$PASSWORD" | vm sudo caravana admin init --email "$EMAIL" >/dev/null
  out="$(vm_curl -D - -o /dev/null --data-urlencode "email=$EMAIL" --data-urlencode "password=$PASSWORD" "https://$DOMAIN/login")"
  grep -qi '^location: /apps' <<<"$out" || die "sign-in didn't redirect to /apps:\n$out"
  grep -qi '^set-cookie: __Host-caravana_session=.*Secure' <<<"$out" ||
    die "behind Traefik the session cookie should be __Host- and Secure:\n$out"

  say "services may burst into spare memory"
  grep -Eq '^Memory Oversubscription +=+ +true' < <(vm sudo caravana-nomad operator scheduler get-config) ||
    die "the installer didn't turn on memory oversubscription"

  say "the certificate settings, the edge address and Team only's file provider (feature 023)"
  check_domains_host
  say "the Docker root, node:read and Traefik's request numbers (feature 025)"
  check_monitoring_host
  say "the private network, Nomad's certificates, the cluster's variables and the registered server (feature 031)"
  check_mesh_host
  say "a rerun changes nothing"
  out="$(install "$v1")"
  grep -q '^changed: $' <<<"$out" || die "a rerun with nothing new changed: $(grep '^changed:' <<<"$out")"
  ! grep -q 'Traefik is restarted once' <<<"$out" || die "a rerun said Traefik is restarted:\n$out"
  ! grep -q 'Nomad restarts twice' <<<"$out" || die "a rerun said Nomad restarts:\n$out"
  out="$(install "$v1" --first)"
  grep -q '^changed: $' <<<"$out" || die "a rerun with --first changed: $(grep '^changed:' <<<"$out")"
  check_domains_host
  check_monitoring_host
  check_mesh_host
  say "the other role's options are refused on a first server, and --uninstall's partner --leave too"
  refuses "--join on a first server" "this is the first server of its cluster" --join "$good_token" --url https://caravana.test
  refuses "--leave on a first server" "this is the first server" --leave

  say "a Traefik job from before feature 025 is restarted once for its request numbers, and says so"
  vm sudo bash -c "grep -v 'metrics.prometheus' /opt/caravana/$v1/traefik.nomad.hcl >/tmp/traefik-old.hcl &&
    caravana-nomad job run -detach -var console_domain=$DOMAIN -var acme=none -var acme_email=$EMAIL /tmp/traefik-old.hcl >/dev/null" ||
    die "couldn't put back a Traefik job without the metrics arguments"
  ! grep -q -- 'metrics.prometheus' < <(vm sudo caravana-nomad job inspect traefik) || die "the old Traefik job still has the metrics arguments"
  out="$(install "$v1")"
  grep -q 'Traefik is restarted once for its request numbers (a few seconds without HTTP)' <<<"$out" ||
    die "the rerun over an older Traefik job should say it is restarted for its request numbers:\n$out"
  check_monitoring_host

  say "deploy whoami"
  TOKEN="$(vm sudo caravana admin create-token --name install-test </dev/null)"
  check_servers_api

  say "Updates reads the host's files, and pro runs, inside the unit's sandbox (feature 027)"
  check_updates_host packages
  api -o /dev/null -X POST -H 'Content-Type: application/json' "https://$DOMAIN/v1/apps/whoami/deployments" -d '{
    "source": {"kind": "image"},
    "services": [{"name": "web", "image": "traefik/whoami:latest",
      "ports": [{"name": "http", "containerPort": 80}],
      "domains": [{"host": "'"$APP"'", "port": "http"}],
      "health": {"path": "/", "interval": "5s", "timeout": "2s"}}]}'
  local end=$((SECONDS + 180))
  until app_answers; do
    [ "$SECONDS" -lt "$end" ] || die "whoami didn't answer within 3 minutes"
    sleep 3
  done

  say "upgrade to $v2 with whoami answering throughout"
  local flag="$WORK/watching" fails
  touch "$flag"
  watch_app "$flag" >"$WORK/fails" &
  install "$v2" >/dev/null || die "the upgrade to $v2 failed"
  rm -f "$flag"
  wait
  fails="$(cat "$WORK/fails")"
  [ "$fails" = 0 ] || die "whoami failed $fails times during the upgrade"
  grep -q "\"version\":\"$v2\"" < <(vm_curl "https://$DOMAIN/v1/health") || die "$v2 isn't the one answering"
  api -fsS -o /dev/null "https://$DOMAIN/v1/tokens" || die "the API token didn't survive the upgrade"
  check_updates_host reads
  check_servers_api
  check_mesh_host

  say "a broken $bad is rolled back to $v2"
  set +e
  out="$(install "$bad" 2>&1)"
  local code=$?
  set -e
  [ "$code" = 1 ] || die "a failed upgrade should exit 1, got $code:\n$out"
  grep -q "$v2 runs again" <<<"$out" || die "the failed upgrade should say $v2 runs again:\n$out"
  grep -q "\"version\":\"$v2\"" < <(vm_curl "https://$DOMAIN/v1/health") || die "$v2 isn't answering after the rollback"
  api -fsS -o /dev/null "https://$DOMAIN/v1/tokens" || die "the data didn't come back with $v2"

  check_console_update "$u1" "$u2" "$ubad"

  say "an older $old is refused"
  set +e
  install "$old" >/dev/null 2>&1
  code=$?
  set -e
  [ "$code" = 2 ] || die "an older version should exit 2, got $code"

  say "uninstall: Caravana goes, whoami keeps answering throughout"
  touch "$flag"
  watch_app "$flag" >"$WORK/fails" &
  vm sudo bash "$WORK/$v2/install.sh" --uninstall >/dev/null
  rm -f "$flag"
  wait
  fails="$(cat "$WORK/fails")"
  [ "$fails" = 0 ] || die "whoami failed $fails times during the uninstall"
  ! vm systemctl is-active --quiet caravana || die "caravana still runs"
  say "the uninstall removed the updater, its timer and its directory"
  local gone
  for gone in /usr/local/libexec/caravana-update /etc/systemd/system/caravana-update.service \
    /etc/systemd/system/caravana-update.timer /var/lib/caravana-update; do
    vm sudo test ! -e "$gone" || die "the uninstall left $gone"
  done
  ! vm systemctl is-active --quiet caravana-update.timer || die "caravana-update.timer still runs"
  ! vm systemctl is-enabled --quiet caravana-update.timer || die "caravana-update.timer is still enabled"
  say "the uninstall stopped the agent's timer and removed node.env, and left the mesh, Nomad and the agent"
  vm sudo test ! -e /etc/caravana-node/node.env || die "the uninstall left node.env"
  ! vm systemctl is-active --quiet caravana-node.timer || die "caravana-node.timer still runs"
  ! vm systemctl is-enabled --quiet caravana-node.timer || die "caravana-node.timer is still enabled"
  grep -q ' 10.98.0.1/32 ' < <(vm ip -4 -o addr show dev caravana-mesh) || die "the uninstall took the mesh away"
  vm systemctl is-active --quiet nomad || die "the uninstall stopped Nomad"
  vm sudo test -x /usr/local/libexec/caravana-node || die "the uninstall removed the agent, which Nomad's unit needs"
  app_answers || die "whoami stopped answering after the uninstall"
  [ "$(vm_curl -o /dev/null -w '%{http_code}' "https://$DOMAIN/v1/health")" = 502 ] ||
    die "the console domain should answer 502 with Caravana gone"

  echo "INSTALL TEST PASSED"
}

main "$@"
