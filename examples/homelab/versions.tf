terraform {
  required_version = ">= 1.11"

  required_providers {
    deed = {
      source = "aurimasniekis/deed"
    }
  }
}

# Endpoint and token come from DEED_ENDPOINT and DEED_TOKEN (an installation admin token: the scenario creates a
# project, a custom kind and a custom relationship type).
provider "deed" {}
