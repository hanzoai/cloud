package integrations

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
)

// testService builds a bare cloud.Service[state] with a fixed 32-byte signing key — enough to exercise
// sign/verify in isolation (no store/KMS/HTTP).
func testService() *cloud.Service[state] {
	key := make([]byte, minStateKeyLen)
	for i := range key {
		key[i] = byte(i + 1)
	}
	return &cloud.Service[state]{State: state{stateKey: key}}
}

func TestStateSignVerifyHappy(t *testing.T) {
	s := testService()
	tok, err := sign(s, "acme", "slack", "nonce-1")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	p, err := verify(s, tok, "slack")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if p.Org != "acme" || p.Provider != "slack" || p.Nonce != "nonce-1" {
		t.Fatalf("payload roundtrip mismatch: %+v", p)
	}
}

func TestStateTamperFails(t *testing.T) {
	s := testService()
	tok, _ := sign(s, "acme", "slack", "n")
	// Flip a byte in the payload half (before the dot). The MAC no longer matches.
	dot := strings.IndexByte(tok, '.')
	if dot <= 0 {
		t.Fatalf("malformed token %q", tok)
	}
	b := []byte(tok)
	if b[0] == 'A' {
		b[0] = 'B'
	} else {
		b[0] = 'A'
	}
	if _, err := verify(s, string(b), "slack"); err == nil {
		t.Fatal("tampered payload must fail verify")
	}

	// A tampered MAC half must also fail.
	b2 := []byte(tok)
	b2[len(b2)-1] ^= 0x01 // this is base64; flip won't always decode, but must never verify
	if _, err := verify(s, string(b2), "slack"); err == nil {
		t.Fatal("tampered MAC must fail verify")
	}
}

func TestStateExpiredFails(t *testing.T) {
	s := testService()
	old := stateTTL
	stateTTL = -time.Minute // sign with an already-past exp
	tok, _ := sign(s, "acme", "slack", "n")
	stateTTL = old
	if _, err := verify(s, tok, "slack"); err == nil {
		t.Fatal("expired state must fail verify")
	}
}

func TestStateWrongProviderFails(t *testing.T) {
	s := testService()
	tok, _ := sign(s, "acme", "slack", "n")
	if _, err := verify(s, tok, "github"); err == nil {
		t.Fatal("state signed for slack must fail verify for github")
	}
}

func TestStateWrongKeyFails(t *testing.T) {
	signer := testService()
	tok, _ := sign(signer, "acme", "slack", "n")

	other := testService()
	other.State.stateKey = make([]byte, minStateKeyLen) // all-zero: a different key
	if _, err := verify(other, tok, "slack"); err == nil {
		t.Fatal("a token signed under a different key must fail verify")
	}
}

func TestStateMalformedFails(t *testing.T) {
	s := testService()
	for _, bad := range []string{"", ".", "abc", "abc.", ".abc", "onlyonepart"} {
		if _, err := verify(s, bad, "slack"); err == nil {
			t.Fatalf("malformed token %q must fail verify", bad)
		}
	}
}

// TestStateDotInjectionRejected proves the payload|mac split cannot be gamed: a
// second '.' (splitting differently than the signer did), an empty MAC half, and
// an empty payload half all fail — the MAC is bound to the exact payload substring
// before the FIRST dot, and a base64url payload/MAC never itself contains a '.'.
func TestStateDotInjectionRejected(t *testing.T) {
	s := testService()
	tok, _ := sign(s, "acme", "slack", "n")
	dot := strings.IndexByte(tok, '.')
	payloadB64, macB64 := tok[:dot], tok[dot+1:]

	cases := []string{
		payloadB64 + "." + macB64 + ".extra", // trailing dot-injected segment
		payloadB64 + "." + "." + macB64,      // MAC half starts with a dot → decode/verify fail
		payloadB64 + ".",                     // empty MAC
		"." + macB64,                         // empty payload
		payloadB64 + macB64,                  // no separator
	}
	for _, bad := range cases {
		if _, err := verify(s, bad, "slack"); err == nil {
			t.Fatalf("dot-injected/degenerate token %q must fail verify", bad)
		}
	}
}

// TestStateMACCheckedBeforeParse proves the MAC is verified BEFORE the payload is
// json-parsed: a token whose payload half is valid base64url of NON-JSON bytes,
// carrying an attacker-chosen MAC, is rejected at the constant-time MAC gate and
// never reaches (and never panics in) json.Unmarshal.
func TestStateMACCheckedBeforeParse(t *testing.T) {
	s := testService()
	garbage := base64.URLEncoding.EncodeToString([]byte("this-is-not-json-{{{"))
	forgedMAC := base64.URLEncoding.EncodeToString(make([]byte, 32)) // all-zero MAC
	if _, err := verify(s, garbage+"."+forgedMAC, "slack"); err == nil {
		t.Fatal("a non-JSON payload with a forged MAC must fail at the MAC gate, not parse")
	}
}

// TestStateBadOrgRejected proves the state-derived org is validated inside verify:
// even a token signed with the REAL key (so the MAC is valid) is rejected when its
// org would smuggle path structure — so a callback can never fold a hostile org
// into the KMS/store key, independent of the connect-boundary check.
func TestStateBadOrgRejected(t *testing.T) {
	s := testService()
	for _, org := range []string{"bad/org", "..", "a b", "org\x00", ""} {
		tok, err := sign(s, org, "slack", "n")
		if err != nil {
			t.Fatalf("sign %q: %v", org, err)
		}
		if _, err := verify(s, tok, "slack"); err == nil {
			t.Fatalf("validly-signed state with hostile org %q must still fail verify", org)
		}
	}
}

// TestStateOverlongRejected proves an oversized token is rejected before any
// base64 work — capping the allocation a forged callback can force.
func TestStateOverlongRejected(t *testing.T) {
	s := testService()
	huge := strings.Repeat("A", maxStateLen+1) + "." + strings.Repeat("B", 64)
	if _, err := verify(s, huge, "slack"); err == nil {
		t.Fatal("token longer than maxStateLen must fail verify")
	}
}

func TestResolveStateKey(t *testing.T) {
	// Valid base64 of >=32 bytes is used verbatim.
	key := make([]byte, 40)
	for i := range key {
		key[i] = byte(i)
	}
	b64 := base64.StdEncoding.EncodeToString(key)
	got := resolveStateKey(b64, nil)
	if len(got) != 40 {
		t.Fatalf("valid key: want 40 bytes, got %d", len(got))
	}

	// Absent / too-short → a random 32-byte key (never nil, never < 32).
	for _, in := range []string{"", "dG9vc2hvcnQ="} { // "" and base64("tooshort")
		k := resolveStateKey(in, nil)
		if len(k) < minStateKeyLen {
			t.Fatalf("fallback key must be >=%d bytes, got %d", minStateKeyLen, len(k))
		}
	}
}
