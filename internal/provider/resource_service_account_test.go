package provider

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/aurimasniekis/terraform-provider-deed/internal/client"
)

func serviceAccountConfig(name, role, extra, tokenExtra string) string {
	roleLine := ""
	if role != "" {
		roleLine = fmt.Sprintf("  installation_role = %q", role)
	}
	return projectConfig(name) + fmt.Sprintf(`
resource "deed_service_account" "tf" {
  name        = %q
  description = "acceptance"
%s
%s
}

resource "deed_service_account_token" "tf" {
  service_account = deed_service_account.tf.name
  name            = "ci"
  expires_in_days = 30
%s
}

resource "deed_project_member" "tf" {
  project = deed_project.p.name
  user_id = deed_service_account.tf.id
  role    = "viewer"
}
`, name, roleLine, extra, tokenExtra)
}

// asToken checks what the issued token can do.
func asToken(t *testing.T, f func(ctx context.Context, c *client.Client, project string) error) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		tok, err := stateAttr(s, "deed_service_account_token.tf", "token")
		if err != nil {
			return err
		}
		project, err := stateAttr(s, "deed_project.p", "name")
		if err != nil {
			return err
		}
		c, err := client.New(client.Config{Endpoint: acc.Endpoint, Token: tok, UserAgent: "tfacc-sa"})
		if err != nil {
			return err
		}
		return f(context.Background(), c, project)
	}
}

func TestAccServiceAccount_lifecycle(t *testing.T) {
	testAccPreCheck(t)
	name := randName("acc-sa")
	var tokenID string
	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			if _, err := adminClient(t).GetServiceAccount(context.Background(), name); !client.IsNotFound(err) {
				return fmt.Errorf("service account %s still exists (%w)", name, err)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: serviceAccountConfig(name, "", "", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr("deed_service_account.tf", "id", idRE("usr")),
					resource.TestCheckResourceAttr("deed_service_account.tf", "display_name", name),
					resource.TestCheckResourceAttr("deed_service_account.tf", "enabled", "true"),
					resource.TestCheckNoResourceAttr("deed_service_account.tf", "installation_role"),
					resource.TestMatchResourceAttr("deed_service_account_token.tf", "token", regexp.MustCompile(`^deed_at_[0-9A-Za-z]{43}$`)),
					resource.TestCheckResourceAttr("deed_service_account_token.tf", "installation_role", "none"),
					resource.TestCheckResourceAttrPair("deed_service_account_token.tf", "user_id", "deed_service_account.tf", "id"),
					capture("deed_service_account_token.tf", "id", &tokenID),
					// A projects-only service account sees its project (viewer) and cannot manage service accounts.
					asToken(t, func(ctx context.Context, c *client.Client, project string) error {
						if _, err := c.GetProject(ctx, project); err != nil {
							return fmt.Errorf("member project: %w", err)
						}
						if _, err := c.CreateServiceAccount(ctx, client.ServiceAccountInput{Name: name + "-x"}); !client.HasCode(err, client.CodeForbidden) {
							return fmt.Errorf("creating a service account without the admin role: %w", err)
						}
						return nil
					}),
				),
			},
			{Config: serviceAccountConfig(name, "", "", ""), ConfigPlanChecks: expectEmptyPlan()},
			// Granting the admin role changes the account in place; the token keeps "none" until it is replaced.
			{
				Config:           serviceAccountConfig(name, "admin", "", `  keepers = { rotated = "1" }`),
				ConfigPlanChecks: expectAction("deed_service_account_token.tf", plancheck.ResourceActionReplace),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("deed_service_account.tf", "installation_role", "admin"),
					resource.TestCheckResourceAttr("deed_service_account_token.tf", "installation_role", "admin"),
					// The admin token can rotate tokens itself.
					asToken(t, func(ctx context.Context, c *client.Client, _ string) error {
						tok, err := c.CreateServiceAccountToken(ctx, name, client.ServiceTokenInput{Name: "self-rotated", ExpiresInDays: 1})
						if err != nil {
							return err
						}
						return c.RevokeServiceAccountToken(ctx, name, tok.ID)
					}),
					// The replaced token was revoked.
					apiCheck(t, func(ctx context.Context, c *client.Client, _ *terraform.State) error {
						old, err := c.GetServiceAccountToken(ctx, name, tokenID)
						if err != nil {
							return err
						}
						if old.RevokedAt == nil {
							return fmt.Errorf("replaced token %s was not revoked", tokenID)
						}
						return nil
					}),
				),
			},
			{ResourceName: "deed_service_account.tf", ImportState: true, ImportStateVerify: true, ImportStateId: name},
			// A token revoked outside Terraform is created again.
			{
				PreConfig:        oob(t, func(ctx context.Context, c *client.Client) error { return revokeAll(ctx, c, name) }),
				Config:           serviceAccountConfig(name, "admin", "", `  keepers = { rotated = "1" }`),
				ConfigPlanChecks: expectAction("deed_service_account_token.tf", plancheck.ResourceActionCreate),
			},
			// rotate_before_days longer than the lifetime replaces the token on every plan.
			{
				Config:             serviceAccountConfig(name, "admin", "", "  keepers = { rotated = \"1\" }\n  rotate_before_days = 60"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			// Disabling revokes the tokens (the token resource is recreated after re-enabling).
			{
				Config:             serviceAccountConfig(name, "admin", "  enabled = false", `  keepers = { rotated = "1" }`),
				Check:              resource.TestCheckResourceAttr("deed_service_account.tf", "enabled", "false"),
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func revokeAll(ctx context.Context, c *client.Client, account string) error {
	toks, err := c.ListServiceAccountTokens(ctx, account)
	if err != nil {
		return err
	}
	for _, t := range toks {
		if t.RevokedAt == nil {
			if err := c.RevokeServiceAccountToken(ctx, account, t.ID); err != nil {
				return err
			}
		}
	}
	return nil
}
