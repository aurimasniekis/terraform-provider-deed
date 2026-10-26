package provider

import (
	"context"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/aurimasniekis/terraform-provider-deed/internal/client"
)

func dsString(desc string) schema.StringAttribute {
	return schema.StringAttribute{MarkdownDescription: desc, Computed: true}
}
func dsBool(desc string) schema.BoolAttribute {
	return schema.BoolAttribute{MarkdownDescription: desc, Computed: true}
}
func dsJSON(desc string) schema.StringAttribute {
	return schema.StringAttribute{MarkdownDescription: desc, Computed: true, CustomType: jsontypes.NormalizedType{}}
}
func dsStrings(desc string) schema.ListAttribute {
	return schema.ListAttribute{MarkdownDescription: desc, Computed: true, ElementType: types.StringType}
}
func dsMap(desc string) schema.MapAttribute {
	return schema.MapAttribute{MarkdownDescription: desc, Computed: true, ElementType: types.StringType}
}

func nonNilStrings(ss []string) []string {
	if ss == nil {
		return []string{}
	}
	return ss
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func rawOrEmpty(raw []byte) jsontypes.Normalized {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		s = "{}"
	}
	return jsontypes.NewNormalizedValue(s)
}

// ---------------------------------------------------------------------------------------------------------------------
// deed_project

var _ datasource.DataSourceWithConfigure = (*projectDataSource)(nil)

type projectDataSource struct{ baseDataSource }

// NewProjectDataSource returns the deed_project data source.
func NewProjectDataSource() datasource.DataSource { return &projectDataSource{} }

type projectDSModel struct {
	ID             types.String `tfsdk:"id"`
	Name           types.String `tfsdk:"name"`
	DisplayName    types.String `tfsdk:"display_name"`
	Description    types.String `tfsdk:"description"`
	AllowHTTPLinks types.Bool   `tfsdk:"allow_http_links"`
	Synthetic      types.Bool   `tfsdk:"synthetic"`
	Role           types.String `tfsdk:"role"`
}

func (d *projectDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

func (d *projectDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads a Deed project by name or ID (any project the token can read).",
		Attributes: map[string]schema.Attribute{
			"id":               schema.StringAttribute{MarkdownDescription: "Project ID (`prj_…`).", Optional: true, Computed: true},
			"name":             schema.StringAttribute{MarkdownDescription: "Project name.", Optional: true, Computed: true},
			"display_name":     dsString("Display name."),
			"description":      dsString("Description."),
			"allow_http_links": dsBool("Whether plain http:// links are allowed."),
			"synthetic":        dsBool("Whether the project is synthetic (created by Deed for imported data)."),
			"role":             dsString("The token's effective role in the project: `viewer`, `editor` or `admin`."),
		},
	}
}

func (d *projectDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name"))}
}

func (d *projectDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m projectDSModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ref := m.ID.ValueString()
	if ref == "" {
		ref = m.Name.ValueString()
	}
	pr, err := d.client.GetProject(ctx, ref)
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot read project "+ref, err, nil)
		return
	}
	m = projectDSModel{ID: types.StringValue(pr.ID), Name: types.StringValue(pr.Name), DisplayName: types.StringValue(pr.DisplayName),
		Description: types.StringValue(pr.Description), AllowHTTPLinks: types.BoolValue(pr.AllowHTTPLinks), Synthetic: types.BoolValue(pr.Synthetic),
		Role: types.StringValue(pr.Role)}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------------------------------------------------
// deed_kind

var _ datasource.DataSourceWithConfigure = (*kindDataSource)(nil)

type kindDataSource struct{ baseDataSource }

// NewKindDataSource returns the deed_kind data source.
func NewKindDataSource() datasource.DataSource { return &kindDataSource{} }

type kindVersionDSModel struct {
	Version   types.String         `tfsdk:"version"`
	Breaking  types.Bool           `tfsdk:"breaking"`
	Migration types.String         `tfsdk:"migration"`
	Schema    jsontypes.Normalized `tfsdk:"schema"`
}

