#!/usr/bin/env bash
# A public Caravana host on a VM you own, driven from your Mac over SSH.
#
#   deploy/vm/host.sh up       set up the host, or update it (idempotent); prints what changed
#   deploy/vm/host.sh env      print NOMAD_ADDR/NOMAD_TOKEN/CARAVANA_URL for the tunnel
#   deploy/vm/host.sh tunnel   forward the host's private ports to this Mac (Ctrl-C to close)
#   deploy/vm/host.sh prove    prove the Phase 1 gate on the host (about 12 minutes)
#   deploy/vm/host.sh ssh      a shell on the host, or: host.sh ssh <command>
#
# Settings come from deploy/vm/host.env (copy host.env.example). See deploy/vm/README.md.
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../.." && pwd)"
STATE=/var/lib/caravana-host

# Local ports for the tunnel; different from the lab's, so both can run at once.
LOCAL_API=17070
LOCAL_NOMAD=14646
LOCAL_DASHBOARD=18081
FORWARDS=(
  -L "$LOCAL_API:127.0.0.1:7070"
  -L "$LOCAL_NOMAD:127.0.0.1:4646"
  -L "$LOCAL_DASHBOARD:127.0.0.1:8081"
)

load_env() {
  local file="$DIR/host.env"
  [ -f "$file" ] || { echo "missing $file: copy deploy/vm/host.env.example and fill it in" >&2; exit 1; }
  set -a
  # shellcheck source=/dev/null
  . "$file"
  set +a
  local name
  for name in CARAVANA_HOST CARAVANA_DOMAIN_APP CARAVANA_DOMAIN_WEBHOOK CARAVANA_ACME_EMAIL CARAVANA_ACME_STAGING; do
    [ -n "${!name:-}" ] || { echo "$name is empty in $file" >&2; exit 1; }
  done
}

# One SSH connection, reused by every command below.
SSH_OPTS=(-o ControlMaster=auto -o "ControlPath=$HOME/.ssh/caravana-host-%C" -o ControlPersist=120)
# ssh joins its arguments into one string the remote shell parses again, so quote each one.
# shellcheck disable=SC2029 # expanded (and quoted) on this side on purpose
remote() { ssh "${SSH_OPTS[@]}" "$CARAVANA_HOST" "$(printf '%q ' "$@")"; }

# The host's IP: the SSH target's host part, resolved.
host_ip() {
  local host="${CARAVANA_HOST#*@}"
  python3 -c 'import socket, sys; print(socket.gethostbyname(sys.argv[1]))' "$host"
}

check_dns() {
  local ip name got
  ip="$(host_ip)"
  for name in "$CARAVANA_DOMAIN_APP" "$CARAVANA_DOMAIN_WEBHOOK"; do
    got="$(python3 -c 'import socket, sys
try: print(socket.gethostbyname(sys.argv[1]))
except OSError: print("")' "$name")"
    if [ "$got" != "$ip" ]; then
      echo "warning: $name resolves to '${got:-nothing}', not the host's $ip; certificates can't be issued until it does" >&2
    fi
  done
}

management_token() { remote sudo cat "$STATE/bootstrap.token"; }

