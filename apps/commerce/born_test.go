// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"testing"

	"github.com/hanzoai/commerce/models/organization"
	commerceorg "github.com/hanzoai/commerce/pkg/org"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/network"
)

// THE WHOLE PATH, through the real Use: the network this deployment declares is
// the kind a customer's org is born in, measured on the record the charge path
// resolves.
func TestANewOrgIsBornInTheNetworksKind(t *testing.T) {
	prev := organization.Born
	t.Cleanup(func() { organization.Born = prev })

	for _, tc := range []struct {
		net, org string
		live     bool
	}{
		{string(network.Mainnet), "born-on-mainnet", true},
		{string(network.Testnet), "born-on-testnet", false},
	} {
		t.Setenv(network.NetworkEnv, tc.net)
		t.Setenv("CLOUD_BRAND", "hanzo")
		t.Setenv("CLOUD_DATA_DIR", t.TempDir())
		app := zip.New(zip.Config{Logger: luxlog.New("born")})
		if err := Use(app, cloud.Deps{}); err != nil {
			t.Fatalf("Use: %v", err)
		}
		if currentEmbedded() == nil {
			t.Fatal("the commerce embed did not boot; nothing here measures the served path")
		}
		o, err := commerceorg.Resolve(context.Background(), tc.org)
		if err != nil {
			t.Fatalf("resolve %q: %v", tc.org, err)
		}
		if o.Live != tc.live || o.TestMode() == tc.live {
			t.Errorf("network=%s: new org %q born Live=%v TestMode=%v, want Live=%v",
				tc.net, tc.org, o.Live, o.TestMode(), tc.live)
		}
	}
}
