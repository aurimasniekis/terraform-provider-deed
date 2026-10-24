package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/aurimasniekis/terraform-provider-deed/internal/client"
)

var (
	_ resource.ResourceWithImportState  = (*projectMemberResource)(nil)
	_ resource.ResourceWithConfigure    = (*projectMemberResource)(nil)
	_ resource.ResourceWithUpgradeState = (*projectMemberResource)(nil)
)

type projectMemberResource struct{ baseResource }

// NewProjectMemberResource returns the deed_project_member resource.
func NewProjectMemberResource() resource.Resource { return &projectMemberResource{} }

type projectMemberModel struct {
	ID          types.String `tfsdk:"id"`
	Project     types.String `tfsdk:"project"`
	ProjectID   types.String `tfsdk:"project_id"`
	UserID      types.String `tfsdk:"user_id"`
	Role        types.String `tfsdk:"role"`
	DisplayName types.String `tfsdk:"display_name"`
	Email       types.String `tfsdk:"email"`
}

func (r *projectMemberResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project_member"
}

func (r *projectMemberResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Version: stateSchemaVersion,
		MarkdownDescription: "A user's explicit role in a Deed project (`viewer`, `editor` or `admin`). Requires the project `admin` role " +
			"(or an installation admin token). Destroying the resource removes the role.",
		Attributes: map[string]schema.Attribute{
			"id": idAttribute("`<project_id>/<user_id>`."),
			"project": schema.StringAttribute{
				MarkdownDescription: "Project name or ID. Changing it forces a new resource (the same project by name or ID does not).",
				Required:            true,
				PlanModifiers:       []planmodifier.String{sameProjectReplace("project_id")},
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 64)},
			},
			"project_id": computedString("Project ID (`prj_…`)."),
			"user_id": requiredImmutableString("User ID (`usr_…`).",
				stringvalidator.LengthBetween(1, 64)),
			"role": schema.StringAttribute{
				MarkdownDescription: "Project role: `viewer`, `editor` or `admin`.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.OneOf("viewer", "editor", "admin")},
			},
			"display_name": schema.StringAttribute{
				MarkdownDescription: "The user's display name.", Computed: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"email": schema.StringAttribute{
				MarkdownDescription: "The user's email address.", Computed: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

var memberFields = rootMapper(map[string]string{"role": "role", "user": "user_id", "project": "project"})

func (m *projectMemberModel) fill(mem *client.ProjectMember) {
	if m.Project.IsNull() || m.Project.IsUnknown() {
		m.Project = types.StringValue(mem.ProjectName)
	}
	m.ProjectID = types.StringValue(mem.ProjectID)
	m.UserID = types.StringValue(mem.UserID)
	m.Role = types.StringValue(mem.Role)
	m.DisplayName = types.StringValue(mem.DisplayName)
	m.Email = types.StringValue(mem.Email)
	m.ID = types.StringValue(mem.ProjectID + "/" + mem.UserID)
}

func (r *projectMemberResource) apply(ctx context.Context, plan *projectMemberModel) (*client.ProjectMember, error) {
	if err := r.client.SetMember(ctx, plan.Project.ValueString(), plan.UserID.ValueString(), plan.Role.ValueString()); err != nil {
		return nil, err
	}
	return r.client.GetMember(ctx, plan.Project.ValueString(), plan.UserID.ValueString())
}

func (r *projectMemberResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan projectMemberModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	mem, err := r.apply(ctx, &plan)
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot grant project role", err, memberFields)
		return
	}
	plan.fill(mem)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *projectMemberResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state projectMemberModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	mem, err := r.client.GetMember(ctx, state.Project.ValueString(), state.UserID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot read project member", err, nil)
		return
	}
	state.fill(mem)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *projectMemberResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan projectMemberModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	mem, err := r.apply(ctx, &plan)
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot change project role", err, memberFields)
		return
	}
	plan.fill(mem)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *projectMemberResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state projectMemberModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.RemoveMember(ctx, state.Project.ValueString(), state.UserID.ValueString()); err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "Cannot remove project role", err, nil)
	}
}

func (r *projectMemberResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := splitID(req.ID, 2, "<project>/<user_id>")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("user_id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
