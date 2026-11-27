// Copyright © 2026 Hanzo AI. MIT License.

package ai

import (
	"os"
	"strings"
	"testing"
)

// MONEY CROSSES THE PROCESS BOUNDARY OVER THE PLANE — ZAP on the canonical unix
// socket — never HTTP back through our own edge.
//
// `ai` is its OWN process, so cloud.BalanceReader() — a package-level var installFinance
// sets in the CLOUD process — is nil there. An HTTP self-call is not an alternative: the
// edge validates a CUSTOMER credential and answers 401 to a service, and the balance gate
// is fail-closed, so that path refuses every completion on a healthy ledger.
//
// The edge is for customers; the plane is for us. commerce already publishes both money
// ops (apps/commerce/balance_rpc.go) precisely because the ledger has ONE writer and
// must be ASKED, not opened.
func TestMoneyCrossesTheProcessBoundaryOverThePlaneNotHTTP(t *testing.T) {
	src, err := os.ReadFile("ai.go")
	if err != nil {
		t.Fatalf("read ai.go: %v", err)
	}
	bal, err := os.ReadFile("balance.go")
	if err != nil {
		t.Fatalf("read balance.go: %v", err)
	}
	body := string(src) + string(bal)

	// The balance READ is asked here. The debit is asked by the meter, one layer down,
	// so its plane path is asserted where it lives rather than where it is called from.
	for _, want := range []string{
		`commercepeer.FinanceBalance(`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q — money must cross the process boundary over the plane", want)
		}
	}

	meter, err := os.ReadFile("../../internal/driver/ledger/ledger.go")
	if err != nil {
		t.Fatalf("read the ledger driver: %v", err)
	}
	if !strings.Contains(string(meter), "commerce.FinanceRecord(") {
		t.Error("the debit must cross to commerce over the plane, never HTTP back through our own edge")
	}

	// Org scoping travels with the call: the callee scopes the ledger to the caller's
	// org, and answering without one would read another tenant's books.
	if !strings.Contains(body, "cloud.For(ctx,") {
		t.Error("plane money calls must carry the org (cloud.For) — the callee scopes on it")
	}

	// The co-resident path stays direct: when this process DOES own the ledger, an
	// in-process read beats a socket round trip.
	if !strings.Contains(body, "if f, ok := finance.Current().(finance.Funder); ok {") {
		t.Error("the co-resident in-process reader must still be preferred when present")
	}

	// And nothing here may reach for the edge. Checked on CODE lines only — the comment
	// above deliberately names the path it is warning about, and a test that cannot tell
	// an explanation from a call would forbid documenting the bug.
	for i, line := range strings.Split(body, "\n") {
		code := line
		if j := strings.Index(code, "//"); j >= 0 {
			code = code[:j]
		}
		if strings.Contains(code, "/v1/billing/") {
			t.Errorf("ai.go:%d calls the public billing edge — that self-call IS the 503: %s",
				i+1, strings.TrimSpace(line))
		}
	}
}
