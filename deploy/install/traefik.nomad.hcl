# Traefik v3 for a public Caravana host (deploy/vm). Same as the lab's, plus Let's Encrypt
# (HTTP-01), HTTP→HTTPS redirects, certificates on a host volume, the dashboard on loopback,
# and one static route: the control plane's webhook host (research R5, R6).
#
# The access log stays off on purpose: webhook tokens can travel in query strings, and Traefik
# would write full URLs to it (spec FR-009). Don't enable it without dropping query strings.

variable "webhook_host" {
  type        = string
  description = "Public host name for Caravana's webhook, e.g. hooks.example.com"
}

variable "acme_email" {
  type        = string
  description = "Let's Encrypt account email"
}

variable "acme_ca" {
  type        = string
  description = "ACME directory URL: Let's Encrypt production, or staging for test runs"
  default     = "https://acme-v02.api.letsencrypt.org/directory"
}

job "traefik" {
  type = "system" # one instance on every client node

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
      # policy traefik-read (deploy/dev/traefik-policy.hcl), attached to this job by setup.sh.
      identity {
        env = true
      }

      volume_mount {
        volume      = "acme"
        destination = "/acme"
      }

      # The one route not made from Nomad service tags (plan's Complexity Tracking): only
      # /v1/webhooks/ on the webhook host, to Caravana's webhook-only listener on loopback.
      # Traefik runs in the host's network, so 127.0.0.1 is the host's loopback.
      template {
        destination = "local/webhook.yml"
        data        = <<-YAML
          http:
            routers:
              caravana-webhook:
                rule: "Host(`${var.webhook_host}`) && PathPrefix(`/v1/webhooks/`)"
                entryPoints: [websecure]
                tls:
                  certResolver: letsencrypt
                service: caravana-webhook
            services:
              caravana-webhook:
                loadBalancer:
                  servers:
                    - url: "http://127.0.0.1:7071"
          YAML
      }

      config {
        image        = "traefik:v3.7.13"
        network_mode = "host"
        args = [
          "--entrypoints.web.address=:80",
          "--entrypoints.web.http.redirections.entrypoint.to=websecure",
          "--entrypoints.web.http.redirections.entrypoint.scheme=https",
          "--entrypoints.websecure.address=:443",
          # Dashboard and API on loopback only; reach them through deploy/vm/host.sh tunnel.
          "--entrypoints.traefik.address=127.0.0.1:8081",
          "--api.dashboard=true",
          "--api.insecure=true",
          "--providers.nomad=true",
          "--providers.nomad.exposedByDefault=false",
          "--providers.nomad.endpoint.address=http://127.0.0.1:4646",
          "--providers.nomad.endpoint.token=${NOMAD_TOKEN}",
          # Follow Nomad's event stream instead of polling every 15 s, so a route is
          # dropped within the translator's 5 s shutdown_delay when an allocation stops.
          "--providers.nomad.watch=true",
          "--providers.file.filename=/local/webhook.yml",
          "--certificatesresolvers.letsencrypt.acme.email=${var.acme_email}",
          "--certificatesresolvers.letsencrypt.acme.storage=/acme/acme.json",
          "--certificatesresolvers.letsencrypt.acme.httpchallenge.entrypoint=web",
          "--certificatesresolvers.letsencrypt.acme.caserver=${var.acme_ca}",
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
