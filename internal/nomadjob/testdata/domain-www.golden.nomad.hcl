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
      tags = ["traefik.enable=true", "traefik.http.routers.shop-web-a379a6f6.rule=Host(`example.com`) || Host(`www.example.com`)", "traefik.http.routers.shop-web-a379a6f6.entrypoints=websecure", "traefik.http.routers.shop-web-a379a6f6.tls=true", "traefik.http.routers.shop-web-a379a6f6.middlewares=shop-web-a379a6f6-www", "traefik.http.middlewares.shop-web-a379a6f6-www.redirectregex.regex=^https://www[.]example[.]com/(.*)", "traefik.http.middlewares.shop-web-a379a6f6-www.redirectregex.replacement=https://example.com/$1", "traefik.http.middlewares.shop-web-a379a6f6-www.redirectregex.permanent=true"]
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
