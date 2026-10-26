// Package provider implements the Deed Terraform provider with terraform-plugin-framework.
//
// Every entity and relationship the provider writes is a declaration made through one Deed source (default
// "terraform"). Resource state mirrors only that declaration, never the effective view merged from every source, so
// imports, runtime observations and UI annotations never cause plan diffs, and destroying a resource removes only the
// provider's facet.
package provider

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/aurimasniekis/terraform-provider-deed/internal/client"
)

// Environment variables read by the provider.
const (
	EnvEndpoint = "DEED_ENDPOINT"
	EnvToken    = "DEED_TOKEN"
	EnvCAFile   = "DEED_CA_FILE"
	EnvSource   = "DEED_SOURCE"
)

// DefaultSource is the Deed source the provider declares through unless configured otherwise.
const DefaultSource = "terraform"

// SupportedAPIVersion is the Deed API major version this provider speaks.
const SupportedAPIVersion = "v1"

// MinimumServerVersion is the oldest Deed release this provider supports (docs/COMPATIBILITY.md in go-deed).
const MinimumServerVersion = "0.1.0"

var sourceNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,61}[a-z0-9])?$`)

var _ provider.Provider = (*deedProvider)(nil)

type deedProvider struct {
	version string
}

type providerModel struct {
	Endpoint types.String `tfsdk:"endpoint"`
	Token    types.String `tfsdk:"token"`
	CAFile   types.String `tfsdk:"ca_file"`
	Source   types.String `tfsdk:"source_name"`
}

// providerData is handed to every resource and data source.
type providerData struct {
	client *client.Client
	// source is the Deed source every declaration is made through.
	source string
}

// New returns a provider factory.
func New(version string) func() provider.Provider {
	return func() provider.Provider { return &deedProvider{version: version} }
}

func (p *deedProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "deed"
	resp.Version = p.version
}

func (p *deedProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a [Deed](https://github.com/aurimasniekis/go-deed) installation — a declarative infrastructure " +
			"catalog — through its versioned API (`/api/v1`): projects, custom kinds and relationship types, entities and the " +
			"relationships between them.\n\n" +
			"Every entity and relationship is written as a **declaration through one Deed source** (`terraform` by default). " +
			"Terraform state holds only that declaration, never the merged view, so data contributed by other sources (imports, " +
			"runtime observations, UI annotations) never causes a diff and survives `terraform destroy`.\n\n" +
			"Authenticate with a user API token (`deed_at_…`). Tokens are created by a signed-in user (the account page of the Deed " +
			"web UI, or `POST /api/v1/me/tokens` from an interactive session with a recent sign-in) and act with at most that user's " +
			"roles. A token with the installation `admin` role can manage everything; a token capped at a project `editor` role can " +
			"manage entities and relationships in that project only.",
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				MarkdownDescription: "Public URL of the Deed installation, for example `https://deed.example.com`. May also be set with the `" +
					EnvEndpoint + "` environment variable.",
				Optional: true,
			},
			"token": schema.StringAttribute{
				MarkdownDescription: "User API token (`deed_at_` followed by 43 base62 characters). May also be set with the `" + EnvToken +
					"` environment variable. Managing projects, kinds and relationship types needs a token with the installation `admin` " +
					"role; entities and relationships need the project `editor` role.",
				Optional:  true,
				Sensitive: true,
			},
			"ca_file": schema.StringAttribute{
				MarkdownDescription: "Path to a PEM file with additional CA certificates to trust (private PKI). May also be set with `" +
					EnvCAFile + "`.",
				Optional: true,
			},
			"source_name": schema.StringAttribute{
				MarkdownDescription: "Name of the Deed source every entity and relationship is declared through. Defaults to `" + DefaultSource +
					"` (a system source of every project); may also be set with `" + EnvSource + "`. Use a dedicated manual source to run " +
					"several independent Terraform configurations against the same entities. (Terraform reserves the argument name " +
					"`source` in provider blocks, hence `source_name`.)",
				Optional:   true,
				Validators: []validator.String{stringvalidator.RegexMatches(sourceNameRE, "must be a lowercase source name")},
			},
		},
	}
}

