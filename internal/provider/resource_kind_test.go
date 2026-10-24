package provider

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/aurimasniekis/terraform-provider-deed/internal/client"
)

func kindConfig(project, kind, schema, extra string) string {
	return projectConfig(project) + fmt.Sprintf(`
resource "deed_kind" "ups" {
  name        = %q
  title       = "UPS"
  description = "Uninterruptible power supply"
  icon        = "tabler:battery-charging"
  color       = "#2f9e44"
  identity_keys = [["/spec/serial"]]
  schema      = %s
%s
}

resource "deed_entity" "ups" {
  project = deed_project.p.name
  kind    = deed_kind.ups.name
  name    = "ups-01"
  spec    = jsonencode({ capacity_va = 1500, serial = "SN-0001" })
}
`, kind, schema, extra)
}

const (
	upsSchemaV1 = `jsonencode({
    type                 = "object"
    additionalProperties = false
    properties = {
      capacity_va = { type = "integer", minimum = 0 }
      serial      = { type = "string" }
    }
  })`
	// Additive: a new optional property (backward compatible because v1 forbids additional properties).
	upsSchemaV2 = `jsonencode({
    type                 = "object"
    additionalProperties = false
    properties = {
      capacity_va = { type = "integer", minimum = 0 }
      serial      = { type = "string" }
      model       = { type = "string" }
    }
  })`
	// Breaking: capacity_va changes type.
	upsSchemaV3 = `jsonencode({
    type                 = "object"
    additionalProperties = false
    properties = {
      capacity_va = { type = "string" }
      serial      = { type = "string" }
      model       = { type = "string" }
    }
  })`
)

func TestAccKind_schemaVersions(t *testing.T) {
	project := randName("acc-kind")
	kind := "example.com/ups_" + acctest.RandStringFromCharSet(8, "abcdefghijklmnopqrstuvwxyz0123456789")
	resource.ParallelTest(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: kindConfig(project, kind, upsSchemaV1, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("deed_kind.ups", "id", kind),
					resource.TestCheckResourceAttr("deed_kind.ups", "storage_version", "v1"),
					resource.TestCheckResourceAttr("deed_kind.ups", "group", "custom"),
					resource.TestCheckResourceAttr("deed_kind.ups", "plural", "UPSs"),
					resource.TestCheckResourceAttr("deed_kind.ups", "identity_keys.0.0", "/spec/serial"),
				),
			},
			{Config: kindConfig(project, kind, upsSchemaV1, ""), PlanOnly: true},
			// Additive change: publishes v2, then the plan is empty.
			{
				Config:           kindConfig(project, kind, upsSchemaV2, ""),
				ConfigPlanChecks: expectAction("deed_kind.ups", plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("deed_kind.ups", "storage_version", "v2"),
					apiCheck(t, func(ctx context.Context, c *client.Client, _ *terraform.State) error {
						k, err := c.GetKind(ctx, kind)
						if err != nil {
							return err
						}
						if len(k.Versions) != 2 || k.StorageVersion != "v2" {
							return fmt.Errorf("versions %+v storage %s", k.Versions, k.StorageVersion)
						}
						return nil
					}),
				),
			},
			{Config: kindConfig(project, kind, upsSchemaV2, ""), ConfigPlanChecks: expectEmptyPlan()},
			// Display-only change: PATCH, no new version.
			{
				Config: kindConfig(project, kind, upsSchemaV2, `  plural = "UPS units"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("deed_kind.ups", "plural", "UPS units"),
					resource.TestCheckResourceAttr("deed_kind.ups", "storage_version", "v2"),
				),
			},
			// Breaking change without breaking = true: refused with the compatibility problems.
			{
				Config:      kindConfig(project, kind, upsSchemaV3, `  plural = "UPS units"`),
				ExpectError: errRE("Schema change was refused by Deed", "Compatibility problems", "capacity_va", "breaking = true"),
			},
			// With breaking = true and migration = "mark_invalid" it is published; the existing entity is flagged.
			{
				Config: kindConfig(project, kind, upsSchemaV3, `  plural = "UPS units"
  breaking  = true
  migration = "mark_invalid"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("deed_kind.ups", "storage_version", "v3"),
					apiCheck(t, func(ctx context.Context, c *client.Client, s *terraform.State) error {
						id, err := stateAttr(s, "deed_entity.ups", "id")
						if err != nil {
							return err
						}
						e, err := c.GetEntity(ctx, id)
						if err != nil {
							return err
						}
						if e.ParsedStatus().Valid {
							return fmt.Errorf("entity still valid after the breaking publication")
						}
						return nil
					}),
				),
			},
			{Config: kindConfig(project, kind, upsSchemaV3, `  plural = "UPS units"
  breaking  = true
  migration = "mark_invalid"`), PlanOnly: true},
			{ResourceName: "deed_kind.ups", ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"breaking", "migration"}},
		},
	})
}

func TestAccKind_validation(t *testing.T) {
	resource.ParallelTest(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `resource "deed_kind" "builtin" {
  name  = "server"
  title = "Server"
}`,
				ExpectError: errRE("Built-in kinds cannot be managed"),
			},
			{
				Config: `resource "deed_kind" "x" {
  name     = "example.com/widget"
  title    = "Widget"
  breaking = true
}`,
				ExpectError: errRE("Missing migration mode"),
				PlanOnly:    true,
			},
			{
				Config: `resource "deed_kind" "x" {
  name      = "example.com/widget"
  title     = "Widget"
  migration = "revalidate"
}`,
				ExpectError: errRE("Migration without a breaking change"),
				PlanOnly:    true,
			},
			{
				Config: `resource "deed_kind" "x" {
  name          = "example.com/widget"
  title         = "Widget"
  identity_keys = [["spec/serial"]]
}`,
				ExpectError: errRE("Invalid identity key"),
				PlanOnly:    true,
			},
		},
	})
}
