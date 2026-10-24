package provider

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
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
	_ resource.ResourceWithImportState  = (*entityResource)(nil)
	_ resource.ResourceWithConfigure    = (*entityResource)(nil)
	_ resource.ResourceWithUpgradeState = (*entityResource)(nil)
)

const defaultEntityTimeout = 5 * time.Minute

type entityResource struct{ baseResource }

// NewEntityResource returns the deed_entity resource.
func NewEntityResource() resource.Resource { return &entityResource{} }

type linkModel struct {
	Type  types.String `tfsdk:"type"`
	Title types.String `tfsdk:"title"`
	URL   types.String `tfsdk:"url"`
}

type entityModel struct {
	ID              types.String         `tfsdk:"id"`
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
	Timeouts        timeouts.Value       `tfsdk:"timeouts"`
}

func (r *entityResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_entity"
}

func optionalString(desc string, v ...validator.String) schema.StringAttribute {
	return schema.StringAttribute{MarkdownDescription: desc, Optional: true, Validators: v}
}

func (r *entityResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	labelKey := stringvalidator.RegexMatches(labelKeyRE, "must be a label key such as team or example.com/team")
	resp.Schema = schema.Schema{
		Version: stateSchemaVersion,
		MarkdownDescription: "An entity in the Deed catalog, **declared through the provider's source** (`terraform` by default, see the " +
			"provider's `source_name` argument).\n\n" +
			"Deed merges the facets of every source that knows an entity — declarations, importers, runtime observations and UI " +
			"annotations — into an effective view. This resource manages only the provider's own declaration: its state is read from " +
			"`GET /entities/{id}/declarations/{source}` and never from the merged view, so values contributed by other sources (and " +
			"observed health or freshness) never cause a diff. Destroying the resource removes only this declaration; the entity " +
			"survives while other sources still contribute to it. Read the effective view with the `deed_entity` data source.\n\n" +
			"If the entity already exists because another source reported it, creating the resource adopts it (the declaration is " +
			"added). If the provider's source already declares it — for example from another Terraform configuration — the apply " +
			"fails and asks you to `terraform import` it instead.\n\n" +
			"Entities cannot be renamed through Terraform: changing `name`, `kind` or `project` replaces the declaration.",
		Attributes: map[string]schema.Attribute{
			"id": idAttribute("Entity ID (`ent_…`)."),
			"project": schema.StringAttribute{
				MarkdownDescription: "Project name or ID. Changing it forces a new resource (the same project by name or ID does not). " +
					"Import records the project name.",
				Required:      true,
				PlanModifiers: []planmodifier.String{sameProjectReplace("project_id")},
				Validators:    []validator.String{stringvalidator.LengthBetween(1, 64)},
			},
			"project_id": computedString("Project ID (`prj_…`)."),
			"kind": requiredImmutableString("Kind, built-in (`server`, `service`, `repository`, …) or custom (`deed_kind`).",
				stringvalidator.RegexMatches(kindNameRE, "must be a kind name")),
			"name": requiredImmutableString("Entity name, unique per project and kind: a lowercase slug (a-z, 0-9, '.', '_', '-').",
				stringvalidator.RegexMatches(entityNameRE, "must be a lowercase slug (a-z, 0-9, '.', '_', '-')")),
			"display_name": optionalString("Human-friendly name.", stringvalidator.LengthAtMost(256)),
			"description":  optionalString("Description (Markdown).", stringvalidator.LengthAtMost(8192)),
			"environment": optionalString("Environment slug, e.g. `production` or `homelab`.",
				stringvalidator.RegexMatches(envRE, "must be a lowercase slug")),
			"owner": optionalString("Owning group: a `group` entity ID or name.",
				stringvalidator.LengthBetween(1, 300)),
			"lifecycle_stage": optionalString("Lifecycle stage: `planned`, `active`, `deprecated` or `retired`.",
				stringvalidator.OneOf("planned", "active", "deprecated", "retired")),
			"icon": optionalString("Icon as `pack:name` (`tabler`, `simple-icons`, `thesvg` or `custom`), e.g. `simple-icons:proxmox`.",
				stringvalidator.RegexMatches(iconRE, "must be pack:name (tabler, simple-icons, thesvg or custom)")),
			"labels": schema.MapAttribute{
				MarkdownDescription: "Labels (at most 64; values up to 256 bytes) used for filtering and grouping. Keys that look like " +
					"secrets are refused by Deed.",
				ElementType: types.StringType, Optional: true,
				Validators: []validator.Map{mapvalidator.SizeAtMost(64), mapvalidator.KeysAre(labelKey),
					mapvalidator.ValueStringsAre(stringvalidator.LengthAtMost(256))},
			},
			"annotations": schema.MapAttribute{
				MarkdownDescription: "Annotations (at most 64; values up to 4096 bytes): free-form notes that are not used for filtering.",
				ElementType:         types.StringType, Optional: true,
				Validators: []validator.Map{mapvalidator.SizeAtMost(64), mapvalidator.KeysAre(labelKey),
					mapvalidator.ValueStringsAre(stringvalidator.LengthAtMost(4096))},
			},
			"links": schema.ListNestedAttribute{
				MarkdownDescription: "Typed external links (at most 32), e.g. the repository, a dashboard or the runbook.",
				Optional:            true,
				Validators:          []validator.List{listvalidator.SizeAtMost(32)},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"type": schema.StringAttribute{
						MarkdownDescription: "`repository`, `dashboard`, `logs`, `docs`, `runbook`, `metrics`, `alerts`, `ticket` or `other`.",
						Required:            true,
						Validators: []validator.String{stringvalidator.OneOf("repository", "dashboard", "logs", "docs", "runbook", "metrics",
							"alerts", "ticket", "other")},
					},
					"title": optionalString("Link title.", stringvalidator.LengthAtMost(200)),
					"url": schema.StringAttribute{
						MarkdownDescription: "Absolute `https://` URL (`http://` only when the project allows http links).",
						Required:            true,
						Validators: []validator.String{stringvalidator.LengthBetween(1, 2048),
							stringvalidator.RegexMatches(regexp.MustCompile(`^https?://`), "must be an absolute http(s) URL")},
					},
				}},
			},
			"tags": schema.SetAttribute{
				MarkdownDescription: "Tags (at most 64, each 1–64 bytes).", ElementType: types.StringType, Optional: true,
				Validators: []validator.Set{setvalidator.SizeAtMost(64), setvalidator.ValueStringsAre(stringvalidator.LengthBetween(1, 64))},
			},
			"version_declared": optionalString("Declared version of the software, e.g. `1.27.3`.", stringvalidator.LengthAtMost(128)),
			"version_policy":   optionalString("Version policy, e.g. `pinned` or `latest-minor`.", stringvalidator.LengthAtMost(128)),
			"version_channel":  optionalString("Release channel, e.g. `stable`.", stringvalidator.LengthAtMost(128)),
			"version_product":  optionalString("Product the version refers to (for upstream release tracking).", stringvalidator.LengthAtMost(128)),
			"spec": schema.StringAttribute{
				CustomType: jsontypes.NormalizedType{},
				MarkdownDescription: "Kind-specific properties as a JSON object string (use `jsonencode`). Deed validates it server-side " +
					"against the kind's storage schema (JSON Schema 2020-12), so the provider does not model custom-kind properties as " +
					"attributes. Deed stores the document re-encoded (sorted keys, compact); the value is compared semantically, so that " +
					"never causes a diff (reformatting the document in the configuration shows one in-place update that changes nothing " +
					"in Deed). Values whose keys " +
					"look like secrets must be secret references (`{ secretRef = { store = \"vault\", key = \"…\" } }`).",
				Optional:   true,
				Validators: []validator.String{jsonObjectValidator{}},
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true, Read: true, Update: true, Delete: true}),
		},
	}
}

