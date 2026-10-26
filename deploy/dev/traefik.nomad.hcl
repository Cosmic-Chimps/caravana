# Traefik v3 for the local lab. It reads Nomad service tags (Nomad provider) and routes by host
# name. Hand-written lab infrastructure; Caravana's own jobs are generated as JSON.
#
# Locally there's no ACME: routers with `tls=true` get Traefik's self-signed default cert.

job "traefik" {
  type = "system" # one instance on every client node

  group "traefik" {
    network {
      mode = "host"
      port "web" { static = 80 }
      port "websecure" { static = 443 }
      port "dashboard" { static = 8081 }
    }

    task "traefik" {
      driver = "docker"

      # Puts a workload identity token in NOMAD_TOKEN. What it may read is set by the ACL
      # policy in traefik-policy.hcl, attached to this job by lab.sh. No static token needed.
      identity {
        env = true
      }

      # Feature 023, Team only: each domain's basic-auth users are an item of the variable
      # nomad/jobs/traefik (key <router>-auth, value team:<bcrypt hash>), written by Caravana.
      # Traefik's own job may read that variable with no policy line. The file provider turns
      # every item into the middleware <router>-auth@file, which the app's router names; a
      # router naming a middleware that doesn't exist is disabled, so Caravana writes the
      # variable before it registers the app. noop: the file is rewritten without restarting
      # the task, and the provider watches the directory. A missing variable renders an empty
      # file, so the task never waits for it. The marker item CARAVANA_TEAM (no -auth suffix)
      # keeps the variable from being empty and is skipped. Without any -auth item the file is
      # `{}`: a file with `http:` and nothing under it is refused by Traefik (the old
      # middlewares would stay), and a template that fails to render kills the task.
      template {
        destination = "local/dynamic/team.yml"
        change_mode = "noop"
        data        = <<-YAML
          {{- $any := false }}
          {{- if nomadVarExists "nomad/jobs/traefik" }}
          {{- with nomadVar "nomad/jobs/traefik" }}
          {{- range $name, $users := . }}
          {{- if regexMatch "-auth$" $name }}{{ $any = true }}{{ end }}
          {{- end }}
          {{- end }}
          {{- end }}
          {{- if $any }}
          http:
            middlewares:
          {{- with nomadVar "nomad/jobs/traefik" }}
          {{- range $name, $users := . }}
          {{- if regexMatch "-auth$" $name }}
              {{ $name }}:
                basicAuth:
                  realm: caravana
                  removeHeader: true
                  users:
                    - {{ $users | toJSON }}
          {{- end }}
          {{- end }}
          {{- end }}
          {{- else }}
          {}
          {{- end }}
          YAML
      }

      config {
        image        = "traefik:v3.7.13"
        network_mode = "host"
        args = [
          "--entrypoints.web.address=:80",
          "--entrypoints.websecure.address=:443",
          "--entrypoints.traefik.address=:8081",
          "--api.dashboard=true",
          "--api.insecure=true", # dashboard on :8081 without auth; lab only
          "--providers.nomad=true",
          "--providers.nomad.exposedByDefault=false",
          "--providers.nomad.endpoint.address=http://127.0.0.1:4646",
          "--providers.nomad.endpoint.token=${NOMAD_TOKEN}",
          # Follow Nomad's event stream instead of polling every 15 s, so a route is
          # dropped within the translator's 5 s shutdown_delay when an allocation stops.
          "--providers.nomad.watch=true",
          "--providers.file.directory=/local/dynamic",
          "--providers.file.watch=true",
          # Feature 025: request counts and durations per service, on the traefik entrypoint
          # (/metrics next to the dashboard). The buckets are finer than Traefik's default
          # (0.1, 0.3, 1.2, 5), which would read any fast app as 95 ms: with these a p95 of 40 ms
          # stays 25-50 ms. Caravana reads them once a minute (internal/metrics).
          "--metrics.prometheus=true",
          "--metrics.prometheus.buckets=0.005,0.01,0.025,0.05,0.1,0.25,0.5,1,2.5,5,10",
          "--log.level=INFO",
        ]
      }

      resources {
        cpu    = 100
        memory = 128
      }
    }
  }
}
