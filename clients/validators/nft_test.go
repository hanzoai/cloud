// Copyright (C) 2019-2026, Hanzo Industries Inc. All rights reserved.

package validators

import (
	"context"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/luxfi/crypto"
	"github.com/luxfi/geth/common"
)

// reader builds the live-mainnet reader shape used across these tests.
func reader(t *testing.T) *nftReader {
	t.Helper()
	r, err := newNFTReader("https://ethereum-rpc.publicnode.com", GenesisNFTContract, 100)
	if err != nil {
		t.Fatalf("newNFTReader: %v", err)
	}
	return r
}

// TestValidatorTier pins the slot bound. A token outside the Validator tier is
// refused before any signature is asked for, so a Wallet-tier or ATM token can
// never buy validator onboarding.
func TestValidatorTier(t *testing.T) {
	r := reader(t)
	for _, tc := range []struct {
		token uint64
		want  bool
	}{{0, false}, {1, true}, {50, true}, {100, true}, {101, false}, {1 << 40, false}} {
		if got := r.isValidatorTier(tc.token); got != tc.want {
			t.Errorf("isValidatorTier(%d) = %v, want %v", tc.token, got, tc.want)
		}
	}
}

// TestObservationIsSelfDescribing proves an observation names its own subject even
// when the read fails. A fact that cannot say which collection and token it is
// about could be misfiled against another one.
func TestObservationIsSelfDescribing(t *testing.T) {
	r := reader(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	cancel() // force the dial/read to fail immediately

	obs, err := r.ownerOf(ctx, 7)
	if err == nil {
		t.Skip("expected the cancelled read to fail; a live RPC answered instead")
	}
	if obs.Token != 7 {
		t.Errorf("observation lost its token: got %d, want 7", obs.Token)
	}
	if !strings.EqualFold(obs.Collection, GenesisNFTContract) {
		t.Errorf("observation lost its collection: got %q, want %q", obs.Collection, GenesisNFTContract)
	}
}

// TestPinnedContract guards the address the whole entitlement hangs on. It is the
// live mainnet Genesis collection — never the wiped C-Chain copy and never the
// GaugeController address that has been mistaken for it before.
func TestPinnedContract(t *testing.T) {
	if !strings.EqualFold(GenesisNFTContract, "0x31e0F919C67ceDd2Bc3E294340Dc900735810311") {
		t.Fatalf("the pinned Genesis collection changed: %s", GenesisNFTContract)
	}
	r := reader(t)
	if !strings.EqualFold(r.contract.Hex(), GenesisNFTContract) {
		t.Fatalf("reader points at %s, not the pinned collection", r.contract.Hex())
	}
}

// TestChallengeBindsOrgSlotNonce proves the signed message cannot be replayed
// across organizations, slots or sessions — the message is rebuilt server-side
// from validated facts, so a signature obtained for one context is useless in
// another.
func TestChallengeBindsOrgSlotNonce(t *testing.T) {
	base := challengeMessage("acme", 7, "n1")
	for name, other := range map[string]string{
		"different org":   challengeMessage("evil", 7, "n1"),
		"different slot":  challengeMessage("acme", 8, "n1"),
		"different nonce": challengeMessage("acme", 7, "n2"),
	} {
		if other == base {
			t.Errorf("%s produced an identical message, so a signature would replay", name)
		}
	}
}

// TestRecoverSignerProvesWalletControl is the wallet-control half of the
// entitlement: only the holder of the key can produce the signature the server
// recovers, and any edit to the message recovers a different address.
func TestRecoverSignerProvesWalletControl(t *testing.T) {
	// A deterministic test wallet, so the assertion is stable and reviewable.
	key, err := crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
	if err != nil {
		t.Fatalf("test key: %v", err)
	}
	wallet := common.Address(crypto.PubkeyToAddress(key.PublicKey))
	msg := challengeMessage("acme", 7, "n1")

	sig, err := crypto.Sign(personalSignHash(msg), key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	sig[64] += 27 // the v ∈ {27,28} form window.ethereum returns

	got, err := recoverSigner(msg, "0x"+hex.EncodeToString(sig))
	if err != nil {
		t.Fatalf("recoverSigner: %v", err)
	}
	if got != wallet {
		t.Fatalf("recovered %s, want the signing wallet %s", got.Hex(), wallet.Hex())
	}

	// A signature over a different slot must not recover the same wallet, or a
	// signature for one slot would authorize another.
	if other, err := recoverSigner(challengeMessage("acme", 8, "n1"), "0x"+hex.EncodeToString(sig)); err == nil && other == wallet {
		t.Fatal("a signature survived a changed slot")
	}
}

// TestLiveOwnerOf is the only test that touches Ethereum. It asserts the read is
// PINNED: an unpinned answer ("latest") cannot be re-checked by anyone else, which
// is exactly what makes an attestation over it worth signing.
func TestLiveOwnerOf(t *testing.T) {
	if os.Getenv("VALIDATORS_LIVE_ETH") != "1" {
		t.Skip("live ETH read: set VALIDATORS_LIVE_ETH=1 to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	obs, err := reader(t).ownerOf(ctx, 1)
	if err != nil {
		t.Fatalf("live ownerOf(1): %v", err)
	}
	if obs.Owner == (common.Address{}) {
		t.Fatal("live ownerOf(1) returned the zero address")
	}
	if obs.Chain != 1 {
		t.Fatalf("live read reported chain %d, want Ethereum mainnet (1)", obs.Chain)
	}
	if obs.Block == 0 {
		t.Fatal("live read is not pinned to a block, so the fact is not citable")
	}
	t.Logf("live ownerOf(1) on %s = %s at chain %d block %d",
		GenesisNFTContract, obs.Owner.Hex(), obs.Chain, obs.Block)
}
