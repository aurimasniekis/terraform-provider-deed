# Everything the GitHub connector imports belongs to the "github" group.
resource "deed_label_rule" "github" {
  project = "infra"
  name    = "github"
  match   = { sources = ["github"] }
  labels  = { group = "github" }
}

# DNS from any state: records, zones and domains.
resource "deed_label_rule" "dns" {
  project = "infra"
  name    = "dns"
  match   = { kinds = ["dns_*", "domain"] }
  labels  = { group = "dns" }
}

# One state holds both DNS records and Cloudflare rulesets: tell them apart by the Terraform resource type label.
resource "deed_label_rule" "cloudflare" {
  project     = "infra"
  name        = "cloudflare-rulesets"
  description = "Rulesets of the Cloudflare stacks"
  match = {
    sources = ["tf-cloudflare-*"]
    labels  = { "terraform/type" = "cloudflare_ruleset" }
  }
  labels = { group = "cloudflare" }
}