type kindSupportDSModel struct {
	Modeling  types.Bool `tfsdk:"modeling"`
	Terraform []string   `tfsdk:"terraform"`
	Runtime   []string   `tfsdk:"runtime"`
	Native    []string   `tfsdk:"native"`
}

type kindDSModel struct {
	ID             types.String         `tfsdk:"id"`
	Name           types.String         `tfsdk:"name"`
	Group          types.String         `tfsdk:"group"`
	Builtin        types.Bool           `tfsdk:"builtin"`
	Title          types.String         `tfsdk:"title"`
	Plural         types.String         `tfsdk:"plural"`
	Description    types.String         `tfsdk:"description"`
	Icon           types.String         `tfsdk:"icon"`
	Color          types.String         `tfsdk:"color"`
	IdentityKeys   [][]string           `tfsdk:"identity_keys"`
	Deprecated     types.Bool           `tfsdk:"deprecated"`
	Grouping       []string             `tfsdk:"grouping_relationship_types"`
	StorageVersion types.String         `tfsdk:"storage_version"`
	Schema         jsontypes.Normalized `tfsdk:"schema"`
	Versions       []kindVersionDSModel `tfsdk:"versions"`
	Support        *kindSupportDSModel  `tfsdk:"support"`
}

func (d *kindDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kind"
}

func (d *kindDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads a built-in or custom Deed kind, including every published schema version and the computed support " +
			"levels (which Terraform resource types, runtime paths and native providers feed it).",
		Attributes: map[string]schema.Attribute{
			"id":    dsString("The kind name."),
			"name":  schema.StringAttribute{MarkdownDescription: "Kind name, e.g. `server` or `example.com/ups`.", Required: true},
			"group": dsString("Catalog group."), "builtin": dsBool("Whether the kind ships with Deed."),
			"title": dsString("Singular display name."), "plural": dsString("Plural display name."),
			"description": dsString("Description."), "icon": dsString("Icon (`pack:name`)."), "color": dsString("Color (`#rrggbb`)."),
			"identity_keys": schema.ListAttribute{MarkdownDescription: "Identity keys (lists of JSON pointers).", Computed: true,
				ElementType: types.ListType{ElemType: types.StringType}},
			"deprecated": dsBool("Whether the kind is deprecated."),
			"grouping_relationship_types": schema.SetAttribute{MarkdownDescription: "Map grouping: relationship types followed to an " +
				"entity of this kind (empty when the kind is not a map grouping).", ElementType: types.StringType, Computed: true},
			"storage_version": dsString("Current storage version, e.g. `v2`."),
			"schema":          dsJSON("JSON Schema of the storage version."),
			"versions": schema.ListNestedAttribute{
				MarkdownDescription: "Every published schema version, oldest first.", Computed: true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"version": dsString("Version, e.g. `v1`."), "breaking": dsBool("Whether it was published as breaking."),
					"migration": dsString("Migration mode of a breaking version."), "schema": dsJSON("JSON Schema."),
				}},
			},
			"support": schema.SingleNestedAttribute{
				MarkdownDescription: "What Deed can do for this kind.", Computed: true,
				Attributes: map[string]schema.Attribute{
					"modeling":  dsBool("Entities of the kind can be modeled."),
					"terraform": dsStrings("Terraform resource types mapped to the kind by the state importer."),
					"runtime":   dsStrings("Runtime ingestion paths."),
					"native":    dsStrings("Native in-process providers."),
				},
			},
		},
	}
}

