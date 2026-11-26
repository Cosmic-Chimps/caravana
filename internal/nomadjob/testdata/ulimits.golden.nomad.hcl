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
        sysctl = {
          "net.core.somaxconn" = "1024"
          "net.ipv4.tcp_keepalive_time" = "600"
        }
        ulimit {
          memlock = "-1:-1"
          nofile = "65536:65536"
        }
      }
      resources {
        cpu = 100
        memory = 64
        memory_max = 256
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
  }
}
