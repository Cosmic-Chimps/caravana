# Exported by Caravana.
#
# Needs: Traefik.

job "shop" {
  type = "service"
  datacenters = ["*"]
  meta = {
    "caravana.app" = "shop"
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
  group "web" {
    count = 1
    shutdown_delay = "5s"
    update {
      canary = 1
    }
    network {
      mode = "bridge"
      port "http" {
        to = 80
      }
    }
    service {
      name = "shop-web"
      port = "http"
      provider = "nomad"
      tags = ["traefik.enable=true", "traefik.http.routers.shop-web-86cf238f.rule=Host(`staging.example.com`)", "traefik.http.routers.shop-web-86cf238f.entrypoints=websecure", "traefik.http.routers.shop-web-86cf238f.tls=true", "traefik.http.routers.shop-web-86cf238f.middlewares=shop-web-86cf238f-auth@file"]
      canary_tags = ["caravana.canary=true"]
      check {
        name = "health"
        type = "http"
        path = "/health"
        interval = "10s"
        timeout = "2s"
      }
    }
    task "web" {
      driver = "docker"
      config {
        image = "traefik/whoami@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
      }
      resources {
        cpu = 100
        memory = 64
        memory_max = 256
      }
    }
  }
}
