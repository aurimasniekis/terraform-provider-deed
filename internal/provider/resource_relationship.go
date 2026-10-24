package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

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
		PlanModifiers: []planmodifier.Object{endpointReplace()},
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

// endpointReplace forces replacement when an endpoint changes, except when the only difference is an endpoint
// project that the state does not record (after import): the endpoint is still the same entity.
func endpointReplace() planmodifier.Object {
	desc := "Changing the endpoint forces a new resource."
	return objectplanmodifier.RequiresReplaceIf(func(ctx context.Context, req planmodifier.ObjectRequest, resp *objectplanmodifier.RequiresReplaceIfFuncResponse) {
		var plan, state endpointModel
		resp.Diagnostics.Append(req.PlanValue.As(ctx, &plan, basetypes.ObjectAsOptions{UnhandledUnknownAsEmpty: false})...)
		resp.Diagnostics.Append(req.StateValue.As(ctx, &state, basetypes.ObjectAsOptions{})...)
		if resp.Diagnostics.HasError() {
			resp.RequiresReplace = true
			return
		}
		same := plan.ID.Equal(state.ID) && plan.Kind.Equal(state.Kind) && plan.Name.Equal(state.Name) &&
			(state.Project.IsNull() || plan.Project.Equal(state.Project))
		resp.RequiresReplace = !same
	}, desc, desc)
}

const sourceIDKey = "source_id"

// assertionFor returns this resource's live assertion: matched by the source ID recorded at creation when known (two
// projects can each have a source with the same name), otherwise by source name.
func assertionFor(ctx context.Context, rel *client.Relation, source string, p privateState) *client.Assertion {
	id := ""
	if p != nil {
		if b, d := p.GetKey(ctx, sourceIDKey); !d.HasError() && len(b) > 0 {
			_ = json.Unmarshal(b, &id)
		}
	}
	for i := range rel.Assertions {
		a := &rel.Assertions[i]
		if a.Live && a.Source == source && (id == "" || a.SourceID == id) {
			return a
		}
	}
	return nil
}

func saveSourceID(ctx context.Context, p privateState, a *client.Assertion) diag.Diagnostics {
	if p == nil || a == nil || a.SourceID == "" {
		return nil
	}
	b, _ := json.Marshal(a.SourceID)
	return p.SetKey(ctx, sourceIDKey, b)
}

// checkSourceProject refuses a declaring project other than the from entity's: Deed records the assertion through
// the source of the given project, but reads and retracts assertions through the source of the relationship's
// (from entity's) project, so such an assertion could never be updated or removed by this resource.
func (r *relationshipResource) checkSourceProject(ctx context.Context, plan *relationshipModel) error {
	if plan.Project.IsNull() || plan.Project.IsUnknown() || plan.Project.ValueString() == "" || plan.From == nil {
		return nil
	}
	src, err := r.client.GetProject(ctx, plan.Project.ValueString())
	if err != nil {
		return err
	}
	fromProject := src.ID
	switch {
	case plan.From.ID.ValueString() != "":
		e, err := r.client.GetEntity(ctx, plan.From.ID.ValueString())
		if err != nil {
			return err
		}
		fromProject = e.Metadata.ProjectID
	case plan.From.Project.ValueString() != "":
		fp, err := r.client.GetProject(ctx, plan.From.Project.ValueString())
		if err != nil {
			return err
		}
		fromProject = fp.ID
	}
	if fromProject != src.ID {
		return fmt.Errorf("project %q is not the project of the from entity; declare the relationship from the from entity's project "+
			"(omit project, or swap from and to): Deed updates and retracts assertions through the source of the from entity's project",
			plan.Project.ValueString())
	}
	return nil
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
				MarkdownDescription: "Project (name or ID) of the declaring source; it must be the project of the `from` entity (Deed " +
					"updates and retracts assertions through that project's source). Defaults to it (recorded as the project name); required " +
					"when `from` is addressed by kind and name. Changing it forces a new resource (the same project by name or ID does not). " +
					"Import records the project name.",
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
	var fromObj, toObj types.Object
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("from"), &fromObj)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("to"), &toObj)...)
	if resp.Diagnostics.HasError() || fromObj.IsUnknown() || toObj.IsUnknown() {
		return
	}
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
	if err := r.checkSourceProject(ctx, &plan); err != nil {
		if _, ok := client.AsAPIError(err); ok {
			apiError(&resp.Diagnostics, "Cannot declare relationship", err, relationshipFields)
		} else {
			resp.Diagnostics.AddAttributeError(path.Root("project"), "Unsupported declaring project", err.Error())
		}
		return
	}
	project := ""
	projectComputed := plan.Project.IsUnknown() || plan.Project.IsNull()
	if !projectComputed {
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
	if projectComputed {
		// Record the project name, the documented form (and what import records).
		if pr, err := r.client.GetProject(ctx, rel.ProjectID); err == nil {
			plan.Project = types.StringValue(pr.Name)
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(saveSourceID(ctx, resp.Private, a)...)
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
	a := assertionFor(ctx, rel, r.source, req.Private)
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
	a := assertionFor(ctx, rel, r.source, req.Private)
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
	a := rel.LiveAssertion(r.source)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), rel.ID)...)
	// Record the project name (the documented form of `project`), so a configuration naming it plans no replacement.
	if pr, err := r.client.GetProject(ctx, rel.ProjectID); err == nil {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project"), pr.Name)...)
	}
	resp.Diagnostics.Append(saveSourceID(ctx, resp.Private, a)...)
}
