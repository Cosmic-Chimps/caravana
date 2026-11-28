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
    spread {
      attribute = "$${node.unique.id}"
      weight = 100
    }
    disconnect {
      lost_after = "5m0s"
      replace = true
      reconcile = "keep_original"
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
      tags = ["traefik.enable=true", "traefik.http.routers.whoami-web.rule=Host(`whoami.localhost`)", "traefik.http.routers.whoami-web.entrypoints=websecure", "traefik.http.routers.whoami-web.tls=true", "traefik.http.routers.whoami-web.middlewares=whoami-web-retry", "traefik.http.middlewares.whoami-web-retry.retry.attempts=3", "traefik.http.services.whoami-web.loadbalancer.healthcheck.path=/", "traefik.http.services.whoami-web.loadbalancer.healthcheck.interval=30s", "traefik.http.services.whoami-web.loadbalancer.healthcheck.timeout=5s"]
      canary_tags = ["caravana.canary=true"]
      check {
        name = "health"
        type = "http"
        path = "/"
        interval = "30s"
        timeout = "5s"
      }
    }
    service {
      name = "whoami-web-internal"
      port = "http"
      provider = "nomad"
      address_mode = "alloc"
      tags = ["caravana-place=default/production", "caravana-name=whoami-web", "caravana-name=whoami"]
      canary_tags = ["caravana-place=default/production", "caravana-name=whoami-web", "caravana-name=whoami", "caravana.canary=true"]
    }
    task "web" {
      driver = "docker"
      config {
        image = "traefik/whoami@sha256:c4717a8d1f0134a7444e24f881160e033991f23027c6c5a9a3f8fd22e70d1d44"
      }
      resources {
        cpu = 500
        memory = 256
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
        data = "{{ range nomadService \"whoami-web-internal\" }}{{ if not (.Tags | contains \"caravana.canary=true\") }}{{ .Address }} web\n{{ end }}{{ end }}{{ range nomadServices }}{{ if .Tags | contains \"caravana-place=default/production\" }}{{ $svc := .Name }}{{ range .Tags }}{{ if regexMatch \"^caravana-name=\" . }}{{ $n := regexReplaceAll \"^caravana-name=\" \"\" . }}{{ $k := printf \"n:%s\" $n }}{{ if scratch.Key $k }}{{ scratch.Set $k \"\" }}{{ else }}{{ scratch.Set $k $svc }}{{ end }}{{ end }}{{ end }}{{ end }}{{ end }}{{ range nomadServices }}{{ if .Tags | contains \"caravana-place=default/production\" }}{{ $svc := .Name }}{{ range .Tags }}{{ if regexMatch \"^caravana-name=\" . }}{{ $n := regexReplaceAll \"^caravana-name=\" \"\" . }}{{ if and (eq (scratch.Get (printf \"n:%s\" $n)) $svc) (ne $n \"web\") }}{{ range nomadService $svc }}{{ if not (.Tags | contains \"caravana.canary=true\") }}{{ .Address }} {{ $n }}\n{{ end }}{{ end }}{{ end }}{{ end }}{{ end }}{{ end }}{{ end }}"
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