cmd_up() {
  load_env
  check_dns

  echo "==> building Caravana for the host"
  local arch tmp rtmp
  arch="$(remote dpkg --print-architecture)"
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN
  (cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o "$tmp/caravana" ./cmd/caravana)

  echo "==> copying files to $CARAVANA_HOST"
  rtmp="$(remote mktemp -d)"
  cp "$DIR/setup.sh" "$DIR/nomad.hcl.tpl" "$DIR/traefik.nomad.hcl" "$DIR/caravana-policy.hcl" \
    "$DIR/caravana.service" "$ROOT/deploy/dev/traefik-policy.hcl" "$tmp/"
  cp "$ROOT/deploy/dev/setup.sh" "$tmp/dev-setup.sh"
  scp -q "${SSH_OPTS[@]}" "$tmp"/* "$CARAVANA_HOST:$rtmp/"

  echo "==> setting up the host"
  local status=0
  remote sudo env \
    CARAVANA_DOMAIN_WEBHOOK="$CARAVANA_DOMAIN_WEBHOOK" \
    CARAVANA_ACME_EMAIL="$CARAVANA_ACME_EMAIL" \
    CARAVANA_ACME_STAGING="$CARAVANA_ACME_STAGING" \
    CARAVANA_BINARY="$rtmp/caravana" \
    bash "$rtmp/setup.sh" || status=$?
  remote rm -rf "$rtmp"
  [ "$status" = 0 ] || { echo "setup failed on the host (exit $status)" >&2; exit "$status"; }

  cat <<EOF

Host is up.
  App URL (once deployed): https://$CARAVANA_DOMAIN_APP
  Webhook:                 https://$CARAVANA_DOMAIN_WEBHOOK/v1/webhooks/<app>?token=…
  Private access:          deploy/vm/host.sh tunnel, then eval "\$(deploy/vm/host.sh env)"
EOF
}

cmd_env() {
  load_env
  printf 'export NOMAD_ADDR=http://127.0.0.1:%s\n' "$LOCAL_NOMAD"
  printf 'export NOMAD_TOKEN=%s\n' "$(management_token)"
  printf 'export CARAVANA_URL=http://127.0.0.1:%s\n' "$LOCAL_API"
}

cmd_tunnel() {
  load_env
  cat <<EOF
Tunnel to $CARAVANA_HOST (Ctrl-C to close):
  Caravana API:       http://127.0.0.1:$LOCAL_API
  Nomad UI and API:   http://127.0.0.1:$LOCAL_NOMAD/ui   (token: deploy/vm/host.sh env)
  Traefik dashboard:  http://127.0.0.1:$LOCAL_DASHBOARD/dashboard/
EOF
  ssh -N -o ExitOnForwardFailure=yes "${FORWARDS[@]}" "$CARAVANA_HOST"
}

cmd_prove() {
  load_env
  local ip token
  # Global, not local: the EXIT trap runs after this function has returned, when its locals
  # are gone, and an empty socket path would leave the tunnel running.
  PROVE_SOCK="$(mktemp -u "${TMPDIR:-/tmp}/caravana-prove.XXXXXX")"
  ssh -f -N -M -S "$PROVE_SOCK" -o ExitOnForwardFailure=yes "${FORWARDS[@]}" "$CARAVANA_HOST"
  trap 'ssh -S "$PROVE_SOCK" -O exit "$CARAVANA_HOST" 2>/dev/null || true' EXIT
  ip="$(host_ip)"
  token="$(management_token)"

  echo "Proving the Phase 1 gate on $CARAVANA_HOST (about 12 minutes)..."
  local status=0
  (cd "$ROOT" && CARAVANA_GATE=1 \
    NOMAD_ADDR="http://127.0.0.1:$LOCAL_NOMAD" NOMAD_TOKEN="$token" \
    GATE_API="http://127.0.0.1:$LOCAL_API" \
    GATE_WEBHOOK="https://$CARAVANA_DOMAIN_WEBHOOK" \
    GATE_APP_HOST="$CARAVANA_DOMAIN_APP" \
    GATE_PUBLIC_IP="$ip" \
    GATE_ACME_STAGING="$CARAVANA_ACME_STAGING" \
    go test ./cmd/caravana -run TestGate -v -count=1 -timeout 20m) 2>&1 |
    awk '
      /--- PASS: TestGate\// { sub(/.*TestGate\//, ""); sub(/ \(.*/, ""); print "PASS " $0; next }
      /--- FAIL: TestGate\// { sub(/.*TestGate\//, ""); sub(/ \(.*/, ""); print "FAIL " $0; next }
      /--- SKIP: TestGate/   { print "SKIP: CARAVANA_GATE not set"; next }
      /_test\.go:[0-9]+:/    { sub(/^[ \t]*/, ""); print "     " $0; next }
    ' || status=$? # with pipefail this is go test's exit code: awk always succeeds
  if [ "$status" = 0 ]; then
    echo "GATE PASSED"
  else
    echo "GATE FAILED"
  fi
  return "$status"
}

cmd_ssh() {
  load_env
  if [ "$#" -eq 0 ]; then
    ssh "$CARAVANA_HOST"
  else
    remote "$@"
  fi
}

case "${1:-}" in
up) cmd_up ;;
env) cmd_env ;;
tunnel) cmd_tunnel ;;
prove) cmd_prove ;;
ssh)
  shift
  cmd_ssh "$@"
  ;;
*)
  sed -n '2,10p' "$0" | sed 's/^# \{0,1\}//'
  exit 2
  ;;
esac
