// Copyright 2026 Hanzo AI, Inc. All rights reserved.

package cloud

import (
	"strings"
	"testing"
)

// TestUsernamePrefersPreferredUsername pins the claim precedence the money path
// depends on. OIDC `name` is a DISPLAY name — IAM fills it from User.DisplayName,
// and a real token carried "Zach Kelling". Reading it as the username addressed
// wallet `hanzo/Zach Kelling`, which no funding path can name, while the balance
// sat in `hanzo/z`; every signed-in completion 402'd against a funded account.
func TestUsernamePrefersPreferredUsername(t *testing.T) {
	// Both present: the username wins, never the human label.
	c := &idClaims{Name: "Zach Kelling", PreferredUsername: "z"}
	if got := c.username(); got != "z" {
		t.Fatalf("username() = %q; want %q (preferred_username must win over the display name)", got, "z")
	}
	// A display name must never be returned when the username is available, and a
	// space is the tell that a display name leaked into an account key.
	if strings.ContainsRune(c.username(), ' ') {
		t.Fatalf("username() = %q; an account key can never contain a space", c.username())
	}
	// Legacy token minted before IAM emitted preferred_username: `name` is all
	// there is, so it stays the answer rather than becoming empty.
	legacy := &idClaims{Name: "z"}
	if got := legacy.username(); got != "z" {
		t.Fatalf("legacy username() = %q; want %q (fallback must be retained)", got, "z")
	}
	// Neither present: empty, never a guess.
	if got := (&idClaims{}).username(); got != "" {
		t.Fatalf("empty username() = %q; want \"\"", got)
	}
}
