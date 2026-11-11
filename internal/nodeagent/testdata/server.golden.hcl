# Written by `caravana node`: the next sync rewrites it. Change it through Caravana.
#
# Nomad agent config for a Caravana server: ACLs on. Unlike the lab, nothing listens publicly: the
# HTTP API is on loopback, Nomad's RPC and serf (on a Nomad server) are on this server's private mesh
# address, and every allocation port is on the private caravana0 interface, so apps are reached only
# through Traefik (spec FR-005, FR-005a).

name       = "fsn-2"
datacenter = "dc1"
data_dir   = "/opt/nomad/data"
bind_addr  = "127.0.0.1"

addresses {
  http = "127.0.0.1"
  rpc  = "10.98.0.3"
  serf = "10.98.0.3"
}

advertise {
  http = "127.0.0.1"
  rpc  = "10.98.0.3"
  serf = "10.98.0.3"
}

tls {
  http                   = false
  rpc                    = true
  ca_file                = "/etc/nomad.d/tls/ca.pem"
  cert_file              = "/etc/nomad.d/tls/agent.pem"
  key_file               = "/etc/nomad.d/tls/agent-key.pem"
  verify_server_hostname = true
}

server {
  enabled          = true
  encrypt          = "dGVzdC1nb3NzaXAta2V5LTMyLWJ5dGVzLWxvbmchIQ=="
  server_join {
    retry_join = ["10.98.0.1:4648", "10.98.0.2:4648"]
  }
}

client {
  enabled               = true
  cni_path              = "/opt/cni/bin"
  node_pool             = "default"

  # Allocation ports bind to this interface's address (10.99.0.N). Nomad's port mappings are
  # DNAT rules that a host firewall can't block, so binding them to a private address is what
  # keeps http://<public-ip>:<port> closed (research R4).
  network_interface     = "caravana0"

  # Each server's containers have a subnet of their own, routed through the mesh, so a service
  # name resolves to a container on another server. The first server keeps Nomad's default.
  bridge_network_subnet = "10.97.12.0/22"

  servers               = ["10.98.0.1:4647", "10.98.0.2:4647"]
}

acl {
  enabled = true
}

plugin "docker" {
  config {
    allow_privileged = false
  }
}

# Nomad's stats hook doesn't collect task stats unless this is set (seen with Nomad 2.0.7): the
# stats endpoint answers 200 with no tasks, and the console's CPU and memory per copy (feature
# 014) would be empty. No metrics sink is configured, so nothing is published anywhere; it only
# turns the collection on.
telemetry {
  publish_allocation_metrics = true
}
