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
