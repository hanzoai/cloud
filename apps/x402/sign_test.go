package x402

import (
	"crypto/ecdsa"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
	protocol "github.com/hanzoai/commerce/payment/x402"
)

// signPayment signs req with key over the given window and nonce: exactly what a
// compliant client sends. A nonce shorter than 32 bytes is left-padded, because the
// protocol's nonce is exactly 32 bytes and the tests name theirs by a short label.
func signPayment(t *testing.T, key *ecdsa.PrivateKey, req protocol.PaymentRequirements, nonce string, validAfter, validBefore int64) protocol.PaymentPayload {
	t.Helper()
	raw, err := hex.DecodeString(strings.TrimPrefix(nonce, "0x"))
	if err != nil || len(raw) > 32 {
		t.Fatalf("nonce %q is not up to 32 bytes of hex", nonce)
	}
	var n [32]byte
	copy(n[32-len(raw):], raw)
	p, err := protocol.Sign(req, key, n, uint64(validAfter), uint64(validBefore))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return p
}

// The leaf client names the three headers so the process holding the request can
// write them without linking the protocol. They are one wire with the protocol
// package's, and this is where the two cannot drift.
func TestClientNamesTheProtocolsHeaders(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{client.HeaderPaymentRequired, protocol.HeaderPaymentRequired},
		{client.HeaderPaymentSignature, protocol.HeaderPaymentSignature},
		{client.HeaderPaymentResponse, protocol.HeaderPaymentResponse},
	} {
		if c.got != c.want {
			t.Errorf("client says %q, the protocol says %q", c.got, c.want)
		}
	}
}