func (p *deedProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for name, v := range map[string]types.String{"endpoint": cfg.Endpoint, "token": cfg.Token, "ca_file": cfg.CAFile, "source_name": cfg.Source} {
		if v.IsUnknown() {
			resp.Diagnostics.AddAttributeError(path.Root(name), "Unknown provider configuration",
				"The provider cannot be configured with a value that is only known after apply. Set "+name+" statically or via the environment.")
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}
	endpoint := firstNonEmpty(cfg.Endpoint.ValueString(), os.Getenv(EnvEndpoint))
	token := firstNonEmpty(cfg.Token.ValueString(), os.Getenv(EnvToken))
	caFile := firstNonEmpty(cfg.CAFile.ValueString(), os.Getenv(EnvCAFile))
	source := firstNonEmpty(cfg.Source.ValueString(), os.Getenv(EnvSource), DefaultSource)
	if endpoint == "" {
		resp.Diagnostics.AddAttributeError(path.Root("endpoint"), "Missing Deed endpoint",
			"Set the endpoint argument or the "+EnvEndpoint+" environment variable to the public URL of Deed.")
	}
	if token == "" {
		resp.Diagnostics.AddAttributeError(path.Root("token"), "Missing Deed API token",
			"Set the token argument or the "+EnvToken+" environment variable to a Deed API token (deed_at_…).")
	} else if err := client.ValidateToken(token); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("token"), "Invalid Deed API token", err.Error())
	}
	if !sourceNameRE.MatchString(source) {
		resp.Diagnostics.AddAttributeError(path.Root("source_name"), "Invalid Deed source",
			fmt.Sprintf("%q is not a valid source name (lowercase letters, digits, '.', '_' and '-').", source))
	}
	if resp.Diagnostics.HasError() {
		return
	}
	c, err := client.New(client.Config{
		Endpoint:  endpoint,
		Token:     token,
		CAFile:    caFile,
		UserAgent: "terraform-provider-deed/" + p.version,
		Observer: func(ctx context.Context, method, apiPath string, status, attempt int, err error) {
			fields := map[string]any{"method": method, "path": apiPath, "status": status, "attempt": attempt}
			if err != nil {
				fields["error"] = err.Error()
			}
			tflog.Debug(ctx, "deed API request", fields)
		},
	})
	if err != nil {
		resp.Diagnostics.AddError("Cannot configure the Deed client", err.Error())
		return
	}
	checkServer(ctx, c, &resp.Diagnostics)
	data := &providerData{client: c, source: source}
	resp.ResourceData = data
	resp.DataSourceData = data
}

// checkServer reads /system/info and warns (never fails) when the server speaks another API major version or is
// older than the minimum supported release. Network failures surface later with the first real request.
func checkServer(ctx context.Context, c *client.Client, diags interface {
	AddWarning(summary, detail string)
}) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	info, err := c.SystemInfo(ctx)
	if err != nil {
		tflog.Warn(ctx, "cannot read Deed system info", map[string]any{"error": err.Error()})
		return
	}
	if info.APIVersion != SupportedAPIVersion {
		diags.AddWarning("Unsupported Deed API version",
			fmt.Sprintf("The Deed server at %s reports API version %q; this provider speaks %q. Some operations may fail; "+
				"upgrade the provider or the server (see the compatibility table in the provider README).", c.Endpoint(), info.APIVersion, SupportedAPIVersion))
	}
	if older(info.Version, MinimumServerVersion) {
		diags.AddWarning("Deed server is older than supported",
			fmt.Sprintf("The Deed server reports version %s; this provider needs %s or newer.", info.Version, MinimumServerVersion))
	}
}

// older reports whether version v (semantic, optional leading "v") is lower than minimum. Non-release versions such
// as "dev" are never reported as older.
func older(v, minimum string) bool {
	a, ok1 := semver(v)
	b, ok2 := semver(minimum)
	if !ok1 || !ok2 {
		return false
	}
	for i := range 3 {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func semver(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, s := range parts {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func (p *deedProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewProjectResource,
		NewProjectMemberResource,
		NewKindResource,
		NewRelationshipTypeResource,
		NewEntityResource,
		NewRelationshipResource,
		NewSourceResource,
		NewServiceAccountResource,
		NewServiceAccountTokenResource,
	}
}

func (p *deedProvider) DataSources(context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewProjectDataSource,
		NewKindDataSource,
		NewRelationshipTypeDataSource,
		NewEntityDataSource,
		NewEntitiesDataSource,
		NewRelationshipDataSource,
		NewSourceDataSource,
	}
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
