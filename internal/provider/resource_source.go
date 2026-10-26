package provider

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/aurimasniekis/terraform-provider-deed/internal/client"
)

var (
	_ resource.ResourceWithImportState  = (*sourceResource)(nil)
	_ resource.ResourceWithConfigure    = (*sourceResource)(nil)
	_ resource.ResourceWithUpgradeState = (*sourceResource)(nil)
)

type sourceResource struct{ baseResource }

// NewSourceResource returns the deed_source resource.
func NewSourceResource() resource.Resource { return &sourceResource{} }

type sourceModel struct {
	ID                      types.String         `tfsdk:"id"`
	Project                 types.String         `tfsdk:"project"`
	ProjectID               types.String         `tfsdk:"project_id"`
	Name                    types.String         `tfsdk:"name"`
	DisplayName             types.String         `tfsdk:"display_name"`
	Provider                types.String         `tfsdk:"provider_type"`
	Layer                   types.String         `tfsdk:"layer"`
	Enabled                 types.Bool           `tfsdk:"enabled"`
	Config                  jsontypes.Normalized `tfsdk:"config"`
	Policy                  jsontypes.Normalized `tfsdk:"policy"`
	ScheduleSeconds         types.Int64          `tfsdk:"schedule_seconds"`
	ExpectedIntervalSeconds types.Int64          `tfsdk:"expected_interval_seconds"`
	SecretsWO               types.Map            `tfsdk:"secrets_wo"`
	SecretsWOVersion        types.Int64          `tfsdk:"secrets_wo_version"`
	SecretFieldsSet         types.Set            `tfsdk:"secret_fields_set"`
	Pull                    types.Bool           `tfsdk:"pull"`
	Push                    types.Bool           `tfsdk:"push"`
}

func (r *sourceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_source"
}

