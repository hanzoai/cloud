package research

// These tests hold the cut a page asks of the run listing to what the full
// listing serves: several ids or benchmarks at once, measures narrowed by
// category and metric, the summary view, and pages that walk the whole set in
// its order — for the org's own read and a stranger's read of what it published.

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"testing"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// listingRuns are five runs over three benchmarks, carrying measures in nested
// categories and two metrics, one with an interval and one without.
func listingRuns() []Run {
	lo, hi := 0.4, 0.6
	ms := []Measure{
		{Category: "all", Metric: "accuracy", Value: 0.5, Lo: &lo, Hi: &hi, N: new(10), Of: new(10)},
		{Category: "all", Metric: "p50ms", Value: 12},
		{Category: "vs-claude-code", Metric: "accuracy", Value: 0.6},
		{Category: "vs-claude-code/search", Metric: "accuracy", Value: 0.7},
		{Category: "vs-claude-codex", Metric: "accuracy", Value: 0.1},
		{Category: "hard", Metric: "accuracy", Value: 0.2},
	}
	run := func(id, bench string) Run {
		return Run{ID: id, Benchmark: bench, Split: "test", System: "kai", Version: "1",
			Prompt: "p.txt", PromptDigest: "pd", DatasetDigest: "dd", StoreDigest: "sd", FactsDigest: "fd",
			Embedder: "e", Reader: "r", K: new(5), MaxTokens: new(64), When: "2026-10-01T00:00:00Z", By: "ci",
			Questions: new(10), Answered: new(10), Ended: "2026-10-01T01:00:00Z", Measures: ms}
	}
	return []Run{
		run("coding-a", "coding.repair"),
		run("coding-b", "coding.repair"),
		run("gpqa-a", "enso.gpqa_diamond"),
		run("lcb-a", "enso.livecodebench"),
		run("other-a", "other"),
	}
}

func seedListing(t *testing.T, app *zip.App) {
	t.Helper()
	if code, m := do(t, app, http.MethodPost, "/v1/research/runs", "acme", "kai", runBatch{Runs: listingRuns()}); code != http.StatusOK {
		t.Fatalf("POST runs status=%d body=%v", code, m)
	}
}

func ids(m map[string]any) []string {
	var out []string
	for _, r := range m["data"].([]any) {
		out = append(out, r.(map[string]any)["id"].(string))
	}
	return out
}

func measureKeys(row map[string]any) []string {
	var out []string
	for _, x := range row["measures"].([]any) {
		m := x.(map[string]any)
		out = append(out, m["category"].(string)+"|"+m["metric"].(string))
	}
	return out
}

func TestListingTakesListsOfIDsAndBenchmarks(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountResearch(t)
		seedListing(t, app)
		for _, c := range []struct {
			query string
			want  []string
		}{
			{"benchmark=coding.repair", []string{"coding-a", "coding-b"}},
			{"benchmark=coding.repair,enso.gpqa_diamond,enso.livecodebench", []string{"coding-a", "coding-b", "gpqa-a", "lcb-a"}},
			{"benchmark=enso.livecodebench,nothing", []string{"lcb-a"}},
			{"id=gpqa-a", []string{"gpqa-a"}},
			{"id=other-a,coding-b,never", []string{"coding-b", "other-a"}},
			{"id=coding-a,coding-b&benchmark=coding.repair,other", []string{"coding-a", "coding-b"}},
		} {
			code, m := do(t, app, http.MethodGet, "/v1/research/runs?"+c.query, "acme", "kai", nil)
			if code != http.StatusOK || !slices.Equal(ids(m), c.want) || int(m["total"].(float64)) != len(c.want) {
				t.Errorf("?%s = %d %v total %v, want %v", c.query, code, ids(m), m["total"], c.want)
			}
		}

	})
}

func TestListingNarrowsMeasuresAndKeepsRuns(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountResearch(t)
		seedListing(t, app)
		for _, c := range []struct {
			query string
			want  []string
		}{
			{"category=all,vs-claude-code", []string{"all|accuracy", "all|p50ms", "vs-claude-code|accuracy", "vs-claude-code/search|accuracy"}},
			{"category=vs-claude-code/search", []string{"vs-claude-code/search|accuracy"}},
			{"metric=p50ms", []string{"all|p50ms"}},
			{"category=all,hard&metric=accuracy", []string{"all|accuracy", "hard|accuracy"}},
			{"category=nothing", nil},
		} {
			code, m := do(t, app, http.MethodGet, "/v1/research/runs?benchmark=coding.repair&"+c.query, "acme", "kai", nil)
			if code != http.StatusOK || len(m["data"].([]any)) != 2 {
				t.Fatalf("?%s = %d %v, want both coding runs whatever their measures", c.query, code, m)
			}
			for _, r := range m["data"].([]any) {
				if got := measureKeys(r.(map[string]any)); !slices.Equal(got, c.want) {
					t.Errorf("?%s measures = %v, want %v", c.query, got, c.want)
				}
			}
		}

	})
}

