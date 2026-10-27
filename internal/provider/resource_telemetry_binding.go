package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/aurimasniekis/terraform-provider-deed/internal/client"
)

var (
	_ resource.ResourceWithImportState  = (*bindingResource)(nil)
	_ resource.ResourceWithConfigure    = (*bindingResource)(nil)
	_ resource.ResourceWithUpgradeState = (*bindingResource)(nil)
)

type bindingResource struct{ baseResource }

// NewTelemetryBindingResource returns the deed_telemetry_binding resource.
func NewTelemetryBindingResource() resource.Resource { return &bindingResource{} }

type bindingParamModel struct {
	Name     types.String `tfsdk:"name"`
	Type     types.String `tfsdk:"type"`
	From     types.String `tfsdk:"from"`
	Default  types.String `tfsdk:"default"`
	Pattern  types.String `tfsdk:"pattern"`
	Required types.Bool   `tfsdk:"required"`
}

type bindingThresholdsModel struct {
	Warning   types.Float64 `tfsdk:"warning"`
	Critical  types.Float64 `tfsdk:"critical"`
	Direction types.String  `tfsdk:"direction"`
}

type bindingModel struct {
	ID               types.String            `tfsdk:"id"`
	Name             types.String            `tfsdk:"name"`
	Title            types.String            `tfsdk:"title"`
	Integration      types.String            `tfsdk:"integration"`
	IntegrationID    types.String            `tfsdk:"integration_id"`
	Kinds            types.Set               `tfsdk:"kinds"`
	Query            types.String            `tfsdk:"query"`
	Params           []bindingParamModel     `tfsdk:"params"`
	Shape            types.String            `tfsdk:"shape"`
	Unit             types.String            `tfsdk:"unit"`
	Role             types.String            `tfsdk:"role"`
	Thresholds       *bindingThresholdsModel `tfsdk:"thresholds"`
	FreshnessSeconds types.Int64             `tfsdk:"freshness_seconds"`
	RelationshipType types.String            `tfsdk:"relationship_type"`
	Guard            types.String            `tfsdk:"guard"`
	WhenEmpty        types.Float64           `tfsdk:"when_empty"`
	OnMap            types.Bool              `tfsdk:"on_map"`
}

func (r *bindingResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_telemetry_binding"
}

