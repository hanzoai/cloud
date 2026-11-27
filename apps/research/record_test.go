package research

// These tests are about the one thing the experiment record adds over a number:
// that a run says whether it finished, and that two runs are not put beside each
// other until somebody has checked they are runs of the same thing.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/principal"
)

// TestCompletionReadsTheRecordAndNothingElse is the defect this type exists for,
// as a table. A harness that checkpoints reports whatever it has answered so
// far, so 57 of 282 questions produces the same shape a finished run does —
// minus the denominator. Every row below is a record that would otherwise have
// been read as a result.
func TestCompletionReadsTheRecordAndNothingElse(t *testing.T) {
	end := "2026-09-10T21:24:01.507Z"
	for _, c := range []struct {
		name      string
		questions *int
		answered  *int
		ended     string
		want      string
	}{
		{"checkpoint published mid-run", new(282), new(57), "", completionPartial},
		{"checkpoint that also stamped an end", new(282), new(57), end, completionPartial},
		{"the same run, finished", new(282), new(282), end, completionComplete},
		{"counts agree but nothing says it ended", new(282), new(282), "", completionUnknown},
		{"no denominator at all", nil, new(282), end, completionUnknown},
		{"no numerator at all", new(282), nil, end, completionUnknown},
		{"answered more than it set out to", new(1536), new(1638), end, completionInconsistent},
		{"the empty record", nil, nil, "", completionUnknown},
	} {
		if got := completionOf(c.questions, c.answered, c.ended); got != c.want {
			t.Errorf("%s: completion = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestAbsentSurvivesTheStore: a count the record does not state must come back
// UNSTATED. The whole guard is worthless if a nil questions round-trips as 0,
// because 0 answered of 0 questions is a complete run of nothing.
func TestAbsentSurvivesTheStore(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st, ctx := newStore(t)
		in := Run{ID: "locomo-retrieval-test-facts-locomo", Benchmark: "locomo", System: "+facts"}
		if _, err := st.putRuns(ctx, proj, []Run{in}); err != nil {
			t.Fatal(err)
		}
		got, ok, err := st.run(ctx, proj, in.ID)
		if err != nil || !ok {
			t.Fatalf("read back: ok=%v err=%v", ok, err)
		}
		if got.Questions != nil || got.Answered != nil || got.K != nil || got.Temperature != nil || got.MaxTokens != nil {
			t.Fatalf("an unstated field came back stated: questions=%v answered=%v k=%v temperature=%v max_tokens=%v",
				got.Questions, got.Answered, got.K, got.Temperature, got.MaxTokens)
		}
		if got.Completion != completionUnknown {
			t.Fatalf("completion=%q, want unknown — a record that says nothing must not read as finished", got.Completion)
		}
		// And the wire says null, not 0: a client reading JSON has to be able to
		// tell them apart too.
		b, _ := json.Marshal(got)
		var wire map[string]any
		_ = json.Unmarshal(b, &wire)
		if wire["questions"] != nil || wire["k"] != nil {
			t.Fatalf("wire rendered an absent count as %v / %v, want null", wire["questions"], wire["k"])
		}

	})
}

// TestRunCorrectionSupersedesAndRetains is the fix path for a number published
// from a checkpoint: the corrected run APPENDS a version, the partial one stays
// retained, and the canonical read is the corrected one.
func TestRunCorrectionSupersedesAndRetains(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st, ctx := newStore(t)
		id := "locomo-all-context-k20-enso-flash"
		mid := Run{ID: id, Benchmark: "locomo", System: "context", Questions: new(282), Answered: new(57),
			Measures: []Measure{{Category: "all", Metric: "f1", Value: 54.6}}}
		done := Run{ID: id, Benchmark: "locomo", System: "context", Questions: new(282), Answered: new(282),
			Ended: "2026-09-10T21:24:01.507Z", Revision: "corrected",
			Measures: []Measure{{Category: "all", Metric: "f1", Value: 45.1}}}

		if n, err := st.putRuns(ctx, proj, []Run{mid}); err != nil || n != 1 {
			t.Fatalf("first put n=%d err=%v", n, err)
		}
		if n, err := st.putRuns(ctx, proj, []Run{mid}); err != nil || n != 0 {
			t.Fatalf("re-put n=%d err=%v, want 0 — identical content appends nothing", n, err)
		}
		if n, err := st.putRuns(ctx, proj, []Run{done}); err != nil || n != 1 {
			t.Fatalf("correction n=%d err=%v, want 1 appended", n, err)
		}
		got, ok, _ := st.run(ctx, proj, id)
		if !ok || got.Completion != completionComplete || got.Measures[0].Value != 45.1 || got.Revision != "corrected" {
			t.Fatalf("canonical run = %+v, want the corrected complete run at 45.1", got)
		}
		// Retained: both versions are still in the file. The published mistake is
		// not erased by its correction.
		recs, err := loadAll[runRecord](ctx, st, kindRun)
		if err != nil || len(recs) != 2 {
			t.Fatalf("retained versions = %d err=%v, want 2", len(recs), err)
		}

		// A retraction withdraws the id entirely; every version stays retained.
		gone := done
		gone.Revision = "retracted"
		if n, err := st.putRuns(ctx, proj, []Run{gone}); err != nil || n != 1 {
			t.Fatalf("retraction n=%d err=%v, want 1", n, err)
		}
		if _, ok, _ := st.run(ctx, proj, id); ok {
			t.Fatal("a retracted run still has a canonical version")
		}
		recs, _ = loadAll[runRecord](ctx, st, kindRun)
		if len(recs) != 3 {
			t.Fatalf("retained after retraction = %d, want 3", len(recs))
		}

	})
}

