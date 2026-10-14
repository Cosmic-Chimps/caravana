# Nomad agent config for the local lab: one node that is both server and client.
# This is agent configuration, not a job; Caravana still submits jobs as JSON.

datacenter = "dc1"
data_dir   = "/opt/nomad/data"
bind_addr  = "0.0.0.0"

server {
  enabled          = true
  bootstrap_expect = 1
}

client {
  enabled  = true
  cni_path = "/opt/cni/bin"

  # ARM VMs often don't report a CPU clock speed, so Nomad sees 0 MHz and places nothing.
  # 2 cores x 2000 MHz. The installer (plan 012) must detect this case and set it the same way.
  cpu_total_compute = 4000
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
# 014) would be empty. No metrics sink is configured, so nothing is published anywhere.
telemetry {
  publish_allocation_metrics = true
}
