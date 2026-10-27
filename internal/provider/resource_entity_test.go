package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/aurimasniekis/terraform-provider-deed/internal/client"
)

func entityConfig(project, host, display string) string {
	return projectConfig(project) + fmt.Sprintf(`
resource "deed_entity" "nas" {
  project      = deed_project.p.name
  kind         = "server"
  name         = "nas-01"
  display_name = %q
  description  = "Primary NAS"
  environment  = "homelab"
  lifecycle_stage = "active"
  icon         = "tabler:server"
  labels = {
    tier = "storage"
  }
  annotations = {
    "example.com/rack" = "r1"
  }
  links = [
    { type = "docs", url = "https://docs.example.com/nas" },
    { type = "dashboard", title = "Grafana", url = "https://grafana.example.com/d/nas" },
  ]
  tags             = ["storage", "critical"]
  version_declared = "7.2"
  version_channel  = "stable"
  spec = jsonencode({
    hostname  = %q
    ipv4      = "192.0.2.10"
    cpu_cores = 4
    memory_mb = 16384
  })
}
`, display, host)
}

func TestAccEntity_lifecycle(t *testing.T) {
	project := randName("acc-ent")
	host := randName("nas") + ".example.com"
	var id string
	resource.ParallelTest(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkDeclarationGone(t, &id),
		Steps: []resource.TestStep{
			// First apply.
			{
				Config: entityConfig(project, host, "NAS"),
				Check: resource.ComposeAggregateTestCheckFunc(
					capture("deed_entity.nas", "id", &id),
					resource.TestMatchResourceAttr("deed_entity.nas", "id", idRE("ent")),
					resource.TestMatchResourceAttr("deed_entity.nas", "project_id", idRE("prj")),
					resource.TestCheckResourceAttr("deed_entity.nas", "project", project),
					resource.TestCheckResourceAttr("deed_entity.nas", "labels.tier", "storage"),
					resource.TestCheckResourceAttr("deed_entity.nas", "links.#", "2"),
					resource.TestCheckResourceAttr("deed_entity.nas", "links.1.title", "Grafana"),
					resource.TestCheckNoResourceAttr("deed_entity.nas", "links.0.title"),
					resource.TestCheckResourceAttr("deed_entity.nas", "tags.#", "2"),
					apiCheck(t, func(ctx context.Context, c *client.Client, _ *terraform.State) error {
						d, err := c.GetDeclaration(ctx, id, "terraform")
						if err != nil {
							return err
						}
						if d.Metadata.DisplayName != "NAS" || d.Metadata.Version == nil || d.Metadata.Version.Channel != "stable" {
							return fmt.Errorf("declaration metadata: %+v", d.Metadata)
						}
						return nil
					}),
				),
			},
			// The second plan is empty.
			{Config: entityConfig(project, host, "NAS"), PlanOnly: true},
			// Update in place.
			{
				Config:           entityConfig(project, host, "NAS (renamed)"),
				ConfigPlanChecks: expectAction("deed_entity.nas", plancheck.ResourceActionUpdate),
				Check:            resource.TestCheckResourceAttr("deed_entity.nas", "display_name", "NAS (renamed)"),
			},
			// Import by ID and by project/kind/name.
			{ResourceName: "deed_entity.nas", ImportState: true, ImportStateVerify: true},
			{ResourceName: "deed_entity.nas", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: importID("deed_entity.nas", "project", "kind", "name")},
			// Forget the resource (without destroying it), import it by natural key and plan: no change.
			{Config: projectConfig(project) + "\nremoved {\n  from = deed_entity.nas\n  lifecycle {\n    destroy = false\n  }\n}\n"},
			{Config: entityConfig(project, host, "NAS (renamed)"), ResourceName: "deed_entity.nas", ImportState: true, ImportStatePersist: true,
				ImportStateId: project + "/server/nas-01"},
			{Config: entityConfig(project, host, "NAS (renamed)"), ConfigPlanChecks: expectEmptyPlan()},
			// Out-of-band change of the declaration: refresh shows drift, apply restores it.
			{
				PreConfig: oob(t, func(ctx context.Context, c *client.Client) error {
					_, err := c.PutDeclaration(ctx, id, "terraform", "", client.DeclarationInput{
						Metadata: &client.Metadata{DisplayName: "changed outside Terraform", Labels: map[string]string{"tier": "other"}},
						Spec:     json.RawMessage(`{"hostname":"` + host + `"}`)})
					return err
				}),
				Config:           entityConfig(project, host, "NAS (renamed)"),
				ConfigPlanChecks: expectAction("deed_entity.nas", plancheck.ResourceActionUpdate),
				Check: apiCheck(t, func(ctx context.Context, c *client.Client, _ *terraform.State) error {
					d, err := c.GetDeclaration(ctx, id, "terraform")
					if err != nil {
						return err
					}
					var spec map[string]any
					_ = json.Unmarshal(d.Spec, &spec)
					if d.Metadata.DisplayName != "NAS (renamed)" || d.Metadata.Labels["tier"] != "storage" || spec["cpu_cores"] != float64(4) {
						return fmt.Errorf("drift not repaired: %+v %s", d.Metadata, d.Spec)
					}
					return nil
				}),
			},
			// Out-of-band removal of the declaration: the next plan declares it again.
			{
				PreConfig: oob(t, func(ctx context.Context, c *client.Client) error {
					return c.DeleteDeclaration(ctx, id, "terraform")
				}),
				Config:           entityConfig(project, host, "NAS (renamed)"),
				ConfigPlanChecks: expectAction("deed_entity.nas", plancheck.ResourceActionCreate),
				Check:            capture("deed_entity.nas", "id", &id),
			},
			{Config: entityConfig(project, host, "NAS (renamed)"), PlanOnly: true},
		},
	})
}