// entityFields maps Deed problem fields (`metadata.labels.team`, `metadata.links[0].url`, `spec/port`) to attributes.
func entityFields(field string) (path.Path, bool) {
	f := strings.TrimPrefix(field, "body.")
	switch {
	case f == "spec" || strings.HasPrefix(f, "spec/") || strings.HasPrefix(f, "spec."):
		return path.Root("spec"), true
	case f == "kind" || f == "name" || f == "project":
		return path.Root(f), true
	}
	rest, ok := strings.CutPrefix(f, "metadata.")
	if !ok {
		return path.Empty(), false
	}
	head, tail, _ := strings.Cut(rest, ".")
	switch head {
	case "displayName":
		return path.Root("display_name"), true
	case "lifecycle":
		return path.Root("lifecycle_stage"), true
	case "description", "environment", "owner", "icon":
		return path.Root(head), true
	case "labels", "annotations":
		if tail != "" {
			return path.Root(head).AtMapKey(tail), true
		}
		return path.Root(head), true
	case "version":
		switch tail {
		case "declared", "policy", "channel", "product":
			return path.Root("version_" + tail), true
		}
		return path.Root("version_declared"), true
	}
	for _, list := range []string{"links", "tags"} {
		if head == list {
			return path.Root(list), true
		}
		if idxs, ok := strings.CutPrefix(rest, list+"["); ok {
			end := strings.Index(idxs, "]")
			if end < 0 {
				return path.Root(list), true
			}
			n, err := strconv.Atoi(idxs[:end])
			if err != nil || list == "tags" {
				return path.Root(list), true
			}
			p := path.Root(list).AtListIndex(n)
			if sub := strings.TrimPrefix(idxs[end+1:], "."); sub == "type" || sub == "title" || sub == "url" {
				p = p.AtName(sub)
			}
			return p, true
		}
	}
	return path.Empty(), false
}

