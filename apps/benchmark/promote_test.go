package benchmark

import (
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// promoteAs sends one promotion as who.
func promoteAs(t *testing.T, app *zip.App, who caller, body map[string]any) (int, promoteOut) {
	t.Helper()
	code, raw := do(t, app, who, "POST", "/v1/benchmark/claims/promote", body)
	var out promoteOut
	_ = json.Unmarshal(raw, &out)
	if code != 200 {
		t.Logf("promote as %q: %d %s", who.user, code, raw)
	}
	return code, out
}

// An org's public claim reaches the board only when a SuperAdmin promotes it, and
// then it arrives as filed — score, protocol, source — marked with the org it came
// from. A private claim is never promoted, promoting twice writes nothing, and a
// claim the org restated is published at its new value.
func TestPromotePublishesAnOrgsPublicClaims(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := mount(t)
		measured := publishedClaim{Benchmark: "gpqa_diamond", Provider: "hanzo", Model: "enso", Score: 92.9, Protocol: "hanzo-measured",
			Source: "https://api.hanzo.ai/v1/research/runs?org=acme&id=enso.gpqa_diamond-test-frontier-enso"}
		secret := publishedClaim{Benchmark: "gpqa_diamond", Provider: "hanzo", Model: "enso-secret", Score: 99, Protocol: "hanzo-measured", Source: "unpublished"}
		if code, _ := put(t, app, acme, measured, Public); code != 200 {
			t.Fatalf("acme files: %d", code)
		}
		if code, _ := put(t, app, acme, secret, Private); code != 200 {
			t.Fatalf("acme files privately: %d", code)
		}
		if _, ok := board(t, app, nobody)["enso"]; ok {
			t.Fatal("an org's claim is on the board before anyone published it")
		}

		code, out := promoteAs(t, app, super, map[string]any{"org": "acme"})
		if code != 200 || out.Promoted != 1 || out.Unchanged != 0 || out.From != "acme" || len(out.Data) != 1 {
			t.Fatalf("promote: %d %+v, want 1 promoted from acme", code, out)
		}
		if r := out.Data[0]; r.Org != curator || r.From != "acme" || r.By != super.user || r.Score != 92.9 || r.Source != measured.Source || r.Origin != "stored" {
			t.Errorf("promoted row = %+v, want the filed claim under admin, from acme, by %s", r, super.user)
		}
		row, ok := board(t, app, nobody)["enso"]
		if !ok || row.Published == nil || *row.Published != 92.9 || row.Protocol != "hanzo-measured" || row.From != "acme" {
			t.Errorf("board row = %+v (found %v), want 92.9 hanzo-measured from acme", row, ok)
		}
		if _, ok := board(t, app, nobody)["enso-secret"]; ok {
			t.Error("a private claim was promoted")
		}
		if intent := trailOf(t, s, promoteAction, "intent"); len(intent) != 1 || !intent[0].Auth.IsAdmin || !strings.Contains(string(intent[0].After), "enso") {
			t.Errorf("promotion intent = %+v, want one SuperAdmin record naming the row", intent)
		}

		if code, out := promoteAs(t, app, super, map[string]any{"org": "acme"}); code != 200 || out.Promoted != 0 || out.Unchanged != 1 {
			t.Errorf("promote again: %d %+v, want nothing written, 1 unchanged", code, out)
		}

		time.Sleep(time.Millisecond)
		restated := measured
		restated.Score = 93.4
		if code, _ := put(t, app, acme, restated, Public); code != 200 {
			t.Fatalf("acme restates: %d", code)
		}
		if code, out := promoteAs(t, app, super, map[string]any{"org": "acme", "protocol": "hanzo-measured"}); code != 200 || out.Promoted != 1 {
			t.Errorf("promote the restatement: %d %+v, want 1 promoted", code, out)
		}
		if row := board(t, app, nobody)["enso"]; row.Published == nil || *row.Published != 93.4 || row.Claims != 1 {
			t.Errorf("board row = %+v, want the restated 93.4 as the one claim", row)
		}
		if code, out := promoteAs(t, app, super, map[string]any{"org": "acme", "model": "nobody"}); code != 200 || out.Promoted != 0 || out.Unchanged != 0 {
			t.Errorf("a filter matching nothing: %d %+v, want nothing", code, out)
		}
	})
}

