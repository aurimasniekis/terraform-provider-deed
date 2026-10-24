# The effective view: every source merged, including observed status.
data "deed_entity" "nas" {
  project = "homelab"
  kind    = "server"
  name    = "nas-01"
}

output "nas_health" {
  value = data.deed_entity.nas.health
}

output "nas_observed" {
  value = jsondecode(data.deed_entity.nas.status).observed
}
