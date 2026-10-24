data "deed_entities" "storage" {
  project = "homelab"
  kind    = "server"
  labels  = { tier = "storage" }
}

output "storage_servers" {
  value = [for e in data.deed_entities.storage.entities : e.name]
}
