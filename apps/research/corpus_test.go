package research

// These tests run against the REAL corpus, committed verbatim under
// testdata/corpus: the harness's own `benchmarks.json` (byte-identical to the
// copy hanzo.ai/benchmarks renders) and every run directory's `meta.json` and
// `metrics.json` from hanzoai/cloud `bench/brain`.
//
// A loader tested against invented fixtures proves it can read fixtures. The
// question here is whether the record fits measurements that were made before
// it existed, and the answer includes the parts of them that were never
// recorded — which is why the gap tally below is an assertion and not a print.

import (
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
)

func corpus(t *testing.T) Corpus {
	t.Helper()
	c, err := Load(os.DirFS("testdata/corpus"))
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	return c
}

// TestCorpusLoadsIntoTheRecord: the whole tree goes in through the same store
// the HTTP surface writes, and comes back out with its completion derived.
func TestCorpusLoadsIntoTheRecord(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		c := corpus(t)
		st, ctx := newStore(t)

		if n, err := st.putBenchmarks(ctx, proj, c.Benchmarks); err != nil || n != len(c.Benchmarks) {
			t.Fatalf("benchmarks put n=%d err=%v, want %d", n, err, len(c.Benchmarks))
		}
		if n, err := st.putStudies(ctx, proj, c.Studies); err != nil || n != len(c.Studies) {
			t.Fatalf("studies put n=%d err=%v, want %d", n, err, len(c.Studies))
		}
		if n, err := st.putRuns(ctx, proj, c.Runs); err != nil || n != len(c.Runs) {
			t.Fatalf("runs put n=%d err=%v, want %d", n, err, len(c.Runs))
		}
		// Idempotent by content: re-loading the same tree appends nothing, which is
		// what makes a backfill safe to re-run.
		if n, err := st.putRuns(ctx, proj, c.Runs); err != nil || n != 0 {
			t.Fatalf("re-put n=%d err=%v, want 0", n, err)
		}

		runs, err := st.runs(ctx, runFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) != len(c.Runs) {
			t.Fatalf("stored %d runs, read back %d", len(c.Runs), len(runs))
		}
		if len(runs) != 56 {
			t.Fatalf("corpus holds %d runs; the committed tree has 56 — update the tree or the number, never neither", len(runs))
		}

		// Every run carries the measures the harness reported, joined at one grain.
		measures := 0
		for _, r := range runs {
			measures += len(r.Measures)
		}
		if measures != 2936 {
			t.Fatalf("corpus holds %d measures, want 2936 — the harness's own numbers, read not recomputed", measures)
		}

		// Completion, counted. Nine runs state that they finished; three state
		// counts that contradict each other; the rest never said.
		tally := map[string]int{}
		for _, r := range runs {
			tally[r.Completion]++
		}
		t.Logf("completion: complete=%d partial=%d inconsistent=%d unknown=%d of %d runs",
			tally[completionComplete], tally[completionPartial],
			tally[completionInconsistent], tally[completionUnknown], len(runs))
		if tally[completionComplete] != 9 || tally[completionInconsistent] != 3 ||
			tally[completionPartial] != 0 || tally[completionUnknown] != 44 {
			t.Fatalf("completion tally = %v, want complete=9 inconsistent=3 partial=0 unknown=44", tally)
		}

		// The inconsistent three are runs whose own meta.json says they answered
		// MORE questions than they set out to. Reading either count as a
		// denominator would be wrong, and the record says so rather than picking.
		var odd []string
		for _, r := range runs {
			if r.Completion == completionInconsistent {
				odd = append(odd, r.ID)
				if *r.Answered <= *r.Questions {
					t.Fatalf("%s marked inconsistent with answered %d <= questions %d", r.ID, *r.Answered, *r.Questions)
				}
			}
		}
		sort.Strings(odd)
		t.Logf("inconsistent runs (answered > questions): %v", odd)

	})
}

// TestCorpusReportsWhatItDoesNotHold is the field-by-field account of the
// corpus's own incompleteness. It is asserted rather than printed because a
// backfill that silently starts filling gaps in is a backfill that has started
// inventing them.
func TestCorpusReportsWhatItDoesNotHold(t *testing.T) {
	c := corpus(t)
	got := map[string]int{}
	for _, g := range c.Gaps {
		got[g.Field] = g.Runs
	}
	for _, g := range c.Gaps {
		t.Logf("gap: %-15s unstated by %2d of %d runs", g.Field, g.Runs, len(c.Runs))
	}
	for _, s := range c.Skipped {
		t.Logf("skipped: %s", s)
	}

	want := map[string]int{
		// Nobody is recorded as having run anything: the harness writes no
		// operator, and one is not inferred from a commit's author.
		"by": 56,
		// No run records the version of the system it measured. Two runs of one
		// system at unrecorded versions are not known to be runs of the same
		// thing, which is a real hole in comparability and not a cosmetic one.
		"version": 56,
		// Only the answer lane records the three completion facts.
		"questions": 44, "answered": 44, "ended": 44,
		// The knobs that only exist where a reader was called.
		"temperature": 28, "max_tokens": 28, "prompt": 28, "prompt_digest": 28,
		"dataset_digest": 28,
		// Digests of the retrieval store and the extracted-fact layer.
		"store_digest": 20, "facts_digest": 44,
		// Identity that the tree does state almost everywhere.
		"split": 16, "embedder": 13, "reader": 28, "commit": 4, "when": 17,
		"system": 1, "k": 1, "measures": 11,
	}
	for field, n := range want {
		if got[field] != n {
			t.Errorf("gap %q = %d runs, want %d", field, got[field], n)
		}
	}
	for field, n := range got {
		if _, named := want[field]; !named {
			t.Errorf("unnamed gap %q on %d runs — a new hole appeared in the corpus", field, n)
		}
	}
	// Two directories under runs/ are not runs: a frozen plan set and a
	// directory holding predictions with no metrics beside them. Naming them is
	// the difference between "not a run" and "a run that failed to load".
	if len(c.Skipped) != 2 ||
		!strings.HasPrefix(c.Skipped[0], "mab-dev-semantic-enso-flash") ||
		!strings.HasPrefix(c.Skipped[1], "mab-frozen-v1") {
		t.Errorf("skipped = %v, want mab-dev-semantic-enso-flash and mab-frozen-v1", c.Skipped)
	}
}

