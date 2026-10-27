# Health from node_exporter: CPU busy per host (instance label = the entity's hostname), shown on the map's node
# cards. The guard limits it to hosts node_exporter actually scrapes; others show "not applicable" instead of a value.
resource "deed_telemetry_binding" "cpu" {
  name        = "host-cpu"
  title       = "CPU busy"
  integration = deed_integration.prometheus.name
  kinds       = ["vm", "server"]
  query       = "100 * (1 - avg(rate(node_cpu_seconds_total{mode=\"idle\",instance=\"{{instance}}\"}[{{window}}])))"
  params      = [{ name = "instance", type = "label", from = "/spec/hostname", required = true }]
  unit        = "percent"
  role        = "health"
  thresholds  = { warning = 80, critical = 95 }
  guard       = "up{job=\"node\",instance=\"{{instance}}\"}"
  on_map      = true
}

# Error ratio of a service; a service whose 5xx counter has no series yet reads 0 instead of "no data".
resource "deed_telemetry_binding" "errors" {
  name        = "http-error-ratio"
  title       = "Errors"
  integration = deed_integration.prometheus.name
  kinds       = ["service"]
  query       = "sum(rate(http_requests_total{service=\"{{svc}}\",code=~\"5..\"}[{{window}}])) / sum(rate(http_requests_total{service=\"{{svc}}\"}[{{window}}]))"
  params      = [{ name = "svc", type = "label", from = "/metadata/name" }]
  unit        = "ratio"
  role        = "health"
  thresholds  = { warning = 0.01, critical = 0.05 }
  guard       = "http_requests_total{service=\"{{svc}}\"}"
  when_empty  = 0
  on_map      = true
}

# A chart: container memory (cAdvisor) by container name.
resource "deed_telemetry_binding" "container_memory" {
  name        = "container-memory"
  title       = "Container memory"
  integration = deed_integration.prometheus.name
  kinds       = ["service", "container"]
  query       = "sum(container_memory_working_set_bytes{name=\"{{container}}\"})"
  params      = [{ name = "container", type = "label", from = "/metadata/name" }]
  shape       = "timeseries"
  unit        = "bytes"
}

# Traffic overlay: Caddy request rate on routes_to relationships. Edge-rate parameters read either end of the
# relationship: "to:/metadata/name" is the site service the proxy routes to (named after its host name).
resource "deed_telemetry_binding" "caddy_rate" {
  name              = "caddy-request-rate"
  integration       = deed_integration.prometheus.name
  query             = "sum(rate(caddy_http_requests_total{host=\"{{host}}\"}[{{window}}]))"
  params            = [{ name = "host", type = "label", from = "to:/metadata/name" }]
  unit              = "req/s"
  role              = "edge_rate"
  relationship_type = "routes_to"
}
