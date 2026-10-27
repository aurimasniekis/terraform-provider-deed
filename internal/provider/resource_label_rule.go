package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/aurimasniekis/terraform-provider-deed/internal/client"
)

var (
	_ resource.ResourceWithImportState  = (*labelRuleResource)(nil)
	_ resource.ResourceWithConfigure    = (*labelRuleResource)(nil)
	_ resource.ResourceWithUpgradeState = (*labelRuleResource)(nil)
)

type labelRuleResource struct{ baseResource }

// NewLabelRuleResource returns the deed_label_rule resource.
func NewLabelRuleResource() resource.Resource { return &labelRuleResource{} }

type labelRuleMatchModel struct {
	Kinds   []string          `tfsdk:"kinds"`
	Sources []string          `tfsdk:"sources"`
	Labels  map[string]string `tfsdk:"labels"`
}

type labelRuleModel struct {
	ID          types.String         `tfsdk:"id"`
	Project     types.String         `tfsdk:"project"`
	ProjectID   types.String         `tfsdk:"project_id"`
	Name        types.String         `tfsdk:"name"`
	Description types.String         `tfsdk:"description"`
	Match       *labelRuleMatchModel `tfsdk:"match"`
	Labels      map[string]string    `tfsdk:"labels"`
}

func (r *labelRuleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_label_rule"
}

func (r *labelRuleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	labelKey := stringvalidator.RegexMatches(labelKeyRE, "must be a label key such as group or example.com/team")
	glob := setvalidator.ValueStringsAre(stringvalidator.LengthBetween(1, 256))
	resp.Schema = schema.Schema{
		Version: stateSchemaVersion,
		MarkdownDescription: "A Deed label rule: labels (such as `group = \"github\"`) added to the entities of a project that the rule's " +
			"`match` selects — entities other sources own, without declaring them. Rules apply **below every source** (a source's " +
			"own label for the same key wins) and never keep an entity alive. Creating, changing or destroying a rule re-applies " +
			"the project's rules before the apply finishes. Requires Deed 0.7.0 or later and a project `admin` token.",
		Attributes: map[string]schema.Attribute{
			"id": idAttribute("Label rule ID (`lbr_…`)."),
			"project": schema.StringAttribute{
				MarkdownDescription: "Project name or ID. Changing it forces a new resource (the same project by name or ID does not).",
				Required:            true,
				PlanModifiers:       []planmodifier.String{sameProjectReplace("project_id")},
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 64)},
			},
			"project_id": computedString("Project ID (`prj_…`)."),
			"name": schema.StringAttribute{
				MarkdownDescription: "Unique name in the project (lowercase slug).", Required: true,
				Validators: []validator.String{stringvalidator.RegexMatches(projectNameRE, "must be a lowercase slug (a-z, 0-9, -) of at most 63 characters")},
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Description.", Optional: true,
				Validators: []validator.String{stringvalidator.LengthAtMost(2000)},
			},
			"match": schema.SingleNestedAttribute{
				MarkdownDescription: "The entities the rule applies to: every given criterion must match; omit `match` for every entity of " +
					"the project. Kinds, source names and label values are globs (`*`, `?`, `[…]`).",
				Optional: true,
				Attributes: map[string]schema.Attribute{
					"kinds": schema.SetAttribute{
						MarkdownDescription: "Entity kinds (any may match), e.g. `[\"dns_*\", \"domain\"]`.",
						ElementType:         types.StringType, Optional: true,
						Validators: []validator.Set{setvalidator.SizeAtMost(50), glob},
					},
					"sources": schema.SetAttribute{
						MarkdownDescription: "Names of sources that describe the entity (any may match), e.g. `[\"tf-cloudflare-*\"]`.",
						ElementType:         types.StringType, Optional: true,
						Validators: []validator.Set{setvalidator.SizeAtMost(50), glob},
					},
					"labels": schema.MapAttribute{
						MarkdownDescription: "Labels the entity's sources set (all must match; values may be globs), e.g. " +
							"`{ \"terraform/type\" = \"cloudflare_ruleset\" }`. Labels added by other rules are not matched.",
						ElementType: types.StringType, Optional: true,
						Validators: []validator.Map{mapvalidator.SizeAtMost(50), mapvalidator.KeysAre(labelKey),
							mapvalidator.ValueStringsAre(stringvalidator.LengthBetween(1, 256))},
					},
				},
			},
			"labels": schema.MapAttribute{
				MarkdownDescription: "Labels to add (at most 32; values 1–256 bytes). A source's own label for the same key wins.",
				ElementType:         types.StringType, Required: true,
				Validators: []validator.Map{mapvalidator.SizeBetween(1, 32), mapvalidator.KeysAre(labelKey),
					mapvalidator.ValueStringsAre(stringvalidator.LengthBetween(1, 256))},
			},
		},
	}
}