// metadata builds the declaration metadata from the model; unset values are omitted.
func (m *entityModel) metadata() *client.Metadata {
	md := &client.Metadata{DisplayName: m.DisplayName.ValueString(), Description: m.Description.ValueString(),
		Environment: m.Environment.ValueString(), Owner: m.Owner.ValueString(), Lifecycle: m.Lifecycle.ValueString(), Icon: m.Icon.ValueString(),
		Labels: m.Labels, Annotations: m.Annotations, Tags: m.Tags}
	for _, l := range m.Links {
		md.Links = append(md.Links, client.Link{Type: l.Type.ValueString(), Title: l.Title.ValueString(), URL: l.URL.ValueString()})
	}
	v := &client.VersionInfo{Declared: m.VersionDeclared.ValueString(), Policy: m.VersionPolicy.ValueString(),
		Channel: m.VersionChannel.ValueString(), Product: m.VersionProduct.ValueString()}
	if !v.IsZero() {
		md.Version = v
	}
	return md
}

// fill copies a declaration into the model. Only the declaration (this source's facet) is used: never the merged
// entity view, so other sources' data cannot cause diffs.
func (m *entityModel) fill(d *client.Declaration) {
	m.ID = types.StringValue(d.EntityID)
	if m.Project.IsNull() || m.Project.IsUnknown() || (m.Project.ValueString() != d.Project && m.Project.ValueString() != d.ProjectID) {
		m.Project = types.StringValue(d.Project)
	}
	m.ProjectID = types.StringValue(d.ProjectID)
	m.Kind = types.StringValue(d.Kind)
	m.Name = types.StringValue(d.Name)
	md := d.Metadata
	m.DisplayName = strVal(md.DisplayName, m.DisplayName)
	m.Description = strVal(md.Description, m.Description)
	m.Environment = strVal(md.Environment, m.Environment)
	m.Owner = strVal(md.Owner, m.Owner)
	m.Lifecycle = strVal(md.Lifecycle, m.Lifecycle)
	m.Icon = strVal(md.Icon, m.Icon)
	m.Labels = mapVal(md.Labels, m.Labels)
	m.Annotations = mapVal(md.Annotations, m.Annotations)
	m.Tags = listVal(md.Tags, m.Tags)
	prior := m.Links
	switch {
	case len(md.Links) > 0:
		m.Links = make([]linkModel, len(md.Links))
		for i, l := range md.Links {
			var priorTitle types.String
			if i < len(prior) {
				priorTitle = prior[i].Title
			}
			m.Links[i] = linkModel{Type: types.StringValue(l.Type), Title: strVal(l.Title, priorTitle), URL: types.StringValue(l.URL)}
		}
	case prior != nil:
		m.Links = []linkModel{}
	default:
		m.Links = nil
	}
	var v client.VersionInfo
	if md.Version != nil {
		v = *md.Version
	}
	m.VersionDeclared = strVal(v.Declared, m.VersionDeclared)
	m.VersionPolicy = strVal(v.Policy, m.VersionPolicy)
	m.VersionChannel = strVal(v.Channel, m.VersionChannel)
	m.VersionProduct = strVal(v.Product, m.VersionProduct)
	m.Spec = jsonValue(d.Spec, m.Spec)
}

