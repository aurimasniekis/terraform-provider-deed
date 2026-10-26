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

func TestSamePolicy(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{`{"on_absent":"orphan"}`, `{"on_absent":"orphan"}`, true},
		{`{"on_absent":"orphan","max_removal_percent":0,"allowed_kinds":[]}`, `{"on_absent":"orphan"}`, true},
		{`{"max_removal_percent":90,"on_absent":"keep"}`, `{"on_absent":"keep","max_removal_percent":90}`, true},
		{`{"on_absent":"orphan"}`, `{"on_absent":"keep"}`, false},
		{`{"priority":0}`, `{}`, false},
		{`not json`, `{}`, false},
	} {
		if got := samePolicy([]byte(c.a), []byte(c.b)); got != c.want {
			t.Errorf("samePolicy(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

func sourceConfig(project, secretsBlock, policy string) string {
	return projectConfig(project) + fmt.Sprintf(`
resource "deed_source" "gh" {
  project       = deed_project.p.name
  name          = "github"
  provider_type = "github"
  enabled       = false # never reach api.github.com from tests
  config        = jsonencode({ owners = ["example-org"], pull_requests = false })
%s
%s
}

data "deed_source" "gh" {
  project = deed_project.p.name
  name    = deed_source.gh.name
}
`, secretsBlock, policy)
}

func TestAccSource_lifecycle(t *testing.T) {
	testAccPreCheck(t)
	project := randName("acc-src")
	var id string
	secrets := func(version int, token string) string {
		if token == "" {
			return fmt.Sprintf("  secrets_wo_version = %d", version)
		}
		return fmt.Sprintf("  secrets_wo         = { token = %q }\n  secrets_wo_version = %d", token, version)
	}
	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			if _, err := adminClient(t).FindSource(context.Background(), project, "github"); !client.IsNotFound(err) {
				return fmt.Errorf("source still exists (%w)", err)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: sourceConfig(project, secrets(1, "github_pat_acceptance"), ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					capture("deed_source.gh", "id", &id),
					resource.TestMatchResourceAttr("deed_source.gh", "id", idRE("src")),
					resource.TestCheckResourceAttr("deed_source.gh", "display_name", "github"),
					resource.TestCheckResourceAttr("deed_source.gh", "layer", "imported"),
					resource.TestCheckResourceAttr("deed_source.gh", "pull", "true"),
					resource.TestCheckResourceAttr("deed_source.gh", "schedule_seconds", "900"),
					resource.TestCheckResourceAttr("deed_source.gh", "secret_fields_set.#", "1"),
					resource.TestCheckTypeSetElemAttr("deed_source.gh", "secret_fields_set.*", "token"),
					// Write-only: never in state.
					resource.TestCheckNoResourceAttr("deed_source.gh", "secrets_wo"),
					resource.TestCheckResourceAttr("deed_source.gh", "policy", `{"max_removal_percent":90}`),
					resource.TestCheckResourceAttr("data.deed_source.gh", "provider_type", "github"),
					resource.TestCheckResourceAttr("data.deed_source.gh", "enabled", "false"),
					resource.TestCheckResourceAttr("data.deed_source.gh", "last_error", ""),
					resource.TestCheckResourceAttrPair("data.deed_source.gh", "id", "deed_source.gh", "id"),
				),
			},
			// A different secret value alone is invisible to Terraform (write-only): no change without a new version.
			{Config: sourceConfig(project, secrets(1, "github_pat_other"), ""), ConfigPlanChecks: expectEmptyPlan()},
			// Policy, schedule and display name change in place.
			{
				Config: sourceConfig(project, secrets(1, "github_pat_other"),
					"  policy = jsonencode({ on_absent = \"orphan\", max_removal_percent = 50 })\n  schedule_seconds = 3600\n  display_name = \"GitHub\""),
				ConfigPlanChecks: expectAction("deed_source.gh", plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("deed_source.gh", "schedule_seconds", "3600"),
					resource.TestCheckResourceAttr("deed_source.gh", "display_name", "GitHub"),
					resource.TestCheckResourceAttr("data.deed_source.gh", "policy", `{"max_removal_percent":50,"on_absent":"orphan"}`),
				),
			},
			// A new version without secrets_wo clears the stored credentials.
			{
				Config:           sourceConfig(project, secrets(2, ""), "  policy = jsonencode({ on_absent = \"orphan\", max_removal_percent = 50 })\n  schedule_seconds = 3600\n  display_name = \"GitHub\""),
				ConfigPlanChecks: expectAction("deed_source.gh", plancheck.ResourceActionUpdate),
				Check:            resource.TestCheckResourceAttr("deed_source.gh", "secret_fields_set.#", "0"),
			},
			{
				ResourceName: "deed_source.gh", ImportState: true, ImportStateVerify: true, ImportStateId: project + "/github",
				ImportStateVerifyIgnore: []string{"secrets_wo_version", "project"},
			},
			// Out-of-band change and deletion.
			{
				PreConfig: oob(t, func(ctx context.Context, c *client.Client) error {
					on := true
					_, err := c.UpdateSource(ctx, id, "", client.SourceInput{Enabled: &on, DisplayName: "changed"})
					return err
				}),
				Config:           sourceConfig(project, secrets(2, ""), "  policy = jsonencode({ on_absent = \"orphan\", max_removal_percent = 50 })\n  schedule_seconds = 3600\n  display_name = \"GitHub\""),
				ConfigPlanChecks: expectAction("deed_source.gh", plancheck.ResourceActionUpdate),
			},
			{
				PreConfig:        oob(t, func(ctx context.Context, c *client.Client) error { return c.DeleteSource(ctx, id) }),
				Config:           sourceConfig(project, secrets(3, "github_pat_new"), ""),
				ConfigPlanChecks: expectAction("deed_source.gh", plancheck.ResourceActionCreate),
				Check:            resource.TestCheckResourceAttr("deed_source.gh", "secret_fields_set.#", "1"),
			},
		},
	})
}

func TestAccSource_push(t *testing.T) {
	testAccPreCheck(t)
	project := randName("acc-push")
	cfg := projectConfig(project) + `
resource "deed_source" "ci" {
  project                   = deed_project.p.id
  name                      = "ci-jobs"
  provider_type             = "push"
  expected_interval_seconds = 3600
}
`
	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("deed_source.ci", "push", "true"),
					resource.TestCheckResourceAttr("deed_source.ci", "pull", "false"),
					resource.TestCheckResourceAttr("deed_source.ci", "schedule_seconds", "0"),
					resource.TestCheckResourceAttr("deed_source.ci", "expected_interval_seconds", "3600"),
					resource.TestCheckNoResourceAttr("deed_source.ci", "config"),
				),
			},
			{Config: cfg, ConfigPlanChecks: expectEmptyPlan()},
			{ResourceName: "deed_source.ci", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"project"}},
		},
	})
}
