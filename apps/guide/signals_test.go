package guide

import (
	"context"
	"testing"
)

// TestClassifyStage is the exhaustive pure test of the growth-stage fold: every
// SignalSet maps to exactly one Stage, the HIGHEST rung whose predicate holds. No
// I/O — classifyStage can never run an effect.
func TestClassifyStage(t *testing.T) {
	cases := []struct {
		name string
		set  SignalSet
		want Stage
	}{
		{"nothing observed", SignalSet{}, StageFormed},
		{"nil map is formed", nil, StageFormed},
		{"deployment is launched", SignalSet{SignalDeployed: true}, StageLaunched},
		{"analytics is launched", SignalSet{SignalAnalytics: true}, StageLaunched},
		{"signups is activated", SignalSet{SignalFunnelSignups: true}, StageActivated},
		{"customers is activated", SignalSet{SignalCustomers: true}, StageActivated},
		{"revenue is scaling", SignalSet{SignalRevenue: true}, StageScaling},
		{"orders is scaling", SignalSet{SignalFunnelOrders: true}, StageScaling},
		{"highest rung wins", SignalSet{SignalDeployed: true, SignalFunnelSignups: true}, StageActivated},
		{"launched+scaling collapses to scaling", SignalSet{SignalDeployed: true, SignalRevenue: true}, StageScaling},
		// Revenue with NO lower signal observed still reads as scaling — the honest
		// floor is the strongest evidence present, not a monotonic-chain requirement.
		{"revenue alone is scaling", SignalSet{SignalRevenue: true}, StageScaling},
		// Context signals (modules/connectors) are NOT stage drivers.
		{"a connected provider does not advance the stage", SignalSet{"connected:stripe": true}, StageFormed},
		{"an installed module does not advance the stage", SignalSet{"module:cms": true}, StageFormed},
		{"false signals never count", SignalSet{SignalDeployed: false, SignalRevenue: false}, StageFormed},
		{"the full ladder is scaling", SignalSet{
			SignalAnalytics: true, SignalDeployed: true, SignalFunnelSignups: true,
			SignalCustomers: true, SignalRevenue: true, SignalFunnelOrders: true,
		}, StageScaling},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyStage(tc.set); got != tc.want {
				t.Fatalf("classifyStage(%v) = %q, want %q", tc.set, got, tc.want)
			}
		})
	}
}

// TestObserveHonestDegrades: with an all-nil seam and an unavailable funnel, EVERY
// growth signal is present=false and every key metric is zero — no spurious true, no
// panic. The observe layer degrades signal-by-signal.
func TestObserveHonestDegrades(t *testing.T) {
	set, m := observe(context.Background(), "acme", Signals{}, Funnel{})
	for name, present := range set {
		if present {
			t.Fatalf("nil seam + empty funnel must yield no present signal; %q was true", name)
		}
	}
	// The fixed vocabulary is always reported (stable shape), all false.
	for _, name := range []string{SignalAnalytics, SignalDeployed, SignalRevenue, SignalCustomers,
		SignalFunnelVisitors, SignalFunnelSignups, SignalFunnelOrders} {
		if _, ok := set[name]; !ok {
			t.Fatalf("fixed signal %q must be present in the set (as false)", name)
		}
	}
	if m.RevenueCents != 0 || m.Records != 0 || m.Funnel.Available {
		t.Fatalf("honest-degraded metrics must be zero, got %+v", m)
	}
	if classifyStage(set) != StageFormed {
		t.Fatal("an org observed with nothing must classify as formed")
	}
}

// TestObserveReflectsSeams: a fake seam + an available funnel is reflected verbatim
// into the SignalSet and the org's OWN key metrics.
func TestObserveReflectsSeams(t *testing.T) {
	sig := Signals{
		ModuleInstalled:  func(_ context.Context, _ /*org*/, module string) bool { return module == "cms" },
		ConnectorPresent: func(_ context.Context, _ /*org*/, provider string) bool { return provider == "stripe" },
		HasDeployment:    func(context.Context, string) (bool, error) { return true, nil },
		RevenueCents:     func(context.Context, string) (int64, error) { return 4200, nil },
		RecordCount:      func(context.Context, string) (int64, error) { return int64(customersThreshold), nil },
	}
	funnel := Funnel{Available: true, WindowDays: 30, Visitors: 10, Signups: 3, Orders: 1, Revenue: 42}
	set, m := observe(context.Background(), "acme", sig, funnel)

	want := map[string]bool{
		SignalAnalytics: true, SignalDeployed: true, SignalRevenue: true, SignalCustomers: true,
		SignalFunnelVisitors: true, SignalFunnelSignups: true, SignalFunnelOrders: true,
		"module:cms": true, "module:erp": false, "connected:stripe": true, "connected:shopify": false,
	}
	for name, exp := range want {
		if set[name] != exp {
			t.Fatalf("signal %q = %v, want %v (set=%v)", name, set[name], exp, set)
		}
	}
	if m.RevenueCents != 4200 || m.Records != int64(customersThreshold) || m.Funnel.Signups != 3 {
		t.Fatalf("key metrics must be the org's own numbers, got %+v", m)
	}
	if classifyStage(set) != StageScaling {
		t.Fatalf("revenue+orders present → scaling, got %q", classifyStage(set))
	}
}

