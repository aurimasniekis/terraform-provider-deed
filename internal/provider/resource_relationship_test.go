package provider

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/aurimasniekis/terraform-provider-deed/internal/client"
)

func relationshipConfig(project, relType, attrs string) string {
	return relationshipBase(project, relType) + fmt.Sprintf(`
# By ID on both ends.
resource "deed_relationship" "backup" {
  type       = deed_relationship_type.backs_up.name
  from       = { id = deed_entity.nas.id }
  to         = { id = deed_entity.pve.id }
  attributes = %s
}
`, attrs)
}

func relationshipBase(project, relType string) string {
	return projectConfig(project) + fmt.Sprintf(`
resource "deed_relationship_type" "backs_up" {
  name         = %q
  inverse_name = "backed_up_by"
  title        = "Backs up"
  from_kinds   = ["server"]
  to_kinds     = ["server", "storage"]
  impact       = "reverse"
  color        = "#1971c2"
  attributes_schema = jsonencode({
    type = "object"
    properties = {
      schedule = { type = "string" }
    }
  })
}

resource "deed_entity" "nas" {
  project = deed_project.p.name
  kind    = "server"
  name    = "nas-01"
  spec    = jsonencode({ hostname = "nas-01.%s.example.com" })
}

resource "deed_entity" "pve" {
  project = deed_project.p.name
  kind    = "server"
  name    = "pve-01"
  spec    = jsonencode({ hostname = "pve-01.%s.example.com" })
}

resource "deed_entity" "ops" {
  project = deed_project.p.name
  kind    = "group"
  name    = "ops"
}

# By natural key (needs the declaring project).
resource "deed_relationship" "owner" {
  project = deed_project.p.name
  type    = "owned_by"
  from    = { kind = "server", name = deed_entity.pve.name }
  to      = { kind = "group", name = deed_entity.ops.name }
}
`, relType, project, project)
}

func TestAccRelationship_lifecycle(t *testing.T) {
	project := randName("acc-rel")
	relType := "example.com/backs_up_" + acctest.RandStringFromCharSet(6, "abcdefghijklmnopqrstuvwxyz")
	var relID, ownerID string
	resource.ParallelTest(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			if relID == "" {
				return nil
			}
			r, err := adminClient(t).GetRelation(context.Background(), relID)
			if client.IsNotFound(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if r.LiveAssertion("terraform") != nil {
				return fmt.Errorf("relationship %s is still asserted by terraform", relID)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: relationshipConfig(project, relType, `jsonencode({ schedule = "daily" })`),
				Check: resource.ComposeAggregateTestCheckFunc(
					capture("deed_relationship.backup", "id", &relID),
					capture("deed_relationship.owner", "id", &ownerID),
					resource.TestMatchResourceAttr("deed_relationship.backup", "id", idRE("rel")),
					resource.TestCheckResourceAttrPair("deed_relationship.backup", "from.id", "deed_entity.nas", "id"),
					resource.TestCheckResourceAttr("deed_relationship.backup", "from.kind", "server"),
					resource.TestCheckResourceAttr("deed_relationship.backup", "to.name", "pve-01"),
					resource.TestCheckResourceAttrPair("deed_relationship.backup", "project", "deed_project.p", "name"),
					resource.TestCheckResourceAttrPair("deed_relationship.owner", "to.id", "deed_entity.ops", "id"),
					resource.TestCheckResourceAttr("deed_relationship_type.backs_up", "directed", "true"),
					resource.TestCheckResourceAttr("deed_relationship_type.backs_up", "cardinality", "many"),
				),
			},
			{Config: relationshipConfig(project, relType, `jsonencode({ schedule = "daily" })`), PlanOnly: true},
			// Attributes change in place (PUT of the assertion).
			{
				Config:           relationshipConfig(project, relType, `jsonencode({ schedule = "hourly" })`),
				ConfigPlanChecks: expectAction("deed_relationship.backup", plancheck.ResourceActionUpdate),
				Check: apiCheck(t, func(ctx context.Context, c *client.Client, _ *terraform.State) error {
					r, err := c.GetRelation(ctx, relID)
					if err != nil {
						return err
					}
					if a := r.LiveAssertion("terraform"); a == nil || string(a.Attributes) != `{"schedule":"hourly"}` {
						return fmt.Errorf("assertion %+v", a)
					}
					return nil
				}),
			},
			{ResourceName: "deed_relationship.backup", ImportState: true, ImportStateVerify: true},
			{ResourceName: "deed_relationship.owner", ImportState: true, ImportStateVerify: true},
			// Forget an imported relationship (endpoints by kind and name, declaring project by name), import it, plan: no change.
			{Config: withoutOwner(relationshipConfig(project, relType, `jsonencode({ schedule = "hourly" })`))},
			{Config: relationshipConfig(project, relType, `jsonencode({ schedule = "hourly" })`), ResourceName: "deed_relationship.owner",
				ImportState: true, ImportStatePersist: true, ImportStateIdFunc: func(*terraform.State) (string, error) { return ownerID, nil }},
			{Config: relationshipConfig(project, relType, `jsonencode({ schedule = "hourly" })`), ConfigPlanChecks: expectEmptyPlan()},
			{ResourceName: "deed_relationship_type.backs_up", ImportState: true, ImportStateVerify: true},
			// Out-of-band retraction of the Terraform assertion: the next plan asserts it again.
			{
				PreConfig: oob(t, func(ctx context.Context, c *client.Client) error {
					return c.DeleteAssertion(ctx, relID, "terraform")
				}),
				Config:           relationshipConfig(project, relType, `jsonencode({ schedule = "hourly" })`),
				ConfigPlanChecks: expectAction("deed_relationship.backup", plancheck.ResourceActionCreate),
			},
			// Another source asserts the same relationship; it never causes a diff.
			{
				Config: relationshipConfig(project, relType, `jsonencode({ schedule = "hourly" })`),
				Check: apiCheck(t, func(ctx context.Context, c *client.Client, _ *terraform.State) error {
					_, err := c.PutAssertion(ctx, relID, "api", []byte(`{"schedule":"weekly"}`))
					return err
				}),
			},
			{Config: relationshipConfig(project, relType, `jsonencode({ schedule = "hourly" })`), PlanOnly: true},
			// Removing the resource retracts only the Terraform assertion: the relationship stays, asserted by api.
			{
				Config: relationshipBase(project, relType),
				Check: apiCheck(t, func(ctx context.Context, c *client.Client, _ *terraform.State) error {
					r, err := c.GetRelation(ctx, relID)
					if err != nil {
						return err
					}
					if !r.Live || r.LiveAssertion("api") == nil || r.LiveAssertion("terraform") != nil {
						return fmt.Errorf("relationship after retraction: live=%v assertions=%+v", r.Live, r.Assertions)
					}
					return nil
				}),
			},
			// Clean up the api assertion so the custom type can be destroyed.
			{
				PreConfig: oob(t, func(ctx context.Context, c *client.Client) error {
					return c.DeleteAssertion(ctx, relID, "api")
				}),
				Config: relationshipBase(project, relType),
			},
		},
	})
}

