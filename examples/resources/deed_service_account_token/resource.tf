resource "deed_service_account_token" "dashboards" {
  service_account    = deed_service_account.dashboards.name
  name               = "grafana"
  expires_in_days    = 366
  rotate_before_days = 30 # replaced on the first apply within 30 days of expiry

  # Create the new token before the old one is revoked.
  lifecycle {
    create_before_destroy = true
  }
}

# Hand the token to whatever needs it, for example a secret manager.
output "dashboards_token" {
  value     = deed_service_account_token.dashboards.token
  sensitive = true
}
