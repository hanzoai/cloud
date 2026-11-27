package research

// The harness posts, the plane reads, and nothing in between checks that the two
// agree on what a field is called.
//
// `bench/brain/record.mjs` (hanzoai/cloud) turns a run directory into a record
// and posts it to /v1/research/runs as the run goes — at the start, at every
// pass, and at the end. testdata/recorded holds one of those bodies, generated
// by that command and committed verbatim. What it pins is the failure that is
// silent at both ends: a harness writing `prompt_sha256` where the plane reads
// `prompt_digest` produces no error anywhere. The run simply arrives with the
// field empty, and if the field is one of the three completion counts, the run
// arrives reading `unknown` — which is exactly the state the recording exists to
// leave behind.
//
// The run in the file is the one the defect happened to:
// locomo-all-context-k20-enso-flash went onto hanzo.ai/benchmarks at 57 of its
// 282 questions. Finished and posted as the harness now posts it, it reads
// `complete`; at 57 it reads `partial`, which is the query that finds it.

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// recorded reads the harness's own POST body.
func recorded(t *testing.T, id string) runBatch {
	t.Helper()
	b, err := os.ReadFile("testdata/recorded/" + id + ".json")
	if err != nil {
		t.Fatalf("read the harness's body: %v", err)
	}
	var in runBatch
	if err := json.Unmarshal(b, &in); err != nil {
		t.Fatalf("the harness's body does not decode as a run batch: %v", err)
	}
	if len(in.Runs) != 1 {
		t.Fatalf("body holds %d runs, want 1", len(in.Runs))
	}
	return in
}

// TestTheHarnessPostsWhatThePlaneReads walks the body through the door it is
// posted at and reads the run back out of the listing, so every field crosses
// the wire, the store and the derivation exactly as it does in production.
func TestTheHarnessPostsWhatThePlaneReads(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		const id = "locomo-all-context-k20-enso-flash"
		app := mountResearch(t)
		if code, _ := do(t, app, http.MethodPost, "/v1/research/runs", "acme", proj, recorded(t, id)); code != http.StatusOK {
			t.Fatalf("POST /v1/research/runs = %d", code)
		}
		code, out := do(t, app, http.MethodGet, "/v1/research/runs?id="+id, "acme", proj, nil)
		if code != http.StatusOK {
			t.Fatalf("GET /v1/research/runs = %d", code)
		}
		data, _ := out["data"].([]any)
		if len(data) != 1 {
			t.Fatalf("listing returned %d runs, want 1", len(data))
		}
		got, _ := data[0].(map[string]any)

		// The three counts completion is derived from. A name the plane does not
		// read arrives absent, and absent is what makes this assertion fail.
		for field, want := range map[string]float64{"questions": 282, "answered": 282} {
			if got[field] != want {
				t.Errorf("%s = %v, want %v — the harness and the plane disagree about this field's name", field, got[field], want)
			}
		}
		if got["ended"] == "" {
			t.Error("the run does not say when it ended")
		}
		if got["completion"] != completionComplete {
			t.Fatalf("completion = %v, want %q", got["completion"], completionComplete)
		}

		// Everything that makes the number re-runnable: the knobs, and the digests
		// that pin what it ran over.
		for field, want := range map[string]any{
			"benchmark": "locomo", "system": "context", "reader": "enso-flash",
			"k": float64(20), "temperature": float64(0), "max_tokens": float64(64),
			"prompt": "prompts/reader-locomo.txt", "study": corpusStudy,
		} {
			if got[field] != want {
				t.Errorf("%s = %v, want %v", field, got[field], want)
			}
		}
		for _, field := range []string{"prompt_digest", "dataset_digest", "store_digest", "commit"} {
			if s, _ := got[field].(string); s == "" {
				t.Errorf("%s is empty; nothing pins what this run ran over", field)
			}
		}
		if ms, _ := got["measures"].([]any); len(ms) == 0 {
			t.Error("the run carries no measures")
		}

	})
}