var labelRuleFields = rootMapper(map[string]string{"name": "name", "description": "description", "match": "match", "labels": "labels"})

func (m *labelRuleModel) input() client.LabelRuleInput {
	desc := m.Description.ValueString()
	in := client.LabelRuleInput{Name: m.Name.ValueString(), Description: &desc, Match: &client.LabelRuleMatch{}, Labels: m.Labels}
	if m.Match != nil {
		in.Match = &client.LabelRuleMatch{Kinds: m.Match.Kinds, Sources: m.Match.Sources, Labels: m.Match.Labels}
	}
	return in
}

func (m *labelRuleModel) fill(r *client.LabelRule) {
	m.ID = types.StringValue(r.ID)
	m.ProjectID = types.StringValue(r.ProjectID)
	if m.Project.IsNull() || m.Project.IsUnknown() {
		m.Project = types.StringValue(r.ProjectID)
	}
	m.Name = types.StringValue(r.Name)
	m.Description = strVal(r.Description, m.Description)
	mt := r.Match
	if m.Match == nil && len(mt.Kinds) == 0 && len(mt.Sources) == 0 && len(mt.Labels) == 0 {
		m.Match = nil
	} else {
		prior := m.Match
		if prior == nil {
			prior = &labelRuleMatchModel{}
		}
		m.Match = &labelRuleMatchModel{Kinds: listVal(mt.Kinds, prior.Kinds), Sources: listVal(mt.Sources, prior.Sources),
			Labels: mapVal(mt.Labels, prior.Labels)}
	}
	m.Labels = r.Labels
}

func (r *labelRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan labelRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rule, err := r.client.CreateLabelRule(ctx, plan.Project.ValueString(), plan.input())
	if err != nil {
		if client.HasCode(err, client.CodeAlreadyExists) {
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Label rule already exists",
				"A label rule named "+plan.Name.ValueString()+" already exists in this project. Import it instead of creating it:\n\n"+
					"  terraform import <address> "+plan.Project.ValueString()+"/"+plan.Name.ValueString())
			return
		}
		apiError(&resp.Diagnostics, "Cannot create label rule", err, labelRuleFields)
		return
	}
	plan.fill(rule)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(saveETag(ctx, resp.Private, rule.ETag)...)
}

func (r *labelRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state labelRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rule, err := r.client.GetLabelRule(ctx, state.ProjectID.ValueString(), state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot read label rule", err, nil)
		return
	}
	state.fill(rule)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(saveETag(ctx, resp.Private, rule.ETag)...)
}

func (r *labelRuleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state labelRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rule, err := r.client.UpdateLabelRule(ctx, state.ProjectID.ValueString(), state.ID.ValueString(), loadETag(ctx, req.Private), plan.input())
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot update label rule", err, labelRuleFields)
		return
	}
	plan.fill(rule)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(saveETag(ctx, resp.Private, rule.ETag)...)
}

func (r *labelRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state labelRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteLabelRule(ctx, state.ProjectID.ValueString(), state.ID.ValueString()); err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "Cannot delete label rule", err, nil)
	}
}

// ImportState takes "<project>/<name or ID>".
func (r *labelRuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	project, rule, ok := strings.Cut(req.ID, "/")
	if !ok || project == "" || rule == "" {
		resp.Diagnostics.AddError("Invalid import ID", "Use <project>/<rule name or ID>, e.g. infra/github.")
		return
	}
	got, err := r.client.GetLabelRule(ctx, project, rule)
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot import label rule", err, nil)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), got.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project"), project)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), got.ProjectID)...)
}
