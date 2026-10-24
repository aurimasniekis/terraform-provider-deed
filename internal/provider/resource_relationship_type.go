package provider

import (
	"context"
	"fmt"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/aurimasniekis/terraform-provider-deed/internal/client"
)

var (
	_ resource.ResourceWithImportState  = (*relTypeResource)(nil)
	_ resource.ResourceWithConfigure    = (*relTypeResource)(nil)
	_ resource.ResourceWithUpgradeState = (*relTypeResource)(nil)
)

type relTypeResource struct{ baseResource }

// NewRelationshipTypeResource returns the deed_relationship_type resource.
func NewRelationshipTypeResource() resource.Resource { return &relTypeResource{} }

type relTypeModel struct {
	ID               types.String         `tfsdk:"id"`
	Name             types.String         `tfsdk:"name"`
	InverseName      types.String         `tfsdk:"inverse_name"`
	Title            types.String         `tfsdk:"title"`
	Description      types.String         `tfsdk:"description"`
	Directed         types.Bool           `tfsdk:"directed"`
	FromKinds        []string             `tfsdk:"from_kinds"`
	ToKinds          []string             `tfsdk:"to_kinds"`
	DefaultEvidence  types.String         `tfsdk:"default_evidence"`
	Cardinality      types.String         `tfsdk:"cardinality"`
	AttributesSchema jsontypes.Normalized `tfsdk:"attributes_schema"`
	Impact           types.String         `tfsdk:"impact"`
	Color            types.String         `tfsdk:"color"`
}

func (r *relTypeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_relationship_type"
}

var relNameRE = kindNameRE

func emptyStringSet() types.Set { return types.SetValueMust(types.StringType, []attr.Value{}) }

func (r *relTypeResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Version: stateSchemaVersion,
		MarkdownDescription: "A custom Deed relationship type (built-in types such as `runs_on`, `depends_on` or `owned_by` are " +
			"owned by Deed and cannot be managed). Requires an installation `admin` (or owner) token. Endpoint kind constraints can " +
			"only be narrowed while no live relationship would violate them.",
		Attributes: map[string]schema.Attribute{
			"id": idAttribute("The relationship type name."),
			"name": requiredImmutableString("Type name: a lowercase identifier, optionally prefixed by a domain (`example.com/backs_up`).",
				stringvalidator.RegexMatches(relNameRE, "must be a lowercase identifier, optionally prefixed by a domain")),
			"inverse_name": schema.StringAttribute{
				MarkdownDescription: "Name of the inverse direction shown on the target, e.g. `backed_up_by`.", Optional: true,
				Validators: []validator.String{stringvalidator.RegexMatches(relNameRE, "must be a lowercase identifier")},
			},
			"title": schema.StringAttribute{
				MarkdownDescription: "Display name, e.g. `Backs up`.", Required: true,
				Validators: []validator.String{stringvalidator.LengthBetween(1, 100)},
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Description.", Optional: true,
				Validators: []validator.String{stringvalidator.LengthAtMost(2000)},
			},
			"directed": schema.BoolAttribute{
				MarkdownDescription: "Whether the relationship has a direction (default `true`).",
				Optional:            true, Computed: true, Default: booldefault.StaticBool(true),
			},
			"from_kinds": schema.SetAttribute{
				MarkdownDescription: "Kinds allowed as the source endpoint (empty = any).", ElementType: types.StringType,
				Optional: true, Computed: true, Default: setdefault.StaticValue(emptyStringSet()),
				Validators: []validator.Set{setvalidator.ValueStringsAre(stringvalidator.RegexMatches(kindNameRE, "must be a kind name"))},
			},
			"to_kinds": schema.SetAttribute{
				MarkdownDescription: "Kinds allowed as the target endpoint (empty = any).", ElementType: types.StringType,
				Optional: true, Computed: true, Default: setdefault.StaticValue(emptyStringSet()),
				Validators: []validator.Set{setvalidator.ValueStringsAre(stringvalidator.RegexMatches(kindNameRE, "must be a kind name"))},
			},
			"default_evidence": schema.StringAttribute{
				MarkdownDescription: "Evidence class of relationships reported by importers: `structural` (default) or `observed`.",
				Optional:            true, Computed: true, Default: stringdefault.StaticString("structural"),
				Validators: []validator.String{stringvalidator.OneOf("structural", "observed")},
			},
			"cardinality": schema.StringAttribute{
				MarkdownDescription: "`many` (default) or `one` target per source entity.",
				Optional:            true, Computed: true, Default: stringdefault.StaticString("many"),
				Validators: []validator.String{stringvalidator.OneOf("one", "many")},
			},
			"attributes_schema": schema.StringAttribute{
				CustomType: jsontypes.NormalizedType{},
				MarkdownDescription: "Optional JSON Schema (2020-12) for relationship attributes, as a JSON string (use `jsonencode`). " +
					"Compared semantically.",
				Optional:   true,
				Validators: []validator.String{jsonObjectValidator{}},
			},
			"impact": schema.StringAttribute{
				MarkdownDescription: "Impact propagation in the map: `forward` (default), `reverse`, `both` or `none`.",
				Optional:            true, Computed: true, Default: stringdefault.StaticString("forward"),
				Validators: []validator.String{stringvalidator.OneOf("forward", "reverse", "both", "none")},
			},
			"color": schema.StringAttribute{
				MarkdownDescription: "Edge color as `#rrggbb`.", Optional: true,
				Validators: []validator.String{stringvalidator.RegexMatches(colorRE, "must be #rrggbb")},
			},
		},
	}
}

