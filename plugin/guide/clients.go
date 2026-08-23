package main

import (
	"github.com/hanzoai/cloud/apps/framework"
	"github.com/hanzoai/cloud/apps/guide"
	"github.com/hanzoai/cloud/apps/integrations"
)

// The growth-OBSERVE client guide owns, wired in ITS OWN composition root.
//
// This used to live in package apps (wire_clients.go), which the whole fleet
// linked. clients/guide imports NONE of these subsystems (decomplected), so this
// main — the ONE place that imports them — binds the reads, the same injected-
// function pattern the coding dispatcher uses. init() runs once at load.
//
// Each probe is PROVABLY org-scoped: framework.ModuleInstalled keys GetDocType on
// the org; integrations.Connected keys store.Get on the org and never surfaces the
// token. HasDeployment/RevenueCents/RecordCount are LEFT NIL: deploy is cluster/
// admin-scoped (not per-org) and commerce-revenue-of-record + the crm record count
// have no clean per-org in-process read yet — binding a read whose org-scoping we
// cannot guarantee would be the bug. Until one lands their signals honest-degrade
// to not-present (a nil client can never be a spurious true).
func init() {
	guide.BindSignals(guide.Signals{
		ModuleInstalled:  framework.ModuleInstalled,
		ConnectorPresent: integrations.Connected,
	})
}
