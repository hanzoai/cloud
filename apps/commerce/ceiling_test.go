package commerce

import (
	"testing"

	"github.com/hanzoai/commerce/models/organization"

	"github.com/hanzoai/cloud/sdk/network"
)

// Every org is held at test off mainnet, and mainnet has no cap.
func TestCeilingIsTheNetworks(t *testing.T) {
	for net, want := range map[string]organization.Kind{
		"":                      organization.Live,
		string(network.Mainnet): organization.Live,
		string(network.Testnet): organization.Test,
		string(network.Devnet):  organization.Test,
	} {
		t.Setenv(network.NetworkEnv, net)
		if got := ceiling(); got != want {
			t.Errorf("ceiling() on %q = %q, want %q", net, got, want)
		}
	}
}
