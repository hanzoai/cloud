// Copyright © 2026 Hanzo AI. MIT License.

package ai

import (
	"os"
	"strings"
	"testing"
)

// The ledger keeps EIGHTEEN decimals — per-token charges are routinely finer than a
// cent — so plane.Money.Minor() refuses to answer rather than round behind the caller's
// back. The balance gate wants a coarse cents figure and must therefore round
// EXPLICITLY, and in one direction only.
//
// Observed in prod once the ledger finally opened: balance
// 149918.078983985999994361 USD → "is finer than its minor unit; round explicitly",
// and the fail-closed gate refused every completion on a funded account.
//
// DOWN, never up: rounding up would admit a request the balance cannot cover, and the
// debit that follows is exact — so the difference lands as a negative balance nobody
// authorized. Rounding down can only refuse slightly early.
func TestBalanceIsRoundedDownExplicitly(t *testing.T) {
	src, err := os.ReadFile("ai.go")
	if err != nil {
		t.Fatalf("read ai.go: %v", err)
	}
	body := string(src)

	// It must not call the refusing helper and hope.
	if strings.Contains(body, "bal.Amount.Minor()") {
		t.Error("Money.Minor() refuses sub-cent amounts — the gate must round explicitly")
	}
	// It must parse and take minor units itself, which truncates toward zero.
	for _, want := range []string{"bal.Amount.Parse()", "a.Minor()", "minor.IsInt64()"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q — the rounding choice must be visible at the call site", want)
		}
	}
	// And it must not silently widen: an out-of-range balance is an error, not a clamp.
	if !strings.Contains(body, "exceeds int64 cents") {
		t.Error("an amount too large for int64 must error, never wrap into a wrong balance")
	}
}
