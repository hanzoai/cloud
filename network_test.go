package cloud_test

import (
	"testing"

	"github.com/hanzoai/cloud"
)

// A DEPLOYMENT THAT DECLARES NOTHING IS PRODUCTION.
//
// This is the half worth a test rather than a comment: every other value here is
// discovered by reading it, but "unset means mainnet" is discovered by a test
// network taking a real card, which is the wrong place to find out.
func TestUnsetAndGarbageAreMainnet(t *testing.T) {
	for _, v := range []string{"", "  ", "MAINNET", "prod", "test", "dev", "testnet-2", "nonsense"} {
		t.Setenv(cloud.NetworkEnv, v)
		if got := cloud.NetworkOf(); got != cloud.Mainnet {
			t.Errorf("CLOUD_NETWORK=%q resolved to %q, want mainnet — an unrecognised value must not decide a deployment's money is fake", v, got)
		}
		if cloud.SandboxOnly() {
			t.Errorf("CLOUD_NETWORK=%q reported sandbox-only", v)
		}
	}
}

// The two networks that ARE named resolve, and both refuse live money.
func TestTheNamedNetworksAreSandboxOnly(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want cloud.Network
	}{
		{"testnet", cloud.Testnet},
		{"devnet", cloud.Devnet},
	} {
		t.Setenv(cloud.NetworkEnv, tc.env)
		if got := cloud.NetworkOf(); got != tc.want {
			t.Errorf("CLOUD_NETWORK=%q resolved to %q, want %q", tc.env, got, tc.want)
		}
		if !cloud.SandboxOnly() {
			t.Errorf("CLOUD_NETWORK=%q is not sandbox-only; live money would be reachable on a test network", tc.env)
		}
	}
}

// Mainnet leaves the answer to the caller, which is the whole point of an org's
// posture. Asserted so a future "safer" default cannot quietly force every
// production charge into the sandbox and stop billing the company's customers.
func TestMainnetLeavesThePostureToTheCaller(t *testing.T) {
	t.Setenv(cloud.NetworkEnv, string(cloud.Mainnet))
	if cloud.SandboxOnly() {
		t.Fatal("mainnet reported sandbox-only: every real charge would be booked as fake")
	}
}
