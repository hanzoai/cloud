package admin

// The board's tier for a workload turns on WHOSE namespace it runs in, and that
// question has exactly one answer in the estate (internal/cluster). Every
// first-party namespace must therefore keep being classified by role and image
// family; one missing from a private list reads as a customer deployment, which
// on this board is indistinguishable from a correct row.

import (
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/cluster"
)

func TestTierClassifiesEveryPlatformNamespaceAsInfra(t *testing.T) {
	for _, ns := range []string{"hanzo", "hanzo-mainnet", "hanzo-testnet", "hanzo-devnet"} {
		if got := tierOf(client.App{Namespace: ns, Registry: "ghcr.io/hanzoai/cloud"}); got != "cloud" {
			t.Errorf("%s: tier = %q, want cloud", ns, got)
		}
		if got := tierOf(client.App{Namespace: ns, Role: "sql"}); got != "data" {
			t.Errorf("%s: tier = %q, want data", ns, got)
		}
	}
}

func TestTierClassifiesACustomerNamespaceAsPaas(t *testing.T) {
	// A tenant namespace short-circuits whatever it runs: the image family says
	// what the workload is, not whose fleet it belongs to.
	if got := tierOf(client.App{Namespace: "tenant-maxpower", Registry: "ghcr.io/hanzoai/cloud"}); got != "paas" {
		t.Errorf("tenant namespace tier = %q, want paas", got)
	}
	if _, _, ok := cluster.Class("tenant-maxpower"); !ok {
		t.Error("a tenant namespace must classify, or no board can group it")
	}
}
