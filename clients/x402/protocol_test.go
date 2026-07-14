package x402

import (
	"crypto/ecdsa"
	"encoding/hex"
	"testing"

	"github.com/luxfi/crypto"
)

// sampleReq is a canonical challenge over a fixed token domain.
func sampleReq(payee string) PaymentRequirements {
	return PaymentRequirements{
		Version: Version, Scheme: Scheme, Resource: "/paid/tool",
		Network: "hanzo", ChainID: 36963,
		Token: "0x1111111111111111111111111111111111111111",
		Payee: payee, Amount: "1000000", ValidFor: 300,
	}
}

// signProof builds and EIP-712-signs a Proof for req with key, over the given
// window + nonce — exactly what a compliant client does.
func signProof(t *testing.T, key *ecdsa.PrivateKey, req PaymentRequirements, nonce string, validAfter, validBefore int64) Proof {
	t.Helper()
	p := Proof{
		From: crypto.PubkeyToAddress(key.PublicKey).Hex(), To: req.Payee, Value: req.Amount,
		ValidAfter: validAfter, ValidBefore: validBefore, Nonce: nonce,
	}
	digest, err := eip712Digest(req, p, DefaultTokenName, DefaultTokenVersion)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	sig, err := crypto.Sign(digest[:], key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	p.Signature = "0x" + hex.EncodeToString(sig)
	return p
}

func TestVerifyRoundTrip(t *testing.T) {
	key, _ := crypto.GenerateKey()
	req := sampleReq("0x2222222222222222222222222222222222222222")
	const now = int64(10_000)
	p := signProof(t, key, req, "0xabc123", now-1, now+300)

	if err := Verify(req, p, DefaultTokenName, DefaultTokenVersion, now); err != nil {
		t.Fatalf("valid proof rejected: %v", err)
	}
}

func TestVerifyRejectsTampering(t *testing.T) {
	key, _ := crypto.GenerateKey()
	other, _ := crypto.GenerateKey()
	req := sampleReq("0x2222222222222222222222222222222222222222")
	const now = int64(10_000)
	good := signProof(t, key, req, "0xdeadbeef", now-1, now+300)

	cases := []struct {
		name  string
		mut   func(p *Proof, r *PaymentRequirements)
		nowAt int64
	}{
		{"amount tampered", func(p *Proof, _ *PaymentRequirements) { p.Value = "999" }, now},
		{"payee tampered", func(p *Proof, _ *PaymentRequirements) { p.To = "0x3333333333333333333333333333333333333333" }, now},
		{"required amount differs from signed", func(_ *Proof, r *PaymentRequirements) { r.Amount = "500" }, now},
		{"required payee differs from signed", func(_ *Proof, r *PaymentRequirements) { r.Payee = "0x4444444444444444444444444444444444444444" }, now},
		{"expired", func(_ *Proof, _ *PaymentRequirements) {}, now + 10_000},
		{"not yet valid", func(p *Proof, _ *PaymentRequirements) { p.ValidAfter = now + 500 }, now},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, r := good, req
			tc.mut(&p, &r)
			if err := Verify(r, p, DefaultTokenName, DefaultTokenVersion, tc.nowAt); err == nil {
				t.Fatalf("%s: Verify accepted a bad proof", tc.name)
			}
		})
	}

	// A proof signed by a DIFFERENT key never recovers to the claimed From.
	forged := good
	forged.Signature = signProof(t, other, req, "0xdeadbeef", now-1, now+300).Signature
	if err := Verify(req, forged, DefaultTokenName, DefaultTokenVersion, now); err == nil {
		t.Fatal("Verify accepted a signature from the wrong signer")
	}
}

// A proof is BOUND to the exact terms it was signed for: reusing it against a
// different resource's requirements (different token/chain/payee) fails recovery.
func TestVerifyBindsToTerms(t *testing.T) {
	key, _ := crypto.GenerateKey()
	reqA := sampleReq("0x2222222222222222222222222222222222222222")
	p := signProof(t, key, reqA, "0xfeed", 9_999, 10_300)

	reqB := reqA
	reqB.ChainID = 1 // different chain → different EIP-712 domain
	if err := Verify(reqB, p, DefaultTokenName, DefaultTokenVersion, 10_000); err == nil {
		t.Fatal("a proof signed for chain 36963 must not verify on chain 1")
	}
}
