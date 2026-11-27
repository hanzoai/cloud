package benchmark

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// runURL is the research run address a claim cites.
func runURL(run string) string {
	return "https://api.hanzo.ai/v1/research/runs?org=hanzo&id=" + run
}

// items is n items, the first ok of them correct.
func items(n, ok int) []Item {
	out := make([]Item, n)
	for i := range out {
		out[i] = Item{Item: fmt.Sprintf("q%03d", i), Correct: i < ok, Answer: "A"}
	}
	return out
}

// importAs sends one import as who.
func importAs(t *testing.T, app *zip.App, who caller, body map[string]any) (int, importOut, string) {
	t.Helper()
	code, raw := do(t, app, who, "POST", "/v1/benchmark/attempts", body)
	var out importOut
	_ = json.Unmarshal(raw, &out)
	return code, out, string(raw)
}

// publish curates one claim for model on gpqa_diamond citing run.
func publish(t *testing.T, app *zip.App, model, run string, score float64) {
	t.Helper()
	c := publishedClaim{Benchmark: "gpqa_diamond", Provider: "hanzo", Model: model, Score: score, Protocol: "hanzo-measured", Source: runURL(run)}
	if code, out := put(t, app, super, c, Public); code != 200 || out.Recorded != 1 {
		t.Fatalf("publish %s: %d %+v", model, code, out)
	}
}

// A published run's items reach the measured plane, and then the board's
// measured column, history and compare all read them: Measured equals the claim,
// history has the run, and two models' runs over the same items pair.
func TestAPublishedRunsItemsFeedHistoryAndCompare(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := mount(t)
		publish(t, app, "m1", "run-m1", 66.7)
		publish(t, app, "m2", "run-m2", 33.3)

		code, out, raw := importAs(t, app, super, map[string]any{"benchmark": "gpqa_diamond", "model": "m1", "run": "run-m1", "at": "2026-10-14T12:00:00Z", "data": items(3, 2)})
		if code != 200 || out.Imported != 3 || out.Claim != 66.7 || out.Source != runURL("run-m1") {
			t.Fatalf("import m1: %d %s", code, raw)
		}
		if code, _, raw := importAs(t, app, super, map[string]any{"benchmark": "gpqa_diamond", "model": "m2", "run": "run-m2", "data": items(3, 1)}); code != 200 {
			t.Fatalf("import m2: %d %s", code, raw)
		}
		if intent := trailOf(t, s, importAction, "intent"); len(intent) != 2 || !intent[0].Auth.IsAdmin {
			t.Errorf("import intents = %+v, want two SuperAdmin records", intent)
		}

		row := board(t, app, nobody)["m1"]
		if row.Measured == nil || row.N != 3 || row.Run != "run-m1" || row.Published == nil || row.Gap == nil || *row.Gap > tolerance || *row.Gap < -tolerance {
			t.Errorf("board m1 = %+v, want measured over 3 items from run-m1 beside its claim", row)
		}

		code, raw2 := do(t, app, nobody, "GET", "/v1/benchmark/history?benchmark=gpqa_diamond&model=m1", nil)
		var h historyOut
		if err := json.Unmarshal(raw2, &h); code != 200 || err != nil || h.Total != 1 || len(h.Data[0].Points) != 1 ||
			h.Data[0].Points[0].Run != "run-m1" || !h.Data[0].Points[0].At.Equal(time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)) {
			t.Errorf("history: %d %s, want run-m1 at its measured time", code, raw2)
		}

		code, raw2 = do(t, app, nobody, "GET", "/v1/benchmark/compare?benchmark=gpqa_diamond&a=m1&b=m2", nil)
		var p pairing
		if err := json.Unmarshal(raw2, &p); code != 200 || err != nil || p.NCommon != 3 || p.RescueAOverB != 1 || p.RescueBOverA != 0 {
			t.Errorf("compare: %d %s, want 3 common items, m1 rescuing 1", code, raw2)
		}

		// A run lands once.
		if code, _, raw := importAs(t, app, super, map[string]any{"benchmark": "gpqa_diamond", "model": "m1", "run": "run-m1", "data": items(3, 2)}); code != 409 {
			t.Errorf("the same run again: %d %s, want 409", code, raw)
		}
		if n := len(attemptsIn(t, s.State.store, "gpqa_diamond")); n != 6 {
			t.Errorf("store holds %d attempts, want 6", n)
		}
	})
}

