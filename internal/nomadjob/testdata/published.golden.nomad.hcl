# Exported by Caravana.
#
# Needs: Traefik.

job "calls" {
  type = "service"
  datacenters = ["*"]
  meta = {
    "caravana.app" = "calls"
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
  group "turn" {
    count = 1
    shutdown_delay = "5s"
    update {
      canary = 0
      auto_promote = false
    }
    network {
      mode = "bridge"
      port "http" {
        to = 8080
      }
      port "turn" {
        static = 3478
        to = 3478
        host_network = "public"
      }
      port "relay" {
        static = 7882
        to = 7882
        host_network = "public"
      }
    }
    service {
      name = "calls-turn"
      port = "http"
      provider = "nomad"
      check {
        name = "health"
        type = "http"
        path = "/healthz"
        interval = "10s"
        timeout = "2s"
      }
    }
    service {
      name = "calls-turn-internal"
      port = "http"
      provider = "nomad"
      address_mode = "alloc"
      canary_tags = ["caravana.canary=true"]
    }
    task "turn" {
      driver = "docker"
      config {
        image = "ghcr.io/acme/turn@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
      }
      resources {
        cpu = 100
        memory = 64
        memory_max = 256
      }
    }
    task "names" {
      driver = "docker"
      kill_timeout = "1s"
      lifecycle {
        hook = "prestart"
        sidecar = true
      }
      config {
        args = ["-c", "[ -f /local/base ] || cp /etc/hosts /local/base\ncat /local/base /local/names > /etc/hosts\ntrap 'exit 0' TERM INT\nwhile :; do sleep 3600 & wait $!; done\n"]
        command = "/bin/sh"
        image = "busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"
      }
      resources {
        cpu = 20
        memory = 16
      }
      template {
        data = "{{ range nomadService \"calls-turn-internal\" }}{{ if not (.Tags | contains \"caravana.canary=true\") }}{{ .Address }} turn\n{{ end }}{{ end }}{{ range nomadService \"calls-web-internal\" }}{{ if not (.Tags | contains \"caravana.canary=true\") }}{{ .Address }} web\n{{ end }}{{ end }}"
        destination = "local/names"
        change_mode = "script"
        change_script {
          command = "/bin/sh"
          args = ["-c", "cat /local/base /local/names > /etc/hosts"]
          timeout = "5s"
          fail_on_error = false
        }
      }
    }
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
        to = 3000
      }
    }
    service {
      name = "calls-web"
      port = "http"
      provider = "nomad"
      tags = ["traefik.enable=true", "traefik.http.routers.calls-web.rule=Host(`calls.example.com`)", "traefik.http.routers.calls-web.entrypoints=websecure", "traefik.http.routers.calls-web.tls=true"]
      canary_tags = ["caravana.canary=true"]
      check {
        name = "health"
        type = "http"
        path = "/"
        interval = "10s"
        timeout = "2s"
      }
    }
    service {
      name = "calls-web-internal"
      port = "http"
      provider = "nomad"
      address_mode = "alloc"
      canary_tags = ["caravana.canary=true"]
    }
    task "web" {
      driver = "docker"
      config {
        image = "ghcr.io/acme/web@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
      }
      resources {
        cpu = 100
        memory = 64
        memory_max = 256
      }
    }
    task "names" {
      driver = "docker"
      kill_timeout = "1s"
      lifecycle {
        hook = "prestart"
        sidecar = true
      }
      config {
        args = ["-c", "[ -f /local/base ] || cp /etc/hosts /local/base\ncat /local/base /local/names > /etc/hosts\ntrap 'exit 0' TERM INT\nwhile :; do sleep 3600 & wait $!; done\n"]
        command = "/bin/sh"
        image = "busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"
      }
      resources {
        cpu = 20
        memory = 16
      }
      template {
        data = "{{ range nomadService \"calls-turn-internal\" }}{{ if not (.Tags | contains \"caravana.canary=true\") }}{{ .Address }} turn\n{{ end }}{{ end }}{{ range nomadService \"calls-web-internal\" }}{{ if not (.Tags | contains \"caravana.canary=true\") }}{{ .Address }} web\n{{ end }}{{ end }}"
        destination = "local/names"
        change_mode = "script"
        change_script {
          command = "/bin/sh"
          args = ["-c", "cat /local/base /local/names > /etc/hosts"]
          timeout = "5s"
          fail_on_error = false
        }
      }
    }
  }
}
