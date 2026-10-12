# Exported by Caravana.
#
# Needs: Traefik.

job "whoami" {
  type = "service"
  datacenters = ["*"]
  meta = {
    "caravana.app" = "whoami"
    "caravana.deployment" = "lab-manual"
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
  group "web" {
    count = 2
    shutdown_delay = "5s"
    update {
      canary = 2
    }
    network {
      mode = "bridge"
      port "http" {
        to = 80
      }
    }
    service {
      name = "whoami-web"
      port = "http"
      provider = "nomad"
      tags = ["traefik.enable=true", "traefik.http.routers.whoami-web.rule=Host(`whoami.localhost`)", "traefik.http.routers.whoami-web.entrypoints=websecure", "traefik.http.routers.whoami-web.tls=true"]
      canary_tags = ["caravana.canary=true"]
      check {
        name = "health"
        type = "http"
        path = "/"
        interval = "10s"
        timeout = "2s"
      }
    }
    task "web" {
      driver = "docker"
      config {
        image = "traefik/whoami@sha256:c4717a8d1f0134a7444e24f881160e033991f23027c6c5a9a3f8fd22e70d1d44"
      }
      resources {
        cpu = 100
        memory = 64
        memory_max = 256
      }
    }
  }
}
