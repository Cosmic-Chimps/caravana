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
  group "indexer" {
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
    task "indexer" {
      driver = "docker"
      config {
        image = "opensearchproject/opensearch@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
        mount {
          readonly = true
          source = "local/caravana-files/0"
          target = "/usr/share/opensearch/config/opensearch.yml"
          type = "bind"
        }
        mount {
          readonly = true
          source = "local/caravana-files/1"
          target = "/etc/app/delims.conf"
          type = "bind"
        }
        mount {
          readonly = true
          source = "local/caravana-files/2"
          target = "/etc/app/crlf.txt"
          type = "bind"
        }
        mount {
          readonly = true
          source = "secrets/caravana-files/3"
          target = "/certs/node.key"
          type = "bind"
        }
        mount {
          readonly = true
          source = "secrets/caravana-files/4"
          target = "/certs/node-copy.key"
          type = "bind"
        }
        mount {
          readonly = true
          source = "secrets/caravana-files/5"
          target = "/usr/share/opensearch/config/internal_users.yml"
          type = "bind"
        }
      }
      resources {
        cpu = 100
        memory = 64
        memory_max = 256
      }
      template {
        data = "{{ with nomadVar \"nomad/jobs/shop/indexer\" }}{{ range .Tuples }}{{ if not (eq .K \"INTERNAL_USERS\" \"TLS_KEY\") }}{{ .K }}={{ printf \"%s\" .V | toJSON }}\n{{ end }}{{ end }}{{ end }}"
        destination = "secrets/caravana.env"
        env = true
        change_mode = "noop"
      }
      template {
        data = <<EOT
cluster.name: shop
note: {{ not a template }} $${HOME} %%{x} [[x]]
EOT
        destination = "local/caravana-files/0"
        change_mode = "noop"
        perms = "0644"
        left_delimiter = "[[cv0"
        right_delimiter = "cv0]]"
      }
      template {
        data = "a [[cv0 b cv0]] c"
        destination = "local/caravana-files/1"
        change_mode = "noop"
        perms = "0600"
        left_delimiter = "[[cv1"
        right_delimiter = "cv1]]"
      }
      template {
        data = "one\r\ntwo\r\n"
        destination = "local/caravana-files/2"
        change_mode = "noop"
        perms = "0640"
        uid = 1000
        gid = 0
        left_delimiter = "[[cv0"
        right_delimiter = "cv0]]"
      }
      template {
        data = "{{ with nomadVar \"nomad/jobs/shop/indexer\" }}{{ range .Tuples }}{{ if eq .K \"TLS_KEY\" }}{{ printf \"%s\" .V }}{{ end }}{{ end }}{{ end }}"
        destination = "secrets/caravana-files/3"
        change_mode = "noop"
        perms = "0400"
      }
      template {
        data = "{{ with nomadVar \"nomad/jobs/shop/indexer\" }}{{ range .Tuples }}{{ if eq .K \"TLS_KEY\" }}{{ printf \"%s\" .V }}{{ end }}{{ end }}{{ end }}"
        destination = "secrets/caravana-files/4"
        change_mode = "noop"
        perms = "0400"
      }
      template {
        data = "{{ with nomadVar \"nomad/jobs/shop/indexer\" }}{{ range .Tuples }}{{ if eq .K \"INTERNAL_USERS\" }}{{ printf \"%s\" .V }}{{ end }}{{ end }}{{ end }}"
        destination = "secrets/caravana-files/5"
        change_mode = "noop"
        perms = "0644"
        uid = 1000
        gid = 1000
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
      template {
        data = "{{ with nomadVar \"nomad/jobs/shop/plain\" }}{{ range .Tuples }}{{ .K }}={{ printf \"%s\" .V | toJSON }}\n{{ end }}{{ end }}"
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
