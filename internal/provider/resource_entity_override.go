package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
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
	_ resource.ResourceWithImportState    = (*entityOverrideResource)(nil)
	_ resource.ResourceWithConfigure      = (*entityOverrideResource)(nil)
	_ resource.ResourceWithUpgradeState   = (*entityOverrideResource)(nil)
	_ resource.ResourceWithValidateConfig = (*entityOverrideResource)(nil)
)

// overrideSource is Deed's per-project system source of the override layer.
const overrideSource = "overrides"

type entityOverrideResource struct{ baseResource }

// NewEntityOverrideResource returns the deed_entity_override resource.
func NewEntityOverrideResource() resource.Resource { return &entityOverrideResource{} }

type entityOverrideModel struct {
	ID              types.String         `tfsdk:"id"`
	EntityID        types.String         `tfsdk:"entity_id"`
	Project         types.String         `tfsdk:"project"`
	ProjectID       types.String         `tfsdk:"project_id"`
	Kind            types.String         `tfsdk:"kind"`
	Name            types.String         `tfsdk:"name"`
	DisplayName     types.String         `tfsdk:"display_name"`
	Description     types.String         `tfsdk:"description"`
	Environment     types.String         `tfsdk:"environment"`
	Owner           types.String         `tfsdk:"owner"`
	Lifecycle       types.String         `tfsdk:"lifecycle_stage"`
	Icon            types.String         `tfsdk:"icon"`
	Labels          map[string]string    `tfsdk:"labels"`
	Annotations     map[string]string    `tfsdk:"annotations"`
	Links           []linkModel          `tfsdk:"links"`
	Tags            []string             `tfsdk:"tags"`
	VersionDeclared types.String         `tfsdk:"version_declared"`
	VersionPolicy   types.String         `tfsdk:"version_policy"`
	VersionChannel  types.String         `tfsdk:"version_channel"`
	VersionProduct  types.String         `tfsdk:"version_product"`
	Spec            jsontypes.Normalized `tfsdk:"spec"`
}

// asEntity and fromEntity reuse deed_entity's declaration mapping for the fields both resources share.
func (m *entityOverrideModel) asEntity() *entityModel {
	return &entityModel{DisplayName: m.DisplayName, Description: m.Description, Environment: m.Environment, Owner: m.Owner,
		Lifecycle: m.Lifecycle, Icon: m.Icon, Labels: m.Labels, Annotations: m.Annotations, Links: m.Links, Tags: m.Tags,
		VersionDeclared: m.VersionDeclared, VersionPolicy: m.VersionPolicy, VersionChannel: m.VersionChannel,
		VersionProduct: m.VersionProduct, Spec: m.Spec, Project: m.Project, Kind: m.Kind, Name: m.Name}
}

func (m *entityOverrideModel) fill(d *client.Declaration) {
	e := m.asEntity()
	e.fill(d)
	m.ID, m.EntityID, m.ProjectID = types.StringValue(d.EntityID), types.StringValue(d.EntityID), types.StringValue(d.ProjectID)
	m.Project, m.Kind, m.Name = e.Project, e.Kind, e.Name
	m.DisplayName, m.Description, m.Environment, m.Owner, m.Lifecycle, m.Icon = e.DisplayName, e.Description, e.Environment, e.Owner,
		e.Lifecycle, e.Icon
	m.Labels, m.Annotations, m.Links, m.Tags = e.Labels, e.Annotations, e.Links, e.Tags
	m.VersionDeclared, m.VersionPolicy, m.VersionChannel, m.VersionProduct = e.VersionDeclared, e.VersionPolicy, e.VersionChannel,
		e.VersionProduct
	m.Spec = e.Spec
}

func (m *entityOverrideModel) input() client.DeclarationInput {
	e := m.asEntity()
	return client.DeclarationInput{Metadata: e.metadata(), Spec: rawJSON(m.Spec)}
}

func (r *entityOverrideResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_entity_override"
}

