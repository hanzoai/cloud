package wallets

// anchor.go is the finance/treasury seam (HIP-0106): it turns the reserve's
// treasury MPC wallet into the on-chain anchor signer. The treasury subsystem
// binds this (treasury.BindAnchorSigner) so the Hanzo-L1 ledger-root anchor is
// committed by the reserve's quorum wallet, not a lone KMS key.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/luxfi/crypto"
)

const (
	reserveAccountName = "treasury"       // reserved account holding the reserve wallets
	reserveWalletName  = "reserve-anchor" // the reserved KindTreasury anchor wallet
)

// TreasuryAnchorSigner resolves-or-provisions the org's stable treasury MPC wallet
// and returns its EVM address plus a sign closure that produces an EVM-recoverable
// (r‖s‖v) signature delegating to the ring's threshold custody. ok=false when
// wallets is unmounted or treasury (ring) custody is not configured — the anchor
// then keeps its KMS-key signer (fail-safe, never a fabricated signer).
//
// The wallet is a KindTreasury wallet under a reserved account+name, so repeated
// calls resolve the SAME wallet (idempotent) — its address is stable, so funding
// it for gas once is durable.
func TreasuryAnchorSigner(ctx context.Context, org, chain string) (address string, sign func(context.Context, []byte) ([]byte, error), ok bool) {
	s := mounted
	if s == nil || strings.TrimSpace(org) == "" {
		return "", nil, false
	}
	cust, has := s.custody[KindTreasury]
	if !has {
		return "", nil, false
	}
	w, err := s.ensureReserveWallet(ctx, org, chain)
	if err != nil {
		s.log.Warn("wallets: treasury anchor signer provision failed", "org", org, "err", err)
		return "", nil, false
	}
	addr := w.Address
	return addr, func(ctx context.Context, hash []byte) ([]byte, error) {
		sig, err := cust.Sign(ctx, w, hash)
		if err != nil {
			return nil, err
		}
		// The ring returns a bare r‖s (64 bytes, no recovery id); an EVM tx needs
		// r‖s‖v. Recover v so the signature applies to a valid signed tx from addr.
		return recoverableSig(hash, sig, addr)
	}, true
}

// ensureReserveWallet resolves the org's reserve-anchor treasury wallet, creating
// it (account + wallet) on first call. Idempotent: a repeat call returns the
// existing wallet (matched by name) rather than minting a new ring key.
func (s *svc) ensureReserveWallet(ctx context.Context, org, chain string) (*Wallet, error) {
	ws, err := s.store.listWallets(ctx, org)
	if err != nil {
		return nil, err
	}
	for i := range ws {
		if ws[i].Custody == KindTreasury && ws[i].Name == reserveWalletName {
			w := ws[i]
			return &w, nil
		}
	}
	accts, err := s.store.listAccounts(ctx, org)
	if err != nil {
		return nil, err
	}
	var acctID string
	for i := range accts {
		if accts[i].Name == reserveAccountName {
			acctID = accts[i].ID
			break
		}
	}
	if acctID == "" {
		a := &Account{ID: newID("acct"), Org: org, Name: reserveAccountName, CreatedAt: time.Now().Unix()}
		if err := s.store.createAccount(ctx, a); err != nil {
			return nil, err
		}
		acctID = a.ID
	}
	cust := s.custody[KindTreasury]
	w := &Wallet{
		ID: newID("wal"), Org: org, AccountID: acctID, Name: reserveWalletName,
		Custody: KindTreasury, Tier: TierCold, Chain: chain, CreatedAt: time.Now().Unix(),
	}
	addr, err := cust.Provision(ctx, w) // sets w.KeyRef (the ring wallet id)
	if err != nil {
		return nil, err
	}
	w.Address = addr
	if err := s.store.createWallet(ctx, w); err != nil {
		return nil, err
	}
	s.log.Info("wallets: provisioned reserve treasury anchor wallet", "org", org, "address", addr)
	return w, nil
}

// recoverableSig turns a bare (r‖s) threshold signature into a 65-byte
// EVM-recoverable signature (r‖s‖v) by finding the v ∈ {0,1} whose recovery over
// hash yields addr. The luxfi/mpc ring returns r‖s WITHOUT the recovery id; EVM tx
// signing (tx.WithSignature) needs it. Fails closed if neither v recovers to addr.
func recoverableSig(hash, sig []byte, addr string) ([]byte, error) {
	if len(sig) == 65 {
		return sig, nil // already recoverable
	}
	if len(sig) != 64 {
		return nil, fmt.Errorf("wallets: unexpected signature length %d (want 64 or 65)", len(sig))
	}
	want := strings.ToLower(strings.TrimPrefix(addr, "0x"))
	for v := byte(0); v < 2; v++ {
		full := make([]byte, 65)
		copy(full, sig)
		full[64] = v
		pub, err := crypto.SigToPub(hash, full)
		if err != nil {
			continue
		}
		if strings.ToLower(strings.TrimPrefix(crypto.PubkeyToAddress(*pub).Hex(), "0x")) == want {
			return full, nil
		}
	}
	return nil, fmt.Errorf("wallets: no recovery id recovers signature to %s", addr)
}
