#!/usr/bin/env bash
# Caravana's installer: turns a fresh Ubuntu 24.04 VM into a Caravana server, upgrades it when run
# again, and takes it out again. The first server (--first, the default) runs everything: Docker,
# Nomad with ACLs, the private network between servers, Traefik with Let's Encrypt, Caravana under
# systemd. Another server (--join) runs Docker, Nomad and Caravana's agent, and joins the first one.
# Each release publishes this file with its version set.
#
#   curl -fsSL https://github.com/Cosmic-Chimps/caravana/releases/latest/download/install.sh |
#     sudo bash -s -- --email you@example.com --domain caravana.example.com
#
# Another server runs the command the console's Add a server page shows:
#
#   curl -fsSL https://github.com/Cosmic-Chimps/caravana/releases/download/vX.Y.Z/install.sh |
#     sudo bash -s -- --join cvjn_... --url https://caravana.example.com
#
# Options (a later run reuses the ones recorded on the host; a join token is never recorded):
#   --first                this is the first server, the one that runs Caravana (the default)
#   --email ADDRESS        the admin's email (first server; required the first time)
#   --domain NAME          the console's domain (first server); default <public-ip>.sslip.io
#   --acme MODE            production (default), staging, or none (Traefik's own certificate)
#   --acme-email ADDRESS   the Let's Encrypt account email; default --email
#   --join TOKEN           join the cluster of the console at --url, with a token made there
#   --url URL              the console's address: https://NAME[:PORT] (http:// only for localhost)
#   --insecure             don't check the console's certificate (it uses Traefik's own: --acme none)
#   --address IP           the address other servers reach this one at; default: the public IP on
#                          the first server, the address the console sees on a joined one
#   --from DIR             use this release's files from DIR instead of downloading them
#   --leave                take a joined server out of the cluster (remove it in the console too)
#   --uninstall [--purge]  remove Caravana from the first server, keep Nomad, Traefik and every app
#                          (--purge: its data too)
#
# Exit codes: 0 done, 1 a step failed, 2 refused before changing anything.
#
# It also installs a small root updater (a systemd timer), so the console's Updates page can ask
# for an update: deploy/install/caravana-update.sh, which runs this installer from the release it
# downloads and checks. Every server runs the agent (caravana-node, a copy of the release's binary):
# it keeps the private network, Nomad's role and Nomad's certificate in line with the console.
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
UPDATER=/usr/local/libexec/caravana-update # the root updater, run every minute by its timer
UPDATE_DIR=/var/lib/caravana-update        # its status and logs; only root writes here
AGENT=/usr/local/libexec/caravana-node     # the server agent: a copy of the release's binary (feature 031)
AGENT_DIR=/etc/caravana-node               # the agent's folder: keys, state, node.env (root only)
NODE_ENV=$AGENT_DIR/node.env               # where the console is and this server's token; says "registered"
MESH_UNITS="caravana-mesh.service caravana-node.service caravana-node.timer"

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

# envq VALUE: VALUE as one word of an env file that the shell (". file") and systemd
# (EnvironmentFile=) both read. Plain values (letters, digits and . _ / : @ , + = -) stay as they
# are, so a file written by an older installer doesn't change. Anything else is single-quoted, so
# nothing in it can run or expand. A single quote can't be quoted the same way in both readers, so
# a value with one is refused.
envq() {
  if [[ "$1" =~ ^[A-Za-z0-9._/:@,+=-]*$ ]]; then
    printf '%s' "$1"
  elif [[ "$1" == *"'"* ]]; then
    fail "can't write a value with a single quote into an env file"
  else
    printf "'%s'" "$1"
  fi
}

# env_value FILE NAME: NAME's value from an env file written with envq, read as text. The file is
# never sourced here, so a hand-edited line can't run anything.
env_value() {
  local v
  v="$(sed -n "s/^$2=//p" "$1" 2>/dev/null | head -1)" || true
  v="${v#\'}"
  printf '%s' "${v%\'}"
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
first_flag=0 join_token="" url="" address="" insecure="" leave=0
while [ $# -gt 0 ]; do
  case "$1" in
  --email | --domain | --acme | --acme-email | --from | --join | --url | --address)
    [ $# -ge 2 ] || refuse "$1 needs a value"
    case "$1" in
    --email) email="$2" ;;
    --domain) domain="$2" ;;
    --acme) acme="$2" ;;
    --acme-email) acme_email="$2" ;;
    --from) from="$2" ;;
    --join) join_token="$2" ;;
    --url) url="$2" ;;
    --address) address="$2" ;;
    esac
    shift 2
    ;;
  --first) first_flag=1 && shift ;;
  --insecure) insecure=1 && shift ;;
  --leave) leave=1 && shift ;;
  --uninstall) uninstall=1 && shift ;;
  --purge) purge=1 && shift ;;
  -h | --help)
    sed -n '2,32p' "$0" 2>/dev/null | sed 's/^# \{0,1\}//' || true
    exit 0
    ;;
  # A join token must never come back in a message: a stray one is named by its shape.
  cvjn_*) refuse "unexpected argument (it looks like a join token: give it as --join TOKEN)" ;;
  *) refuse "unknown option $1 (see --help)" ;;
  esac
done

[ "$first_flag" = 0 ] || [ -z "$join_token" ] || refuse "--first and --join are for different servers: the first one, or one that joins it"
[ "$leave" = 0 ] || [ "$uninstall" = 0 ] || refuse "--leave is for a joined server and --uninstall for the first one: not both"

