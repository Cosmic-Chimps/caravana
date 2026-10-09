# Nomad agent config for a public Caravana host: one node, server + client, ACLs on.
# Rendered by deploy/vm/setup.sh (it fills or drops the CPU line). Unlike the lab, nothing
# listens publicly: Nomad is on loopback and every allocation port is on the private
# caravana0 interface, so apps are reached only through Traefik (spec FR-005, FR-005a).

datacenter = "dc1"
data_dir   = "/opt/nomad/data"
bind_addr  = "127.0.0.1"

advertise {
  http = "127.0.0.1"
  rpc  = "127.0.0.1"
  serf = "127.0.0.1"
}

server {
  enabled          = true
  bootstrap_expect = 1
}

client {
  enabled  = true
  cni_path = "/opt/cni/bin"

  # Allocation ports bind to this interface's address (10.99.0.1). Nomad's port mappings are
  # DNAT rules that a host firewall can't block, so binding them to a private address is what
  # keeps http://<public-ip>:<port> closed (research R4).
  network_interface = "caravana0"

  #CPU_TOTAL_COMPUTE#

  # Traefik keeps its Let's Encrypt certificates here, across restarts and reboots.
  host_volume "traefik-acme" {
    path      = "/var/lib/caravana-host/traefik"
    read_only = false
  }
}

acl {
  enabled = true
}

plugin "docker" {
  config {
    allow_privileged = false
  }
}
