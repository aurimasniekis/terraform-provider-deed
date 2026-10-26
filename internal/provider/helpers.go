// Adapted from terraform-provider-porch internal/provider/helpers.go (Apache-2.0).

package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/aurimasniekis/terraform-provider-deed/internal/client"
)

// privateState is the subset of the framework's private state API used to keep the ETag out of the visible schema.
type privateState interface {
	GetKey(ctx context.Context, key string) ([]byte, diag.Diagnostics)
	SetKey(ctx context.Context, key string, value []byte) diag.Diagnostics
}

const etagKey = "etag"

// saveETag stores the resource version (sent as If-Match on the next change) in private state.
func saveETag(ctx context.Context, p privateState, etag string) diag.Diagnostics {
	if p == nil || etag == "" {
		return nil
	}
	b, _ := json.Marshal(etag)
	return p.SetKey(ctx, etagKey, b)
}

// loadETag returns the stored ETag ("" when unknown, which makes the change unconditional).
func loadETag(ctx context.Context, p privateState) string {
	if p == nil {
		return ""
	}
	b, diags := p.GetKey(ctx, etagKey)
	if diags.HasError() || len(b) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(b, &s) != nil {
		return ""
	}
	return s
}

// configureData extracts the provider data.
func configureData(data any, diags *diag.Diagnostics) *providerData {
	if data == nil {
		return nil
	}
	d, ok := data.(*providerData)
	if !ok {
		diags.AddError("Unexpected provider data", fmt.Sprintf("expected *providerData, got %T", data))
		return nil
	}
	return d
}

// baseResource carries the client and the declaring source for every resource.
type baseResource struct {
	stateUpgrades
	client *client.Client
	source string
}

func (b *baseResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if d := configureData(req.ProviderData, &resp.Diagnostics); d != nil {
		b.client, b.source = d.client, d.source
	}
}

// baseDataSource carries the client for every data source.
type baseDataSource struct {
	client *client.Client
	source string
}

func (b *baseDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if d := configureData(req.ProviderData, &resp.Diagnostics); d != nil {
		b.client, b.source = d.client, d.source
	}
}

// fieldMapper maps a Deed problem field (e.g. `metadata.labels.team`, `spec/port`, `body.title`) to an attribute path.
type fieldMapper func(field string) (path.Path, bool)

// apiError adds an API error as diagnostics: one attribute diagnostic per problem field the mapper recognises (so
// Terraform points at the offending argument) plus a general diagnostic with the problem code, detail and any
// remaining fields. Stale ETags (412) and authorization failures get actionable advice.
func apiError(diags *diag.Diagnostics, summary string, err error, mapper fieldMapper) {
	ae, ok := client.AsAPIError(err)
	if !ok {
		diags.AddError(summary, err.Error())
		return
	}
	type fieldMsg struct{ field, msg string }
	var rest []string
	all := make([]fieldMsg, 0, len(ae.Fields)+len(ae.Errors))
	for _, f := range ae.Fields {
		all = append(all, fieldMsg{f.Field, f.Message})
	}
	for _, e := range ae.Errors {
		all = append(all, fieldMsg{e.Location, e.Message})
	}
	for _, f := range all {
		if mapper != nil && f.field != "" {
			if p, ok := mapper(strings.TrimPrefix(f.field, "body.")); ok {
				diags.AddAttributeError(p, summary, f.msg+" (Deed field `"+f.field+"`)")
				continue
			}
		}
		if f.field != "" {
			rest = append(rest, f.field+": "+f.msg)
		} else {
			rest = append(rest, f.msg)
		}
	}
	detail := ae.Summary()
	if len(rest) > 0 {
		detail += "\n\n" + strings.Join(rest, "\n")
	}
	switch ae.StatusCode {
	case http.StatusPreconditionFailed:
		detail += "\n\nThe object was changed outside Terraform after it was last read (its ETag no longer matches). " +
			"Refresh the state (`terraform plan` or `terraform apply -refresh-only`) and apply again."
	case http.StatusUnauthorized:
		detail += "\n\nThe API token is invalid, expired or revoked. Create a new token (deed_at_…) and set it in the provider " +
			"configuration or DEED_TOKEN."
	case http.StatusForbidden:
		detail += "\n\nThe API token lacks the required role: projects, kinds and relationship types need an installation admin " +
			"token; entities and relationships need the editor role in their project."
	}
	diags.AddError(summary, detail)
}

// splitID splits a composite import ID into exactly n non-empty parts separated by "/".
func splitID(id string, n int, format string) ([]string, error) {
	parts := strings.Split(id, "/")
	if len(parts) != n || slices.Contains(parts, "") {
		return nil, fmt.Errorf("expected an import ID of the form %q, got %q", format, id)
	}
	return parts, nil
}

// splitNaturalKey splits "<project>/<kind>/<name>". Kind names may contain a domain prefix with a slash
// ("example.com/ups"), entity and project names never do, so the first segment is the project, the last the name and
// everything in between the kind.
func splitNaturalKey(id string) (project, kind, name string, err error) {
	parts := strings.Split(id, "/")
	if len(parts) < 3 || slices.Contains(parts, "") {
		return "", "", "", fmt.Errorf("expected an entity ID or %q, got %q", "<project>/<kind>/<name>", id)
	}
	return parts[0], strings.Join(parts[1:len(parts)-1], "/"), parts[len(parts)-1], nil
}

