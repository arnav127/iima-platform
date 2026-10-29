# The core service (sign-in, /auth/verify, roles, /admin). Written by hand because core is the one
# service jobgen does not cover: Traefik reaches it on the "internal" entry point, not by app path.
#
#   nomad job run -var version=v0.3.0 -var artifact=<url of core tar.gz> -var sha256=<hash> core.nomad.hcl
#
# The artifact holds ./core, ./apps (the hand-made registry at that release) and ./cluster.toml
# (deploy/nomad/cluster.toml). Secrets live in the Nomad Variable nomad/jobs/core:
#   GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET   sign-in
#   NOMAD_TOKEN                              a token under the deploy policy, for the deploy portal
# The deploy portal's GitHub App is created from /admin/github by a super admin and kept in core's
# database (or set GITHUB_APP_ID, GITHUB_APP_SLUG, GITHUB_APP_PRIVATE_KEY, GITHUB_WEBHOOK_SECRET in the
# variable instead). Nomad clients download app builds from core on its allocated port, so the
# firewall must let the cluster reach it (it does: cluster traffic is open between nodes).
#
# Admin commands run inside the allocation, against the same database:
#   nomad alloc exec -task core <alloc id> local/core superadmin add someone@iima.ac.in

variable "version" {
  type = string
}

variable "artifact" {
  type = string
}

variable "sha256" {
  type = string
}

variable "public_url" {
  type    = string
  default = "https://students.iima.ac.in"
}

job "core" {
  datacenters = ["iima"]
  type        = "service"

  meta {
    version = var.version
  }

  update {
    max_parallel      = 1
    health_check      = "checks"
    min_healthy_time  = "10s"
    healthy_deadline  = "2m"
    progress_deadline = "3m"
    auto_revert       = true
  }

  group "core" {
    count = 1 # SQLite: one instance until core moves to Postgres (phase 7)

    shutdown_delay = "5s" # leave Traefik's routing before stopping

    # "fail", not "delay": a crashing core fails its allocation, so a bad deploy reverts.
    restart {
      attempts = 2
      interval = "5m"
      delay    = "10s"
      mode     = "fail"
    }

    network {
      port "http" {}
    }

    # Sessions, roles, signing keys. The volume pins core to the node that holds it.
    volume "data" {
      type            = "host"
      source          = "core-data"
      access_mode     = "single-node-single-writer"
      attachment_mode = "file-system"
    }

    service {
      name     = "core"
      provider = "nomad"
      port     = "http"
      # Only on the internal entry point: Traefik itself calls core there, for forward auth and
      # for core's public routes (defined in Traefik's file config, so no job can claim /auth).
      tags = [
        "traefik.enable=true",
        "traefik.http.routers.core-internal.entrypoints=internal",
        "traefik.http.routers.core-internal.rule=PathPrefix(`/`)",
        "traefik.http.routers.core-internal.service=core",
      ]
      check {
        type     = "http"
        path     = "/health"
        interval = "10s"
        timeout  = "3s"
      }
    }

    task "core" {
      driver = "exec"

      artifact {
        source      = var.artifact
        destination = "local/"
        options {
          checksum = "sha256:${var.sha256}"
        }
      }

      config {
        command = "local/core"
      }

      volume_mount {
        volume      = "data"
        destination = "/data"
      }

      env {
        PUBLIC_URL     = var.public_url
        CORE_DATA      = "/data"
        REGISTRY       = "local/apps"
        CLUSTER_CONFIG = "local/cluster.toml"
        # The local Nomad agent's API; the portal submits app jobs there.
        NOMAD_ADDR = "http://${attr.unique.network.ip-address}:4646"
      }

      template {
        data        = <<-EOT
          {{ with nomadVar "nomad/jobs/core" }}{{ range .Tuples }}{{ .K }}={{ .V | toJSON }}
          {{ end }}{{ end }}
        EOT
        destination = "secrets/core.env"
        env         = true
        change_mode = "restart"
      }

      resources {
        cpu    = 300
        memory = 512 # builds stream through core on their way to Nomad
      }

      kill_timeout = "10s"
    }
  }
}
