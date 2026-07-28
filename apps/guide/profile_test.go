package guide

import (
	"context"
	"net/http"
	"sync"
	"testing"
)

// TestHTTPProfile: the observe surface is fail-closed without a principal (403), and
// a fresh org (no seams wired, no warehouse) reports the stable signal vocabulary all
// false, a formed stage, and zeroed key metrics — an honest, complete profile.
func TestHTTPProfile(t *testing.T) {
	app := newApp(t)
	if r := req(t, app, http.MethodGet, "/v1/guide/profile", "", nil); r.Code != http.StatusForbidden {
		t.Fatalf("profile without principal want 403, got %d", r.Code)
	}
	r := req(t, app, http.MethodGet, "/v1/guide/profile", "acme", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("profile want 200, got %d (%s)", r.Code, r.Body)
	}
	p := decode[profileResponse](t, r.Body)
	if p.Stage != StageFormed {
		t.Fatalf("a fresh org observes nothing → formed, got %q", p.Stage)
	}
	// The fixed vocabulary is always present (stable shape) and all false.
	for _, name := range []string{SignalAnalytics, SignalDeployed, SignalRevenue, SignalCustomers,
		SignalFunnelSignups, SignalFunnelOrders} {
		if p.Signals[name] {
			t.Fatalf("fresh org signal %q must be false, got true", name)
		}
	}
	if p.KeyMetrics.RevenueCents != 0 || p.KeyMetrics.Records != 0 || p.KeyMetrics.Funnel.Available {
		t.Fatalf("fresh org key metrics must be zero, got %+v", p.KeyMetrics)
	}
	// Launch progress reflects the reconciled snapshot (a fresh org has done 0).
	if p.KeyMetrics.LaunchProgress.Total == 0 || p.KeyMetrics.LaunchProgress.Done != 0 {
		t.Fatalf("fresh org launch progress want {done:0,total>0}, got %+v", p.KeyMetrics.LaunchProgress)
	}
}

// TestHTTPProfileCrossTenantIsolation is the load-bearing isolation proof the observe
// layer must satisfy: org A's profile NEVER reflects org B's signals. A fake seam is
// true only for org "acme"; every probe records the org it was asked about, cleared
// per request. We assert (1) acme observes its OWN signals + own numbers and reaches
// scaling, (2) "other" observes NONE of them and stays formed with zeroed numbers,
// and (3) each request's probes only ever read the CALLER's own org — a detector for
// org A never reads org B's state.
func TestHTTPProfileCrossTenantIsolation(t *testing.T) {
	app := newApp(t)

	var mu sync.Mutex
	var seen []string // orgs any probe was asked about during the current request
	record := func(org string) {
		mu.Lock()
		seen = append(seen, org)
		mu.Unlock()
	}
	// TRUE only for org "acme": module cms, connector stripe, a deployment, revenue,
	// and a record count over the threshold. "other" gets nothing.
	mounted.State.signals = Signals{
		ModuleInstalled: func(_ context.Context, org, module string) bool {
			record(org)
			return org == "acme" && module == "cms"
		},
		ConnectorPresent: func(_ context.Context, org, provider string) bool {
			record(org)
			return org == "acme" && provider == "stripe"
		},
		HasDeployment: func(_ context.Context, org string) (bool, error) {
			record(org)
			return org == "acme", nil
		},
		RevenueCents: func(_ context.Context, org string) (int64, error) {
			record(org)
			if org == "acme" {
				return 9900, nil
			}
			return 0, nil
		},
		RecordCount: func(_ context.Context, org string) (int64, error) {
			record(org)
			if org == "acme" {
				return 500, nil
			}
			return 0, nil
		},
	}

	// Run a profile request and return the parsed profile + the orgs the probes read.
	run := func(org string) (profileResponse, []string) {
		mu.Lock()
		seen = nil
		mu.Unlock()
		r := req(t, app, http.MethodGet, "/v1/guide/profile", org, nil)
		if r.Code != http.StatusOK {
			t.Fatalf("profile %s want 200, got %d (%s)", org, r.Code, r.Body)
		}
		mu.Lock()
		got := append([]string(nil), seen...)
		mu.Unlock()
		return decode[profileResponse](t, r.Body), got
	}

	acme, acmeSeen := run("acme")
	other, otherSeen := run("other")

	// (1) acme observes its OWN signals + own numbers → scaling.
	for _, name := range []string{"module:cms", "connected:stripe", SignalDeployed, SignalRevenue, SignalCustomers} {
		if !acme.Signals[name] {
			t.Fatalf("acme must observe its own signal %q, got %+v", name, acme.Signals)
		}
	}
	if acme.Stage != StageScaling {
		t.Fatalf("acme has revenue → scaling, got %q", acme.Stage)
	}
	if acme.KeyMetrics.RevenueCents != 9900 || acme.KeyMetrics.Records != 500 {
		t.Fatalf("acme key metrics must be its OWN numbers, got %+v", acme.KeyMetrics)
	}

	// (2) "other" observes NONE of acme's signals — zero leakage — and stays formed.
	for name, present := range other.Signals {
		if present {
			t.Fatalf("cross-tenant leak: org 'other' observed %q=true (belongs to acme)", name)
		}
	}
	if other.Stage != StageFormed {
		t.Fatalf("'other' observed nothing → formed, got %q", other.Stage)
	}
	if other.KeyMetrics.RevenueCents != 0 || other.KeyMetrics.Records != 0 {
		t.Fatalf("'other' key metrics must be zero (no cross-org number), got %+v", other.KeyMetrics)
	}

	// (3) each request's probes read ONLY the caller's own org.
	assertOnly := func(caller string, got []string) {
		if len(got) == 0 {
			t.Fatalf("%s: expected the probes to run", caller)
		}
		for _, org := range got {
			if org != caller {
				t.Fatalf("isolation breach: a %s request read org %q", caller, org)
			}
		}
	}
	assertOnly("acme", acmeSeen)
	assertOnly("other", otherSeen)
}
