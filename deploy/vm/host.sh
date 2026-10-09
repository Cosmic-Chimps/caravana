#!/usr/bin/env bash
# A public Caravana host on a VM you own, driven from your Mac over SSH. It installs with the
# product installer (deploy/install/install.sh), from a release built here, so the gate proves
# the install users run.
#
#   deploy/vm/host.sh up         build a dev release and install or upgrade it on the host
#   deploy/vm/host.sh admin      sudo caravana admin <command> on the host (status, reset-password,
#                                create-token, revoke-token; up creates the admin)
#   deploy/vm/host.sh prove      prove the gate on the host (about 15 minutes)
#   deploy/vm/host.sh env        print NOMAD_ADDR/NOMAD_TOKEN/CARAVANA_URL for the tunnel
#   deploy/vm/host.sh tunnel     forward Nomad and Traefik's dashboard to this Mac (Ctrl-C to close)
#   deploy/vm/host.sh uninstall  remove Caravana from the host; Nomad, Traefik and the apps stay
#   deploy/vm/host.sh ssh        a shell on the host, or: host.sh ssh <command>
#
# Settings come from deploy/vm/host.env (copy host.env.example). See deploy/vm/README.md.
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../.." && pwd)"
STATE=/var/lib/caravana-host # the installer's state (install.sh)

# Local ports for the tunnel; different from the lab's, so both can run at once.
LOCAL_NOMAD=14646
LOCAL_DASHBOARD=18081
FORWARDS=(
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
  for name in CARAVANA_HOST CARAVANA_DOMAIN_CONSOLE CARAVANA_DOMAIN_APP CARAVANA_ADMIN_EMAIL CARAVANA_ACME_EMAIL CARAVANA_ACME_STAGING; do
    [ -n "${!name:-}" ] || { echo "$name is empty in $file" >&2; exit 1; }
  done
}

# One SSH connection, reused by every command below.
SSH_OPTS=(-o ControlMaster=auto -o "ControlPath=$HOME/.ssh/caravana-host-%C" -o ControlPersist=120)
# ssh joins its arguments into one string the remote shell parses again, so quote each one.
# shellcheck disable=SC2029 # expanded (and quoted) on this side on purpose
remote() { ssh "${SSH_OPTS[@]}" "$CARAVANA_HOST" "$(printf '%q ' "$@")"; }
# remote_tty is remote with a terminal, for the password prompts.
# shellcheck disable=SC2029
remote_tty() {
  local tty=()
  [ -t 0 ] && tty=(-t)
  ssh "${tty[@]}" "${SSH_OPTS[@]}" "$CARAVANA_HOST" "$(printf '%q ' "$@")"
}

# The host's IP: the SSH target's host part, resolved.
host_ip() {
  local host="${CARAVANA_HOST#*@}"
  python3 -c 'import socket, sys; print(socket.gethostbyname(sys.argv[1]))' "$host"
}

# The gate's app domain must point at the host too; the installer checks the console's.
check_dns() {
  local ip got
  ip="$(host_ip)"
  got="$(python3 -c 'import socket, sys
try: print(socket.gethostbyname(sys.argv[1]))
except OSError: print("")' "$CARAVANA_DOMAIN_APP")"
  if [ "$got" != "$ip" ]; then
    echo "warning: $CARAVANA_DOMAIN_APP resolves to '${got:-nothing}', not the host's $ip; the gate's app can't get a certificate until it does" >&2
  fi
}

management_token() { remote sudo cat "$STATE/bootstrap.token"; }

