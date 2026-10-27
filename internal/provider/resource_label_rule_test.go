package provider

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/aurimasniekis/terraform-provider-deed/internal/client"
)

func labelConfig(project, rule, override string) string {
	return projectConfig(project) + fmt.Sprintf(`
resource "deed_entity" "svc" {
  project      = deed_project.p.name
  kind         = "service"
  name         = "billing-api"
  display_name = "Billing API"
  labels       = { team = "payments" }
}
%s
%s
`, rule, override)
}

const ruleV1 = `
resource "deed_label_rule" "apps" {
  project = deed_project.p.name
  name    = "apps"
  match   = { kinds = ["serv*"] }
  labels  = { group = "apps" }
}`

const ruleV2 = `
resource "deed_label_rule" "apps" {
  project     = deed_project.p.name
  name        = "apps"
  description = "Services of the Terraform source"
  match       = { kinds = ["service"], sources = ["terraform"] }
  labels      = { group = "core", tier = "backend" }
}`

func effectiveLabels(ctx context.Context, c *client.Client, s *terraform.State) (map[string]string, *client.Entity, error) {
	id, err := stateAttr(s, "deed_entity.svc", "id")
	if err != nil {
		return nil, nil, err
	}
	e, err := c.GetEntity(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	return e.Metadata.Labels, e, nil
}

func TestAccLabelRule_lifecycle(t *testing.T) {
	testAccPreCheck(t)
	project := randName("acc-lr")
	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: labelConfig(project, ruleV1, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr("deed_label_rule.apps", "id", idRE("lbr")),
					resource.TestCheckResourceAttr("deed_label_rule.apps", "match.kinds.#", "1"),
					resource.TestCheckNoResourceAttr("deed_label_rule.apps", "description"),
					// The rule's label is effective, but not part of the entity's declaration.
					resource.TestCheckNoResourceAttr("deed_entity.svc", "labels.group"),
					apiCheck(t, func(ctx context.Context, c *client.Client, s *terraform.State) error {
						l, _, err := effectiveLabels(ctx, c, s)
						if err != nil {
							return err
						}
						if l["group"] != "apps" || l["team"] != "payments" {
							return fmt.Errorf("effective labels %v", l)
						}
						return nil
					}),
				),
			},
			{Config: labelConfig(project, ruleV1, ""), ConfigPlanChecks: expectEmptyPlan()},
			{
				Config:           labelConfig(project, ruleV2, ""),
				ConfigPlanChecks: expectAction("deed_label_rule.apps", plancheck.ResourceActionUpdate),
				Check: apiCheck(t, func(ctx context.Context, c *client.Client, s *terraform.State) error {
					l, _, err := effectiveLabels(ctx, c, s)
					if err != nil {
						return err
					}
					if l["group"] != "core" || l["tier"] != "backend" {
						return fmt.Errorf("effective labels after update %v", l)
					}
					return nil
				}),
			},
			{Config: labelConfig(project, ruleV2, ""), ConfigPlanChecks: expectEmptyPlan()},
			{ResourceName: "deed_label_rule.apps", ImportState: true, ImportStateVerify: true, ImportStateId: project + "/apps"},
			// Removed outside Terraform: created again.
			{
				PreConfig: oob(t, func(ctx context.Context, c *client.Client) error { return c.DeleteLabelRule(ctx, project, "apps") }),
				Config:    labelConfig(project, ruleV2, ""), ConfigPlanChecks: expectAction("deed_label_rule.apps", plancheck.ResourceActionCreate),
			},
			// Destroyed: its labels go.
			{
				Config: labelConfig(project, "", ""),
				Check: apiCheck(t, func(ctx context.Context, c *client.Client, s *terraform.State) error {
					l, _, err := effectiveLabels(ctx, c, s)
					if err != nil {
						return err
					}
					if _, ok := l["group"]; ok {
						return fmt.Errorf("label of a destroyed rule kept: %v", l)
					}
					return nil
				}),
			},
		},
	})
}

const overrideV1 = `
resource "deed_entity_override" "svc" {
  project      = deed_project.p.name
  kind         = "service"
  name         = "billing-api"
  display_name = "Billing"
  icon         = "tabler:credit-card"
  labels       = { group = "payments" }
  depends_on   = [deed_entity.svc]
}`

const overrideV2 = `
resource "deed_entity_override" "svc" {
  project      = deed_project.p.name
  kind         = "service"
  name         = "billing-api"
  display_name = "Billing"
  labels       = { group = "payments", team = "finance" }
  spec         = jsonencode({ language = "go" })
  depends_on   = [deed_entity.svc]
}`

func TestAccEntityOverride_lifecycle(t *testing.T) {
	testAccPreCheck(t)
	project := randName("acc-ov")
	var entityID string
	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: labelConfig(project, "", overrideV1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("deed_entity_override.svc", "id", "deed_entity.svc", "id"),
					resource.TestCheckResourceAttrPair("deed_entity_override.svc", "entity_id", "deed_entity.svc", "id"),
					resource.TestCheckResourceAttr("deed_entity_override.svc", "display_name", "Billing"),
					resource.TestCheckNoResourceAttr("deed_entity_override.svc", "description"),
					// The declaration keeps its own values: no diff from the override.
					resource.TestCheckResourceAttr("deed_entity.svc", "display_name", "Billing API"),
					capture("deed_entity.svc", "id", &entityID),
					apiCheck(t, func(ctx context.Context, c *client.Client, s *terraform.State) error {
						l, e, err := effectiveLabels(ctx, c, s)
						if err != nil {
							return err
						}
						if e.Metadata.DisplayName != "Billing" || e.Metadata.Icon != "tabler:credit-card" || l["group"] != "payments" || l["team"] != "payments" {
							return fmt.Errorf("effective view %+v", e.Metadata)
						}
						return nil
					}),
				),
			},
			{Config: labelConfig(project, "", overrideV1), ConfigPlanChecks: expectEmptyPlan()},
			{
				Config:           labelConfig(project, "", overrideV2),
				ConfigPlanChecks: expectAction("deed_entity_override.svc", plancheck.ResourceActionUpdate),
				Check: apiCheck(t, func(ctx context.Context, c *client.Client, s *terraform.State) error {
					l, e, err := effectiveLabels(ctx, c, s)
					if err != nil {
						return err
					}
					if e.Metadata.Icon != "" || l["team"] != "finance" {
						return fmt.Errorf("after update %+v", e.Metadata)
					}
					return nil
				}),
			},
			{Config: labelConfig(project, "", overrideV2), ConfigPlanChecks: expectEmptyPlan()},
			{ResourceName: "deed_entity_override.svc", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return project + "/service/billing-api", nil }},
			// Removed outside Terraform: created again.
			{
				PreConfig: oob(t, func(ctx context.Context, c *client.Client) error {
					return c.DeleteDeclaration(ctx, entityID, "overrides")
				}),
				Config: labelConfig(project, "", overrideV2), ConfigPlanChecks: expectAction("deed_entity_override.svc", plancheck.ResourceActionCreate),
			},
			// The owning declaration goes away outside Terraform: the entity is gone, and the override reads as removed instead
			// of keeping it alive. Both are created again (the entity first).
			{
				PreConfig: oob(t, func(ctx context.Context, c *client.Client) error {
					return c.DeleteDeclaration(ctx, entityID, "terraform")
				}),
				Config:           labelConfig(project, "", overrideV2),
				ConfigPlanChecks: expectAction("deed_entity_override.svc", plancheck.ResourceActionCreate),
			},
			{Config: labelConfig(project, "", overrideV2), ConfigPlanChecks: expectEmptyPlan()},
		},
	})
}
