package husd

import (
	"math/big"
	"os"
	"testing"
)

func TestCentsToWei(t *testing.T) {
	cases := []struct {
		cents    int64
		decimals int
		want     string
	}{
		{0, 18, "0"},
		{1, 18, "10000000000000000"},        // $0.01 → 1e16
		{100, 18, "1000000000000000000"},    // $1.00 → 1e18
		{2550, 18, "25500000000000000000"},  // $25.50 → 25.5e18
		{100, 6, "1000000"},                 // 6-decimal stablecoin: $1.00 → 1e6
		{100, 2, "100"},                     // 2-decimal: cents == base units
		{1234567, 18, "12345670000000000000000"},
	}
	for _, c := range cases {
		got, err := CentsToWei(c.cents, c.decimals)
		if err != nil {
			t.Fatalf("CentsToWei(%d,%d): %v", c.cents, c.decimals, err)
		}
		if got.String() != c.want {
			t.Errorf("CentsToWei(%d,%d)=%s, want %s", c.cents, c.decimals, got, c.want)
		}
	}
	if _, err := CentsToWei(100, 1); err == nil {
		t.Error("CentsToWei with decimals<2 should error")
	}
	if _, err := CentsToWei(-1, 18); err == nil {
		t.Error("CentsToWei with negative cents should error")
	}
}

// Round-trip: every whole-cent amount survives cents→wei→cents with zero
// remainder. This is the exactness guarantee the chain-index reconciliation
// depends on (no dust invented or lost).
func TestCentsWeiRoundTrip(t *testing.T) {
	for _, decimals := range []int{2, 6, 8, 18} {
		for _, cents := range []int64{0, 1, 2, 99, 100, 2550, 1_000_000, 123_456_789} {
			wei, err := CentsToWei(cents, decimals)
			if err != nil {
				t.Fatalf("CentsToWei(%d,%d): %v", cents, decimals, err)
			}
			back, rem, err := WeiToCents(wei, decimals)
			if err != nil {
				t.Fatalf("WeiToCents(%s,%d): %v", wei, decimals, err)
			}
			if back != cents {
				t.Errorf("round-trip cents=%d decimals=%d -> %d", cents, decimals, back)
			}
			if rem.Sign() != 0 {
				t.Errorf("round-trip cents=%d decimals=%d left remainder %s", cents, decimals, rem)
			}
		}
	}
}

// Sub-cent dust in a raw balance is reported as remainder, never silently
// folded into a cent (fail-loud on dust).
func TestWeiToCentsRemainder(t *testing.T) {
	// 1e18 + 1 wei at 18 decimals = 100 cents + 1 wei remainder.
	wei := new(big.Int).Add(big.NewInt(0), mustWei(t, 100, 18))
	wei.Add(wei, big.NewInt(1))
	cents, rem, err := WeiToCents(wei, 18)
	if err != nil {
		t.Fatal(err)
	}
	if cents != 100 {
		t.Errorf("cents=%d, want 100", cents)
	}
	if rem.Cmp(big.NewInt(1)) != 0 {
		t.Errorf("remainder=%s, want 1", rem)
	}
	if _, _, err := WeiToCents(big.NewInt(-1), 18); err == nil {
		t.Error("WeiToCents(negative) should error")
	}
}

func mustWei(t *testing.T, cents int64, decimals int) *big.Int {
	t.Helper()
	w, err := CentsToWei(cents, decimals)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestConfigLoadFromEnvDefaults(t *testing.T) {
	for _, k := range []string{"HUSD_CHAIN_ID", "HUSD_RPC_URL", "HUSD_TOKEN_ADDRESS", "HUSD_TOKEN_DECIMALS", "HUSD_TREASURY_KEY", "HUSD_GAS_LIMIT"} {
		t.Setenv(k, "")
	}
	c := Config{}
	c.LoadFromEnv()
	if c.ChainID != DefaultChainID {
		t.Errorf("ChainID=%d, want %d", c.ChainID, DefaultChainID)
	}
	if c.RPCURL != DefaultRPCURL {
		t.Errorf("RPCURL=%q, want default", c.RPCURL)
	}
	if c.Decimals != DefaultDecimals {
		t.Errorf("Decimals=%d, want %d", c.Decimals, DefaultDecimals)
	}
	if c.TokenAddress != "" {
		t.Errorf("TokenAddress should have NO default, got %q", c.TokenAddress)
	}
	if c.Configured() {
		t.Error("unconfigured (no token/key) must report Configured()==false — fail closed")
	}
}

func TestConfigLoadFromEnvOverrides(t *testing.T) {
	t.Setenv("HUSD_CHAIN_ID", "36962")
	t.Setenv("HUSD_RPC_URL", "http://localhost:19630/ext/bc/C/rpc")
	t.Setenv("HUSD_TOKEN_ADDRESS", "0xc57b7eCE2Ce2E74ef3Bc08Cfd5f5Fb41B6Ad4D66")
	t.Setenv("HUSD_TREASURY_KEY", "deadbeef")
	c := Config{}
	c.LoadFromEnv()
	if c.ChainID != 36962 {
		t.Errorf("ChainID=%d, want 36962", c.ChainID)
	}
	if !c.Configured() {
		t.Error("token+key set must report Configured()==true")
	}
}

func TestConfigLoadFromEnvPreservesSet(t *testing.T) {
	t.Setenv("HUSD_CHAIN_ID", "36962")
	c := Config{TokenAddress: "0xExplicit", ChainID: 99, Decimals: 8}
	c.LoadFromEnv()
	if c.ChainID != 99 || c.TokenAddress != "0xExplicit" || c.Decimals != 8 {
		t.Errorf("LoadFromEnv clobbered explicit fields: %+v", c)
	}
}

var _ = os.Getenv

func TestSettlementDrift(t *testing.T) {
	cases := []struct {
		name              string
		onchain, spend    int64
		threshold         int64
		wantDrift         int64
		wantSettle        bool
	}{
		{"usage drew down", 10000, 7000, 1, 3000, true},       // $30 consumed → settle
		{"nothing consumed", 10000, 10000, 1, 0, false},        // level → no settle
		{"below threshold", 10000, 9950, 100, 50, false},       // 50c < $1 threshold
		{"exactly threshold", 10000, 9900, 100, 100, true},     // 100c == threshold
		{"ledger ahead (unprojected mint)", 5000, 8000, 1, -3000, false}, // never over-sweep
		{"spendable negative clamps to 0", 4200, -10, 1, 4200, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, settle := SettlementDrift(c.onchain, c.spend, c.threshold)
			if d != c.wantDrift || settle != c.wantSettle {
				t.Fatalf("SettlementDrift(%d,%d,%d)=(%d,%v), want (%d,%v)",
					c.onchain, c.spend, c.threshold, d, settle, c.wantDrift, c.wantSettle)
			}
		})
	}
}
