package blockchain

import (
	"context"
	"errors"
	"math/big"
)

// TokenTransfer describes a treasury-signed ERC-20 transfer on an EVM chain.
//
// All on-chain parameters (RPC endpoint, token contract, treasury key) are
// supplied by the caller, which sources them from KMS (KMSSecret CRD → k8s
// Secret → env). TreasuryKey is used in-memory only by the registered
// implementation; it is never persisted and never logged.
type TokenTransfer struct {
	ChainID      int64    // EVM chain id (e.g. 36900 Hanzo)
	RPCURL       string   // JSON-RPC endpoint (e.g. https://rpc.hanzo.network)
	TokenAddress string   // ERC-20 contract address (0x…)
	TreasuryKey  string   // hex private key (with or without 0x); from KMS, never persisted
	To           string   // recipient address (0x…)
	AmountWei    *big.Int // amount in the token's base units
	GasLimit     uint64   // 0 → implementation default
}

// TokenTransferFn signs and submits an ERC-20 transfer, returning the tx hash.
type TokenTransferFn func(ctx context.Context, t TokenTransfer) (txHash string, err error)

// tokenTransferFn is the registered EVM token-transfer implementation. It is
// wired by the github.com/hanzoai/cloud/clients/commerce/luxfi/cevm sub-module on
// import, mirroring RegisterPayment — consumers that never touch EVM never
// link luxfi/geth.
var tokenTransferFn TokenTransferFn

// RegisterTokenTransfer wires the ERC-20 transfer implementation. Call once at
// init time; later registrations override earlier ones.
func RegisterTokenTransfer(fn TokenTransferFn) { tokenTransferFn = fn }

// TokenTransferRegistered reports whether an implementation is wired. Callers
// can use this to fail fast (or skip) before attempting an on-chain payout.
func TokenTransferRegistered() bool { return tokenTransferFn != nil }

// ErrNoTokenTransfer is returned when TransferToken is called without a
// registered implementation (the luxfi/cevm sub-module not imported).
var ErrNoTokenTransfer = errors.New("blockchain: no ERC-20 token transfer registered (import github.com/hanzoai/cloud/clients/commerce/luxfi/cevm)")

// TransferToken validates the request and dispatches to the registered EVM
// implementation. It is the single entry point for treasury ERC-20 transfers.
func TransferToken(ctx context.Context, t TokenTransfer) (string, error) {
	if t.RPCURL == "" || t.TokenAddress == "" || t.TreasuryKey == "" || t.To == "" {
		return "", errors.New("blockchain: token transfer missing required field (rpc/token/key/to)")
	}
	if t.AmountWei == nil || t.AmountWei.Sign() <= 0 {
		return "", errors.New("blockchain: token transfer amount must be positive")
	}
	if tokenTransferFn == nil {
		return "", ErrNoTokenTransfer
	}
	return tokenTransferFn(ctx, t)
}