// strVal maps an API string to a state value, keeping an explicitly configured empty string (prior "") instead of
// turning it into null, so configurations that set "" do not produce inconsistent results.
func strVal(s string, prior types.String) types.String {
	if s != "" {
		return types.StringValue(s)
	}
	if !prior.IsNull() && !prior.IsUnknown() && prior.ValueString() == "" {
		return types.StringValue("")
	}
	return types.StringNull()
}

// mapVal maps an API string map to state: empty becomes null unless the prior value was an empty map.
func mapVal(m map[string]string, prior map[string]string) map[string]string {
	if len(m) > 0 {
		return m
	}
	if prior != nil {
		return map[string]string{}
	}
	return nil
}

// listVal maps an API string list to state: empty becomes null unless the prior value was an empty list.
func listVal(ss []string, prior []string) []string {
	if len(ss) > 0 {
		return ss
	}
	if prior != nil {
		return []string{}
	}
	return nil
}

// jsonValue converts a raw JSON document to a normalized JSON string value. Empty or null documents become null
// unless the prior value was set (then "{}"), so an omitted argument stays omitted.
func jsonValue(raw json.RawMessage, prior jsontypes.Normalized) jsontypes.Normalized {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) || isEmptyObject(t) {
		if !prior.IsNull() && !prior.IsUnknown() {
			if len(t) == 0 || bytes.Equal(t, []byte("null")) {
				return jsontypes.NewNormalizedValue("{}")
			}
			return jsontypes.NewNormalizedValue(string(t))
		}
		return jsontypes.NewNormalizedNull()
	}
	return jsontypes.NewNormalizedValue(string(t))
}

func isEmptyObject(b []byte) bool {
	var m map[string]any
	return json.Unmarshal(b, &m) == nil && m != nil && len(m) == 0
}

// rawJSON returns the configured JSON document (nil for null/unknown).
func rawJSON(v jsontypes.Normalized) json.RawMessage {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	return json.RawMessage(v.ValueString())
}

// jsonObjectValidator requires a JSON document to be an object.
type jsonObjectValidator struct{}

func (jsonObjectValidator) Description(context.Context) string { return "must be a JSON object" }
func (v jsonObjectValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (jsonObjectValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(req.ConfigValue.ValueString()), &m); err != nil || m == nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid JSON object", "The value must be a JSON object, for example jsonencode({ port = 443 }).")
	}
}

// sortedKeys returns the keys of m in order.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var (
	projectNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	entityNameRE  = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,251}[a-z0-9])?$`)
	kindNameRE    = regexp.MustCompile(`^([a-z0-9]([a-z0-9.-]{0,61}[a-z0-9])?/)?[a-z][a-z0-9_]{0,62}$`)
	envRE         = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	iconRE        = regexp.MustCompile(`^(tabler|simple-icons|thesvg|custom):[a-z0-9][a-z0-9._-]{0,127}$`)
	colorRE       = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	labelKeyRE    = regexp.MustCompile(`^([a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?/)?[a-zA-Z0-9]([a-zA-Z0-9._-]{0,61}[a-zA-Z0-9])?$`)
)

// Common schema attributes.

func idAttribute(desc string) schema.StringAttribute {
	return schema.StringAttribute{
		MarkdownDescription: desc,
		Computed:            true,
		PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
}

func requiredImmutableString(desc string, v ...validator.String) schema.StringAttribute {
	return schema.StringAttribute{
		MarkdownDescription: desc + " Changing it forces a new resource.",
		Required:            true,
		PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
		Validators:          v,
	}
}

func computedString(desc string) schema.StringAttribute {
	return schema.StringAttribute{
		MarkdownDescription: desc,
		Computed:            true,
		PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
}

// sameProjectReplace forces replacement when `project` changes, except when the new value names the same project as
// the state (its name or its ID, recorded in project_id): switching between the two forms is an in-place no-op.
func sameProjectReplace(idAttr string) planmodifier.String {
	return stringplanmodifier.RequiresReplaceIf(func(ctx context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
		var pid types.String
		resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root(idAttr), &pid)...)
		v := req.PlanValue.ValueString()
		resp.RequiresReplace = v != pid.ValueString() && v != req.StateValue.ValueString()
	}, "Changing the project forces a new resource (the same project by name or ID does not).",
		"Changing the project forces a new resource (the same project by name or ID does not).")
}

// rootMapper maps simple top-level fields to attributes with the same name, with optional renames.
func rootMapper(attrs map[string]string) fieldMapper {
	return func(field string) (path.Path, bool) {
		head := field
		if i := strings.IndexAny(head, ".[/"); i >= 0 {
			head = head[:i]
		}
		if a, ok := attrs[head]; ok {
			return path.Root(a), true
		}
		return path.Empty(), false
	}
}

var serviceAccountNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
