package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// State schema versioning policy
//
// Every resource schema is at version 0 (stateSchemaVersion). A change that alters how existing state must be
// interpreted — renaming or removing an attribute, changing an attribute's type, or changing the meaning of a stored
// value — bumps that resource's schema Version to N+1 and adds an upgrader for version N to its UpgradeState map, which
// converts the prior state (decoded with the prior schema, kept next to the upgrader) into the current model. Upgraders
// are never removed, so state written by any earlier provider release keeps working. Additive changes (new optional or
// computed attributes) do not bump the version. The ETag kept in private state is advisory: an upgrader may drop it,
// which only makes the next write unconditional.
const stateSchemaVersion int64 = 0

// stateUpgrades is embedded in every resource and provides the (currently empty) upgrader set. A resource that bumps
// its schema version overrides UpgradeState.
type stateUpgrades struct{}

func (stateUpgrades) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{}
}
