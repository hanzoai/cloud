package guide

import (
	"net/http"
	"strings"
	"testing"
)

// TestTagSatisfied covers the tag join (reconciliation b): a stage tag is a monotone
// readiness floor (research == formed), a has: tag requires its mapped observe signal,
// an unknown capability/stage is fail-closed, an un-kinded tag is a free label.
func TestTagSatisfied(t *testing.T) {
	cases := []struct {
		tag     string
		stage   Stage
		signals SignalSet
		want    bool
	}{
		{"stage:research", StageFormed, nil, true},    // research == formed (rank 0)
		{"stage:formed", StageFormed, nil, true},      // the observe alias
		{"stage:launched", StageFormed, nil, false},   // not yet reached
		{"stage:launched", StageScaling, nil, true},   // monotone: scaling >= launched
		{"stage:scaling", StageActivated, nil, false}, // activated < scaling
		{"stage:bogus", StageScaling, nil, false},     // unknown stage → fail-closed
		{"has:analytics", StageFormed, SignalSet{SignalAnalytics: true}, true},
		{"has:analytics", StageFormed, SignalSet{}, false}, // signal absent
		{"has:facebook", StageFormed, SignalSet{kindConnected + ":facebook": true}, true},
		{"has:website", StageFormed, SignalSet{SignalDeployed: true}, true}, // website == a live deployment
		{"has:leads", StageFormed, SignalSet{SignalFunnelSignups: true}, true},
		{"has:unknown", StageScaling, nil, false}, // unmapped capability → fail-closed
		{"freelabel", StageFormed, nil, true},     // un-kinded → free label, not a gate
	}
	for _, tc := range cases {
		if got := tagSatisfied(tc.tag, tc.stage, tc.signals); got != tc.want {
			t.Errorf("tagSatisfied(%q, %s, %v) = %v, want %v", tc.tag, tc.stage, tc.signals, got, tc.want)
		}
	}
}

// TestStrategyMatches: a strategy surfaces only when EVERY tag is satisfied; an
// untagged strategy is universally applicable (vacuously true).
func TestStrategyMatches(t *testing.T) {
	analytics := SignalSet{SignalAnalytics: true}
	if strategyMatches(Strategy{Tags: []string{"has:analytics", "stage:scaling"}}, StageFormed, analytics) {
		t.Fatal("a strategy needing scaling must not surface for a formed org")
	}
	if !strategyMatches(Strategy{Tags: []string{"has:analytics", "stage:scaling"}}, StageScaling, analytics) {
		t.Fatal("both tags satisfied → the strategy must surface")
	}
	if !strategyMatches(Strategy{Tags: nil}, StageFormed, nil) {
		t.Fatal("an untagged strategy is universally applicable")
	}
}

type corpusResp struct {
	Stage      string `json:"stage"`
	Count      int    `json:"count"`
	Strategies []struct {
		ID       string   `json:"id"`
		Category string   `json:"category"`
		Workload string   `json:"workload"`
		Tags     []string `json:"tags"`
	} `json:"strategies"`
}

func (r corpusResp) has(id string) bool {
	for _, s := range r.Strategies {
		if s.ID == id {
			return true
		}
	}
	return false
}

