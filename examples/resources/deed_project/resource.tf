resource "deed_project" "homelab" {
  name         = "homelab"
  display_name = "Home lab"
  description  = "Servers, services and repositories of the home lab."

  # Allow plain http:// links (for example to appliances on the LAN).
  allow_http_links = true
}
