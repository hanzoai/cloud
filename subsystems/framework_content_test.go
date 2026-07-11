package subsystems

import (
	"testing"

	"github.com/hanzoai/cloud/clients/framework"
)

// TestFrameworkContentModulesLinked guards the framework CONTENT modules
// (cms/erp/help) against silent removal. They are not mount subsystems, so they
// never appear in Wire(); they register their DocTypes and — for erp — the
// ledger-posting lifecycle hooks into the framework engine from a package
// init(), reached ONLY via the blank imports in subsystems.go. #248 dropped
// those imports, which stripped the erp ledger hooks from the binary with no
// mount change and no failing mount test. This asserts the engine's module
// registry carries each lane, so that money-adjacent regression cannot recur.
func TestFrameworkContentModulesLinked(t *testing.T) {
	got := make(map[string]bool)
	for _, m := range framework.RegisteredModules() {
		got[m] = true
	}
	for _, want := range []string{"cms", "erp", "help"} {
		if !got[want] {
			t.Errorf("framework content module %q not registered — a blank import in subsystems.go is missing (erp drop = ledger hooks gone from the binary)", want)
		}
	}
	// The module registry proves DocTypes are linked, but erp's ledger-posting
	// HOOKS register in a separate init() step; assert them directly so the guard
	// survives a future split of registerHooks() out of erp's module init().
	if framework.RegisteredHookCount() == 0 {
		t.Error("no framework lifecycle hooks registered — erp's ledger-posting hooks (computeJournalTotals, journalEntry/paymentEntry submit+cancel, …) are not linked into the binary")
	}
}
