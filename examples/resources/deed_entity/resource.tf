resource "deed_entity" "nas" {
  project = deed_project.homelab.name
  kind    = "server"
  name    = "nas-01"

  display_name    = "NAS"
  description     = "Primary network storage."
  environment     = "homelab"
  owner           = "platform" # a group entity name or ID
  lifecycle_stage = "active"
  icon            = "tabler:server"

  labels = {
    tier = "storage"
  }
  annotations = {
    "example.com/rack" = "rack-1, unit 4"
  }
  links = [
    { type = "dashboard", title = "Grafana", url = "https://grafana.example.com/d/nas-01" },
    { type = "runbook", url = "https://docs.example.com/runbooks/nas-01" },
  ]
  tags = ["storage", "backup-target"]

  version_declared = "7.2.1"
  version_channel  = "stable"

  # Kind-specific properties, validated by Deed against the kind's JSON Schema.
  spec = jsonencode({
    hostname  = "nas-01.home.example.com"
    ipv4      = "192.168.10.20"
    cpu_cores = 4
    memory_mb = 16384
  })

  timeouts = {
    create = "2m"
  }
}