func TestAccRelationship_missingEndpoint(t *testing.T) {
	project := randName("acc-relx")
	resource.ParallelTest(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: projectConfig(project) + `
resource "deed_relationship" "r" {
  type = "depends_on"
  from = { kind = "service", name = "a" }
  to   = { id = "ent_x", name = "b" }
}
`,
				PlanOnly:    true,
				ExpectError: errRE("Missing project"),
			},
			{
				Config: projectConfig(project) + `
resource "deed_entity" "a" {
  project = deed_project.p.name
  kind    = "service"
  name    = "a"
}

resource "deed_relationship" "r" {
  project = deed_project.p.name
  type    = "depends_on"
  from    = { id = deed_entity.a.id }
  to      = { kind = "service", name = "does-not-exist" }
}
`,
				ExpectError: errRE("Cannot declare relationship", "endpoint not found"),
			},
		},
	})
}

func TestAccRelationship_foreignDeclaringProject(t *testing.T) {
	a, b := randName("acc-rpa"), randName("acc-rpb")
	resource.ParallelTest(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "deed_project" "a" {
  name = %q
}

resource "deed_project" "b" {
  name = %q
}

resource "deed_entity" "x" {
  project = deed_project.a.name
  kind    = "service"
  name    = "x"
}

resource "deed_relationship" "r" {
  project    = deed_project.b.name
  type       = "depends_on"
  from       = { id = deed_entity.x.id }
  to         = { kind = "service", name = "y", project = deed_project.b.name }
  depends_on = [deed_entity.x]
}
`, a, b),
			ExpectError: errRE("Unsupported declaring project", "not the project of the from entity"),
		}},
	})
}

const ownerBlock = `# By natural key (needs the declaring project).
resource "deed_relationship" "owner" {
  project = deed_project.p.name
  type    = "owned_by"
  from    = { kind = "server", name = deed_entity.pve.name }
  to      = { kind = "group", name = deed_entity.ops.name }
}
`

// withoutOwner replaces the owner relationship with a removed block that forgets it without destroying it.
func withoutOwner(cfg string) string {
	if !strings.Contains(cfg, ownerBlock) {
		panic("owner block not found")
	}
	return strings.Replace(cfg, ownerBlock, "removed {\n  from = deed_relationship.owner\n  lifecycle {\n    destroy = false\n  }\n}\n", 1)
}
