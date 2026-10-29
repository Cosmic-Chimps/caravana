#!/usr/bin/env bash
# Tests the installer (deploy/install/install.sh) end to end on a throwaway Lima VM: a fresh
# Ubuntu 24.04, nothing else. It installs, reruns, upgrades with an app answering throughout,
# rolls back a release that doesn't come up, refuses an older one, and uninstalls with the app
# still answering. Then it deletes the VM. About 10 minutes.
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

cleanup() {
  if [ "${KEEP:-0}" = 1 ]; then
    echo "kept the VM: limactl shell $NAME"
  else
    limactl delete --force "$NAME" >/dev/null 2>&1 || true
  fi
}

# release VERSION: build a release into $WORK/VERSION.
release() { "$ROOT/tools/release.sh" build "$1" "$WORK/$1" >/dev/null; }

# broken VERSION: a release whose Caravana exits at once, checksums made to match.
broken() {
  local v="$1" arch dir
  release "$v"
  for arch in amd64 arm64; do
    dir="$(mktemp -d)"
    tar -xzf "$WORK/$v/caravana_${v}_linux_${arch}.tar.gz" -C "$dir"
    printf '#!/bin/sh\necho "this release is broken on purpose" >&2\nexit 1\n' >"$dir/caravana_${v}_linux_${arch}/caravana"
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
app_answers() { vm_curl -o /dev/null -w '%{http_code}' "https://$APP/" | grep -q '^200$'; }

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

main() {
  command -v limactl >/dev/null || die "Lima isn't installed"
  trap cleanup EXIT
  rm -rf "$WORK" && mkdir -p "$WORK"
  local base v1 v2 bad old out
  base="$(date +%s)"
  old="0.0.0-dev.$((base - 1))" v1="0.0.0-dev.$base" v2="0.0.0-dev.$((base + 1))" bad="0.0.0-dev.$((base + 2))"
  say "building releases $old, $v1, $v2 and a broken $bad"
  release "$old"
  release "$v1"
  release "$v2"
  broken "$bad"

  say "a fresh Ubuntu 24.04 VM"
  limactl delete --force "$NAME" >/dev/null 2>&1 || true
  limactl start --name "$NAME" --tty=false --cpus 2 --memory 2 --containerd none template:ubuntu-24.04 >/dev/null

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
  for path in /etc/caravana /var/lib/caravana-host /opt/caravana /etc/nomad.d; do
    vm test ! -e "$path" || die "a refused install changed the host: $path exists"
  done

  say "install $v1"
  out="$(install "$v1" --email "$EMAIL" --domain "$DOMAIN" --acme none)" || { echo "$out"; die "install"; }
  grep -q "Create the admin: sudo caravana admin init --email $EMAIL" <<<"$out" ||
    die "without a terminal the installer should print the admin command:\n$out"
  vm_curl "https://$DOMAIN/v1/health" | grep -q "\"version\":\"$v1\"" || die "the console doesn't answer over HTTPS with $v1"

  say "the admin, and signing in through Traefik"
  printf '%s\n' "$PASSWORD" | vm sudo caravana admin init --email "$EMAIL" >/dev/null
  out="$(vm_curl -D - -o /dev/null --data-urlencode "email=$EMAIL" --data-urlencode "password=$PASSWORD" "https://$DOMAIN/login")"
  grep -qi '^location: /apps' <<<"$out" || die "sign-in didn't redirect to /apps:\n$out"
  grep -qi '^set-cookie: __Host-caravana_session=.*Secure' <<<"$out" ||
    die "behind Traefik the session cookie should be __Host- and Secure:\n$out"

  say "services may burst into spare memory"
  vm sudo caravana-nomad operator scheduler get-config | grep -Eq '^Memory Oversubscription +=+ +true' ||
    die "the installer didn't turn on memory oversubscription"

  say "the certificate settings, the edge address and Team only's file provider (feature 023)"
  check_domains_host
  say "the Docker root, node:read and Traefik's request numbers (feature 025)"
  check_monitoring_host
  say "a rerun changes nothing"
  out="$(install "$v1")"
  grep -q '^changed: $' <<<"$out" || die "a rerun with nothing new changed: $(grep '^changed:' <<<"$out")"
  ! grep -q 'Traefik is restarted once' <<<"$out" || die "a rerun said Traefik is restarted:\n$out"
  check_domains_host
  check_monitoring_host

  say "a Traefik job from before feature 025 is restarted once for its request numbers, and says so"
  vm sudo bash -c "grep -v 'metrics.prometheus' /opt/caravana/$v1/traefik.nomad.hcl >/tmp/traefik-old.hcl &&
    caravana-nomad job run -detach -var console_domain=$DOMAIN -var acme=none -var acme_email=$EMAIL /tmp/traefik-old.hcl >/dev/null" ||
    die "couldn't put back a Traefik job without the metrics arguments"
  ! vm sudo caravana-nomad job inspect traefik | grep -q -- 'metrics.prometheus' || die "the old Traefik job still has the metrics arguments"
  out="$(install "$v1")"
  grep -q 'Traefik is restarted once for its request numbers (a few seconds without HTTP)' <<<"$out" ||
    die "the rerun over an older Traefik job should say it is restarted for its request numbers:\n$out"
  check_monitoring_host

  say "deploy whoami"
  TOKEN="$(vm sudo caravana admin create-token --name install-test </dev/null)"
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
  vm_curl "https://$DOMAIN/v1/health" | grep -q "\"version\":\"$v2\"" || die "$v2 isn't the one answering"
  api -fsS -o /dev/null "https://$DOMAIN/v1/tokens" || die "the API token didn't survive the upgrade"

  say "a broken $bad is rolled back to $v2"
  set +e
  out="$(install "$bad" 2>&1)"
  local code=$?
  set -e
  [ "$code" = 1 ] || die "a failed upgrade should exit 1, got $code:\n$out"
  grep -q "$v2 runs again" <<<"$out" || die "the failed upgrade should say $v2 runs again:\n$out"
  vm_curl "https://$DOMAIN/v1/health" | grep -q "\"version\":\"$v2\"" || die "$v2 isn't answering after the rollback"
  api -fsS -o /dev/null "https://$DOMAIN/v1/tokens" || die "the data didn't come back with $v2"

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
  app_answers || die "whoami stopped answering after the uninstall"
  [ "$(vm_curl -o /dev/null -w '%{http_code}' "https://$DOMAIN/v1/health")" = 502 ] ||
    die "the console domain should answer 502 with Caravana gone"

  echo "INSTALL TEST PASSED"
}

main "$@"
