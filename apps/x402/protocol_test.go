package x402

import (
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/luxfi/crypto"
)

// sampleReq is a canonical challenge over a fixed asset domain.
func sampleReq(payTo string) PaymentRequirements {
	return PaymentRequirements{
		Scheme:            SchemeExact,
		Network:           "eip155:36963",
		Amount:            "1000000",
		Asset:             "0x1111111111111111111111111111111111111111",
		PayTo:             payTo,
		MaxTimeoutSeconds: DefaultMaxTimeoutSeconds,
		Extra: &Extra{
			AssetTransferMethod: TransferEIP3009,
			Name:                DefaultAssetName,
			Version:             DefaultAssetVersion,
		},
	}
}

// signPayment builds and EIP-712-signs a PaymentPayload for req with key, over the
// given window + nonce — exactly what a compliant client does.
func signPayment(t *testing.T, key *ecdsa.PrivateKey, req PaymentRequirements, nonce string, validAfter, validBefore int64) PaymentPayload {
	t.Helper()
	p, err := Sign(req, key, nonce, validAfter, validBefore)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return p
}

func TestVerifyRoundTrip(t *testing.T) {
	key, _ := crypto.GenerateKey()
	req := sampleReq("0x2222222222222222222222222222222222222222")
	const now = int64(10_000)
	p := signPayment(t, key, req, "0xabc123", now-1, now+300)

	if err := Verify(req, p); err != nil {
		t.Fatalf("valid payment rejected: %v", err)
	}
	if err := InWindow(p.Payload.Authorization, now); err != nil {
		t.Fatalf("in-window authorization rejected: %v", err)
	}
}

func TestVerifyRejectsTampering(t *testing.T) {
	key, _ := crypto.GenerateKey()
	other, _ := crypto.GenerateKey()
	req := sampleReq("0x2222222222222222222222222222222222222222")
	const now = int64(10_000)
	good := signPayment(t, key, req, "0xdeadbeef", now-1, now+300)

	// Every one of these must be refused, and refused with the SPEC's own reason —
	// the vocabulary a client matches on to decide whether retrying is worth
	// anything. A correct refusal reported under an invented code is a client that
	// cannot tell "re-sign" from "give up".
	cases := []struct {
		name   string
		mut    func(p *PaymentPayload, r *PaymentRequirements)
		reason string
	}{
		{"amount tampered", func(p *PaymentPayload, _ *PaymentRequirements) {
			p.Payload.Authorization.Value = "999"
		}, reasonValueMismatch},
		{"payee tampered", func(p *PaymentPayload, _ *PaymentRequirements) {
			p.Payload.Authorization.To = "0x3333333333333333333333333333333333333333"
		}, reasonRecipient},
		{"required amount differs from signed", func(_ *PaymentPayload, r *PaymentRequirements) {
			r.Amount = "500"
		}, reasonValueMismatch},
		{"required payee differs from signed", func(_ *PaymentPayload, r *PaymentRequirements) {
			r.PayTo = "0x4444444444444444444444444444444444444444"
		}, reasonRecipient},
		{"wrong protocol version", func(p *PaymentPayload, _ *PaymentRequirements) {
			p.X402Version = 1
		}, reasonVersion},
		{"scheme swapped", func(p *PaymentPayload, _ *PaymentRequirements) {
			p.Accepted.Scheme = "upto"
		}, reasonScheme},
		// The echoed `extra` IS the EIP-712 domain. A client free to restate it could
		// sign a message of its own devising that verified perfectly against itself,
		// so the echo is checked against what we offered rather than trusted.
		{"echoed domain restated", func(p *PaymentPayload, _ *PaymentRequirements) {
			p.Accepted.Extra = &Extra{Name: "Evil Coin", Version: "9"}
		}, reasonPayload},
		{"echoed asset restated", func(p *PaymentPayload, _ *PaymentRequirements) {
			p.Accepted.Asset = "0x9999999999999999999999999999999999999999"
		}, reasonPayload},
		{"echoed network restated", func(p *PaymentPayload, _ *PaymentRequirements) {
			p.Accepted.Network = "eip155:1"
		}, reasonNetwork},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, r := good, req
			tc.mut(&p, &r)
			err := Verify(r, p)
			if err == nil {
				t.Fatalf("Verify accepted a bad payment")
			}
			if got := reasonOf(err); got != tc.reason {
				t.Fatalf("reason = %q, want the spec's %q (%v)", got, tc.reason, err)
			}
		})
	}

	// A payment signed by a DIFFERENT key never recovers to the claimed From.
	forged := good
	forged.Payload.Signature = signPayment(t, other, req, "0xdeadbeef", now-1, now+300).Payload.Signature
	if err := Verify(req, forged); err == nil {
		t.Fatal("Verify accepted a signature from the wrong signer")
	} else if got := reasonOf(err); got != reasonSignature {
		t.Fatalf("forged signature reason = %q, want %q", got, reasonSignature)
	}
}