// The door imports only what the platform already publishes, from a SuperAdmin
// acting in org admin, and only items that add up to the published number. Every
// refusal writes nothing.
func TestAnImportItemizesAPublishedNumberOrNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := mount(t)
		publish(t, app, "m1", "run-m1", 66.7)
		body := func(edit func(map[string]any)) map[string]any {
			b := map[string]any{"benchmark": "gpqa_diamond", "model": "m1", "run": "run-m1", "data": items(3, 2)}
			if edit != nil {
				edit(b)
			}
			return b
		}

		for _, who := range []caller{adminBob, adminApp, switched, acme} {
			if code, _, _ := importAs(t, app, who, body(nil)); code != 403 {
				t.Errorf("%s acting in %s: %d, want 403", who.user, who.org, code)
			}
		}
		if code, _, _ := importAs(t, app, nobody, body(nil)); code != 401 {
			t.Errorf("anonymous: %d, want 401", code)
		}
		if denied := trailOf(t, s, importAction, "deny"); len(denied) != 4 {
			t.Errorf("trail holds %d denials, want 4", len(denied))
		}

		for _, tc := range []struct {
			why  string
			edit func(map[string]any)
			want int
		}{
			{"no claim cites the run", func(b map[string]any) { b["run"] = "run-other" }, 422},
			{"no claim for the model", func(b map[string]any) { b["model"] = "m9" }, 422},
			{"items that do not add up", func(b map[string]any) { b["data"] = items(3, 3) }, 422},
			{"an item twice", func(b map[string]any) { b["data"] = append(items(3, 2), items(1, 1)...) }, 422},
			{"an item with no answer", func(b map[string]any) {
				d := items(3, 2)
				d[2].Answer = ""
				b["data"] = d
			}, 422},
			{"an unknown benchmark", func(b map[string]any) { b["benchmark"] = "nope" }, 422},
			{"a lower-is-better benchmark", func(b map[string]any) { b["benchmark"] = "jailbreak.wild" }, 422},
			{"no items", func(b map[string]any) { b["data"] = []Item{} }, 400},
			{"a measured time in the future", func(b map[string]any) { b["at"] = time.Now().Add(time.Hour).UTC().Format(time.RFC3339) }, 400},
			{"too many items", func(b map[string]any) { b["data"] = items(maxItems+1, 0) }, 413},
		} {
			if code, _, raw := importAs(t, app, super, body(tc.edit)); code != tc.want {
				t.Errorf("%s: %d %s, want %d", tc.why, code, raw, tc.want)
			}
		}
		if n := len(attemptsIn(t, s.State.store, "")); n != 0 {
			t.Errorf("refused imports stored %d attempts", n)
		}

		// An org's own claim citing the run is not a published one.
		c := publishedClaim{Benchmark: "gpqa_diamond", Provider: "acme", Model: "acme-1", Score: 66.7, Protocol: "hanzo-measured", Source: runURL("run-acme")}
		if code, _ := put(t, app, acme, c, Public); code != 200 {
			t.Fatalf("acme files: %d", code)
		}
		if code, _, raw := importAs(t, app, super, map[string]any{"benchmark": "gpqa_diamond", "model": "acme-1", "run": "run-acme", "data": items(3, 2)}); code != 422 {
			t.Errorf("an unpublished org claim: %d %s, want 422", code, raw)
		}
	})
}

// citesRun reads the run id from a research run address and nothing else: a
// prefix of the id, another path, or another parameter is not a citation.
func TestCitesRun(t *testing.T) {
	for _, tc := range []struct {
		source, run string
		want        bool
	}{
		{runURL("enso.gpqa"), "enso.gpqa", true},
		{"https://api.hanzo.ai/v1/research/runs?id=a&org=hanzo", "a", true},
		{runURL("enso.gpqa-2"), "enso.gpqa", false},
		{"https://api.hanzo.ai/v1/research/studies?id=a", "a", false},
		{"https://api.hanzo.ai/v1/research/runs?benchmark=a", "a", false},
		{"provider card", "a", false},
	} {
		if got := citesRun(tc.source, tc.run); got != tc.want {
			t.Errorf("citesRun(%q, %q) = %v, want %v", tc.source, tc.run, got, tc.want)
		}
	}
}
