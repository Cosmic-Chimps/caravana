# Exported by Caravana.
#
# Needs: Traefik.

job "jobs" {
  type = "service"
  datacenters = ["*"]
  meta = {
    "caravana.app" = "jobs"
    "caravana.deployment" = "d1"
  }
  update {
    max_parallel = 1
    health_check = "checks"
    min_healthy_time = "10s"
    healthy_deadline = "3m0s"
    progress_deadline = "10m0s"
    auto_revert = true
    auto_promote = true
  }
  group "worker" {
    count = 1
    shutdown_delay = "5s"
    update {
      canary = 1
    }
    network {
      mode = "bridge"
    }
    task "worker" {
      driver = "docker"
      kill_timeout = "20s"
      config {
        image = "ghcr.io/acme/worker@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
      }
      resources {
        cpu = 100
        memory = 64
        memory_max = 256
      }
    }
  }
}