// TestCorpusBenchmarkIdsComeFromTheTree: the record's benchmark is the run
// directory's first segment, which is the harness's own rule — so the record and
// the tree cannot disagree about which task a run measured.
func TestCorpusBenchmarkIdsComeFromTheTree(t *testing.T) {
	c := corpus(t)
	defined := map[string]bool{}
	for _, b := range c.Benchmarks {
		defined[b.ID] = true
	}
	seen := map[string]int{}
	for _, r := range c.Runs {
		seen[r.Benchmark]++
		if !defined[r.Benchmark] {
			t.Errorf("run %q measured benchmark %q, which no definition covers", r.ID, r.Benchmark)
		}
		if r.Study != corpusStudy {
			t.Errorf("run %q belongs to no study", r.ID)
		}
	}
	t.Logf("runs per benchmark: %v", seen)
	if seen["locomo"] != 36 || seen["mab"] != 16 || seen["conv"] != 4 {
		t.Fatalf("runs per benchmark = %v, want locomo=36 mab=16 conv=4", seen)
	}
	// Two definitions cover benchmarks the tree reports as tables rather than
	// as runs, so they are recorded with no run against them. A definition
	// without a run is the point of keeping the task separate from the run.
	if seen["repobench-r"] != 0 || seen["longmemeval"] != 0 {
		t.Fatalf("repobench-r/longmemeval gained runs: %v — the tree reports them as tables", seen)
	}
}

// TestCorpusLicencesAreRecorded: every definition names the terms its dataset
// comes under. Three of these five are non-commercial or non-derivative, which
// is the fact a public leaderboard is built on top of and never writes down.
func TestCorpusLicencesAreRecorded(t *testing.T) {
	want := map[string]string{
		"locomo": "CC BY-NC 4.0", "conv": "CC BY-NC 4.0",
		"repobench-r": "CC BY-NC-ND 4.0", "mab": "MIT", "longmemeval": "MIT",
	}
	for _, b := range corpus(t).Benchmarks {
		if b.License != want[b.ID] {
			t.Errorf("%s licence = %q, want %q", b.ID, b.License, want[b.ID])
		}
		if b.Citation == "" || b.Dataset == "" || b.Metric == "" {
			t.Errorf("%s is missing a citation, a dataset or its official metric: %+v", b.ID, b)
		}
	}
}

// TestCorpusComparesThePublishedPair: the two locomo-all-context runs the public
// page put side by side. Both are complete in the tree as it stands now, so they
// do compare — and the comparison names the reader difference the delta is
// actually a difference in, and says the headline change sits inside the
// intervals both runs declared.
func TestCorpusComparesThePublishedPair(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		c := corpus(t)
		st, ctx := newStore(t)
		if _, err := st.putRuns(ctx, proj, c.Runs); err != nil {
			t.Fatal(err)
		}
		a, ok, _ := st.run(ctx, proj, "locomo-all-context-k20-enso-flash")
		if !ok {
			t.Fatal("the enso-flash context run is not in the corpus")
		}
		b, ok, _ := st.run(ctx, proj, "locomo-all-context-k20-gpt-oss-120b")
		if !ok {
			t.Fatal("the gpt-oss-120b context run is not in the corpus")
		}
		cmp := compare(a, b)
		if !cmp.Comparable {
			t.Fatalf("the two published context runs do not compare: %v", cmp.Blocks)
		}
		if !diffs(cmp.Config, "reader") {
			t.Fatalf("config = %+v, want the reader named — the delta is a difference in readers", cmp.Config)
		}
		if len(cmp.Deltas) == 0 {
			t.Fatal("no deltas over two runs that measured the same categories")
		}
		// Every delta is reported with whether the two intervals overlap, and the
		// headline f1 is one where they do — the change is inside the noise both
		// runs declared.
		for _, d := range cmp.Deltas {
			if d.Category == "all" && d.Metric == "f1" {
				if d.Overlap == nil {
					t.Fatal("the headline f1 delta claims nothing about its intervals")
				}
				t.Logf("all/f1: %.4f → %.4f (change %+.4f, intervals overlap %v)", d.A, d.B, d.Change, *d.Overlap)
				return
			}
		}
		t.Fatal("the two runs share no all/f1 measure")

	})
}
