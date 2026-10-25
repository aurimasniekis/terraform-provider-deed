package provider

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/aurimasniekis/terraform-provider-deed/internal/client"
)

var (
	_ resource.ResourceWithImportState    = (*kindResource)(nil)
	_ resource.ResourceWithConfigure      = (*kindResource)(nil)
	_ resource.ResourceWithValidateConfig = (*kindResource)(nil)
	_ resource.ResourceWithUpgradeState   = (*kindResource)(nil)
)

type kindResource struct{ baseResource }

// NewKindResource returns the deed_kind resource.
func NewKindResource() resource.Resource { return &kindResource{} }

type kindModel struct {
	ID             types.String         `tfsdk:"id"`
	Name           types.String         `tfsdk:"name"`
	Group          types.String         `tfsdk:"group"`
	Title          types.String         `tfsdk:"title"`
	Plural         types.String         `tfsdk:"plural"`
	Description    types.String         `tfsdk:"description"`
	Icon           types.String         `tfsdk:"icon"`
	Color          types.String         `tfsdk:"color"`
	IdentityKeys   [][]string           `tfsdk:"identity_keys"`
	Deprecated     types.Bool           `tfsdk:"deprecated"`
	Schema         jsontypes.Normalized `tfsdk:"schema"`
	Breaking       types.Bool           `tfsdk:"breaking"`
	Migration      types.String         `tfsdk:"migration"`
	StorageVersion types.String         `tfsdk:"storage_version"`
}

func (r *kindResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kind"
}

