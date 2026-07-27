package cloud_test

import (
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/finance"
)

// The money plane the co-resident inference module gates on is installed by the
// composition root through cloud.RegisterBillingInstaller — package cloud hands over
// the built Deps and never imports the module. A cut that compiles but stops calling
// the installer is a revenue leak, not a build break, so these tests pin the two
// halves of the contract: BuildDeps calls it, and it calls it with everything the
// bindings need already in place.

// TestBuildDepsInvokesBillingInstaller proves BuildDeps invokes the registered
// installer exactly once, with the metering client built and the finance ledger
// ALREADY PUBLISHED — the ordering the tier read and the prepaid gate depend on. The
// ledger is asserted from INSIDE the callback: asserting it afterwards would pass
// even if the installer ran first.
func TestBuildDepsInvokesBillingInstaller(t *testing.T) {
	finance.Publish(nil)
	t.Cleanup(func() {
		cloud.RegisterBillingInstaller(nil)
		finance.Publish(nil)
	})

	calls := 0
	var metering bool
	var ledger bool
	cloud.RegisterBillingInstaller(func(deps cloud.Deps) {
		calls++
		metering = deps.Metering != nil && deps.Metering.Enabled()
		ledger = finance.Current() != nil
	})

	cloud.BuildDeps(&cloud.Config{
		Brand:   "hanzo",
		DataDir: t.TempDir(),
		Enable:  []string{"commerce"}, // money layer co-resident (the unified binary)
	})

	if calls != 1 {
		t.Fatalf("billing installer ran %d times, want exactly 1 per BuildDeps", calls)
	}
	if !metering {
		t.Error("installer ran before the metering client was built: the per-tier SKU gate would never be installed")
	}
	if !ledger {
		t.Error("installer ran before the finance ledger was published: the prepaid balance/usage hooks would never be installed")
	}
}

// TestBuildDepsWithoutBillingInstaller proves an unregistered seam is inert, not
// fatal: a binary that links cloud without the composition root (cmd/kmsreseal, any
// test harness) boots normally and leaves the module's own HTTP billing path alone.
func TestBuildDepsWithoutBillingInstaller(t *testing.T) {
	cloud.RegisterBillingInstaller(nil)
	t.Cleanup(func() { finance.Publish(nil) })

	deps := cloud.BuildDeps(&cloud.Config{Brand: "hanzo", DataDir: t.TempDir()})
	if deps.Logger == nil {
		t.Fatal("BuildDeps must still build deps with no billing installer registered")
	}
}
