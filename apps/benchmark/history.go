package benchmark

import (
	"context"
	"sort"
	"time"
)

// Measurement over time.
//
// A benchmark score is not a fact about a model, it is a fact about a model on a
// day. The model changes — a new checkpoint, a fixed serving bug, a provider
// swapping quantization underneath the same name — and the harness changes too.
// A number recorded once and never revisited slowly becomes a claim about the
// past that reads as a claim about the present.
//
// So the leaderboard answers "how good is it now" from the latest run, and this
// answers "what has it done": every run for a model, oldest first, with the
// change between them. A model that scored badly in March and well in August is
// the case that motivated this, and it is invisible to any surface that keeps
// one number per model.
//
// Nothing here recomputes. Runs are the attempts already on disk, grouped by the
// run they belong to — the append-only store was always keeping this, and until
// now nothing could read it.

// RunPoint is one measurement of one model: what it scored, over how many items,
// and when.
type RunPoint struct {
	// Run is the measurement id these attempts were recorded under.
	Run string `json:"run"`
	// At is when the run was recorded.
	At time.Time `json:"at,omitempty"`
	// Score is accuracy over the items this run covered, as a percentage.
	Score float64 `json:"score"`
	// N is how many items the run covered. Two runs are only comparable at the
	// same n, which is why it travels with every point rather than being assumed.
	N int `json:"n"`
	// Delta is the change in score from the previous run for this model, absent
	// on the first. It is the number the whole surface exists to make visible.
	Delta *float64 `json:"delta,omitempty"`
}

// ModelHistory is one model's runs on one benchmark, oldest first.
type ModelHistory struct {
	// Model is the system these runs measured.
	Model string `json:"model"`
	// Points is every run, oldest first.
	Points []RunPoint `json:"points"`
	// Trend is the change from the first run to the last, absent when there has
	// only been one. It answers the question a list of points makes you compute.
	Trend *float64 `json:"trend,omitempty"`
}

type historyIn struct {
	// Benchmark is the catalog id to read, defaulting to gpqa_diamond.
	Benchmark string `query:"benchmark"`
	// Model filters to one model. Empty returns every model measured.
	Model string `query:"model"`
}

type historyOut struct {
	// Benchmark is the catalog id these histories are about.
	Benchmark string `json:"benchmark"`
	// Data is one entry per model, ordered by model name.
	Data []ModelHistory `json:"data"`
	// Total is how many models Data holds.
	Total int `json:"total"`
}

// history returns each model's measured score per run over time, oldest first,
// with the change between runs.
//
// This is the counterweight to a leaderboard: the board shows the latest run
// because that is what "how good is it" means, and a single latest number cannot
// distinguish a model that has always been strong from one that just improved,
// or from one that regressed after a provider changed something. Both matter for
// routing, and only one of them is visible on a board.
//
// Runs with no id — attempts recorded before runs existed — group under the
// empty run, which is honestly what they are: one undated measurement.
func (o ops) history(ctx context.Context, in *historyIn) (*historyOut, error) {
	bench := in.Benchmark
	if bench == "" {
		bench = "gpqa_diamond"
	}
	data := computeHistory(o.s.State.store.Attempts(bench), bench, in.Model)
	return &historyOut{Benchmark: bench, Data: data, Total: len(data)}, nil
}

// computeHistory is the pure aggregation, testable without a service — the same
// split computeLeaderboard already makes, and for the same reason: the arithmetic
// that decides whether a model improved should be checkable on a table of
// attempts, not only through a running server.
func computeHistory(attempts []attempt, bench, model string) []ModelHistory {
	type key struct{ model, run string }
	type acc struct {
		ok, n int
		at    time.Time
	}
	byRun := map[key]*acc{}
	for _, a := range attempts {
		if a.Benchmark != bench || a.Answer == "" {
			continue
		}
		if model != "" && a.Model != model {
			continue
		}
		k := key{a.Model, a.Run}
		if byRun[k] == nil {
			byRun[k] = &acc{}
		}
		byRun[k].n++
		if a.Correct {
			byRun[k].ok++
		}
		if a.At.After(byRun[k].at) {
			byRun[k].at = a.At
		}
	}

	perModel := map[string][]RunPoint{}
	for k, v := range byRun {
		if v.n == 0 {
			continue
		}
		perModel[k.model] = append(perModel[k.model], RunPoint{
			Run:   k.run,
			At:    v.at,
			Score: float64(v.ok) / float64(v.n) * 100,
			N:     v.n,
		})
	}

	out := make([]ModelHistory, 0, len(perModel))
	for name, points := range perModel {
		// Oldest first, and by TIME rather than by run id — a run id is a name,
		// not an ordering, and sorting names would put "run-10" before "run-9".
		sort.Slice(points, func(i, j int) bool {
			if !points[i].At.Equal(points[j].At) {
				return points[i].At.Before(points[j].At)
			}
			return points[i].Run < points[j].Run
		})
		for i := 1; i < len(points); i++ {
			d := points[i].Score - points[i-1].Score
			points[i].Delta = &d
		}
		h := ModelHistory{Model: name, Points: points}
		if len(points) > 1 {
			t := points[len(points)-1].Score - points[0].Score
			h.Trend = &t
		}
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}
