package provider

import (
	"context"
	"encoding/json"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/aurimasniekis/terraform-provider-deed/internal/client"
)

// ---------------------------------------------------------------------------------------------------------------------
// deed_integration

var (
	_ resource.ResourceWithImportState  = (*integrationResource)(nil)
	_ resource.ResourceWithConfigure    = (*integrationResource)(nil)
	_ resource.ResourceWithUpgradeState = (*integrationResource)(nil)
)

type integrationResource struct{ baseResource }

// NewIntegrationResource returns the deed_integration resource.
func NewIntegrationResource() resource.Resource { return &integrationResource{} }

type integrationModel struct {
	ID               types.String         `tfsdk:"id"`
	Name             types.String         `tfsdk:"name"`
	Adapter          types.String         `tfsdk:"adapter"`
	Config           jsontypes.Normalized `tfsdk:"config"`
	Enabled          types.Bool           `tfsdk:"enabled"`
	SecretsWO        types.Map            `tfsdk:"secrets_wo"`
	SecretsWOVersion types.Int64          `tfsdk:"secrets_wo_version"`
	HasSecrets       types.Bool           `tfsdk:"has_secrets"`
}

func (r *integrationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_integration"
}

func (r *integrationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Version: stateSchemaVersion,
		MarkdownDescription: "A Deed telemetry integration: a backend that bindings (`deed_telemetry_binding`) query — `prometheus` " +
			"(Prometheus, Mimir, VictoriaMetrics, or a Grafana data-source proxy URL), `loki`, `grafana` (links and search), " +
			"`natsmon` or `opensearch`. Installation-wide; requires an installation `admin` token. See Deed's docs/TELEMETRY.md " +
			"for each adapter's configuration.\n\n" +
			"Credentials go in `secrets_wo`, a **write-only** argument (Terraform 1.11+): sent to Deed, never stored in state or " +
			"plan. Change `secrets_wo_version` to send new values.\n\n" +
			"~> Deleting an integration deletes its bindings.",
		Attributes: map[string]schema.Attribute{
			"id": idAttribute("Integration ID."),
			"name": schema.StringAttribute{
				MarkdownDescription: "Unique name; bindings refer to the integration by name or ID.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.RegexMatches(projectNameRE, "must be a lowercase slug (a-z, 0-9, -) of at most 63 characters")},
			},
			"adapter": requiredImmutableString("Adapter: `prometheus`, `loki`, `grafana`, `natsmon` or `opensearch`.",
				stringvalidator.LengthBetween(1, 32)),
			"config": schema.StringAttribute{
				CustomType: jsontypes.NormalizedType{},
				MarkdownDescription: "Adapter configuration as a JSON object string (use `jsonencode`), validated by Deed against the " +
					"adapter's schema: for example `url`, `tenant`, `allow_cidrs`, `insecure_http`, `timeout_seconds`. Secret fields " +
					"do not belong here: put them in `secrets_wo`.",
				Optional:   true,
				Validators: []validator.String{jsonObjectValidator{}},
			},
			"enabled": schema.BoolAttribute{
				MarkdownDescription: "Whether bindings may query it (default `true`).",
				Optional:            true, Computed: true, Default: booldefault.StaticBool(true),
			},
			"secrets_wo": schema.MapAttribute{
				MarkdownDescription: "Write-only credentials keyed by the adapter's secret fields (`token`, or `username` and " +
					"`password`; OpenSearch also `api_key`). Sent on create and whenever `secrets_wo_version` changes; never stored in " +
					"state. When the version changes and this is unset, the stored credentials are cleared.",
				ElementType: types.StringType,
				Optional:    true, Sensitive: true, WriteOnly: true,
			},
			"secrets_wo_version": schema.Int64Attribute{
				MarkdownDescription: "Change this to send `secrets_wo` again (rotation).",
				Optional:            true,
			},
			"has_secrets": schema.BoolAttribute{
				MarkdownDescription: "Whether Deed stores credentials for the integration.", Computed: true,
			},
		},
	}
}

var integrationFields = rootMapper(map[string]string{"name": "name", "adapter": "adapter", "config": "config", "secrets": "secrets_wo"})

func (m *integrationModel) fill(it *client.Integration) {
	m.ID = types.StringValue(it.ID)
	m.Name = types.StringValue(it.Name)
	m.Adapter = types.StringValue(it.Adapter)
	m.Config = jsonValue(it.Config, m.Config)
	m.Enabled = types.BoolValue(it.Enabled)
	m.HasSecrets = types.BoolValue(it.HasSecrets)
	m.SecretsWO = types.MapNull(types.StringType)
}