func (d *kindDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m kindDSModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	k, err := d.client.GetKind(ctx, m.Name.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot read kind "+m.Name.ValueString(), err, nil)
		return
	}
	m = kindDSModel{ID: types.StringValue(k.Name), Name: types.StringValue(k.Name), Group: types.StringValue(k.Group),
		Builtin: types.BoolValue(k.Builtin), Title: types.StringValue(k.Title), Plural: types.StringValue(k.Plural),
		Description: types.StringValue(k.Description), Icon: types.StringValue(k.Icon), Color: types.StringValue(k.Color),
		IdentityKeys: [][]string{}, Deprecated: types.BoolValue(k.Deprecated), StorageVersion: types.StringValue(k.StorageVersion),
		Grouping: []string{},
		Schema:   rawOrEmpty(k.StorageSchema()), Versions: []kindVersionDSModel{},
		Support: &kindSupportDSModel{Modeling: types.BoolValue(k.Support.Modeling), Terraform: nonNilStrings(k.Support.Terraform),
			Runtime: nonNilStrings(k.Support.Runtime), Native: nonNilStrings(k.Support.Native)}}
	for _, key := range k.IdentityKeys {
		m.IdentityKeys = append(m.IdentityKeys, nonNilStrings(key))
	}
	if k.Grouping != nil {
		m.Grouping = nonNilStrings(k.Grouping.RelationshipTypes)
	}
	for _, v := range k.Versions {
		m.Versions = append(m.Versions, kindVersionDSModel{Version: types.StringValue(v.Version), Breaking: types.BoolValue(v.Breaking),
			Migration: types.StringValue(v.Migration), Schema: rawOrEmpty(v.Schema)})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------------------------------------------------
// deed_relationship_type

var _ datasource.DataSourceWithConfigure = (*relTypeDataSource)(nil)

type relTypeDataSource struct{ baseDataSource }

// NewRelationshipTypeDataSource returns the deed_relationship_type data source.
func NewRelationshipTypeDataSource() datasource.DataSource { return &relTypeDataSource{} }

type relTypeDSModel struct {
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
	Builtin          types.Bool           `tfsdk:"builtin"`
}

func (d *relTypeDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_relationship_type"
}

func (d *relTypeDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads a built-in or custom Deed relationship type.",
		Attributes: map[string]schema.Attribute{
			"id":           dsString("The type name."),
			"name":         schema.StringAttribute{MarkdownDescription: "Type name, e.g. `runs_on`.", Required: true},
			"inverse_name": dsString("Inverse name."), "title": dsString("Display name."), "description": dsString("Description."),
			"directed":   dsBool("Whether the relationship is directed."),
			"from_kinds": dsStrings("Kinds allowed as source (empty = any)."), "to_kinds": dsStrings("Kinds allowed as target (empty = any)."),
			"default_evidence": dsString("Default evidence class."), "cardinality": dsString("`one` or `many`."),
			"attributes_schema": dsJSON("JSON Schema of relationship attributes (`{}` when none)."),
			"impact":            dsString("Impact propagation."), "color": dsString("Color."), "builtin": dsBool("Whether the type ships with Deed."),
		},
	}
}

func (d *relTypeDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m relTypeDSModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rt, err := d.client.GetRelType(ctx, m.Name.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot read relationship type "+m.Name.ValueString(), err, nil)
		return
	}
	m = relTypeDSModel{ID: types.StringValue(rt.Name), Name: types.StringValue(rt.Name), InverseName: types.StringValue(rt.InverseName),
		Title: types.StringValue(rt.Title), Description: types.StringValue(rt.Description), Directed: types.BoolValue(rt.Directed),
		FromKinds: nonNilStrings(rt.FromKinds), ToKinds: nonNilStrings(rt.ToKinds), DefaultEvidence: types.StringValue(rt.DefaultEvidence),
		Cardinality: types.StringValue(rt.Cardinality), AttributesSchema: rawOrEmpty(rt.AttributesSchema), Impact: types.StringValue(rt.Impact),
		Color: types.StringValue(rt.Color), Builtin: types.BoolValue(rt.Builtin)}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------------------------------------------------
// deed_entity

var _ datasource.DataSourceWithConfigure = (*entityDataSource)(nil)

type entityDataSource struct{ baseDataSource }

// NewEntityDataSource returns the deed_entity data source.
func NewEntityDataSource() datasource.DataSource { return &entityDataSource{} }

type entityDSLink struct {
	Type  types.String `tfsdk:"type"`
	Title types.String `tfsdk:"title"`
	URL   types.String `tfsdk:"url"`
}

type entityDSModel struct {
	ID          types.String         `tfsdk:"id"`
	Project     types.String         `tfsdk:"project"`
	ProjectID   types.String         `tfsdk:"project_id"`
	Kind        types.String         `tfsdk:"kind"`
	Name        types.String         `tfsdk:"name"`
	DisplayName types.String         `tfsdk:"display_name"`
	Description types.String         `tfsdk:"description"`
	Environment types.String         `tfsdk:"environment"`
	Owner       types.String         `tfsdk:"owner"`
	Lifecycle   types.String         `tfsdk:"lifecycle_stage"`
	Icon        types.String         `tfsdk:"icon"`
	Labels      map[string]string    `tfsdk:"labels"`
	Annotations map[string]string    `tfsdk:"annotations"`
	Links       []entityDSLink       `tfsdk:"links"`
	Tags        []string             `tfsdk:"tags"`
	Version     map[string]string    `tfsdk:"version"`
	Aliases     []string             `tfsdk:"aliases"`
	Spec        jsontypes.Normalized `tfsdk:"spec"`
	Status      jsontypes.Normalized `tfsdk:"status"`
	Health      types.String         `tfsdk:"health"`
	Freshness   types.String         `tfsdk:"freshness"`
	Valid       types.Bool           `tfsdk:"valid"`
	Declared    types.Bool           `tfsdk:"declared"`
	Facets      types.Int64          `tfsdk:"facets"`
}

func (d *entityDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_entity"
}

func (d *entityDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads the **effective view** of an entity: every source's facets merged (declarations, imports, runtime " +
			"observations, annotations), including the computed status (health, freshness, validity). Look it up by `id`, or by " +
			"`project`, `kind` and `name` (aliases of renamed entities included). Unlike the `deed_entity` resource, which holds only " +
			"the provider's declaration, these values change as other sources report.",
		Attributes: map[string]schema.Attribute{
			"id":           schema.StringAttribute{MarkdownDescription: "Entity ID (`ent_…`).", Optional: true, Computed: true},
			"project":      schema.StringAttribute{MarkdownDescription: "Project name or ID (with `kind` and `name`).", Optional: true, Computed: true},
			"project_id":   dsString("Project ID."),
			"kind":         schema.StringAttribute{MarkdownDescription: "Kind (with `project` and `name`).", Optional: true, Computed: true},
			"name":         schema.StringAttribute{MarkdownDescription: "Entity name (with `project` and `kind`).", Optional: true, Computed: true},
			"display_name": dsString("Effective display name."), "description": dsString("Effective description."),
			"environment": dsString("Effective environment."), "owner": dsString("Effective owner."),
			"lifecycle_stage": dsString("Effective lifecycle stage."), "icon": dsString("Effective icon."),
			"labels": dsMap("Effective labels."), "annotations": dsMap("Effective annotations."),
			"links": schema.ListNestedAttribute{MarkdownDescription: "Effective links.", Computed: true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"type": dsString("Link type."), "title": dsString("Title."), "url": dsString("URL."),
				}}},
			"tags":      dsStrings("Effective tags."),
			"version":   dsMap("Version information (`declared`, `observed`, `available`, `policy`, `channel`, `product`)."),
			"aliases":   dsStrings("Former names (after renames)."),
			"spec":      dsJSON("Effective spec as JSON."),
			"status":    dsJSON("Computed status as JSON: health, freshness, observed values, validation, facet and conflict counts, findings."),
			"health":    dsString("Health state: `ok`, `warning`, `failed` or `unknown`."),
			"freshness": dsString("Freshness state: `fresh`, `aging`, `stale` or `unknown`."),
			"valid":     dsBool("Whether the spec validates against the kind's storage schema."),
			"declared":  dsBool("Whether any declaring source (API, Terraform, …) declares the entity."),
			"facets":    schema.Int64Attribute{MarkdownDescription: "Number of live facets (contributing sources).", Computed: true},
		},
	}
}

