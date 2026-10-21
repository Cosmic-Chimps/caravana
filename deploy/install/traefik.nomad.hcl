# Traefik v3 for a Caravana host, run by deploy/install/install.sh. Same as the lab's, plus
# Let's Encrypt (HTTP-01), HTTP→HTTPS redirects, certificates on a host volume, the dashboard
# on loopback, and one static route: the console domain, to Caravana (feature 012, R5). Apps
# get their routes from Nomad service tags.
#
# The access log stays off on purpose: webhook tokens can travel in query strings, and Traefik
# would write full URLs to it. Don't enable it without dropping query strings.

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

      # The one route not made from Nomad service tags: the console domain, to Caravana's
      # listener on loopback (console, API and webhooks). Traefik runs in the host's network,
      # so 127.0.0.1 is the host's loopback, which Caravana trusts as its proxy. The installer
      # adds the domain only once the admin exists. The router's priority is far above any
      # app's (Traefik's default is the rule's length), so an app that was given the console's
      # domain can never take the console's requests; Caravana also refuses such a domain.
      template {
        destination = "local/caravana.yml"
        data        = var.console_domain == "" ? "{}\n" : <<-YAML
          http:
            routers:
              caravana:
                rule: "Host(`${var.console_domain}`)"
                priority: 10000
                entryPoints: [websecure]
                tls: ${local.resolver}
                service: caravana
            services:
              caravana:
                loadBalancer:
                  servers:
                    - url: "http://127.0.0.1:7070"
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
          # Dashboard and API on loopback only; reach them through an SSH tunnel.
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
          "--providers.file.filename=/local/caravana.yml",
          "--certificatesresolvers.letsencrypt.acme.email=${var.acme_email}",
          "--certificatesresolvers.letsencrypt.acme.storage=/acme/acme.json",
          "--certificatesresolvers.letsencrypt.acme.httpchallenge.entrypoint=web",
          "--certificatesresolvers.letsencrypt.acme.caserver=${local.acme_ca}",
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