// Publication takes a SuperAdmin acting in org admin. A member of admin, a
// SuperAdmin acting in another org and an org's own member are refused 403 on the
// record, an anonymous caller 401, and none of them writes a row.
func TestPromoteTakesASuperAdminActingInAdmin(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := mount(t)
		measured := claim
		measured.Source = "acme measured"
		if code, _ := put(t, app, acme, measured, Public); code != 200 {
			t.Fatalf("acme files: %d", code)
		}
		before := len(storedClaimsOf(t, s.State.store, ""))
		for _, who := range []caller{adminBob, adminApp, switched, acme} {
			if code, _ := promoteAs(t, app, who, map[string]any{"org": "acme"}); code != 403 {
				t.Errorf("%s acting in %s: %d, want 403", who.user, who.org, code)
			}
		}
		if code, _ := promoteAs(t, app, nobody, map[string]any{"org": "acme"}); code != 401 {
			t.Errorf("anonymous: %d, want 401", code)
		}
		if denied := trailOf(t, s, promoteAction, "deny"); len(denied) != 4 {
			t.Errorf("trail holds %d denials, want 4", len(denied))
		}
		for _, from := range []string{"admin", " "} {
			if code, _ := promoteAs(t, app, super, map[string]any{"org": from}); code != 400 {
				t.Errorf("promote from %q: %d, want 400", from, code)
			}
		}
		if n := len(storedClaimsOf(t, s.State.store, "")); n != before {
			t.Errorf("refused promotions wrote %d rows", n-before)
		}
	})
}

// A lower-is-better benchmark is ordered lowest first, and among one model's
// claims the lower is the one the column shows. A board with no measurement is
// ordered by its claims, then by model, never by map order.
func TestTheBoardIsOrderedByItsDirection(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := mount(t)
		rows := []publishedClaim{
			{Benchmark: "jailbreak.wild", Provider: "hanzo", Model: "kai-1.2", Score: 4.0, Protocol: "hanzo-measured", Source: "kai"},
			{Benchmark: "jailbreak.wild", Provider: "hanzo", Model: "jev-1.13", Score: 21.0, Protocol: "hanzo-measured", Source: "jev"},
			{Benchmark: "jailbreak.wild", Provider: "hanzo", Model: "other", Score: 12.0, Protocol: "hanzo-measured", Source: "a"},
			{Benchmark: "jailbreak.wild", Provider: "hanzo", Model: "other", Score: 10.0, Protocol: "hanzo-measured", Source: "b"},
			{Benchmark: "jailbreak.wild", Provider: "hanzo", Model: "tied-b", Score: 30.0, Protocol: "hanzo-measured", Source: "t"},
			{Benchmark: "jailbreak.wild", Provider: "hanzo", Model: "tied-a", Score: 30.0, Protocol: "hanzo-measured", Source: "t"},
		}
		code, raw := do(t, app, super, "POST", "/v1/benchmark/claims", map[string]any{"data": rows, "visibility": Public})
		if code != 200 {
			t.Fatalf("curate: %d %s", code, raw)
		}
		for i := 0; i < 5; i++ {
			code, raw := do(t, app, nobody, "GET", "/v1/benchmark/leaderboard?benchmark=jailbreak.wild", nil)
			var lb leaderboard
			if err := json.Unmarshal(raw, &lb); code != 200 || err != nil {
				t.Fatalf("leaderboard: %d %v", code, err)
			}
			var order []string
			for _, r := range lb.Rows {
				order = append(order, r.Model)
			}
			if lb.Direction != Lower || strings.Join(order, ",") != "kai-1.2,other,jev-1.13,tied-a,tied-b" {
				t.Fatalf("read %d: direction %q order %v, want lower: kai-1.2, other, jev-1.13, tied-a, tied-b", i, lb.Direction, order)
			}
			if other := lb.Rows[1]; *other.Published != 10 || other.Claims != 2 {
				t.Errorf("other = %+v, want the lower of its two claims, 10", other)
			}
		}

		// Higher is the other way, measured rows first.
		m := 50.0
		high := computeLeaderboard([]attempt{
			{Benchmark: "gpqa_diamond", ID: "1", Model: "measured", Correct: true, Answer: "A"},
			{Benchmark: "gpqa_diamond", ID: "2", Model: "measured", Correct: false, Answer: "B"},
		}, "gpqa_diamond", Higher, map[string][]storedClaim{
			"low":  {{publishedClaim: publishedClaim{Model: "low", Score: 40}}},
			"high": {{publishedClaim: publishedClaim{Model: "high", Score: 90}}},
		})
		var order []string
		for _, r := range high {
			order = append(order, r.Model)
		}
		if strings.Join(order, ",") != "measured,high,low" || *high[0].Measured != m {
			t.Errorf("higher order %v, want measured, high, low", order)
		}
	})
}

