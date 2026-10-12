#!/usr/bin/env bash
# Caravana's installer: turns a fresh Ubuntu 24.04 VM into a Caravana host (Docker, Nomad with
# ACLs, Traefik with Let's Encrypt, Caravana under systemd), upgrades it when run again, and
# removes Caravana with --uninstall. Each release publishes this file with its version set.
#
#   curl -fsSL https://github.com/Cosmic-Chimps/caravana/releases/latest/download/install.sh |
#     sudo bash -s -- --email you@example.com --domain caravana.example.com
#
# Options (a later run reuses the ones recorded on the host):
#   --email ADDRESS        the admin's email (required the first time)
#   --domain NAME          the console's domain; default <public-ip>.sslip.io
#   --acme MODE            production (default), staging, or none (Traefik's own certificate)
#   --acme-email ADDRESS   the Let's Encrypt account email; default --email
#   --from DIR             use this release's files from DIR instead of downloading them
#   --uninstall [--purge]  remove Caravana, keep Nomad, Traefik and every app (--purge: its data too)
#
# Exit codes: 0 done, 1 a step failed, 2 refused before changing anything.
set -euo pipefail

CARAVANA_VERSION=dev # set by tools/release.sh
RELEASES=https://github.com/Cosmic-Chimps/caravana/releases/download
# The same pins as the lab (deploy/dev/setup.sh); a test keeps them equal.
NOMAD_VERSION=2.0.7
CNI_VERSION=1.9.1

STATE=/var/lib/caravana-host # installer state, Nomad's bootstrap token, Traefik's certificates
OPT=/opt/caravana            # one directory per installed version, and the "current" link
DATA=/var/lib/caravana       # Caravana's data
ENV_FILE=/etc/caravana/caravana.env

changed=()
step() { echo "==> $*"; }
refuse() {
  echo "caravana install: $*" >&2
  exit 2
}
fail() {
  echo "caravana install: $*" >&2
  exit 1
}

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

nomad_cli() { NOMAD_ADDR=http://127.0.0.1:4646 NOMAD_TOKEN="$(cat "$STATE/bootstrap.token")" nomad "$@"; }

# installed_version prints the version "current" points at, or nothing.
installed_version() {
  local target
  target="$(readlink "$OPT/current" 2>/dev/null)" || return 0
  basename "$target"
}

# newer A B: true when version A sorts after version B.
newer() { [ "$1" != "$2" ] && [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | tail -1)" = "$1" ]; }

# ---- options -------------------------------------------------------------------------------

email="" domain="" acme="" acme_email="" from="" uninstall=0 purge=0
while [ $# -gt 0 ]; do
  case "$1" in
  --email | --domain | --acme | --acme-email | --from)
    [ $# -ge 2 ] || refuse "$1 needs a value"
    case "$1" in
    --email) email="$2" ;;
    --domain) domain="$2" ;;
    --acme) acme="$2" ;;
    --acme-email) acme_email="$2" ;;
    --from) from="$2" ;;
    esac
    shift 2
    ;;
  --uninstall) uninstall=1 && shift ;;
  --purge) purge=1 && shift ;;
  -h | --help)
    sed -n '2,17p' "$0" 2>/dev/null | sed 's/^# \{0,1\}//' || true
    exit 0
    ;;
  *) refuse "unknown option $1 (see --help)" ;;
  esac
done

# Options not given on this run come from the last one.
if [ -f "$STATE/install.env" ]; then
  # shellcheck source=/dev/null
  . "$STATE/install.env"
  email="${email:-${CARAVANA_ADMIN_EMAIL:-}}"
  domain="${domain:-${CARAVANA_DOMAIN:-}}"
  acme="${acme:-${CARAVANA_ACME:-}}"
  acme_email="${acme_email:-${CARAVANA_ACME_EMAIL:-}}"
fi
acme="${acme:-production}"
acme_email="${acme_email:-$email}"

# ---- preflight: nothing changes until every check passes ------------------------------------