func (r *bindingResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	// Optional and computed without a "use state" modifier: removing the argument returns to Deed's default.
	computedStr := func(desc string, v ...validator.String) schema.StringAttribute {
		return schema.StringAttribute{MarkdownDescription: desc, Optional: true, Computed: true, Validators: v}
	}
	resp.Schema = schema.Schema{
		Version: stateSchemaVersion,
		MarkdownDescription: "A Deed telemetry binding: a named, admin-authored query template that users run against entities of " +
			"the listed kinds, with typed parameters filled from entity fields (Deed's docs/TELEMETRY.md). Role `chart` draws it on " +
			"the entity page, `health` turns a scalar into health with thresholds, `edge_rate` measures traffic on a relationship " +
			"type for the map's traffic overlay, `link` opens the backend (Grafana). Installation-wide; requires an installation " +
			"`admin` token.",
		Attributes: map[string]schema.Attribute{
			"id": idAttribute("Binding ID."),
			"name": schema.StringAttribute{
				MarkdownDescription: "Unique name.", Required: true,
				Validators: []validator.String{stringvalidator.RegexMatches(projectNameRE, "must be a lowercase slug (a-z, 0-9, -) of at most 63 characters")},
			},
			"title": schema.StringAttribute{
				MarkdownDescription: "Title shown in the UI. Defaults to `name`.", Optional: true, Computed: true,
				Validators:    []validator.String{stringvalidator.LengthBetween(1, 128)},
				PlanModifiers: []planmodifier.String{defaultFromAttribute("name")},
			},
			"integration": schema.StringAttribute{
				MarkdownDescription: "Integration name or ID (for example `deed_integration.prometheus.name`).", Required: true,
				Validators: []validator.String{stringvalidator.LengthBetween(1, 64)},
			},
			"integration_id": computedString("Integration ID."),
			"kinds": schema.SetAttribute{
				MarkdownDescription: "Entity kinds the binding applies to. Omit for every kind.",
				ElementType:         types.StringType, Optional: true,
				Validators: []validator.Set{setvalidator.SizeAtMost(50)},
			},
			"query": schema.StringAttribute{
				MarkdownDescription: "Query template with `{{param}}` placeholders and the built-ins `{{window}}` and `{{step}}` " +
					"(PromQL, LogQL, a Grafana search, …). Parameters are typed and validated before substitution.",
				Optional:   true,
				Validators: []validator.String{stringvalidator.LengthAtMost(8192)},
			},
			"params": schema.ListNestedAttribute{
				MarkdownDescription: "Typed parameters (at most 16).", Optional: true,
				Validators: []validator.List{listvalidator.SizeAtMost(16)},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"name": schema.StringAttribute{MarkdownDescription: "Placeholder name.", Required: true},
					"type": schema.StringAttribute{
						MarkdownDescription: "`label`, `string`, `int`, `duration` or `subject`.", Required: true,
						Validators: []validator.String{stringvalidator.OneOf("label", "string", "int", "duration", "subject")},
					},
					"from": schema.StringAttribute{
						MarkdownDescription: "JSON pointer into the entity the value comes from, e.g. `/spec/hostname`, " +
							"`/metadata/name` or `/metadata/labels/instance`. Edge-rate bindings read either end of the relationship " +
							"with a `from:` or `to:` prefix (`to:/metadata/name`).", Optional: true,
					},
					"default":  schema.StringAttribute{MarkdownDescription: "Value when the field is empty.", Optional: true},
					"pattern":  schema.StringAttribute{MarkdownDescription: "Extra RE2 the value must match.", Optional: true},
					"required": schema.BoolAttribute{MarkdownDescription: "Skip the binding for entities without a value.", Optional: true},
				}},
			},
			"shape": computedStr("Result shape: `scalar` (default), `timeseries`, `table` or `link`.",
				stringvalidator.OneOf("scalar", "timeseries", "table", "link")),
			"unit": schema.StringAttribute{
				MarkdownDescription: "Display unit, e.g. `percent`, `bytes`, `req/s`.", Optional: true,
				Validators: []validator.String{stringvalidator.LengthAtMost(32)},
			},
			"role": computedStr("`chart` (default; `link` for link-shaped bindings), `health`, `edge_rate` or `link`.",
				stringvalidator.OneOf("chart", "health", "edge_rate", "link")),
			"thresholds": schema.SingleNestedAttribute{
				MarkdownDescription: "Warning and critical thresholds (required for `health`).", Optional: true,
				Attributes: map[string]schema.Attribute{
					"warning":  schema.Float64Attribute{MarkdownDescription: "Warning threshold.", Optional: true},
					"critical": schema.Float64Attribute{MarkdownDescription: "Critical threshold.", Optional: true},
					"direction": schema.StringAttribute{
						MarkdownDescription: "`above` (default: higher is worse) or `below`.", Optional: true,
						Validators: []validator.String{stringvalidator.OneOf("above", "below")},
					},
				},
			},
			"freshness_seconds": schema.Int64Attribute{
				MarkdownDescription: "How long a result counts as fresh (15–86400, default 300).", Optional: true, Computed: true,
				Validators: []validator.Int64{int64validator.Between(15, 86400)},
			},
			"relationship_type": schema.StringAttribute{
				MarkdownDescription: "Relationship type an `edge_rate` binding measures.", Optional: true,
				Validators: []validator.String{stringvalidator.LengthBetween(1, 64)},
			},
			"guard": schema.StringAttribute{
				MarkdownDescription: "Applicability query with the same placeholders: the binding applies to an entity only when it " +
					"returns data, otherwise the result is \"not applicable\" (no value, no health). For example " +
					"`up{job=\"node\",instance=\"{{instance}}\"}` for \"this host has node_exporter\". Requires Deed 0.4.0 or later.",
				Optional:   true,
				Validators: []validator.String{stringvalidator.LengthBetween(1, 8192)},
			},
			"when_empty": schema.Float64Attribute{
				MarkdownDescription: "Value of an empty result (after the guard passed) for `scalar` bindings, e.g. `0` for an error " +
					"rate whose counter has no series yet; replaces `or vector(0)`. Requires Deed 0.4.0 or later.",
				Optional: true,
			},
			"on_map": schema.BoolAttribute{
				MarkdownDescription: "Show the value under the entity's node card on the map (`scalar` bindings with role `chart` or " +
					"`health`; Deed draws up to six per entity). Default `false`. Requires Deed 0.4.0 or later.",
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
			},
		},
	}
}