// The time window is its OWN question, with its own spec reasons, because its
// answer changes with the clock while everything Verify asks is true forever.
func TestInWindow(t *testing.T) {
	a := Authorization{ValidAfter: 9_999, ValidBefore: 10_300}
	if err := InWindow(a, 10_000); err != nil {
		t.Fatalf("in-window rejected: %v", err)
	}
	if err := InWindow(a, 10_301); err == nil || reasonOf(err) != reasonValidBefore {
		t.Fatalf("expired = %v, want %s", err, reasonValidBefore)
	}
	if err := InWindow(a, 9_998); err == nil || reasonOf(err) != reasonValidAfter {
		t.Fatalf("not-yet-valid = %v, want %s", err, reasonValidAfter)
	}
}

// A payment is BOUND to the exact terms it was signed for: reusing it against a
// different resource's requirements (different asset/chain/payee) fails recovery.
func TestVerifyBindsToTerms(t *testing.T) {
	key, _ := crypto.GenerateKey()
	reqA := sampleReq("0x2222222222222222222222222222222222222222")
	p := signPayment(t, key, reqA, "0xfeed", 9_999, 10_300)

	reqB := reqA
	reqB.Network = "eip155:1" // different chain → different EIP-712 domain
	if err := Verify(reqB, p); err == nil {
		t.Fatal("a payment signed for eip155:36963 must not verify on eip155:1")
	}
}

// ── the v2 wire ───────────────────────────────────────────────────────────────

// The headers are the x402 v2 names and they carry BASE64 JSON. This is the one
// test that would catch a silent regression to the v1 spelling, which is the bug
// where a compliant client pays and the server never sees it.
func TestV2HeaderNamesAndBase64RoundTrip(t *testing.T) {
	if HeaderPaymentRequired != "PAYMENT-REQUIRED" ||
		HeaderPaymentSignature != "PAYMENT-SIGNATURE" ||
		HeaderPaymentResponse != "PAYMENT-RESPONSE" {
		t.Fatalf("header names drifted from the spec: %q %q %q",
			HeaderPaymentRequired, HeaderPaymentSignature, HeaderPaymentResponse)
	}

	key, _ := crypto.GenerateKey()
	req := sampleReq("0x2222222222222222222222222222222222222222")
	required := &PaymentRequired{
		X402Version: Version,
		Error:       "payment required for /paid/tool",
		Resource:    ResourceInfo{URL: "/paid/tool"},
		Accepts:     []PaymentRequirements{req},
	}

	// PAYMENT-REQUIRED: encodes to base64, decodes back to the same object, and the
	// bytes underneath are the spec's JSON field names.
	enc := EncodeHeader(required)
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("PAYMENT-REQUIRED is not standard base64: %v", err)
	}
	for _, field := range []string{
		`"x402Version":2`, `"accepts"`, `"scheme":"exact"`, `"network":"eip155:36963"`,
		`"amount":"1000000"`, `"asset"`, `"payTo"`, `"maxTimeoutSeconds":300`,
		`"extra"`, `"assetTransferMethod":"eip3009"`,
	} {
		if !strings.Contains(string(raw), field) {
			t.Fatalf("PAYMENT-REQUIRED is missing the spec field %s\ngot: %s", field, raw)
		}
	}
	var back PaymentRequired
	if err := DecodeHeader(enc, &back); err != nil {
		t.Fatalf("decode PAYMENT-REQUIRED: %v", err)
	}
	if !reflect.DeepEqual(&back, required) {
		t.Fatalf("PAYMENT-REQUIRED did not round-trip:\n got %+v\nwant %+v", back, *required)
	}

	// PAYMENT-SIGNATURE: the payload the client sends back.
	pay := signPayment(t, key, req, "0xabc123", 10_000, 10_300)
	payHdr := EncodeHeader(pay)
	parsed, err := ParsePayment(payHdr)
	if err != nil {
		t.Fatalf("parse PAYMENT-SIGNATURE: %v", err)
	}
	if !reflect.DeepEqual(*parsed, pay) {
		t.Fatalf("PAYMENT-SIGNATURE did not round-trip:\n got %+v\nwant %+v", *parsed, pay)
	}
	if err := Verify(req, *parsed); err != nil {
		t.Fatalf("a payment that survived the wire must still verify: %v", err)
	}
	// validAfter/validBefore are STRINGS on the wire (spec §5.2.2).
	praw, _ := base64.StdEncoding.DecodeString(payHdr)
	if !strings.Contains(string(praw), `"validAfter":"10000"`) ||
		!strings.Contains(string(praw), `"validBefore":"10300"`) {
		t.Fatalf("authorization timestamps must be quoted strings: %s", praw)
	}

	// PAYMENT-RESPONSE: the settlement result.
	resp := SettlementResponse{
		Success: true, Payer: "0x857b06519E91e3A54538791bDbb0E22373e36b66",
		Transaction: "x402_abc", Network: req.Network, Amount: "1",
	}
	var rback SettlementResponse
	if err := DecodeHeader(EncodeHeader(resp), &rback); err != nil {
		t.Fatalf("decode PAYMENT-RESPONSE: %v", err)
	}
	if !reflect.DeepEqual(rback, resp) {
		t.Fatalf("PAYMENT-RESPONSE did not round-trip: %+v", rback)
	}
}