func (r *entityResource) importHint(project, kind, name string) string {
	return fmt.Sprintf("  terraform import <address> %s/%s/%s", project, kind, name)
}

func (r *entityResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan entityModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, diags := plan.Timeouts.Create(ctx, defaultEntityTimeout)
	resp.Diagnostics.Append(diags...)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	d, err := r.client.CreateEntity(ctx, client.EntityCreate{Project: plan.Project.ValueString(), Source: r.source, Kind: plan.Kind.ValueString(),
		Name: plan.Name.ValueString(), Metadata: plan.metadata(), Spec: rawJSON(plan.Spec)})
	if err != nil {
		if client.HasCode(err, client.CodeAlreadyExists) {
			resp.Diagnostics.AddError("Entity is already declared by source \""+r.source+"\"",
				fmt.Sprintf("Deed already has a declaration of %s/%s in project %s made through the source %q — by another resource in "+
					"this configuration, another Terraform configuration or an earlier apply whose state was lost. Two resources must not "+
					"declare the same entity through the same source. Import the existing declaration instead of creating it:\n\n%s\n\n%s",
					plan.Kind.ValueString(), plan.Name.ValueString(), plan.Project.ValueString(), r.source,
					r.importHint(plan.Project.ValueString(), plan.Kind.ValueString(), plan.Name.ValueString()), err.Error()))
			return
		}
		apiError(&resp.Diagnostics, "Cannot declare entity", err, entityFields)
		return
	}
	plan.fill(d)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(saveETag(ctx, resp.Private, d.ETag)...)
}

func (r *entityResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state entityModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, diags := state.Timeouts.Read(ctx, defaultEntityTimeout)
	resp.Diagnostics.Append(diags...)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	d, err := r.client.GetDeclaration(ctx, state.ID.ValueString(), r.source)
	if client.IsNotFound(err) {
		// The entity is gone, or this source no longer declares it: plan a new declaration.
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot read entity declaration", err, nil)
		return
	}
	state.fill(d)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(saveETag(ctx, resp.Private, d.ETag)...)
}

func (r *entityResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state entityModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, diags := plan.Timeouts.Update(ctx, defaultEntityTimeout)
	resp.Diagnostics.Append(diags...)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	d, err := r.client.PutDeclaration(ctx, state.ID.ValueString(), r.source, loadETag(ctx, req.Private),
		client.DeclarationInput{Metadata: plan.metadata(), Spec: rawJSON(plan.Spec)})
	if err != nil {
		apiError(&resp.Diagnostics, "Cannot update entity declaration", err, entityFields)
		return
	}
	plan.fill(d)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(saveETag(ctx, resp.Private, d.ETag)...)
}

func (r *entityResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state entityModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, diags := state.Timeouts.Delete(ctx, defaultEntityTimeout)
	resp.Diagnostics.Append(diags...)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Removes only this source's facet: imports, observations and annotations from other sources survive.
	if err := r.client.DeleteDeclaration(ctx, state.ID.ValueString(), r.source); err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "Cannot remove entity declaration", err, nil)
	}
}

func (r *entityResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := req.ID
	if strings.Contains(id, "/") {
		project, kind, name, err := splitNaturalKey(id)
		if err != nil {
			resp.Diagnostics.AddError("Invalid import ID", err.Error())
			return
		}
		e, err := r.client.LookupEntity(ctx, project, kind, name)
		if err != nil {
			apiError(&resp.Diagnostics, "Cannot import entity "+id, err, nil)
			return
		}
		id = e.Metadata.ID
	}
	if _, err := r.client.GetDeclaration(ctx, id, r.source); err != nil {
		if client.IsNotFound(err) {
			resp.Diagnostics.AddError("Entity is not declared by source \""+r.source+"\"",
				fmt.Sprintf("%s exists (or was not found), but the source %q has no declaration of it, so there is nothing to import. "+
					"Remove the import and apply the resource instead: creating it adopts an existing entity reported by other sources "+
					"and adds this source's declaration.\n\n%s", req.ID, r.source, err.Error()))
			return
		}
		apiError(&resp.Diagnostics, "Cannot import entity "+req.ID, err, nil)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}