# Which server this is. An install.env without a role was written before servers could join.
recorded_role=""
if [ -f "$STATE/install.env" ]; then
  recorded_role="$(env_value "$STATE/install.env" CARAVANA_ROLE)"
  recorded_role="${recorded_role:-first}"
fi
if [ -n "$join_token" ]; then
  role="join"
elif [ "$first_flag" = 1 ]; then
  role="first"
else
  role="${recorded_role:-first}"
fi
if [ "$leave" = 0 ] && [ "$uninstall" = 0 ] && [ -n "$recorded_role" ] && [ "$role" != "$recorded_role" ]; then
  case "$recorded_role" in
  first) refuse "this is the first server of its cluster, which runs Caravana: --join is for another server" ;;
  *) refuse "this server joined a cluster: --first is for the one server that runs Caravana" ;;
  esac
fi
if [ "$uninstall" = 1 ] && [ "$recorded_role" = join ]; then
  refuse "this server joined a cluster: --uninstall removes Caravana from the first server. Use --leave to take this one out"
fi
# Each role has its own options: a wrong one is a mistake, not something to ignore.
if [ "$role" = join ]; then
  { [ -z "$email" ] && [ -z "$domain" ] && [ -z "$acme" ] && [ -z "$acme_email" ]; } ||
    refuse "--email, --domain, --acme and --acme-email are for the first server; this one joins"
else
  { [ -z "$url" ] && [ -z "$insecure" ]; } || refuse "--url and --insecure are for --join: the first server has no console to call"
fi

# Options not given on this run come from the last one.
if [ -f "$STATE/install.env" ]; then
  # Read as text, never sourced: an older installer wrote these values unquoted and unchecked.
  email="${email:-$(env_value "$STATE/install.env" CARAVANA_ADMIN_EMAIL)}"
  domain="${domain:-$(env_value "$STATE/install.env" CARAVANA_DOMAIN)}"
  acme="${acme:-$(env_value "$STATE/install.env" CARAVANA_ACME)}"
  acme_email="${acme_email:-$(env_value "$STATE/install.env" CARAVANA_ACME_EMAIL)}"
  address="${address:-$(env_value "$STATE/install.env" CARAVANA_ADDRESS)}"
  if [ "$role" = join ]; then
    url="${url:-$(env_value "$STATE/install.env" CARAVANA_URL)}"
    insecure="${insecure:-$(env_value "$STATE/install.env" CARAVANA_INSECURE)}"
  fi
fi
acme="${acme:-production}"
acme_email="${acme_email:-$email}"
insecure="${insecure:-0}"
url="${url%/}"

# ---- preflight: nothing changes until every check passes ------------------------------------

# is_hostname NAME: dot-separated labels of letters, digits and dashes (none at either end), at
# least two labels, at most 253 characters.
is_hostname() {
  local label='[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?'
  local re="^${label}(\\.${label})+\$"
  [ "${#1}" -le 253 ] && [[ "$1" =~ $re ]]
}