func (r *kindResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Version: stateSchemaVersion,
		MarkdownDescription: "A custom Deed kind: an entity type with a versioned JSON Schema (2020-12) for `spec`. Built-in kinds " +
			"(`server`, `service`, `repository`, …) are owned by Deed and cannot be managed; read them with the `deed_kind` data source. " +
			"Requires an installation `admin` (or owner) token.\n\n" +
			"Changing `schema` **publishes a new schema version** that becomes the storage version. Deed accepts a non-breaking " +
			"version only when it is backward compatible with the current one and every existing entity validates; otherwise the " +
			"apply fails with the compatibility problems. Set `breaking = true` and a `migration` mode to publish an incompatible " +
			"schema: `revalidate` refuses the publication if any entity fails validation, `mark_invalid` publishes it and flags failing " +
			"entities as invalid (they are never dropped).\n\n" +
			"Destroying a kind fails while live entities still use it; entities that are only tombstoned do not block it.",
		Attributes: map[string]schema.Attribute{
			"id": idAttribute("The kind name."),
			"name": requiredImmutableString("Kind name: a lowercase identifier, optionally prefixed by a domain (`example.com/ups`).",
				stringvalidator.RegexMatches(kindNameRE, "must be a lowercase identifier, optionally prefixed by a domain (example.com/ups)")),
			"group": schema.StringAttribute{
				MarkdownDescription: "Catalog group the kind is listed under (default `custom`).",
				Optional:            true, Computed: true, Default: stringdefault.StaticString("custom"),
				Validators: []validator.String{stringvalidator.RegexMatches(groupRE, "must be a lowercase identifier")},
			},
			"title": schema.StringAttribute{
				MarkdownDescription: "Singular display name, e.g. `UPS`.", Required: true,
				Validators: []validator.String{stringvalidator.LengthBetween(1, 100)},
			},
			"plural": schema.StringAttribute{
				MarkdownDescription: "Plural display name. Defaults to `title` followed by `s`.",
				Optional:            true, Computed: true,
				Validators:    []validator.String{stringvalidator.LengthBetween(1, 100)},
				PlanModifiers: []planmodifier.String{defaultFromAttributeSuffix("title", "s")},
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Description.", Optional: true,
				Validators: []validator.String{stringvalidator.LengthAtMost(2000)},
			},
			"icon": schema.StringAttribute{
				MarkdownDescription: "Icon as `pack:name` with pack `tabler`, `simple-icons`, `thesvg` or `custom`, e.g. `tabler:battery`.",
				Optional:            true,
				Validators:          []validator.String{stringvalidator.RegexMatches(iconRE, "must be pack:name (tabler, simple-icons, thesvg or custom)")},
			},
			"color": schema.StringAttribute{
				MarkdownDescription: "Color as `#rrggbb`.", Optional: true,
				Validators: []validator.String{stringvalidator.RegexMatches(colorRE, "must be #rrggbb")},
			},
			"identity_keys": schema.ListAttribute{
				MarkdownDescription: "Identity keys: each key is a list of 1–4 JSON pointers (`/spec/…` or `/metadata/…`). Sources that " +
					"report an entity whose key values match exactly one existing entity converge on it instead of creating a duplicate.",
				ElementType: types.ListType{ElemType: types.StringType},
				Optional:    true, Computed: true,
				Default:    listdefault.StaticValue(types.ListValueMust(types.ListType{ElemType: types.StringType}, []attr.Value{})),
				Validators: []validator.List{listvalidator.SizeAtMost(8), identityKeysValidator{}},
			},
			"deprecated": schema.BoolAttribute{
				MarkdownDescription: "Mark the kind deprecated (default `false`).",
				Optional:            true, Computed: true, Default: booldefault.StaticBool(false),
			},
			"schema": schema.StringAttribute{
				CustomType: jsontypes.NormalizedType{},
				MarkdownDescription: "JSON Schema (2020-12) for `spec`, as a JSON string (use `jsonencode`). Defaults to an open object. " +
					"Compared semantically, so Deed's re-encoding (sorted keys, compact) never causes a diff. A semantic change publishes a " +
					"new version.",
				Optional: true, Computed: true,
				Validators:    []validator.String{jsonObjectValidator{}},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"breaking": schema.BoolAttribute{
				MarkdownDescription: "Publish `schema` changes as a breaking version (default `false`). Requires `migration`. Only used when " +
					"`schema` changes; it is not read back from Deed.",
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
			},
			"migration": schema.StringAttribute{
				MarkdownDescription: "Migration mode of a breaking publication: `revalidate` (refuse when an existing entity fails) or " +
					"`mark_invalid` (flag failing entities). Only used when `schema` changes with `breaking = true`.",
				Optional:   true,
				Validators: []validator.String{stringvalidator.OneOf("revalidate", "mark_invalid")},
			},
			"storage_version": schema.StringAttribute{
				MarkdownDescription: "Current storage (schema) version, e.g. `v2`.", Computed: true,
				PlanModifiers: []planmodifier.String{unknownWhenSchemaChanges{}},
			},
		},
	}
}

var groupRE = regexp.MustCompile(`^[a-z][a-z0-9.-]{0,62}$`)

var kindFields = rootMapper(map[string]string{"name": "name", "group": "group", "title": "title", "plural": "plural",
	"description": "description", "icon": "icon", "color": "color", "identity_keys": "identity_keys", "schema": "schema",
	"migration": "migration", "breaking": "breaking"})

// identityKeysValidator checks that every identity key lists 1–4 JSON pointers into spec or metadata.
type identityKeysValidator struct{}

func (identityKeysValidator) Description(context.Context) string {
	return "each key lists 1–4 JSON pointers starting with /spec/ or /metadata/"
}
func (v identityKeysValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (identityKeysValidator) ValidateList(ctx context.Context, req validator.ListRequest, resp *validator.ListResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	var keys []types.List
	resp.Diagnostics.Append(req.ConfigValue.ElementsAs(ctx, &keys, false)...)
	for i, k := range keys {
		if k.IsUnknown() || k.IsNull() {
			continue
		}
		var ptrs []types.String
		resp.Diagnostics.Append(k.ElementsAs(ctx, &ptrs, false)...)
		if len(ptrs) == 0 || len(ptrs) > 4 {
			resp.Diagnostics.AddAttributeError(req.Path.AtListIndex(i), "Invalid identity key", "Each identity key lists 1–4 JSON pointers.")
		}
		for _, p := range ptrs {
			if p.IsUnknown() || p.IsNull() {
				continue
			}
			if s := p.ValueString(); !strings.HasPrefix(s, "/spec/") && !strings.HasPrefix(s, "/metadata/") {
				resp.Diagnostics.AddAttributeError(req.Path.AtListIndex(i), "Invalid identity key",
					fmt.Sprintf("Pointer %q must start with /spec/ or /metadata/.", s))
			}
		}
	}
}

func (r *kindResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var breaking types.Bool
	var migration types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("breaking"), &breaking)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("migration"), &migration)...)
	if breaking.IsUnknown() || migration.IsUnknown() {
		return
	}
	if breaking.ValueBool() && migration.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("migration"), "Missing migration mode",
			"A breaking schema publication needs migration = \"revalidate\" or \"mark_invalid\".")
	}
	if !breaking.ValueBool() && !migration.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("migration"), "Migration without a breaking change",
			"migration applies only to breaking publications; set breaking = true or remove migration.")
	}
}

