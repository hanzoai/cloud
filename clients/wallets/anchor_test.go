package wallets

import (
	"math/big"
	"strings"
	"testing"

	"github.com/luxfi/crypto"
)

// TestRecoverableSig proves recoverableSig reconstructs the EVM recovery id from a
// bare r‖s ring signature so the 65-byte result recovers to the signer address —
// the exact fix the treasury anchor needs (the ring returns 64 bytes; EVM needs 65).
func TestRecoverableSig(t *testing.T) {
	for i := 0; i < 8; i++ { // exercise both parities
		priv, err := crypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		addr := crypto.PubkeyToAddress(priv.PublicKey).Hex()
		digest := crypto.Keccak256([]byte{byte(i), 'a', 'n', 'c', 'h', 'o', 'r'})
		full, err := crypto.Sign(digest, priv) // 65-byte r‖s‖v
		if err != nil {
			t.Fatal(err)
		}
		bare := full[:64] // drop v, as the ring does

		got, err := recoverableSig(digest, bare, addr)
		if err != nil {
			t.Fatalf("recoverableSig: %v", err)
		}
		if len(got) != 65 {
			t.Fatalf("recoverableSig len = %d, want 65", len(got))
		}
		pub, err := crypto.SigToPub(digest, got)
		if err != nil {
			t.Fatalf("recover: %v", err)
		}
		if !strings.EqualFold(crypto.PubkeyToAddress(*pub).Hex(), addr) {
			t.Fatalf("recovered %s, want %s", crypto.PubkeyToAddress(*pub).Hex(), addr)
		}
	}
}

// TestRecoverableSig_NoMatch fails closed when neither v recovers to the address.
func TestRecoverableSig_NoMatch(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	digest := crypto.Keccak256([]byte("x"))
	full, _ := crypto.Sign(digest, priv)
	if _, err := recoverableSig(digest, full[:64], "0x0000000000000000000000000000000000000001"); err == nil {
		t.Fatal("expected error for a signature that recovers to a different address")
	}
}

// TestRecoverableSig_LowSNormalization proves a HIGH-S ring signature is
// canonicalized to EIP-2 low-S AND still recovers to the signer (the fix for the
// anchor's "invalid sender" — geth rejects s > N/2).
func TestRecoverableSig_LowSNormalization(t *testing.T) {
	for i := 0; i < 8; i++ {
		priv, _ := crypto.GenerateKey()
		addr := crypto.PubkeyToAddress(priv.PublicKey).Hex()
		digest := crypto.Keccak256([]byte{byte(i), 'l', 'o', 'w', 's'})
		full, _ := crypto.Sign(digest, priv) // low-S 65-byte
		// Force HIGH-S: s' = N - s (the equivalent upper-half scalar).
		s := new(big.Int).SetBytes(full[32:64])
		s.Sub(secp256k1N, s)
		high := make([]byte, 64)
		copy(high[:32], full[:32])
		s.FillBytes(high[32:64])
		if new(big.Int).SetBytes(high[32:64]).Cmp(secp256k1HalfN) <= 0 {
			t.Fatal("test setup: expected high-S")
		}
		got, err := recoverableSig(digest, high, addr)
		if err != nil {
			t.Fatalf("recoverableSig(high-S): %v", err)
		}
		if new(big.Int).SetBytes(got[32:64]).Cmp(secp256k1HalfN) > 0 {
			t.Fatal("output is still high-S — not normalized")
		}
		pub, _ := crypto.SigToPub(digest, got)
		if !strings.EqualFold(crypto.PubkeyToAddress(*pub).Hex(), addr) {
			t.Fatalf("normalized sig recovers to %s, want %s", crypto.PubkeyToAddress(*pub).Hex(), addr)
		}
	}
}