func (r *entityOverrideResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	var es resource.SchemaResponse
	(&entityResource{}).Schema(ctx, resource.SchemaRequest{}, &es)
	target := func(desc string, v ...validator.String) schema.StringAttribute {
		return schema.StringAttribute{MarkdownDescription: desc + " Changing it forces a new resource.", Optional: true, Computed: true,
			PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured(), stringplanmodifier.UseStateForUnknown()},
			Validators:    v}
	}
	attrs := map[string]schema.Attribute{
		"id": idAttribute("Entity ID (`ent_…`) of the overridden entity."),
		"entity_id": target("The entity to override, by ID. Set either `entity_id`, or `project`, `kind` and `name`.",
			stringvalidator.RegexMatches(entityIDRE, "must be an entity ID (ent_…)")),
		"project": target("Project of the entity to override (with `kind` and `name`).", stringvalidator.LengthBetween(1, 64)),
		"kind":    target("Kind of the entity to override.", stringvalidator.RegexMatches(kindNameRE, "must be a kind name")),
		"name": target("Name of the entity to override (aliases work too).",
			stringvalidator.RegexMatches(entityNameRE, "must be a lowercase slug (a-z, 0-9, '.', '_', '-')")),
		"project_id": computedString("Project ID (`prj_…`)."),
	}
	for _, name := range []string{"display_name", "description", "environment", "owner", "lifecycle_stage", "icon", "labels", "annotations",
		"links", "tags", "version_declared", "version_policy", "version_channel", "version_product", "spec"} {
		attrs[name] = es.Schema.Attributes[name]
	}
	resp.Schema = schema.Schema{
		Version: stateSchemaVersion,
		MarkdownDescription: "An **override** of an entity that other sources own (a GitHub repository, a UniFi client, a HallPass server): " +
			"display name, icon, owner, labels, … and spec values, written to the project's `overrides` source (layer `override`, " +
			"above declarations and imports) **without declaring the entity**.\n\n" +
			"- Labels and annotations merge per key, `links` and `tags` as unions; what the override does not set keeps coming " +
			"from the sources.\n" +
			"- State is read only from the override (`GET /entities/{id}/declarations/overrides`), so other sources' values never " +
			"cause a diff. Destroying it removes only the override.\n" +
			"- An override never keeps an entity alive. When the last source lets go, the entity is gone and the override reads as " +
			"removed: Terraform plans to create it again, and the apply fails clearly until a source describes the entity again.\n\n" +
			"Requires Deed 0.7.0 or later and a project `admin` token. For the same labels on many entities, use `deed_label_rule`.",
		Attributes: attrs,
	}
}

// ValidateConfig requires a target: entity_id, or project, kind and name together.
func (r *entityOverrideResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m entityOverrideModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	set := func(v types.String) bool { return !v.IsNull() }
	byID, byName := set(m.EntityID), set(m.Project) || set(m.Kind) || set(m.Name)
	switch {
	case byID && byName:
		resp.Diagnostics.AddAttributeError(path.Root("entity_id"), "Two targets",
			"Set either entity_id, or project, kind and name — not both.")
	case !byID && !byName:
		resp.Diagnostics.AddError("Missing target", "Set entity_id, or project, kind and name, to name the entity to override.")
	case byName && (!set(m.Project) || !set(m.Kind) || !set(m.Name)):
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Incomplete target", "project, kind and name are needed together.")
	}
}

func (r *entityOverrideResource) target(ctx context.Context, m *entityOverrideModel) (string, error) {
	if id := m.EntityID.ValueString(); id != "" && !m.EntityID.IsUnknown() {
		return id, nil
	}
	e, err := r.client.LookupEntity(ctx, m.Project.ValueString(), m.Kind.ValueString(), m.Name.ValueString())
	if err != nil {
		return "", err
	}
	return e.Metadata.ID, nil
}

func (r *entityOverrideResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan entityOverrideModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := r.target(ctx, &plan)
	if client.IsNotFound(err) {
		resp.Diagnostics.AddError("Entity not found",
			"No entity "+plan.Project.ValueString()+"/"+plan.Kind.ValueString()+"/"+plan.Name.ValueString()+" exists. Overrides change entities "+
				"that sources describe; they cannot create one (declare it with deed_entity instead).")
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot find the entity to override", err, nil)
		return
	}
	d, err := r.client.PutDeclaration(ctx, id, overrideSource, "", plan.input())
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot override the entity", err, entityFields)
		return
	}
	plan.fill(d)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(saveETag(ctx, resp.Private, d.ETag)...)
}

func (r *entityOverrideResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state entityOverrideModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, err := r.client.GetDeclaration(ctx, state.ID.ValueString(), overrideSource)
	if client.IsNotFound(err) {
		// Removed outside Terraform, or the entity is gone (no source describes it): plan to create it again.
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot read the override", err, nil)
		return
	}
	state.fill(d)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(saveETag(ctx, resp.Private, d.ETag)...)
}

func (r *entityOverrideResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state entityOverrideModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, err := r.client.PutDeclaration(ctx, state.ID.ValueString(), overrideSource, loadETag(ctx, req.Private), plan.input())
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot update the override", err, entityFields)
		return
	}
	plan.fill(d)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(saveETag(ctx, resp.Private, d.ETag)...)
}

func (r *entityOverrideResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state entityOverrideModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteDeclaration(ctx, state.ID.ValueString(), overrideSource); err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "Cannot remove the override", err, nil)
	}
}

// ImportState takes an entity ID or "<project>/<kind>/<name>".
func (r *entityOverrideResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := req.ID
	if parts := strings.Split(req.ID, "/"); len(parts) >= 3 {
		e, err := r.client.LookupEntity(ctx, parts[0], strings.Join(parts[1:len(parts)-1], "/"), parts[len(parts)-1])
		if err != nil {
			apiError(&resp.Diagnostics, "Cannot import the override", err, nil)
			return
		}
		id = e.Metadata.ID
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("entity_id"), id)...)
}
