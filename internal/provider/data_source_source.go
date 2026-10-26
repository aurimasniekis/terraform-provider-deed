package provider

import (
	"context"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/aurimasniekis/terraform-provider-deed/internal/client"
)

var (
	_ datasource.DataSourceWithConfigure        = (*sourceDataSource)(nil)
	_ datasource.DataSourceWithConfigValidators = (*sourceDataSource)(nil)
)

type sourceDataSource struct{ baseDataSource }

// NewSourceDataSource returns the deed_source data source.
func NewSourceDataSource() datasource.DataSource { return &sourceDataSource{} }

type sourceDSModel struct {
	ID                      types.String         `tfsdk:"id"`
	Project                 types.String         `tfsdk:"project"`
	Name                    types.String         `tfsdk:"name"`
	ProjectID               types.String         `tfsdk:"project_id"`
	DisplayName             types.String         `tfsdk:"display_name"`
	Provider                types.String         `tfsdk:"provider_type"`
	Layer                   types.String         `tfsdk:"layer"`
	System                  types.Bool           `tfsdk:"system"`
	Enabled                 types.Bool           `tfsdk:"enabled"`
	Config                  jsontypes.Normalized `tfsdk:"config"`
	Policy                  jsontypes.Normalized `tfsdk:"policy"`
	ScheduleSeconds         types.Int64          `tfsdk:"schedule_seconds"`
	ExpectedIntervalSeconds types.Int64          `tfsdk:"expected_interval_seconds"`
	SecretFieldsSet         types.Set            `tfsdk:"secret_fields_set"`
	Pull                    types.Bool           `tfsdk:"pull"`
	Push                    types.Bool           `tfsdk:"push"`
	Running                 types.Bool           `tfsdk:"running"`
	Freshness               types.String         `tfsdk:"freshness"`
	LastGeneration          types.Int64          `tfsdk:"last_generation"`
	LastRunAt               types.String         `tfsdk:"last_run_at"`
	LastSuccessAt           types.String         `tfsdk:"last_success_at"`
	LastHeartbeatAt         types.String         `tfsdk:"last_heartbeat_at"`
	LastError               types.String         `tfsdk:"last_error"`
	NextRunAt               types.String         `tfsdk:"next_run_at"`
}

func (d *sourceDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_source"
}

func dsInt64(desc string) schema.Int64Attribute {
	return schema.Int64Attribute{MarkdownDescription: desc, Computed: true}
}

func (d *sourceDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads a Deed source and its sync status by ID, or by `project` and `name` (any source the token can read). " +
			"Useful for checks, for example that a connector synced recently and without errors.",
		Attributes: map[string]schema.Attribute{
			"id":                        schema.StringAttribute{MarkdownDescription: "Source ID (`src_…`).", Optional: true, Computed: true},
			"project":                   schema.StringAttribute{MarkdownDescription: "Project name or ID (with `name`).", Optional: true, Computed: true},
			"name":                      schema.StringAttribute{MarkdownDescription: "Source name (with `project`).", Optional: true, Computed: true},
			"project_id":                dsString("Project ID."),
			"display_name":              dsString("Display name."),
			"provider_type":             dsString("Source provider (`github`, `terraform_state`, `push`, …)."),
			"layer":                     dsString("Fact layer the source writes."),
			"system":                    dsBool("Whether Deed created the source for the project (`api`, `terraform`, `ui`, `overrides`)."),
			"enabled":                   dsBool("Whether the source runs and accepts data."),
			"config":                    dsJSON("Provider configuration (JSON)."),
			"policy":                    dsJSON("Reconciliation policy (JSON)."),
			"schedule_seconds":          dsInt64("Pull interval in seconds (`0` = on demand)."),
			"expected_interval_seconds": dsInt64("Expected data interval in seconds."),
			"secret_fields_set": schema.SetAttribute{MarkdownDescription: "Secret fields that have a stored value.", ElementType: types.StringType,
				Computed: true},
			"pull":              dsBool("Whether Deed pulls from the source."),
			"push":              dsBool("Whether programs push to the source."),
			"running":           dsBool("Whether a sync is running now."),
			"freshness":         dsString("Freshness: `fresh`, `stale`, `unknown` or `disabled`."),
			"last_generation":   dsInt64("Generation of the last committed snapshot."),
			"last_run_at":       dsString("Last sync start (RFC 3339), empty when never run."),
			"last_success_at":   dsString("Last successful sync (RFC 3339), empty when never."),
			"last_heartbeat_at": dsString("Last heartbeat from a push source (RFC 3339)."),
			"last_error":        dsString("Error of the last failed sync, empty after a success."),
			"next_run_at":       dsString("Next scheduled sync (RFC 3339)."),
		},
	}
}

func (d *sourceDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name")),
		datasourcevalidator.RequiredTogether(path.MatchRoot("project"), path.MatchRoot("name")),
	}
}

func rfc3339(t *time.Time) types.String {
	if t == nil {
		return types.StringValue("")
	}
	return types.StringValue(t.UTC().Format(time.RFC3339))
}

func (d *sourceDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m sourceDSModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var s *client.Source
	var err error
	if id := m.ID.ValueString(); id != "" {
		s, err = d.client.GetSource(ctx, id)
	} else {
		s, err = d.client.FindSource(ctx, m.Project.ValueString(), m.Name.ValueString())
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot read source", err, nil)
		return
	}
	fields, diags := types.SetValueFrom(ctx, types.StringType, append([]string{}, s.SecretFieldsSet...))
	resp.Diagnostics.Append(diags...)
	project := m.Project
	if project.IsNull() || project.IsUnknown() {
		project = types.StringValue(s.ProjectID)
	}
	m = sourceDSModel{ID: types.StringValue(s.ID), Project: project, Name: types.StringValue(s.Name), ProjectID: types.StringValue(s.ProjectID),
		DisplayName: types.StringValue(s.DisplayName), Provider: types.StringValue(s.Provider), Layer: types.StringValue(s.Layer),
		System: types.BoolValue(s.System), Enabled: types.BoolValue(s.Enabled),
		Config: jsontypes.NewNormalizedValue(string(nonEmptyJSON(s.Config))), Policy: jsontypes.NewNormalizedValue(string(nonEmptyJSON(s.Policy))),
		ScheduleSeconds: types.Int64Value(s.ScheduleSeconds), ExpectedIntervalSeconds: types.Int64Value(s.ExpectedIntervalSeconds),
		SecretFieldsSet: fields, Pull: types.BoolValue(s.Pull), Push: types.BoolValue(s.Push), Running: types.BoolValue(s.Running),
		Freshness: types.StringValue(s.Freshness), LastGeneration: types.Int64Value(s.LastGeneration), LastRunAt: rfc3339(s.LastRunAt),
		LastSuccessAt: rfc3339(s.LastSuccessAt), LastHeartbeatAt: rfc3339(s.LastHeartbeatAt), LastError: types.StringValue(s.LastError),
		NextRunAt: rfc3339(s.NextRunAt)}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