preflight() {
  [ "$(id -u)" = 0 ] || refuse "run it as root (sudo bash ...)"

  local os_id="" os_version=""
  # shellcheck source=/dev/null
  [ -r /etc/os-release ] && os_id="$(. /etc/os-release && echo "$ID")" && os_version="$(. /etc/os-release && echo "$VERSION_ID")"
  if [ "$os_id" != ubuntu ] || [ "$os_version" != 24.04 ]; then
    refuse "this is ${os_id:-an unknown OS} ${os_version}; Caravana installs on Ubuntu 24.04 LTS"
  fi

  arch="$(dpkg --print-architecture)"
  case "$arch" in amd64 | arm64) ;; *) refuse "this CPU is $arch; Caravana runs on amd64 and arm64" ;; esac

  local mem_kb
  mem_kb="$(awk '/^MemTotal:/ {print $2}' /proc/meminfo)"
  # A "2 GB" VM reports a little under 2 GiB.
  [ "$mem_kb" -ge 1800000 ] || refuse "this machine has $((mem_kb / 1024)) MB of memory; Caravana needs 2 GB"

  [ "$CARAVANA_VERSION" != dev ] ||
    refuse "this install.sh has no version: use the one published with a release, or build one with tools/release.sh"
  case "$acme" in production | staging | none) ;; *) refuse "--acme is production, staging or none, not $acme" ;; esac
  [ -n "$email" ] || refuse "--email is required the first time: the admin signs in with it"

  local installed
  installed="$(installed_version)"
  if [ -n "$installed" ] && newer "$installed" "$CARAVANA_VERSION"; then
    refuse "Caravana $installed is installed; this installer is for $CARAVANA_VERSION, which is older. Its data may be newer than $CARAVANA_VERSION can read."
  fi

  # A Nomad or Docker this installer didn't set up belongs to someone else.
  if [ ! -f "$STATE/install.env" ] && [ ! -f "$STATE/bootstrap.token" ]; then
    if command -v nomad >/dev/null || [ -f /etc/systemd/system/nomad.service ]; then
      refuse "Nomad is already installed here; Caravana installs its own on a fresh VM"
    fi
    if command -v docker >/dev/null; then
      refuse "Docker is already installed here; Caravana installs its own on a fresh VM"
    fi
  fi

  local port holder
  for port in 80 443; do
    holder="$(ss -ltnpH "sport = :$port" 2>/dev/null | head -1)"
    if [ -n "$holder" ] && ! grep -q '"traefik"' <<<"$holder"; then
      refuse "port $port is in use ($(grep -o 'users:(("[^"]*"' <<<"$holder" | cut -d'"' -f2 || echo "something else")); Traefik needs 80 and 443"
    fi
  done

  if [ -z "$domain" ] || [ "$acme" != none ]; then
    public_ip="$(curl -fsS --max-time 10 https://checkip.amazonaws.com | tr -d '[:space:]')" ||
      refuse "can't find this machine's public IP address (https://checkip.amazonaws.com)"
  fi
  domain="${domain:-${public_ip//./-}.sslip.io}"
  if [ "$acme" != none ]; then
    local resolved
    resolved="$(getent ahostsv4 "$domain" | awk 'NR == 1 {print $1}')"
    [ "$resolved" = "$public_ip" ] ||
      refuse "$domain resolves to ${resolved:-nothing}, not this machine ($public_ip); Let's Encrypt can't issue its certificate until it does"
  fi
}

# ---- the release -----------------------------------------------------------------------------

