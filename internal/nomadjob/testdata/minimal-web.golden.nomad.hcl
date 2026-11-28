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
      tags = ["traefik.enable=true", "traefik.http.routers.blog-web.rule=Host(`blog.example.com`)", "traefik.http.routers.blog-web.entrypoints=websecure", "traefik.http.routers.blog-web.tls=true", "traefik.http.routers.blog-web.middlewares=blog-web-retry", "traefik.http.middlewares.blog-web-retry.retry.attempts=3"]
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
      tags = ["caravana-place=default/production", "caravana-name=blog-web", "caravana-name=blog"]
      canary_tags = ["caravana-place=default/production", "caravana-name=blog-web", "caravana-name=blog", "caravana.canary=true"]
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
        data = "{{ range nomadService \"blog-web-internal\" }}{{ if not (.Tags | contains \"caravana.canary=true\") }}{{ .Address }} web\n{{ end }}{{ end }}{{ range nomadServices }}{{ if .Tags | contains \"caravana-place=default/production\" }}{{ $svc := .Name }}{{ range .Tags }}{{ if regexMatch \"^caravana-name=\" . }}{{ $n := regexReplaceAll \"^caravana-name=\" \"\" . }}{{ $k := printf \"n:%s\" $n }}{{ if scratch.Key $k }}{{ scratch.Set $k \"\" }}{{ else }}{{ scratch.Set $k $svc }}{{ end }}{{ end }}{{ end }}{{ end }}{{ end }}{{ range nomadServices }}{{ if .Tags | contains \"caravana-place=default/production\" }}{{ $svc := .Name }}{{ range .Tags }}{{ if regexMatch \"^caravana-name=\" . }}{{ $n := regexReplaceAll \"^caravana-name=\" \"\" . }}{{ if and (eq (scratch.Get (printf \"n:%s\" $n)) $svc) (ne $n \"web\") }}{{ range nomadService $svc }}{{ if not (.Tags | contains \"caravana.canary=true\") }}{{ .Address }} {{ $n }}\n{{ end }}{{ end }}{{ end }}{{ end }}{{ end }}{{ end }}{{ end }}"
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
