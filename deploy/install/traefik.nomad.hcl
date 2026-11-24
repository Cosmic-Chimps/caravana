# Traefik v3 for a Caravana host, run by deploy/install/install.sh. Same as the lab's, plus
# Let's Encrypt (HTTP-01), HTTP→HTTPS redirects, certificates on a host volume, the dashboard
# on loopback, and one static route: the console domain, to Caravana (feature 012, R5). Apps
# get their routes from Nomad service tags.
#
# The access log is on (feature 047): one JSON line per request on Traefik's stdout, which
# Nomad keeps (see the logs block) and the console's Requests page reads back. Webhook tokens
# and session ids travel in query strings and headers, so Traefik is told to drop every header
# and every query parameter and to keep only the fields the page shows (the allowlist below);
# the line holds the path alone. Traefik's own log goes to stderr, so stdout is the access log
# and nothing else. Don't widen the kept fields without reading docs/requests.md.

variable "console_domain" {
  type        = string
  description = "The console's domain, e.g. caravana.example.com; empty for no console route"
  default     = ""
}

variable "acme" {
  type        = string
  description = "production or staging (Let's Encrypt), or none (Traefik's default certificate)"
  default     = "production"
}

variable "acme_email" {
  type        = string
  description = "Let's Encrypt account email"
  default     = ""
}

locals {
  acme_ca  = var.acme == "staging" ? "https://acme-staging-v02.api.letsencrypt.org/directory" : "https://acme-v02.api.letsencrypt.org/directory"
  resolver = var.acme == "none" ? "{}" : "{certResolver: letsencrypt}"
}

job "traefik" {
  type = "system" # one instance on every client node that passes the constraint below

  # Feature 034: Caravana reads this to know the job renders its static configuration from
  # caravana/acme and serves uploaded certificates. A job from before has neither.
  meta {
    caravana_certificates = "1"
  }

  # One ingress: Traefik runs on the first server only, whose Nomad client has
  # meta { caravana_ingress = "true" } (the installer's --first sets it). Let's Encrypt's
  # HTTP-01 challenge must reach the server that holds the certificates, and a
  # joined server's client has no such meta, so a system job would otherwise start a
  # second Traefik there, with its own empty certificate store (feature 031, research D6).
  constraint {
    attribute = "${meta.caravana_ingress}"
    value     = "true"
  }

  group "traefik" {
    network {
      mode = "host"
      port "web" { static = 80 }
      port "websecure" { static = 443 }
    }

    volume "acme" {
      type   = "host"
      source = "traefik-acme"
    }

    task "traefik" {
      driver = "docker"

      # Puts a workload identity token in NOMAD_TOKEN. What it may read is set by the ACL
      # policy traefik-read (traefik-policy.hcl), which install.sh attaches to this job before
      # it runs it: without the policy the templates of caravana/ wait and the task never starts.
      identity {
        env = true
      }

      volume_mount {
        volume      = "acme"
        destination = "/acme"
      }

      # Feature 034, Traefik's static configuration. It is rendered into secrets/ (Nomad's fs API
      # can't read that folder) from the job's variables and, when the owner chose another ACME
      # issuer in Settings › Certificates, from the Nomad Variable caravana/acme, which the
      # job-scoped policy traefik-read lets this task read. restart: a new issuer restarts the
      # task once, in place (a few seconds without HTTP). Without the variable it is the issuer
      # the installer chose (--acme), as before. Each issuer keeps its account and certificates
      # in its own file of the acme volume (the variable's item storage), so going back to an
      # issuer finds its certificates. The resolver keeps the name letsencrypt whatever the
      # issuer: every deployed router names it. The Nomad provider takes the task's NOMAD_TOKEN
      # by itself. Every item of caravana/acme is always written (Caravana writes "" for unset),
      # and an item prints as its value but is no string, so each goes through printf.
      template {
        destination = "secrets/traefik.yml"
        change_mode = "restart"
        data        = <<-YAML
          entryPoints:
            web:
              address: ":80"
              http:
                redirections:
                  entryPoint:
                    to: websecure
                    scheme: https
            websecure:
              address: ":443"
            traefik:
              # Dashboard and API on loopback only; reach them through an SSH tunnel.
              address: "127.0.0.1:8081"
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
                caServer: "${local.acme_ca}"
                email: "${var.acme_email}"
                storage: /acme/acme.json
          {{- else }}
                caServer: {{ $server | toJSON }}
                email: {{ $email | toJSON }}
                storage: {{ printf "/acme/%s" $storage | toJSON }}
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

      # The one route not made from Nomad service tags: the console's domains, to Caravana's
      # listener on loopback (console, API and webhooks). Traefik runs in the host's network,
      # so 127.0.0.1 is the host's loopback, which Caravana trusts as its proxy. The installer
      # adds the domain only once the admin exists. The router's priority is far above any
      # app's (Traefik's default is the rule's length), so an app that was given the console's
      # domain can never take the console's requests; Caravana also refuses such a domain.
      #
      # Feature 051 (change the console's address from the console): the domains are the item
      # "hosts" of the variable nomad/jobs/traefik/traefik, a space-separated list Caravana
      # writes (the current domain, a new one being checked, and the old ones that still
      # redirect). Traefik's job may read its own group's variable with no policy line. Without
      # that variable, or while it has no hosts, the route is var.console_domain, as the
      # installer set it, so a host that never changes its address behaves as before. noop: the
      # file is rewritten without restarting the task and the file provider watches the
      # directory, so a change of address never costs a gap on 80 and 443. One router per host
      # (caravana, caravana-1, ...), so each gets its own certificate and a domain whose
      # certificate isn't issued yet can't hold up the current one. An item is a struct that
      # prints as its value, not a string: compared with "" it fails to render and kills the
      # task (lab, 2026-11-23), so it goes through printf first. The HCL interpolations
      # (${...}) are replaced when the job is registered; the {{ }} ones when the file is rendered.
      template {
        destination = "secrets/dynamic/caravana.yml"
        change_mode = "noop"
        data        = <<-YAML
          {{- $hosts := "" }}
          {{- if nomadVarExists "nomad/jobs/traefik/traefik" }}
          {{- with nomadVar "nomad/jobs/traefik/traefik" }}{{ with index . "hosts" }}{{ $hosts = printf "%s" . }}{{ end }}{{ end }}
          {{- end }}
          {{- if eq $hosts "" }}{{ $hosts = "${var.console_domain}" }}{{ end }}
          {{- if eq $hosts "" }}
          {}
          {{- else }}
          http:
            routers:
          {{- range $i, $host := split " " $hosts }}
              caravana{{ if $i }}-{{ $i }}{{ end }}:
                rule: "Host(`{{ $host }}`)"
                priority: 10000
                entryPoints: [websecure]
                tls: ${local.resolver}
                service: caravana
          {{- end }}
            services:
              caravana:
                loadBalancer:
                  servers:
                    - url: "http://127.0.0.1:7070"
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
        # Feature 034: the whole static configuration is the file the first template below
        # renders. Traefik reads one source, a file or flags, never both, and the issuer's EAB key
        # can't be a flag (an empty one would break Let's Encrypt, a set one would be in the
        # process list).
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
