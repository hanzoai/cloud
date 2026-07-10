package contributor

import (
	"context"
	"errors"
	"math/big"
	"regexp"
	"testing"

	contribModel "github.com/hanzoai/cloud/clients/commerce/models/contributor"
	"github.com/hanzoai/cloud/clients/commerce/util/blockchain"
)

// realTxHash is a syntactically valid 32-byte EVM transaction hash.
const realTxHash = "0xabcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"

var txHashShape = regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)

// --- husdCentsToWei ---

func TestHusdCentsToWei(t *testing.T) {
	cases := []struct {
		cents    int64
		decimals int
		want     string
	}{
		{100, 18, "1000000000000000000"},   // $1.00 → 1e18 (18-decimal stablecoin)
		{2550, 18, "25500000000000000000"}, // $25.50
		{100, 6, "1000000"},                // $1.00 → 1e6 (USDC-style 6 decimals)
		{0, 18, "0"},
	}
	for _, c := range cases {
		got, err := husdCentsToWei(c.cents, c.decimals)
		if err != nil {
			t.Fatalf("husdCentsToWei(%d,%d): %v", c.cents, c.decimals, err)
		}
		if got.String() != c.want {
			t.Errorf("husdCentsToWei(%d,%d)=%s, want %s", c.cents, c.decimals, got, c.want)
		}
	}
	if _, err := husdCentsToWei(100, 1); err == nil {
		t.Error("expected error for decimals < 2")
	}
	if _, err := husdCentsToWei(-1, 18); err == nil {
		t.Error("expected error for negative cents")
	}
}

// --- executeCryptoPayout: on-chain HUSD path ---

func TestExecuteCryptoPayout_OnChain(t *testing.T) {
	var seen blockchain.TokenTransfer
	blockchain.RegisterTokenTransfer(func(_ context.Context, tt blockchain.TokenTransfer) (string, error) {
		seen = tt
		return realTxHash, nil
	})
	t.Cleanup(func() { blockchain.RegisterTokenTransfer(nil) })

	c := &contribModel.Contributor{
		GitLogin:     "alice",
		PayoutMethod: "crypto",
		PayoutTarget: "0x00000000000000000000000000000000000000ff",
	}
	alloc := contribModel.PayoutAllocation{GitLogin: "alice", AmountCents: 2550} // $25.50
	cfg := Config{HUSD: HUSDConfig{
		ChainID:      36900,
		RPCURL:       "https://rpc.hanzo.network",
		TokenAddress: "0x000000000000000000000000000000000000dead",
		TreasuryKey:  "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef0",
		Decimals:     18,
	}}

	txHash, err := executeCryptoPayout(context.Background(), c, alloc, cfg)
	if err != nil {
		t.Fatalf("executeCryptoPayout: %v", err)
	}
	if txHash != realTxHash {
		t.Errorf("txHash=%s, want %s", txHash, realTxHash)
	}
	if !txHashShape.MatchString(txHash) {
		t.Errorf("txHash %q is not a 32-byte hash shape", txHash)
	}

	// cents→wei: 2550 cents at 18 decimals = 2550 * 1e16 = 2.55e19.
	wantWei, _ := new(big.Int).SetString("25500000000000000000", 10)
	if seen.AmountWei == nil || seen.AmountWei.Cmp(wantWei) != 0 {
		t.Errorf("AmountWei=%v, want %v", seen.AmountWei, wantWei)
	}
	if seen.To != c.PayoutTarget {
		t.Errorf("To=%s, want %s", seen.To, c.PayoutTarget)
	}
	if seen.ChainID != 36900 {
		t.Errorf("ChainID=%d, want 36900", seen.ChainID)
	}
	if seen.TokenAddress != cfg.HUSD.TokenAddress {
		t.Errorf("TokenAddress=%s, want %s", seen.TokenAddress, cfg.HUSD.TokenAddress)
	}
}

func TestExecuteCryptoPayout_NotConfigured(t *testing.T) {
	c := &contribModel.Contributor{GitLogin: "a", PayoutMethod: "crypto", PayoutTarget: "0xabc"}
	cfg := Config{HUSD: HUSDConfig{ChainID: 36900, Decimals: 18}} // no token/key
	if _, err := executeCryptoPayout(context.Background(), c, contribModel.PayoutAllocation{AmountCents: 100}, cfg); !errors.Is(err, ErrHUSDNotConfigured) {
		t.Fatalf("want ErrHUSDNotConfigured, got %v", err)
	}
}

