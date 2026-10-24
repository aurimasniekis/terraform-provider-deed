package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/aurimasniekis/terraform-provider-deed/internal/client"
)

var (
	_ resource.ResourceWithImportState    = (*relationshipResource)(nil)
	_ resource.ResourceWithConfigure      = (*relationshipResource)(nil)
	_ resource.ResourceWithValidateConfig = (*relationshipResource)(nil)
	_ resource.ResourceWithUpgradeState   = (*relationshipResource)(nil)
)

type relationshipResource struct{ baseResource }

// NewRelationshipResource returns the deed_relationship resource.
func NewRelationshipResource() resource.Resource { return &relationshipResource{} }

type endpointModel struct {
	ID      types.String `tfsdk:"id"`
	Project types.String `tfsdk:"project"`
	Kind    types.String `tfsdk:"kind"`
	Name    types.String `tfsdk:"name"`
}

type relationshipModel struct {
	ID         types.String         `tfsdk:"id"`
	Project    types.String         `tfsdk:"project"`
	ProjectID  types.String         `tfsdk:"project_id"`
	Type       types.String         `tfsdk:"type"`
	From       *endpointModel       `tfsdk:"from"`
	To         *endpointModel       `tfsdk:"to"`
	Attributes jsontypes.Normalized `tfsdk:"attributes"`
}

func (r *relationshipResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_relationship"
}

func endpointAttribute(desc string) schema.SingleNestedAttribute {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	return schema.SingleNestedAttribute{
		MarkdownDescription: desc + " Set `id`, or `kind` and `name` (plus `project` for an entity in another project). Changing it " +
			"forces a new resource.",
		Required:      true,
		PlanModifiers: []planmodifier.Object{objectplanmodifier.RequiresReplace()},
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{MarkdownDescription: "Entity ID (`ent_…`), e.g. `deed_entity.x.id`.", Optional: true, Computed: true,
				PlanModifiers: keep},
			"project": schema.StringAttribute{MarkdownDescription: "Project name or ID of the entity when it is addressed by kind and name " +
				"in another project.", Optional: true},
			"kind": schema.StringAttribute{MarkdownDescription: "Entity kind.", Optional: true, Computed: true, PlanModifiers: keep,
				Validators: []validator.String{stringvalidator.RegexMatches(kindNameRE, "must be a kind name")}},
			"name": schema.StringAttribute{MarkdownDescription: "Entity name.", Optional: true, Computed: true, PlanModifiers: keep,
				Validators: []validator.String{stringvalidator.RegexMatches(entityNameRE, "must be an entity name")}},
		},
	}
}

func (r *relationshipResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Version: stateSchemaVersion,
		MarkdownDescription: "A relationship between two entities, **asserted through the provider's source** (`terraform` by default). " +
			"Several sources can assert the same relationship (for example an importer and Terraform); this resource manages only the " +
			"provider's assertion: it is treated as gone when that assertion is missing or no longer live, and destroying the resource " +
			"retracts only that assertion — the relationship stays while other sources still assert it.\n\n" +
			"Cross-project relationships need a bridge between the projects (or a `bridge` entity endpoint).",
		Attributes: map[string]schema.Attribute{
			"id": idAttribute("Relationship ID (`rel_…`)."),
			"project": schema.StringAttribute{
				MarkdownDescription: "Project (name or ID) of the declaring source. Defaults to the project of the `from` entity; required " +
					"when `from` is addressed by kind and name. Changing it forces a new resource (the same project by name or ID does not).",
				Optional: true, Computed: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), sameProjectReplace("project_id")},
				Validators:    []validator.String{stringvalidator.LengthBetween(1, 64)},
			},
			"project_id": computedString("ID (`prj_…`) of the relationship's project (the `from` entity's project)."),
			"type": requiredImmutableString("Relationship type, built-in (`runs_on`, `hosted_on`, `depends_on`, `owned_by`, …) or custom "+
				"(`deed_relationship_type`).", stringvalidator.RegexMatches(kindNameRE, "must be a relationship type name")),
			"from": endpointAttribute("Source endpoint."),
			"to":   endpointAttribute("Target endpoint."),
			"attributes": schema.StringAttribute{
				CustomType: jsontypes.NormalizedType{},
				MarkdownDescription: "Relationship attributes as a JSON object string (use `jsonencode`), validated against the type's " +
					"attributes schema. Compared semantically.",
				Optional:   true,
				Validators: []validator.String{jsonObjectValidator{}},
			},
		},
	}
}

