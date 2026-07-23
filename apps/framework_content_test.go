package apps

import (
	"testing"

	"github.com/hanzoai/cloud/clients/framework"
)

// TestFrameworkContentModulesLinked guards the framework CONTENT modules
// (cms/erp/help) against silent removal. cms/erp are pure fixture lanes reached
// ONLY via blank imports in apps.go; help is a real import + a Wire() spec (it also
// mounts the /v1/help public plane) but still contributes its DocTypes the SAME way,
// from a package init() (framework.RegisterModule). #248 dropped the blank imports,
// which stripped the erp ledger hooks from the binary with no mount change and no
// failing mount test. This asserts the engine's module registry carries each lane,
// so that money-adjacent regression cannot recur — for help, dropping the import
// would also drop its Wire() spec and fail TestWireOrderMatchesFrozen, but this keeps
// the fixture-linkage guard uniform across all three lanes.
func TestFrameworkContentModulesLinked(t *testing.T) {
	got := make(map[string]bool)
	for _, m := range framework.RegisteredModules() {
		got[m] = true
	}
	for _, want := range []string{"cms", "erp", "help"} {
		if !got[want] {
			t.Errorf("framework content module %q not registered — a blank import in apps.go is missing (erp drop = ledger hooks gone from the binary)", want)
		}
	}
	// The module registry proves DocTypes are linked, but erp's ledger-posting
	// HOOKS register in a separate init() step; assert them directly so the guard
	// survives a future split of registerHooks() out of erp's module init().
	if framework.RegisteredHookCount() == 0 {
		t.Error("no framework lifecycle hooks registered — erp's ledger-posting hooks (computeJournalTotals, journalEntry/paymentEntry submit+cancel, …) are not linked into the binary")
	}
}
