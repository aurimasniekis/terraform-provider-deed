# A small home lab described in Deed: two servers, a UPS (custom kind), a media service, the repository it is
# deployed from and the team that owns everything, connected by relationships.

resource "deed_project" "homelab" {
  name         = var.project
  display_name = "Home lab"
  description  = "Everything that runs at home."
}

# --- Custom catalog schema --------------------------------------------------------------------------------------------

resource "deed_kind" "ups" {
  name          = "homelab.example.com/ups"
  title         = "UPS"
  description   = "Uninterruptible power supply."
  icon          = "tabler:battery-charging"
  color         = "#2f9e44"
  identity_keys = [["/spec/serial"]]

  schema = jsonencode({
    type                 = "object"
    additionalProperties = false
    required             = ["capacity_va"]
    properties = {
      capacity_va = { type = "integer", minimum = 0 }
      serial      = { type = "string" }
      model       = { type = "string" }
    }
  })
}

resource "deed_relationship_type" "powers" {
  name         = "homelab.example.com/powers"
  inverse_name = "powered_by"
  title        = "Powers"
  from_kinds   = [deed_kind.ups.name]
  to_kinds     = ["server"]

  attributes_schema = jsonencode({
    type       = "object"
    properties = { outlet = { type = "integer", minimum = 1 } }
  })
}

# --- Entities ---------------------------------------------------------------------------------------------------------

resource "deed_entity" "platform" {
  project      = deed_project.homelab.name
  kind         = "group"
  name         = "platform"
  display_name = "Platform team"
  spec         = jsonencode({ type = "team", email = "platform@example.com" })
}

resource "deed_entity" "pve" {
  project         = deed_project.homelab.name
  kind            = "server"
  name            = "pve-01"
  display_name    = "Proxmox host"
  environment     = "homelab"
  owner           = deed_entity.platform.name
  lifecycle_stage = "active"
  icon            = "simple-icons:proxmox"
  labels          = { tier = "compute" }
  tags            = ["hypervisor"]

  spec = jsonencode({
    hostname  = "pve-01.home.example.com"
    ipv4      = "192.168.10.10"
    cpu_cores = 16
    memory_mb = 65536
    os        = "Proxmox VE 8"
  })
}

resource "deed_entity" "nas" {
  project         = deed_project.homelab.name
  kind            = "server"
  name            = "nas-01"
  display_name    = "NAS"
  environment     = "homelab"
  owner           = deed_entity.platform.name
  lifecycle_stage = "active"
  labels          = { tier = "storage" }
  tags            = ["storage", "backup-target"]

  spec = jsonencode({
    hostname  = "nas-01.home.example.com"
    ipv4      = "192.168.10.20"
    cpu_cores = 4
    memory_mb = 16384
  })
}

resource "deed_entity" "ups" {
  project      = deed_project.homelab.name
  kind         = deed_kind.ups.name
  name         = "ups-01"
  display_name = "Rack UPS"
  owner        = deed_entity.platform.name
  spec         = jsonencode({ capacity_va = 1500, serial = "UPS-0001", model = "Example 1500" })
}

resource "deed_entity" "repo" {
  project      = deed_project.homelab.name
  kind         = "repository"
  name         = "homelab-infra"
  display_name = "homelab-infra"
  description  = "Terraform and Ansible for the home lab."
  owner        = deed_entity.platform.name

  links = [
    { type = "repository", title = "GitHub", url = "https://github.com/example/homelab-infra" },
  ]

  spec = jsonencode({
    url            = "https://github.com/example/homelab-infra"
    provider       = "github"
    full_name      = "example/homelab-infra"
    default_branch = "main"
  })
}

resource "deed_entity" "jellyfin" {
  project          = deed_project.homelab.name
  kind             = "service"
  name             = "jellyfin"
  display_name     = "Jellyfin"
  environment      = "homelab"
  owner            = deed_entity.platform.name
  lifecycle_stage  = "active"
  icon             = "simple-icons:jellyfin"
  labels           = { tier = "media" }
  version_declared = "10.10.3"
  version_policy   = "latest-minor"

  links = [
    { type = "dashboard", title = "Grafana", url = "https://grafana.example.com/d/jellyfin/jellyfin" },
    { type = "docs", url = "https://docs.example.com/homelab/jellyfin" },
  ]

  spec = jsonencode({
    url      = "https://media.home.example.com"
    port     = 8096
    protocol = "https"
  })
}

# --- Relationships ----------------------------------------------------------------------------------------------------

resource "deed_relationship" "jellyfin_runs_on_pve" {
  type = "runs_on"
  from = { id = deed_entity.jellyfin.id }
  to   = { id = deed_entity.pve.id }
}

resource "deed_relationship" "jellyfin_deployed_from_repo" {
  type = "deployed_from"
  from = { id = deed_entity.jellyfin.id }
  to   = { id = deed_entity.repo.id }
}

resource "deed_relationship" "jellyfin_stores_on_nas" {
  type       = "stored_in"
  from       = { id = deed_entity.jellyfin.id }
  to         = { id = deed_entity.nas.id }
  attributes = jsonencode({ path = "/volume1/media" })
}

resource "deed_relationship" "owned_by" {
  for_each = {
    pve      = deed_entity.pve.id
    nas      = deed_entity.nas.id
    ups      = deed_entity.ups.id
    jellyfin = deed_entity.jellyfin.id
  }

  type = "owned_by"
  from = { id = each.value }
  to   = { id = deed_entity.platform.id }
}

resource "deed_relationship" "ups_powers" {
  for_each = {
    pve = { id = deed_entity.pve.id, outlet = 1 }
    nas = { id = deed_entity.nas.id, outlet = 2 }
  }

  type       = deed_relationship_type.powers.name
  from       = { id = deed_entity.ups.id }
  to         = { id = each.value.id }
  attributes = jsonencode({ outlet = each.value.outlet })
}
