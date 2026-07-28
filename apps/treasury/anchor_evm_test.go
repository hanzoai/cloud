package treasury

import (
	"context"
	"math/big"
	"testing"

	"github.com/luxfi/crypto"
	"github.com/luxfi/geth/common"
	"github.com/luxfi/geth/core/types"
)

// TestAnchorSignerSeam proves the anchor's signing was decoupled from a single
// in-process key into a quorum-gateable seam WITHOUT changing the on-chain result:
//   - keySigner (the local KMS key path) still produces a tx that recovers to its
//     address — byte-identical to the reference types.SignTx.
//   - a bound quorum signer (BindAnchorSigner — the reserve's treasury MPC wallet)
//     WINS over any local key, is invoked exactly once, and its tx recovers to the
//     bound address. This is the "single-signer → MPC treasury sign, quorum-gated"
//     path, exercised with a stand-in ring signer (the real ring is a config swap).
func TestAnchorSignerSeam(t *testing.T) {
	chainID := big.NewInt(defaultHanzoChainID)
	evmSigner := types.LatestSignerForChainID(chainID)
	to := common.HexToAddress("0x00112233445566778899aabbccddeeff00112233")
	newTx := func() *types.Transaction {
		return types.NewTx(&types.LegacyTx{
			Nonce: 7, To: &to, Value: big.NewInt(0),
			Gas: anchorGasLimit, GasPrice: big.NewInt(1_000_000_000), Data: append(selfTxMagic, []byte("root")...),
		})
	}

	// ── keySigner: seam output == reference types.SignTx, recovers to address ──
	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	ks := keySigner{priv: priv}
	sig, err := ks.signHash(context.Background(), evmSigner.Hash(newTx()).Bytes())
	if err != nil {
		t.Fatalf("keySigner sign: %v", err)
	}
	signed, err := newTx().WithSignature(evmSigner, sig)
	if err != nil {
		t.Fatalf("apply signature: %v", err)
	}
	sender, err := types.Sender(evmSigner, signed)
	if err != nil {
		t.Fatalf("recover sender: %v", err)
	}
	if sender != ks.address() {
		t.Fatalf("keySigner tx recovered to %s, want %s", sender, ks.address())
	}
	ref, err := types.SignTx(newTx(), evmSigner, priv)
	if err != nil {
		t.Fatalf("reference SignTx: %v", err)
	}
	if signed.Hash() != ref.Hash() {
		t.Fatalf("seam-signed tx %s != reference SignTx %s", signed.Hash().Hex(), ref.Hash().Hex())
	}

	// ── mpcSigner (quorum-gated) via BindAnchorSigner wins over the local key ──
	ringPriv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("gen ring key: %v", err)
	}
	ringAddr := common.Address(crypto.PubkeyToAddress(ringPriv.PublicKey))
	called := 0
	BindAnchorSigner(ringAddr, func(_ context.Context, hash []byte) ([]byte, error) {
		called++
		return crypto.Sign(hash, ringPriv)
	})
	t.Cleanup(func() { BindAnchorSigner(common.Address{}, nil) })

	a := &anchorer{} // NO local key: only the bound quorum signer is available
	sgn, err := a.signer()
	if err != nil {
		t.Fatalf("signer() with bound quorum signer: %v", err)
	}
	if sgn.address() != ringAddr {
		t.Fatalf("bound signer address %s, want ring %s", sgn.address(), ringAddr)
	}
	rsig, err := sgn.signHash(context.Background(), evmSigner.Hash(newTx()).Bytes())
	if err != nil {
		t.Fatalf("mpc sign: %v", err)
	}
	rsigned, err := newTx().WithSignature(evmSigner, rsig)
	if err != nil {
		t.Fatalf("apply mpc signature: %v", err)
	}
	rsender, err := types.Sender(evmSigner, rsigned)
	if err != nil {
		t.Fatalf("recover mpc sender: %v", err)
	}
	if rsender != ringAddr {
		t.Fatalf("quorum-signed tx recovered to %s, want ring %s", rsender, ringAddr)
	}
	if called != 1 {
		t.Fatalf("quorum signer invoked %d times, want exactly 1", called)
	}

	// configured() must hold with a bound signer even with no local key (rpc set).
	a.rpcURL = "http://hanzod-rpc-internal/v1/bc/C/rpc"
	if !a.configured() {
		t.Fatal("bound quorum signer must satisfy configured() without a local key")
	}
}
