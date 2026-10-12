# Exported by Caravana.
#
# Needs: Traefik.

job "api" {
  type = "service"
  datacenters = ["*"]
  meta = {
    "caravana.app" = "api"
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
  group "grpc" {
    count = 1
    shutdown_delay = "5s"
    update {
      canary = 1
    }
    network {
      mode = "bridge"
      port "grpc" {
        to = 50051
      }
    }
    service {
      name = "api-grpc"
      port = "grpc"
      provider = "nomad"
      check {
        name = "health"
        type = "tcp"
        interval = "10s"
        timeout = "2s"
      }
    }
    task "grpc" {
      driver = "docker"
      config {
        image = "ghcr.io/acme/api@sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
      }
      resources {
        cpu = 100
        memory = 64
        memory_max = 256
      }
    }
  }
}
