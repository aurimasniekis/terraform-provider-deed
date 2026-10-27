package provider

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/aurimasniekis/terraform-provider-deed/internal/client"
)

// The Deed 0.4.0 fields are sent only when used, so bindings without them keep working against older Deed versions,
// and to clear them on update.
func TestBindingInputNewFields(t *testing.T) {
	ctx := context.Background()
	plain := bindingModel{Guard: types.StringNull(), WhenEmpty: types.Float64Null(), OnMap: types.BoolValue(false)}
	in := plain.input(ctx, nil)
	if in.Guard != nil || in.WhenEmpty != nil || in.OnMap != nil {
		t.Fatalf("unused fields sent: %+v", in)
	}
	set := bindingModel{Guard: types.StringValue("up"), WhenEmpty: types.Float64Value(0), OnMap: types.BoolValue(true)}
	in = set.input(ctx, nil)
	if in.Guard == nil || *in.Guard != "up" || in.WhenEmpty == nil || in.WhenEmpty.Value == nil || *in.WhenEmpty.Value != 0 ||
		in.OnMap == nil || !*in.OnMap {
		t.Fatalf("set fields not sent: %+v", in)
	}
	in = plain.input(ctx, &set)
	if in.Guard == nil || *in.Guard != "" || in.WhenEmpty == nil || in.WhenEmpty.Value != nil || in.OnMap == nil || *in.OnMap {
		t.Fatalf("removed fields not cleared: %+v", in)
	}
}

func telemetryConfig(name, integrationExtra, binding string) string {
	return fmt.Sprintf(`
resource "deed_integration" "prom" {
  name    = %[1]q
  adapter = "prometheus"
  config = jsonencode({
    url           = "http://127.0.0.1:9/api/datasources/proxy/uid/prometheus"
    insecure_http = true
    allow_cidrs   = ["127.0.0.0/8"]
  })
%[2]s
}

resource "deed_telemetry_binding" "cpu" {
  name        = "%[1]s-cpu"
  integration = deed_integration.prom.name
%[3]s
}

resource "deed_telemetry_binding" "edge" {
  name              = "%[1]s-edge"
  integration       = deed_integration.prom.name
  query             = "sum(rate(caddy_http_requests_total{host=\"{{host}}\"}[{{window}}]))"
  params            = [{ name = "host", type = "label", from = "to:/metadata/name" }]
  unit              = "req/s"
  role              = "edge_rate"
  relationship_type = "routes_to"
}

data "deed_integrations" "prom" {
  adapter    = "prometheus"
  depends_on = [deed_integration.prom]
}
`, name, integrationExtra, binding)
}

const healthBinding = `
  title = "CPU busy"
  kinds = ["vm", "server"]
  query = "100 * (1 - avg(rate(node_cpu_seconds_total{mode=\"idle\",instance=\"{{instance}}\"}[{{window}}])))"
  params = [{ name = "instance", type = "label", from = "/spec/hostname", required = true }]
  unit  = "percent"
  role  = "health"
  thresholds = { warning = 80, critical = 95 }
  freshness_seconds = 120
  guard      = "up{job=\"node\",instance=\"{{instance}}\"}"
  when_empty = 0
  on_map     = true
`

const chartBinding = `
  kinds = ["vm"]
  query = "avg(rate(node_cpu_seconds_total{mode!=\"idle\",instance=\"{{instance}}\"}[{{window}}]))"
  params = [{ name = "instance", type = "label", from = "/spec/hostname" }]
  shape = "timeseries"
`