// Timestamps decode from either spelling, because implementations disagree on the
// JSON type and a payment lost to a quoting convention is still a payment lost.
func TestEpochDecodesNumberOrString(t *testing.T) {
	for _, body := range []string{
		`{"validAfter":"1740672089","validBefore":"1740672154"}`,
		`{"validAfter":1740672089,"validBefore":1740672154}`,
	} {
		var a Authorization
		if err := json.Unmarshal([]byte(body), &a); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if a.ValidAfter != 1740672089 || a.ValidBefore != 1740672154 {
			t.Fatalf("%s decoded to %d/%d", body, a.ValidAfter, a.ValidBefore)
		}
	}
}

// Header decoding accepts the base64 spellings a client library reaches for by
// accident; every one of them is unambiguous.
func TestHeaderDecodeAcceptsBase64Variants(t *testing.T) {
	want := SettlementResponse{Success: true, Transaction: "x402_a", Network: "eip155:1"}
	b, _ := json.Marshal(want)
	for name, enc := range map[string]*base64.Encoding{
		"std": base64.StdEncoding, "rawstd": base64.RawStdEncoding,
		"url": base64.URLEncoding, "rawurl": base64.RawURLEncoding,
	} {
		var got SettlementResponse
		if err := DecodeHeader(enc.EncodeToString(b), &got); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: got %+v", name, got)
		}
	}
	if err := DecodeHeader("not base64!!", &SettlementResponse{}); err == nil {
		t.Fatal("a non-base64 header must be refused")
	}
}

// ── CAIP-2 ────────────────────────────────────────────────────────────────────

// The network is ONE value and the chain id is READ OUT of it. This is what
// replaced a network string beside a separate chainId — one fact with two
// spellings, and therefore one fact that could disagree with itself.
func TestCAIP2ChainID(t *testing.T) {
	ok := map[string]int64{
		"eip155:1":       1,
		"eip155:8453":    8453,
		"eip155:84532":   84532,
		"eip155:36963":   36963, // the Hanzo L1 default
		"eip155:43114":   43114,
		" eip155:137 ":   137, // surrounding space is not a different chain
		"eip155:8675309": 8675309,
	}
	for network, want := range ok {
		got, err := ChainID(network)
		if err != nil {
			t.Fatalf("ChainID(%q): %v", network, err)
		}
		if got != want {
			t.Fatalf("ChainID(%q) = %d, want %d", network, got, want)
		}
	}

	bad := []string{
		"",             // nothing
		"base-sepolia", // the v1 spelling — must NOT be silently accepted
		"hanzo",        // our own old label
		"eip155",       // no reference
		":8453",        // no namespace
		"eip155:",      // empty reference
		"eip155:base",  // non-numeric reference
		"eip155:0",     // chain 0 is not a chain
		"eip155:-1",    // nor a negative one
		"ei:1",         // namespace too short
		"toolongnamespace:1",
		"eip155:" + strings.Repeat("9", 33), // reference too long
		"solana:5eykt4UsFv8P8NJdTREpY1vzqKqZKvdp", // valid CAIP-2, but not EVM
	}
	for _, network := range bad {
		if got, err := ChainID(network); err == nil {
			t.Fatalf("ChainID(%q) = %d, want an error", network, got)
		} else if reasonOf(err) != reasonNetwork {
			t.Fatalf("ChainID(%q) reason = %q, want %q", network, reasonOf(err), reasonNetwork)
		}
	}
}

// A challenge cannot even be STATED on a network no signature could be made
// against, so the misconfiguration surfaces where an operator can see it rather
// than as a recovery failure on every client forever.
func TestRequirementsRefusesANonCAIP2Network(t *testing.T) {
	if _, err := requirements(Config{Network: "base-sepolia"}, Terms{}, "0x22"); err == nil {
		t.Fatal("requirements accepted a v1 network label")
	}
	req, err := requirements(Config{}, Terms{}, "0x2222222222222222222222222222222222222222")
	if err != nil {
		t.Fatalf("default config must state a valid challenge: %v", err)
	}
	if req.Network != DefaultNetwork || req.Scheme != SchemeExact {
		t.Fatalf("default challenge = %+v", req)
	}
	if _, err := ChainID(req.Network); err != nil {
		t.Fatalf("the default network must be a real EVM chain: %v", err)
	}
}
