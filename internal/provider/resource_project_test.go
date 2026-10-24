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

func projectFull(name, display, extra string) string {
	return fmt.Sprintf(`
resource "deed_project" "p" {
  name        = %q
  %s
  description = "Acceptance project"
%s
}
`, name, display, extra)
}

func TestAccProject_lifecycle(t *testing.T) {
	name := randName("acc-prj")
	var id string
	resource.ParallelTest(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			if _, err := adminClient(t).GetProject(context.Background(), name); !client.IsNotFound(err) {
				return fmt.Errorf("project %s still exists (%w)", name, err)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: projectFull(name, "", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					capture("deed_project.p", "id", &id),
					resource.TestMatchResourceAttr("deed_project.p", "id", idRE("prj")),
					resource.TestCheckResourceAttr("deed_project.p", "display_name", name),
					resource.TestCheckResourceAttr("deed_project.p", "allow_http_links", "false"),
				),
			},
			{Config: projectFull(name, "", ""), PlanOnly: true},
			{
				Config:           projectFull(name, `display_name = "Home Lab"`, "  allow_http_links = true"),
				ConfigPlanChecks: expectAction("deed_project.p", plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("deed_project.p", "display_name", "Home Lab"),
					resource.TestCheckResourceAttr("deed_project.p", "allow_http_links", "true"),
				),
			},
			// Removing display_name restores the default (the name) instead of keeping the old value.
			{
				Config: projectFull(name, "", "  allow_http_links = true"),
				Check:  resource.TestCheckResourceAttr("deed_project.p", "display_name", name),
			},
			{ResourceName: "deed_project.p", ImportState: true, ImportStateVerify: true, ImportStateId: name},
			// Out-of-band change: drift is detected and repaired.
			{
				PreConfig: oob(t, func(ctx context.Context, c *client.Client) error {
					_, err := c.UpdateProject(ctx, id, "", client.ProjectInput{DisplayName: "Changed", Description: "changed"})
					return err
				}),
				Config:           projectFull(name, "", "  allow_http_links = true"),
				ConfigPlanChecks: expectAction("deed_project.p", plancheck.ResourceActionUpdate),
			},
			// Out-of-band deletion: recreated.
			{
				PreConfig: oob(t, func(ctx context.Context, c *client.Client) error {
					return c.DeleteProject(ctx, id, true)
				}),
				Config:           projectFull(name, "", "  allow_http_links = true"),
				ConfigPlanChecks: expectAction("deed_project.p", plancheck.ResourceActionCreate),
			},
		},
	})
}

func TestAccProjectMember_lifecycle(t *testing.T) {
	testAccPreCheck(t)
	name := randName("acc-mem")
	cfg := func(role string) string {
		return projectConfig(name) + fmt.Sprintf(`
resource "deed_project_member" "m" {
  project = deed_project.p.name
  user_id = %q
  role    = %q
}
`, acc.MemberID, role)
	}
	resource.ParallelTest(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg("viewer"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("deed_project_member.m", "role", "viewer"),
					resource.TestCheckResourceAttr("deed_project_member.m", "email", "member@example.com"),
					resource.TestCheckResourceAttrPair("deed_project_member.m", "project_id", "deed_project.p", "id"),
				),
			},
			{Config: cfg("viewer"), PlanOnly: true},
			{
				Config:           cfg("editor"),
				ConfigPlanChecks: expectAction("deed_project_member.m", plancheck.ResourceActionUpdate),
				Check:            resource.TestCheckResourceAttr("deed_project_member.m", "role", "editor"),
			},
			{ResourceName: "deed_project_member.m", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: importID("deed_project_member.m", "project", "user_id")},
			{
				PreConfig: oob(t, func(ctx context.Context, c *client.Client) error {
					return c.RemoveMember(ctx, name, acc.MemberID)
				}),
				Config:           cfg("editor"),
				ConfigPlanChecks: expectAction("deed_project_member.m", plancheck.ResourceActionCreate),
			},
		},
	})
}

// TestAccProvider_editorToken uses a token capped at the editor role of one project: entities can be declared there,
// installation-level objects cannot.
func TestAccProvider_editorToken(t *testing.T) {
	testAccPreCheck(t)
	provider := fmt.Sprintf(`
provider "deed" {
  endpoint = %q
  token    = %q
}
`, acc.Endpoint, acc.EditorToken)
	resource.ParallelTest(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: provider + `
resource "deed_project" "p" {
  name = "acc-editor-denied"
}
`,
				ExpectError: errRE("Cannot create project", "403 forbidden", "installation admin token"),
			},
			{
				Config: provider + fmt.Sprintf(`
resource "deed_entity" "e" {
  project = %q
  kind    = "service"
  name    = "editor-made"
}
`, acc.LimitedProject),
				Check: resource.TestMatchResourceAttr("deed_entity.e", "id", idRE("ent")),
			},
		},
	})
}