// checkDeclarationGone verifies that the provider's declaration of *id was removed.
func checkDeclarationGone(t *testing.T, id *string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if *id == "" {
			return nil
		}
		_, err := adminClient(t).GetDeclaration(context.Background(), *id, "terraform")
		if err == nil {
			return fmt.Errorf("declaration of %s still exists", *id)
		}
		if !client.IsNotFound(err) {
			return err
		}
		return nil
	}
}

const svcSpecHeredoc = "<<-EOT\n    {\n      \"protocol\": \"https\",\n      \"port\": 8096,\n      \"url\": \"https://media.example.com\"\n    }\n  EOT"

func TestAccEntity_minimalAndSpecSemantics(t *testing.T) {
	project := randName("acc-min")
	cfg := func(spec string) string {
		return projectConfig(project) + `
resource "deed_entity" "svc" {
  project = deed_project.p.id
  kind    = "service"
  name    = "jellyfin"
  spec    = ` + spec + `
}

resource "deed_entity" "bare" {
  project = deed_project.p.name
  kind    = "application"
  name    = "media"
}
`
	}
	resource.ParallelTest(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg(`jsonencode({ url = "https://media.example.com", port = 8096, protocol = "https" })`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("deed_entity.bare", "spec"),
					resource.TestCheckNoResourceAttr("deed_entity.bare", "labels"),
					resource.TestCheckNoResourceAttr("deed_entity.bare", "display_name"),
					resource.TestMatchResourceAttr("deed_entity.svc", "project", idRE("prj")),
				),
			},
			// Import records the project name; the configuration uses the ID: no replacement.
			// jsonencode produces the same text Deed stores, so the imported spec is identical.
			{ResourceName: "deed_entity.svc", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"project"}},
			// Deed stores the spec re-encoded (sorted keys, compact); semantic equality keeps the configured text, so a
			// hand-formatted document with another key order applies once (a no-op in Deed) and then plans empty.
			{
				Config: cfg(svcSpecHeredoc),
				Check: apiCheck(t, func(ctx context.Context, c *client.Client, s *terraform.State) error {
					id, err := stateAttr(s, "deed_entity.svc", "id")
					if err != nil {
						return err
					}
					d, err := c.GetDeclaration(ctx, id, "terraform")
					if err != nil {
						return err
					}
					if d.ResourceVersion != 1 {
						return fmt.Errorf("re-formatting the spec changed the declaration (version %d)", d.ResourceVersion)
					}
					return nil
				}),
			},
			{Config: cfg(svcSpecHeredoc), ConfigPlanChecks: expectEmptyPlan()},
		},
	})
}

