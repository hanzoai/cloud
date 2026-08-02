package x402

import (
	"net/http"
	"testing"
)

// TestEnforceRefusesWithNoPriceTable: a route group that applied Enforce declared
// itself PRICED. If no price table has been published, x402 cannot tell what that
// group costs — and "I don't know" must never render as "free".
//
// This is the failure mode the fleet has already been bitten by once, in
// resource_billing_peer.go: "Splitting apps into their own binaries turned every
// priced create free without changing a line of billing code." The published
// registry is a process-global installed by marketplace.Mount, and in the shipped
// topology every app is its OWN process (manifest/apps.go + Dockerfile: one binary
// per app, cmd/cloud loads each as a child). So in ANY process that mounts x402
// without marketplace — which is every process that mounts x402 — the table is nil,
// and a passthrough here is a priced route served for nothing, forever, silently.
//
// The tool path asks a different question — it offers EVERY dispatch to the seam,
// free ones included, so it must be able to answer "this costs nothing" — and it
// reaches this same safe answer the other way round: it asks the process that OWNS
// the table (peer.go), and refuses when that process cannot answer. Neither path
// renders a missing table as free.
func TestEnforceRefusesWithNoPriceTable(t *testing.T) {
	h := newHarness(t)
	Publish(nil) // no marketplace in this process — the shipped topology

	code, body, _ := h.req(http.MethodGet, "/paid/tool", h.payerOrg, "", "")
	if code == http.StatusOK {
		t.Fatalf("a priced route was served FREE with no price table: %d (%s)", code, body)
	}
	if code != http.StatusServiceUnavailable {
		t.Fatalf("no price table = %d (%s), want 503 — unenforceable is not free", code, body)
	}
}

// TestEnforcePassesUnpricedRouteWithATable: with a table published, a route the
// table does not price is genuinely free and passes through. The refusal above is
// about an ABSENT table, not about an absent entry — a priced group may hold free
// routes, and the table is what decides which.
func TestEnforcePassesUnpricedRouteWithATable(t *testing.T) {
	h := newHarness(t)
	pubRegistry(map[string]Terms{}) // a real table that prices nothing

	code, body, _ := h.req(http.MethodGet, "/paid/free", h.payerOrg, "", "")
	if code != http.StatusOK {
		t.Fatalf("unpriced route with a published table = %d (%s), want 200", code, body)
	}
}