// TestCompareStatesWhatStopsIt: the comparison's job is to refuse before it
// subtracts. A partial run, a different split and different dataset bytes each
// have to say so by name.
func TestCompareStatesWhatStopsIt(t *testing.T) {
	base := Run{ID: "a", Benchmark: "locomo", Split: "all", System: "context",
		DatasetDigest: "79fa87e90f040813", Questions: new(282), Answered: new(282),
		Ended: "2026-09-10T21:24:01Z", Reader: "enso-flash", K: new(20)}
	base.Completion = completionOf(base.Questions, base.Answered, base.Ended)

	partial := base
	partial.ID = "b"
	partial.Answered = new(57)
	partial.Reader = "gpt-oss-120b"
	partial.Completion = completionOf(partial.Questions, partial.Answered, partial.Ended)

	c := compare(base, partial)
	if c.Comparable {
		t.Fatal("a finished run and a 57-of-282 checkpoint were reported comparable")
	}
	if !mentions(c.Blocks, "not established as complete") {
		t.Fatalf("blocks = %v, want the incomplete run named", c.Blocks)
	}
	// The configuration difference is reported whether or not the pair is
	// comparable — the incomparable pair is the one whose differences most
	// need naming.
	if !diffs(c.Config, "reader") {
		t.Fatalf("config = %+v, want the reader difference named", c.Config)
	}

	other := base
	other.ID = "c"
	other.Split = "test"
	other.DatasetDigest = "0000000000000000"
	other.Completion = base.Completion
	c = compare(base, other)
	if c.Comparable {
		t.Fatal("runs on different splits and different dataset bytes were reported comparable")
	}
	if !mentions(c.Blocks, "different splits") || !mentions(c.Blocks, "different dataset digests") {
		t.Fatalf("blocks = %v, want both the split and the dataset named", c.Blocks)
	}
}

