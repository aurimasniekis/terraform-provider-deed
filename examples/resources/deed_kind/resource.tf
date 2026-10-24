resource "deed_kind" "ups" {
  name        = "example.com/ups"
  title       = "UPS"
  description = "Uninterruptible power supply."
  icon        = "tabler:battery-charging"
  color       = "#2f9e44"

  # Reports of a UPS with a known serial number converge on the same entity.
  identity_keys = [["/spec/serial"]]

  # JSON Schema (2020-12) for spec. Changing it publishes a new schema version.
  schema = jsonencode({
    type                 = "object"
    additionalProperties = false
    required             = ["capacity_va"]
    properties = {
      capacity_va = { type = "integer", minimum = 0 }
      serial      = { type = "string" }
      model       = { type = "string" }
    }
  })

  # Publish incompatible schema changes as breaking versions that flag failing entities instead of refusing them.
  # breaking  = true
  # migration = "mark_invalid"
}