cmd_up() {
  load_env
  check_dns

  local version tmp rtmp acme=production status=0
  version="0.0.0-dev.$(date +%s)"
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN
  echo "==> building release $version"
  "$ROOT/tools/release.sh" build "$version" "$tmp" >/dev/null

  echo "==> copying it to $CARAVANA_HOST"
  rtmp="$(remote mktemp -d)"
  scp -q "${SSH_OPTS[@]}" "$tmp"/* "$CARAVANA_HOST:$rtmp/"

  [ "$CARAVANA_ACME_STAGING" = 1 ] && acme=staging
  remote_tty sudo bash "$rtmp/install.sh" --from "$rtmp" \
    --email "$CARAVANA_ADMIN_EMAIL" --domain "$CARAVANA_DOMAIN_CONSOLE" \
    --acme "$acme" --acme-email "$CARAVANA_ACME_EMAIL" || status=$?
  remote rm -rf "$rtmp"
  [ "$status" = 0 ] || { echo "the install failed on the host (exit $status)" >&2; exit "$status"; }
}

cmd_uninstall() {
  load_env
  local rtmp
  rtmp="$(remote mktemp -d)"
  scp -q "${SSH_OPTS[@]}" "$ROOT/deploy/install/install.sh" "$CARAVANA_HOST:$rtmp/"
  remote sudo bash "$rtmp/install.sh" --uninstall "$@"
  remote rm -rf "$rtmp"
}

cmd_env() {
  load_env
  printf 'export NOMAD_ADDR=http://127.0.0.1:%s\n' "$LOCAL_NOMAD"
  printf 'export NOMAD_TOKEN=%s\n' "$(management_token)"
  printf 'export CARAVANA_URL=https://%s\n' "$CARAVANA_DOMAIN_CONSOLE"
}

cmd_tunnel() {
  load_env
  cat <<EOF
Tunnel to $CARAVANA_HOST (Ctrl-C to close):
  Nomad UI and API:   http://127.0.0.1:$LOCAL_NOMAD/ui   (token: deploy/vm/host.sh env)
  Traefik dashboard:  http://127.0.0.1:$LOCAL_DASHBOARD/dashboard/
The console is public: https://$CARAVANA_DOMAIN_CONSOLE/
EOF
  ssh -N -o ExitOnForwardFailure=yes "${FORWARDS[@]}" "$CARAVANA_HOST"
}

cmd_admin() {
  load_env
  [ "$#" -gt 0 ] || { echo "usage: deploy/vm/host.sh admin <init|status|reset-password|create-token|revoke-token> [flags]" >&2; exit 2; }
  remote_tty sudo caravana admin "$@"
}

# What the end of a prove run undoes: the tunnel, and the API token the gate used.
# Global variables, not locals: the EXIT trap runs after cmd_prove has returned, when its
# locals are gone, and an empty socket path would leave the tunnel running.
PROVE_SOCK=""
PROVE_TOKEN_NAME=""
prove_cleanup() {
  [ -z "$PROVE_TOKEN_NAME" ] || remote sudo caravana admin revoke-token --name "$PROVE_TOKEN_NAME" >/dev/null 2>&1 || true
  [ -z "$PROVE_SOCK" ] || ssh -S "$PROVE_SOCK" -O exit "$CARAVANA_HOST" 2>/dev/null || true
}

cmd_prove() {
  load_env
  local ip token api_token
  # The API is closed without a token (feature 006), so the gate needs an admin to exist.
  if ! remote sudo caravana admin status >/dev/null 2>&1; then
    echo "create the admin first: deploy/vm/host.sh admin init --email <email>" >&2
    exit 1
  fi
  trap prove_cleanup EXIT
  # Nomad stays private: the gate reads it through the tunnel.
  PROVE_SOCK="$(mktemp -u "${TMPDIR:-/tmp}/caravana-prove.XXXXXX")"
  ssh -f -N -M -S "$PROVE_SOCK" -o ExitOnForwardFailure=yes "${FORWARDS[@]}" "$CARAVANA_HOST"
  ip="$(host_ip)"
  token="$(management_token)"
  # The gate's own API token: made now, revoked when the run ends, pass or fail.
  PROVE_TOKEN_NAME="gate-$(date +%s)"
  api_token="$(remote sudo caravana admin create-token --name "$PROVE_TOKEN_NAME" </dev/null 2>/dev/null)" || {
    PROVE_TOKEN_NAME=""
    echo "could not create the gate's API token" >&2
    exit 1
  }

  # The API and the webhook go through the public console domain, as a user's would.
  echo "Proving the gate on $CARAVANA_HOST (about 15 minutes)..."
  local status=0
  (cd "$ROOT" && CARAVANA_GATE=1 \
    NOMAD_ADDR="http://127.0.0.1:$LOCAL_NOMAD" NOMAD_TOKEN="$token" \
    GATE_API="https://$CARAVANA_DOMAIN_CONSOLE" \
    GATE_API_TOKEN="$api_token" \
    GATE_WEBHOOK="https://$CARAVANA_DOMAIN_CONSOLE" \
    GATE_APP_HOST="$CARAVANA_DOMAIN_APP" \
    GATE_PUBLIC_IP="$ip" \
    GATE_ACME_STAGING="$CARAVANA_ACME_STAGING" \
    go test ./cmd/caravana -run TestGate -v -count=1 -timeout 25m) 2>&1 |
    awk '
      /--- PASS: TestGate\// { sub(/.*TestGate\//, ""); sub(/ \(.*/, ""); print "PASS " $0; next }
      /--- FAIL: TestGate\// { sub(/.*TestGate\//, ""); sub(/ \(.*/, ""); print "FAIL " $0; next }
      /--- SKIP: TestGate\// { sub(/.*TestGate\//, ""); sub(/ \(.*/, ""); print "SKIP " $0; next }
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
admin)
  shift
  cmd_admin "$@"
  ;;
uninstall)
  shift
  cmd_uninstall "$@"
  ;;
prove) cmd_prove ;;
ssh)
  shift
  cmd_ssh "$@"
  ;;
*)
  sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//'
  exit 2
  ;;
esac
