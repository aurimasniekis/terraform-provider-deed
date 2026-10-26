package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	fwprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-log/tflogtest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/aurimasniekis/terraform-provider-deed/internal/client"
)

const unitToken = "deed_at_0123456789abcdefghijklmnopqrstuvwxyzABCDEFG"

var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"deed": providerserver.NewProtocol6WithError(New("test")()),
}

func tfString(s string) types.String { return types.StringValue(s) }
func tfNull() types.String           { return types.StringNull() }

func TestSchemasAreValid(t *testing.T) {
	ctx := context.Background()
	p := New("test")()
	var ps fwprovider.SchemaResponse
	p.Schema(ctx, fwprovider.SchemaRequest{}, &ps)
	if ps.Diagnostics.HasError() {
		t.Fatalf("provider schema: %v", ps.Diagnostics)
	}
	if d := ps.Schema.ValidateImplementation(ctx); d.HasError() {
		t.Fatalf("provider schema: %v", d)
	}
	if !ps.Schema.Attributes["token"].IsSensitive() {
		t.Error("token is not sensitive")
	}
	seen := map[string]bool{}
	for _, f := range p.Resources(ctx) {
		r := f()
		var md fwresource.MetadataResponse
		r.Metadata(ctx, fwresource.MetadataRequest{ProviderTypeName: "deed"}, &md)
		if seen[md.TypeName] {
			t.Fatalf("duplicate resource %s", md.TypeName)
		}
		seen[md.TypeName] = true
		var sr fwresource.SchemaResponse
		r.Schema(ctx, fwresource.SchemaRequest{}, &sr)
		if sr.Diagnostics.HasError() {
			t.Fatalf("%s: %v", md.TypeName, sr.Diagnostics)
		}
		if d := sr.Schema.ValidateImplementation(ctx); d.HasError() {
			t.Fatalf("%s: %v", md.TypeName, d)
		}
		// A token's secret cannot be read back, so importing one could only produce a token without its value.
		if _, ok := r.(fwresource.ResourceWithImportState); !ok && md.TypeName != "deed_service_account_token" {
			t.Errorf("%s does not support import", md.TypeName)
		}
		if _, ok := r.(fwresource.ResourceWithUpgradeState); !ok {
			t.Errorf("%s has no state upgrade plumbing", md.TypeName)
		}
		if sr.Schema.Version != stateSchemaVersion {
			t.Errorf("%s: schema version %d, want %d", md.TypeName, sr.Schema.Version, stateSchemaVersion)
		}
		if _, ok := sr.Schema.Attributes["id"]; !ok {
			t.Errorf("%s has no id attribute", md.TypeName)
		}
		if sr.Schema.MarkdownDescription == "" {
			t.Errorf("%s has no description", md.TypeName)
		}
	}
	want := []string{"deed_project", "deed_project_member", "deed_kind", "deed_relationship_type", "deed_entity", "deed_relationship",
		"deed_source", "deed_service_account", "deed_service_account_token"}
	for _, w := range want {
		if !seen[w] {
			t.Errorf("missing resource %s", w)
		}
	}
	if len(seen) != len(want) {
		t.Errorf("got %d resources, want %d", len(seen), len(want))
	}
	dss := map[string]bool{}
	for _, f := range p.DataSources(ctx) {
		d := f()
		var md datasource.MetadataResponse
		d.Metadata(ctx, datasource.MetadataRequest{ProviderTypeName: "deed"}, &md)
		dss[md.TypeName] = true
		var sr datasource.SchemaResponse
		d.Schema(ctx, datasource.SchemaRequest{}, &sr)
		if diags := sr.Schema.ValidateImplementation(ctx); diags.HasError() {
			t.Fatalf("%s: %v", md.TypeName, diags)
		}
	}
	for _, w := range []string{"deed_project", "deed_kind", "deed_relationship_type", "deed_entity", "deed_entities", "deed_relationship", "deed_source"} {
		if !dss[w] {
			t.Errorf("missing data source %s", w)
		}
	}
}

func TestSourceSecretsAreWriteOnly(t *testing.T) {
	var sr fwresource.SchemaResponse
	NewSourceResource().Schema(context.Background(), fwresource.SchemaRequest{}, &sr)
	a := sr.Schema.Attributes["secrets_wo"]
	if a == nil || !a.IsWriteOnly() || !a.IsSensitive() || a.IsComputed() {
		t.Fatalf("secrets_wo must be write-only, sensitive and not computed: %+v", a)
	}
	for _, v := range []string{"freshness", "last_run_at", "last_error", "running"} {
		if _, ok := sr.Schema.Attributes[v]; ok {
			t.Errorf("deed_source exposes volatile attribute %q (it belongs in the data source)", v)
		}
	}
}

