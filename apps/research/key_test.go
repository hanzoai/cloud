package research

// A run's key is what lets a harness ask "is this already measured?" before it
// spends. These tests hold the three things that question needs: the bytes are
// the same ones every client writes, the key moves with what decides a number
// and with nothing else, and the listing answers ?key= on every row ever stored.

import (
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// keyVectors is the shared test vector. hanzo-research (pkg/hanzo-research,
// tests/test_measure.py) asserts the same two hex strings for the same two
// identities, so a key computed client-side is the key the listing filters on.
// Changing the canonical form is a change to both, in the same breath.
var keyVectors = []struct {
	run  Run
	want string
}{
	{Run{Benchmark: "locomo", Split: "all", System: "enso-flash", Version: "1.2.0",
		K: new(20), Temperature: new(0.0), MaxTokens: new(512),
		Embedder: "text-embedding-3-small", Reader: "zen6",
		PromptDigest: "9f2c", DatasetDigest: "79fa87e90f040813"},
		"098c0e3fe9a62a04ee9b814f62f4812b058cdadc6c72521b5a0112a5440258da"},
	{Run{Benchmark: "coding.repair", Split: "held", System: "hanzo-dev", Temperature: new(0.7)},
		"8304fbac139f449f61526f5d58e8c429df235230da789367159c7ab98609a552"},
}

func TestKeyVector(t *testing.T) {
	for _, v := range keyVectors {
		if got := keyOf(v.run); got != v.want {
			t.Errorf("keyOf(%s/%s/%s) = %s, want %s", v.run.Benchmark, v.run.Split, v.run.System, got, v.want)
		}
	}
}

// TestKeyIsTheMeasurementNotTheRecord: who ran it, when, at which commit, under
// which id and what it found are facts about one execution. The key is the
// thing measured, so two executions of one configuration share it — and any
// knob that changes the number gives a different one.
func TestKeyIsTheMeasurementNotTheRecord(t *testing.T) {
	base := keyVectors[0].run
	same := base
	same.ID, same.Study, same.Commit, same.When, same.Ended, same.By = "another-id", "s2", "abc123", "2026-10-15T00:00:00Z", "2026-10-15T01:00:00Z", "ci"
	same.Questions, same.Answered, same.Notes, same.Prompt = new(282), new(282), "rerun", "prompts/v2.txt"
	same.Measures = []Measure{{Category: "all", Metric: "f1", Value: 0.45}}
	if keyOf(same) != keyOf(base) {
		t.Fatal("a re-execution of the same configuration got a different key")
	}

	for name, change := range map[string]func(*Run){
		"benchmark":      func(r *Run) { r.Benchmark = "mab" },
		"split":          func(r *Run) { r.Split = "test" },
		"system":         func(r *Run) { r.System = "context" },
		"version":        func(r *Run) { r.Version = "1.2.1" },
		"k":              func(r *Run) { r.K = new(10) },
		"k unstated":     func(r *Run) { r.K = nil },
		"temperature":    func(r *Run) { r.Temperature = new(0.2) },
		"temperature ∅":  func(r *Run) { r.Temperature = nil },
		"max_tokens":     func(r *Run) { r.MaxTokens = new(1024) },
		"embedder":       func(r *Run) { r.Embedder = "bge-m3" },
		"reader":         func(r *Run) { r.Reader = "none" },
		"prompt_digest":  func(r *Run) { r.PromptDigest = "aa" },
		"dataset_digest": func(r *Run) { r.DatasetDigest = "bb" },
		"store_digest":   func(r *Run) { r.StoreDigest = "cc" },
		"facts_digest":   func(r *Run) { r.FactsDigest = "dd" },
		// Field boundaries are part of the bytes: moving text from one field
		// into its neighbour is a different identity, not the same string.
		"boundary": func(r *Run) { r.Split, r.System = "allenso-flash", "" },
	} {
		r := base
		change(&r)
		if keyOf(r) == keyOf(base) {
			t.Errorf("changing %s left the key unchanged", name)
		}
	}
}

// TestKeyListsTheHeldMeasurement walks the read a harness makes before it
// spends: every run comes back with its key, ?key= narrows to the runs of that
// identity, and a key sent in a write is ignored — it is derived, never taken.
func TestKeyListsTheHeldMeasurement(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountResearch(t)
		held := keyVectors[0].run
		held.ID, held.Questions, held.Answered, held.Ended = "locomo-all-k20-enso-flash", new(282), new(282), "2026-09-10T21:24:01Z"
		held.Key = "forged"
		other := Run{ID: "coding-held-dev", Benchmark: "coding.repair", Split: "held", System: "hanzo-dev"}
		if code, m := do(t, app, http.MethodPost, "/v1/research/runs", "acme", "enso-bench", runBatch{Runs: []Run{held, other}}); code != http.StatusOK {
			t.Fatalf("record: %d %v", code, m)
		}

		want := keyVectors[0].want
		code, m := do(t, app, http.MethodGet, "/v1/research/runs?completion=complete&key="+want, "acme", "enso-bench", nil)
		if code != http.StatusOK || m["total"].(float64) != 1 {
			t.Fatalf("?key= listing: %d %v, want the one held run", code, m)
		}
		row := m["data"].([]any)[0].(map[string]any)
		if row["id"] != held.ID || row["key"] != want {
			t.Fatalf("hit = %v / %v, want %s under %s", row["id"], row["key"], held.ID, want)
		}

		if _, m = do(t, app, http.MethodGet, "/v1/research/runs?key=forged", "acme", "enso-bench", nil); m["total"].(float64) != 0 {
			t.Fatalf("a key sent in the body was stored: %v", m)
		}
		if _, m = do(t, app, http.MethodGet, "/v1/research/runs", "acme", "enso-bench", nil); m["total"].(float64) != 2 {
			t.Fatalf("unfiltered listing: %v", m)
		}
		for _, r := range m["data"].([]any) {
			if k, _ := r.(map[string]any)["key"].(string); len(k) != 64 {
				t.Fatalf("run %v reads without a key", r.(map[string]any)["id"])
			}
		}

	})
}
