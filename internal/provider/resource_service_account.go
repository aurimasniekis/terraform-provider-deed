package provider

import (
	"context"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/aurimasniekis/terraform-provider-deed/internal/client"
)

// ---------------------------------------------------------------------------------------------------------------------
// deed_service_account

var (
	_ resource.ResourceWithImportState  = (*serviceAccountResource)(nil)
	_ resource.ResourceWithConfigure    = (*serviceAccountResource)(nil)
	_ resource.ResourceWithUpgradeState = (*serviceAccountResource)(nil)
)

type serviceAccountResource struct{ baseResource }

// NewServiceAccountResource returns the deed_service_account resource.
func NewServiceAccountResource() resource.Resource { return &serviceAccountResource{} }

type serviceAccountModel struct {
	ID               types.String `tfsdk:"id"`
	Name             types.String `tfsdk:"name"`
	DisplayName      types.String `tfsdk:"display_name"`
	Description      types.String `tfsdk:"description"`
	InstallationRole types.String `tfsdk:"installation_role"`
	Enabled          types.Bool   `tfsdk:"enabled"`
}

func (r *serviceAccountResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_account"
}

func (r *serviceAccountResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Version: stateSchemaVersion,
		MarkdownDescription: "A Deed service account: a non-human user for automation (Terraform, CI) that has no password, SSO identity " +
			"or session and authenticates only with API tokens (`deed_service_account_token`). Its installation role and project " +
			"memberships (`deed_project_member` with its `id` as `user_id`) bound every token. Requires an installation `admin` (or " +
			"owner) token and Deed 0.2.0 or newer. Destroying it deletes its tokens and memberships.",
		Attributes: map[string]schema.Attribute{
			"id": idAttribute("User ID of the service account (`usr_…`); use it as `user_id` in `deed_project_member`."),
			"name": requiredImmutableString("Unique name: 1–63 lowercase letters, digits, `-` or `_`.",
				stringvalidator.RegexMatches(serviceAccountNameRE, "must be 1–63 lowercase letters, digits, '-' or '_', starting with a letter or digit")),
			"display_name": schema.StringAttribute{
				MarkdownDescription: "Human-friendly name. Defaults to `name`.",
				Optional:            true, Computed: true,
				Validators:    []validator.String{stringvalidator.LengthBetween(1, 128)},
				PlanModifiers: []planmodifier.String{defaultFromAttribute("name")},
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "What the service account is for.",
				Optional:            true,
				Validators:          []validator.String{stringvalidator.LengthAtMost(500)},
			},
			"installation_role": schema.StringAttribute{
				MarkdownDescription: "Installation role: `admin` or `auditor`. Omit for none (the account then sees only the projects it " +
					"is a member of). A service account cannot be an owner.",
				Optional:   true,
				Validators: []validator.String{stringvalidator.OneOf("admin", "auditor")},
			},
			"enabled": schema.BoolAttribute{
				MarkdownDescription: "Disabling (`false`) revokes every token of the account. Default `true`.",
				Optional:            true, Computed: true, Default: booldefault.StaticBool(true),
			},
		},
	}
}

var serviceAccountFields = rootMapper(map[string]string{"name": "name", "display_name": "display_name", "description": "description",
	"installation_role": "installation_role", "status": "enabled"})

func (m *serviceAccountModel) fill(sa *client.ServiceAccount) {
	m.ID = types.StringValue(sa.ID)
	m.Name = types.StringValue(sa.Name)
	m.DisplayName = types.StringValue(sa.DisplayName)
	m.Description = strVal(sa.Description, m.Description)
	m.InstallationRole = strVal(sa.InstallationRole, types.StringNull())
	m.Enabled = types.BoolValue(sa.Status == "active")
}

func (m *serviceAccountModel) patch() client.ServiceAccountPatch {
	display, desc, role := m.DisplayName.ValueString(), m.Description.ValueString(), m.InstallationRole.ValueString()
	status := "active"
	if !m.Enabled.ValueBool() {
		status = "disabled"
	}
	return client.ServiceAccountPatch{DisplayName: &display, Description: &desc, InstallationRole: &role, Status: &status}
}

