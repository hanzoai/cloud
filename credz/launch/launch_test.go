package launch

import (
	"strings"
	"testing"
)

// value strips the NAME= that Env returns, which is what a child reads out of
// its environment and hands the broker.
func value(entry string) string {
	_, v, _ := strings.Cut(entry, "=")
	return v
}

// TestRoundTrip — the launcher's stamp opens under the launcher's secret, and
// says the app the launcher started.
func TestRoundTrip(t *testing.T) {
	s := Secret()
	for _, app := range []string{"ai", "billing", "kms", "o11y"} {
		if got := Open(s, value(Env(s, app))); got != app {
			t.Fatalf("Open(Env(%q)) = %q, want %q", app, got, app)
		}
	}
}

// TestEnvIsAnEnvironmentEntryForOneApp — the shape both spawn sites append to
// zip.Plugin.Env, and the fact that two apps never get the same one.
func TestEnvIsAnEnvironmentEntryForOneApp(t *testing.T) {
	s := Secret()
	ai, billing := Env(s, "ai"), Env(s, "billing")
	if !strings.HasPrefix(ai, TokenEnv+"=ai:") {
		t.Fatalf("Env = %q, want %s=ai:<mac>", ai, TokenEnv)
	}
	if ai == billing {
		t.Fatal("two apps were stamped with the same token")
	}
}

// TestForgery is the bug. Every one of these is what a child could do for free
// when identity came from argv, and every one of them must open to nothing.
func TestForgery(t *testing.T) {
	s := Secret()
	other := Secret()
	ai := value(Env(s, "ai"))

	for _, tc := range []struct {
		name string
		tok  string
	}{
		{"nothing at all", ""},
		{"a bare claim, no proof", "billing"},
		{"a claim with an empty proof", "billing:"},
		{"a name the launcher never signed", value(Env(other, "billing"))},
		{"a hand-written mac", "billing:" + strings.Repeat("00", 32)},
		{"a mac that is not hex", "billing:zzzz"},
		{"a truncated mac", ai[:len(ai)-2]},
		// The reason claim and proof are ONE variable: ai's proof pasted onto
		// billing's name must not open as either.
		{"another app's proof under my own name", "billing" + ai[len("ai"):]},
	} {
		if got := Open(s, tc.tok); got != "" {
			t.Fatalf("%s: Open(%q) = %q, want refusal", tc.name, tc.tok, got)
		}
	}
}

// TestNoSecretGrantsNothing — a broker that was never handed a secret must
// refuse everyone, not accept everyone. This is the direction the failure has to
// go, and it is one `secret == ""` away from going the other way.
func TestNoSecretGrantsNothing(t *testing.T) {
	if got := Open("", value(Env("", "ai"))); got != "" {
		t.Fatalf("an empty secret verified its own token as %q — the broker would grant on no secret at all", got)
	}
	if got := Open("", value(Env(Secret(), "ai"))); got != "" {
		t.Fatalf("Open with no secret = %q, want refusal", got)
	}
}

// TestSecretIsRandom — two launchers on one box must not be able to sign for
// each other's children.
func TestSecretIsRandom(t *testing.T) {
	a, b := Secret(), Secret()
	if a == b {
		t.Fatal("Secret() returned the same value twice")
	}
	if len(a) != 64 { // 32 bytes, hex
		t.Fatalf("secret is %d hex chars, want 64 (32 bytes)", len(a))
	}
}
