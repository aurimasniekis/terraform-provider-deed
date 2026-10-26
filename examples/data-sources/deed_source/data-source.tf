data "deed_source" "github" {
  project = "homelab"
  name    = "github"
}

# Fail the run when the connector reports an error.
check "github_sync" {
  assert {
    condition     = data.deed_source.github.last_error == ""
    error_message = "GitHub sync failed: ${data.deed_source.github.last_error}"
  }
}
