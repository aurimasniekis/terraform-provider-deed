# GitHub connector: the token is write-only (never stored in state); bump secrets_wo_version to rotate it.
resource "deed_source" "github" {
  project       = deed_project.homelab.name
  name          = "github"
  provider_type = "github"
  config = jsonencode({
    owners        = ["example-org"]
    pull_requests = true
    issues        = true
  })
  secrets_wo         = { token = var.github_token }
  secrets_wo_version = 1
  schedule_seconds   = 900
}

# Terraform state read from an S3-compatible backend on the LAN.
resource "deed_source" "core_state" {
  project       = deed_project.homelab.name
  name          = "core-state"
  provider_type = "terraform_state"
  config = jsonencode({
    backend            = "s3"
    s3                 = { endpoint = "minio.example.com:9000", bucket = "tfstate", key = "core/terraform.tfstate" }
    workspace          = "core"
    infer_dependencies = true
    allow_cidrs        = ["192.168.10.0/24"]
  })
  secrets_wo = {
    s3_access_key_id     = var.state_reader_key_id
    s3_secret_access_key = var.state_reader_secret
  }
  secrets_wo_version = 1
  policy             = jsonencode({ on_absent = "tombstone", max_removal_percent = 25 })
}

# Pulse admin adapter (declared publish/subscribe grants and durable consumers).
resource "deed_source" "pulse" {
  project       = deed_project.homelab.name
  name          = "pulse"
  provider_type = "pulse_admin"
  config = jsonencode({
    admin_url     = "http://10.0.0.5:8080"
    insecure_http = true
    allow_cidrs   = ["10.0.0.0/24"]
  })
  secrets_wo         = { token = var.pulse_admin_token }
  secrets_wo_version = 1
}

# Push source fed by CI with source tokens (created in the UI or with the API).
resource "deed_source" "ci" {
  project                   = deed_project.homelab.name
  name                      = "ci-jobs"
  provider_type             = "push"
  expected_interval_seconds = 3600
}
