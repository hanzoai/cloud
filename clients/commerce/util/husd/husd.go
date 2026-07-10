// Package husd is the ONE place that configures on-chain HUSD (Hanzo USD
// stablecoin, ERC-20 on the Hanzo EVM) operations and converts USD cents to and
// from the token's base units.
//
// It is a leaf package (stdlib only): no datastore, no gin, no geth. Both the
// OSS-contributor payout executor (cron/payout/contributor) and the chain-backed
// credit ledger (treasury, billing/husdindex) source their HUSD configuration
// and cent↔wei math from here, so there is exactly one definition of what "HUSD"
// means and how a USD amount maps to the chain — never a second, drifting copy.
//
// TreasuryKey is held in memory only — never persisted, never logged.
package husd

import (
	"errors"
	"fmt"
	"math/big"
	"os"
	"strconv"
)

const (
	// DefaultChainID is the Hanzo EVM mainnet chain id (36963). Testnet (36962)
	// and devnet (36964) are selected by setting HUSD_CHAIN_ID + HUSD_RPC_URL
	// from KMS.
	DefaultChainID int64 = 36963
	// DefaultRPCURL is the Hanzo EVM mainnet C-chain JSON-RPC endpoint.
	DefaultRPCURL = "https://api.hanzo.network/ext/bc/C/rpc"
	// DefaultDecimals is the HUSD token's decimals (18, standard ERC-20).
	DefaultDecimals = 18
)

// ErrNotConfigured is returned when the HUSD token address or treasury key is
// missing. Operations fail closed: with no token address (mainnet has no
// default) an unset deploy can never mis-target or mint against the wrong chain.
var ErrNotConfigured = errors.New(
	"husd: on-chain HUSD not configured (set HUSD_TOKEN_ADDRESS + HUSD_TREASURY_KEY in KMS)")

// Config configures on-chain HUSD stablecoin operations. Values are sourced from
// the environment, which is populated from KMS (KMSSecret CRD → k8s Secret →
// env). TreasuryKey is held in memory only — never persisted, never logged.
type Config struct {
	ChainID      int64
	RPCURL       string
	TokenAddress string
	Decimals     int
	TreasuryKey  string
	GasLimit     uint64
}

// LoadFromEnv fills any unset field from the environment (KMS-injected).
// Already-set fields are preserved, so callers (and tests) may supply values
// directly. The token address has NO default (greenfield, KMS-only) so an unset
// HUSD_TOKEN_ADDRESS fails closed instead of silently targeting mainnet.
func (c *Config) LoadFromEnv() {
	if c.ChainID == 0 {
		c.ChainID = envInt64("HUSD_CHAIN_ID", DefaultChainID)
	}
	if c.RPCURL == "" {
		c.RPCURL = envStr("HUSD_RPC_URL", DefaultRPCURL)
	}
	if c.TokenAddress == "" {
		c.TokenAddress = os.Getenv("HUSD_TOKEN_ADDRESS")
	}
	if c.Decimals == 0 {
		c.Decimals = int(envInt64("HUSD_TOKEN_DECIMALS", DefaultDecimals))
	}
	if c.TreasuryKey == "" {
		c.TreasuryKey = os.Getenv("HUSD_TREASURY_KEY")
	}
	if c.GasLimit == 0 {
		c.GasLimit = uint64(envInt64("HUSD_GAS_LIMIT", 0))
	}
}

// Configured reports whether the on-chain path can execute: a token contract and
// a treasury signing key are both present.
func (c *Config) Configured() bool {
	return c.TokenAddress != "" && c.TreasuryKey != ""
}

// CentsToWei converts USD cents into HUSD base units for a token with the given
// decimals: wei = cents * 10^(decimals-2). e.g. 18 decimals, 100 cents ($1.00)
// → 1e18. Decimals must be >= 2 (a stablecoin never has fewer than 2) and cents
// must be non-negative.
func CentsToWei(cents int64, decimals int) (*big.Int, error) {
	if decimals < 2 {
		return nil, fmt.Errorf("husd: decimals must be >= 2, got %d", decimals)
	}
	if cents < 0 {
		return nil, fmt.Errorf("husd: cents must be non-negative, got %d", cents)
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals-2)), nil)
	return new(big.Int).Mul(big.NewInt(cents), scale), nil
}

// WeiToCents converts HUSD base units back to whole USD cents, truncating any
// sub-cent remainder (which is returned separately). It is the inverse of
// CentsToWei for exact multiples: cents = wei / 10^(decimals-2). The remainder
// is reported so callers indexing on-chain balances can assert no dust is lost
// silently. Decimals must be >= 2; wei must be non-negative.
func WeiToCents(wei *big.Int, decimals int) (cents int64, remainderWei *big.Int, err error) {
	if decimals < 2 {
		return 0, nil, fmt.Errorf("husd: decimals must be >= 2, got %d", decimals)
	}
	if wei == nil || wei.Sign() < 0 {
		return 0, nil, fmt.Errorf("husd: wei must be non-negative")
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals-2)), nil)
	q, r := new(big.Int).QuoRem(wei, scale, new(big.Int))
	if !q.IsInt64() {
		return 0, nil, fmt.Errorf("husd: balance %s cents overflows int64", q)
	}
	return q.Int64(), r, nil
}

// SettlementDrift computes how much HUSD to sweep from an org's on-chain address
// back to the treasury so its on-chain balance tracks its off-chain ledger.
//
//	drift = onChainCents − max(0, spendableCents)
//
// i.e. the value drawn down by off-chain metered usage (and any reclaimed/expired
// grants) that the chain has not yet reflected (mints put HUSD on the org address;
// usage happens off-chain, so the chain over-states the balance until settled).
// settle is true only when drift ≥ max(1, thresholdCents), so dust never churns
// gas. A non-positive drift — the ledger is level with, or AHEAD of, the chain (an
// un-projected mint) — never settles: settling then would over-sweep, so the
// indexer must catch up first.
func SettlementDrift(onChainCents, spendableCents, thresholdCents int64) (drift int64, settle bool) {
	target := spendableCents
	if target < 0 {
		target = 0
	}
	drift = onChainCents - target
	if drift <= 0 {
		return drift, false
	}
	if thresholdCents < 1 {
		thresholdCents = 1
	}
	return drift, drift >= thresholdCents
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt64(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}