// TestCompareSubtractsOnlyWhatBothMeasured, and says when a change is inside the
// noise both runs already declared.
func TestCompareSubtractsOnlyWhatBothMeasured(t *testing.T) {
	mk := func(id string, f1 float64, lo, hi float64, extra bool) Run {
		r := Run{ID: id, Benchmark: "locomo", Split: "all", System: "context",
			DatasetDigest: "79fa87e90f040813", Questions: new(282), Answered: new(282),
			Ended:    "2026-09-10T21:24:01Z",
			Measures: []Measure{{Category: "all", Metric: "f1", Value: f1, Lo: new(lo), Hi: new(hi)}}}
		if extra {
			r.Measures = append(r.Measures, Measure{Category: "all", Metric: "supported", Value: 0.13})
		}
		r.Completion = completionOf(r.Questions, r.Answered, r.Ended)
		return r
	}
	c := compare(mk("a", 0.451, 0.411, 0.490, false), mk("b", 0.454, 0.412, 0.493, true))
	if !c.Comparable {
		t.Fatalf("blocks = %v, want none", c.Blocks)
	}
	if len(c.Deltas) != 1 {
		t.Fatalf("deltas = %+v, want exactly the one metric both runs measured", c.Deltas)
	}
	d := c.Deltas[0]
	if d.Metric != "f1" || d.Overlap == nil || !*d.Overlap {
		t.Fatalf("delta = %+v, want f1 with overlapping intervals — a change inside declared noise", d)
	}
	// No interval on either side means nothing is claimed about overlap, rather
	// than a claim of no overlap.
	a, b := mk("a", 1, 0, 2, false), mk("b", 2, 0, 3, false)
	a.Measures[0].Lo, a.Measures[0].Hi = nil, nil
	if o := compare(a, b).Deltas[0].Overlap; o != nil {
		t.Fatalf("overlap = %v with one interval missing, want null", *o)
	}
}

// TestRecordRoundTripAndTenantIsolation walks the surface the way a caller does,
// and proves the org boundary holds across the new kinds: the file IS the org.
func TestRecordRoundTripAndTenantIsolation(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountResearch(t)

		code, m := do(t, app, http.MethodPost, "/v1/research/benchmarks", "acme", "enso-bench",
			benchmarkBatch{Benchmarks: []ResearchBenchmark{{ID: "locomo", Title: "LoCoMo", License: "CC BY-NC 4.0", Metric: "f1"}}})
		if code != http.StatusOK || m["recorded"].(float64) != 1 {
			t.Fatalf("POST benchmarks status=%d body=%v", code, m)
		}
		code, m = do(t, app, http.MethodPost, "/v1/research/studies", "acme", "enso-bench",
			studyBatch{Studies: []Study{{ID: "memory-retrieval", Question: "does connection beat similarity"}}})
		if code != http.StatusOK || m["recorded"].(float64) != 1 {
			t.Fatalf("POST studies status=%d body=%v", code, m)
		}
		runs := []Run{
			{ID: "locomo-all-context-k20-enso-flash", Benchmark: "locomo", Split: "all", System: "context",
				Study: "memory-retrieval", Questions: new(282), Answered: new(282), Ended: "2026-09-10T21:24:01Z",
				When: "2026-09-10T01:19:00Z", Measures: []Measure{{Category: "all", Metric: "f1", Value: 0.451}}},
			{ID: "locomo-all-context-k20-gpt-oss-120b", Benchmark: "locomo", Split: "all", System: "context",
				Study: "memory-retrieval", Questions: new(282), Answered: new(181), When: "2026-09-09T01:19:00Z"},
		}
		code, m = do(t, app, http.MethodPost, "/v1/research/runs", "acme", "enso-bench", runBatch{Runs: runs})
		if code != http.StatusOK || m["recorded"].(float64) != 2 {
			t.Fatalf("POST runs status=%d body=%v", code, m)
		}
		code, m = do(t, app, http.MethodPost, "/v1/research/papers", "acme", "enso-bench",
			paperBatch{Papers: []Paper{{ID: "hip-1145", Title: "The Experiment Record", Status: "published",
				Study: "memory-retrieval", Cites: []string{"locomo-all-context-k20-enso-flash"}}}})
		if code != http.StatusOK || m["recorded"].(float64) != 1 {
			t.Fatalf("POST papers status=%d body=%v", code, m)
		}

		// ?completion=partial is the query the record was built to answer.
		code, m = do(t, app, http.MethodGet, "/v1/research/runs?completion=partial", "acme", "enso-bench", nil)
		if code != http.StatusOK || m["total"].(float64) != 1 {
			t.Fatalf("partial listing = %v", m)
		}
		if got := m["data"].([]any)[0].(map[string]any)["id"]; got != "locomo-all-context-k20-gpt-oss-120b" {
			t.Fatalf("partial run = %v, want the 181-of-282 one", got)
		}
		// A completion outside the closed set is refused rather than matching all.
		if code, _ = do(t, app, http.MethodGet, "/v1/research/runs?completion=finished", "acme", "enso-bench", nil); code != http.StatusUnprocessableEntity {
			t.Fatalf("unknown completion status=%d, want 422", code)
		}
		// ?cites= finds everything a correction would invalidate.
		code, m = do(t, app, http.MethodGet, "/v1/research/papers?cites=locomo-all-context-k20-enso-flash", "acme", "enso-bench", nil)
		if code != http.StatusOK || m["total"].(float64) != 1 {
			t.Fatalf("cites listing = %v", m)
		}
		// Date bounds read the run's start, and a bound never matches an unstated date.
		code, m = do(t, app, http.MethodGet, "/v1/research/runs?since=2026-09-10T00:00:00Z", "acme", "enso-bench", nil)
		if code != http.StatusOK || m["total"].(float64) != 1 {
			t.Fatalf("since listing = %v", m)
		}

		// The comparison refuses, in words, and still names the configuration.
		code, m = do(t, app, http.MethodGet,
			"/v1/research/compare?a=locomo-all-context-k20-enso-flash&b=locomo-all-context-k20-gpt-oss-120b",
			"acme", "enso-bench", nil)
		if code != http.StatusOK {
			t.Fatalf("compare status=%d body=%v", code, m)
		}
		if m["comparable"].(bool) {
			t.Fatalf("compare reported a finished run and a 181-of-282 one comparable: %v", m)
		}
		if len(m["blocks"].([]any)) == 0 {
			t.Fatal("compare refused without saying why")
		}

		// Another org's store is another FILE: it sees none of this.
		code, m = do(t, app, http.MethodGet, "/v1/research/runs", "other", "enso-bench", nil)
		if code != http.StatusOK || m["total"].(float64) != 0 {
			t.Fatalf("cross-org run listing = %v, want 0", m)
		}
		// And no validated org at all is refused outright.
		if code, _ = do(t, app, http.MethodGet, "/v1/research/runs", "", "", nil); code != http.StatusUnauthorized {
			t.Fatalf("anonymous listing status=%d, want 401", code)
		}

	})
}

