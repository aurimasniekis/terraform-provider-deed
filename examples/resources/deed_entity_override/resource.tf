# A repository the GitHub connector imports: a friendlier name, an owner and a label, without declaring it.
resource "deed_entity_override" "app_repo" {
  project      = "infra"
  kind         = "repository"
  name         = "example-org-app"
  display_name = "App"
  owner        = "platform"
  labels       = { tier = "core" }
}

# By entity ID, correcting an imported spec value.
resource "deed_entity_override" "nas" {
  entity_id = "ent_01h0000000000000000000000a"
  icon      = "simple-icons:synology"
  spec      = jsonencode({ os = "DSM 7.2" })
}
