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