var bindingFields = rootMapper(map[string]string{"name": "name", "title": "title", "integration": "integration", "kinds": "kinds",
	"query": "query", "params": "params", "shape": "shape", "unit": "unit", "role": "role", "thresholds": "thresholds",
	"freshness_seconds": "freshness_seconds", "relationship_type": "relationship_type", "guard": "guard",
	"when_empty": "when_empty", "on_map": "on_map"})

func optStr(s string, prior types.String) types.String { return strVal(s, prior) }

func (m *bindingModel) fill(b *client.Binding) {
	m.ID = types.StringValue(b.ID)
	m.Name = types.StringValue(b.Name)
	m.Title = types.StringValue(b.Title)
	m.IntegrationID = types.StringValue(b.IntegrationID)
	if m.Integration.IsNull() || m.Integration.IsUnknown() {
		m.Integration = types.StringValue(b.IntegrationID)
	}
	if len(b.Kinds) > 0 || !m.Kinds.IsNull() {
		m.Kinds, _ = types.SetValueFrom(context.Background(), types.StringType, append([]string{}, b.Kinds...))
	}
	m.Query = optStr(b.Query, m.Query)
	if len(b.Params) > 0 || m.Params != nil {
		params := make([]bindingParamModel, 0, len(b.Params))
		for i, p := range b.Params {
			var prior bindingParamModel
			if i < len(m.Params) {
				prior = m.Params[i]
			}
			req := types.BoolNull()
			if p.Required || (!prior.Required.IsNull() && !prior.Required.IsUnknown()) {
				req = types.BoolValue(p.Required)
			}
			params = append(params, bindingParamModel{Name: types.StringValue(p.Name), Type: types.StringValue(p.Type),
				From: optStr(p.From, prior.From), Default: optStr(p.Default, prior.Default), Pattern: optStr(p.Pattern, prior.Pattern), Required: req})
		}
		m.Params = params
	}
	m.Shape = types.StringValue(b.Shape)
	m.Unit = optStr(b.Unit, m.Unit)
	m.Role = types.StringValue(b.Role)
	t := b.Thresholds
	// Only the default direction (which Deed fills in) and no thresholds: unset unless configured.
	onlyDefault := t.Warning == nil && t.Critical == nil && (t.Direction == "" || t.Direction == "above")
	if m.Thresholds == nil && onlyDefault {
		m.Thresholds = nil
	} else if t.Warning != nil || t.Critical != nil || t.Direction != "" || m.Thresholds != nil {
		th := &bindingThresholdsModel{Warning: types.Float64Null(), Critical: types.Float64Null(), Direction: types.StringNull()}
		if t.Warning != nil {
			th.Warning = types.Float64Value(*t.Warning)
		}
		if t.Critical != nil {
			th.Critical = types.Float64Value(*t.Critical)
		}
		var prior types.String
		if m.Thresholds != nil {
			prior = m.Thresholds.Direction
		}
		// Deed fills in the default direction: keep it unset when the configuration did not set it.
		dir := t.Direction
		if dir == "above" && (prior.IsNull() || prior.IsUnknown()) {
			dir = ""
		}
		th.Direction = optStr(dir, prior)
		m.Thresholds = th
	}
	m.FreshnessSeconds = types.Int64Value(b.FreshnessSeconds)
	m.RelationshipType = optStr(b.RelationshipType, m.RelationshipType)
	m.Guard = optStr(b.Guard, m.Guard)
	m.WhenEmpty = types.Float64Null()
	if b.WhenEmpty != nil {
		m.WhenEmpty = types.Float64Value(*b.WhenEmpty)
	}
	m.OnMap = types.BoolValue(b.OnMap)
}

