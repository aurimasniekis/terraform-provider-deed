resource "deed_service_account" "terraform" {
  name              = "terraform"
  description       = "tf-infra"
  installation_role = "admin" # admin, auditor, or omit for project memberships only
}

# A read-only account for dashboards: no installation role, viewer in one project.
resource "deed_service_account" "dashboards" {
  name = "dashboards"
}

resource "deed_project_member" "dashboards" {
  project = deed_project.homelab.name
  user_id = deed_service_account.dashboards.id
  role    = "viewer"
}
