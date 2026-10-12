#!/usr/bin/env bash
# Local Nomad lab for Caravana, in a Lima VM.
#
#   deploy/dev/lab.sh up       create or start the VM, run Traefik, write deploy/dev/.data/env
#   deploy/dev/lab.sh check [job.json]
#                              deploy whoami (default: deploy/dev/whoami.json) and call it over HTTPS
#   deploy/dev/lab.sh env      print NOMAD_ADDR/NOMAD_TOKEN exports: eval "$(deploy/dev/lab.sh env)"
#   deploy/dev/lab.sh shell    open a shell in the VM (use `sudo lab-nomad ...` there)
#   deploy/dev/lab.sh stop     stop the VM, keeping its state
#   deploy/dev/lab.sh delete   remove the VM and its state
set -euo pipefail

NAME=caravana-lab
DIR="$(cd "$(dirname "$0")" && pwd)"
DATA="$DIR/.data"

vm_nomad() { limactl shell --workdir / "$NAME" sudo lab-nomad "$@"; }

exists() { limactl list --quiet 2>/dev/null | grep -qx "$NAME"; }

write_env() {
  mkdir -p "$DATA"
  local token
  token="$(limactl shell --workdir / "$NAME" sudo cat /var/lib/caravana-lab/bootstrap.token)"
  (
    umask 077
    printf 'export NOMAD_ADDR=http://127.0.0.1:4646\nexport NOMAD_TOKEN=%s\n' "$token" >"$DATA/env"
  )
}

cmd_up() {
  if exists; then
    limactl start "$NAME"
  else
    limactl start --name "$NAME" --tty=false "$DIR/lab.yaml"
  fi
  write_env

  # Services may burst past their memory reservation up to their ceiling, as on an installed
  # host (deploy/install/install.sh).
  vm_nomad operator scheduler set-config -memory-oversubscription=true >/dev/null

  # The repo is visible inside the VM at the same path (home folder mount, read-only).
  vm_nomad acl policy apply -namespace default -job traefik \
    -description "Traefik reads service registrations" traefik-read "$DIR/traefik-policy.hcl"
  vm_nomad job run -detach "$DIR/traefik.nomad.hcl"

  cat <<EOF

Lab is up.
  Nomad UI:          http://127.0.0.1:4646/ui   (token: NOMAD_TOKEN in deploy/dev/.data/env)
  Traefik dashboard: http://127.0.0.1:8088/dashboard/
  Apps:              https://<host>.localhost:8443 (self-signed cert)
Next: deploy/dev/lab.sh check
EOF
}

cmd_check() {
  local job="${1:-$DIR/whoami.json}"
  [ -f "$job" ] || { echo "no such job file: $job" >&2; return 1; }
  # The VM mounts the repo at the same absolute path, so pass an absolute one.
  job="$(cd "$(dirname "$job")" && pwd)/$(basename "$job")"
  vm_nomad job run -json "$job"
  echo "Waiting for Traefik to route whoami.localhost..."
  for _ in $(seq 1 30); do # about a minute
    if curl -fsk https://whoami.localhost:8443/; then
      echo "OK: whoami answers over HTTPS through Traefik."
      return 0
    fi
    sleep 2
  done
  echo "whoami did not answer on https://whoami.localhost:8443/" >&2
  return 1
}

case "${1:-}" in
  up) cmd_up ;;
  check)
    shift
    cmd_check "$@"
    ;;
  env) cat "$DATA/env" ;;
  shell) limactl shell "$NAME" ;;
  stop) limactl stop "$NAME" ;;
  delete) limactl delete --force "$NAME" && rm -f "$DATA/env" ;;
  *)
    sed -n '2,10p' "$0" | sed 's/^# \{0,1\}//'
    exit 2
    ;;
esac
