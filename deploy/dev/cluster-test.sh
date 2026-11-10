#!/usr/bin/env bash
# Tests feature 031 (Add a server) on three throwaway Lima VMs, cv-cluster-1 to 3: Ubuntu 24.04,
# 2 CPUs, 2 GB, on Lima's user-v2 network, where the VMs reach each other. It follows
# specs/031-add-a-server/tasks.md, "Quickstart", step by step:
#
#    1  an existing host converts (installed from the commit before the feature, then this tree's
#       installer, with an app answering throughout)
#    2  a refused join changes nothing
#    3  server 2 joins, with the console's own command; a used token is refused
#    4  server 3 joins and, within 4 minutes, all three are Nomad servers
#    5  copies spread over the servers, and services reach each other by name across them
#    6  a service's volumes land on one server
#    7  losing server 3, and getting it back by itself after a reboot
#    8  losing server 1 (the leader, Caravana and Traefik), and getting it back
#    9  a certificate renewal reloads Nomad without restarting it
#   10  removing a server that is down
#   11  a server leaves
#
# The release is built into .cache/cluster-test/<version> under the repository, which Lima mounts
# read-only at the same path inside the VMs. The console's domain, caravana.test, is a line in each
# VM's /etc/hosts (its Traefik certificate is its own, so joins use --insecure, as the console's
# command says for --acme none), and the apps' domains are reached with curl's --resolve. About 25
# minutes. It ends CLUSTER TEST PASSED, and deletes the VMs.
#
#   deploy/dev/cluster-test.sh          run it (KEEP=1 keeps the VMs and the pre-feature worktree for a look)
#   PRE_MESH_REF=<commit> ...           the commit step 1 installs first (default d13c37c, the commit before 031)
#
# Needs Lima, git and python3 on this machine. A VM that comes back from a stop must keep its address
# (Lima's user-v2 network leases by MAC address), since the console remembers where each server is.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
DOMAIN=caravana.test
EMAIL=admin@example.com
PASSWORD="a cluster-test password"
PRE_MESH_REF="${PRE_MESH_REF:-d13c37c}"
# Under the home folder, which Lima mounts read-only at the same path inside the VMs.
WORK="$ROOT/.cache/cluster-test"
STATE=/var/lib/caravana-host
LOG="$WORK/installer.log" # every installer output: no token may be in it

TOKEN="" MGMT="" PRE="" NEW=""
ADDR=("" "" "" "") # ADDR[1] to ADDR[3]: the VMs' addresses

say() { echo "--- $*"; }
die() {
  printf 'FAIL: %b\n' "$*" >&2
  exit 1
}
# vm N COMMAND...: run a command in VM N.
vm() {
  local n="$1"
  shift
  limactl shell --workdir / "cv-cluster-$n" "$@"
}
# api CURL_ARGS...: a call to the console, from VM 1, with the API token and Traefik's own certificate.
api() { vm 1 curl -ksS --max-time 20 --resolve "$DOMAIN:443:127.0.0.1" -H "Authorization: Bearer $TOKEN" "$@"; }
# jsonq PYTHON_EXPRESSION: the value of an expression over the JSON on standard input (d is the document).
jsonq() { python3 -c 'import json,sys; d=json.load(sys.stdin); print(eval(sys.argv[1]))' "$1"; }
# nomad_on N ARGS...: Nomad's CLI on VM N, with the management token read from VM 1.
nomad_on() {
  local n="$1"
  shift
  vm "$n" sudo env NOMAD_ADDR=http://127.0.0.1:4646 "NOMAD_TOKEN=$MGMT" nomad "$@"
}
# wait_for SECONDS WHAT COMMAND...: run COMMAND every 3 seconds until it succeeds; die after SECONDS.
wait_for() {
  local secs="$1" what="$2" end
  shift 2
  end=$((SECONDS + secs))
  until "$@"; do
    [ "$SECONDS" -lt "$end" ] || die "$what, within $secs s"
    sleep 3
  done
}

cleanup() {
  if [ "${KEEP:-0}" = 1 ]; then
    echo "kept the VMs: limactl shell cv-cluster-1 (and 2, 3); the pre-feature worktree is $WORK/pre"
    return
  fi
  local n
  for n in 1 2 3; do limactl delete --force "cv-cluster-$n" >/dev/null 2>&1 || true; done
  git -C "$ROOT" worktree remove --force "$WORK/pre" >/dev/null 2>&1 || true
}

# start_vm N: create and start VM N on the user-v2 network.
start_vm() {
  limactl start --name "cv-cluster-$1" --tty=false --cpus 2 --memory 2 --containerd none \
    --network lima:user-v2 template:ubuntu-24.04 >/dev/null 2>&1
}

