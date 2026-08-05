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
//
// This used to be asserted by GREPPING ai.go for `a.Minor()` and a comment claiming it
// "truncates toward zero". It does not — money.Amount.Minor() is Rescale, which rounds
// HALF-AWAY-FROM-ZERO — so the test passed while the property it named was false, and
// 4.995 was admitted against a 5.00 charge. A test that reads the source can only
// confirm the code still says what it said; it cannot notice that the sentence is
// wrong. The arithmetic is asserted where the rounding now lives, plane/money_test.go.
// What is left here is the one thing only this package can say: that THIS gate still
// asks for the floored figure, and has not drifted back to the helper that refuses.
func TestBalanceGateDoesNotCallRefusingMinor(t *testing.T) {
	src, err := os.ReadFile("ai.go")
	if err != nil {
		t.Fatalf("read ai.go: %v", err)
	}
	body := string(src)

	if strings.Contains(body, "bal.Amount.Minor()") {
		t.Error("Money.Minor() refuses sub-cent amounts — the gate must round explicitly")
	}
	if !strings.Contains(body, "bal.Amount.FloorMinor()") {
		t.Error("the balance gate must floor: rounding up admits spend the balance cannot cover")
	}
}