func (m *integrationModel) input() client.IntegrationInput {
	in := client.IntegrationInput{Name: m.Name.ValueString(), Adapter: m.Adapter.ValueString(), Config: rawJSON(m.Config)}
	if !m.Enabled.IsNull() && !m.Enabled.IsUnknown() {
		b := m.Enabled.ValueBool()
		in.Enabled = &b
	}
	if in.Config == nil {
		in.Config = json.RawMessage(`{}`)
	}
	return in
}

func (r *integrationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan integrationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	secrets, set, diags := configSecrets(ctx, req.Config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	in := plan.input()
	if set {
		in.Secrets = secrets
	}
	it, err := r.client.CreateIntegration(ctx, in)
	if err != nil {
		if client.HasCode(err, client.CodeAlreadyExists) {
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Integration already exists",
				"An integration named "+plan.Name.ValueString()+" already exists. Import it instead of creating it:\n\n  terraform import <address> "+
					plan.Name.ValueString())
			return
		}
		apiError(&resp.Diagnostics, "Cannot create integration", err, integrationFields)
		return
	}
	plan.fill(it)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(saveETag(ctx, resp.Private, it.ETag)...)
}

func (r *integrationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state integrationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	it, err := r.client.GetIntegration(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot read integration", err, nil)
		return
	}
	state.fill(it)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(saveETag(ctx, resp.Private, it.ETag)...)
}

func (r *integrationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state integrationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in := plan.input()
	in.Adapter = ""
	if !plan.SecretsWOVersion.Equal(state.SecretsWOVersion) {
		secrets, set, diags := configSecrets(ctx, req.Config)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		if !set {
			secrets = map[string]string{}
		}
		in.Secrets = secrets
	}
	it, err := r.client.UpdateIntegration(ctx, state.ID.ValueString(), loadETag(ctx, req.Private), in)
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot update integration", err, integrationFields)
		return
	}
	plan.fill(it)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(saveETag(ctx, resp.Private, it.ETag)...)
}

func (r *integrationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state integrationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteIntegration(ctx, state.ID.ValueString()); err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "Cannot delete integration", err, nil)
	}
}

func (r *integrationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	it, err := r.client.GetIntegration(ctx, req.ID)
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot import integration", err, nil)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), it.ID)...)
}

// ---------------------------------------------------------------------------------------------------------------------
// deed_integrations data source

var _ datasource.DataSourceWithConfigure = (*integrationsDataSource)(nil)

type integrationsDataSource struct{ baseDataSource }

// NewIntegrationsDataSource returns the deed_integrations data source.
func NewIntegrationsDataSource() datasource.DataSource { return &integrationsDataSource{} }

type integrationDSItem struct {
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	Adapter    types.String `tfsdk:"adapter"`
	Enabled    types.Bool   `tfsdk:"enabled"`
	HasSecrets types.Bool   `tfsdk:"has_secrets"`
	LastTestOK types.Bool   `tfsdk:"last_test_ok"`
	LastError  types.String `tfsdk:"last_error"`
}

type integrationsDSModel struct {
	Adapter      types.String        `tfsdk:"adapter"`
	Integrations []integrationDSItem `tfsdk:"integrations"`
}

func (d *integrationsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_integrations"
}

func (d *integrationsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		MarkdownDescription: "Lists Deed's telemetry integrations (never their credentials), optionally of one adapter.",
		Attributes: map[string]dschema.Attribute{
			"adapter": dschema.StringAttribute{MarkdownDescription: "Only integrations of this adapter.", Optional: true},
			"integrations": dschema.ListNestedAttribute{
				MarkdownDescription: "Integrations, by name.", Computed: true,
				NestedObject: dschema.NestedAttributeObject{Attributes: map[string]dschema.Attribute{
					"id":           dsString("Integration ID."),
					"name":         dsString("Name."),
					"adapter":      dsString("Adapter."),
					"enabled":      dsBool("Whether bindings may query it."),
					"has_secrets":  dsBool("Whether credentials are stored."),
					"last_test_ok": dsBool("Outcome of the last connection test (null when never tested)."),
					"last_error":   dsString("Error of the last failed test."),
				}},
			},
		},
	}
}

func (d *integrationsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m integrationsDSModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	list, err := d.client.ListIntegrations(ctx)
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot list integrations", err, nil)
		return
	}
	m.Integrations = []integrationDSItem{}
	for _, it := range list {
		if a := m.Adapter.ValueString(); a != "" && it.Adapter != a {
			continue
		}
		ok := types.BoolNull()
		if it.LastTestOK != nil {
			ok = types.BoolValue(*it.LastTestOK)
		}
		m.Integrations = append(m.Integrations, integrationDSItem{ID: types.StringValue(it.ID), Name: types.StringValue(it.Name),
			Adapter: types.StringValue(it.Adapter), Enabled: types.BoolValue(it.Enabled), HasSecrets: types.BoolValue(it.HasSecrets), LastTestOK: ok,
			LastError: types.StringValue(it.LastError)})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
