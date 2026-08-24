package attest

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func key(t *testing.T, s string) Key {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	var k Key
	copy(k[:], b)
	return k
}

// TestATokenMintedUnderOneKeyDoesNotCheckUnderAnother is the fact the boot verdicts
// exist for, measured rather than assumed: two processes that resolved different
// keys accept none of each other's tokens, so an unset key across the mint/check
// split is a permanent refusal and not a degraded one.
func TestATokenMintedUnderOneKeyDoesNotCheckUnderAnother(t *testing.T) {
	mint := key(t, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	other := key(t, "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210")

	tok, ttl := mint.Mint("alice", "acme")
	if ttl != int64(TTL/time.Second) {
		t.Errorf("lifetime %ds is not the declared %s", ttl, TTL)
	}
	if !mint.Valid(tok, "alice", "acme") {
		t.Fatal("a token does not check under the key that minted it")
	}
	if other.Valid(tok, "alice", "acme") {
		t.Fatal("a token checked under a key that did not mint it")
	}
}

// TestATokenIsBoundToWhoAsked: a token minted for one principal must not authorize
// a change as another, which is what stops one tenant's token being replayed in
// another's session.
func TestATokenIsBoundToWhoAsked(t *testing.T) {
	k := key(t, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	tok, _ := k.Mint("alice", "acme")
	for _, who := range [][2]string{{"bob", "acme"}, {"alice", "other"}, {"", ""}} {
		if k.Valid(tok, who[0], who[1]) {
			t.Errorf("alice/acme's token authorized %q/%q", who[0], who[1])
		}
	}
}

// TestAMalformedTokenIsRefused: every shape that is not a token is refused rather
// than decoded into one.
func TestAMalformedTokenIsRefused(t *testing.T) {
	k := key(t, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	good, _ := k.Mint("alice", "acme")
	for _, bad := range []string{"", "   ", "not-base64!!", good[:len(good)-2], good + "AA", strings.ToUpper(good)} {
		if k.Valid(bad, "alice", "acme") {
			t.Errorf("%q was accepted as a token", bad)
		}
	}
}

// TestSharedSaysWhyWhenThereIsNoKey: the boot verdict has to name the value an
// operator must set, or the process that refuses every change says nothing an
// operator can act on.
func TestSharedSaysWhyWhenThereIsNoKey(t *testing.T) {
	t.Setenv(KeyEnv, "")
	err := Shared()
	if err == nil {
		t.Fatal("an unset key reported itself shared")
	}
	if !strings.Contains(err.Error(), KeyEnv) {
		t.Errorf("the reason does not name the value to set: %v", err)
	}
	t.Setenv(KeyEnv, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if err := Shared(); err != nil {
		t.Fatalf("a provisioned key was not recognised: %v", err)
	}
	t.Setenv(KeyEnv, "too-short")
	if Shared() == nil {
		t.Fatal("a key that is not 32 bytes reported itself shared")
	}
}