# valid_url URL: https:// and a host name with an optional port, or http:// for 127.0.0.1 and
# localhost only. No path, no credentials.
valid_url() {
  local u="${1%/}" hostport host port=""
  case "$u" in
  https://*) hostport="${u#https://}" ;;
  http://*) hostport="${u#http://}" ;;
  *) return 1 ;;
  esac
  [[ "$hostport" != */* ]] || return 1
  host="${hostport%%:*}"
  if [[ "$hostport" == *:* ]]; then
    port="${hostport#*:}"
    [[ "$port" =~ ^[0-9]{1,5}$ ]] && [ "$((10#$port))" -ge 1 ] && [ "$((10#$port))" -le 65535 ] || return 1
  fi
  if [[ "$u" == https://* ]]; then
    is_hostname "$host"
  else
    [ "$host" = 127.0.0.1 ] || [ "$host" = localhost ]
  fi
}

# valid_address ADDR: an IPv4 or IPv6 literal that other servers could reach: not loopback,
# unspecified, link-local or multicast.
valid_address() {
  local a="$1" o o1 o2
  if [[ "$a" =~ ^([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})$ ]]; then
    o1="${BASH_REMATCH[1]}" o2="${BASH_REMATCH[2]}"
    for o in "$o1" "$o2" "${BASH_REMATCH[3]}" "${BASH_REMATCH[4]}"; do
      [[ "$o" =~ ^(0|[1-9][0-9]*)$ ]] && [ "$o" -le 255 ] || return 1
    done
    [ "$o1" -ne 0 ] && [ "$o1" -ne 127 ] && [ "$o1" -lt 224 ] && ! { [ "$o1" -eq 169 ] && [ "$o2" -eq 254 ]; }
  elif [[ "$a" =~ ^[0-9A-Fa-f:]+$ ]] && [[ "$a" == *:*:* ]] && [ "${#a}" -le 39 ]; then
    case "${a,,}" in
    :: | ::1 | fe[89ab][0-9a-f]:* | ff[0-9a-f][0-9a-f]:*) return 1 ;;
    esac
    getent ahostsv6 "$a" >/dev/null 2>&1
  else
    return 1
  fi
}

# ip_int A.B.C.D: the address as a number.
ip_int() {
  local a b c d
  IFS=. read -r a b c d <<<"$1"
  echo $(((10#$a << 24) | (10#$b << 16) | (10#$c << 8) | 10#$d))
}

# inside ADDR LEN NET NETLEN: ADDR/LEN lies inside NET/NETLEN.
inside() {
  local mask
  [ "$2" -ge "$4" ] || return 1
  mask=$(((0xFFFFFFFF << (32 - $4)) & 0xFFFFFFFF))
  [ $(($(ip_int "$1") & mask)) -eq "$(ip_int "$3")" ]
}

# check_private_ranges: Caravana's servers use 10.97.0.0/16, 10.98.0.0/24 and 10.99.0.0/24 between
# them. A host that already has an address or a route inside one of them, on an interface that
# isn't ours, would send that traffic the wrong way.
check_private_ranges() {
  local ifname cidr dst dev rest net len prefix found=""
  while read -r _ ifname _ cidr _; do
    case "$ifname" in caravana0 | caravana-mesh | nomad) continue ;; esac
    [[ "$cidr" =~ ^([0-9.]+)/([0-9]+)$ ]] || continue
    for prefix in 10.97.0.0/16 10.98.0.0/24 10.99.0.0/24; do
      inside "${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}" "${prefix%/*}" "${prefix#*/}" && found="the address $cidr on $ifname"
    done
  done < <(ip -4 -o addr show 2>/dev/null)
  while read -r dst rest; do
    [ "$dst" != default ] || continue
    [[ "$dst" == */* ]] || dst="$dst/32"
    net="${dst%/*}" len="${dst#*/}"
    [[ "$net" =~ ^[0-9.]+$ ]] && [[ "$len" =~ ^[0-9]+$ ]] || continue
    dev="$(sed -n 's/.* dev \([^ ]*\).*/\1/p' <<<" $rest")"
    case "$dev" in caravana0 | caravana-mesh | nomad) continue ;; esac
    for prefix in 10.97.0.0/16 10.98.0.0/24 10.99.0.0/24; do
      inside "$net" "$len" "${prefix%/*}" "${prefix#*/}" && found="the route $dst on ${dev:-an unnamed interface}"
    done
  done < <(ip -4 route show 2>/dev/null)
  [ -z "$found" ] ||
    refuse "this host has $found, which is inside 10.97.0.0/16, 10.98.0.0/24 or 10.99.0.0/24: Caravana's servers use those between them. Change that network first"
}

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
  # The values end up in files the shell and systemd read, in a Let's Encrypt account and on the
  # agent's command line: refuse anything that isn't plain, before anything changes.
  if [ -n "$address" ]; then
    valid_address "$address" || refuse "--address is not an IPv4 or IPv6 address that other servers could reach (not loopback, unspecified, link-local or multicast)"
  fi
  if [ "$role" = join ]; then
    preflight_join
  else
    preflight_first
  fi

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
  check_private_ranges
}

preflight_first() {
  case "$acme" in production | staging | none) ;; *) refuse "--acme is production, staging or none, not $acme" ;; esac
  [ -n "$email" ] || refuse "--email is required the first time: the admin signs in with it"
  local addr_re='^[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+$'
  [[ "$email" =~ $addr_re ]] || refuse "--email is not a plain email address (letters, digits and . _ % + - before one @, then a host name)"
  [[ "$acme_email" =~ $addr_re ]] || refuse "--acme-email is not a plain email address (letters, digits and . _ % + - before one @, then a host name)"
  if [ -n "$domain" ]; then
    is_hostname "$domain" ||
      refuse "--domain is not a host name (labels of letters, digits and dashes, no dash at either end, at least one dot, at most 253 characters)"
  fi

  local port holder
  for port in 80 443; do
    holder="$(ss -ltnpH "sport = :$port" 2>/dev/null | head -1)"
    if [ -n "$holder" ] && ! grep -q '"traefik"' <<<"$holder"; then
      refuse "port $port is in use ($(grep -o 'users:(("[^"]*"' <<<"$holder" | cut -d'"' -f2 || echo "something else")); Traefik needs 80 and 443"
    fi
  done

  # The public IP names this server to the others (--address) and the console's domain to Let's
  # Encrypt. With neither needing it, no lookup is made.
  if [ -z "$domain" ] || [ "$acme" != none ] || [ -z "$address" ]; then
    public_ip="$(curl -fsS --max-time 10 https://checkip.amazonaws.com | tr -d '[:space:]')" ||
      refuse "can't find this machine's public IP address (https://checkip.amazonaws.com); give --address (and --domain with --acme none) to go without it"
    valid_address "$public_ip" || refuse "https://checkip.amazonaws.com didn't answer an IP address"
  fi
  domain="${domain:-${public_ip//./-}.sslip.io}"
  address="${address:-$public_ip}"
  if [ "$acme" != none ]; then
    local resolved
    resolved="$(getent ahostsv4 "$domain" | awk 'NR == 1 {print $1}')"
    [ "$resolved" = "$public_ip" ] ||
      refuse "$domain resolves to ${resolved:-nothing}, not this machine ($public_ip); Let's Encrypt can't issue its certificate until it does"
  fi
}