func (d *entityDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name")),
		datasourcevalidator.RequiredTogether(path.MatchRoot("project"), path.MatchRoot("kind"), path.MatchRoot("name")),
		datasourcevalidator.Conflicting(path.MatchRoot("id"), path.MatchRoot("project")),
		datasourcevalidator.Conflicting(path.MatchRoot("id"), path.MatchRoot("kind")),
	}
}

func (d *entityDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m entityDSModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var e *client.Entity
	var err error
	if !m.ID.IsNull() {
		e, err = d.client.GetEntity(ctx, m.ID.ValueString())
	} else {
		e, err = d.client.LookupEntity(ctx, m.Project.ValueString(), m.Kind.ValueString(), m.Name.ValueString())
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot read entity", err, nil)
		return
	}
	md := e.Metadata
	st := e.ParsedStatus()
	project := m.Project
	if project.IsNull() {
		project = types.StringValue(md.Project)
	}
	out := entityDSModel{ID: types.StringValue(md.ID), Project: project, ProjectID: types.StringValue(md.ProjectID),
		Kind: types.StringValue(e.Kind), Name: types.StringValue(md.Name), DisplayName: types.StringValue(md.DisplayName),
		Description: types.StringValue(md.Description), Environment: types.StringValue(md.Environment), Owner: types.StringValue(md.Owner),
		Lifecycle: types.StringValue(md.Lifecycle), Icon: types.StringValue(md.Icon), Labels: nonNilMap(md.Labels),
		Annotations: nonNilMap(md.Annotations), Links: []entityDSLink{}, Tags: nonNilStrings(md.Tags), Version: nonNilMap(md.Version),
		Aliases: nonNilStrings(md.Aliases), Spec: rawOrEmpty(e.Spec), Status: rawOrEmpty(e.Status), Health: types.StringValue(st.Health.State),
		Freshness: types.StringValue(st.Freshness.State), Valid: types.BoolValue(st.Valid), Declared: types.BoolValue(st.Declared),
		Facets: types.Int64Value(int64(st.Facets))}
	if !m.Name.IsNull() {
		// Keep the looked-up name (it may be an alias of a renamed entity).
		out.Name = m.Name
	}
	for _, l := range md.Links {
		out.Links = append(out.Links, entityDSLink{Type: types.StringValue(l.Type), Title: types.StringValue(l.Title), URL: types.StringValue(l.URL)})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &out)...)
}

