#!/bin/bash
# Turns a fresh Ubuntu VM into a public Caravana host. Runs on the host as root, from the
# temporary directory deploy/vm/host.sh copies everything to. Idempotent: run it again and it
# changes nothing, except what its inputs changed (research R8). Prints "changed: <list>".
#
# Inputs (environment): CARAVANA_DOMAIN_WEBHOOK, CARAVANA_ACME_EMAIL, CARAVANA_ACME_STAGING,
# CARAVANA_BINARY (path of the Caravana binary to install).
set -euo pipefail

SRC="$(cd "$(dirname "$0")" && pwd)"
STATE=/var/lib/caravana-host
LE_PRODUCTION=https://acme-v02.api.letsencrypt.org/directory
LE_STAGING=https://acme-staging-v02.api.letsencrypt.org/directory

: "${CARAVANA_DOMAIN_WEBHOOK:?}" "${CARAVANA_ACME_EMAIL:?}" "${CARAVANA_ACME_STAGING:?}" "${CARAVANA_BINARY:?}"

changed=()
step() { echo "==> $*"; }

# write_if_changed DEST MODE NAME: write stdin to DEST (with MODE) only if it differs, and
# record NAME as changed. Returns 0 when it wrote, 1 when DEST was already the same.
write_if_changed() {
  local dest="$1" mode="$2" name="$3" tmp
  tmp="$(mktemp)"
  cat >"$tmp"
  if [ -f "$dest" ] && cmp -s "$tmp" "$dest"; then
    rm -f "$tmp"
    return 1
  fi
  install -D -m "$mode" "$tmp" "$dest"
  rm -f "$tmp"
  changed+=("$name")
}

export DEBIAN_FRONTEND=noninteractive

step "packages"
if ! command -v ufw >/dev/null; then
  apt-get update
  apt-get install -y ufw
fi

step "private interface caravana0 (allocation ports bind here, never publicly)"
write_if_changed /etc/systemd/network/10-caravana0.netdev 0644 caravana0 <<'EOF' || true
[NetDev]
Name=caravana0
Kind=dummy
EOF
write_if_changed /etc/systemd/network/10-caravana0.network 0644 caravana0 <<'EOF' || true
[Match]
Name=caravana0

[Network]
Address=10.99.0.1/32
EOF
systemctl enable --now systemd-networkd >/dev/null
networkctl reload
timeout 30s bash -c 'until ip -4 addr show dev caravana0 2>/dev/null | grep -q 10.99.0.1; do sleep 1; done'

step "Nomad config"
mkdir -p "$STATE/traefik"
chmod 700 "$STATE"
cpu_line=""
if ! lscpu | grep -qi 'mhz'; then
  # ARM VMs often report no clock speed; Nomad then sees 0 MHz and places nothing (lab finding).
  cpu_line="cpu_total_compute = $(($(nproc) * 2000))"
fi
sed "s|#CPU_TOTAL_COMPUTE#|$cpu_line|" "$SRC/nomad.hcl.tpl" |
  write_if_changed /etc/nomad.d/nomad.hcl 0644 nomad-config || true

step "Docker, CNI, Nomad (the lab's bootstrap)"
# The lab's setup.sh is verbose (set -x); keep its log and pass on only what it changed.
CARAVANA_STATE_DIR="$STATE" bash "$SRC/dev-setup.sh" >"$STATE/bootstrap.log" 2>&1 ||
  { tail -20 "$STATE/bootstrap.log"; exit 1; }
while read -r name; do changed+=("$name"); done < <(sed -n 's/^changed: //p' "$STATE/bootstrap.log")
nomad_cli() { /usr/local/bin/lab-nomad "$@"; }

step "firewall: only 22, 80, 443 from the internet"
before="$(ufw status verbose 2>/dev/null || true)"
ufw allow 22/tcp >/dev/null
ufw allow 80/tcp >/dev/null
ufw allow 443/tcp >/dev/null
ufw default deny incoming >/dev/null
ufw default allow outgoing >/dev/null
# Containers on Nomad's bridge need forwarding; inbound exposure is handled by caravana0.
ufw default allow routed >/dev/null
ufw --force enable >/dev/null
[ "$before" = "$(ufw status verbose)" ] || changed+=(firewall)

