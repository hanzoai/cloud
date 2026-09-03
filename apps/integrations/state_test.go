package integrations

import (
	"encoding/base64"
	"errors"
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

// TestStateBadOrgRejected proves verify refuses a state whose org names no store,
// even when the MAC is valid — so a signing bug cannot produce a callback that
// proceeds with no tenant at all.
//
// The list holds what folds to NOTHING: empty, and the whitespace/control class no
// injective fold survives. "bad/org" and ".." are absent on purpose — they name
// their own subtree now rather than smuggling structure into someone else's, which
// TestAnOrgCannotNameAnothersPath in the broker proves directly.
func TestStateBadOrgRejected(t *testing.T) {
	s := testService()
	for _, org := range []string{"a b", "org\x00", " ", ""} {
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

// A rejected state must stay ONE opaque failure to the caller while carrying the
// precise reason for the log. The distinction that matters most in production is
// "signed by a different key" (a restart with no operator key, so every in-flight
// flow breaks) versus "expired" (the user simply took too long) — they look
// identical in the browser and demand opposite responses from an operator.
func TestVerifyCauseSeparatesWrongKeyFromExpiry(t *testing.T) {
	signer := testService()
	tok, err := sign(signer, "acme", "slack", "n")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	// A DIFFERENT process: same code, its own randomly generated key.
	other := testService()
	other.State.stateKey = resolveStateKey("", nil)

	_, err = verify(other, tok, "slack")
	if !errors.Is(err, errBadState) {
		t.Fatalf("a foreign-key state must still fail as errBadState, got %v", err)
	}
	if !strings.Contains(err.Error(), "signature mismatch") {
		t.Fatalf("cause must name the key mismatch, got %q", err)
	}
	if !strings.Contains(err.Error(), stateKeyEnv) {
		t.Fatalf("cause must point at the env var that fixes it, got %q", err)
	}

	// Expiry is a different cause entirely, and must not read as a key problem.
	defer func(d time.Duration) { stateTTL = d }(stateTTL)
	stateTTL = -time.Second
	expired, err := sign(signer, "acme", "slack", "n")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	_, err = verify(signer, expired, "slack")
	if !errors.Is(err, errBadState) {
		t.Fatalf("expired state must fail as errBadState, got %v", err)
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Fatalf("cause must say expired, got %q", err)
	}
	if strings.Contains(err.Error(), "signature mismatch") {
		t.Fatalf("expiry must not be reported as a key mismatch, got %q", err)
	}
}
