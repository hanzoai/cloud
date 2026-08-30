// Copyright © 2026 Hanzo AI. MIT License.

package ai

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/flags"
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
	body := string(src)

	// The balance READ is asked here. The debit is asked by the meter, one layer down,
	// so its plane path is asserted where it lives rather than where it is called from.
	for _, want := range []string{
		`cloud.Ask[plane.BalanceIn, plane.Balance](`,
		`plane.FinanceBalance`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q — money must cross the process boundary over the plane", want)
		}
	}

	meter, err := os.ReadFile("../metering/metering.go")
	if err != nil {
		t.Fatalf("read metering.go: %v", err)
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
	if !strings.Contains(body, "if f := cloud.BalanceReader(); f != nil {") {
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

// The ceiling's switches must REGISTER, and register here, in the binary that reads
// them. They used to be registered by apps/rollingcap — a process this one does not
// link and nothing ever started — so every lookup missed and the ceiling was
// uncapped everywhere. Asking the live registry (not the seed map) is the whole
// point: it is the only reading that can tell the two situations apart.
func TestCeilingSwitchesAreLiveInThisBinary(t *testing.T) {
	if got := flags.Int(capWindow); got != 3 {
		t.Errorf("%s = %d, want 3 — the window switch is not registered in this binary", capWindow, got)
	}
	for tier, cents := range capSeed {
		if got := flags.Int(capOf(tier)); got != cents {
			t.Errorf("%s = %d, want %d (the seeded default)", capOf(tier), got, cents)
		}
	}
	if got := flags.Int(capFallback); got != 0 {
		t.Errorf("%s = %d, want 0 — the fallback is opt-in", capFallback, got)
	}
}

// A tier this deployment cannot look up takes the SAME path an unseeded plan takes:
// no ceiling of its own, so the fallback decides. With the fallback at its opt-in
// default that is uncapped, and — the part that matters — overCap must answer
// without asking commerce anything, since there is no ledger in a bare binary and a
// plane read would be an error where the honest answer is "no ceiling".
func TestNoCeilingAdmitsWithoutReadingTheLedger(t *testing.T) {
	if cloud.TierReader() != nil {
		t.Fatal("a bare test binary has no tier reader; this case is about that")
	}
	cents, err := capFor(context.Background(), "s", "o")
	if err != nil || cents != 0 {
		t.Fatalf("capFor with no tier reader = %d,%v, want 0,nil (fallback, uncapped)", cents, err)
	}
	if over, err := overCap(context.Background(), "s", "o"); over || err != nil {
		t.Fatalf("overCap = %v,%v, want false,nil — an uncapped caller must never reach the plane", over, err)
	}
}

// Both taxonomies commerce may answer with are seeded, so whichever name comes back
// resolves to a ceiling instead of silently reading 0.
func TestSeedCoversBothTaxonomies(t *testing.T) {
	for _, tier := range []string{"free", "developer", "starter", "pro", "plus", "max", "enterprise"} {
		if _, ok := capSeed[tier]; !ok {
			t.Errorf("capSeed has no %q — an unseeded tier reads 0 and falls to the fallback", tier)
		}
	}
}
