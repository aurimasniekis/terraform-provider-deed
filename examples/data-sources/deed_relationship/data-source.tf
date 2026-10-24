data "deed_relationship" "jellyfin_host" {
  type    = "runs_on"
  from_id = deed_entity.jellyfin.id
  to_id   = deed_entity.pve.id
}

output "asserted_by" {
  value = data.deed_relationship.jellyfin_host.sources
}
