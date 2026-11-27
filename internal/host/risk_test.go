package host

// The fail policy is the single most consequential function in the lifecycle
// defense: it decides what happens to every request when the judge is out. These
// tests are its specification.
//
// Each one is mutation-proven — invert the branch it guards and exactly this test
// goes red.

import (
	"testing"

	cloud "github.com/hanzoai/cloud/sdk"
)

// resetScorer restores the client after a test, so one test's installed scorer can
// never leak into another's — the package-level client is the only shared state
// here and it is reset explicitly rather than hoped about.
func resetScorer(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { cloud.SetRiskScorer(nil) })
}

// A SCOPE KEY MUST NAME THE ROUTE. The service axis of the per-scope rate rule
// and of the per-scope spend cap is derived from the path, and it was derived
// from the RAW path — so "/V1/AI/chat" produced the service "AI" while reaching
// exactly the "/v1/ai/chat" handler. A different scope key is a different rate
// bucket and a different cap: an authenticated caller could multiply its own
// ceiling by holding down the shift key.
func TestCanonicalService_NamesTheRouteNotTheSpelling(t *testing.T) {
	for _, spelling := range []string{
		"/v1/ai/chat", "/V1/AI/chat", "/v1/AI/Chat", "/v1/ai/chat/", "/V1/Ai/",
	} {
		if got := canonicalService(spelling); got != "ai" {
			t.Errorf("canonicalService(%q) = %q, want %q", spelling, got, "ai")
		}
	}
	// The alias table is applied to the normalized segment, not before it.
	if got := canonicalService("/V1/ML/models"); got != "compute" {
		t.Errorf("canonicalService(/V1/ML/models) = %q, want compute", got)
	}
	// And a non-/v1 path still has no service.
	if got := canonicalService("/healthz"); got != "" {
		t.Errorf("canonicalService(/healthz) = %q, want empty", got)
	}
}
