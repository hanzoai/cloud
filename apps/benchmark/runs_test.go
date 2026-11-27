package benchmark

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// TestRunServesWhatIsHeldAndQueuesNothing: a run request is answered from the
// attempts already recorded, and where none are recorded the answer says that
// nothing was queued and who would measure it. Nothing is stored either way.
func TestRunServesWhatIsHeldAndQueuesNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := mount(t)
		at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
		for i, ok := range []bool{true, true, false, true} {
			a := attempt{Benchmark: "gpqa_diamond", ID: fmt.Sprintf("q%d", i), Model: "grok-4.5", Correct: ok, Answer: "A", Run: "r1", At: at}
			if err := s.State.store.Append(bg, a); err != nil {
				t.Fatal(err)
			}
		}

		ask := map[string]any{"benchmarks": []string{"gpqa_diamond", "humanitys_last_exam", "swe_bench_pro"}, "model": "grok-4.5"}
		code, raw := do(t, app, acme, "POST", "/v1/benchmark/runs", ask)
		var m measurements
		if err := json.Unmarshal(raw, &m); err != nil || code != 200 || len(m.Data) != 3 {
			t.Fatalf("run: %d %s, want 200 with one answer per benchmark", code, raw)
		}

		held, native, none := m.Data[0], m.Data[1], m.Data[2]
		if held.Benchmark != "gpqa_diamond" || held.Latest == nil || held.Latest.Run != "r1" || held.Latest.N != 4 || held.Latest.Score != 75 {
			t.Errorf("held = %+v, want gpqa_diamond served from run r1: 3 of 4", held)
		}
		if !strings.Contains(held.Note, "nothing spent") {
			t.Errorf("held note = %q, want it to say nothing was spent", held.Note)
		}
		if native.Latest != nil || native.Harness != measurer || !strings.Contains(native.Note, "nothing was queued") {
			t.Errorf("unmeasured native = %+v, want nothing queued and %s named", native, measurer)
		}
		if none.Latest != nil || none.Harness != "" || !strings.Contains(none.Note, "nothing was queued") || !strings.Contains(none.Note, "/v1/benchmark/attempts") {
			t.Errorf("unmeasured, no harness = %+v, want nothing queued and the import named", none)
		}
		if n := len(attemptsIn(t, s.State.store, "")); n != 4 {
			t.Errorf("store holds %d attempts after the request, want the 4 it held", n)
		}

		// A target nothing here would run is not part of the request: without a
		// model there is nothing held to look up.
		if code, raw := do(t, app, acme, "POST", "/v1/benchmark/runs", map[string]any{"benchmarks": []string{"gpqa_diamond"}, "endpoint": "https://example.com/v1"}); code != 400 {
			t.Errorf("endpoint without a model: %d %s, want 400", code, raw)
		}
		if code, raw := do(t, app, acme, "POST", "/v1/benchmark/runs", map[string]any{"benchmarks": []string{"gpqa", "mmlu_pro"}, "model": "grok-4.5"}); code != 422 || !strings.Contains(string(raw), "gpqa") {
			t.Errorf("unknown id: %d %s, want 422 naming it", code, raw)
		}
	})
}
