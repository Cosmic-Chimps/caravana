# Traefik v3 for the local lab. It reads Nomad service tags (Nomad provider) and routes by host
# name. Hand-written lab infrastructure; Caravana's own jobs are generated as JSON.
#
# Locally there's no ACME unless the gate's certificates step points the resolver at a Pebble server
# (feature 034): routers with `tls=true` get Traefik's self-signed default cert. The static
# configuration, the uploaded certificates and Team only are rendered as the host's job does
# (deploy/install/traefik.nomad.hcl); keep the two the same apart from what this header names: no
# redirect to HTTPS, the dashboard on every interface, no acme volume and no console route.

job "traefik" {
  type = "system" # one instance on every client node

  # Feature 034: Caravana reads this to know the job renders caravana/acme and serves uploaded
  # certificates.
  meta {
    caravana_certificates = "1"
  }

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
      # policy in traefik-policy.hcl, attached to this job by lab.sh before it runs it (without
      # it the templates of caravana/ wait and the task never starts). No static token needed.
      identity {
        env = true
      }

      # Feature 034, Traefik's static configuration, rendered into secrets/ as on a host (see
      # deploy/install/traefik.nomad.hcl for the why). Without the variable caravana/acme the
      # resolver points at Let's Encrypt's staging issuer, which no router names unless Caravana
      # runs with CARAVANA_CERT_RESOLVER=letsencrypt (the gate's certificates step does, with a
      # Pebble server as the issuer). Each issuer's store file is in local/: the lab has no acme
      # volume.
      template {
        destination = "secrets/traefik.yml"
        change_mode = "restart"
        data        = <<-YAML
          entryPoints:
            web:
              address: ":80"
            websecure:
              address: ":443"
            traefik:
              address: ":8081" # dashboard without auth, published on the Mac's 8088; lab only
          api:
            dashboard: true
            insecure: true
          providers:
            nomad:
              exposedByDefault: false
              # Follow Nomad's event stream instead of polling every 15 s, so a route is
              # dropped within the translator's 5 s shutdown_delay when an allocation stops.
              watch: true
              endpoint:
                address: "http://127.0.0.1:4646"
            file:
              directory: /secrets/dynamic
              watch: true
          # Feature 025: request counts and durations per service, on the traefik entrypoint
          # (/metrics next to the dashboard). The buckets are finer than Traefik's default
          # (0.1, 0.3, 1.2, 5), which would read any fast app as 95 ms: with these a p95 of
          # 40 ms stays 25-50 ms. Caravana reads them once a minute (internal/metrics).
          metrics:
            prometheus:
              buckets: [0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10]
          # Feature 047: the access log, one JSON line per request on Traefik's stdout, which
          # Nomad keeps in alloc/logs/traefik.stdout.<n> (the logs block below says how much)
          # and the Requests page reads back through Nomad's fs API. Only the fields the page
          # needs are kept: no header, and no query string (tokens travel in them).
          accessLog:
            format: json
            fields:
              defaultMode: drop
              names:
                StartUTC: keep
                RouterName: keep
                ServiceName: keep
                RequestMethod: keep
                RequestHost: keep
                RequestPath: keep
                DownstreamStatus: keep
                Duration: keep
                DownstreamContentSize: keep
                ClientHost: keep
                TLSVersion: keep
                RetryAttempts: keep
                entryPointName: keep
              headers:
                defaultMode: drop
              queryParameters:
                defaultMode: drop
          log:
            level: INFO
            filePath: /dev/stderr # Traefik's own log on stderr, so stdout is the access log alone
          certificatesResolvers:
            letsencrypt:
              acme:
                httpChallenge:
                  entryPoint: web
          {{- $server := "" }}{{ $email := "" }}{{ $storage := "" }}{{ $kid := "" }}{{ $hmac := "" }}{{ $ca := "" }}
          {{- if nomadVarExists "caravana/acme" }}
          {{- with nomadVar "caravana/acme" }}
          {{- with index . "server" }}{{ $server = printf "%s" . }}{{ end }}
          {{- with index . "email" }}{{ $email = printf "%s" . }}{{ end }}
          {{- with index . "storage" }}{{ $storage = printf "%s" . }}{{ end }}
          {{- with index . "eab_kid" }}{{ $kid = printf "%s" . }}{{ end }}
          {{- with index . "eab_hmac" }}{{ $hmac = printf "%s" . }}{{ end }}
          {{- with index . "ca" }}{{ $ca = printf "%s" . }}{{ end }}
          {{- end }}
          {{- end }}
          {{- if eq $server "" }}
                caServer: "https://acme-staging-v02.api.letsencrypt.org/directory"
                storage: /local/acme.json
          {{- else }}
                caServer: {{ $server | toJSON }}
                email: {{ $email | toJSON }}
                storage: {{ printf "/local/%s" $storage | toJSON }}
          {{- if ne $kid "" }}
                eab:
                  kid: {{ $kid | toJSON }}
                  hmacEncoded: {{ $hmac | toJSON }}
          {{- end }}
          {{- if ne $ca "" }}
                caCertificates: [/secrets/acme-ca.pem]
                caSystemCertPool: true
          {{- end }}
          {{- end }}
          YAML
      }

      # The custom issuer's own CA, when it has one (an issuer with a private CA, or the lab's
      # Pebble): Traefik trusts it for the ACME server's HTTPS. Empty otherwise.
      template {
        destination = "secrets/acme-ca.pem"
        change_mode = "restart"
        data        = <<-PEM
          {{- if nomadVarExists "caravana/acme" }}{{ with nomadVar "caravana/acme" }}{{ with index . "ca" }}{{ printf "%s" . }}{{ end }}{{ end }}{{ end }}
          PEM
      }

      # Feature 034, uploaded certificates: one Nomad Variable per certificate under
      # caravana/certificates/ (items certificate, key and names), written by Caravana. Traefik
      # serves each for every name it covers, and doesn't ask the ACME issuer for those names
      # (it skips a name its store already holds). noop: the provider watches the directory, so
      # an upload, a replacement or a removal needs no restart. secrets/: the keys are in this
      # file. Without a certificate the file is {}.
      template {
        destination = "secrets/dynamic/certificates.yml"
        change_mode = "noop"
        data        = <<-YAML
          {{- $any := false }}
          {{- range nomadVarList "caravana/certificates" }}{{ $any = true }}{{ end }}
          {{- if $any }}
          tls:
            certificates:
          {{- range nomadVarList "caravana/certificates" }}
          {{- with nomadVar .Path }}
              - certFile: {{ printf "%s" (index . "certificate") | toJSON }}
                keyFile: {{ printf "%s" (index . "key") | toJSON }}
          {{- end }}
          {{- end }}
          {{- else }}
          {}
          {{- end }}
          YAML
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
        destination = "secrets/dynamic/team.yml"
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
        # Feature 034: the static configuration is the file the first template renders.
        args = ["--configFile=/secrets/traefik.yml"]
      }

      # The access log's files: 10 of 20 MB, about 500,000 requests, before the oldest is
      # purged (feature 047). Traefik's own log is on stderr, kept the same way.
      logs {
        max_files     = 10
        max_file_size = 20
      }

      resources {
        cpu    = 100
        memory = 128
      }
    }
  }
}
