package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/aurimasniekis/terraform-provider-deed/internal/client"
)

func randName(prefix string) string {
	return prefix + "-" + acctest.RandStringFromCharSet(8, "abcdefghijklmnopqrstuvwxyz0123456789")
}

// attr returns an attribute of a resource from state.
func stateAttr(s *terraform.State, name, key string) (string, error) {
	rs, ok := s.RootModule().Resources[name]
	if !ok {
		return "", fmt.Errorf("%s not in state", name)
	}
	v := rs.Primary.Attributes[key]
	if v == "" {
		return "", fmt.Errorf("%s.%s is empty", name, key)
	}
	return v, nil
}

// capture stores an attribute of a resource in *dst (for out-of-band steps).
func capture(name, key string, dst *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		v, err := stateAttr(s, name, key)
		*dst = v
		return err
	}
}

// importID builds "<a>/<b>/…" import IDs from state attributes.
func importID(name string, keys ...string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			v, err := stateAttr(s, name, k)
			if err != nil {
				return "", err
			}
			parts = append(parts, v)
		}
		return strings.Join(parts, "/"), nil
	}
}

func expectAction(name string, a plancheck.ResourceActionType) resource.ConfigPlanChecks {
	return resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(name, a)}}
}

func expectEmptyPlan() resource.ConfigPlanChecks {
	return resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
}

// oob runs an out-of-band API call with the admin token and fails the test on error.
func oob(t *testing.T, f func(ctx context.Context, c *client.Client) error) func() {
	return func() {
		t.Helper()
		if err := f(context.Background(), adminClient(t)); err != nil {
			t.Fatalf("out-of-band change: %v", err)
		}
	}
}

// apiCheck runs an API assertion as a TestCheckFunc.
func apiCheck(t *testing.T, f func(ctx context.Context, c *client.Client, s *terraform.State) error) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		return f(context.Background(), adminClient(t), s)
	}
}

func projectConfig(name string) string {
	return fmt.Sprintf(`
resource "deed_project" "p" {
  name          = %q
  force_destroy = true
}
`, name)
}

func idRE(prefix string) *regexp.Regexp { return regexp.MustCompile(`^` + prefix + `_[0-9a-z]+$`) }