step "Nomad ACL policies"
nomad_cli acl policy apply -namespace default -job traefik \
  -description "Traefik reads service registrations" traefik-read "$SRC/traefik-policy.hcl" >/dev/null
nomad_cli acl policy apply -description "Caravana registers jobs and follows deployments" \
  caravana "$SRC/caravana-policy.hcl" >/dev/null

step "Traefik"
acme_ca="$LE_PRODUCTION"
[ "$CARAVANA_ACME_STAGING" = "1" ] && acme_ca="$LE_STAGING"
traefik_version() { nomad_cli job inspect -t '{{.Version}}' traefik 2>/dev/null || echo none; }
v_before="$(traefik_version)"
# Traefik keeps serving any stored certificate that is still valid, whichever CA issued it, so
# switching between staging and production starts from an empty store. Traefik is stopped
# first: it holds the certificates in memory and would write them back.
if [ "$(cat "$STATE/acme-ca" 2>/dev/null)" != "$acme_ca" ]; then
  if [ -f "$STATE/traefik/acme.json" ]; then
    nomad_cli job stop -detach traefik >/dev/null 2>&1 || true
    timeout 60s bash -c 'while docker ps --format "{{.Image}}" | grep -q "^traefik:"; do sleep 1; done'
    rm -f "$STATE/traefik/acme.json"
    changed+=(certificates)
  fi
  echo "$acme_ca" >"$STATE/acme-ca"
fi
nomad_cli job run -detach \
  -var "webhook_host=$CARAVANA_DOMAIN_WEBHOOK" \
  -var "acme_email=$CARAVANA_ACME_EMAIL" \
  -var "acme_ca=$acme_ca" \
  "$SRC/traefik.nomad.hcl" >/dev/null
[ "$v_before" = "$(traefik_version)" ] || changed+=(traefik)

step "Caravana"
id caravana >/dev/null 2>&1 || useradd --system --home-dir /var/lib/caravana --shell /usr/sbin/nologin caravana
install -d -m 0700 -o caravana -g caravana /var/lib/caravana

# Caravana's Nomad token is created once and kept in its env file.
token="$(sed -n 's/^NOMAD_TOKEN=//p' /etc/caravana/caravana.env 2>/dev/null || true)"
if [ -z "$token" ]; then
  token="$(nomad_cli acl token create -name caravana -policy caravana -t '{{.SecretID}}')"
fi
service_changed=0
write_if_changed /etc/caravana/caravana.env 0600 caravana-env <<EOF && service_changed=1
CARAVANA_LISTEN=127.0.0.1:7070
CARAVANA_WEBHOOK_LISTEN=127.0.0.1:7071
CARAVANA_DATA_DIR=/var/lib/caravana
CARAVANA_CERT_RESOLVER=letsencrypt
NOMAD_ADDR=http://127.0.0.1:4646
NOMAD_TOKEN=$token
EOF
write_if_changed /usr/local/bin/caravana 0755 caravana-binary <"$CARAVANA_BINARY" && service_changed=1
write_if_changed /etc/systemd/system/caravana.service 0644 caravana-unit <"$SRC/caravana.service" && service_changed=1
systemctl daemon-reload
systemctl enable caravana >/dev/null 2>&1
if [ "$service_changed" = 1 ] || ! systemctl is-active --quiet caravana; then
  systemctl restart caravana
fi
if ! timeout 30s bash -c 'until curl -fsS http://127.0.0.1:7070/v1/health >/dev/null 2>&1; do sleep 1; done'; then
  journalctl -u caravana -n 20 --no-pager
  echo "Caravana didn't answer its health check within 30 s" >&2
  exit 1
fi

# One line per name, without repeats.
echo "changed: $(printf '%s\n' "${changed[@]}" | awk 'NF && !seen[$0]++' | paste -sd ' ' -)"