// TestParseThreshold covers the customers:>=N grammar.
func TestParseThreshold(t *testing.T) {
	cases := []struct {
		signal string
		want   int64
	}{
		{"customers", customersThreshold},       // bare → default
		{"customers:>=250", 250},                // explicit
		{"customers:100", 100},                  // bare number
		{"customers:>= 40", 40},                 // spaced
		{"customers:>=abc", customersThreshold}, // malformed → default
		{"customers:>=-5", customersThreshold},  // negative → default
		{"customers:", customersThreshold},      // empty param → default
	}
	for _, tc := range cases {
		if got := parseThreshold(tc.signal, customersThreshold); got != tc.want {
			t.Fatalf("parseThreshold(%q) = %d, want %d", tc.signal, got, tc.want)
		}
	}
}

// TestGrowthSignalsResolveAndDegrade: the growth signals extend the Detector
// vocabulary. lookupDetector resolves the parameterized families to their kind
// detector, a nil seam honest-degrades to not-present, and a wired seam drives a
// curriculum step to auto-done through reconcile — org-scoped, so the seam is only
// ever asked about the caller's own org.
func TestGrowthSignalsResolveAndDegrade(t *testing.T) {
	ctx := context.Background()

	// A fake seam that is TRUE only for org "acme" and records the org it is asked
	// about — the org-scoping proof at the detector level.
	var askedModuleOrg string
	sig := Signals{
		ModuleInstalled: func(_ context.Context, org, module string) bool {
			askedModuleOrg = org
			return org == "acme" && module == "cms"
		},
	}
	store := func(context.Context, string) (*Store, error) { return nil, nil }
	dets := newDetectors(store, sig)

	// Every parameterized family + fixed signal resolves to a detector.
	for _, s := range []string{"module:cms", "connected:stripe", "funnel:signups",
		"customers:>=100", SignalDeployed, SignalRevenue, "acted", "analytics"} {
		if _, ok := lookupDetector(dets, s); !ok {
			t.Fatalf("signal %q must resolve to a detector", s)
		}
	}
	// An unknown signal does not resolve (curriculum may name a not-yet-shipped one).
	if _, ok := lookupDetector(dets, "future:thing"); ok {
		t.Fatal("an unregistered signal kind must not resolve")
	}

	// A step naming module:cms auto-completes for acme (seam present)…
	cur := Curriculum{Steps: []Step{{ID: "sell", Title: "Sell", Signal: "module:cms"}}}
	states := map[string]State{}
	marked := 0
	if err := reconcile(ctx, "acme", cur, states, dets, func(string) error { marked++; return nil }); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if marked != 1 || states["sell"] != StateDone {
		t.Fatalf("module:cms must auto-complete for acme, marked=%d state=%q", marked, states["sell"])
	}
	if askedModuleOrg != "acme" {
		t.Fatalf("the module seam must be asked about the CALLER's org, got %q", askedModuleOrg)
	}

	// …and for a DIFFERENT org the same seam reports absent → the step stays open
	// (per-org isolation at the reconcile boundary).
	states = map[string]State{}
	marked = 0
	if err := reconcile(ctx, "other", cur, states, dets, func(string) error { marked++; return nil }); err != nil {
		t.Fatalf("reconcile other: %v", err)
	}
	if marked != 0 || states["sell"] == StateDone {
		t.Fatalf("module:cms must NOT complete for a different org, marked=%d state=%q", marked, states["sell"])
	}
	if askedModuleOrg != "other" {
		t.Fatalf("the seam must be asked about the OTHER org on its own request, got %q", askedModuleOrg)
	}

	// A nil-seam registry honest-degrades: the same step never marks.
	degraded := newDetectors(store, Signals{})
	states = map[string]State{}
	marked = 0
	if err := reconcile(ctx, "acme", cur, states, degraded, func(string) error { marked++; return nil }); err != nil {
		t.Fatalf("reconcile degraded: %v", err)
	}
	if marked != 0 {
		t.Fatalf("a nil seam must honest-degrade (no auto-complete), marked=%d", marked)
	}
}
