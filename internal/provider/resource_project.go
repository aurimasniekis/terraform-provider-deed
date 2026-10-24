package provider

import (
	"context"

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
	_ resource.ResourceWithImportState  = (*projectResource)(nil)
	_ resource.ResourceWithConfigure    = (*projectResource)(nil)
	_ resource.ResourceWithUpgradeState = (*projectResource)(nil)
)

type projectResource struct{ baseResource }

// NewProjectResource returns the deed_project resource.
func NewProjectResource() resource.Resource { return &projectResource{} }

type projectModel struct {
	ID             types.String `tfsdk:"id"`
	Name           types.String `tfsdk:"name"`
	DisplayName    types.String `tfsdk:"display_name"`
	Description    types.String `tfsdk:"description"`
	AllowHTTPLinks types.Bool   `tfsdk:"allow_http_links"`
	ForceDestroy   types.Bool   `tfsdk:"force_destroy"`
}

func (r *projectResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

func (r *projectResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Version: stateSchemaVersion,
		MarkdownDescription: "A Deed project: the organizational and security scope that owns entities, sources and relationships. " +
			"Every project is created with the system sources `api`, `terraform`, `ui` and `overrides`. Requires an installation " +
			"`admin` (or owner) token.",
		Attributes: map[string]schema.Attribute{
			"id": idAttribute("Project ID (`prj_…`)."),
			"name": requiredImmutableString("Project name: a lowercase slug (a-z, 0-9, -) of at most 63 characters, unique in the installation.",
				stringvalidator.RegexMatches(projectNameRE, "must be a lowercase slug (a-z, 0-9, -) of at most 63 characters")),
			"display_name": schema.StringAttribute{
				MarkdownDescription: "Human-friendly name. Defaults to `name`.",
				Optional:            true, Computed: true,
				Validators:    []validator.String{stringvalidator.LengthBetween(1, 128)},
				PlanModifiers: []planmodifier.String{defaultFromAttribute("name")},
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Description (Markdown).",
				Optional:            true,
				Validators:          []validator.String{stringvalidator.LengthAtMost(4096)},
			},
			"allow_http_links": schema.BoolAttribute{
				MarkdownDescription: "Allow plain `http://` entity links in this project (default `false`: https only).",
				Optional:            true, Computed: true, Default: booldefault.StaticBool(false),
			},
			"force_destroy": schema.BoolAttribute{
				MarkdownDescription: "Delete the project even when it still contains entities contributed by other sources (imports, " +
					"observations, other Terraform configurations). Default `false`: destroying a project that still has live entities fails.",
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
			},
		},
	}
}

var projectFields = rootMapper(map[string]string{"name": "name", "display_name": "display_name", "description": "description"})

func (m *projectModel) fill(pr *client.Project) {
	m.ID = types.StringValue(pr.ID)
	m.Name = types.StringValue(pr.Name)
	m.DisplayName = types.StringValue(pr.DisplayName)
	m.Description = strVal(pr.Description, m.Description)
	m.AllowHTTPLinks = types.BoolValue(pr.AllowHTTPLinks)
	if m.ForceDestroy.IsNull() || m.ForceDestroy.IsUnknown() {
		m.ForceDestroy = types.BoolValue(false)
	}
}

func (m *projectModel) input() client.ProjectInput {
	return client.ProjectInput{Name: m.Name.ValueString(), DisplayName: m.DisplayName.ValueString(), Description: m.Description.ValueString(),
		AllowHTTPLinks: m.AllowHTTPLinks.ValueBool()}
}

func (r *projectResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan projectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pr, err := r.client.CreateProject(ctx, plan.input())
	if err != nil {
		if client.HasCode(err, client.CodeAlreadyExists) {
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Project already exists",
				"A Deed project named "+plan.Name.ValueString()+" already exists. Import it instead of creating it:\n\n  terraform import <address> "+
					plan.Name.ValueString())
			return
		}
		apiError(&resp.Diagnostics, "Cannot create project", err, projectFields)
		return
	}
	plan.fill(pr)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(saveETag(ctx, resp.Private, pr.ETag)...)
}

func (r *projectResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state projectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pr, err := r.client.GetProject(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot read project", err, nil)
		return
	}
	state.fill(pr)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(saveETag(ctx, resp.Private, pr.ETag)...)
}

func (r *projectResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state projectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pr, err := r.client.UpdateProject(ctx, state.ID.ValueString(), loadETag(ctx, req.Private), plan.input())
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot update project", err, projectFields)
		return
	}
	plan.fill(pr)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(saveETag(ctx, resp.Private, pr.ETag)...)
}

func (r *projectResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state projectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.DeleteProject(ctx, state.ID.ValueString(), state.ForceDestroy.ValueBool())
	if err != nil && !client.IsNotFound(err) {
		if client.HasCode(err, client.CodeConflict) {
			apiError(&resp.Diagnostics, "Cannot delete project: it still has entities", err, nil)
			resp.Diagnostics.AddAttributeWarning(path.Root("force_destroy"), "Project still has entities",
				"Other sources still contribute entities to this project. Set force_destroy = true (and apply) to delete it with everything "+
					"it contains, or remove those entities first.")
			return
		}
		apiError(&resp.Diagnostics, "Cannot delete project", err, nil)
	}
}

func (r *projectResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Accept an ID or a name; Read fills the rest.
	pr, err := r.client.GetProject(ctx, req.ID)
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot import project", err, nil)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), pr.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("force_destroy"), false)...)
}