// ---------------------------------------------------------------------------------------------------------------------
// deed_entities

var _ datasource.DataSourceWithConfigure = (*entitiesDataSource)(nil)

type entitiesDataSource struct{ baseDataSource }

// NewEntitiesDataSource returns the deed_entities data source.
func NewEntitiesDataSource() datasource.DataSource { return &entitiesDataSource{} }

type entitySummary struct {
	ID          types.String      `tfsdk:"id"`
	Project     types.String      `tfsdk:"project"`
	ProjectID   types.String      `tfsdk:"project_id"`
	Kind        types.String      `tfsdk:"kind"`
	Name        types.String      `tfsdk:"name"`
	DisplayName types.String      `tfsdk:"display_name"`
	Environment types.String      `tfsdk:"environment"`
	Owner       types.String      `tfsdk:"owner"`
	Lifecycle   types.String      `tfsdk:"lifecycle_stage"`
	Labels      map[string]string `tfsdk:"labels"`
	Health      types.String      `tfsdk:"health"`
	Freshness   types.String      `tfsdk:"freshness"`
}

type entitiesDSModel struct {
	ID       types.String      `tfsdk:"id"`
	Project  types.String      `tfsdk:"project"`
	Kind     types.String      `tfsdk:"kind"`
	Labels   map[string]string `tfsdk:"labels"`
	Query    types.String      `tfsdk:"q"`
	Limit    types.Int64       `tfsdk:"limit"`
	IDs      []string          `tfsdk:"ids"`
	Entities []entitySummary   `tfsdk:"entities"`
}

func (d *entitiesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_entities"
}