// input is the full desired state: empty slices and "" clear on update (the API keeps omitted fields). The Deed 0.4.0
// fields (guard, when_empty, on_map) are sent only when set, or to clear what prior (the state on update) has set.
func (m *bindingModel) input(ctx context.Context, prior *bindingModel) client.BindingInput {
	unit, rel := m.Unit.ValueString(), m.RelationshipType.ValueString()
	in := client.BindingInput{Name: m.Name.ValueString(), Title: m.Title.ValueString(), Integration: m.Integration.ValueString(),
		Kinds: []string{}, Query: m.Query.ValueString(), Params: []client.BindingParam{}, Unit: &unit, RelationshipType: &rel,
		Thresholds: &client.BindingThresholds{}}
	if !m.Kinds.IsNull() && !m.Kinds.IsUnknown() {
		_ = m.Kinds.ElementsAs(ctx, &in.Kinds, false)
	}
	for _, p := range m.Params {
		in.Params = append(in.Params, client.BindingParam{Name: p.Name.ValueString(), Type: p.Type.ValueString(), From: p.From.ValueString(),
			Default: p.Default.ValueString(), Pattern: p.Pattern.ValueString(), Required: p.Required.ValueBool()})
	}
	// Deed's defaults, sent explicitly: on update the API keeps whatever an omitted field had.
	in.Shape = "scalar"
	if !m.Shape.IsNull() && !m.Shape.IsUnknown() {
		in.Shape = m.Shape.ValueString()
	}
	in.Role = "chart"
	if in.Shape == "link" {
		in.Role = "link"
	}
	if !m.Role.IsNull() && !m.Role.IsUnknown() {
		in.Role = m.Role.ValueString()
	}
	in.FreshnessSeconds = 300
	if t := m.Thresholds; t != nil {
		if !t.Warning.IsNull() && !t.Warning.IsUnknown() {
			v := t.Warning.ValueFloat64()
			in.Thresholds.Warning = &v
		}
		if !t.Critical.IsNull() && !t.Critical.IsUnknown() {
			v := t.Critical.ValueFloat64()
			in.Thresholds.Critical = &v
		}
		in.Thresholds.Direction = t.Direction.ValueString()
	}
	if !m.FreshnessSeconds.IsNull() && !m.FreshnessSeconds.IsUnknown() {
		in.FreshnessSeconds = m.FreshnessSeconds.ValueInt64()
	}
	if prior == nil {
		prior = &bindingModel{}
	}
	if guard := m.Guard.ValueString(); guard != "" || prior.Guard.ValueString() != "" {
		in.Guard = &guard
	}
	if !m.WhenEmpty.IsNull() && !m.WhenEmpty.IsUnknown() {
		v := m.WhenEmpty.ValueFloat64()
		in.WhenEmpty = &client.WhenEmpty{Value: &v}
	} else if !prior.WhenEmpty.IsNull() && !prior.WhenEmpty.IsUnknown() {
		in.WhenEmpty = &client.WhenEmpty{}
	}
	if onMap := m.OnMap.ValueBool(); onMap || prior.OnMap.ValueBool() {
		in.OnMap = &onMap
	}
	return in
}

func (r *bindingResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan bindingModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	b, err := r.client.CreateBinding(ctx, plan.input(ctx, nil))
	if err != nil {
		if client.HasCode(err, client.CodeAlreadyExists) {
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Binding already exists",
				"A binding named "+plan.Name.ValueString()+" already exists. Import it instead of creating it:\n\n  terraform import <address> "+
					plan.Name.ValueString())
			return
		}
		apiError(&resp.Diagnostics, "Cannot create telemetry binding", err, bindingFields)
		return
	}
	plan.fill(b)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(saveETag(ctx, resp.Private, b.ETag)...)
}

func (r *bindingResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state bindingModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	b, err := r.client.GetBinding(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot read telemetry binding", err, nil)
		return
	}
	state.fill(b)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(saveETag(ctx, resp.Private, b.ETag)...)
}

func (r *bindingResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state bindingModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	b, err := r.client.UpdateBinding(ctx, state.ID.ValueString(), loadETag(ctx, req.Private), plan.input(ctx, &state))
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot update telemetry binding", err, bindingFields)
		return
	}
	plan.fill(b)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(saveETag(ctx, resp.Private, b.ETag)...)
}

func (r *bindingResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state bindingModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteBinding(ctx, state.ID.ValueString()); err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "Cannot delete telemetry binding", err, nil)
	}
}

func (r *bindingResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	b, err := r.client.GetBinding(ctx, req.ID)
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot import telemetry binding", err, nil)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), b.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("integration"), b.IntegrationID)...)
}
