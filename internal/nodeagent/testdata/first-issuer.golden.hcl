# Written by `caravana node`: the next sync rewrites it. Change it through Caravana.
#
# Nomad agent config for a Caravana server: ACLs on. Unlike the lab, nothing listens publicly: the
# HTTP API is on loopback, Nomad's RPC and serf (on a Nomad server) are on this server's private mesh
# address, and every allocation port is on the private caravana0 interface, so apps are reached only
# through Traefik (spec FR-005, FR-005a).

datacenter = "dc1"

# SIGTERM makes Nomad leave the cluster before it exits; "caravana node leave" sends it. A stop, a
# restart and a reboot send SIGINT (the unit's KillSignal), which leaves nothing: a Nomad server that
# only restarts must keep its place in raft. Without a graceful leave, a server taken out of a cluster
# with two Nomad servers left the other without a quorum (cluster test, research L16).
leave_on_terminate = true
data_dir   = "/opt/nomad/data"
bind_addr  = "127.0.0.1"

addresses {
  http = "127.0.0.1"
  rpc  = "10.98.0.1"
  serf = "10.98.0.1"
}

advertise {
  http = "127.0.0.1"
  rpc  = "10.98.0.1"
  serf = "10.98.0.1"
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
  bootstrap_expect = 1
  encrypt          = "dGVzdC1nb3NzaXAta2V5LTMyLWJ5dGVzLWxvbmchIQ=="

  # The cluster's issuer: Caravana serves its discovery at this address (spec 038).
  oidc_issuer = "https://console.example.com"

  # After a new leader is elected, Nomad gives every node this long to heartbeat before it calls one
  # down. Its default is 5 minutes: when the server that was lost was the leader, its copies stayed
  # registered, and Traefik kept sending them requests, for five minutes (cluster test, research L14).
  failover_heartbeat_ttl = "1m"

  # How long past its heartbeat's time a node may be before Nomad calls it down. Nomad's default is
  # 10 seconds. A node's heartbeat goes to any Nomad server; when that server is the one that was
  # lost, the heartbeat hangs until the connection fails (24 seconds in the cluster test), and with
  # the default a healthy server was called down and its copies were moved (research L18).
  heartbeat_grace = "30s"
}

client {
  enabled               = true
  cni_path              = "/opt/cni/bin"
  node_pool             = "default"

  # Allocation ports bind to this interface's address (10.99.0.N). Nomad's port mappings are
  # DNAT rules that a host firewall can't block, so binding them to a private address is what
  # keeps http://<public-ip>:<port> closed (research R4).
  network_interface     = "caravana0"

  # Traefik, the one ingress, runs on the server marked here.
  meta {
    caravana_ingress = "true"
  }

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

# Nomad's stats hook doesn't collect task stats unless this is set (seen with Nomad 2.0.7): the
# stats endpoint answers 200 with no tasks, and the console's CPU and memory per copy (feature
# 014) would be empty. No metrics sink is configured, so nothing is published anywhere; it only
# turns the collection on.
telemetry {
  publish_allocation_metrics = true
}