func (d *entitiesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Searches entities (effective view) the token can read, ordered by name. All filters are optional and " +
			"combined with AND.",
		Attributes: map[string]schema.Attribute{
			"id":      dsString("Identifier of this search."),
			"project": schema.StringAttribute{MarkdownDescription: "Only entities in this project (name or ID).", Optional: true},
			"kind":    schema.StringAttribute{MarkdownDescription: "Only entities of this kind.", Optional: true},
			"labels": schema.MapAttribute{MarkdownDescription: "Only entities carrying every one of these labels.", Optional: true,
				ElementType: types.StringType},
			"q": schema.StringAttribute{MarkdownDescription: "Full-text search (names, descriptions, labels, spec values, external IDs).",
				Optional: true},
			"limit": schema.Int64Attribute{MarkdownDescription: "Maximum number of entities to return (default: all).", Optional: true,
				Validators: []validator.Int64{int64validator.AtLeast(1)}},
			"ids": dsStrings("IDs of the matching entities."),
			"entities": schema.ListNestedAttribute{
				MarkdownDescription: "Matching entities.", Computed: true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id": dsString("Entity ID."), "project": dsString("Project name."), "project_id": dsString("Project ID."),
					"kind": dsString("Kind."), "name": dsString("Name."), "display_name": dsString("Display name."),
					"environment": dsString("Environment."), "owner": dsString("Owner."), "lifecycle_stage": dsString("Lifecycle stage."),
					"labels": dsMap("Labels."), "health": dsString("Health state."), "freshness": dsString("Freshness state."),
				}},
			},
		},
	}
}

func (d *entitiesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m entitiesDSModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	f := client.EntityFilter{Labels: m.Labels, Query: m.Query.ValueString(), Limit: int(m.Limit.ValueInt64())}
	if v := m.Project.ValueString(); v != "" {
		f.Projects = []string{v}
	}
	if v := m.Kind.ValueString(); v != "" {
		f.Kinds = []string{v}
	}
	es, err := d.client.ListEntities(ctx, f)
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot list entities", err, nil)
		return
	}
	m.IDs = []string{}
	m.Entities = []entitySummary{}
	for _, e := range es {
		st := e.ParsedStatus()
		md := e.Metadata
		m.IDs = append(m.IDs, md.ID)
		m.Entities = append(m.Entities, entitySummary{ID: types.StringValue(md.ID), Project: types.StringValue(md.Project),
			ProjectID: types.StringValue(md.ProjectID), Kind: types.StringValue(e.Kind), Name: types.StringValue(md.Name),
			DisplayName: types.StringValue(md.DisplayName), Environment: types.StringValue(md.Environment), Owner: types.StringValue(md.Owner),
			Lifecycle: types.StringValue(md.Lifecycle), Labels: nonNilMap(md.Labels), Health: types.StringValue(st.Health.State),
			Freshness: types.StringValue(st.Freshness.State)})
	}
	parts := []string{"project=" + m.Project.ValueString(), "kind=" + m.Kind.ValueString(), "q=" + m.Query.ValueString()}
	for _, k := range sortedKeys(m.Labels) {
		parts = append(parts, "label="+k+"="+m.Labels[k])
	}
	m.ID = types.StringValue(strings.Join(parts, "&"))
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------------------------------------------------
// deed_relationship

var _ datasource.DataSourceWithConfigure = (*relationshipDataSource)(nil)

type relationshipDataSource struct{ baseDataSource }

// NewRelationshipDataSource returns the deed_relationship data source.
func NewRelationshipDataSource() datasource.DataSource { return &relationshipDataSource{} }

type relEndpointDSModel struct {
	ID        types.String `tfsdk:"id"`
	ProjectID types.String `tfsdk:"project_id"`
	Kind      types.String `tfsdk:"kind"`
	Name      types.String `tfsdk:"name"`
}

type relationshipDSModel struct {
	ID            types.String         `tfsdk:"id"`
	Type          types.String         `tfsdk:"type"`
	FromID        types.String         `tfsdk:"from_id"`
	ToID          types.String         `tfsdk:"to_id"`
	ProjectID     types.String         `tfsdk:"project_id"`
	From          *relEndpointDSModel  `tfsdk:"from"`
	To            *relEndpointDSModel  `tfsdk:"to"`
	Attributes    jsontypes.Normalized `tfsdk:"attributes"`
	EvidenceClass types.String         `tfsdk:"evidence_class"`
	Live          types.Bool           `tfsdk:"live"`
	CrossProject  types.Bool           `tfsdk:"cross_project"`
	Sources       []string             `tfsdk:"sources"`
}

func (d *relationshipDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_relationship"
}