preflight_join() {
  # A server that joined already needs neither; a first run needs both.
  if [ ! -f "$NODE_ENV" ]; then
    [ -n "$join_token" ] || refuse "--join TOKEN is required: make one in the console (Servers, Add a server), which shows the whole command"
    [ -n "$url" ] || refuse "--url is required with --join: the console's address, https://caravana.example.com"
  fi
  if [ -n "$join_token" ]; then
    [[ "$join_token" =~ ^cvjn_[A-Za-z0-9_-]{43}$ ]] || refuse "--join is not a join token: it is cvjn_ and 43 letters, digits, - or _. Copy the command from the console"
  fi
  if [ -n "$url" ]; then
    valid_url "$url" || refuse "--url must be https:// and the console's host name, with an optional port (http:// only for localhost), no path"
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
  for pkg in docker.io unzip curl ca-certificates ufw wireguard-tools; do
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

# install_nomad_binaries: Nomad, the CNI plugins and the bridge sysctl, for either role.
install_nomad_binaries() {
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

  # A container's interface keeps the MAC address it was made with. systemd's default link policy
  # gives a new veth a "persistent" address a moment after the network plugin made it and wrote its
  # address down. Nomad checks each copy's network when its agent restarts, finds an address it
  # doesn't know ("Mac doesn't match") and replaces the copy (research L17). Copies made before
  # this file existed can still be replaced by the next Nomad restart; copies made after can't.
  if write_if_changed /etc/systemd/network/10-caravana-veth.link 0644 veth-mac <<'EOF'; then
[Match]
Driver=veth

[Link]
MACAddressPolicy=none
EOF
    udevadm control --reload
  fi
}

# private_interface_first: caravana0 on the first server, made by systemd-networkd as before. The
# agent makes it too when it is missing (and the mesh unit runs first at boot), so a joined server,
# whose address is 10.99.0.N, has no networkd files.
private_interface_first() {
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
}

# install_agent: the server agent (a copy of the release's caravana, so a rolled-back Caravana never
# takes its commands away) and the units of the private network, Nomad and the agent's timer.
units_changed=0
install_agent() {
  step "the server agent and its units"
  local src="$OPT/$CARAVANA_VERSION" f
  for f in caravana nomad.service $MESH_UNITS; do
    [ -f "$src/$f" ] || fail "$src has no $f"
  done
  units_changed=0
  write_if_changed "$AGENT" 0755 agent <"$src/caravana" || true
  write_if_changed /etc/systemd/system/nomad.service 0644 nomad-unit <"$src/nomad.service" && units_changed=1
  for f in $MESH_UNITS; do
    write_if_changed "/etc/systemd/system/$f" 0644 "$f" <"$src/$f" && units_changed=1
  done
  [ "$units_changed" = 0 ] || systemctl daemon-reload
  systemctl enable caravana-mesh.service >/dev/null 2>&1
}

# cpu_flags: the agent's --cpu-total-compute, for VMs that report no clock speed (ARM ones often
# don't: Nomad then sees 0 MHz and places nothing).
cpu_flags() {
  if ! lscpu | grep -qi 'mhz'; then
    echo "--cpu-total-compute $(($(nproc) * 2000))"
  fi
}

# nomad_sum: what decides that Nomad must restart: its binary, its unit, its config and its
# certificates.
nomad_sum() {
  {
    cat /usr/local/bin/nomad /etc/systemd/system/nomad.service
    find /etc/nomad.d -type f -print0 | sort -z | xargs -0 cat
  } | sha256sum
}

# restart_nomad_if_changed: restart Nomad only when its binary, unit, config or certificates changed
# (or it isn't running), so a rerun leaves it alone. Returns 0 when it restarted, 1 when it didn't.
restart_nomad_if_changed() {
  local sum
  sum="$(nomad_sum)"
  if [ "$(cat "$STATE/nomad.sum" 2>/dev/null)" != "$sum" ] || ! systemctl is-active --quiet nomad; then
    systemctl enable nomad >/dev/null 2>&1
    systemctl restart nomad
    echo "$sum" >"$STATE/nomad.sum"
    changed+=(nomad)
    return 0
  fi
  return 1
}

# wait_leader SECONDS: wait until Nomad knows a leader.
wait_leader() {
  # shellcheck disable=SC2016 # expanded by the inner bash, on each try
  timeout "${1}s" bash -c 'until [ -n "$(curl -fsS http://127.0.0.1:4646/v1/status/leader 2>/dev/null | tr -d "\"")" ]; do sleep 2; done'
}

# no_leader WHAT: Nomad has no leader 2 minutes after WHAT was done to it. During the conversion
# of an existing host the message says what was done and where the old config is.
converting=0
no_leader() {
  local msg="Nomad didn't elect a leader within 2 minutes${1:+ $1} (journalctl -u nomad)"
  if [ "$converting" = 1 ]; then
    msg="$msg. This host's Nomad was moving to the private network: its old config is $STATE/nomad.hcl.before-mesh (put it back as /etc/nomad.d/nomad.hcl and run: systemctl restart nomad); its jobs, tokens and variables are still in /opt/nomad/data"
  fi
  fail "$msg"
}

# keep_old_nomad_config: the first conversion keeps the config an older installer wrote, once, here
# and not in /etc/nomad.d, which Nomad reads whole.
keep_old_nomad_config() {
  local conf=/etc/nomad.d/nomad.hcl
  [ -f "$conf" ] || return 0
  [ ! -f "$STATE/nomad.hcl.before-mesh" ] || return 0
  # shellcheck disable=SC2016 # the backticks are in the file's header
  head -1 "$conf" | grep -q '^# Written by `caravana node`' && return 0
  install -m 0600 "$conf" "$STATE/nomad.hcl.before-mesh"
  step "the config this host's Nomad had is kept as $STATE/nomad.hcl.before-mesh"
}

# convert_raft_once: Nomad's raft on a host installed before the private network still lists this
# server at 127.0.0.1:4647, and a second server could never reach it. Once per host, after Nomad runs
# with the new config, Nomad is stopped, told its one peer's new address (peers.json, Nomad's outage
# recovery: research L2) and started again. Its state is kept.
convert_raft_once() {
  [ ! -f "$STATE/raft-mesh" ] || return 0
  if [ "$raft_before" != 1 ]; then
    echo mesh >"$STATE/raft-mesh"
    return 0
  fi
  local peers node_id
  peers="$(curl -fsS http://127.0.0.1:4646/v1/status/peers 2>/dev/null || true)"
  if grep -q '"10.98.0.1:4647"' <<<"$peers"; then
    echo mesh >"$STATE/raft-mesh"
    return 0
  fi
  step "Nomad's raft peer moves to 10.98.0.1:4647 (once per host)"
  node_id="$(cat /opt/nomad/data/server/node-id 2>/dev/null || true)"
  [[ "$node_id" =~ ^[0-9a-fA-F-]{36}$ ]] ||
    fail "Nomad's node ID isn't in /opt/nomad/data/server/node-id, so its raft peer can't be moved. Nomad runs with the private network's config; its old config is $STATE/nomad.hcl.before-mesh"
  systemctl stop nomad
  printf '[{"id": "%s", "address": "10.98.0.1:4647", "non_voter": false}]\n' "$node_id" >/opt/nomad/data/server/raft/peers.json
  systemctl start nomad
  changed+=(nomad-raft)
  wait_leader 120 || no_leader "after its raft peer was set to 10.98.0.1:4647 (peers.json) and it was started again"
  peers="$(curl -fsS http://127.0.0.1:4646/v1/status/peers 2>/dev/null || true)"
  grep -q '"10.98.0.1:4647"' <<<"$peers" ||
    fail "Nomad's raft still doesn't list 10.98.0.1:4647 after the recovery (sudo caravana-nomad operator raft list-peers; journalctl -u nomad). Its old config is $STATE/nomad.hcl.before-mesh"
  echo mesh >"$STATE/raft-mesh"
}

# init_first_server: the agent makes server 1 offline: keys, the cluster's CA and this server's
# certificate, Nomad's config and the private network. The old config is kept first.
raft_before=0
init_first_server() {
  step "the first server: private network, cluster certificates, Nomad's config"
  [ ! -d /opt/nomad/data/server/raft ] || raft_before=1
  keep_old_nomad_config
  if [ "$raft_before" = 1 ] && [ ! -f "$STATE/raft-mesh" ]; then
    converting=1
  elif [ "$raft_before" = 0 ] && [ ! -f "$STATE/raft-mesh" ]; then
    echo mesh >"$STATE/raft-mesh" # a fresh host starts on the mesh address: nothing to convert
  fi
  local flags=()
  # shellcheck disable=SC2207 # two words, no spaces inside them
  flags=($(cpu_flags))
  "$AGENT" node init-first --address "$address" "${flags[@]}" ||
    fail "the server agent couldn't set this server up (see above)"
  systemctl start caravana-mesh.service
}

# nomad_first: Nomad restarts when its binary, unit, config or certificates changed; on a host
# installed before the private network the raft peer is moved once.
nomad_first() {
  step "Nomad (first server, ACLs on)"
  mkdir -p "$STATE/traefik" /opt/nomad/data /etc/nomad.d
  chmod 700 "$STATE"
  if [ "$converting" = 1 ]; then
    echo "  Nomad restarts twice, once only: this host's Nomad moves to the private network (jobs, tokens and"
    echo "  variables are kept, apps keep running). The old config is kept as $STATE/nomad.hcl.before-mesh"
    echo "  A copy whose network Nomad can't verify after its restart is replaced: an app with one copy"
    echo "  may not answer for a few seconds."
  fi
  if restart_nomad_if_changed; then
    wait_leader 120 || no_leader "after it was restarted with the private network's config"
  fi
  convert_raft_once
  wait_leader 120 || no_leader

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

  configure_firewall first

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

# configure_firewall ROLE: only SSH, WireGuard's port and, on the first server, HTTP and HTTPS from
# the internet; everything the other servers send over the mesh is allowed.
configure_firewall() {
  local before
  if [ "$1" = first ]; then
    step "firewall: only 22, 80, 443 and 51820/udp (the mesh) from the internet"
  else
    step "firewall: only 22 and 51820/udp (the mesh) from the internet"
  fi
  before="$(ufw status verbose 2>/dev/null || true)"
  ufw allow 22/tcp >/dev/null
  if [ "$1" = first ]; then
    ufw allow 80/tcp >/dev/null
    ufw allow 443/tcp >/dev/null
  fi
  ufw allow 51820/udp >/dev/null
  ufw allow in on caravana-mesh >/dev/null
  ufw default deny incoming >/dev/null
  ufw default allow outgoing >/dev/null
  # Containers on Nomad's bridge need forwarding; inbound exposure is handled by caravana0.
  ufw default allow routed >/dev/null
  ufw --force enable >/dev/null
  [ "$before" = "$(ufw status verbose)" ] || changed+=(firewall)
}

# store_cluster_secrets: the cluster's CA key lives in a Nomad Variable, never on disk beyond this
# run, and so does the gossip key (constitution VII). A failure stops the install with the key
# still on disk.
store_cluster_secrets() {
  local ca_key="$AGENT_DIR/ca-key.pem" ca=/etc/nomad.d/tls/ca.pem
  if [ -f "$ca_key" ]; then
    step "the cluster's CA key goes into a Nomad Variable (caravana/cluster/ca)"
    nomad_cli var put -force caravana/cluster/ca cert=@"$ca" key=@"$ca_key" >/dev/null 2>&1 ||
      fail "storing the cluster's CA in the Nomad Variable caravana/cluster/ca failed; its key is still in $ca_key"
    if ! { cmp -s <(nomad_cli var get -item key caravana/cluster/ca) "$ca_key" &&
      cmp -s <(nomad_cli var get -item cert caravana/cluster/ca) "$ca"; }; then
      fail "the Nomad Variable caravana/cluster/ca doesn't read back as the CA that was stored; its key is still in $ca_key"
    fi
    shred -u "$ca_key"
    changed+=(cluster-ca)
  fi
  if ! nomad_cli var get -item key caravana/cluster/gossip >/dev/null 2>&1; then
    step "the gossip key goes into a Nomad Variable (caravana/cluster/gossip)"
    # The value goes in as JSON on standard input: no key on a command line, no trailing newline.
    printf '{"Items":{"key":"%s"}}\n' "$(tr -d '[:space:]' <"$AGENT_DIR/gossip.key")" |
      nomad_cli var put -force -in json caravana/cluster/gossip - >/dev/null 2>&1 ||
      fail "storing the gossip key in the Nomad Variable caravana/cluster/gossip failed"
    changed+=(cluster-gossip)
  fi
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
  # Feature 023: Team only's password hashes reach Traefik through its file provider, which
  # watches a directory instead of one file. An existing Traefik job still has the file, so
  # this run restarts it once.
  if [ "$before" != none ]; then
    local spec
    spec="$(nomad_cli job inspect traefik 2>/dev/null || true)"
    grep -q 'providers.file.directory' <<<"$spec" ||
      echo "  Traefik is restarted once for the file provider (a few seconds without HTTP)"
    # Feature 025: Monitoring reads Traefik's request numbers (Prometheus text on its loopback
    # entrypoint). An existing Traefik job doesn't have the two metrics arguments yet.
    grep -q 'metrics.prometheus' <<<"$spec" ||
      echo "  Traefik is restarted once for its request numbers (a few seconds without HTTP)"
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
  local token resolver=letsencrypt docker_root
  token="$(env_value "$ENV_FILE" NOMAD_TOKEN)"
  if [ -z "$token" ]; then
    token="$(nomad_cli acl token create -name caravana -policy caravana -t '{{.SecretID}}')"
  fi
  [ "$acme" = none ] && resolver=""
  # Feature 025: Monitoring watches the disk Docker's images and layers live on. Docker says where
  # that is; Caravana can't ask it (it has no Docker access), so the installer writes it down.
  docker_root="$(docker info --format '{{.DockerRootDir}}' 2>/dev/null || true)"
  [ -n "$docker_root" ] || docker_root=/var/lib/docker
  # Traefik, on this host's loopback, is the only proxy: Caravana believes its forwarded
  # headers, so cookies are Secure and sign-in limits count each real client. The public URL
  # is the console's own address, which notifications link to (feature 018). The certificate
  # settings are shown read-only on Settings, and the edge address is where Caravana connects
  # to read each domain's certificate and test its route (feature 023): Traefik, on loopback.
  service_changed=0
  write_if_changed "$ENV_FILE" 0600 caravana-env <<EOF && service_changed=1
CARAVANA_LISTEN=127.0.0.1:7070
CARAVANA_DATA_DIR=$(envq "$DATA")
CARAVANA_CERT_RESOLVER=$(envq "$resolver")
CARAVANA_TRUSTED_PROXIES=127.0.0.1,::1
CARAVANA_PUBLIC_URL=$(envq "https://$domain")
CARAVANA_ACME=$(envq "$acme")
CARAVANA_ACME_EMAIL=$(envq "$acme_email")
CARAVANA_EDGE_ADDRESS=127.0.0.1:443
CARAVANA_DOCKER_ROOT=$(envq "$docker_root")
NOMAD_ADDR=http://127.0.0.1:4646
NOMAD_TOKEN=$(envq "$token")
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

# install_updater: the root updater and its timer (feature 030). The console can't be root, so
# it writes a request (a version and nothing else) into its own data directory, and the updater
# checks that request and runs this installer from a checksum-verified release. It is installed
# before Caravana starts, so the timer exists when the new version runs. The status and logs it
# writes are in UPDATE_DIR, which Caravana can read but never write.
install_updater() {
  step "the updater (a root timer that runs an update the console asked for)"
  local src="$OPT/$CARAVANA_VERSION" f units_changed=0
  for f in caravana-update.sh caravana-update.service caravana-update.timer; do
    [ -f "$src/$f" ] || fail "$src has no $f"
  done
  install -d -m 0755 "$UPDATE_DIR"
  install -d -m 0700 "$UPDATE_DIR/logs"
  # The marker tells Caravana the updater is installed. Its status file is the updater's to write.
  echo 1 | write_if_changed "$UPDATE_DIR/updater" 0644 updater || true
  write_if_changed "$UPDATER" 0755 updater <"$src/caravana-update.sh" || true
  write_if_changed /etc/systemd/system/caravana-update.service 0644 updater <"$src/caravana-update.service" && units_changed=1
  write_if_changed /etc/systemd/system/caravana-update.timer 0644 updater <"$src/caravana-update.timer" && units_changed=1
  [ "$units_changed" = 0 ] || systemctl daemon-reload
  systemctl enable --now caravana-update.timer >/dev/null 2>&1
}

# point_current VERSION: make "current" point at VERSION, in one step.
point_current() {
  ln -sfn "$1" "$OPT/current.new"
  mv -T "$OPT/current.new" "$OPT/current"
}

# prune_versions OLD: keep this version and the one before it. "current" is a link, not a version.
prune_versions() {
  local dir
  for dir in "$OPT"/*; do
    if [ -d "$dir" ] && [ ! -L "$dir" ]; then
      case "$(basename "$dir")" in "$CARAVANA_VERSION" | "$1") ;; *) rm -rf "$dir" ;; esac
    fi
  done
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
    prune_versions "$installed"
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

# record_options: what a later run reuses. Never a token: a join token is for one use.
record_options() {
  if [ "$role" = join ]; then
    write_if_changed "$STATE/install.env" 0600 options <<EOF || true
CARAVANA_ROLE=join
CARAVANA_URL=$(envq "$url")
CARAVANA_INSECURE=$(envq "$insecure")
CARAVANA_ADDRESS=$(envq "$address")
EOF
  else
    write_if_changed "$STATE/install.env" 0600 options <<EOF || true
CARAVANA_ADMIN_EMAIL=$(envq "$email")
CARAVANA_DOMAIN=$(envq "$domain")
CARAVANA_ACME=$(envq "$acme")
CARAVANA_ACME_EMAIL=$(envq "$acme_email")
CARAVANA_ROLE=first
CARAVANA_ADDRESS=$(envq "$address")
EOF
  fi
}

# register_first_server: Caravana learns about this server (it is server 1) and hands the agent a
# node token to sync with. The token is printed once by the CLI and goes only into node.env.
register_first_server() {
  [ ! -f "$NODE_ENV" ] || return 0
  step "register this server with Caravana"
  local name key out token
  name="$("$AGENT" node show name)"
  key="$("$AGENT" node show public-key)"
  # The CLI opens Caravana's store beside the running server, as `admin init` does. A Caravana
  # that doesn't know the command would start as a server instead, so the call has a time limit.
  if ! out="$(umask 077 && timeout 60 /usr/local/bin/caravana server adopt-first --name "$name" --public-key "$key" --address "$address" 2>"$STATE/adopt.err")"; then
    tail -5 "$STATE/adopt.err" >&2
    fail "registering this server with Caravana failed: 'caravana server adopt-first' exited with an error. Does Caravana $CARAVANA_VERSION have that command? The server is set up; run the installer again once it does"
  fi
  token="$out"
  [[ "$token" =~ ^cvnd_[A-Za-z0-9_-]{43}$ ]] ||
    fail "'caravana server adopt-first' didn't print a node token, so this server can't sync with Caravana yet. Does Caravana $CARAVANA_VERSION have that command? Run the installer again once it does"
  (
    umask 077
    printf 'CARAVANA_URL=%s\nCARAVANA_NODE_TOKEN=%s\nCARAVANA_INSECURE=0\n' "$(envq http://127.0.0.1:7070)" "$(envq "$token")" >"$NODE_ENV.tmp"
  )
  mv "$NODE_ENV.tmp" "$NODE_ENV"
  rm -f "$STATE/adopt.err"
  changed+=(server-registered)
}

install_caravana() {
  preflight
  mkdir -p "$STATE" && chmod 700 "$STATE"
  record_options # from here on, this installer owns Nomad and Docker on this host
  step "Caravana $CARAVANA_VERSION release files"
  command -v curl >/dev/null || { apt-get update -qq && apt-get install -y -qq curl >/dev/null; }
  fetch_release
  install_packages
  install_nomad_binaries
  private_interface_first
  install_agent
  init_first_server
  nomad_first
  store_cluster_secrets
  configure_caravana
  install_updater
  start_version
  ensure_admin
  register_first_server
  systemctl enable --now caravana-node.timer >/dev/null 2>&1
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
  echo "  Servers:  add another one in the console (Servers, Add a server); it shows the command"
  echo "  Nomad:    sudo caravana-nomad status"
}

# ---- another server ---------------------------------------------------------------------------

# join_cluster: the agent calls the console with the join token (the one place it goes: its own
# command line, once) and writes the server's keys, certificate and config. A server that joined
# already has node.env: the agent then only renders its config and brings the network up, with no
# call.
join_cluster() {
  local args=(node join)
  if [ ! -f "$NODE_ENV" ]; then
    step "join the cluster of $url"
    args+=(--url "$url" --token "$join_token")
    [ -z "$address" ] || args+=(--address "$address")
    [ "$insecure" != 1 ] || args+=(--insecure)
  else
    step "this server has joined already: its config and network are made again"
  fi
  # shellcheck disable=SC2207 # two words, no spaces inside them
  args+=($(cpu_flags))
  "$AGENT" "${args[@]}" ||
    fail "the join didn't go through (see above), so this server is not part of the cluster. Make a new command in the console and run it again"
  systemctl start caravana-mesh.service
}

# start_node_timer: the agent's timer runs, so the server keeps in line with the console. It is
# started after the installer's own checks, which would otherwise run beside it.
start_node_timer() { systemctl start caravana-node.timer >/dev/null 2>&1 || true; }

# wait_joined: sync every 5 seconds, up to 3 minutes, and print each check as the console reports it
# passed. A server the console says is ready has joined.
wait_joined() {
  step "waiting for the console to confirm this server (up to 3 minutes)"
  local end=$((SECONDS + 180)) out line rest cname word detail state shown=" " open="" name
  while :; do
    out="$(umask 077 && "$AGENT" node sync --checks 2>"$STATE/sync.err" || true)"
    state="" open=""
    while IFS= read -r line; do
      case "$line" in
      "check "*)
        rest="${line#check }"
        cname="${rest%% *}"
        rest="${rest#"$cname"}"
        rest="${rest# }"
        word="${rest%% *}"
        detail="${rest#"$word"}"
        detail="${detail# }"
        if [ "$word" = ok ]; then
          case "$shown" in *" $cname "*) ;; *)
            echo "  ok: $detail"
            shown="$shown$cname "
            ;;
          esac
        else
          open="$open  open: $detail"$'\n'
        fi
        ;;
      "state "*) state="${line#state }" ;;
      esac
    done <<<"$out"
    [ "$state" != ready ] || break
    if [ "$SECONDS" -ge "$end" ]; then
      start_node_timer
      if [ -n "$open" ]; then
        printf '%s' "$open" >&2
      else
        echo "  the console didn't answer: $(tail -1 "$STATE/sync.err" 2>/dev/null)" >&2
      fi
      fail "this server hasn't joined after 3 minutes: the checks above are still open. It keeps trying every 30 seconds; the console's Servers page shows them too"
    fi
    sleep 5
  done
  start_node_timer
  name="$("$AGENT" node show name)"
  echo
  echo "This server joined as $name."
}

install_join() {
  preflight
  mkdir -p "$STATE" && chmod 700 "$STATE"
  record_options
  step "Caravana $CARAVANA_VERSION release files"
  command -v curl >/dev/null || { apt-get update -qq && apt-get install -y -qq curl >/dev/null; }
  fetch_release
  install_packages
  install_nomad_binaries
  mkdir -p /opt/nomad/data /etc/nomad.d
  install_agent
  local installed
  installed="$(installed_version)"
  point_current "$CARAVANA_VERSION"
  [ "$installed" = "$CARAVANA_VERSION" ] || { [ -z "$installed" ] || prune_versions "$installed"; }
  join_cluster
  step "Nomad (joined server)"
  restart_nomad_if_changed || true
  configure_firewall join
  systemctl enable caravana-node.timer >/dev/null 2>&1
  wait_joined
  echo "changed: $(printf '%s\n' "${changed[@]}" | awk 'NF && !seen[$0]++' | paste -sd ' ' -)"
}

# leave_server: this joined server goes out of the cluster on its own side. Docker and the
# packages stay; the console still lists the server until it is removed there.
leave_server() {
  [ "$(id -u)" = 0 ] || refuse "run it as root (sudo bash ...)"
  case "$recorded_role" in
  join) ;;
  first) refuse "this is the first server, which runs Caravana: --leave is for a server that joined. Use --uninstall to remove Caravana" ;;
  *)
    echo "This server hasn't joined a cluster; nothing to leave."
    exit 0
    ;;
  esac
  step "leave the cluster, then stop the agent and the private network"
  systemctl disable --now caravana-node.timer >/dev/null 2>&1 || true
  # The agent goes first: it asks Nomad to leave the cluster (SIGTERM) and waits for it, so a Nomad
  # server goes out of raft while it can still reach the others. Stopping the mesh unit first would
  # stop Nomad with it (Nomad requires that unit) by SIGINT, which leaves nothing: in a cluster with
  # two Nomad servers the other one then has no quorum (research L16).
  if [ -x "$AGENT" ]; then
    "$AGENT" node leave || fail "the agent couldn't take this server out of the cluster (see above)"
  fi
  systemctl disable --now caravana-mesh.service >/dev/null 2>&1 || true
  step "remove the agent and its units"
  local f
  for f in $MESH_UNITS nomad.service; do rm -f "/etc/systemd/system/$f"; done
  rm -f "$AGENT" /etc/nomad.d/nomad.hcl "$STATE/nomad.sum"
  systemctl daemon-reload
  systemctl reset-failed nomad.service caravana-mesh.service >/dev/null 2>&1 || true
  echo
  echo "This server left the cluster. Nomad is stopped and the private network and the agent are gone."
  echo "  Remove it in the console too (Servers), or it stays there as down."
  echo "  Docker, Nomad's binary and any containers the apps left running are still here (docker ps)."
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
  # The updater goes with it: without Caravana there is nothing to ask for an update.
  systemctl disable --now caravana-update.timer >/dev/null 2>&1 || true
  rm -f /etc/systemd/system/caravana-update.timer /etc/systemd/system/caravana-update.service "$UPDATER"
  rm -rf "$UPDATE_DIR"
  # So does the agent's timer: without Caravana there is nothing to sync with. The private network
  # and Nomad stay, with the apps; so do the agent and its units, which they need.
  systemctl disable --now caravana-node.timer >/dev/null 2>&1 || true
  rm -f "$NODE_ENV"
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
elif [ "$leave" = 1 ]; then
  leave_server
elif [ "$role" = join ]; then
  install_join
else
  install_caravana
fi
