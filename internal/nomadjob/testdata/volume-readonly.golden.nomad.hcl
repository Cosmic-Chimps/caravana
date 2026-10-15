# Exported by Caravana.
#
# Needs: Traefik.

job "kuma" {
  type = "service"
  datacenters = ["*"]
  meta = {
    "caravana.app" = "kuma"
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
      canary = 0
      auto_promote = false
    }
    network {
      mode = "bridge"
      port "http" {
        to = 3001
      }
    }
    volume "data" {
      type = "host"
      source = "kuma_data"
      read_only = true
      access_mode = "single-node-single-writer"
      attachment_mode = "file-system"
    }
    service {
      name = "kuma-web"
      port = "http"
      provider = "nomad"
      tags = ["traefik.enable=true", "traefik.http.routers.kuma-web.rule=Host(`kuma.example.com`)", "traefik.http.routers.kuma-web.entrypoints=websecure", "traefik.http.routers.kuma-web.tls=true"]
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
        image = "louislam/uptime-kuma@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
      }
      resources {
        cpu = 100
        memory = 64
        memory_max = 256
      }
      volume_mount {
        volume = "data"
        destination = "/app/data"
        read_only = true
      }
    }
    task "caravana-volumes" {
      driver = "docker"
      lifecycle {
        hook = "prestart"
        sidecar = false
      }
      config {
        args = ["[ -n \"$ROOT\" ] || ROOT=/caravana-volumes\n[ -n \"$DATA\" ] || DATA=/alloc/data/caravana\n[ -n \"$REQUEST_FILE\" ] || REQUEST_FILE=/local/request.env\n\n# One \"<volume> <kilobytes>\" line for each volume, replaced as a whole so\n# Caravana never reads half a file.\nmeasure_sizes() {\n  mkdir -p \"$DATA\" || return 0\n  : > \"$DATA/.sizes.tmp\" || return 0\n  for dir in \"$ROOT\"/*; do\n    [ -d \"$dir\" ] || continue\n    kb=$(du -sk \"$dir\" 2>/dev/null | cut -f1)\n    [ -n \"$kb\" ] || kb=0\n    echo \"$(basename \"$dir\") $kb\" >> \"$DATA/.sizes.tmp\"\n  done\n  mv \"$DATA/.sizes.tmp\" \"$DATA/sizes\"\n}\n\n# finish <status> writes the result file from MESSAGE and the detail lines the\n# run left in $DETAIL, replacing nothing half-written.\nfinish() {\n  message=$(printf '%s' \"$MESSAGE\" | tr '\\n' ' ')\n  {\n    echo \"status=$1\"\n    echo \"message=$message\"\n    [ ! -f \"$DETAIL\" ] || cat \"$DETAIL\"\n  } > \"$DATA/.$REQUEST.tmp\" && mv \"$DATA/.$REQUEST.tmp\" \"$DATA/$REQUEST.result\"\n  rm -f \"$DETAIL\"\n}\n\n# --- The runs. Each returns 0 for success, or 1 with MESSAGE saying why. It\n# --- appends \"key=value\" lines to $DETAIL for the result file.\n\ndo_backup() {\n  MESSAGE=\"not implemented yet\"\n  return 1\n}\n\ndo_restore() {\n  MESSAGE=\"not implemented yet\"\n  return 1\n}\n\n# --- The request.\n\nhandle_request() {\n  [ -f \"$REQUEST_FILE\" ] || return 0\n  REQUEST= ACTION= OBJECT= NOT_AFTER=\n  set -a\n  . \"$REQUEST_FILE\"\n  set +a\n  # The ID names a file: letters, digits, \"-\" and \"_\" only.\n  case \"$REQUEST\" in ''|*[!A-Za-z0-9_-]*) return 0 ;; esac\n  case \"$NOT_AFTER\" in ''|*[!0-9]*) return 0 ;; esac\n  [ \"$NOT_AFTER\" -ge \"$(date +%s)\" ] || return 0\n  [ ! -e \"$DATA/$REQUEST.result\" ] || return 0\n\n  mkdir -p \"$DATA\" || return 0\n  DETAIL=\"$DATA/.$REQUEST.detail\"\n  rm -f \"$DETAIL\"\n  MESSAGE=\n  case \"$ACTION\" in\n    backup) if do_backup; then finish succeeded; else finish failed; fi ;;\n    restore) if do_restore; then finish succeeded; else finish failed; fi ;;\n    *) MESSAGE=\"unknown action\"; finish failed ;;\n  esac\n}\n\nmeasure_sizes\n( handle_request )\nexit 0\n"]
        entrypoint = ["/bin/sh", "-c"]
        image = "rclone/rclone@sha256:3103526c506266a9ecdf064efe99bf3677d92ef6407af124d8c56b4f49cbaa51"
      }
      resources {
        cpu = 50
        memory = 32
        memory_max = 512
      }
      volume_mount {
        volume = "data"
        destination = "/caravana-volumes/data"
        read_only = true
      }
      template {
        data = "{{ with nomadVar \"nomad/jobs/kuma/web/caravana-volumes\" }}{{ range .Tuples }}{{ .K }}='{{ .V | replaceAll \"'\" \"'\\\\''\" }}'\n{{ end }}{{ end }}"
        destination = "local/request.env"
        env = false
        change_mode = "noop"
      }
    }
  }
}