// The catalog carries the coding tests and the Decision Suite, every row states
// its direction, and an id is written once.
func TestTheCatalogCarriesTheDecisionSuite(t *testing.T) {
	want := map[string]struct {
		items int
		dir   string
	}{
		"jev.ag_news": {400, Higher}, "jev.emotion": {400, Higher}, "jev.banking77_full": {400, Higher},
		"app.support_triage": {400, Higher}, "app.email_spam": {400, Higher}, "app.phishing": {400, Higher},
		"app.moderation_toxicity": {400, Higher}, "app.rag_relevance": {400, Higher}, "app.model_routing_domain": {399, Higher},
		"typed_decisions": {2000, Higher}, "massive": {5100, Higher}, "cap.cardinality": {400, Higher},
		"decision.stability": {4200, Higher}, "jailbreak.wild": {632, Lower}, "decision.isolation": {0, Lower},
	}
	seen := map[string]bool{}
	for _, b := range catalog {
		if seen[b.ID] {
			t.Errorf("%s is in the catalog twice", b.ID)
		}
		seen[b.ID] = true
		if b.Direction != Higher && b.Direction != Lower {
			t.Errorf("%s states direction %q", b.ID, b.Direction)
		}
		if w, ok := want[b.ID]; ok && (b.Items != w.items || b.Direction != w.dir || b.Native) {
			t.Errorf("%s = %+v, want %d items, %s, not native", b.ID, b, w.items, w.dir)
		}
	}
	for id := range want {
		if !seen[id] {
			t.Errorf("%s is missing", id)
		}
	}
	if seen["decision.latency"] {
		t.Error("decision.latency is a serving measure in milliseconds, not a percentage; it lives in research runs")
	}
	// The tests coding agents are judged on: claims only, each its own test.
	for id, items := range map[string]int{"deepswe": 0, "terminal_bench_4_0": 0, "swe_atlas_qna": 0, "swe_bench_pro_v2": 642} {
		if b, ok := lookup(id); !ok || b.Native || b.Items != items || b.Direction != Higher {
			t.Errorf("%s = %+v (in catalog %v), want %d items, higher, not native", id, b, ok, items)
		}
	}
	for _, v1 := range []string{"terminal_bench_2_1", "swe_bench_pro"} {
		if !known(v1) {
			t.Errorf("%s left the catalog when its successor arrived", v1)
		}
	}
	if len(catalog) != 14+4+len(want) {
		t.Errorf("catalog holds %d rows, want the public 14, the coding 4 and the Decision Suite's %d", len(catalog), len(want))
	}
}

// A claim names the research records behind it — the runs it was measured in and
// the evidence it rests on — and they travel with it: on the row filed, through
// promotion, and onto the board. A promotion whose records changed is not
// unchanged, and a row naming too many records, or an empty one, is rejected.
func TestAClaimCarriesItsRunsAndEvidence(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := mount(t)
		runs := []string{"gpqa_diamond-test-frontier-enso"}
		evidence := []string{"run://RUN_THERMAL_SWEEP_0007", "techdata://sph/tsb/22"}
		c := map[string]any{"benchmark": "gpqa_diamond", "provider": "hanzo", "model": "enso", "score": 92.9, "protocol": "hanzo-measured",
			"source": "https://api.hanzo.ai/v1/research/runs?org=acme&id=" + runs[0], "runs": runs, "evidence": evidence}
		file := func(c map[string]any) putClaimsOut {
			t.Helper()
			code, raw := do(t, app, acme, "POST", "/v1/benchmark/claims", map[string]any{"data": []map[string]any{c}, "visibility": Public})
			var out putClaimsOut
			_ = json.Unmarshal(raw, &out)
			if code != 200 {
				t.Fatalf("file: %d %s", code, raw)
			}
			return out
		}
		if out := file(c); out.Recorded != 1 {
			t.Fatalf("file = %+v, want 1 recorded", out)
		}
		row := read(t, app, acme, "&org=acme")[c["source"].(string)]
		if !slices.Equal(row.Runs, runs) || !slices.Equal(row.Evidence, evidence) {
			t.Fatalf("claim row = %+v, want its runs and evidence", row)
		}
		if code, out := promoteAs(t, app, super, map[string]any{"org": "acme"}); code != 200 || out.Promoted != 1 || !slices.Equal(out.Data[0].Runs, runs) {
			t.Fatalf("promote = %d %+v, want the row with its runs", code, out)
		}
		if b := board(t, app, nobody)["enso"]; !slices.Equal(b.Runs, runs) || !slices.Equal(b.Evidence, evidence) {
			t.Errorf("board row = %+v, want the published claim's runs and evidence", b)
		}

		time.Sleep(time.Millisecond)
		c["evidence"] = []string{"techdata://sph/tsb/22"}
		file(c)
		if code, out := promoteAs(t, app, super, map[string]any{"org": "acme"}); code != 200 || out.Promoted != 1 || out.Unchanged != 0 {
			t.Errorf("promote after the evidence changed = %d %+v, want 1 promoted", code, out)
		}
		if b := board(t, app, nobody)["enso"]; !slices.Equal(b.Evidence, []string{"techdata://sph/tsb/22"}) {
			t.Errorf("board evidence = %v, want the restated one", b.Evidence)
		}

		many := make([]string, maxClaimRefs+1)
		for i := range many {
			many[i] = "run-" + strconv.Itoa(i)
		}
		for name, refs := range map[string][]string{"too many": many, "an empty one": {""}} {
			bad := maps.Clone(c)
			bad["runs"] = refs
			if out := file(bad); out.Recorded != 0 || len(out.Rejected) != 1 {
				t.Errorf("%s: %+v, want the row rejected", name, out)
			}
		}
	})
}