// dropped are the fields the summary view leaves out of a run.
var dropped = []string{"key", "prompt_digest", "dataset_digest", "store_digest", "facts_digest", "prompt",
	"embedder", "reader", "k", "temperature", "max_tokens", "when", "by", "trainable", "publishable"}

func TestSummaryViewServesWhatAPageDraws(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountResearch(t)
		seedListing(t, app)
		code, full := do(t, app, http.MethodGet, "/v1/research/runs?id=gpqa-a", "acme", "kai", nil)
		if code != http.StatusOK || full["gaps"] == nil {
			t.Fatalf("full listing = %d %v", code, full)
		}
		frow := full["data"].([]any)[0].(map[string]any)
		for _, f := range dropped {
			if _, ok := frow[f]; !ok {
				t.Errorf("the full record lost %q", f)
			}
		}
		if m := frow["measures"].([]any)[1].(map[string]any); m["lo"] != nil || !hasKey(m, "lo") {
			t.Errorf("the full record's unstated interval = %v, want an explicit null", m)
		}

		code, m := do(t, app, http.MethodGet, "/v1/research/runs?id=gpqa-a&view=summary", "acme", "kai", nil)
		if code != http.StatusOK {
			t.Fatalf("summary status=%d body=%v", code, m)
		}
		if hasKey(m, "gaps") || m["total"].(float64) != 1 || m["completion"].(map[string]any)["complete"].(float64) != 1 {
			t.Fatalf("summary envelope = %v, want total and completion and no gaps", m)
		}
		row := m["data"].([]any)[0].(map[string]any)
		for _, f := range dropped {
			if hasKey(row, f) {
				t.Errorf("summary row carries %q", f)
			}
		}
		for f := range frow {
			if !slices.Contains(dropped, f) && !hasKey(row, f) {
				t.Errorf("summary row lost %q", f)
			}
		}
		for _, f := range []string{"id", "benchmark", "split", "system", "version", "completion", "visibility", "commit", "ended"} {
			if row[f] != frow[f] {
				t.Errorf("summary %s = %v, full = %v", f, row[f], frow[f])
			}
		}
		stated := row["measures"].([]any)[0].(map[string]any)
		if stated["lo"].(float64) != 0.4 || stated["hi"].(float64) != 0.6 || stated["n"].(float64) != 10 || stated["of"].(float64) != 10 {
			t.Errorf("summary measure with an interval = %v", stated)
		}
		bare := row["measures"].([]any)[1].(map[string]any)
		for _, f := range []string{"lo", "hi", "n", "of"} {
			if hasKey(bare, f) {
				t.Errorf("summary measure carries the unstated %q: %v", f, bare)
			}
		}
		if bare["value"].(float64) != 12 || bare["metric"] != "p50ms" {
			t.Errorf("summary measure = %v", bare)
		}

		if code, _ := do(t, app, http.MethodGet, "/v1/research/runs?view=compact", "acme", "kai", nil); code != http.StatusUnprocessableEntity {
			t.Errorf("unknown view status=%d, want 422", code)
		}

	})
}

func hasKey(m map[string]any, k string) bool { _, ok := m[k]; return ok }

// walk reads every page of a listing by its cursor and answers the ids in order,
// the totals each page reported, and how many pages it took.
func walk(t *testing.T, app *zip.App, query, org string) ([]string, []float64, int) {
	t.Helper()
	var got []string
	var totals []float64
	cursor := ""
	for pages := 1; pages < 20; pages++ {
		q := query
		if cursor != "" {
			q += "&cursor=" + url.QueryEscape(cursor)
		}
		code, m := do(t, app, http.MethodGet, "/v1/research/runs?"+q, org, "", nil)
		if code != http.StatusOK {
			t.Fatalf("?%s = %d %v", q, code, m)
		}
		got = append(got, ids(m)...)
		totals = append(totals, m["total"].(float64))
		next, _ := m["next"].(string)
		if next == "" {
			if hasKey(m, "next") {
				t.Fatalf("?%s: the last page carries next %v", q, m["next"])
			}
			return got, totals, pages
		}
		cursor = next
	}
	t.Fatalf("?%s never reached a last page", query)
	return nil, nil, 0
}

