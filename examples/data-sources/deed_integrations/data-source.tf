data "deed_integrations" "prometheus" {
  adapter = "prometheus"
}

output "prometheus_integrations" {
  value = [for i in data.deed_integrations.prometheus.integrations : i.name]
}