// TestHTTPStrategiesCorpus proves the org-scoped corpus read: fail-closed without a
// principal; a fresh (formed, no-signal) org surfaces ONLY the stage:research tactics;
// an explicit ?stage= previews a later stage; category/workload narrow it.
func TestHTTPStrategiesCorpus(t *testing.T) {
	app := newApp(t)

	if r := req(t, app, http.MethodGet, "/v1/guide/strategies", "", nil); r.Code != http.StatusForbidden {
		t.Fatalf("corpus without principal want 403, got %d", r.Code)
	}

	// A fresh org is at the formed stage with no capability signals: it surfaces ONLY the
	// tactics whose every tag is formed-satisfiable (no has:* precondition, no stage above
	// formed). The full genome (888 modern + 114 heritage) makes this MORE than the old
	// heritage-only handful — modern formed-stage tactics surface too — so assert the
	// invariant (purity) + a lower bound that proves the modern corpus is folded in, never a
	// brittle exact count.
	fresh := decode[corpusResp](t, req(t, app, http.MethodGet, "/v1/guide/strategies", "acme", nil).Body)
	if fresh.Stage != string(StageFormed) {
		t.Fatalf("fresh org stage want formed, got %q", fresh.Stage)
	}
	if fresh.Count < 15 {
		t.Fatalf("fresh org must surface the formed-satisfiable corpus (modern + heritage), got only %d", fresh.Count)
	}
	// PURITY: nothing surfaced may require a signal (has:*) or a stage above formed.
	for _, s := range fresh.Strategies {
		for _, tag := range s.Tags {
			if strings.HasPrefix(tag, "has:") {
				t.Fatalf("fresh (no-signal) org surfaced %q with an unmet capability gate %q", s.ID, tag)
			}
			switch tag {
			case "stage:launched", "stage:activated", "stage:scaling":
				t.Fatalf("fresh (formed) org surfaced %q needing a higher stage %q", s.ID, tag)
			}
		}
	}
	if !fresh.has("buyer-personas") {
		t.Fatal("a stage:research tactic (buyer-personas) must surface for a formed org")
	}
	if fresh.has("forum-participation") {
		t.Fatal("a stage:launched tactic must NOT surface for a formed org")
	}
	if fresh.has("clv-cac") {
		t.Fatal("a has:analytics tactic must NOT surface without the analytics signal")
	}

	// ?stage=launched previews the launched stage: the stage:launched tactic now
	// surfaces, but a has:analytics tactic still does not (no real signal).
	launched := decode[corpusResp](t, req(t, app, http.MethodGet, "/v1/guide/strategies?stage=launched", "acme", nil).Body)
	if !launched.has("forum-participation") {
		t.Fatal("?stage=launched must surface the stage:launched tactic")
	}
	if launched.has("clv-cac") {
		t.Fatal("?stage= previews stage only — has:analytics still needs the real signal")
	}

	// ?category=&stage= narrow to one category.
	cat := decode[corpusResp](t, req(t, app, http.MethodGet,
		"/v1/guide/strategies?stage=scaling&category=viral-coefficient", "acme", nil).Body)
	if cat.Count == 0 {
		t.Fatal("scaling org should have viral-coefficient tactics")
	}
	for _, s := range cat.Strategies {
		if s.Category != "viral-coefficient" {
			t.Fatalf("category filter leaked %q", s.Category)
		}
	}

	// ?workload= narrows by effort.
	wl := decode[corpusResp](t, req(t, app, http.MethodGet,
		"/v1/guide/strategies?stage=scaling&workload=high", "acme", nil).Body)
	for _, s := range wl.Strategies {
		if s.Workload != "high" {
			t.Fatalf("workload filter leaked %q", s.Workload)
		}
	}
}

// TestHTTPStrategiesRespectsDisable: a SuperAdmin disabling a strategy removes it from
// every org's corpus on the next read (the corpus reads the resolved blueprint's ENABLED
// strategies).
func TestHTTPStrategiesRespectsDisable(t *testing.T) {
	app := newApp(t)
	if !decode[corpusResp](t, req(t, app, http.MethodGet, "/v1/guide/strategies", "acme", nil).Body).has("buyer-personas") {
		t.Fatal("baseline corpus must contain buyer-personas")
	}
	// SuperAdmin disables the strategy.
	r := reqH(t, app, http.MethodPatch, "/v1/guide/blueprint/strategies/buyer-personas", superHdr, map[string]any{"enabled": false})
	if r.Code != http.StatusOK {
		t.Fatalf("disable strategy want 200, got %d (%s)", r.Code, r.Body)
	}
	if decode[corpusResp](t, req(t, app, http.MethodGet, "/v1/guide/strategies", "acme", nil).Body).has("buyer-personas") {
		t.Fatal("a disabled strategy must drop from the corpus")
	}
}