var relTypeFields = rootMapper(map[string]string{"name": "name", "inverse_name": "inverse_name", "title": "title",
	"description": "description", "default_evidence": "default_evidence", "cardinality": "cardinality",
	"attributes_schema": "attributes_schema", "impact": "impact", "color": "color", "from_kinds": "from_kinds", "to_kinds": "to_kinds"})

func sortedCopy(ss []string) []string {
	out := append([]string{}, ss...)
	slices.Sort(out)
	return out
}

func (m *relTypeModel) fill(rt *client.RelType) {
	m.ID = types.StringValue(rt.Name)
	m.Name = types.StringValue(rt.Name)
	m.InverseName = strVal(rt.InverseName, m.InverseName)
	m.Title = types.StringValue(rt.Title)
	m.Description = strVal(rt.Description, m.Description)
	m.Directed = types.BoolValue(rt.Directed)
	m.FromKinds = sortedCopy(rt.FromKinds)
	m.ToKinds = sortedCopy(rt.ToKinds)
	m.DefaultEvidence = types.StringValue(rt.DefaultEvidence)
	m.Cardinality = types.StringValue(rt.Cardinality)
	m.AttributesSchema = jsonValue(rt.AttributesSchema, m.AttributesSchema)
	m.Impact = types.StringValue(rt.Impact)
	m.Color = strVal(rt.Color, m.Color)
}

func (m *relTypeModel) input() client.RelTypeInput {
	d := m.Directed.ValueBool()
	return client.RelTypeInput{Name: m.Name.ValueString(), InverseName: m.InverseName.ValueString(), Title: m.Title.ValueString(),
		Description: m.Description.ValueString(), Directed: &d, FromKinds: m.FromKinds, ToKinds: m.ToKinds,
		DefaultEvidence: m.DefaultEvidence.ValueString(), Cardinality: m.Cardinality.ValueString(), AttributesSchema: rawJSON(m.AttributesSchema),
		Impact: m.Impact.ValueString(), Color: m.Color.ValueString()}
}

func builtinRelTypeError(diags *diag.Diagnostics, name string) {
	diags.AddAttributeError(path.Root("name"), "Built-in relationship types cannot be managed",
		fmt.Sprintf("%q is a built-in Deed relationship type and cannot be created, changed or deleted. Use it directly in "+
			"deed_relationship.type, or read it with the deed_relationship_type data source.", name))
}

func (r *relTypeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan relTypeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rt, err := r.client.CreateRelType(ctx, plan.input())
	if err != nil {
		if client.HasCode(err, client.CodeAlreadyExists) {
			if cur, gerr := r.client.GetRelType(ctx, plan.Name.ValueString()); gerr == nil && cur.Builtin {
				builtinRelTypeError(&resp.Diagnostics, cur.Name)
				return
			}
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Relationship type already exists",
				"A custom relationship type named "+plan.Name.ValueString()+" already exists in Deed. Import it instead of creating it:\n\n"+
					"  terraform import <address> "+plan.Name.ValueString())
			return
		}
		apiError(&resp.Diagnostics, "Cannot create relationship type", err, relTypeFields)
		return
	}
	plan.fill(rt)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(saveETag(ctx, resp.Private, rt.ETag)...)
}

func (r *relTypeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state relTypeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rt, err := r.client.GetRelType(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot read relationship type", err, nil)
		return
	}
	if rt.Builtin {
		builtinRelTypeError(&resp.Diagnostics, rt.Name)
		return
	}
	state.fill(rt)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(saveETag(ctx, resp.Private, rt.ETag)...)
}

func (r *relTypeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state relTypeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rt, err := r.client.UpdateRelType(ctx, state.ID.ValueString(), loadETag(ctx, req.Private), plan.input())
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot update relationship type", err, relTypeFields)
		return
	}
	plan.fill(rt)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(saveETag(ctx, resp.Private, rt.ETag)...)
}

func (r *relTypeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state relTypeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := state.ID.ValueString()
	err := r.client.DeleteRelType(ctx, name)
	if err == nil || client.IsNotFound(err) {
		return
	}
	if client.HasCode(err, client.CodeConflict) {
		live, lerr := r.client.ListRelations(ctx, client.RelationFilter{Types: []string{name}})
		// The search only covers projects the token can read: trust "no live relationships" only from an unrestricted token.
		if lerr == nil && len(live) == 0 && r.client.SeesAllProjects(ctx) {
			resp.Diagnostics.AddWarning("Relationship type kept by Deed until retracted relationships are compacted",
				fmt.Sprintf("Relationship type %q has no live relationships, but Deed keeps a type while retracted relationships still "+
					"reference it (they are compacted after the retention period). It was removed from the Terraform state and still exists "+
					"in Deed; delete it later in Deed, or import it to manage it again (terraform import <address> %s).", name, name))
			return
		}
	}
	apiError(&resp.Diagnostics, "Cannot delete relationship type", err, nil)
}

func (r *relTypeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	rt, err := r.client.GetRelType(ctx, req.ID)
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot import relationship type", err, nil)
		return
	}
	if rt.Builtin {
		builtinRelTypeError(&resp.Diagnostics, rt.Name)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), rt.Name)...)
}