func (r *serviceAccountResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan serviceAccountModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	sa, err := r.client.CreateServiceAccount(ctx, client.ServiceAccountInput{Name: plan.Name.ValueString(), DisplayName: plan.DisplayName.ValueString(),
		Description: plan.Description.ValueString(), InstallationRole: plan.InstallationRole.ValueString()})
	if err != nil {
		if client.HasCode(err, client.CodeAlreadyExists) {
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Service account already exists",
				"A service account named "+plan.Name.ValueString()+" already exists. Import it instead of creating it:\n\n  terraform import <address> "+
					plan.Name.ValueString())
			return
		}
		apiError(&resp.Diagnostics, "Cannot create service account", err, serviceAccountFields)
		return
	}
	if !plan.Enabled.ValueBool() {
		if sa, err = r.client.UpdateServiceAccount(ctx, sa.ID, plan.patch()); err != nil {
			apiError(&resp.Diagnostics, "Cannot disable service account", err, serviceAccountFields)
			return
		}
	}
	plan.fill(sa)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *serviceAccountResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state serviceAccountModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	sa, err := r.client.GetServiceAccount(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot read service account", err, nil)
		return
	}
	state.fill(sa)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *serviceAccountResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state serviceAccountModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	sa, err := r.client.UpdateServiceAccount(ctx, state.ID.ValueString(), plan.patch())
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot update service account", err, serviceAccountFields)
		return
	}
	plan.fill(sa)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *serviceAccountResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state serviceAccountModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteServiceAccount(ctx, state.ID.ValueString()); err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "Cannot delete service account", err, nil)
	}
}

func (r *serviceAccountResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Accept an ID or a name; Read fills the rest.
	sa, err := r.client.GetServiceAccount(ctx, req.ID)
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot import service account", err, nil)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), sa.ID)...)
}

// ---------------------------------------------------------------------------------------------------------------------
// deed_service_account_token

var (
	_ resource.ResourceWithConfigure    = (*serviceTokenResource)(nil)
	_ resource.ResourceWithModifyPlan   = (*serviceTokenResource)(nil)
	_ resource.ResourceWithUpgradeState = (*serviceTokenResource)(nil)
)

type serviceTokenResource struct{ baseResource }

// NewServiceAccountTokenResource returns the deed_service_account_token resource.
func NewServiceAccountTokenResource() resource.Resource { return &serviceTokenResource{} }

type serviceTokenModel struct {
	ID               types.String `tfsdk:"id"`
	ServiceAccount   types.String `tfsdk:"service_account"`
	Name             types.String `tfsdk:"name"`
	InstallationRole types.String `tfsdk:"installation_role"`
	ProjectRole      types.String `tfsdk:"project_role"`
	Projects         types.Set    `tfsdk:"projects"`
	ExpiresInDays    types.Int64  `tfsdk:"expires_in_days"`
	RotateBeforeDays types.Int64  `tfsdk:"rotate_before_days"`
	Keepers          types.Map    `tfsdk:"keepers"`
	Token            types.String `tfsdk:"token"`
	UserID           types.String `tfsdk:"user_id"`
	CreatedAt        types.String `tfsdk:"created_at"`
	ExpiresAt        types.String `tfsdk:"expires_at"`
}

func (r *serviceTokenResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_account_token"
}