func TestExecuteCryptoPayout_NoWallet(t *testing.T) {
	c := &contribModel.Contributor{GitLogin: "a", PayoutMethod: "crypto"} // no PayoutTarget
	cfg := Config{HUSD: HUSDConfig{TokenAddress: "0xT", TreasuryKey: "k", Decimals: 18}}
	if _, err := executeCryptoPayout(context.Background(), c, contribModel.PayoutAllocation{AmountCents: 100}, cfg); err == nil {
		t.Fatal("expected error for missing wallet address")
	}
}

func TestExecuteCryptoPayout_NoImplementationRegistered(t *testing.T) {
	blockchain.RegisterTokenTransfer(nil) // simulate thirdparty/ethereum not linked
	c := &contribModel.Contributor{GitLogin: "a", PayoutMethod: "crypto", PayoutTarget: "0xabc"}
	cfg := Config{HUSD: HUSDConfig{ChainID: 36900, RPCURL: "https://rpc.hanzo.network", TokenAddress: "0xT", TreasuryKey: "k", Decimals: 18}}
	_, err := executeCryptoPayout(context.Background(), c, contribModel.PayoutAllocation{AmountCents: 100}, cfg)
	if !errors.Is(err, blockchain.ErrNoTokenTransfer) {
		t.Fatalf("want ErrNoTokenTransfer, got %v", err)
	}
}

// --- HUSDConfig env loading ---

func TestHUSDConfig_LoadFromEnvDefaults(t *testing.T) {
	for _, k := range []string{"HUSD_CHAIN_ID", "HUSD_RPC_URL", "HUSD_TOKEN_ADDRESS", "HUSD_TOKEN_DECIMALS", "HUSD_TREASURY_KEY", "HUSD_GAS_LIMIT"} {
		t.Setenv(k, "")
	}
	h := HUSDConfig{}
	h.LoadFromEnv()
	if h.ChainID != defaultHUSDChainID {
		t.Errorf("ChainID=%d, want %d", h.ChainID, defaultHUSDChainID)
	}
	if h.RPCURL != defaultHUSDRPCURL {
		t.Errorf("RPCURL=%s, want %s", h.RPCURL, defaultHUSDRPCURL)
	}
	if h.Decimals != defaultHUSDDecimals {
		t.Errorf("Decimals=%d, want %d", h.Decimals, defaultHUSDDecimals)
	}
	if h.TokenAddress != "" || h.TreasuryKey != "" {
		t.Error("token address / treasury key must NOT have defaults (greenfield, KMS-only)")
	}
}

func TestHUSDConfig_LoadFromEnvOverrides(t *testing.T) {
	t.Setenv("HUSD_CHAIN_ID", "1234")
	t.Setenv("HUSD_RPC_URL", "https://rpc.example")
	t.Setenv("HUSD_TOKEN_ADDRESS", "0xToken")
	t.Setenv("HUSD_TOKEN_DECIMALS", "6")
	t.Setenv("HUSD_TREASURY_KEY", "abc123")
	t.Setenv("HUSD_GAS_LIMIT", "120000")
	h := HUSDConfig{}
	h.LoadFromEnv()
	if h.ChainID != 1234 || h.RPCURL != "https://rpc.example" || h.TokenAddress != "0xToken" ||
		h.Decimals != 6 || h.TreasuryKey != "abc123" || h.GasLimit != 120000 {
		t.Errorf("env not applied: %+v", h)
	}
}

func TestHUSDConfig_LoadFromEnvPreservesSet(t *testing.T) {
	t.Setenv("HUSD_TOKEN_ADDRESS", "0xFromEnv")
	h := HUSDConfig{TokenAddress: "0xExplicit", ChainID: 99, Decimals: 8}
	h.LoadFromEnv()
	if h.TokenAddress != "0xExplicit" {
		t.Errorf("explicitly-set field overwritten: %s", h.TokenAddress)
	}
	if h.ChainID != 99 || h.Decimals != 8 {
		t.Errorf("explicit values not preserved: %+v", h)
	}
}