// TestRecordingGrantsNothing: a run is a measurement, so it carries the same
// private-by-default consent an experiment does, and the same separate grant
// moves it.
func TestRecordingGrantsNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountResearch(t)
		id := "locomo-all-context-k20-enso-flash"
		code, m := do(t, app, http.MethodPost, "/v1/research/runs", "acme", "enso-bench",
			runBatch{Runs: []Run{{ID: id, Benchmark: "locomo", System: "context", Trainable: true, Publishable: true, Visibility: "public"}}})
		if code != http.StatusOK {
			t.Fatalf("POST runs status=%d body=%v", code, m)
		}
		code, m = do(t, app, http.MethodGet, "/v1/research/runs?id="+id, "acme", "enso-bench", nil)
		row := m["data"].([]any)[0].(map[string]any)
		if row["visibility"] != "private" || row["trainable"].(bool) || row["publishable"].(bool) {
			t.Fatalf("a write set its own consent: %v", row)
		}
		pub := "public"
		yes := true
		code, m = do(t, app, http.MethodPost, "/v1/research/grants", "acme", "enso-bench",
			GrantRequest{ID: id, Visibility: &pub, Trainable: &yes})
		if code != http.StatusOK || m["updated"].(float64) != 1 {
			t.Fatalf("grant status=%d body=%v, want 1 run version moved", code, m)
		}
		code, m = do(t, app, http.MethodGet, "/v1/research/runs?id="+id, "acme", "enso-bench", nil)
		row = m["data"].([]any)[0].(map[string]any)
		if row["visibility"] != "public" || !row["trainable"].(bool) || row["publishable"].(bool) {
			t.Fatalf("grant did not land as written: %v", row)
		}

	})
}

