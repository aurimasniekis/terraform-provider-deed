# Endpoints by entity ID …
resource "deed_relationship" "jellyfin_runs_on_pve" {
  type = "runs_on"
  from = { id = deed_entity.jellyfin.id }
  to   = { id = deed_entity.pve.id }
}

# … or by kind and name (the declaring project is then required).
resource "deed_relationship" "ups_powers_nas" {
  project    = deed_project.homelab.name
  type       = deed_relationship_type.powers.name
  from       = { kind = "example.com/ups", name = "ups-01" }
  to         = { kind = "server", name = "nas-01" }
  attributes = jsonencode({ outlet = 3 })
}
