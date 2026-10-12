# Exported by Caravana.
#
# Needs: Traefik.

job "blog" {
  type = "service"
  datacenters = ["*"]
  meta = {
    "caravana.app" = "blog"
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
      name = "blog-web"
      port = "http"
      provider = "nomad"
      tags = ["traefik.enable=true", "traefik.http.routers.blog-web.rule=Host(`blog.example.com`)", "traefik.http.routers.blog-web.entrypoints=websecure", "traefik.http.routers.blog-web.tls=true"]
      canary_tags = ["caravana.canary=true"]
      check {
        name = "health"
        type = "tcp"
        interval = "10s"
        timeout = "2s"
      }
    }
    service {
      name = "blog-web-internal"
      port = "http"
      provider = "nomad"
      address_mode = "alloc"
      canary_tags = ["caravana.canary=true"]
    }
    task "web" {
      driver = "docker"
      config {
        image = "ghcr.io/acme/blog@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
      }
      resources {
        cpu = 100
        memory = 64
        memory_max = 256
      }
      template {
        data = "{{ with nomadVar \"nomad/jobs/blog/web\" }}{{ range .Tuples }}{{ .K }}={{ printf \"%s\" .V | toJSON }}\n{{ end }}{{ end }}"
        destination = "secrets/caravana.env"
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
        data = "{{ range nomadService \"blog-web-internal\" }}{{ if not (.Tags | contains \"caravana.canary=true\") }}{{ .Address }} web\n{{ end }}{{ end }}"
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
      config {
        args = ["--queue", "default"]
        command = "/app/worker"
        image = "ghcr.io/acme/jobs@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
      }
      env = {
        "LOG_LEVEL" = "info"
        "QUEUE_URL" = "redis://queue:6379"
      }
      resources {
        cpu = 100
        memory = 64
        memory_max = 256
      }
      template {
        data = "{{ with nomadVar \"nomad/jobs/blog/worker\" }}{{ range .Tuples }}{{ .K }}={{ printf \"%s\" .V | toJSON }}\n{{ end }}{{ end }}"
        destination = "secrets/caravana.env"
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
        data = "{{ range nomadService \"blog-web-internal\" }}{{ if not (.Tags | contains \"caravana.canary=true\") }}{{ .Address }} web\n{{ end }}{{ end }}"
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