# fetch_release: put this version's files in $OPT/<version>, after checking their checksums.
fetch_release() {
  local name="caravana_${CARAVANA_VERSION}_linux_${arch}" dir="$OPT/$CARAVANA_VERSION" tmp
  [ -x "$dir/caravana" ] && return 0
  tmp="$(mktemp -d)"
  if [ -n "$from" ]; then
    cp "$from/SHA256SUMS" "$from/$name.tar.gz" "$tmp/" || fail "$from has no SHA256SUMS or $name.tar.gz"
  else
    if ! curl -fsSL -o "$tmp/SHA256SUMS" "$RELEASES/v$CARAVANA_VERSION/SHA256SUMS" ||
      ! curl -fsSL -o "$tmp/$name.tar.gz" "$RELEASES/v$CARAVANA_VERSION/$name.tar.gz"; then
      fail "can't download Caravana $CARAVANA_VERSION from $RELEASES"
    fi
  fi
  (cd "$tmp" && grep " $name.tar.gz\$" SHA256SUMS | sha256sum -c --quiet -) ||
    fail "$name.tar.gz doesn't match its checksum; nothing was installed"
  tar -xzf "$tmp/$name.tar.gz" -C "$tmp"
  mkdir -p "$OPT"
  rm -rf "$dir"
  mv "$tmp/$name" "$dir"
  rm -rf "$tmp"
  changed+=(caravana-release)
}

# ---- the host: packages, Nomad, network, firewall --------------------------------------------

