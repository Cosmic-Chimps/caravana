# Exported by Caravana.
#
# Needs: Traefik.

job "mix" {
  type = "service"
  datacenters = ["*"]
  meta = {
    "caravana.app" = "mix"
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
        to = 3000
      }
    }
    service {
      name = "mix-web"
      port = "http"
      provider = "nomad"
      tags = ["traefik.enable=true", "traefik.http.routers.mix-web.rule=Host(`mix.example.com`)", "traefik.http.routers.mix-web.entrypoints=websecure", "traefik.http.routers.mix-web.tls=true"]
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
      name = "mix-web-internal"
      port = "http"
      provider = "nomad"
      address_mode = "alloc"
      canary_tags = ["caravana.canary=true"]
    }
    task "web" {
      driver = "docker"
      config {
        auth {
          password = "$${CARAVANA_REGISTRY_PASSWORD}"
          server_address = "ghcr.io"
          username = "$${CARAVANA_REGISTRY_USERNAME}"
        }
        image = "ghcr.io/acme/web@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
      }
      resources {
        cpu = 100
        memory = 64
        memory_max = 256
      }
      template {
        data = "{{ with nomadVar \"nomad/jobs/mix/web/web\" }}{{ $user := \"\" }}{{ $pass := \"\" }}{{ range .Tuples }}{{ if eq .K \"CARAVANA_REGISTRY_USERNAME\" }}{{ $user = printf \"%s\" .V }}{{ end }}{{ if eq .K \"CARAVANA_REGISTRY_PASSWORD\" }}{{ $pass = printf \"%s\" .V }}{{ end }}{{ end }}CARAVANA_REGISTRY_USERNAME={{ $user | toJSON }}\nCARAVANA_REGISTRY_PASSWORD={{ $pass | toJSON }}\n{{ end }}"
        destination = "secrets/registry.env"
        env = true
        change_mode = "noop"
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
        data = "{{ range nomadService \"mix-web-internal\" }}{{ if not (.Tags | contains \"caravana.canary=true\") }}{{ .Address }} web\n{{ end }}{{ end }}{{ range nomadService \"mix-api-internal\" }}{{ if not (.Tags | contains \"caravana.canary=true\") }}{{ .Address }} api\n{{ end }}{{ end }}{{ range nomadService \"mix-cache-internal\" }}{{ if not (.Tags | contains \"caravana.canary=true\") }}{{ .Address }} cache\n{{ end }}{{ end }}"
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
  group "api" {
    count = 1
    shutdown_delay = "5s"
    update {
      canary = 1
    }
    network {
      mode = "bridge"
      port "http" {
        to = 4000
      }
    }
    service {
      name = "mix-api"
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
      name = "mix-api-internal"
      port = "http"
      provider = "nomad"
      address_mode = "alloc"
      canary_tags = ["caravana.canary=true"]
    }
    task "api" {
      driver = "docker"
      config {
        auth {
          password = "$${CARAVANA_REGISTRY_PASSWORD}"
          server_address = "registry.example.com:5000"
          username = "$${CARAVANA_REGISTRY_USERNAME}"
        }
        image = "registry.example.com:5000/acme/api@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
      }
      resources {
        cpu = 100
        memory = 64
        memory_max = 256
      }
      template {
        data = "{{ with nomadVar \"nomad/jobs/mix/api/api\" }}{{ $user := \"\" }}{{ $pass := \"\" }}{{ range .Tuples }}{{ if eq .K \"CARAVANA_REGISTRY_USERNAME\" }}{{ $user = printf \"%s\" .V }}{{ end }}{{ if eq .K \"CARAVANA_REGISTRY_PASSWORD\" }}{{ $pass = printf \"%s\" .V }}{{ end }}{{ end }}CARAVANA_REGISTRY_USERNAME={{ $user | toJSON }}\nCARAVANA_REGISTRY_PASSWORD={{ $pass | toJSON }}\n{{ end }}"
        destination = "secrets/registry.env"
        env = true
        change_mode = "noop"
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
        data = "{{ range nomadService \"mix-web-internal\" }}{{ if not (.Tags | contains \"caravana.canary=true\") }}{{ .Address }} web\n{{ end }}{{ end }}{{ range nomadService \"mix-api-internal\" }}{{ if not (.Tags | contains \"caravana.canary=true\") }}{{ .Address }} api\n{{ end }}{{ end }}{{ range nomadService \"mix-cache-internal\" }}{{ if not (.Tags | contains \"caravana.canary=true\") }}{{ .Address }} cache\n{{ end }}{{ end }}"
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
  group "cache" {
    count = 1
    shutdown_delay = "5s"
    update {
      canary = 1
    }
    network {
      mode = "bridge"
      port "redis" {
        to = 6379
      }
    }
    service {
      name = "mix-cache"
      port = "redis"
      provider = "nomad"
      check {
        name = "health"
        type = "tcp"
        interval = "10s"
        timeout = "2s"
      }
    }
    service {
      name = "mix-cache-internal"
      port = "redis"
      provider = "nomad"
      address_mode = "alloc"
      canary_tags = ["caravana.canary=true"]
    }
    task "cache" {
      driver = "docker"
      config {
        image = "redis@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
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
        data = "{{ range nomadService \"mix-web-internal\" }}{{ if not (.Tags | contains \"caravana.canary=true\") }}{{ .Address }} web\n{{ end }}{{ end }}{{ range nomadService \"mix-api-internal\" }}{{ if not (.Tags | contains \"caravana.canary=true\") }}{{ .Address }} api\n{{ end }}{{ end }}{{ range nomadService \"mix-cache-internal\" }}{{ if not (.Tags | contains \"caravana.canary=true\") }}{{ .Address }} cache\n{{ end }}{{ end }}"
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
