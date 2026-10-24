resource "deed_project_member" "alice" {
  project = deed_project.homelab.name
  user_id = "usr_01m5qxa3jpemn9k3x8n16ftjc3"
  role    = "editor" # viewer, editor or admin
}