func TestAccEntity_conflictingOwnership(t *testing.T) {
	project := randName("acc-dup")
	resource.ParallelTest(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: projectConfig(project) + `
resource "deed_entity" "a" {
  project = deed_project.p.name
  kind    = "group"
  name    = "platform"
}
`},
			{
				Config: projectConfig(project) + `
resource "deed_entity" "a" {
  project = deed_project.p.name
  kind    = "group"
  name    = "platform"
}

resource "deed_entity" "b" {
  project      = deed_project.p.name
  kind         = "group"
  name         = "platform"
  display_name = "Second owner"
  depends_on   = [deed_entity.a]
}
`,
				ExpectError: errRE(`Entity is already declared by source "terraform"`, "terraform import", project+"/group/platform"),
			},
		},
	})
}

// TestAccEntity_destroyKeepsOtherSources checks that destroying the resource removes only the Terraform declaration:
// a UI annotation facet written for the same entity survives, and so does the entity.
func TestAccEntity_destroyKeepsOtherSources(t *testing.T) {
	project := randName("acc-keep")
	var id string
	resource.ParallelTest(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: projectConfig(project) + `
resource "deed_entity" "repo" {
  project      = deed_project.p.name
  kind         = "repository"
  name         = "homelab-infra"
  display_name = "Homelab infrastructure"
  links        = [{ type = "repository", url = "https://github.com/example/homelab-infra" }]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					capture("deed_entity.repo", "id", &id),
					apiCheck(t, func(ctx context.Context, c *client.Client, _ *terraform.State) error {
						// Another describing source (the project's api source) and a UI annotation. Annotations alone would not keep
						// the entity alive (Deed 0.7.0); the api declaration does.
						if _, err := c.PutDeclaration(ctx, id, "api", "", client.DeclarationInput{
							Metadata: &client.Metadata{Owner: "platform"}}); err != nil {
							return err
						}
						_, err := c.PutDeclaration(ctx, id, "ui", "", client.DeclarationInput{
							Metadata: &client.Metadata{Annotations: map[string]string{"note": "kept by the UI"}}})
						return err
					}),
				),
			},
			// The UI facet is another source's data: it never causes a diff.
			{Config: projectConfig(project) + `
resource "deed_entity" "repo" {
  project      = deed_project.p.name
  kind         = "repository"
  name         = "homelab-infra"
  display_name = "Homelab infrastructure"
  links        = [{ type = "repository", url = "https://github.com/example/homelab-infra" }]
}
`, PlanOnly: true},
			// Remove the resource: only the Terraform facet goes away.
			{
				Config: projectConfig(project),
				Check: apiCheck(t, func(ctx context.Context, c *client.Client, _ *terraform.State) error {
					if _, err := c.GetDeclaration(ctx, id, "terraform"); !client.IsNotFound(err) {
						return fmt.Errorf("terraform declaration still present: %w", err)
					}
					e, err := c.GetEntity(ctx, id)
					if err != nil {
						return fmt.Errorf("entity was deleted with the Terraform declaration: %w", err)
					}
					st := e.ParsedStatus()
					if e.Metadata.Annotations["note"] != "kept by the UI" || e.Metadata.Owner != "platform" || st.TombstonedAt != "" || st.Facets != 2 {
						return fmt.Errorf("independent data lost: annotations=%v status=%+v", e.Metadata.Annotations, st)
					}
					if len(e.Metadata.Links) != 0 || e.Metadata.DisplayName != "" {
						return fmt.Errorf("terraform-declared values survived: %+v", e.Metadata)
					}
					return nil
				}),
			},
		},
	})
}

// TestAccProvider_tokenNotLogged runs Terraform and the provider with trace logging and checks that the API token
// never appears in the logs, while the provider's request logs do. The token reaches the provider block through a
// sensitive variable (TF_VAR_deed_token), so the test framework's own echo of the configuration does not contain it.
func TestAccProvider_tokenNotLogged(t *testing.T) {
	testAccPreCheck(t)
	logPath := filepath.Join(t.TempDir(), "tf.log")
	t.Setenv("TF_ACC_LOG_PATH", logPath)
	t.Setenv("TF_ACC_LOG", "TRACE")
	t.Setenv("TF_LOG_SDK", "TRACE")
	t.Setenv("TF_LOG_PROVIDER", "TRACE")
	t.Setenv("TF_VAR_deed_token", acc.AdminToken)
	t.Setenv(EnvToken, "")
	project := randName("acc-log")
	cfg := fmt.Sprintf(`
variable "deed_token" {
  type      = string
  sensitive = true
}

provider "deed" {
  endpoint = %q
  token    = var.deed_token
}

data "deed_kind" "server" {
  name = "server"
}
`, acc.Endpoint) + projectConfig(project) + `
resource "deed_entity" "x" {
  project = deed_project.p.name
  kind    = "group"
  name    = "logs"
  spec    = jsonencode({ email = "ops@example.com" })
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: func(*terraform.State) error {
				b, err := os.ReadFile(logPath)
				if err != nil {
					return err
				}
				out := string(b)
				if !strings.Contains(out, "deed API request") {
					return fmt.Errorf("provider request logs missing from %s", logPath)
				}
				if i := strings.Index(out, acc.AdminToken[len(client.TokenPrefix):]); i >= 0 {
					return fmt.Errorf("the API token appears in the logs: %s", out[max(0, i-600):min(len(out), i+100)])
				}
				return nil
			},
		}},
	})
}