func (r *sourceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Version: stateSchemaVersion,
		MarkdownDescription: "A Deed source: a connector Deed pulls from on a schedule (`github`, `hallpass`, `porch`, `terraform_state`, " +
			"`pulse_admin`, `pulse_tail`, …) or a push source that external programs feed through source tokens. Requires the project " +
			"`admin` role (or an installation admin token). The provider's configuration schema, secret fields and defaults are listed " +
			"by `GET /api/v1/providers` and on the source page in the UI.\n\n" +
			"Credentials go in `secrets_wo`, a **write-only** argument (Terraform 1.11+): Terraform sends it to Deed but never stores it " +
			"in state or plan, and Deed never returns it. Change `secrets_wo_version` to send new values (rotation).\n\n" +
			"~> Destroying a source deletes every fact it contributed: entities only it declared are tombstoned and its tokens are revoked.",
		Attributes: map[string]schema.Attribute{
			"id": idAttribute("Source ID (`src_…`)."),
			"project": schema.StringAttribute{
				MarkdownDescription: "Project name or ID. Changing it forces a new resource (the same project by name or ID does not).",
				Required:            true,
				PlanModifiers:       []planmodifier.String{sameProjectReplace("project_id")},
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 64)},
			},
			"project_id": computedString("Project ID (`prj_…`)."),
			"name": requiredImmutableString("Source name, unique in the project.",
				stringvalidator.RegexMatches(projectNameRE, "must be a lowercase slug (a-z, 0-9, -) of at most 63 characters")),
			"display_name": schema.StringAttribute{
				MarkdownDescription: "Human-friendly name. Defaults to `name`.",
				Optional:            true, Computed: true,
				Validators:    []validator.String{stringvalidator.LengthBetween(1, 128)},
				PlanModifiers: []planmodifier.String{defaultFromAttribute("name")},
			},
			"provider_type": requiredImmutableString("Source provider: `github`, `hallpass`, `porch`, `terraform_state`, `pulse_admin`, "+
				"`pulse_tail`, `push`, … (`GET /api/v1/providers`).", stringvalidator.LengthBetween(1, 64)),
			"layer": schema.StringAttribute{
				MarkdownDescription: "Fact layer the source writes: `declared`, `imported`, `discovered` or `observation`. Defaults to the " +
					"provider's layer. Changing it forces a new resource.",
				Optional: true, Computed: true,
				Validators:    []validator.String{stringvalidator.OneOf("declared", "imported", "discovered", "observation")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
			},
			"enabled": schema.BoolAttribute{
				MarkdownDescription: "Whether the source runs and accepts data (default `true`).",
				Optional:            true, Computed: true, Default: booldefault.StaticBool(true),
			},
			"config": schema.StringAttribute{
				CustomType: jsontypes.NormalizedType{},
				MarkdownDescription: "Provider configuration as a JSON object string (use `jsonencode`), validated by Deed against the " +
					"provider's schema. Secret fields do not belong here: put them in `secrets_wo`.",
				Optional:   true,
				Validators: []validator.String{jsonObjectValidator{}},
			},
			"policy": schema.StringAttribute{
				CustomType: jsontypes.NormalizedType{},
				MarkdownDescription: "Reconciliation policy as a JSON object string: `priority`, `field_priority`, `on_absent` " +
					"(`tombstone`, `orphan` or `keep`), `max_removal_percent`, `allowed_kinds`, `default_ttl_seconds`. Defaults to the " +
					"provider's policy.",
				Optional: true, Computed: true,
				Validators:    []validator.String{jsonObjectValidator{}},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"schedule_seconds": schema.Int64Attribute{
				MarkdownDescription: "Pull interval in seconds (`0` = only on demand). Defaults to the provider's interval; always `0` for " +
					"push-only providers.",
				Optional: true, Computed: true,
				Validators:    []validator.Int64{int64validator.Between(0, 7*24*3600)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"expected_interval_seconds": schema.Int64Attribute{
				MarkdownDescription: "How often data is expected (drives freshness for push sources). Defaults to Deed's choice.",
				Optional:            true, Computed: true,
				Validators:    []validator.Int64{int64validator.Between(0, 30*24*3600)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"secrets_wo": schema.MapAttribute{
				MarkdownDescription: "Write-only credentials keyed by the provider's secret fields (for example `{ token = var.github_token }`). " +
					"Sent on create and whenever `secrets_wo_version` changes; never stored in state. When the version changes and this is " +
					"unset, the stored credentials are cleared.",
				ElementType: types.StringType,
				Optional:    true, Sensitive: true, WriteOnly: true,
			},
			"secrets_wo_version": schema.Int64Attribute{
				MarkdownDescription: "Change this (for example increment it, or use a 1Password item version) to send `secrets_wo` again.",
				Optional:            true,
			},
			"secret_fields_set": schema.SetAttribute{
				MarkdownDescription: "Secret fields that have a stored value in Deed (the values are never returned).",
				ElementType:         types.StringType, Computed: true,
			},
			"pull": schema.BoolAttribute{
				MarkdownDescription: "Whether Deed pulls from this source on a schedule.", Computed: true,
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"push": schema.BoolAttribute{
				MarkdownDescription: "Whether external programs push to this source with source tokens.", Computed: true,
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

var sourceFields = rootMapper(map[string]string{"name": "name", "display_name": "display_name", "provider": "provider_type", "layer": "layer",
	"config": "config", "policy": "policy", "secrets": "secrets_wo", "schedule_seconds": "schedule_seconds", "project": "project",
	"expected_interval_seconds": "expected_interval_seconds"})

// policyShape mirrors Deed's source policy, so a configured policy that differs only in zero values or key order from
// what Deed returns (it omits zero fields) is kept as configured instead of producing an inconsistent result.
type policyShape struct {
	Priority          *int           `json:"priority"`
	FieldPriority     map[string]int `json:"field_priority"`
	OnAbsent          string         `json:"on_absent"`
	MaxRemovalPercent float64        `json:"max_removal_percent"`
	AllowedKinds      []string       `json:"allowed_kinds"`
	DefaultTTLSeconds int            `json:"default_ttl_seconds"`
}

func samePolicy(a, b []byte) bool {
	var pa, pb policyShape
	if json.Unmarshal(a, &pa) != nil || json.Unmarshal(b, &pb) != nil {
		return false
	}
	for _, p := range []*policyShape{&pa, &pb} {
		if len(p.FieldPriority) == 0 {
			p.FieldPriority = nil
		}
		if len(p.AllowedKinds) == 0 {
			p.AllowedKinds = nil
		}
	}
	return reflect.DeepEqual(pa, pb)
}

func (m *sourceModel) fill(s *client.Source) {
	if m.Project.IsNull() || m.Project.IsUnknown() {
		m.Project = types.StringValue(s.ProjectID)
	}
	m.ID = types.StringValue(s.ID)
	m.ProjectID = types.StringValue(s.ProjectID)
	m.Name = types.StringValue(s.Name)
	m.DisplayName = types.StringValue(s.DisplayName)
	m.Provider = types.StringValue(s.Provider)
	m.Layer = types.StringValue(s.Layer)
	m.Enabled = types.BoolValue(s.Enabled)
	m.Config = jsonValue(s.Config, m.Config)
	if m.Policy.IsNull() || m.Policy.IsUnknown() || !samePolicy([]byte(m.Policy.ValueString()), s.Policy) {
		m.Policy = jsontypes.NewNormalizedValue(string(nonEmptyJSON(s.Policy)))
	}
	m.ScheduleSeconds = types.Int64Value(s.ScheduleSeconds)
	m.ExpectedIntervalSeconds = types.Int64Value(s.ExpectedIntervalSeconds)
	fields := make([]string, 0, len(s.SecretFieldsSet))
	fields = append(fields, s.SecretFieldsSet...)
	m.SecretFieldsSet, _ = types.SetValueFrom(context.Background(), types.StringType, fields)
	m.Pull = types.BoolValue(s.Pull)
	m.Push = types.BoolValue(s.Push)
	m.SecretsWO = types.MapNull(types.StringType)
}

// nonEmptyJSON returns raw with sorted keys (as jsonencode writes them), or {} for an empty document.
func nonEmptyJSON(raw json.RawMessage) json.RawMessage {
	if t := strings.TrimSpace(string(raw)); t == "" || t == "null" {
		return json.RawMessage(`{}`)
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	b, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return b
}

// configSecrets reads the write-only secrets from the configuration (they are never in the plan or state).
func configSecrets(ctx context.Context, cfg tfsdk.Config) (map[string]string, bool, diag.Diagnostics) {
	var v types.Map
	diags := cfg.GetAttribute(ctx, path.Root("secrets_wo"), &v)
	if diags.HasError() || v.IsNull() || v.IsUnknown() {
		return nil, false, diags
	}
	out := map[string]string{}
	diags.Append(v.ElementsAs(ctx, &out, false)...)
	return out, true, diags
}

func (m *sourceModel) input() client.SourceInput {
	in := client.SourceInput{Project: m.Project.ValueString(), Name: m.Name.ValueString(), DisplayName: m.DisplayName.ValueString(),
		Provider: m.Provider.ValueString(), Config: rawJSON(m.Config)}
	if !m.Layer.IsNull() && !m.Layer.IsUnknown() {
		in.Layer = m.Layer.ValueString()
	}
	if !m.Enabled.IsNull() && !m.Enabled.IsUnknown() {
		b := m.Enabled.ValueBool()
		in.Enabled = &b
	}
	if !m.Policy.IsNull() && !m.Policy.IsUnknown() {
		in.Policy = rawJSON(m.Policy)
	}
	if !m.ScheduleSeconds.IsNull() && !m.ScheduleSeconds.IsUnknown() {
		v := m.ScheduleSeconds.ValueInt64()
		in.ScheduleSeconds = &v
	}
	if !m.ExpectedIntervalSeconds.IsNull() && !m.ExpectedIntervalSeconds.IsUnknown() {
		in.ExpectedIntervalSeconds = m.ExpectedIntervalSeconds.ValueInt64()
	}
	return in
}

func (r *sourceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan sourceModel
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
	s, err := r.client.CreateSource(ctx, in)
	if err != nil {
		if client.HasCode(err, client.CodeAlreadyExists) {
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Source already exists",
				"Project "+plan.Project.ValueString()+" already has a source named "+plan.Name.ValueString()+". Import it instead of creating it:\n\n"+
					"  terraform import <address> "+plan.Project.ValueString()+"/"+plan.Name.ValueString())
			return
		}
		apiError(&resp.Diagnostics, "Cannot create source", err, sourceFields)
		return
	}
	plan.fill(s)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(saveETag(ctx, resp.Private, s.ETag)...)
}

func (r *sourceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state sourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	s, err := r.client.GetSource(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot read source", err, nil)
		return
	}
	state.fill(s)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(saveETag(ctx, resp.Private, s.ETag)...)
}

func (r *sourceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state sourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in := plan.input()
	in.Project, in.Name, in.Provider, in.Layer = "", "", "", ""
	if in.Config == nil && !state.Config.IsNull() {
		in.Config = json.RawMessage(`{}`) // the argument was removed: clear the configuration
	}
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
	s, err := r.client.UpdateSource(ctx, state.ID.ValueString(), loadETag(ctx, req.Private), in)
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot update source", err, sourceFields)
		return
	}
	plan.fill(s)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(saveETag(ctx, resp.Private, s.ETag)...)
}

func (r *sourceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state sourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteSource(ctx, state.ID.ValueString()); err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "Cannot delete source", err, nil)
	}
}

func (r *sourceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Accept a source ID or <project>/<name>; Read fills the rest.
	var s *client.Source
	var err error
	if strings.Contains(req.ID, "/") {
		parts, perr := splitID(req.ID, 2, "<project>/<name>")
		if perr != nil {
			resp.Diagnostics.AddError("Invalid import ID", perr.Error())
			return
		}
		s, err = r.client.FindSource(ctx, parts[0], parts[1])
		if err == nil {
			resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project"), parts[0])...)
		}
	} else {
		s, err = r.client.GetSource(ctx, req.ID)
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot import source", err, nil)
		return
	}
	if s.System {
		resp.Diagnostics.AddError("Cannot import a system source",
			"Source "+s.Name+" is created by Deed for every project (api, terraform, ui, overrides) and cannot be managed by Terraform.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), s.ID)...)
}