// TestRecordNeedsAnIdentity: the three fields without which a record cannot be
// found again are refused, and a run whose system is genuinely unrecorded is
// NOT — requiring a field the source cannot supply is how records get invented.
func TestRecordNeedsAnIdentity(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountResearch(t)
		for _, c := range []struct {
			path string
			body any
			want int
		}{
			{"/v1/research/benchmarks", benchmarkBatch{Benchmarks: []ResearchBenchmark{{Title: "no id"}}}, http.StatusUnprocessableEntity},
			{"/v1/research/runs", runBatch{Runs: []Run{{ID: "x", System: "y"}}}, http.StatusUnprocessableEntity},
			{"/v1/research/studies", studyBatch{Studies: []Study{{Title: "no id"}}}, http.StatusUnprocessableEntity},
			{"/v1/research/papers", paperBatch{Papers: []Paper{{Title: "no id"}}}, http.StatusUnprocessableEntity},
			{"/v1/research/runs", runBatch{}, http.StatusBadRequest},
			{"/v1/research/runs", runBatch{Runs: []Run{{ID: "x", Benchmark: "locomo"}}}, http.StatusOK},
		} {
			if code, m := do(t, app, http.MethodPost, c.path, "acme", "enso-bench", c.body); code != c.want {
				t.Errorf("POST %s %#v status=%d body=%v, want %d", c.path, c.body, code, m, c.want)
			}
		}

	})
}

func mentions(xs []string, want string) bool {
	for _, x := range xs {
		if strings.Contains(x, want) {
			return true
		}
	}
	return false
}

func diffs(ds []Difference, field string) bool {
	for _, d := range ds {
		if d.Field == field {
			return true
		}
	}
	return false
}

// TestCompareRefusesAnUnnamedSystem: a delta between two runs whose system is
// unrecorded is a delta in something, and nothing says in what.
func TestCompareRefusesAnUnnamedSystem(t *testing.T) {
	mk := func(id, system string) Run {
		r := Run{ID: id, Benchmark: "locomo", Split: "all", System: system,
			Questions: new(10), Answered: new(10), Ended: "2026-09-10T21:24:01Z"}
		r.Completion = completionOf(r.Questions, r.Answered, r.Ended)
		return r
	}
	c := compare(mk("a", "context"), mk("b", ""))
	if c.Comparable || !mentions(c.Blocks, "does not name the system under test") {
		t.Fatalf("comparable=%v blocks=%v, want a refusal naming the unrecorded system", c.Comparable, c.Blocks)
	}
	if !compare(mk("a", "context"), mk("b", "cer")).Comparable {
		t.Fatal("two named systems did not compare")
	}
}

