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
        to = 8080
      }
    }
    service {
      name = "shop-web"
      port = "http"
      provider = "nomad"
      tags = ["traefik.enable=true", "traefik.http.routers.shop-web.rule=Host(`shop.example.com`)", "traefik.http.routers.shop-web.entrypoints=websecure", "traefik.http.routers.shop-web.tls=true"]
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
        auth {
          password = "$${CARAVANA_REGISTRY_PASSWORD}"
          server_address = "ghcr.io"
          username = "$${CARAVANA_REGISTRY_USERNAME}"
        }
        image = "ghcr.io/acme/shop@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
      }
      resources {
        cpu = 100
        memory = 64
        memory_max = 256
      }
      template {
        data = "{{ with nomadVar \"nomad/jobs/shop/web\" }}{{ range .Tuples }}{{ .K }}={{ printf \"%s\" .V | toJSON }}\n{{ end }}{{ end }}"
        destination = "secrets/caravana.env"
        env = true
        change_mode = "noop"
      }
      template {
        data = "{{ with nomadVar \"nomad/jobs/shop/web/web\" }}{{ $user := \"\" }}{{ $pass := \"\" }}{{ range .Tuples }}{{ if eq .K \"CARAVANA_REGISTRY_USERNAME\" }}{{ $user = printf \"%s\" .V }}{{ end }}{{ if eq .K \"CARAVANA_REGISTRY_PASSWORD\" }}{{ $pass = printf \"%s\" .V }}{{ end }}{{ end }}CARAVANA_REGISTRY_USERNAME={{ $user | toJSON }}\nCARAVANA_REGISTRY_PASSWORD={{ $pass | toJSON }}\n{{ end }}"
        destination = "secrets/registry.env"
        env = true
        change_mode = "noop"
      }
    }
  }
}
