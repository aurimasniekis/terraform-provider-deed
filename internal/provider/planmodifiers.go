package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// defaultFromAttribute plans an Optional+Computed string as `<attr><suffix>` when it is not configured. It mirrors a
// deterministic server default (a project's display name defaults to its name, a kind's plural to its title + "s"),
// so the plan is exact instead of "known after apply".
type defaultFromAttributeModifier struct {
	attr, suffix string
}

func defaultFromAttribute(attr string) planmodifier.String {
	return defaultFromAttributeModifier{attr: attr}
}

func defaultFromAttributeSuffix(attr, suffix string) planmodifier.String {
	return defaultFromAttributeModifier{attr: attr, suffix: suffix}
}

func (m defaultFromAttributeModifier) Description(context.Context) string {
	return "Defaults to the value of " + m.attr + m.suffixDesc() + "."
}

func (m defaultFromAttributeModifier) suffixDesc() string {
	if m.suffix == "" {
		return ""
	}
	return " followed by \"" + m.suffix + "\""
}

func (m defaultFromAttributeModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m defaultFromAttributeModifier) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if !req.ConfigValue.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var src types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root(m.attr), &src)...)
	if src.IsUnknown() || src.IsNull() {
		resp.PlanValue = types.StringUnknown()
		return
	}
	resp.PlanValue = types.StringValue(src.ValueString() + m.suffix)
}

// unknownWhenSchemaChanges marks a computed string (a kind's storage_version) unknown when the planned schema differs
// semantically from the state, because publishing a new version changes it; otherwise it keeps the state value.
type unknownWhenSchemaChanges struct{}

func (unknownWhenSchemaChanges) Description(context.Context) string {
	return "Known after apply when the schema changes; otherwise unchanged."
}

func (m unknownWhenSchemaChanges) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (unknownWhenSchemaChanges) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var planSchema, stateSchema jsontypes.Normalized
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("schema"), &planSchema)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("schema"), &stateSchema)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if planSchema.IsUnknown() {
		resp.PlanValue = types.StringUnknown()
		return
	}
	if planSchema.IsNull() {
		resp.PlanValue = req.StateValue
		return
	}
	eq, diags := planSchema.StringSemanticEquals(ctx, stateSchema)
	resp.Diagnostics.Append(diags...)
	if eq {
		resp.PlanValue = req.StateValue
		return
	}
	resp.PlanValue = types.StringUnknown()
}