# release VERSION: build this tree's release into $WORK/VERSION.
release() { "$ROOT/tools/release.sh" build "$1" "$WORK/$1" >/dev/null; }

# install_on N VERSION ARGS...: run that release's installer on VM N. Prints its output and keeps it
# in $LOG; leaves the exit code in INSTALL_CODE and the output in INSTALL_OUT.
INSTALL_CODE=0 INSTALL_OUT=""
install_on() {
  local n="$1" v="$2"
  shift 2
  set +e
  INSTALL_OUT="$(vm "$n" sudo bash "$WORK/$v/install.sh" --from "$WORK/$v" "$@" 2>&1)"
  INSTALL_CODE=$?
  set -e
  printf '%s\n' "$INSTALL_OUT" >>"$LOG"
}
# install_ok N VERSION ARGS...: the same, and it must exit 0.
install_ok() {
  install_on "$@"
  [ "$INSTALL_CODE" = 0 ] || die "the installer on VM $1 exited $INSTALL_CODE:\n$INSTALL_OUT"
}

# app_code HOST: the status of https://HOST/ through Traefik on VM 1.
app_code() { vm 1 curl -ksS --max-time 3 -o /dev/null -w '%{http_code}' --resolve "$1:443:127.0.0.1" "https://$1/" 2>/dev/null || true; }
app_answers() { [ "$(app_code "$1")" = 200 ]; }

# Two scripts the VMs run themselves (one SSH call each, instead of one per request).
write_watchers() {
  cat >"$WORK/watch.sh" <<'EOF'
#!/bin/bash
# watch.sh HOST FLAG: call https://HOST/ every half second until FLAG is gone; print the time and status
# of each failed call, then, on the last line, how many failed.
fails=0
while [ -e "$2" ]; do
  code="$(curl -ksS --max-time 2 -o /dev/null -w '%{http_code}' --resolve "$1:443:127.0.0.1" "https://$1/" 2>/dev/null || true)"
  [ "$code" = 200 ] || { fails=$((fails + 1)) && echo "failed at $(date +%T) with ${code:-000}"; }
  sleep 0.5
done
echo "$fails"
EOF
  cat >"$WORK/calls.sh" <<'EOF'
#!/bin/bash
# calls.sh HOST SECONDS: call https://HOST/ every half second for SECONDS; print "<epoch> <status>" per call.
end=$((SECONDS + $2))
while [ "$SECONDS" -lt "$end" ]; do
  code="$(curl -ksS --max-time 2 -o /dev/null -w '%{http_code}' --resolve "$1:443:127.0.0.1" "https://$1/" 2>/dev/null || true)"
  echo "$(date +%s) ${code:-000}"
  sleep 0.5
done
EOF
}

# deploy APP JSON: POST /v1/apps/APP/deployments.
deploy() {
  api -fsS -o /dev/null -X POST -H 'Content-Type: application/json' "https://$DOMAIN/v1/apps/$1/deployments" -d "$2" ||
    die "deploying $1 failed"
}
# delete_app APP: remove an app (best effort).
delete_app() { api -o /dev/null -X DELETE "https://$DOMAIN/v1/apps/$1" || true; }

# --- reads of the console's answers ---------------------------------------------------------------

