package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

func TestAccRelationshipType_lifecycle(t *testing.T) {
	name := "example.com/feeds_" + acctest.RandStringFromCharSet(6, "abcdefghijklmnopqrstuvwxyz")
	cfg := func(title, extra string) string {
		return fmt.Sprintf(`
resource "deed_relationship_type" "t" {
  name  = %q
  title = %q
%s
}
`, name, title, extra)
	}
	resource.ParallelTest(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg("Feeds", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("deed_relationship_type.t", "id", name),
					resource.TestCheckResourceAttr("deed_relationship_type.t", "default_evidence", "structural"),
					resource.TestCheckResourceAttr("deed_relationship_type.t", "impact", "forward"),
					resource.TestCheckResourceAttr("deed_relationship_type.t", "from_kinds.#", "0"),
					resource.TestCheckNoResourceAttr("deed_relationship_type.t", "attributes_schema"),
				),
			},
			{Config: cfg("Feeds", ""), PlanOnly: true},
			{
				Config: cfg("Feeds data into", `  description = "Data flow"
  directed    = false
  cardinality = "one"
  to_kinds    = ["database"]`),
				ConfigPlanChecks: expectAction("deed_relationship_type.t", plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("deed_relationship_type.t", "title", "Feeds data into"),
					resource.TestCheckResourceAttr("deed_relationship_type.t", "directed", "false"),
					resource.TestCheckResourceAttr("deed_relationship_type.t", "to_kinds.0", "database"),
				),
			},
			{ResourceName: "deed_relationship_type.t", ImportState: true, ImportStateVerify: true},
			{
				Config: `
resource "deed_relationship_type" "builtin" {
  name  = "runs_on"
  title = "Runs on"
}
`,
				ExpectError: errRE("Built-in relationship types cannot be managed"),
			},
		},
	})
}
