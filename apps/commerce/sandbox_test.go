package commerce

import (
	"testing"

	"github.com/hanzoai/commerce/models/organization"

	"github.com/hanzoai/cloud/sdk/network"
)

// THE GATE ONLY EVER FORCES SANDBOX, NEVER LIVE.
//
// Both directions matter and they are not the same risk. Failing to force
// sandbox on a test network lets a customer be charged for real. Forcing LIVE
// anywhere would book a sandbox charge as revenue, which restates income and is
// the one thing no deployment setting may do — so it is asserted rather than
// left to the reading of a one-line function.
func TestSandboxedOnlyEverForcesSandbox(t *testing.T) {
	for _, tc := range []struct {
		net           string
		asked, expect bool
		why           string
	}{
		{string(network.Mainnet), false, false, "mainnet must leave a live charge live"},
		{string(network.Mainnet), true, true, "mainnet must honour a caller's sandbox charge"},
		{string(network.Testnet), false, true, "testnet must force a live request onto the sandbox books"},
		{string(network.Testnet), true, true, "testnet must keep a sandbox charge sandboxed"},
		{string(network.Devnet), false, true, "devnet must force a live request onto the sandbox books"},
		{string(network.Devnet), true, true, "devnet must keep a sandbox charge sandboxed"},
	} {
		t.Setenv(network.NetworkEnv, tc.net)
		if got := sandboxed(tc.asked); got != tc.expect {
			t.Errorf("network=%s sandboxed(%v)=%v want %v — %s", tc.net, tc.asked, got, tc.expect, tc.why)
		}
	}
}

// A sandbox charge stays sandboxed on EVERY network, which is what makes the
// helper safe to call at an address that is also used on mainnet.
func TestASandboxChargeIsNeverPromoted(t *testing.T) {
	for _, net := range []string{string(network.Mainnet), string(network.Testnet), string(network.Devnet), "", "garbage"} {
		t.Setenv(network.NetworkEnv, net)
		if !sandboxed(true) {
			t.Fatalf("network=%q turned a sandbox charge into live money", net)
		}
	}
}

// A new org is born live only where real money exists: on mainnet, including a
// deployment that declares no network, and nowhere else.
func TestBornLiveOnlyOnMainnet(t *testing.T) {
	for _, tc := range []struct {
		net  string
		want organization.Kind
	}{
		{string(network.Mainnet), organization.Live},
		{"", organization.Live},
		{string(network.Testnet), organization.Test},
		{string(network.Devnet), organization.Test},
	} {
		t.Setenv(network.NetworkEnv, tc.net)
		if got := born(); got != tc.want {
			t.Errorf("network=%q born()=%q want %q", tc.net, got, tc.want)
		}
	}
}