func (m *kindModel) fill(k *client.Kind) {
	m.ID = types.StringValue(k.Name)
	m.Name = types.StringValue(k.Name)
	m.Group = types.StringValue(k.Group)
	m.Title = types.StringValue(k.Title)
	m.Plural = types.StringValue(k.Plural)
	m.Description = strVal(k.Description, m.Description)
	m.Icon = strVal(k.Icon, m.Icon)
	m.Color = strVal(k.Color, m.Color)
	m.IdentityKeys = [][]string{}
	for _, key := range k.IdentityKeys {
		m.IdentityKeys = append(m.IdentityKeys, append([]string{}, key...))
	}
	m.Deprecated = types.BoolValue(k.Deprecated)
	if s := k.StorageSchema(); len(s) > 0 {
		m.Schema = jsontypes.NewNormalizedValue(string(s))
	}
	m.StorageVersion = types.StringValue(k.StorageVersion)
	if m.Breaking.IsNull() || m.Breaking.IsUnknown() {
		m.Breaking = types.BoolValue(false)
	}
}

func (m *kindModel) input() client.KindInput {
	return client.KindInput{Name: m.Name.ValueString(), Group: m.Group.ValueString(), Title: m.Title.ValueString(),
		Plural: m.Plural.ValueString(), Description: m.Description.ValueString(), Icon: m.Icon.ValueString(), Color: m.Color.ValueString(),
		IdentityKeys: m.IdentityKeys, Deprecated: m.Deprecated.ValueBool()}
}

func builtinKindError(diags *diag.Diagnostics, name string) {
	diags.AddAttributeError(path.Root("name"), "Built-in kinds cannot be managed",
		fmt.Sprintf("%q is a built-in Deed kind: its definition and schema versions ship with Deed and cannot be created, changed or "+
			"deleted. Use it directly in deed_entity.kind, or read it with the deed_kind data source.", name))
}

func (r *kindResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan kindModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	k, err := r.client.CreateKind(ctx, plan.input(), rawJSON(plan.Schema))
	if err != nil {
		if client.HasCode(err, client.CodeAlreadyExists) {
			if cur, gerr := r.client.GetKind(ctx, plan.Name.ValueString()); gerr == nil && cur.Builtin {
				builtinKindError(&resp.Diagnostics, cur.Name)
				return
			}
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Kind already exists",
				"A custom kind named "+plan.Name.ValueString()+" already exists in Deed. Import it instead of creating it:\n\n"+
					"  terraform import <address> "+plan.Name.ValueString())
			return
		}
		apiError(&resp.Diagnostics, "Cannot create kind", err, kindFields)
		return
	}
	plan.fill(k)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(saveETag(ctx, resp.Private, k.ETag)...)
}

func (r *kindResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state kindModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	k, err := r.client.GetKind(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot read kind", err, nil)
		return
	}
	if k.Builtin {
		builtinKindError(&resp.Diagnostics, k.Name)
		return
	}
	state.fill(k)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(saveETag(ctx, resp.Private, k.ETag)...)
}