// TestTheHarnessAndTheLoaderRecordOneRun is the two implementations, side by
// side. The harness reads a run directory in JavaScript as it runs; the loader
// reads the same directory in Go, after the fact, to backfill what was measured
// before the record existed. They read the same three files and must produce the
// same run — otherwise which one wrote a row decides what the row says.
func TestTheHarnessAndTheLoaderRecordOneRun(t *testing.T) {
	const id = "locomo-all-context-k20-enso-flash"
	posted := recorded(t, id).Runs[0]

	var loaded Run
	for _, r := range corpus(t).Runs {
		if r.ID == id {
			loaded = r
		}
	}
	if loaded.ID == "" {
		t.Fatalf("the loader does not hold %s", id)
	}

	same := func(field string, a, b any) {
		t.Helper()
		if a != b {
			t.Errorf("%s: harness %v, loader %v", field, a, b)
		}
	}
	same("benchmark", posted.Benchmark, loaded.Benchmark)
	same("system", posted.System, loaded.System)
	same("reader", posted.Reader, loaded.Reader)
	same("commit", posted.Commit, loaded.Commit)
	same("prompt", posted.Prompt, loaded.Prompt)
	same("prompt_digest", posted.PromptDigest, loaded.PromptDigest)
	same("dataset_digest", posted.DatasetDigest, loaded.DatasetDigest)
	same("store_digest", posted.StoreDigest, loaded.StoreDigest)
	same("facts_digest", posted.FactsDigest, loaded.FactsDigest)
	same("when", posted.When, loaded.When)
	same("ended", posted.Ended, loaded.Ended)
	same("study", posted.Study, loaded.Study)
	same("questions", *posted.Questions, *loaded.Questions)
	same("answered", *posted.Answered, *loaded.Answered)
	same("k", *posted.K, *loaded.K)
	same("temperature", *posted.Temperature, *loaded.Temperature)
	same("max_tokens", *posted.MaxTokens, *loaded.MaxTokens)

	if len(posted.Measures) != len(loaded.Measures) {
		t.Fatalf("measures: harness %d, loader %d", len(posted.Measures), len(loaded.Measures))
	}
	for i, m := range posted.Measures {
		if m != loaded.Measures[i] {
			// Value, interval, n and of, at the same grain and in the same order.
			if m.Category != loaded.Measures[i].Category || m.Metric != loaded.Measures[i].Metric ||
				m.Value != loaded.Measures[i].Value || !samePtrF(m.Lo, loaded.Measures[i].Lo) ||
				!samePtrF(m.Hi, loaded.Measures[i].Hi) || !samePtrI(m.N, loaded.Measures[i].N) ||
				!samePtrI(m.Of, loaded.Measures[i].Of) {
				t.Errorf("measure %d: harness %+v, loader %+v", i, m, loaded.Measures[i])
			}
		}
	}
}

func samePtrF(a, b *float64) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

func samePtrI(a, b *int) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

// TestAListingSaysHowMuchOfItselfIsRecorded: every run listing answers two
// questions about itself — how many of the matched runs reached the end, and
// which fields some of them never stated. Reading an aggregate without them is
// how an average over runs that may not have finished gets published as a
// result, which is the whole reason this plane carries a denominator.
func TestAListingSaysHowMuchOfItselfIsRecorded(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountResearch(t)
		c := corpus(t)
		if code, _ := do(t, app, http.MethodPost, "/v1/research/runs", "acme", proj, runBatch{Runs: c.Runs}); code != http.StatusOK {
			t.Fatalf("POST /v1/research/runs = %d", code)
		}
		code, out := do(t, app, http.MethodGet, "/v1/research/runs", "acme", proj, nil)
		if code != http.StatusOK {
			t.Fatalf("GET /v1/research/runs = %d", code)
		}
		if out["total"] != float64(len(c.Runs)) {
			t.Fatalf("total = %v, want %d", out["total"], len(c.Runs))
		}

		// The tally is the whole closed set, so it sums to the total — a reader
		// never has to work out what an absent key meant.
		tally, _ := out["completion"].(map[string]any)
		sum := 0.0
		for _, v := range tally {
			n, _ := v.(float64)
			sum += n
		}
		if sum != out["total"] {
			t.Errorf("completion tally sums to %v over %v runs", sum, out["total"])
		}
		if tally["complete"] != float64(9) || tally["inconsistent"] != float64(3) ||
			tally["partial"] != float64(0) || tally["unknown"] != float64(44) {
			t.Errorf("completion = %v, want complete=9 partial=0 inconsistent=3 unknown=44", tally)
		}

		// The gaps, worst first, naming the field a reader would have gone looking
		// for. Two are unstated by every run in the tree: nobody is recorded as
		// having run anything, and no run records the version of what it measured.
		gaps, _ := out["gaps"].([]any)
		if len(gaps) == 0 {
			t.Fatal("a listing with unstated fields reported no gaps")
		}
		got := map[string]float64{}
		for _, g := range gaps {
			m, _ := g.(map[string]any)
			name, _ := m["field"].(string)
			got[name], _ = m["runs"].(float64)
		}
		for _, field := range []string{"by", "version"} {
			if got[field] != float64(len(c.Runs)) {
				t.Errorf("gap %q = %v runs, want %d — unstated by every run in the tree", field, got[field], len(c.Runs))
			}
		}
		first, _ := gaps[0].(map[string]any)
		if n, _ := first["runs"].(float64); n != float64(len(c.Runs)) {
			t.Errorf("the worst gap is %v of %d runs; the list is not worst-first", n, len(c.Runs))
		}

		// Narrowing the listing narrows the account of it: one run, and the tally
		// and the gaps are that run's.
		_, one := do(t, app, http.MethodGet, "/v1/research/runs?id=locomo-all-context-k20-enso-flash", "acme", proj, nil)
		if one["total"] != float64(1) {
			t.Fatalf("?id= returned %v runs", one["total"])
		}
		if t1, _ := one["completion"].(map[string]any); t1["complete"] != float64(1) {
			t.Errorf("the finished run's own tally = %v, want complete=1", t1)
		}

		// ?completion=partial is the query that finds a number published early.
		// Nothing in the committed tree is partial, and the listing says so rather
		// than answering with everything.
		_, none := do(t, app, http.MethodGet, "/v1/research/runs?completion=partial", "acme", proj, nil)
		if none["total"] != float64(0) {
			t.Errorf("?completion=partial matched %v runs in a tree with none", none["total"])
		}

	})
}