// TestAStrangerListsOnlyWhatTheNamedOrgPublished: GET /v1/research/runs?org=
// answers a caller with no org the runs that org granted public and nothing else,
// while a caller with an org reads its own exactly as before, ?org= or not.
//
// Until the named org has published a run the answer is the unrouted 404, the
// bytes the stage gives an org outside research, and asking mints no store. Once
// it has, the answer is a listing, so a private run is what an absent run is in any
// listing: an empty page, not a 404 of its own.
func TestAStrangerListsOnlyWhatTheNamedOrgPublished(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountResearch(t)
		runs := []Run{
			{ID: "orig-test-kai", Benchmark: "orig", Split: "test", System: "kai",
				Questions: new(10), Answered: new(10), Ended: "2026-09-27T00:00:00Z",
				Measures: []Measure{{Category: "all", Metric: "accuracy", Value: 0.7}}},
			{ID: "orig-test-kai-private", Benchmark: "orig", Split: "test", System: "kai"},
		}
		if code, m := do(t, app, http.MethodPost, "/v1/research/runs", "acme", "kai", runBatch{Runs: runs}); code != http.StatusOK {
			t.Fatalf("POST runs status=%d body=%v", code, m)
		}
		_, miss := doRaw(t, app, http.MethodGet, "/v1/research/nosuchthing", "", "")
		unseen := func(path string) {
			t.Helper()
			if code, body := doRaw(t, app, http.MethodGet, path, "", ""); code != http.StatusNotFound || string(body) != string(miss) {
				t.Errorf("GET %s with no org = %d %s, want the unrouted 404 %s", path, code, body, miss)
			}
		}
		unseen("/v1/research/runs?org=acme")     // recorded, nothing granted
		unseen("/v1/research/runs?org=stranger") // no store at all
		if mountedStores.Has(principal.MustOrgNamespace("stranger", "")) {
			t.Fatal("a read naming an org with no store created one")
		}
		if code, _ := do(t, app, http.MethodGet, "/v1/research/runs", "", "", nil); code != http.StatusUnauthorized {
			t.Fatalf("no org and no ?org= status=%d, want the 401 it always had", code)
		}

		pub := "public"
		if code, m := do(t, app, http.MethodPost, "/v1/research/grants", "acme", "kai",
			GrantRequest{ID: "orig-test-kai", Visibility: &pub}); code != http.StatusOK {
			t.Fatalf("grant status=%d body=%v", code, m)
		}
		code, m := do(t, app, http.MethodGet, "/v1/research/runs?org=acme&project=kai", "", "", nil)
		if code != http.StatusOK || m["total"].(float64) != 1 {
			t.Fatalf("stranger listing = %d %v, want the one granted run", code, m)
		}
		row := m["data"].([]any)[0].(map[string]any)
		if row["id"] != "orig-test-kai" || row["visibility"] != "public" || row["completion"] != "complete" ||
			row["measures"].([]any)[0].(map[string]any)["value"].(float64) != 0.7 {
			t.Fatalf("public run = %v", row)
		}

		// The private run and a run never recorded read the same.
		_, private := doRaw(t, app, http.MethodGet, "/v1/research/runs?org=acme&id=orig-test-kai-private", "", "")
		_, absent := doRaw(t, app, http.MethodGet, "/v1/research/runs?org=acme&id=never-recorded", "", "")
		if string(private) != string(absent) || !strings.Contains(string(private), `"total":0`) {
			t.Fatalf("private run %s, absent run %s: want one empty page for both", private, absent)
		}
		// The org has published, so its listing refuses a bad filter as any listing does.
		if code, _ := do(t, app, http.MethodGet, "/v1/research/runs?org=acme&completion=finished", "", "", nil); code != http.StatusUnprocessableEntity {
			t.Fatalf("stranger unknown completion status=%d, want 422", code)
		}

		// A caller with an org reads its own, all of it, whatever ?org= says.
		for _, path := range []string{"/v1/research/runs", "/v1/research/runs?org=other"} {
			if code, m := do(t, app, http.MethodGet, path, "acme", "kai", nil); code != http.StatusOK || m["total"].(float64) != 2 {
				t.Fatalf("acme GET %s = %d %v, want both of its runs", path, code, m)
			}
		}
		if code, m := do(t, app, http.MethodGet, "/v1/research/runs?org=acme", "other", "", nil); code != http.StatusOK || m["total"].(float64) != 0 {
			t.Fatalf("another org's member naming acme = %d %v, want its own empty listing", code, m)
		}

	})
}

// A NUL in a string is refused with a reason on both engines, not recorded on one
// and a 500 on the other: SQLite's TEXT keeps it and PostgreSQL's jsonb does not.
func TestAStringHoldingANULIsRefusedOnBothEngines(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountResearch(t)
		exp := Experiment{ID: "benchmark:m:t", Kind: "benchmark", Subject: "m", Metric: "accuracy", Value: 1}
		att := Attempt{Benchmark: "t", Item: "q1", Model: "m", Response: "answer\x00tail"}
		if code, m := do(t, app, http.MethodPost, "/v1/research/experiments", "acme", "enso-bench",
			IngestRequest{Experiments: []Experiment{exp}, Attempts: []Attempt{att}}); code != http.StatusUnprocessableEntity {
			t.Fatalf("an attempt holding a NUL: %d %v, want 422", code, m)
		}
		run := Run{ID: "r1", Benchmark: "b", Split: "held", System: "s\x00"}
		if code, m := do(t, app, http.MethodPost, "/v1/research/runs", "acme", "enso-bench", runBatch{Runs: []Run{run}}); code != http.StatusUnprocessableEntity {
			t.Fatalf("a run holding a NUL: %d %v, want 422", code, m)
		}
		att.Response = "answer"
		if code, m := do(t, app, http.MethodPost, "/v1/research/experiments", "acme", "enso-bench",
			IngestRequest{Experiments: []Experiment{exp}, Attempts: []Attempt{att}}); code != http.StatusOK {
			t.Fatalf("the same without the NUL: %d %v, want 200", code, m)
		}
	})
}
