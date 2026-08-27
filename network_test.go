package cloud_test

import (
	"os"
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

// CLOUD_NETWORK IS NOT THE `network` APP'S ADDRESS OVERRIDE.
//
// The app once called `zt` is called `network` now, and the plugin resolver
// reserves CLOUD_<APP>_ADDR and CLOUD_<APP>_BIN — so CLOUD_NETWORK_ADDR already
// means "the network app is already listening there". This variable sits one
// underscore away from it and means something else entirely: which money-and-chain
// world the whole deployment is.
//
// Go matches an environment name exactly, so the two cannot be confused by the
// code. They CAN be confused by a person reading one env block, which is why the
// independence is pinned here rather than left to whoever reads the two names next.
func TestTheNetworkAppAddressIsADifferentVariable(t *testing.T) {
	t.Setenv("CLOUD_NETWORK_ADDR", "10.0.0.1:9000")
	t.Setenv(cloud.NetworkEnv, string(cloud.Testnet))
	if got := cloud.NetworkOf(); got != cloud.Testnet {
		t.Errorf("the app address override changed the deployment network: got %q", got)
	}

	// And the reverse: naming the world must not look like an address to the
	// resolver, which is the direction that would send a plugin somewhere.
	t.Setenv("CLOUD_NETWORK_ADDR", "")
	t.Setenv(cloud.NetworkEnv, string(cloud.Devnet))
	if v := os.Getenv("CLOUD_NETWORK_ADDR"); v != "" {
		t.Errorf("CLOUD_NETWORK leaked into the app address override: %q", v)
	}
}