func (r *serviceTokenResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replaceStr := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Version: stateSchemaVersion,
		MarkdownDescription: "An API token (`deed_at_…`) for a service account. Every argument forces a new token; destroying it revokes " +
			"the token. Requires Deed 0.2.0 or newer and an installation `admin` (or owner) token, which may be a service account " +
			"token itself, so Terraform " +
			"can rotate its own credential.\n\n" +
			"The secret is a sensitive computed attribute and is therefore stored in the Terraform state: keep the state encrypted, " +
			"or create the first token with `deed admin token` on the Deed host and store it only in your secret manager.\n\n" +
			"Rotation: the token is replaced when it is revoked or expired outside Terraform, when it expires within " +
			"`rotate_before_days`, or when a value in `keepers` changes.",
		Attributes: map[string]schema.Attribute{
			"id": idAttribute("Token ID (`atk_…`)."),
			"service_account": schema.StringAttribute{
				MarkdownDescription: "Service account ID or name. Changing it forces a new token.",
				Required:            true, PlanModifiers: replaceStr,
				Validators: []validator.String{stringvalidator.LengthBetween(1, 64)},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Token name, shown in the UI. Changing it forces a new token.",
				Required:            true, PlanModifiers: replaceStr,
				Validators: []validator.String{stringvalidator.LengthBetween(1, 100)},
			},
			"installation_role": schema.StringAttribute{
				MarkdownDescription: "Installation role of the token: `admin`, `auditor` or `none` (project memberships only). Defaults to " +
					"the service account's role; it can never exceed it. Changing it forces a new token.",
				Optional: true, Computed: true,
				Validators:    []validator.String{stringvalidator.OneOf("admin", "auditor", "none")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
			},
			"project_role": schema.StringAttribute{
				MarkdownDescription: "Caps every project role of the token: `viewer`, `editor` or `admin`. Changing it forces a new token.",
				Optional:            true, PlanModifiers: replaceStr,
				Validators: []validator.String{stringvalidator.OneOf("viewer", "editor", "admin")},
			},
			"projects": schema.SetAttribute{
				MarkdownDescription: "Restrict the token to these projects (names or IDs). Omit for every project the account can access. " +
					"Changing it forces a new token.",
				ElementType: types.StringType, Optional: true,
				PlanModifiers: []planmodifier.Set{setplanmodifier.RequiresReplace()},
				Validators:    []validator.Set{setvalidator.SizeBetween(1, 100)},
			},
			"expires_in_days": schema.Int64Attribute{
				MarkdownDescription: "Lifetime in days (default 90, at most Deed's `auth.max_token_lifetime`, 366 by default). Changing it " +
					"forces a new token.",
				Optional: true, Computed: true, Default: int64default.StaticInt64(90),
				Validators:    []validator.Int64{int64validator.Between(1, 366)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"rotate_before_days": schema.Int64Attribute{
				MarkdownDescription: "Replace the token on the next apply once it expires within this many days. Omit to replace it only " +
					"after it has expired.",
				Optional:   true,
				Validators: []validator.Int64{int64validator.AtLeast(1)},
			},
			"keepers": schema.MapAttribute{
				MarkdownDescription: "Arbitrary values that force a new token when they change (for example a rotation date).",
				ElementType:         types.StringType, Optional: true,
				PlanModifiers: []planmodifier.Map{mapplanmodifier.RequiresReplace()},
			},
			"token": schema.StringAttribute{
				MarkdownDescription: "The bearer token (`deed_at_…`). Sensitive; stored in state.",
				Computed:            true, Sensitive: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"user_id":    computedString("User ID of the service account."),
			"created_at": computedString("Creation time (RFC 3339)."),
			"expires_at": computedString("Expiry time (RFC 3339)."),
		},
	}
}

var serviceTokenFields = rootMapper(map[string]string{"name": "name", "installation_role": "installation_role", "project_role": "project_role",
	"projects": "projects", "expires_in_days": "expires_in_days"})

// ModifyPlan replaces a token that expires within rotate_before_days (or has expired): marking expires_at unknown gives
// the plan a change that RequiresReplace turns into a replacement.
func (r *serviceTokenResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var state, plan serviceTokenModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	exp, err := time.Parse(time.RFC3339, state.ExpiresAt.ValueString())
	if err != nil {
		return
	}
	margin := time.Duration(plan.RotateBeforeDays.ValueInt64()) * 24 * time.Hour
	if time.Until(exp) > margin {
		return
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("expires_at"), types.StringUnknown())...)
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("token"), types.StringUnknown())...)
	resp.RequiresReplace = append(resp.RequiresReplace, path.Root("expires_at"))
}

func tokenRole(r string) string {
	if r == "" {
		return "none"
	}
	return r
}

func (r *serviceTokenResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan serviceTokenModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in := client.ServiceTokenInput{Name: plan.Name.ValueString(), ProjectRole: plan.ProjectRole.ValueString(), ExpiresInDays: plan.ExpiresInDays.ValueInt64()}
	if !plan.InstallationRole.IsNull() && !plan.InstallationRole.IsUnknown() {
		role := plan.InstallationRole.ValueString()
		if role == "none" {
			role = ""
		}
		in.InstallationRole = &role
	}
	if !plan.Projects.IsNull() && !plan.Projects.IsUnknown() {
		resp.Diagnostics.Append(plan.Projects.ElementsAs(ctx, &in.Projects, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	t, err := r.client.CreateServiceAccountToken(ctx, plan.ServiceAccount.ValueString(), in)
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot create service account token", err, serviceTokenFields)
		return
	}
	plan.ID = types.StringValue(t.ID)
	plan.Token = types.StringValue(t.Token)
	plan.UserID = types.StringValue(t.UserID)
	plan.InstallationRole = types.StringValue(tokenRole(t.InstallationRole))
	plan.CreatedAt = types.StringValue(t.CreatedAt.UTC().Format(time.RFC3339))
	plan.ExpiresAt = types.StringValue(t.ExpiresAt.UTC().Format(time.RFC3339))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *serviceTokenResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state serviceTokenModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	t, err := r.client.GetServiceAccountToken(ctx, state.ServiceAccount.ValueString(), state.ID.ValueString())
	// A revoked or expired token (or a deleted account) is gone: the next apply creates a new one.
	if client.IsNotFound(err) || err == nil && (t.RevokedAt != nil || !time.Now().Before(t.ExpiresAt)) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot read service account token", err, nil)
		return
	}
	state.UserID = types.StringValue(t.UserID)
	state.InstallationRole = types.StringValue(tokenRole(t.InstallationRole))
	state.CreatedAt = types.StringValue(t.CreatedAt.UTC().Format(time.RFC3339))
	state.ExpiresAt = types.StringValue(t.ExpiresAt.UTC().Format(time.RFC3339))
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update only runs for rotate_before_days (every other argument forces a new token).
func (r *serviceTokenResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan serviceTokenModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *serviceTokenResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state serviceTokenModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.RevokeServiceAccountToken(ctx, state.ServiceAccount.ValueString(), state.ID.ValueString())
	if err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "Cannot revoke service account token", err, nil)
	}
}
