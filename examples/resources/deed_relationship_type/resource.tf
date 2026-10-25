resource "deed_relationship_type" "powers" {
  name         = "example.com/powers"
  inverse_name = "powered_by"
  title        = "Powers"
  description  = "Electrical power supply."
  from_kinds   = ["example.com/ups"]
  to_kinds     = ["server", "storage", "network"]
  impact       = "reverse" # what it powers depends on the UPS: losing the UPS affects them

  attributes_schema = jsonencode({
    type = "object"
    properties = {
      outlet = { type = "integer", minimum = 1 }
    }
  })
}
