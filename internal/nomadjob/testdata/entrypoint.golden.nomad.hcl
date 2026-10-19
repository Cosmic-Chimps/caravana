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
  group "cache" {
    count = 1
    shutdown_delay = "5s"
    update {
      canary = 1
    }
    network {
      mode = "bridge"
      port "p6379" {
        to = 6379
      }
    }
    service {
      name = "shop-cache"
      port = "p6379"
      provider = "nomad"
      check {
        name = "health"
        type = "tcp"
        interval = "10s"
        timeout = "2s"
      }
    }
    service {
      name = "shop-cache-internal"
      port = "p6379"
      provider = "nomad"
      address_mode = "alloc"
      canary_tags = ["caravana.canary=true"]
    }
    task "cache" {
      driver = "docker"
      config {
        args = ["redis-server --requirepass $REDIS_PASSWORD"]
        command = "-c"
        entrypoint = ["/bin/sh"]
        image = "redis@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
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
        data = "{{ range nomadService \"shop-cache-internal\" }}{{ if not (.Tags | contains \"caravana.canary=true\") }}{{ .Address }} cache\n{{ end }}{{ end }}"
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
  group "tool" {
    count = 1
    shutdown_delay = "5s"
    update {
      canary = 1
    }
    network {
      mode = "bridge"
    }
    task "tool" {
      driver = "docker"
      config {
        entrypoint = ["/usr/bin/tool", "--serve"]
        image = "ghcr.io/acme/tool@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
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
        data = "{{ range nomadService \"shop-cache-internal\" }}{{ if not (.Tags | contains \"caravana.canary=true\") }}{{ .Address }} cache\n{{ end }}{{ end }}"
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
