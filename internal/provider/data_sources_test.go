package provider

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/aurimasniekis/terraform-provider-deed/internal/client"
)

func TestAccDataSources(t *testing.T) {
	project := randName("acc-ds")
	base := projectConfig(project) + `
resource "deed_entity" "svc" {
  project      = deed_project.p.name
  kind         = "service"
  name         = "grafana"
  display_name = "Grafana"
  labels       = { team = "observability" }
  spec         = jsonencode({ port = 3000 })
}

resource "deed_entity" "host" {
  project = deed_project.p.name
  kind    = "docker_host"
  name    = "docker-01"
  labels  = { team = "observability" }
}

resource "deed_relationship" "runs_on" {
  type = "runs_on"
  from = { id = deed_entity.svc.id }
  to   = { id = deed_entity.host.id }
}
`
	var svcID string
	resource.ParallelTest(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: base,
				Check: resource.ComposeAggregateTestCheckFunc(
					capture("deed_entity.svc", "id", &svcID),
					// Another source annotates the service: visible in the effective view only.
					apiCheck(t, func(ctx context.Context, c *client.Client, _ *terraform.State) error {
						_, err := c.PutDeclaration(ctx, svcID, "ui", "", client.DeclarationInput{
							Metadata: &client.Metadata{Annotations: map[string]string{"note": "from the UI"}}})
						return err
					}),
				),
			},
			{
				Config: base + `
data "deed_project" "by_name" {
  name = deed_project.p.name
}

data "deed_entity" "by_id" {
  id = deed_entity.svc.id
}

data "deed_entity" "by_key" {
  project = deed_project.p.name
  kind    = "service"
  name    = "grafana"
}

data "deed_entities" "team" {
  project = deed_project.p.name
  labels  = { team = "observability" }
}

data "deed_entities" "services" {
  project = deed_project.p.name
  kind    = "service"
}

data "deed_kind" "server" {
  name = "server"
}

data "deed_relationship_type" "runs_on" {
  name = "runs_on"
}

data "deed_relationship" "by_id" {
  id = deed_relationship.runs_on.id
}

data "deed_relationship" "by_ends" {
  type    = "runs_on"
  from_id = deed_entity.svc.id
  to_id   = deed_entity.host.id
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("data.deed_project.by_name", "id", "deed_project.p", "id"),
					resource.TestCheckResourceAttr("data.deed_project.by_name", "role", "admin"),
					resource.TestCheckResourceAttr("data.deed_entity.by_id", "display_name", "Grafana"),
					resource.TestCheckResourceAttr("data.deed_entity.by_id", "annotations.note", "from the UI"),
					resource.TestCheckResourceAttr("data.deed_entity.by_id", "health", "unknown"),
					resource.TestCheckResourceAttr("data.deed_entity.by_id", "declared", "true"),
					resource.TestCheckResourceAttr("data.deed_entity.by_id", "facets", "2"),
					resource.TestCheckResourceAttr("data.deed_entity.by_id", "spec", `{"port":3000}`),
					resource.TestCheckResourceAttrSet("data.deed_entity.by_id", "status"),
					resource.TestCheckResourceAttrPair("data.deed_entity.by_key", "id", "deed_entity.svc", "id"),
					resource.TestCheckResourceAttr("data.deed_entities.team", "entities.#", "2"),
					resource.TestCheckResourceAttr("data.deed_entities.team", "ids.#", "2"),
					resource.TestCheckResourceAttr("data.deed_entities.services", "entities.#", "1"),
					resource.TestCheckResourceAttr("data.deed_entities.services", "entities.0.name", "grafana"),
					resource.TestCheckResourceAttr("data.deed_kind.server", "builtin", "true"),
					resource.TestCheckResourceAttr("data.deed_kind.server", "storage_version", "v1"),
					resource.TestCheckResourceAttr("data.deed_kind.server", "versions.#", "1"),
					resource.TestCheckResourceAttr("data.deed_relationship_type.runs_on", "inverse_name", "runs"),
					resource.TestCheckResourceAttr("data.deed_relationship_type.runs_on", "builtin", "true"),
					resource.TestCheckResourceAttr("data.deed_relationship.by_id", "sources.#", "1"),
					resource.TestCheckResourceAttr("data.deed_relationship.by_id", "sources.0", "terraform"),
					resource.TestCheckResourceAttr("data.deed_relationship.by_id", "live", "true"),
					resource.TestCheckResourceAttrPair("data.deed_relationship.by_ends", "id", "deed_relationship.runs_on", "id"),
					func(s *terraform.State) error {
						if v, _ := stateAttr(s, "data.deed_relationship.by_ends", "evidence_class"); v != "declared" {
							return fmt.Errorf("evidence_class %q", v)
						}
						return nil
					},
				),
			},
		},
	})
}