func displayChanged(plan, state *kindModel) bool {
	return !plan.Group.Equal(state.Group) || !plan.Title.Equal(state.Title) || !plan.Plural.Equal(state.Plural) ||
		plan.Description.ValueString() != state.Description.ValueString() || plan.Icon.ValueString() != state.Icon.ValueString() ||
		plan.Color.ValueString() != state.Color.ValueString() || !plan.Deprecated.Equal(state.Deprecated) ||
		fmt.Sprint(plan.IdentityKeys) != fmt.Sprint(state.IdentityKeys)
}

func (r *kindResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state kindModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := state.ID.ValueString()
	etag := loadETag(ctx, req.Private)
	var cur *client.Kind
	if displayChanged(&plan, &state) {
		k, err := r.client.UpdateKind(ctx, name, etag, plan.input())
		if err != nil {
			apiError(&resp.Diagnostics, "Cannot update kind", err, kindFields)
			return
		}
		cur, etag = k, k.ETag
	}
	schemaChanged := false
	if !plan.Schema.IsNull() && !plan.Schema.IsUnknown() {
		eq, diags := plan.Schema.StringSemanticEquals(ctx, state.Schema)
		resp.Diagnostics.Append(diags...)
		schemaChanged = !eq
	}
	if schemaChanged {
		res, err := r.client.PublishKindVersion(ctx, name, etag, client.PublishInput{Schema: rawJSON(plan.Schema),
			Breaking: plan.Breaking.ValueBool(), Migration: plan.Migration.ValueString()})
		if err != nil {
			publishError(&resp.Diagnostics, err, plan.Breaking.ValueBool())
			if cur != nil {
				// The display fields were changed: record them with the previous schema, so the next plan retries only the publication.
				partial := state
				partial.fill(cur)
				resp.Diagnostics.Append(resp.State.Set(ctx, &partial)...)
				resp.Diagnostics.Append(saveETag(ctx, resp.Private, cur.ETag)...)
			}
			return
		}
		if res.Invalid > 0 {
			resp.Diagnostics.AddAttributeWarning(path.Root("schema"), "Entities marked invalid",
				fmt.Sprintf("Version %s was published; %d of %d existing %s entities do not validate against it and are flagged as invalid.",
					res.Version, res.Invalid, res.Revalidated, name))
		}
		if res.Kind != nil {
			cur = res.Kind
		}
	}
	if cur == nil {
		// Only breaking/migration (or the schema's formatting) changed: nothing to send.
		plan.StorageVersion = state.StorageVersion
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}
	plan.fill(cur)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(saveETag(ctx, resp.Private, cur.ETag)...)
}

// publishError reports a refused schema publication, listing each compatibility problem on the schema attribute.
func publishError(diags *diag.Diagnostics, err error, breaking bool) {
	ae, ok := client.AsAPIError(err)
	if !ok || ae.StatusCode != http.StatusConflict {
		apiError(diags, "Cannot publish schema version", err, kindFields)
		return
	}
	var probs []string
	for _, f := range ae.Fields {
		probs = append(probs, "- "+f.Message)
	}
	detail := ae.Detail
	if len(probs) > 0 {
		detail += "\n\nCompatibility problems:\n" + strings.Join(probs, "\n")
	}
	if !breaking {
		detail += "\n\nTo publish an incompatible schema set breaking = true and migration = \"revalidate\" (refused if an existing entity " +
			"fails validation) or \"mark_invalid\" (failing entities are flagged, never dropped)."
	}
	diags.AddAttributeError(path.Root("schema"), "Schema change was refused by Deed", detail)
}

func (r *kindResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state kindModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := state.ID.ValueString()
	err := r.client.DeleteKind(ctx, name)
	if err == nil || client.IsNotFound(err) {
		return
	}
	apiError(&resp.Diagnostics, "Cannot delete kind", err, nil)
}

func (r *kindResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	k, err := r.client.GetKind(ctx, req.ID)
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot import kind", err, nil)
		return
	}
	if k.Builtin {
		builtinKindError(&resp.Diagnostics, k.Name)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), k.Name)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("breaking"), false)...)
}