func TestAccTelemetry_lifecycle(t *testing.T) {
	testAccPreCheck(t)
	name := randName("acc-tel")
	var bindingID string
	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			if _, err := adminClient(t).GetIntegration(context.Background(), name); !client.IsNotFound(err) {
				return fmt.Errorf("integration %s still exists (%w)", name, err)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: telemetryConfig(name, "  secrets_wo = { token = \"glsa_acceptance\" }\n  secrets_wo_version = 1", healthBinding),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("deed_integration.prom", "has_secrets", "true"),
					resource.TestCheckResourceAttr("deed_integration.prom", "enabled", "true"),
					resource.TestCheckNoResourceAttr("deed_integration.prom", "secrets_wo"),
					resource.TestCheckResourceAttr("deed_telemetry_binding.cpu", "shape", "scalar"),
					resource.TestCheckResourceAttr("deed_telemetry_binding.cpu", "role", "health"),
					resource.TestCheckResourceAttr("deed_telemetry_binding.cpu", "thresholds.critical", "95"),
					resource.TestCheckResourceAttr("deed_telemetry_binding.cpu", "params.0.from", "/spec/hostname"),
					resource.TestCheckResourceAttrPair("deed_telemetry_binding.cpu", "integration_id", "deed_integration.prom", "id"),
					resource.TestCheckResourceAttr("data.deed_integrations.prom", "integrations.0.name", name),
					resource.TestCheckResourceAttr("deed_telemetry_binding.edge", "role", "edge_rate"),
					resource.TestCheckResourceAttr("deed_telemetry_binding.edge", "shape", "scalar"),
					resource.TestCheckResourceAttr("deed_telemetry_binding.cpu", "guard", `up{job="node",instance="{{instance}}"}`),
					resource.TestCheckResourceAttr("deed_telemetry_binding.cpu", "when_empty", "0"),
					resource.TestCheckResourceAttr("deed_telemetry_binding.cpu", "on_map", "true"),
					resource.TestCheckResourceAttr("deed_telemetry_binding.edge", "on_map", "false"),
					resource.TestCheckNoResourceAttr("deed_telemetry_binding.edge", "guard"),
					resource.TestCheckNoResourceAttr("deed_telemetry_binding.edge", "when_empty"),
					capture("deed_telemetry_binding.cpu", "id", &bindingID),
				),
			},
			{Config: telemetryConfig(name, "  secrets_wo = { token = \"glsa_acceptance\" }\n  secrets_wo_version = 1", healthBinding), ConfigPlanChecks: expectEmptyPlan()},
			// Switch to a chart: thresholds, unit, title, the second kind, the guard, when_empty and on_map are cleared, not kept.
			{
				Config:           telemetryConfig(name, "  enabled = false\n  secrets_wo_version = 2", chartBinding),
				ConfigPlanChecks: expectAction("deed_telemetry_binding.cpu", plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("deed_integration.prom", "has_secrets", "false"),
					resource.TestCheckResourceAttr("deed_integration.prom", "enabled", "false"),
					resource.TestCheckResourceAttr("deed_telemetry_binding.cpu", "role", "chart"),
					resource.TestCheckResourceAttr("deed_telemetry_binding.cpu", "title", name+"-cpu"),
					resource.TestCheckNoResourceAttr("deed_telemetry_binding.cpu", "thresholds"),
					resource.TestCheckNoResourceAttr("deed_telemetry_binding.cpu", "unit"),
					resource.TestCheckResourceAttr("deed_telemetry_binding.cpu", "kinds.#", "1"),
					resource.TestCheckNoResourceAttr("deed_telemetry_binding.cpu", "guard"),
					resource.TestCheckNoResourceAttr("deed_telemetry_binding.cpu", "when_empty"),
					resource.TestCheckResourceAttr("deed_telemetry_binding.cpu", "on_map", "false"),
					apiCheck(t, func(ctx context.Context, c *client.Client, _ *terraform.State) error {
						b, err := c.GetBinding(ctx, name+"-cpu")
						if err != nil {
							return err
						}
						if b.Unit != "" || b.Thresholds.Critical != nil || len(b.Kinds) != 1 || b.Guard != "" || b.WhenEmpty != nil || b.OnMap {
							return fmt.Errorf("cleared fields kept: %+v", b)
						}
						return nil
					}),
				),
			},
			{Config: telemetryConfig(name, "  enabled = false\n  secrets_wo_version = 2", chartBinding), ConfigPlanChecks: expectEmptyPlan()},
			{ResourceName: "deed_integration.prom", ImportState: true, ImportStateVerify: true, ImportStateId: name,
				ImportStateVerifyIgnore: []string{"secrets_wo_version"}},
			{ResourceName: "deed_telemetry_binding.cpu", ImportState: true, ImportStateVerify: true, ImportStateId: name + "-cpu",
				ImportStateVerifyIgnore: []string{"integration"}},
			// Deleted outside Terraform: created again.
			{
				PreConfig:        oob(t, func(ctx context.Context, c *client.Client) error { return c.DeleteBinding(ctx, bindingID) }),
				Config:           telemetryConfig(name, "  enabled = false\n  secrets_wo_version = 2", chartBinding),
				ConfigPlanChecks: expectAction("deed_telemetry_binding.cpu", plancheck.ResourceActionCreate),
			},
		},
	})
}