servers_json() { api -fsS "https://$DOMAIN/v1/servers"; }
# srv NAME FIELD: a field of GET /v1/servers/NAME.
srv() { api -fsS "https://$DOMAIN/v1/servers/$1" | jsonq "d['$2']"; }
# cluster_is FIELD VALUE: a field of the list's "cluster" object equals VALUE (Python's spelling: True, 3).
cluster_is() { [ "$(servers_json | jsonq "d['cluster']['$1']")" = "$2" ]; }
srv_is() { [ "$(srv "$1" "$2" 2>/dev/null)" = "$3" ]; }
copies_json() { api -fsS "https://$DOMAIN/v1/apps/$1/copies"; }
# healthy_copies APP N: the app has N healthy copies.
healthy_copies() { [ "$(copies_json "$1" | jsonq 'len([c for c in d["copies"] if c["status"]=="healthy"])')" = "$2" ]; }
# copy_node APP SERVICE: the node of the service's healthy copy.
copy_node() { copies_json "$1" | jsonq "[c['node'] for c in d['copies'] if c['service']=='$2' and c['status']=='healthy'][0]" 2>/dev/null; }
# nomad_has_leader N: the Nomad on VM N knows a leader.
nomad_has_leader() { [ -n "$(vm "$1" curl -fsS http://127.0.0.1:4646/v1/status/leader 2>/dev/null | tr -d '"')" ]; }
# voters: how many raft voters Nomad lists, asked on VM N.
voters() { nomad_on "$1" operator raft list-peers | awk 'NR > 1 && $(NF - 1) == "true"' | wc -l | tr -d ' '; }
voters_are() { [ "$(voters "$1")" = "$2" ]; }
# leader_address N: the raft leader's address as VM N sees it, or nothing.
leader_address() { nomad_on "$1" operator raft list-peers 2>/dev/null | awk 'NR > 1 && $(NF - 2) == "leader" {print $(NF - 3)}'; }
leader_is_not_server_1() {
  local a
  a="$(leader_address "$1")"
  [ -n "$a" ] && [ "$a" != 10.98.0.1:4647 ]
}
# request METHOD PATH [JSON]: a call to the console that leaves its status in REQ_CODE and its body in REQ_BODY.
REQ_CODE="" REQ_BODY=""
request() {
  local method="$1" path="$2" body="${3:-}" resp
  if [ -n "$body" ]; then
    resp="$(api -X "$method" -H 'Content-Type: application/json' -w '\n%{http_code}' "https://$DOMAIN$path" -d "$body")" || resp=$'\n000'
  else
    resp="$(api -X "$method" -w '\n%{http_code}' "https://$DOMAIN$path")" || resp=$'\n000'
  fi
  REQ_CODE="${resp##*$'\n'}"
  REQ_BODY="${resp%$'\n'*}"
}
# add_server NAME: POST /v1/servers; leaves the join token in JOIN_TOKEN (never printed).
JOIN_TOKEN=""
add_server() {
  request POST /v1/servers "{\"name\":\"$1\"}"
  [ "$REQ_CODE" = 201 ] || die "POST /v1/servers for $1 answered $REQ_CODE: $REQ_BODY"
  JOIN_TOKEN="$(jsonq 'd["token"]' <<<"$REQ_BODY")"
  [[ "$JOIN_TOKEN" =~ ^cvjn_[A-Za-z0-9_-]{43}$ ]] || die "POST /v1/servers didn't answer a join token"
}

main() {
  command -v limactl >/dev/null || die "Lima isn't installed"
  command -v python3 >/dev/null || die "python3 isn't installed (it reads the API's JSON)"
  trap cleanup EXIT
  git -C "$ROOT" worktree remove --force "$WORK/pre" >/dev/null 2>&1 || true
  rm -rf "$WORK" && mkdir -p "$WORK"
  git -C "$ROOT" worktree prune
  : >"$LOG"

  local base n
  base="$(date +%s)"
  PRE="0.0.0-dev.$base" NEW="0.0.0-dev.$((base + 1))"
  say "building the release of $PRE_MESH_REF ($PRE), the one before the feature, and this tree's ($NEW)"
  git -C "$ROOT" worktree add --detach "$WORK/pre" "$PRE_MESH_REF" >/dev/null 2>&1 || die "can't check out $PRE_MESH_REF"
  (cd "$WORK/pre" && ./tools/release.sh build "$PRE" "$WORK/$PRE" >/dev/null) || die "can't build the release of $PRE_MESH_REF"
  release "$NEW"
  write_watchers

  say "three Ubuntu 24.04 VMs on one network"
  # The first VM starts Lima's user-v2 network daemon, which stops when no VM is on the network:
  # starting all three at once on a cold network races for its socket ("connect: no such file"),
  # so VM 1 starts first and the other two follow in parallel.
  local pids=()
  for n in 1 2 3; do limactl delete --force "cv-cluster-$n" >/dev/null 2>&1 || true; done
  start_vm 1 || die "VM 1 didn't start"
  for n in 2 3; do
    start_vm "$n" &
    pids+=($!)
  done
  for n in 0 1; do wait "${pids[$n]}" || die "a VM didn't start"; done
  for n in 1 2 3; do
    ADDR[n]="$(vm "$n" ip -4 -o route get 1.1.1.1 | sed -n 's/.* src \([0-9.]*\).*/\1/p')"
    [ -n "${ADDR[$n]}" ] || die "can't find VM $n's address"
  done
  [ "${ADDR[1]}" != "${ADDR[2]}" ] && [ "${ADDR[2]}" != "${ADDR[3]}" ] || die "the VMs share an address: ${ADDR[1]} ${ADDR[2]} ${ADDR[3]}"
  vm 1 sudo bash -c "echo '127.0.0.1 $DOMAIN' >>/etc/hosts"
  for n in 2 3; do vm "$n" sudo bash -c "echo '${ADDR[1]} $DOMAIN' >>/etc/hosts"; done
  vm 2 ping -c 1 -W 3 "${ADDR[1]}" >/dev/null || die "VM 2 can't reach VM 1 at ${ADDR[1]}: is this Lima's user-v2 network?"

  # ---- 1. an existing host converts --------------------------------------------------------------
  say "1. install the release before the feature on VM 1, make the admin, deploy whoami"
  install_ok 1 "$PRE" --email "$EMAIL" --domain "$DOMAIN" --acme none
  printf '%s\n' "$PASSWORD" | vm 1 sudo caravana admin init --email "$EMAIL" >/dev/null
  TOKEN="$(vm 1 sudo caravana admin create-token --name cluster-test </dev/null)"
  MGMT="$(vm 1 sudo cat "$STATE/bootstrap.token")"
  ! vm 1 sudo grep -q CARAVANA_ROLE "$STATE/install.env" || die "the release before the feature records a role"
  deploy conv '{"source":{"kind":"image"},"services":[{"name":"web","image":"traefik/whoami:latest",
    "ports":[{"name":"http","containerPort":80}],"domains":[{"host":"conv.test","port":"http"}],
    "health":{"path":"/","interval":"5s","timeout":"2s"}}]}'
  wait_for 240 "conv.test should answer" app_answers conv.test
  vm 1 sudo caravana-nomad operator raft list-peers | grep -q '127.0.0.1:4647' || die "the old host should list its raft peer at 127.0.0.1:4647"

  say "1. install this tree's release over it, with whoami called every half second"
  vm 1 touch /tmp/watching
  vm 1 bash "$WORK/watch.sh" conv.test /tmp/watching >"$WORK/conv-fails" </dev/null &
  local watcher=$!
  install_on 1 "$NEW" --address "${ADDR[1]}"
  vm 1 rm -f /tmp/watching
  wait "$watcher" || true
  [ "$INSTALL_CODE" = 0 ] || die "the conversion exited $INSTALL_CODE:\n$INSTALL_OUT"
  grep -q 'Nomad restarts twice, once only' <<<"$INSTALL_OUT" || die "the installer should say Nomad restarts twice, once:\n$INSTALL_OUT"
  # The old release made conv's copy without the installer's veth link file, so systemd may have
  # rewritten its interface's MAC after the network plugin wrote it down. Nomad then replaces the copy
  # at its first restart (research L17): a few seconds without an answer, never more than ten.
  local conv_failed
  conv_failed="$(tail -1 "$WORK/conv-fails")"
  echo "    $conv_failed calls failed during the conversion"
  [ "$conv_failed" -le 20 ] || die "conv.test failed $conv_failed times during the conversion (at most 20, ten seconds, may):\n$(cat "$WORK/conv-fails")"
  # From here on a Nomad restart replaces nothing: the link file is there, and whatever copy runs now
  # passed Nomad's check or was made after it.
  vm 1 test -f /etc/systemd/network/10-caravana-veth.link || die "the installer didn't write the veth link file"
  wait_for 120 "conv.test should answer after the conversion" app_answers conv.test
  local conv_copy
  conv_copy="$(vm 1 sudo caravana-nomad job allocs -t '{{range .}}{{if eq .ClientStatus "running"}}{{.ID}}{{end}}{{end}}' conv)"
  vm 1 sudo systemctl restart nomad
  wait_for 120 "Nomad should have a leader after a plain restart" nomad_has_leader 1
  sleep 10
  [ "$(vm 1 sudo caravana-nomad job allocs -t '{{range .}}{{if eq .ClientStatus "running"}}{{.ID}}{{end}}{{end}}' conv)" = "$conv_copy" ] ||
    die "a plain Nomad restart replaced conv's copy $conv_copy:\n$(vm 1 sudo caravana-nomad job allocs conv)"
  vm 1 sudo caravana-nomad operator raft list-peers | grep -Eq '10\.98\.0\.1:4647 +leader' || die "raft should list 10.98.0.1:4647 after the conversion"
  vm 1 sudo test -f "$STATE/raft-mesh" || die "no raft-mesh marker after the conversion"
  vm 1 sudo test -f "$STATE/nomad.hcl.before-mesh" || die "the old nomad.hcl wasn't kept"
  vm 1 sudo test ! -e /etc/caravana-node/ca-key.pem || die "the CA's key is still on disk"
  vm 1 sudo caravana-nomad var get -item cert caravana/cluster/ca | grep -q 'BEGIN CERTIFICATE' || die "the CA's certificate isn't in the Nomad Variable"
  vm 1 sudo caravana-nomad var get -item key caravana/cluster/ca | grep -q 'BEGIN' || die "the CA's key isn't in the Nomad Variable"
  app_answers conv.test || die "conv.test stopped answering after the conversion"
  wait_for 120 "GET /v1/servers should list the first server as ready" srv_is "$(vm 1 hostname)" state ready
  local out
  out="$(servers_json)"
  [ "$(jsonq 'len(d["servers"])' <<<"$out")" = 1 ] || die "GET /v1/servers should list one server:\n$out"
  [ "$(jsonq 'd["servers"][0]["first"]' <<<"$out")" = True ] || die "the server should be the first:\n$out"
  [ "$(jsonq 'd["servers"][0]["nomadServer"]' <<<"$out")" = True ] || die "the first server should be a Nomad server:\n$out"
  say "1. a rerun changes nothing"
  install_ok 1 "$NEW"
  grep -q '^changed: $' <<<"$INSTALL_OUT" || die "a rerun changed: $(grep '^changed:' <<<"$INSTALL_OUT")"
  delete_app conv

  # ---- 2. a refused join changes nothing ---------------------------------------------------------
  say "2. VM 2: a bad token format, a bad console address and a bad address change nothing"
  local fake path
  fake="cvjn_$(printf 'x%.0s' $(seq 43))"
  install_on 2 "$NEW" --join "cvjn_short" --url "https://$DOMAIN"
  [ "$INSTALL_CODE" = 2 ] || die "a bad token format should exit 2, got $INSTALL_CODE:\n$INSTALL_OUT"
  install_on 2 "$NEW" --join "$fake" --url "http://$DOMAIN"
  [ "$INSTALL_CODE" = 2 ] || die "a bad --url should exit 2, got $INSTALL_CODE:\n$INSTALL_OUT"
  install_on 2 "$NEW" --join "$fake" --url "https://$DOMAIN" --address 127.0.0.1
  [ "$INSTALL_CODE" = 2 ] || die "a bad --address should exit 2, got $INSTALL_CODE:\n$INSTALL_OUT"
  for path in /opt/caravana "$STATE" /etc/nomad.d /etc/caravana-node /usr/local/bin/nomad /usr/local/libexec/caravana-node; do
    vm 2 sudo test ! -e "$path" || die "a refused install changed VM 2: $path exists"
  done
  say "2. VM 2: a made-up token is refused by the console, and nothing of the join is written"
  install_on 2 "$NEW" --join "$fake" --url "https://$DOMAIN" --insecure --address "${ADDR[2]}"
  [ "$INSTALL_CODE" = 1 ] || die "a made-up token should exit 1, got $INSTALL_CODE:\n$INSTALL_OUT"
  grep -q "This join token isn't valid" <<<"$INSTALL_OUT" || die "the console's message should reach the installer's output:\n$INSTALL_OUT"
  vm 2 sudo test ! -e /etc/caravana-node/node.env || die "a refused join wrote node.env"
  vm 2 sudo test ! -e /etc/nomad.d/nomad.hcl || die "a refused join wrote nomad.hcl"
  [ "$(vm 2 sudo ls /etc/caravana-node)" = mesh.key ] || die "a refused join should leave nothing but mesh.key in /etc/caravana-node"

  # ---- 3. join 2 ---------------------------------------------------------------------------------
  say "3. add server two in the console, and join VM 2 with a token"
  add_server two
  local token2="$JOIN_TOKEN"
  install_ok 2 "$NEW" --join "$token2" --url "https://$DOMAIN" --insecure --address "${ADDR[2]}"
  local want
  for want in "Connected to the cluster" "Docker running" "Reaches" "Ready to take work" "This server joined as two."; do
    grep -qF "$want" <<<"$INSTALL_OUT" || die "the installer should print '$want':\n$INSTALL_OUT"
  done
  wait_for 120 "two should be ready" srv_is two state ready
  out="$(servers_json)"
  [ "$(jsonq 'len(d["servers"])' <<<"$out")" = 2 ] || die "GET /v1/servers should list two servers:\n$out"
  [ "$(jsonq 'd["cluster"]["ready"]' <<<"$out")" = 2 ] || die "two servers should be ready:\n$out"
  [ "$(jsonq 'd["cluster"]["nomadServers"]' <<<"$out")" = 1 ] || die "only the first should be a Nomad server yet:\n$out"
  [ "$(jsonq 'd["cluster"]["survivesLoss"]' <<<"$out")" = False ] || die "two servers can't survive a loss:\n$out"
  say "3. the same command again changes nothing, and the used token is refused on VM 3"
  install_ok 2 "$NEW" --join "$token2" --url "https://$DOMAIN" --insecure --address "${ADDR[2]}"
  grep -q '^changed: $' <<<"$INSTALL_OUT" || die "the same join command again changed: $(grep '^changed:' <<<"$INSTALL_OUT")"
  install_on 3 "$NEW" --join "$token2" --url "https://$DOMAIN" --insecure --address "${ADDR[3]}"
  [ "$INSTALL_CODE" = 1 ] || die "a used token should exit 1, got $INSTALL_CODE:\n$INSTALL_OUT"
  grep -q "This join token isn't valid" <<<"$INSTALL_OUT" || die "a used token should be refused with the console's message:\n$INSTALL_OUT"

  # ---- 4. join 3 and the raft --------------------------------------------------------------------
  say "4. add server three and join VM 3; within 4 minutes all three are Nomad servers"
  add_server three
  local token3="$JOIN_TOKEN"
  install_ok 3 "$NEW" --join "$token3" --url "https://$DOMAIN" --insecure --address "${ADDR[3]}"
  grep -q "This server joined as three." <<<"$INSTALL_OUT" || die "VM 3 should join as three:\n$INSTALL_OUT"
  wait_for 240 "three Nomad servers that survive a loss" cluster_is survivesLoss True
  out="$(servers_json)"
  [ "$(jsonq 'd["cluster"]["nomadServers"]' <<<"$out")" = 3 ] || die "three servers should be Nomad servers:\n$out"
  [ "$(jsonq 'len([s for s in d["servers"] if s["nomadServer"]])' <<<"$out")" = 3 ] || die "every server should be marked a Nomad server:\n$out"
  wait_for 60 "raft should have three voters" voters_are 1 3

  # ---- 5. spread and names -----------------------------------------------------------------------
  say "5. a three-copy app spreads over the three servers, and 30 calls all answer"
  deploy spread '{"source":{"kind":"image"},"services":[{"name":"web","image":"traefik/whoami:latest","replicas":3,
    "ports":[{"name":"http","containerPort":80}],"domains":[{"host":"spread.test","port":"http"}],
    "health":{"path":"/","interval":"5s","timeout":"2s"}}]}'
  wait_for 240 "spread should have three healthy copies" healthy_copies spread 3
  [ "$(copies_json spread | jsonq 'len(set(c["node"] for c in d["copies"] if c["status"]=="healthy"))')" = 3 ] ||
    die "spread's copies should be on three different servers:\n$(copies_json spread)"
  local bad=0 calls=0
  while [ "$((calls += 1))" -le 30 ]; do app_answers spread.test || bad=$((bad + 1)); done
  [ "$bad" = 0 ] || die "$bad of 30 calls to spread.test failed"
  say "5. front reaches back by name when they are on different servers"
  deploy pair '{"source":{"kind":"image"},"services":[
    {"name":"front","image":"busybox:1.37","command":"sh","args":["-c","while true; do wget -qO- http://back/; sleep 2; done"]},
    {"name":"back","image":"traefik/whoami:latest","ports":[{"name":"http","containerPort":80}],
     "health":{"path":"/","interval":"5s","timeout":"2s"}}]}'
  wait_for 240 "pair should have two healthy copies" healthy_copies pair 2
  local front back
  front="$(copy_node pair front)"
  back="$(copy_node pair back)"
  if [ "$front" = "$back" ]; then
    say "5. both are on $front: that server is made ineligible, front is stopped and starts elsewhere"
    local node_id front_alloc
    node_id="$(nomad_on 1 node status -t '{{range .}}{{.ID}} {{.Name}}{{"\n"}}{{end}}' | awk -v n="$front" '$2 == n {print $1}')"
    [ -n "$node_id" ] || die "can't find the node ID of $front"
    front_alloc="$(copies_json pair | jsonq "[c['id'] for c in d['copies'] if c['service']=='front' and c['status']=='healthy'][0]")"
    nomad_on 1 node eligibility -disable "$node_id" >/dev/null
    nomad_on 1 alloc stop -detach "$front_alloc" >/dev/null
    wait_for 120 "front should run on another server" front_left "$front"
    nomad_on 1 node eligibility -enable "$node_id" >/dev/null
    front="$(copy_node pair front)"
  fi
  [ "$front" != "$back" ] || die "front and back should be on different servers (both on $front)"
  wait_for 120 "front's log should have whoami's answer from back" front_got_answer
  delete_app pair

  # ---- 6. volumes --------------------------------------------------------------------------------
  say "6. a service with two volumes has them on one server, and runs there"
  deploy vol '{"source":{"kind":"image"},"services":[{"name":"main","image":"busybox:1.37","command":"sleep","args":["3600"],
    "volumes":[{"name":"a","path":"/a"},{"name":"b","path":"/b"}]}]}'
  wait_for 240 "vol should have a healthy copy" healthy_copies vol 1
  out="$(api -fsS "https://$DOMAIN/v1/apps/vol/volumes")"
  [ "$(jsonq 'len(set(v["server"] for v in d if v["mounted"]))' <<<"$out")" = 1 ] || die "vol's volumes should name one server:\n$out"
  [ "$(jsonq 'd[0]["server"]' <<<"$out")" = "$(copy_node vol main)" ] || die "vol's copy should run on its volumes' server:\n$out"

  # ---- 7. lose server 3 --------------------------------------------------------------------------
  say "7. stop VM 3 for good while spread is called every half second for 120 seconds"
  # When the lost server is Nomad's leader, the new leader waits failover_heartbeat_ttl (1 minute, set
  # by the agent; Nomad's default is 5) before it calls the node down, so its copy stays registered
  # for about a minute longer (research L14). The bounds follow which case this run is.
  # A server that isn't the leader is called down after its heartbeat's time plus heartbeat_grace (30
  # seconds, research L18): up to about 50 seconds in which a third of the calls time out.
  local stop_at was_leader=0 max_failed=25 quiet_after=75
  if [ "$(vm 1 curl -fsS http://127.0.0.1:4646/v1/status/leader | tr -d '"')" = "10.98.0.3:4647" ]; then
    was_leader=1 max_failed=50 quiet_after=100
    echo "    (server 3 is Nomad's leader in this run)"
  fi
  vm 1 bash "$WORK/calls.sh" spread.test 120 >"$WORK/spread-calls" </dev/null &
  watcher=$!
  sleep 3
  stop_at="$(vm 1 date +%s)"
  limactl stop -f cv-cluster-3 >/dev/null 2>&1
  wait "$watcher" || true
  local failed late
  failed="$(awk '$2 != 200' "$WORK/spread-calls" | wc -l | tr -d ' ')"
  late="$(awk -v s="$stop_at" -v q="$quiet_after" '$2 != 200 && $1 > s + q' "$WORK/spread-calls" | wc -l | tr -d ' ')"
  echo "    $failed calls failed (leader lost: $was_leader)"
  [ "$failed" -le "$max_failed" ] || die "$failed calls failed after server 3 was lost (at most $max_failed may)"
  [ "$late" = 0 ] || die "$late calls failed more than $quiet_after seconds after server 3 was lost"
  healthy_copies spread 3 || die "spread should have three healthy copies again:\n$(copies_json spread)"
  [ "$(copies_json spread | jsonq 'len(set(c["node"] for c in d["copies"] if c["status"]=="healthy"))')" = 2 ] ||
    die "spread's three copies should be on two servers:\n$(copies_json spread)"
  wait_for 120 "three should be down" srv_is three state down
  out="$(api -fsS "https://$DOMAIN/v1/attention")"
  grep -q '"kind":"server_down"' <<<"$out" || die "GET /v1/attention should have a server_down item:\n$out"
  request DELETE /v1/servers/two
  { [ "$REQ_CODE" = 409 ] && grep -q '"code":"server_running"' <<<"$REQ_BODY"; } || die "removing a running server should be 409 server_running, got $REQ_CODE: $REQ_BODY"
  say "7. start VM 3 again: its interfaces and Nomad come back by themselves, and it is ready and a voter"
  limactl start cv-cluster-3 >/dev/null 2>&1
  wait_for 180 "three should be ready again" srv_is three state ready
  wait_for 60 "raft should have three voters again" voters_are 1 3
  vm 3 systemctl is-active --quiet nomad || die "Nomad isn't running on VM 3 after its reboot"
  vm 3 ip -4 -o addr show dev caravana-mesh | grep -q ' 10.98.0.3/32 ' || die "caravana-mesh isn't back on VM 3"

  # ---- 8. lose server 1 --------------------------------------------------------------------------
  say "8. stop VM 1 (the leader, Caravana and Traefik); VM 2 elects another leader"
  local node1 survivors
  node1="$(vm 1 hostname)"
  # Copies of spread that aren't on server 1 must keep running.
  survivors="$(copies_json spread | jsonq "' '.join(c['id'] for c in d['copies'] if c['status']=='healthy' and c['node']!='$node1')")"
  [ -n "$survivors" ] || die "spread has no copy off server 1, so the test can't tell"
  limactl stop -f cv-cluster-1 >/dev/null 2>&1
  wait_for 60 "a leader that isn't server 1 should be elected" leader_is_not_server_1 2
  local id
  for id in $survivors; do
    [ "$(nomad_on 2 alloc status -t '{{.ClientStatus}}' "$id")" = running ] || die "the copy $id of spread stopped running when server 1 was lost"
  done
  say "8. start VM 1 again: the console answers through Traefik, spread answers, three voters"
  limactl start cv-cluster-1 >/dev/null 2>&1
  wait_for 180 "GET /v1/health should answer through Traefik" console_health
  wait_for 120 "spread should answer" app_answers spread.test
  wait_for 120 "raft should have three voters" voters_are 2 3
  wait_for 120 "all three servers should be ready" cluster_is ready 3

  # ---- 9. a renewal ------------------------------------------------------------------------------
  say "9. VM 2 renews its certificate now: the serial changes, Nomad's process doesn't"
  local serial pid expires
  serial="$(vm 2 sudo openssl x509 -in /etc/nomad.d/tls/agent.pem -noout -serial)"
  pid="$(vm 2 systemctl show nomad -p MainPID --value)"
  expires="$(srv two certificateExpiresAt)"
  vm 2 sudo /usr/local/libexec/caravana-node node sync --renew
  [ "$(vm 2 sudo openssl x509 -in /etc/nomad.d/tls/agent.pem -noout -serial)" != "$serial" ] || die "the certificate's serial didn't change"
  [ "$(vm 2 systemctl show nomad -p MainPID --value)" = "$pid" ] || die "Nomad's process changed: a renewal should reload it, not restart it"
  wait_for 120 "two should still be ready" srv_is two state ready
  wait_for 120 "two's certificateExpiresAt should change" certificate_changed two "$expires"

  # ---- 10. remove --------------------------------------------------------------------------------
  say "10. stop VM 3 for good, wait for down, remove it: the others drop its peer"
  limactl stop -f cv-cluster-3 >/dev/null 2>&1
  wait_for 180 "three should be down" srv_is three state down
  request DELETE /v1/servers/three
  [ "$REQ_CODE" = 204 ] || die "removing a down server should be 204, got $REQ_CODE: $REQ_BODY"
  wait_for 90 "VMs 1 and 2 should have one peer each" one_peer_each
  request POST /v1/servers '{"name":"four"}'
  [ "$REQ_CODE" = 201 ] || die "a fourth name can be added after a removal: got $REQ_CODE: $REQ_BODY"
  request DELETE /v1/servers/four
  [ "$REQ_CODE" = 204 ] || die "a waiting join is removed without conditions: got $REQ_CODE: $REQ_BODY"

  # ---- 11. leave ---------------------------------------------------------------------------------
  say "11. VM 2 leaves: it goes out of raft and VM 1 keeps a leader; then the console removes it"
  install_ok 2 "$NEW" --leave
  vm 2 systemctl is-active --quiet nomad && die "Nomad still runs on VM 2 after --leave"
  vm 2 ip link show caravana-mesh >/dev/null 2>&1 && die "caravana-mesh is still on VM 2 after --leave"
  vm 2 sudo test ! -e /etc/caravana-node || die "/etc/caravana-node is still on VM 2 after --leave"
  # Two Nomad servers were left (server 3 is gone): VM 2 left raft before it stopped, so VM 1 is one
  # voter of one and still has a leader. Stopping VM 2's Nomad without that left VM 1 with no quorum
  # (research L16).
  wait_for 60 "server 1 should be the only raft voter after server 2 left, with a leader" voters_are 1 1
  [ -n "$(vm 1 curl -fsS http://127.0.0.1:4646/v1/status/leader | tr -d '"')" ] || die "Nomad has no leader after server 2 left"
  # Until Nomad notices the node is gone it says running, and the console refuses.
  wait_for 180 "two can be removed once Nomad says it is down" remove_two

  ! grep -q -e "$token2" -e "$token3" "$LOG" || die "an installer printed a join token"
  ! grep -q 'cvnd_' "$LOG" || die "an installer printed a node token"
  echo "CLUSTER TEST PASSED"
}

# --- predicates for wait_for that read more than one thing ------------------------------------------

# front_left NODE: front has a healthy copy, and not on NODE.
front_left() {
  local n
  n="$(copy_node pair front)" || return 1
  [ -n "$n" ] && [ "$n" != "$1" ]
}
# front_got_answer: front's log has whoami's answer.
front_got_answer() {
  local id
  id="$(copies_json pair | jsonq "[c['id'] for c in d['copies'] if c['service']=='front' and c['status']=='healthy'][0]" 2>/dev/null)" || return 1
  api -fsS "https://$DOMAIN/v1/apps/pair/logs?copy=$id&lines=50" | grep -q 'Hostname:'
}
# console_health: the console answers its health check through Traefik.
console_health() { vm 1 curl -ksS --max-time 5 --resolve "$DOMAIN:443:127.0.0.1" "https://$DOMAIN/v1/health" 2>/dev/null | grep -q '"status":"ok"'; }
# certificate_changed NAME OLD: the server's certificate end isn't OLD any more.
certificate_changed() { [ "$(srv "$1" certificateExpiresAt 2>/dev/null)" != "$2" ]; }
# one_peer_each: VMs 1 and 2 each have one WireGuard peer.
one_peer_each() {
  [ "$(vm 1 sudo wg show caravana-mesh peers | wc -l | tr -d ' ')" = 1 ] && [ "$(vm 2 sudo wg show caravana-mesh peers | wc -l | tr -d ' ')" = 1 ]
}
# remove_two: the console removes server two (204).
remove_two() {
  request DELETE /v1/servers/two
  [ "$REQ_CODE" = 204 ]
}

main "$@"
