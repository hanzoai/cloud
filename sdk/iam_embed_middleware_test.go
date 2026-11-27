package cloud

import (
	"net/http"
	"testing"
)

// controlPricedPath is the synthetic priced route the gate-liveness control uses. It
// is not a product route: every real surface is Free or Metered, so a control drawn
// from the real declaration cannot distinguish a live gate from a dead one.
const controlPricedPath = "/v1/probe/priced"

// iamAuthPrefixes are the subtrees the embedded IAM surface owns. Restated here rather
// than read from clients/iam because that package imports this one — and the list is
// the FIXTURE under test: these are the paths an auth outage would run through.
var iamAuthPrefixes = []string{"/v1/iam", "/login/oauth", "/.well-known"}

// TestDefaultPriceExemptsIAM pins the price-0 (ungated, unbilled) exemption for every
// IAM-owned prefix, so a change that starts billing an auth path fails here rather
// than in production (the M2M token mint would 402).
//
// Under surface pricing the exemption is a DECLARATION, so the test asserts the
// declaration reaches the gate: iam declares Free and every auth path resolves to 0,
// while a sibling surface declared at 1c resolves to 1. That second half is what makes
// the first half mean something — without it, a nil index would return 0 for every
// path in the binary and this test would pass having measured nothing.
func TestDefaultPriceExemptsIAM(t *testing.T) {
	index(t, &Config{Enable: []string{"iam", "probe"}},
		Plugin{Name: "iam", Price: Free, Prefixes: iamAuthPrefixes},
		Plugin{Name: "probe", Price: 1, Prefixes: []string{"/v1/probe"}},
	)
	if got := PriceOf(controlPricedPath).Cents(); got != 1 {
		t.Fatalf("the declared 1c control resolved to %dc — the index is not live, so the "+
			"IAM zeroes below prove nothing", got)
	}
	for _, path := range []string{
		"/v1/iam/oauth/token",
		"/v1/iam/oauth/token",
		"/v1/iam/login",
		"/v1/iam/.well-known/jwks",
		"/.well-known/openid-configuration",
		"/.well-known/jwks",
		"/login/oauth/authorize",
	} {
		// A WRITE, deliberately: a read costs nothing on every surface (Consumes),
		// so a GET here would answer 0 for a path priced at a pound and the
		// exemption would be unfalsifiable.
		if got := DefaultPrice(http.MethodPost, path); got != 0 {
			t.Errorf("DefaultPrice(POST %q) = %d, want 0 (IAM auth paths must never be billed)", path, got)
		}
	}
}
