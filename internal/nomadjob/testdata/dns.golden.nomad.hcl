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
  group "api" {
    count = 1
    shutdown_delay = "5s"
    update {
      canary = 1
    }
    disconnect {
      lost_after = "5m0s"
      replace = true
      reconcile = "keep_original"
    }
    network {
      mode = "bridge"
      dns {
        servers = ["1.1.1.1", "2606:4700:4700::1111"]
        searches = ["corp.example.com"]
        options = ["ndots:2", "edns0"]
      }
    }
    task "api" {
      driver = "docker"
      config {
        image = "ghcr.io/acme/api@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
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
        data = "{{ range nomadServices }}{{ if .Tags | contains \"caravana-place=default/production\" }}{{ $svc := .Name }}{{ range .Tags }}{{ if regexMatch \"^caravana-name=\" . }}{{ $n := regexReplaceAll \"^caravana-name=\" \"\" . }}{{ $k := printf \"n:%s\" $n }}{{ if scratch.Key $k }}{{ scratch.Set $k \"\" }}{{ else }}{{ scratch.Set $k $svc }}{{ end }}{{ end }}{{ end }}{{ end }}{{ end }}{{ range nomadServices }}{{ if .Tags | contains \"caravana-place=default/production\" }}{{ $svc := .Name }}{{ range .Tags }}{{ if regexMatch \"^caravana-name=\" . }}{{ $n := regexReplaceAll \"^caravana-name=\" \"\" . }}{{ if and (eq (scratch.Get (printf \"n:%s\" $n)) $svc) }}{{ range nomadService $svc }}{{ if not (.Tags | contains \"caravana.canary=true\") }}{{ .Address }} {{ $n }}\n{{ end }}{{ end }}{{ end }}{{ end }}{{ end }}{{ end }}{{ end }}"
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
    disconnect {
      lost_after = "5m0s"
      replace = true
      reconcile = "keep_original"
    }
    network {
      mode = "bridge"
      dns {
        servers = ["10.0.0.2"]
      }
    }
    task "worker" {
      driver = "docker"
      config {
        image = "ghcr.io/acme/worker@sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
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
        data = "{{ range nomadServices }}{{ if .Tags | contains \"caravana-place=default/production\" }}{{ $svc := .Name }}{{ range .Tags }}{{ if regexMatch \"^caravana-name=\" . }}{{ $n := regexReplaceAll \"^caravana-name=\" \"\" . }}{{ $k := printf \"n:%s\" $n }}{{ if scratch.Key $k }}{{ scratch.Set $k \"\" }}{{ else }}{{ scratch.Set $k $svc }}{{ end }}{{ end }}{{ end }}{{ end }}{{ end }}{{ range nomadServices }}{{ if .Tags | contains \"caravana-place=default/production\" }}{{ $svc := .Name }}{{ range .Tags }}{{ if regexMatch \"^caravana-name=\" . }}{{ $n := regexReplaceAll \"^caravana-name=\" \"\" . }}{{ if and (eq (scratch.Get (printf \"n:%s\" $n)) $svc) }}{{ range nomadService $svc }}{{ if not (.Tags | contains \"caravana.canary=true\") }}{{ .Address }} {{ $n }}\n{{ end }}{{ end }}{{ end }}{{ end }}{{ end }}{{ end }}{{ end }}"
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
  group "plain" {
    count = 1
    shutdown_delay = "5s"
    update {
      canary = 1
    }
    disconnect {
      lost_after = "5m0s"
      replace = true
      reconcile = "keep_original"
    }
    network {
      mode = "bridge"
    }
    task "plain" {
      driver = "docker"
      config {
        image = "ghcr.io/acme/plain@sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
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
        data = "{{ range nomadServices }}{{ if .Tags | contains \"caravana-place=default/production\" }}{{ $svc := .Name }}{{ range .Tags }}{{ if regexMatch \"^caravana-name=\" . }}{{ $n := regexReplaceAll \"^caravana-name=\" \"\" . }}{{ $k := printf \"n:%s\" $n }}{{ if scratch.Key $k }}{{ scratch.Set $k \"\" }}{{ else }}{{ scratch.Set $k $svc }}{{ end }}{{ end }}{{ end }}{{ end }}{{ end }}{{ range nomadServices }}{{ if .Tags | contains \"caravana-place=default/production\" }}{{ $svc := .Name }}{{ range .Tags }}{{ if regexMatch \"^caravana-name=\" . }}{{ $n := regexReplaceAll \"^caravana-name=\" \"\" . }}{{ if and (eq (scratch.Get (printf \"n:%s\" $n)) $svc) }}{{ range nomadService $svc }}{{ if not (.Tags | contains \"caravana.canary=true\") }}{{ .Address }} {{ $n }}\n{{ end }}{{ end }}{{ end }}{{ end }}{{ end }}{{ end }}{{ end }}"
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
