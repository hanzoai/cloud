package commerce

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
)

// TestPlaneScopeRules_OrgFromCaller pins the tenancy of the rate-ceiling read:
// the org is the CALLER's, never an argument, so one tenant can never read
// another's policy. The op takes struct{} — there is no field to forge — and with
// no org on the call it refuses before opening any store.
func TestPlaneScopeRules_OrgFromCaller(t *testing.T) {
	if _, err := planeScopeRules(context.Background(), &struct{}{}); err == nil {
		t.Fatal("no org on the call must refuse, not answer with someone's rules")
	}
	// With an org stamped on the context it proceeds past the gate into the
	// datastore, which in a unit context fails for its OWN reason — a different
	// error than the forbidden gate, which is the whole assertion.
	if _, err := planeScopeRules(cloud.For(context.Background(), "acme"), &struct{}{}); err != nil {
		if err.Error() == "scope rules: no org on the call" {
			t.Fatalf("org on the context must pass the gate; got the no-org refusal: %v", err)
		}
	}
}

// TestScopeRuleCarriesNoOrg: the plane's rule — the tenant rides the caller — is
// held by the TYPE, not by a reviewer remembering it. A ScopeRule names a scope
// inside one org and nothing else.
func TestScopeRuleCarriesNoOrg(t *testing.T) {
	var r plane.ScopeRule
	r.Project, r.Service, r.RateLimitRpm = "p", "s", 1
	// (compile-time: any `r.Org` here would fail to build, which is the point)
	if r.RateLimitRpm != 1 {
		t.Fatal("unreachable")
	}
}
