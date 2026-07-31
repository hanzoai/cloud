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
// `ai` is its OWN process (prod pod: /cloud pid 7, /billing pid 111, /ai pid 83), so
// cloud.BalanceReader() — a package-level var wireFinance sets in the CLOUD process —
// is ALWAYS nil there. The ai module then fell back to an HTTP self-call to
// /v1/billing/balance carrying COMMERCE_SERVICE_TOKEN and no user; that is not a
// validated principal at the edge, so it answered 401, and because the balance gate is
// fail-CLOSED every completion became 503 balance_unavailable — chat, copilot and
// documents dead on a pod whose ledger was healthy.
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

	// Both directions must have a plane path for the split-process case.
	for _, want := range []string{
		`cloud.Ask[plane.BalanceIn, plane.Balance](`,
		`plane.FinanceBalance`,
		`cloud.Ask[plane.RecordIn, plane.Recorded](`,
		`plane.FinanceRecord`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q — money must cross the process boundary over the plane", want)
		}
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

// The rolling cap IS a genuine trampoline and must stay one: clients/rollingcap
// installs it from Mount — after this package, in some binaries — and unlike the money
// hooks a missing cap is benign (uncapped), so resolving per call is safe.
func TestRollingCapStaysATrampoline(t *testing.T) {
	src, err := os.ReadFile("ai.go")
	if err != nil {
		t.Fatalf("read ai.go: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, "aiobject.SetRollingCapReader(func(") {
		t.Error("RollingCapReader must resolve cloud.RollingCapReader() per call")
	}
	if !strings.Contains(body, "cloud.RollingCapReader()") {
		t.Error("RollingCapReader trampoline must re-resolve the host hook inside the closure")
	}
}
