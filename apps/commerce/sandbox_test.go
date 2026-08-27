package commerce

import (
	"testing"

	"github.com/hanzoai/cloud"
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
		{string(cloud.Mainnet), false, false, "mainnet must leave a live charge live"},
		{string(cloud.Mainnet), true, true, "mainnet must honour a caller's sandbox charge"},
		{string(cloud.Testnet), false, true, "testnet must force a live request onto the sandbox books"},
		{string(cloud.Testnet), true, true, "testnet must keep a sandbox charge sandboxed"},
		{string(cloud.Devnet), false, true, "devnet must force a live request onto the sandbox books"},
		{string(cloud.Devnet), true, true, "devnet must keep a sandbox charge sandboxed"},
	} {
		t.Setenv(cloud.NetworkEnv, tc.net)
		if got := sandboxed(tc.asked); got != tc.expect {
			t.Errorf("network=%s sandboxed(%v)=%v want %v — %s", tc.net, tc.asked, got, tc.expect, tc.why)
		}
	}
}

// A sandbox charge stays sandboxed on EVERY network, which is what makes the
// helper safe to call at an address that is also used on mainnet.
func TestASandboxChargeIsNeverPromoted(t *testing.T) {
	for _, net := range []string{string(cloud.Mainnet), string(cloud.Testnet), string(cloud.Devnet), "", "garbage"} {
		t.Setenv(cloud.NetworkEnv, net)
		if !sandboxed(true) {
			t.Fatalf("network=%q turned a sandbox charge into live money", net)
		}
	}
}
