// SPDX-License-Identifier: Apache-2.0 OR MIT

package x402

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
)

// SettlementProof is returned by an independent x402 Facilitator upon executing
// a non-custodial stablecoin transfer.
type SettlementProof struct {
	FacilitatorID string `json:"facilitator_id"`
	TxHash        string `json:"tx_hash"`
	ChainID       string `json:"chain_id"`
	Asset         string `json:"asset"`
	From          string `json:"from"`
	To            string `json:"to"`
	AmountAtomic  uint64 `json:"amount_atomic"`
	SettledAtUnix int64  `json:"settled_at_unix"`
}

// ChainSettlementState is queried during asynchronous receipt reconciliation to
// detect settlement reversals, chain reorgs, or facilitator discrepancies.
type ChainSettlementState struct {
	Found        bool   `json:"found"`
	Finalized    bool   `json:"finalized"`
	Reverted     bool   `json:"reverted"`
	AmountAtomic uint64 `json:"amount_atomic"`
	To           string `json:"to"`
	Reason       string `json:"reason,omitempty"`
}

// Facilitator abstracts any independent x402 payment relayer/facilitator
// (direct permissionless ERC-3009 relayer, self-hosted provider node, or an
// optional Circle-compatible facilitator). None is a mandatory intermediary.
type Facilitator interface {
	ID() string
	VerifyHold(ctx context.Context, auth PaymentAuthorization) (holdID string, err error)
	SettleHold(ctx context.Context, holdID string) (*SettlementProof, error)
	ReleaseHold(ctx context.Context, holdID string, reason string) error
	QuerySettlement(ctx context.Context, txHash string) (ChainSettlementState, error)
}

type memHold struct {
	auth     PaymentAuthorization
	settled  bool
	released bool
	txHash   string
}

// IndependentERC3009Facilitator is a self-contained, independently operable
// x402 facilitator verifying ERC-3009 authorizations directly without any
// Hanzo-hosted or Circle-hosted intermediary. It supports deterministic fault
// and reversal injection for protocol conformance testing.
type IndependentERC3009Facilitator struct {
	id          string
	nowFn       func() int64
	mu          sync.Mutex
	holds       map[string]*memHold
	settlements map[string]ChainSettlementState
	usedNonces  map[string]string
}

// NewIndependentERC3009Facilitator creates an independent ERC-3009 facilitator
// (for example "direct-erc3009" or "circle-usdc-opt").
func NewIndependentERC3009Facilitator(id string, nowFn func() int64) *IndependentERC3009Facilitator {
	return &IndependentERC3009Facilitator{
		id:          id,
		nowFn:       nowFn,
		holds:       make(map[string]*memHold),
		settlements: make(map[string]ChainSettlementState),
		usedNonces:  make(map[string]string),
	}
}

func (f *IndependentERC3009Facilitator) ID() string { return f.id }

func (f *IndependentERC3009Facilitator) VerifyHold(_ context.Context, auth PaymentAuthorization) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	nonceKey := auth.ChainID + "|" + auth.Asset + "|" + auth.From + "|" + auth.Nonce
	if prev, exists := f.usedNonces[nonceKey]; exists {
		return "", fmt.Errorf("facilitator %s: ERC-3009 nonce already consumed by %s", f.id, prev)
	}

	h := sha256.Sum256(append([]byte(f.id+"|"), auth.ERC3009Digest()...))
	holdID := "hold_" + hex.EncodeToString(h[:12])
	f.usedNonces[nonceKey] = holdID
	f.holds[holdID] = &memHold{auth: auth}
	return holdID, nil
}

func (f *IndependentERC3009Facilitator) SettleHold(_ context.Context, holdID string) (*SettlementProof, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	hold, ok := f.holds[holdID]
	if !ok {
		return nil, errors.New("unknown hold_id")
	}
	if hold.released {
		return nil, errors.New("hold already released")
	}
	if !hold.settled {
		txDigest := sha256.Sum256([]byte("tx|" + f.id + "|" + holdID))
		hold.txHash = "0x" + hex.EncodeToString(txDigest[:])
		hold.settled = true
		f.settlements[hold.txHash] = ChainSettlementState{
			Found:        true,
			Finalized:    true,
			Reverted:     false,
			AmountAtomic: hold.auth.ValueAtomic,
			To:           hold.auth.To,
		}
	}
	return &SettlementProof{
		FacilitatorID: f.id,
		TxHash:        hold.txHash,
		ChainID:       hold.auth.ChainID,
		Asset:         hold.auth.Asset,
		From:          hold.auth.From,
		To:            hold.auth.To,
		AmountAtomic:  hold.auth.ValueAtomic,
		SettledAtUnix: f.nowFn(),
	}, nil
}

func (f *IndependentERC3009Facilitator) ReleaseHold(_ context.Context, holdID string, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	hold, ok := f.holds[holdID]
	if !ok {
		return nil
	}
	if hold.settled {
		return errors.New("cannot release an already settled hold")
	}
	hold.released = true
	nonceKey := hold.auth.ChainID + "|" + hold.auth.Asset + "|" + hold.auth.From + "|" + hold.auth.Nonce
	delete(f.usedNonces, nonceKey)
	return nil
}

func (f *IndependentERC3009Facilitator) QuerySettlement(_ context.Context, txHash string) (ChainSettlementState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	st, ok := f.settlements[txHash]
	if !ok {
		return ChainSettlementState{Found: false, Reason: "transaction not found on chain"}, nil
	}
	return st, nil
}

// SimulateChainReversal marks txHash as reverted on chain (e.g., L2 reorg or
// facilitator reversal) so reconciliation tests verify reversal detection.
func (f *IndependentERC3009Facilitator) SimulateChainReversal(txHash string, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.settlements[txHash]
	st.Found = true
	st.Finalized = false
	st.Reverted = true
	st.Reason = reason
	f.settlements[txHash] = st
}
