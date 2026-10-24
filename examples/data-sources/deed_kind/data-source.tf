data "deed_kind" "server" {
  name = "server"
}

output "server_schema" {
  value = jsondecode(data.deed_kind.server.schema)
}
