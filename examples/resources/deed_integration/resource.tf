# Prometheus through Grafana's data-source proxy, with a Grafana service-account token (Viewer).
resource "deed_integration" "prometheus" {
  name    = "prometheus"
  adapter = "prometheus"
  config = jsonencode({
    url         = "https://grafana.example.com/api/datasources/proxy/uid/prometheus"
    allow_cidrs = ["192.168.0.7/32"] # grafana.example.com resolves to a LAN address
  })
  secrets_wo         = { token = var.grafana_token }
  secrets_wo_version = 1
}

resource "deed_integration" "loki" {
  name    = "loki"
  adapter = "loki"
  config = jsonencode({
    url         = "https://grafana.example.com/api/datasources/proxy/uid/loki"
    allow_cidrs = ["192.168.0.7/32"]
  })
  secrets_wo         = { token = var.grafana_token }
  secrets_wo_version = 1
}

# Links to dashboards and search.
resource "deed_integration" "grafana" {
  name    = "grafana"
  adapter = "grafana"
  config = jsonencode({
    url         = "https://grafana.example.com"
    allow_cidrs = ["192.168.0.7/32"]
  })
  secrets_wo         = { token = var.grafana_token }
  secrets_wo_version = 1
}