func TestListingPagesWalkTheWholeSet(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountResearch(t)
		seedListing(t, app)
		_, all := do(t, app, http.MethodGet, "/v1/research/runs", "acme", "kai", nil)
		want := ids(all)
		if len(want) != 5 {
			t.Fatalf("unpaged listing = %v, want five runs", want)
		}
		for _, c := range []struct {
			limit string
			pages int
		}{{"1", 5}, {"2", 3}, {"5", 1}, {"200", 1}} {
			for _, view := range []string{"", "&view=summary"} {
				got, totals, pages := walk(t, app, "limit="+c.limit+view, "acme")
				if !slices.Equal(got, want) || pages != c.pages {
					t.Errorf("limit=%s%s walked %v in %d pages, want %v in %d", c.limit, view, got, pages, want, c.pages)
				}
				for _, n := range totals {
					if n != 5 {
						t.Errorf("limit=%s%s page total = %v, want 5 on every page", c.limit, view, n)
					}
				}
			}
		}
		// Narrowed, the pages walk the narrowed set.
		got, totals, _ := walk(t, app, "limit=1&benchmark=coding.repair,enso.livecodebench", "acme")
		if !slices.Equal(got, []string{"coding-a", "coding-b", "lcb-a"}) || totals[0] != 3 {
			t.Errorf("narrowed walk = %v totals %v", got, totals)
		}
		for _, q := range []string{"limit=201", "limit=-1", "cursor=not-a-cursor", "cursor=" + writeCursor(cursor{project: "kai"})} {
			if code, m := do(t, app, http.MethodGet, "/v1/research/runs?"+q, "acme", "kai", nil); code != http.StatusUnprocessableEntity {
				t.Errorf("?%s = %d %v, want 422", q, code, m)
			}
		}

	})
}

// TestAStrangersListingCutsTheSameWay: the anonymous ?org= read takes every
// parameter the org's own read does, over what the org published.
func TestAStrangersListingCutsTheSameWay(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountResearch(t)
		seedListing(t, app)
		pub := "public"
		for _, r := range listingRuns() {
			if r.ID == "other-a" {
				continue
			}
			if code, m := do(t, app, http.MethodPost, "/v1/research/grants", "acme", "kai",
				GrantRequest{ID: r.ID, Visibility: &pub}); code != http.StatusOK {
				t.Fatalf("grant %s status=%d body=%v", r.ID, code, m)
			}
		}
		got, totals, pages := walk(t, app, "org=acme&limit=2&view=summary&benchmark=coding.repair,enso.livecodebench,other", "")
		if !slices.Equal(got, []string{"coding-a", "coding-b", "lcb-a"}) || totals[0] != 3 || pages != 2 {
			t.Errorf("stranger walk = %v totals %v pages %d", got, totals, pages)
		}
		code, m := do(t, app, http.MethodGet, "/v1/research/runs?org=acme&id=coding-a&category=vs-claude-code&metric=accuracy&view=summary", "", "", nil)
		if code != http.StatusOK || len(m["data"].([]any)) != 1 {
			t.Fatalf("stranger narrowed read = %d %v", code, m)
		}
		if keys := measureKeys(m["data"].([]any)[0].(map[string]any)); !slices.Equal(keys, []string{"vs-claude-code|accuracy", "vs-claude-code/search|accuracy"}) {
			t.Errorf("stranger measures = %v", keys)
		}
		if code, _ := do(t, app, http.MethodGet, "/v1/research/runs?org=acme&limit=201", "", "", nil); code != http.StatusUnprocessableEntity {
			t.Errorf("stranger limit=201 status=%d, want 422", code)
		}

	})
}

// TestAListingNamingNoLimitIsAPage: a query that names no limit is answered 100
// runs and a next, so a listing as long as the record cannot arrive whole.
func TestAListingNamingNoLimitIsAPage(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountResearch(t)
		runs := make([]Run, pageDefault+5)
		for i := range runs {
			runs[i] = Run{ID: fmt.Sprintf("run-%03d", i), Benchmark: "b", System: "s"}
		}
		if code, m := do(t, app, http.MethodPost, "/v1/research/runs", "acme", "kai", runBatch{Runs: runs}); code != http.StatusOK {
			t.Fatalf("POST runs status=%d body=%v", code, m)
		}
		code, m := do(t, app, http.MethodGet, "/v1/research/runs", "acme", "kai", nil)
		if code != http.StatusOK || len(m["data"].([]any)) != pageDefault || m["total"].(float64) != float64(len(runs)) || m["next"] == nil {
			t.Fatalf("unlimited listing = %d, %d rows, total %v, next %v; want %d rows of %d and a next",
				code, len(m["data"].([]any)), m["total"], m["next"], pageDefault, len(runs))
		}
		got, _, pages := walk(t, app, "limit=0", "acme")
		if len(got) != len(runs) || pages != 2 {
			t.Errorf("walking the default pages read %d runs in %d pages, want %d in 2", len(got), pages, len(runs))
		}

	})
}
