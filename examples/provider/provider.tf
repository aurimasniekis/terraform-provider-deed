terraform {
  required_providers {
    deed = {
      source = "aurimasniekis/deed"
    }
  }
}

# The endpoint and token can also come from DEED_ENDPOINT and DEED_TOKEN.
provider "deed" {
  endpoint = "https://deed.example.com"
  token    = var.deed_token # a user API token (deed_at_…) created by a signed-in user

  # Optional: the Deed source every entity and relationship is declared through (default "terraform", or DEED_SOURCE).
  # source_name = "terraform"

  # Optional: trust a private CA in addition to the system roots (or DEED_CA_FILE).
  # ca_file = "/etc/ssl/private-ca.pem"
}

variable "deed_token" {
  type      = string
  sensitive = true
}