// TestAccEntity_adoptsByIdentityKey declares a server whose hostname (an identity key of the server kind) matches an
// entity another source reported under a different name: Deed adopts that entity, and the configured name stays in
// the state without inconsistencies or replacement loops.
func TestAccEntity_adoptsByIdentityKey(t *testing.T) {
	testAccPreCheck(t)
	project := randName("acc-adopt")
	host := randName("pve") + ".example.com"
	cfg := projectConfig(project) + fmt.Sprintf(`
resource "deed_entity" "pve" {
  project = deed_project.p.name
  kind    = "server"
  name    = "pve-01"
  spec    = jsonencode({ hostname = %q })
}
`, host)
	var discovered string
	resource.ParallelTest(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: projectConfig(project),
				Check: apiCheck(t, func(ctx context.Context, c *client.Client, _ *terraform.State) error {
					d, err := c.CreateEntity(ctx, client.EntityCreate{Project: project, Source: "api", Kind: "server", Name: "pve01",
						Spec: json.RawMessage(`{"hostname":"` + host + `"}`)})
					if err != nil {
						return err
					}
					discovered = d.EntityID
					return nil
				}),
			},
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("deed_entity.pve", "name", "pve-01"),
					func(s *terraform.State) error {
						id, err := stateAttr(s, "deed_entity.pve", "id")
						if err != nil {
							return err
						}
						if id != discovered {
							return fmt.Errorf("declared %s, expected the adopted entity %s", id, discovered)
						}
						return nil
					},
				),
			},
			{Config: cfg, ConfigPlanChecks: expectEmptyPlan()},
		},
	})
}