func relEndpointDS(desc string) schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{MarkdownDescription: desc, Computed: true, Attributes: map[string]schema.Attribute{
		"id": dsString("Entity ID."), "project_id": dsString("Project ID."), "kind": dsString("Kind."), "name": dsString("Name."),
	}}
}

func (d *relationshipDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads a relationship (merged over every asserting source) by `id`, or by `type`, `from_id` and `to_id`.",
		Attributes: map[string]schema.Attribute{
			"id":             schema.StringAttribute{MarkdownDescription: "Relationship ID (`rel_…`).", Optional: true, Computed: true},
			"type":           schema.StringAttribute{MarkdownDescription: "Relationship type (with `from_id` and `to_id`).", Optional: true, Computed: true},
			"from_id":        schema.StringAttribute{MarkdownDescription: "Source entity ID (with `type` and `to_id`).", Optional: true, Computed: true},
			"to_id":          schema.StringAttribute{MarkdownDescription: "Target entity ID (with `type` and `from_id`).", Optional: true, Computed: true},
			"project_id":     dsString("Project ID of the relationship."),
			"from":           relEndpointDS("Source endpoint."),
			"to":             relEndpointDS("Target endpoint."),
			"attributes":     dsJSON("Merged attributes as JSON."),
			"evidence_class": dsString("Strongest evidence class: `declared`, `structural`, `observed` or `inferred`."),
			"live":           dsBool("Whether at least one source currently asserts the relationship."),
			"cross_project":  dsBool("Whether the endpoints are in different projects."),
			"sources":        dsStrings("Names of the sources that currently assert the relationship."),
		},
	}
}

func (d *relationshipDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("type")),
		datasourcevalidator.RequiredTogether(path.MatchRoot("type"), path.MatchRoot("from_id"), path.MatchRoot("to_id")),
	}
}

func (d *relationshipDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m relationshipDSModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var rel *client.Relation
	if !m.ID.IsNull() {
		r, err := d.client.GetRelation(ctx, m.ID.ValueString())
		if err != nil {
			apiError(&resp.Diagnostics, "Cannot read relationship", err, nil)
			return
		}
		rel = r
	} else {
		rs, err := d.client.ListRelations(ctx, client.RelationFilter{Entity: m.FromID.ValueString(), Direction: "out",
			Types: []string{m.Type.ValueString()}})
		if err != nil {
			apiError(&resp.Diagnostics, "Cannot list relationships", err, nil)
			return
		}
		for i := range rs {
			if rs[i].To.ID == m.ToID.ValueString() && rs[i].From.ID == m.FromID.ValueString() {
				full, err := d.client.GetRelation(ctx, rs[i].ID)
				if err != nil {
					apiError(&resp.Diagnostics, "Cannot read relationship", err, nil)
					return
				}
				rel = full
				break
			}
		}
		if rel == nil {
			resp.Diagnostics.AddError("Relationship not found", "No live "+m.Type.ValueString()+" relationship from "+m.FromID.ValueString()+
				" to "+m.ToID.ValueString()+".")
			return
		}
	}
	ep := func(r client.EntityRef) *relEndpointDSModel {
		return &relEndpointDSModel{ID: types.StringValue(r.ID), ProjectID: types.StringValue(r.ProjectID), Kind: types.StringValue(r.Kind),
			Name: types.StringValue(r.Name)}
	}
	sources := []string{}
	for _, a := range rel.Assertions {
		if a.Live && !slices.Contains(sources, a.Source) {
			sources = append(sources, a.Source)
		}
	}
	slices.Sort(sources)
	out := relationshipDSModel{ID: types.StringValue(rel.ID), Type: types.StringValue(rel.Type), FromID: types.StringValue(rel.From.ID),
		ToID: types.StringValue(rel.To.ID), ProjectID: types.StringValue(rel.ProjectID), From: ep(rel.From), To: ep(rel.To),
		Attributes: rawOrEmpty(rel.Attributes), EvidenceClass: types.StringValue(rel.EvidenceClass), Live: types.BoolValue(rel.Live),
		CrossProject: types.BoolValue(rel.CrossProject), Sources: sources}
	resp.Diagnostics.Append(resp.State.Set(ctx, &out)...)
}