install_packages() {
  step "packages"
  export DEBIAN_FRONTEND=noninteractive
  local missing=()
  for pkg in docker.io unzip curl ca-certificates ufw; do
    dpkg -s "$pkg" >/dev/null 2>&1 || missing+=("$pkg")
  done
  if [ ${#missing[@]} -gt 0 ]; then
    # needrestart's report on Ubuntu is noise here; the log keeps everything.
    if ! { apt-get update -qq && NEEDRESTART_SUSPEND=1 apt-get install -y -qq "${missing[@]}"; } >"$STATE/apt.log" 2>&1; then
      tail -20 "$STATE/apt.log" >&2
      fail "installing ${missing[*]} failed (the whole log: $STATE/apt.log)"
    fi
    changed+=(packages)
  fi
  systemctl enable --now docker >/dev/null 2>&1
}

install_nomad() {
  step "Nomad $NOMAD_VERSION and CNI plugins $CNI_VERSION"
  local tmp base file
  if [ "$(nomad version 2>/dev/null | head -1)" != "Nomad v${NOMAD_VERSION}" ]; then
    tmp="$(mktemp -d)"
    base="https://releases.hashicorp.com/nomad/${NOMAD_VERSION}"
    curl -fsSL -o "$tmp/nomad_${NOMAD_VERSION}_linux_${arch}.zip" "$base/nomad_${NOMAD_VERSION}_linux_${arch}.zip"
    curl -fsSL -o "$tmp/SHA256SUMS" "$base/nomad_${NOMAD_VERSION}_SHA256SUMS"
    (cd "$tmp" && sha256sum --ignore-missing --quiet -c SHA256SUMS) || fail "Nomad's download doesn't match its checksum"
    unzip -qo "$tmp/nomad_${NOMAD_VERSION}_linux_${arch}.zip" -d /usr/local/bin
    rm -rf "$tmp"
    changed+=(nomad-binary)
  fi
  if [ "$(cat /opt/cni/bin/.version 2>/dev/null)" != "$CNI_VERSION" ]; then
    tmp="$(mktemp -d)"
    file="cni-plugins-linux-${arch}-v${CNI_VERSION}.tgz"
    base="https://github.com/containernetworking/plugins/releases/download/v${CNI_VERSION}"
    curl -fsSL -o "$tmp/$file" "$base/$file"
    curl -fsSL -o "$tmp/$file.sha256" "$base/$file.sha256"
    (cd "$tmp" && sha256sum --quiet -c "$file.sha256") || fail "the CNI plugins' download doesn't match its checksum"
    mkdir -p /opt/cni/bin
    tar -xzf "$tmp/$file" -C /opt/cni/bin
    echo "$CNI_VERSION" >/opt/cni/bin/.version
    rm -rf "$tmp"
    changed+=(cni)
  fi

  # Bridge traffic must pass through iptables so Nomad's port mappings work.
  echo br_netfilter | write_if_changed /etc/modules-load.d/nomad.conf 0644 bridge-netfilter || true
  modprobe br_netfilter
  if write_if_changed /etc/sysctl.d/99-nomad.conf 0644 bridge-netfilter <<'EOF'; then
net.bridge.bridge-nf-call-arptables = 1
net.bridge.bridge-nf-call-ip6tables = 1
net.bridge.bridge-nf-call-iptables = 1
EOF
    sysctl --system >/dev/null
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
  systemctl enable --now systemd-networkd >/dev/null 2>&1
  networkctl reload
  timeout 30s bash -c 'until ip -4 addr show dev caravana0 2>/dev/null | grep -q 10.99.0.1; do sleep 1; done' ||
    fail "the caravana0 interface didn't come up"

  step "Nomad (one node, ACLs on)"
  mkdir -p "$STATE/traefik" /opt/nomad/data /etc/nomad.d
  chmod 700 "$STATE"
  local cpu_line=""
  if ! lscpu | grep -qi 'mhz'; then
    # ARM VMs often report no clock speed; Nomad then sees 0 MHz and places nothing.
    cpu_line="cpu_total_compute = $(($(nproc) * 2000))"
  fi
  sed "s|#CPU_TOTAL_COMPUTE#|$cpu_line|" "$OPT/$CARAVANA_VERSION/nomad.hcl.tpl" |
    write_if_changed /etc/nomad.d/nomad.hcl 0644 nomad-config || true
  write_if_changed /etc/systemd/system/nomad.service 0644 nomad-unit <<'EOF' || true
[Unit]
Description=Nomad agent
Wants=network-online.target
After=network-online.target docker.service

[Service]
ExecStart=/usr/local/bin/nomad agent -config /etc/nomad.d
ExecReload=/bin/kill -HUP $MAINPID
KillMode=process
KillSignal=SIGINT
Restart=on-failure
LimitNOFILE=65536
TasksMax=infinity
OOMScoreAdjust=-1000

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  # Restart Nomad only when its binary, unit or config changed, so a rerun leaves it alone.
  local sum
  sum="$(cat /usr/local/bin/nomad /etc/systemd/system/nomad.service /etc/nomad.d/*.hcl | sha256sum)"
  if [ "$(cat "$STATE/nomad.sum" 2>/dev/null)" != "$sum" ] || ! systemctl is-active --quiet nomad; then
    systemctl enable nomad >/dev/null 2>&1
    systemctl restart nomad
    echo "$sum" >"$STATE/nomad.sum"
    changed+=(nomad)
  fi
  # shellcheck disable=SC2016 # expanded by the inner bash, on each try
  timeout 120s bash -c 'until [ -n "$(curl -fsS http://127.0.0.1:4646/v1/status/leader 2>/dev/null | tr -d "\"")" ]; do sleep 2; done' ||
    fail "Nomad didn't elect a leader within 2 minutes (journalctl -u nomad)"

  # ACLs are bootstrapped once, with a token generated here, so it is on disk before Nomad
  # accepts it. It stays root-only on this host.
  if [ ! -s "$STATE/bootstrap.token" ]; then
    (umask 077 && cat /proc/sys/kernel/random/uuid >"$STATE/bootstrap.token.tmp")
    timeout 60s bash -c "until nomad acl bootstrap -address=http://127.0.0.1:4646 '$STATE/bootstrap.token.tmp' >/dev/null; do sleep 2; done" ||
      fail "Nomad's ACL bootstrap failed"
    mv "$STATE/bootstrap.token.tmp" "$STATE/bootstrap.token"
    changed+=(nomad-acl)
  fi
  write_if_changed /usr/local/bin/caravana-nomad 0755 nomad-cli <<EOF || true
#!/bin/sh
# Nomad's CLI on this host with the management token: sudo caravana-nomad status
NOMAD_ADDR=http://127.0.0.1:4646 NOMAD_TOKEN="\$(cat $STATE/bootstrap.token)" exec nomad "\$@"
EOF

  step "firewall: only 22, 80 and 443 from the internet"
  local before
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

  step "Nomad scheduler: services may burst into spare memory"
  # A service reserves its memory and may use up to its ceiling (memory_max) while the node has
  # memory spare. Nomad ignores ceilings unless the cluster allows memory oversubscription.
  if ! nomad_cli operator scheduler get-config | grep -Eq '^Memory Oversubscription +=+ +true'; then
    nomad_cli operator scheduler set-config -memory-oversubscription=true >/dev/null
    changed+=(scheduler)
  fi

  step "Nomad ACL policies"
  nomad_cli acl policy apply -namespace default -job traefik \
    -description "Traefik reads service registrations" traefik-read "$OPT/$CARAVANA_VERSION/traefik-policy.hcl" >/dev/null
  nomad_cli acl policy apply -description "Caravana registers jobs and follows deployments" \
    caravana "$OPT/$CARAVANA_VERSION/caravana-policy.hcl" >/dev/null
}

# run_traefik [CONSOLE_DOMAIN]: run Traefik, routing CONSOLE_DOMAIN to Caravana when given.
run_traefik() {
  local console="${1:-}" before
  before="$(nomad_cli job inspect -t '{{.Version}}' traefik 2>/dev/null || echo none)"
  # Traefik keeps serving a stored certificate while it is valid, whichever CA issued it, so a
  # change of mode starts from an empty store. Traefik is stopped first: it holds the
  # certificates in memory and would write them back.
  if [ "$(cat "$STATE/acme" 2>/dev/null)" != "$acme" ]; then
    if [ -f "$STATE/traefik/acme.json" ]; then
      nomad_cli job stop -detach traefik >/dev/null 2>&1 || true
      timeout 60s bash -c 'while docker ps --format "{{.Image}}" | grep -q "^traefik:"; do sleep 1; done' || true
      rm -f "$STATE/traefik/acme.json"
      changed+=(certificates)
    fi
    echo "$acme" >"$STATE/acme"
  fi
  local out
  out="$(nomad_cli job run -detach \
    -var "console_domain=$console" \
    -var "acme=$acme" \
    -var "acme_email=$acme_email" \
    "$OPT/$CARAVANA_VERSION/traefik.nomad.hcl" 2>&1)" || { echo "$out" >&2 && fail "Nomad refused the Traefik job"; }
  [ "$before" = "$(nomad_cli job inspect -t '{{.Version}}' traefik 2>/dev/null || echo none)" ] || changed+=(traefik)
}

# ---- Caravana --------------------------------------------------------------------------------

# health_is VERSION: true once Caravana answers its health check with VERSION, within 60 s.
health_is() {
  local want="$1"
  timeout 60s bash -c "until curl -fsS http://127.0.0.1:7070/v1/health 2>/dev/null | grep -q '\"version\":\"$want\"'; do sleep 1; done"
}

configure_caravana() {
  step "Caravana's user, settings and service"
  id caravana >/dev/null 2>&1 || useradd --system --home-dir "$DATA" --shell /usr/sbin/nologin caravana
  install -d -m 0700 -o caravana -g caravana "$DATA"

  # Caravana's Nomad token, limited to its policy, is created once and kept in its env file.
  local token resolver=letsencrypt
  token="$(sed -n 's/^NOMAD_TOKEN=//p' "$ENV_FILE" 2>/dev/null || true)"
  if [ -z "$token" ]; then
    token="$(nomad_cli acl token create -name caravana -policy caravana -t '{{.SecretID}}')"
  fi
  [ "$acme" = none ] && resolver=""
  # Traefik, on this host's loopback, is the only proxy: Caravana believes its forwarded
  # headers, so cookies are Secure and sign-in limits count each real client.
  service_changed=0
  write_if_changed "$ENV_FILE" 0600 caravana-env <<EOF && service_changed=1
CARAVANA_LISTEN=127.0.0.1:7070
CARAVANA_DATA_DIR=$DATA
CARAVANA_CERT_RESOLVER=$resolver
CARAVANA_TRUSTED_PROXIES=127.0.0.1,::1
NOMAD_ADDR=http://127.0.0.1:4646
NOMAD_TOKEN=$token
EOF
  chown caravana:caravana "$ENV_FILE"
  write_if_changed /etc/systemd/system/caravana.service 0644 caravana-unit <"$OPT/$CARAVANA_VERSION/caravana.service" && service_changed=1
  # `sudo caravana admin ...` (and `sudo caravana version`) on this host's data, as caravana.
  write_if_changed /usr/local/bin/caravana 0755 caravana-cli <<EOF || true
#!/bin/sh
# Caravana's CLI on this host: sudo caravana admin status, sudo caravana version
[ "\$(id -u)" = 0 ] || { echo "run it with sudo: sudo caravana \$*" >&2; exit 1; }
set -a
. $ENV_FILE
set +a
exec setpriv --reuid=caravana --regid=caravana --init-groups $OPT/current/caravana "\$@"
EOF
  systemctl daemon-reload
  systemctl enable caravana >/dev/null 2>&1
}

# point_current VERSION: make "current" point at VERSION, in one step.
point_current() {
  ln -sfn "$1" "$OPT/current.new"
  mv -T "$OPT/current.new" "$OPT/current"
}

# start_version: run this version. On a first install it just starts. On the same version it
# restarts only when its settings changed. On an upgrade it stops the old version, keeps a copy
# of the data (a new version may migrate it, and the old one can't read a migrated store),
# starts the new one, and when that one doesn't answer its health check puts the old binary
# and the copied data back. Apps keep running throughout: Nomad runs them, not Caravana.
start_version() {
  local installed
  installed="$(installed_version)"
  if [ "$installed" = "$CARAVANA_VERSION" ]; then
    if [ "$service_changed" = 1 ] || ! systemctl is-active --quiet caravana; then
      systemctl restart caravana
    fi
  elif [ -z "$installed" ]; then
    step "Caravana $CARAVANA_VERSION"
    point_current "$CARAVANA_VERSION"
    systemctl restart caravana
    changed+=(caravana)
  else
    step "upgrade Caravana $installed to $CARAVANA_VERSION"
    systemctl stop caravana
    rm -rf "$DATA".pre-*
    cp -a "$DATA" "$DATA.pre-$installed"
    point_current "$CARAVANA_VERSION"
    systemctl start caravana
    if ! health_is "$CARAVANA_VERSION"; then
      journalctl -u caravana -n 20 --no-pager >&2
      echo "==> putting Caravana $installed back" >&2
      systemctl stop caravana
      rm -rf "$DATA"
      cp -a "$DATA.pre-$installed" "$DATA"
      point_current "$installed"
      systemctl start caravana
      health_is "$installed" || fail "Caravana $installed didn't come back either (journalctl -u caravana)"
      fail "Caravana $CARAVANA_VERSION didn't answer its health check within 60 s; $installed runs again, with its data as it was"
    fi
    changed+=(caravana)
    # Keep this version and the one before it. "current" is a link, not a version.
    local dir
    for dir in "$OPT"/*; do
      if [ -d "$dir" ] && [ ! -L "$dir" ]; then
        case "$(basename "$dir")" in "$CARAVANA_VERSION" | "$installed") ;; *) rm -rf "$dir" ;; esac
      fi
    done
  fi
  health_is "$CARAVANA_VERSION" || {
    journalctl -u caravana -n 20 --no-pager >&2
    fail "Caravana $CARAVANA_VERSION didn't answer its health check within 60 s"
  }
}

# ensure_admin: create the admin in a terminal; otherwise say how. Prints nothing when one exists.
admin_note=""
ensure_admin() {
  if /usr/local/bin/caravana admin status >/dev/null 2>&1; then
    return 0
  fi
  if { : </dev/tty; } 2>/dev/null; then
    step "the admin ($email)"
    /usr/local/bin/caravana admin init --email "$email" </dev/tty ||
      admin_note="Create the admin: sudo caravana admin init --email $email"
  else
    admin_note="Create the admin: sudo caravana admin init --email $email"
  fi
}

record_options() {
  write_if_changed "$STATE/install.env" 0600 options <<EOF || true
CARAVANA_ADMIN_EMAIL=$email
CARAVANA_DOMAIN=$domain
CARAVANA_ACME=$acme
CARAVANA_ACME_EMAIL=$acme_email
EOF
}

install_caravana() {
  preflight
  mkdir -p "$STATE" && chmod 700 "$STATE"
  record_options # from here on, this installer owns Nomad and Docker on this host
  step "Caravana $CARAVANA_VERSION release files"
  command -v curl >/dev/null || { apt-get update -qq && apt-get install -y -qq curl >/dev/null; }
  fetch_release
  install_packages
  install_nomad
  configure_caravana
  start_version
  ensure_admin
  step "Traefik: https://$domain to Caravana"
  run_traefik "$domain"
  # The address printed below must answer: wait until Traefik routes the domain to Caravana
  # (its first start pulls its image). The certificate isn't checked here: Let's Encrypt may
  # still be issuing it, and Traefik serves its own until then.
  timeout 180s bash -c "until curl -ksf -o /dev/null --resolve '$domain:443:127.0.0.1' https://$domain/v1/health; do sleep 2; done" ||
    fail "Traefik doesn't route https://$domain to Caravana after 3 minutes (sudo caravana-nomad alloc logs -job traefik)"

  echo "changed: $(printf '%s\n' "${changed[@]}" | awk 'NF && !seen[$0]++' | paste -sd ' ' -)"
  echo
  echo "Caravana $CARAVANA_VERSION is installed."
  echo "  Console:  https://$domain/"
  [ "$acme" = none ] && echo "            (Traefik's own certificate: browsers will warn)"
  if [ -n "$admin_note" ]; then
    echo "  Next:     $admin_note, then sign in at https://$domain/login"
  else
    echo "  Next:     sign in at https://$domain/login as $email"
  fi
  echo "  Nomad:    sudo caravana-nomad status"
}

# uninstall_caravana removes Caravana and leaves Nomad, Docker, Traefik, the firewall and every
# app running: the apps are Nomad jobs, and feature 011 exports them as plain jobspecs. With
# --purge it also deletes Caravana's data, settings and user.
uninstall_caravana() {
  [ "$(id -u)" = 0 ] || refuse "run it as root (sudo bash ...)"
  if [ ! -e "$OPT/current" ] && [ ! -f /etc/systemd/system/caravana.service ]; then
    echo "Caravana isn't installed here; nothing to remove."
    [ "$purge" = 1 ] || exit 0
  fi
  step "stop Caravana"
  systemctl disable --now caravana >/dev/null 2>&1 || true
  rm -f /etc/systemd/system/caravana.service /usr/local/bin/caravana
  systemctl daemon-reload

  if [ -s "$STATE/bootstrap.token" ] && command -v nomad >/dev/null; then
    step "revoke Caravana's Nomad token"
    local id
    for id in $(nomad_cli acl token list -t '{{range .}}{{if eq .Name "caravana"}}{{.AccessorID}} {{end}}{{end}}' 2>/dev/null); do
      nomad_cli acl token delete "$id" >/dev/null
    done
  fi
  # Traefik is left as it is: any change to its job restarts it (its ports are static), and
  # every app's route would drop for a few seconds. The console domain answers 502 until
  # Caravana is installed again.
  # The token in the env file is revoked: a later install makes a new one.
  rm -f "$ENV_FILE"
  rm -rf "$OPT"

  if [ "$purge" = 1 ]; then
    step "delete Caravana's data, settings and user"
    rm -rf "$DATA" "$DATA".pre-* /etc/caravana
    userdel caravana 2>/dev/null || true
  fi
  echo
  echo "Caravana is removed. Nomad, Traefik and every app keep running."
  echo "  https://$domain/ answers 502 (Bad Gateway) until Caravana is installed again."
  [ "$purge" = 1 ] || echo "  Its data stays in $DATA; installing again picks it up."
  echo "  Nomad:    sudo caravana-nomad status"
}

if [ "$uninstall" = 1 ]; then
  uninstall_caravana
else
  install_caravana
fi
