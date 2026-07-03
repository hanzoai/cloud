package integrations

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// testSvc builds a bare svc with a fixed 32-byte signing key — enough to exercise
// sign/verify in isolation (no store/KMS/HTTP).
func testSvc() *svc {
	key := make([]byte, minStateKeyLen)
	for i := range key {
		key[i] = byte(i + 1)
	}
	return &svc{stateKey: key}
}

func TestStateSignVerifyHappy(t *testing.T) {
	s := testSvc()
	tok, err := s.sign("acme", "slack", "nonce-1")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	p, err := s.verify(tok, "slack")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if p.Org != "acme" || p.Provider != "slack" || p.Nonce != "nonce-1" {
		t.Fatalf("payload roundtrip mismatch: %+v", p)
	}
}

func TestStateTamperFails(t *testing.T) {
	s := testSvc()
	tok, _ := s.sign("acme", "slack", "n")
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
	if _, err := s.verify(string(b), "slack"); err == nil {
		t.Fatal("tampered payload must fail verify")
	}

	// A tampered MAC half must also fail.
	b2 := []byte(tok)
	b2[len(b2)-1] ^= 0x01 // this is base64; flip won't always decode, but must never verify
	if _, err := s.verify(string(b2), "slack"); err == nil {
		t.Fatal("tampered MAC must fail verify")
	}
}

func TestStateExpiredFails(t *testing.T) {
	s := testSvc()
	old := stateTTL
	stateTTL = -time.Minute // sign with an already-past exp
	tok, _ := s.sign("acme", "slack", "n")
	stateTTL = old
	if _, err := s.verify(tok, "slack"); err == nil {
		t.Fatal("expired state must fail verify")
	}
}

func TestStateWrongProviderFails(t *testing.T) {
	s := testSvc()
	tok, _ := s.sign("acme", "slack", "n")
	if _, err := s.verify(tok, "github"); err == nil {
		t.Fatal("state signed for slack must fail verify for github")
	}
}

func TestStateWrongKeyFails(t *testing.T) {
	signer := testSvc()
	tok, _ := signer.sign("acme", "slack", "n")

	other := testSvc()
	other.stateKey = make([]byte, minStateKeyLen) // all-zero: a different key
	if _, err := other.verify(tok, "slack"); err == nil {
		t.Fatal("a token signed under a different key must fail verify")
	}
}

func TestStateMalformedFails(t *testing.T) {
	s := testSvc()
	for _, bad := range []string{"", ".", "abc", "abc.", ".abc", "onlyonepart"} {
		if _, err := s.verify(bad, "slack"); err == nil {
			t.Fatalf("malformed token %q must fail verify", bad)
		}
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