func (r *relationshipResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m relationshipModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for name, ep := range map[string]*endpointModel{"from": m.From, "to": m.To} {
		if ep == nil || ep.ID.IsUnknown() || ep.Kind.IsUnknown() || ep.Name.IsUnknown() {
			continue
		}
		hasID := !ep.ID.IsNull()
		hasKey := !ep.Kind.IsNull() || !ep.Name.IsNull()
		switch {
		case hasID && hasKey:
			resp.Diagnostics.AddAttributeError(path.Root(name), "Ambiguous endpoint", "Set either id, or kind and name — not both.")
		case !hasID && (ep.Kind.IsNull() || ep.Name.IsNull()):
			resp.Diagnostics.AddAttributeError(path.Root(name), "Incomplete endpoint", "Set id, or both kind and name.")
		case hasID && !ep.Project.IsNull():
			resp.Diagnostics.AddAttributeError(path.Root(name).AtName("project"), "Unexpected project",
				"project only qualifies an endpoint addressed by kind and name.")
		}
	}
	if m.From != nil && m.From.ID.IsNull() && !m.From.Kind.IsNull() && m.Project.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("project"), "Missing project",
			"project is required when from is addressed by kind and name.")
	}
}

func relationshipFields(field string) (path.Path, bool) {
	head := field
	if i := strings.IndexAny(head, ".[/"); i >= 0 {
		head = head[:i]
	}
	switch head {
	case "from", "to", "attributes", "type", "project":
		return path.Root(head), true
	}
	return path.Empty(), false
}

func (e *endpointModel) ref() client.Ref {
	if e == nil {
		return client.Ref{}
	}
	return client.Ref{ID: e.ID.ValueString(), Project: e.Project.ValueString(), Kind: e.Kind.ValueString(), Name: e.Name.ValueString()}
}

func fillEndpoint(prior *endpointModel, ref client.EntityRef) *endpointModel {
	out := &endpointModel{ID: types.StringValue(ref.ID), Kind: types.StringValue(ref.Kind), Name: types.StringValue(ref.Name),
		Project: types.StringNull()}
	if prior != nil {
		out.Project = prior.Project
		if out.Project.IsUnknown() {
			out.Project = types.StringNull()
		}
	}
	return out
}

func (m *relationshipModel) fill(rel *client.Relation, a *client.Assertion) {
	m.ID = types.StringValue(rel.ID)
	if m.Project.IsNull() || m.Project.IsUnknown() {
		m.Project = types.StringValue(rel.ProjectID)
	}
	m.ProjectID = types.StringValue(rel.ProjectID)
	m.Type = types.StringValue(rel.Type)
	m.From = fillEndpoint(m.From, rel.From)
	m.To = fillEndpoint(m.To, rel.To)
	m.Attributes = jsonValue(a.Attributes, m.Attributes)
}

func (r *relationshipResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan relationshipModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project := ""
	if !plan.Project.IsUnknown() {
		project = plan.Project.ValueString()
	}
	rel, err := r.client.CreateRelation(ctx, client.RelationCreate{Project: project, Source: r.source, Type: plan.Type.ValueString(),
		From: plan.From.ref(), To: plan.To.ref(), Attributes: rawJSON(plan.Attributes)})
	if err != nil {
		if client.HasCode(err, client.CodeAlreadyExists) {
			resp.Diagnostics.AddError("Relationship is already asserted by source \""+r.source+"\"",
				fmt.Sprintf("Deed already records this %s relationship as asserted through the source %q (by another resource or "+
					"Terraform configuration). Import it by its relationship ID instead of creating it:\n\n  terraform import <address> "+
					"<rel_…>\n\n%s", plan.Type.ValueString(), r.source, err.Error()))
			return
		}
		apiError(&resp.Diagnostics, "Cannot declare relationship", err, relationshipFields)
		return
	}
	a := rel.LiveAssertion(r.source)
	if a == nil {
		resp.Diagnostics.AddError("Relationship assertion missing", "Deed created relationship "+rel.ID+" but does not report a live "+
			"assertion by source "+r.source+".")
		return
	}
	plan.fill(rel, a)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *relationshipResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state relationshipModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rel, err := r.client.GetRelation(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot read relationship", err, nil)
		return
	}
	a := rel.LiveAssertion(r.source)
	if a == nil {
		// Other sources may still assert it, but this source's assertion is gone: plan to assert it again.
		resp.State.RemoveResource(ctx)
		return
	}
	state.fill(rel, a)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *relationshipResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state relationshipModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rel, err := r.client.PutAssertion(ctx, state.ID.ValueString(), r.source, rawJSON(plan.Attributes))
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot update relationship", err, relationshipFields)
		return
	}
	a := rel.LiveAssertion(r.source)
	if a == nil {
		resp.Diagnostics.AddError("Relationship assertion missing", "Deed does not report a live assertion of "+rel.ID+" by source "+r.source+".")
		return
	}
	plan.fill(rel, a)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *relationshipResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state relationshipModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteAssertion(ctx, state.ID.ValueString(), r.source); err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "Cannot retract relationship", err, nil)
	}
}

func (r *relationshipResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	rel, err := r.client.GetRelation(ctx, req.ID)
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot import relationship "+req.ID, err, nil)
		return
	}
	if rel.LiveAssertion(r.source) == nil {
		resp.Diagnostics.AddError("Relationship is not asserted by source \""+r.source+"\"",
			fmt.Sprintf("Relationship %s exists, but the source %q does not assert it, so there is nothing to import. Remove the import "+
				"and apply the resource instead: creating it adds this source's assertion to the existing relationship.", req.ID, r.source))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), rel.ID)...)
}
