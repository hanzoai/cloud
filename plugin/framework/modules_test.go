package main

import (
	"testing"

	"github.com/hanzoai/cloud/apps/framework"
)

// The app lanes are LINKED, and a blank import is the only thing that says so.
//
// cms and erp register their fixtures from package init(), so the import in
// main.go is load-bearing in the one way nothing else catches: remove it and the
// build stays green, the plugin still serves, and the module simply is not there
// — `POST /v1/framework/modules/erp/install` then answers for a module the
// engine has never heard of. That is exactly what both packages did before this,
// for months, with every gate passing.
//
// So the assertion is on the REGISTRY rather than on the import: an unused
// import is a compile error a linter would offer to fix, and the fix would be to
// delete the line that makes the product work.
func TestTheAppLanesAreRegistered(t *testing.T) {
	got := map[string]bool{}
	for _, m := range framework.RegisteredModules() {
		got[m] = true
	}
	for _, want := range []string{"cms", "erp"} {
		if !got[want] {
			t.Fatalf("module %q is not registered — plugin/framework must blank-import apps/%s, "+
				"or its init never runs and the engine has never heard of it (registered: %v)",
				want, want, framework.RegisteredModules())
		}
	}
}