func TestEntityResourceHasNoVolatileAttributes(t *testing.T) {
	var sr fwresource.SchemaResponse
	NewEntityResource().Schema(context.Background(), fwresource.SchemaRequest{}, &sr)
	for _, a := range []string{"health", "freshness", "status", "observed", "valid", "facets"} {
		if _, ok := sr.Schema.Attributes[a]; ok {
			t.Errorf("deed_entity exposes volatile attribute %q (it belongs in the data source)", a)
		}
	}
	for name, a := range sr.Schema.Attributes {
		if a.IsComputed() && name != "id" && name != "project_id" {
			t.Errorf("deed_entity attribute %q is computed; only server-determined stable values may be", name)
		}
	}
}

func TestSplitID(t *testing.T) {
	if p, err := splitID("homelab/usr_2", 2, "<project>/<user_id>"); err != nil || p[0] != "homelab" || p[1] != "usr_2" {
		t.Fatalf("got %v %v", p, err)
	}
	for _, bad := range []string{"", "x", "a/", "/b", "a/b/c"} {
		if _, err := splitID(bad, 2, "<project>/<user_id>"); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestSplitNaturalKey(t *testing.T) {
	cases := map[string][3]string{
		"homelab/server/nas-01":          {"homelab", "server", "nas-01"},
		"homelab/example.com/ups/ups-01": {"homelab", "example.com/ups", "ups-01"},
	}
	for in, want := range cases {
		p, k, n, err := splitNaturalKey(in)
		if err != nil || [3]string{p, k, n} != want {
			t.Errorf("splitNaturalKey(%q) = %q %q %q %v", in, p, k, n, err)
		}
	}
	for _, bad := range []string{"homelab/server", "homelab//x", "/server/x", "homelab/server/"} {
		if _, _, _, err := splitNaturalKey(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestEntityFieldMapping(t *testing.T) {
	cases := map[string]path.Path{
		"metadata.displayName":      path.Root("display_name"),
		"body.metadata.description": path.Root("description"),
		"metadata.labels.Team":      path.Root("labels").AtMapKey("Team"),
		"metadata.annotations":      path.Root("annotations"),
		"metadata.links[1].url":     path.Root("links").AtListIndex(1).AtName("url"),
		"metadata.links[0]":         path.Root("links").AtListIndex(0),
		"metadata.tags[3]":          path.Root("tags"),
		"metadata.version.channel":  path.Root("version_channel"),
		"metadata.owner":            path.Root("owner"),
		"metadata.lifecycle":        path.Root("lifecycle_stage"),
		"spec/cpu_cores":            path.Root("spec"),
		"spec":                      path.Root("spec"),
		"kind":                      path.Root("kind"),
	}
	for in, want := range cases {
		got, ok := entityFields(in)
		if !ok || !got.Equal(want) {
			t.Errorf("entityFields(%q) = %v %v, want %v", in, got, ok, want)
		}
	}
	if _, ok := entityFields("item"); ok {
		t.Error("unknown field mapped")
	}
}

func TestAPIErrorDiagnostics(t *testing.T) {
	var diags diag.Diagnostics
	err := &client.APIError{StatusCode: 422, Code: client.CodeValidationFailed, Detail: "spec does not validate", Method: "POST", Path: "/entities",
		Fields: []client.FieldError{{Field: "spec/port", Message: "got string, want integer"}, {Field: "item", Message: "too large"}}}
	apiError(&diags, "Cannot declare entity", err, entityFields)
	if diags.ErrorsCount() != 2 {
		t.Fatalf("diagnostics: %v", diags)
	}
	var attr, general bool
	for _, d := range diags {
		if wp, ok := d.(diag.DiagnosticWithPath); ok && wp.Path().Equal(path.Root("spec")) {
			attr = strings.Contains(d.Detail(), "got string, want integer")
			continue
		}
		general = strings.Contains(d.Detail(), "422 validation_failed") && strings.Contains(d.Detail(), "item: too large")
	}
	if !attr || !general {
		t.Errorf("diagnostics: %v", diags)
	}
	diags = nil
	apiError(&diags, "Cannot update", &client.APIError{StatusCode: 412, Code: client.CodePreconditionFailed, Method: "PUT", Path: "/x"}, nil)
	if !strings.Contains(diags[0].Detail(), "terraform apply -refresh-only") {
		t.Errorf("412 advice missing: %v", diags)
	}
}

func TestJSONSemanticEquality(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		a, b string
		eq   bool
	}{
		{`{"a":1,"b":[1,2]}`, `{ "b": [1, 2], "a": 1 }`, true},
		// Deed stores documents re-encoded (sorted keys, compact), exactly like Terraform's jsonencode.
		{`{"spec":{"port":443,"hosts":["a","b"]},"name":"x"}`, `{"name":"x","spec":{"hosts":["a","b"],"port":443}}`, true},
		// Number spelling is significant (no float conversion), which jsonencode never produces differently.
		{`{"a":1.0}`, `{"a":1}`, false},
		{`{"a":{"x":"y","z":null}}`, "{\n  \"a\": {\"z\": null, \"x\": \"y\"}\n}", true},
		{`{"a":[1,2]}`, `{"a":[2,1]}`, false},
		{`{"a":"1"}`, `{"a":1}`, false},
		{`{}`, `{"a":null}`, false},
	}
	for _, tc := range cases {
		eq, d := jsontypes.NewNormalizedValue(tc.a).StringSemanticEquals(ctx, jsontypes.NewNormalizedValue(tc.b))
		if d.HasError() || eq != tc.eq {
			t.Errorf("%s vs %s: eq=%v %v, want %v", tc.a, tc.b, eq, d, tc.eq)
		}
	}
}

func TestJSONValue(t *testing.T) {
	null := jsontypes.NewNormalizedNull()
	set := jsontypes.NewNormalizedValue(`{}`)
	if v := jsonValue(nil, null); !v.IsNull() {
		t.Errorf("empty response with null prior = %v", v)
	}
	if v := jsonValue(json.RawMessage(`{}`), null); !v.IsNull() {
		t.Errorf("{} with null prior = %v", v)
	}
	if v := jsonValue(json.RawMessage(`{}`), set); v.ValueString() != `{}` {
		t.Errorf("{} with set prior = %v", v)
	}
	if v := jsonValue(json.RawMessage(`{"port":443}`), null); v.ValueString() != `{"port":443}` {
		t.Errorf("value = %v", v)
	}
}

func TestStrMapListValues(t *testing.T) {
	if v := strVal("", tfString("")); v.IsNull() || v.ValueString() != "" {
		t.Error(`"" prior kept`)
	}
	if v := strVal("", tfNull()); !v.IsNull() {
		t.Error("null prior kept")
	}
	if v := mapVal(nil, map[string]string{}); v == nil || len(v) != 0 {
		t.Error("empty map prior kept")
	}
	if v := mapVal(map[string]string{}, nil); v != nil {
		t.Error("null map prior kept")
	}
	if v := listVal(nil, []string{}); v == nil {
		t.Error("empty list prior kept")
	}
}

func TestOlder(t *testing.T) {
	for _, tc := range []struct {
		v    string
		want bool
	}{{"0.0.9", true}, {"v0.1.0", false}, {"0.2.0-rc.1", false}, {"dev", false}, {"1.0.0", false}, {"0.0.1+abc", true}} {
		if got := older(tc.v, MinimumServerVersion); got != tc.want {
			t.Errorf("older(%q) = %v", tc.v, got)
		}
	}
}

func TestProviderConfigValidation(t *testing.T) {
	t.Setenv(EnvEndpoint, "")
	t.Setenv(EnvToken, "")
	t.Setenv(EnvSource, "")
	const ds = `
data "deed_kind" "x" {
  name = "server"
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      `provider "deed" { endpoint = "https://deed.example.com" }` + ds,
				ExpectError: errRE("Missing Deed API token"),
			},
			{
				Config:      `provider "deed" { token = "` + unitToken + `" }` + ds,
				ExpectError: errRE("Missing Deed endpoint"),
			},
			{
				Config:      "provider \"deed\" {\n  endpoint = \"https://deed.example.com\"\n  token = \"deed_setup_abc\"\n}\n" + ds,
				ExpectError: errRE("Invalid Deed API token", `token must be a Deed API token starting with "deed_at_"`),
			},
			{
				Config:      "provider \"deed\" {\n  endpoint = \"https://deed.example.com\"\n  token = \"deed_at_short\"\n}\n" + ds,
				ExpectError: errRE("Invalid Deed API token", "43 base62 characters"),
			},
			{
				Config:      "provider \"deed\" {\n  endpoint = \"deed.example.com\"\n  token = \"" + unitToken + "\"\n}\n" + ds,
				ExpectError: errRE("Cannot configure the Deed client", "endpoint must be an absolute http"),
			},
			{
				Config: `provider "deed" {
  endpoint = "https://deed.example.com"
  token    = "` + unitToken + `"
  ca_file  = "/nonexistent/ca.pem"
}` + ds,
				ExpectError: errRE("Cannot configure the Deed client", "ca_file"),
			},
			{
				Config: `provider "deed" {
  endpoint = "https://deed.example.com"
  token    = "` + unitToken + `"
  source_name = "Not A Source"
}` + ds,
				ExpectError: errRE("must be a lowercase source name"),
			},
			{
				Config: `resource "terraform_data" "endpoint" {
  input = "https://deed.example.com"
}

provider "deed" {
  endpoint = terraform_data.endpoint.output
  token    = "` + unitToken + `"
}
` + ds,
				ExpectError: errRE("Unknown provider configuration"),
			},
		},
	})
}

// errRE matches every fragment in order, across Terraform's line wrapping.
func errRE(fragments ...string) *regexp.Regexp {
	parts := make([]string, len(fragments))
	for i, f := range fragments {
		words := strings.Fields(f)
		for j, w := range words {
			words[j] = regexp.QuoteMeta(w)
		}
		parts[i] = strings.Join(words, `\s+`)
	}
	return regexp.MustCompile(`(?s)` + strings.Join(parts, ".*"))
}

// TestTokenNeverLogged configures the provider against a fake server with the provider logger captured, performs
// requests and checks that neither the token nor any request or response body reaches the logs.
func TestTokenNeverLogged(t *testing.T) {
	const secret = "spec-secret-value-42"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/system/info":
			_, _ = w.Write([]byte(`{"name":"deed","version":"dev","api_version":"v2","features":[]}`))
		default:
			w.Header().Set("ETag", `"1"`)
			_, _ = w.Write([]byte(`{"entity_id":"ent_1","source":"terraform","spec":{"x":"` + secret + `"},"resource_version":1}`))
		}
	}))
	t.Cleanup(srv.Close)
	var logs bytes.Buffer
	ctx := tflogtest.RootLogger(context.Background(), &logs)
	p := New("test")()
	var sr fwprovider.SchemaResponse
	p.Schema(ctx, fwprovider.SchemaRequest{}, &sr)
	cfg := tfsdk.Config{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), map[string]tftypes.Value{
		"endpoint":    tftypes.NewValue(tftypes.String, srv.URL),
		"token":       tftypes.NewValue(tftypes.String, unitToken),
		"ca_file":     tftypes.NewValue(tftypes.String, nil),
		"source_name": tftypes.NewValue(tftypes.String, nil),
	})}
	var resp fwprovider.ConfigureResponse
	p.Configure(ctx, fwprovider.ConfigureRequest{Config: cfg}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("configure: %v", resp.Diagnostics)
	}
	if resp.Diagnostics.WarningsCount() != 1 || !strings.Contains(resp.Diagnostics[0].Summary(), "Unsupported Deed API version") {
		t.Errorf("api_version warning missing: %v", resp.Diagnostics)
	}
	data := resp.ResourceData.(*providerData)
	if data.source != DefaultSource {
		t.Errorf("source %q", data.source)
	}
	if _, err := data.client.GetDeclaration(ctx, "ent_1", "terraform"); err != nil {
		t.Fatal(err)
	}
	out := logs.String()
	if !strings.Contains(out, "deed API request") || !strings.Contains(out, "/entities/ent_1/declarations/terraform") {
		t.Fatalf("expected request logs, got %s", out)
	}
	if strings.Contains(out, unitToken) || strings.Contains(out, unitToken[len("deed_at_"):]) || strings.Contains(out, secret) {
		t.Fatalf("logs leak the token or a body: %s", out)
	}
}

func TestSourceFromEnvironment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"api_version":"v1","version":"0.1.0"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(EnvEndpoint, srv.URL)
	t.Setenv(EnvToken, unitToken)
	t.Setenv(EnvSource, "infra-tf")
	ctx := context.Background()
	p := New("test")()
	var sr fwprovider.SchemaResponse
	p.Schema(ctx, fwprovider.SchemaRequest{}, &sr)
	null := tftypes.NewValue(tftypes.String, nil)
	cfg := tfsdk.Config{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), map[string]tftypes.Value{
		"endpoint": null, "token": null, "ca_file": null, "source_name": null,
	})}
	var resp fwprovider.ConfigureResponse
	p.Configure(ctx, fwprovider.ConfigureRequest{Config: cfg}, &resp)
	if resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() != 0 {
		t.Fatalf("configure: %v", resp.Diagnostics)
	}
	if got := resp.ResourceData.(*providerData).source; got != "infra-tf" {
		t.Errorf("source %q", got)
	}
}
