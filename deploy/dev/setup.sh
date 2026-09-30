#!/bin/bash
# Turns a fresh Ubuntu VM into a single-node Nomad host: Docker, CNI plugins, bridge networking,
# Nomad under systemd with ACLs bootstrapped. Runs as root on every boot, so every step is
# idempotent. A prototype of the one-command installer (plan item 012).
set -eux -o pipefail

NOMAD_VERSION=2.0.7
CNI_VERSION=1.9.1
ARCH="$(dpkg --print-architecture)" # arm64 or amd64, same names Nomad and CNI use
STATE=/var/lib/caravana-lab

export DEBIAN_FRONTEND=noninteractive

# Packages: Docker Engine and the tools used below.
if ! command -v docker >/dev/null || ! command -v unzip >/dev/null; then
  apt-get update
  apt-get install -y docker.io unzip curl ca-certificates
fi
systemctl enable --now docker

# Nomad binary, verified against HashiCorp's published checksums.
if [ "$(nomad version 2>/dev/null | head -1)" != "Nomad v${NOMAD_VERSION}" ]; then
  tmp="$(mktemp -d)"
  base="https://releases.hashicorp.com/nomad/${NOMAD_VERSION}"
  curl -fsSL -o "$tmp/nomad_${NOMAD_VERSION}_linux_${ARCH}.zip" "$base/nomad_${NOMAD_VERSION}_linux_${ARCH}.zip"
  curl -fsSL -o "$tmp/SHA256SUMS" "$base/nomad_${NOMAD_VERSION}_SHA256SUMS"
  (cd "$tmp" && sha256sum --ignore-missing -c SHA256SUMS)
  unzip -o "$tmp/nomad_${NOMAD_VERSION}_linux_${ARCH}.zip" -d /usr/local/bin
  rm -rf "$tmp"
fi

# CNI plugins, needed for Nomad bridge networking.
if [ "$(cat /opt/cni/bin/.version 2>/dev/null)" != "$CNI_VERSION" ]; then
  tmp="$(mktemp -d)"
  file="cni-plugins-linux-${ARCH}-v${CNI_VERSION}.tgz"
  base="https://github.com/containernetworking/plugins/releases/download/v${CNI_VERSION}"
  curl -fsSL -o "$tmp/$file" "$base/$file"
  curl -fsSL -o "$tmp/$file.sha256" "$base/$file.sha256"
  (cd "$tmp" && sha256sum -c "$file.sha256")
  mkdir -p /opt/cni/bin
  tar -xzf "$tmp/$file" -C /opt/cni/bin
  echo "$CNI_VERSION" >/opt/cni/bin/.version
  rm -rf "$tmp"
fi

# Bridge traffic must pass through iptables so Nomad's port mappings work.
echo br_netfilter >/etc/modules-load.d/nomad.conf
modprobe br_netfilter
cat >/etc/sysctl.d/99-nomad.conf <<'EOF'
net.bridge.bridge-nf-call-arptables = 1
net.bridge.bridge-nf-call-ip6tables = 1
net.bridge.bridge-nf-call-iptables = 1
EOF
sysctl --system >/dev/null

# Nomad under systemd. The client runs as root; it has to manage containers and networks.
mkdir -p /opt/nomad/data /etc/nomad.d
cat >/etc/systemd/system/nomad.service <<'EOF'
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
systemctl enable nomad
systemctl restart nomad

# Wait until the server has elected itself leader.
timeout 120s bash -c 'until [ -n "$(curl -fsS http://127.0.0.1:4646/v1/status/leader 2>/dev/null | tr -d "\"")" ]; do sleep 2; done'

# Bootstrap ACLs once. The management token stays root-only inside the VM; lab.sh copies it to
# the gitignored deploy/dev/.data/env on the Mac.
mkdir -p "$STATE"
chmod 700 "$STATE"
# We generate the token ourselves and hand it to Nomad, so it's on disk before Nomad accepts it.
if [ ! -s "$STATE/bootstrap.token" ]; then
  (umask 077 && cat /proc/sys/kernel/random/uuid >"$STATE/bootstrap.token.tmp")
  timeout 60s bash -c "until nomad acl bootstrap -address=http://127.0.0.1:4646 '$STATE/bootstrap.token.tmp' >/dev/null; do sleep 2; done"
  mv "$STATE/bootstrap.token.tmp" "$STATE/bootstrap.token"
fi

# `sudo lab-nomad ...` runs the Nomad CLI inside the VM with the management token.
cat >/usr/local/bin/lab-nomad <<EOF
#!/bin/sh
NOMAD_ADDR=http://127.0.0.1:4646 NOMAD_TOKEN="\$(cat $STATE/bootstrap.token)" exec nomad "\$@"
EOF
chmod 755 /usr/local/bin/lab-nomad

# Marker for the Lima readiness probe: world-readable, and /run is cleared on every boot.
touch /run/caravana-lab.ready
