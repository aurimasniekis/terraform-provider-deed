output "project_id" {
  value = deed_project.homelab.id
}

output "entities" {
  description = "Entity IDs by name."
  value = {
    for e in [deed_entity.platform, deed_entity.pve, deed_entity.nas, deed_entity.ups, deed_entity.repo, deed_entity.jellyfin] :
    "${e.kind}/${e.name}" => e.id
  }
}

output "ups_schema_version" {
  value = deed_kind.ups.storage_version
}
