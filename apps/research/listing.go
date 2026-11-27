package research

// listing.go is how a run listing is cut for a reader who wants less than the
// whole record: several ids or benchmarks at once, a run's measures narrowed to
// the slices and quantities a page draws, the summary view that drops what only
// a harness reads, and pages in the listing's own order.
//
// None of it changes what a run IS. A narrowed measure list, a summary row and a
// page are all read from the same canonical runs the full listing serves; the
// census and the gaps still describe every run the query matched, so a reader
// of one page still learns how much of the whole set is recorded.

import (
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/zap-proto/zip"
)

// pageMax is the most runs one page carries, and pageDefault how many a query
// that names no limit is given.
const (
	pageMax     = 200
	pageDefault = 100
)

// viewSummary is the one view besides the full record.
const viewSummary = "summary"

// listQuery is a run listing's query, parsed and checked once.
type listQuery struct {
	filter     runFilter
	categories []string
	metrics    []string
	summary    bool
	limit      int
	after      *cursor
}

// cursor is where the previous page ended: the last run's project and id, the
// two keys the listing is ordered by.
type cursor struct {
	project, id string
}

// query parses a listing's query, refusing a completion outside the closed set,
// a view nobody serves, a limit above pageMax or below zero, and a cursor this
// listing did not issue, rather than letting any of them quietly match something
// else. A zero limit is an absent one, and an absent one is pageDefault.
func (in runsIn) query() (listQuery, error) {
	if in.Completion != "" && !completions[in.Completion] {
		return listQuery{}, zip.Errorf(http.StatusUnprocessableEntity,
			"completion must be complete, partial, inconsistent, or unknown")
	}
	if in.View != "" && in.View != viewSummary {
		return listQuery{}, zip.Errorf(http.StatusUnprocessableEntity, "view must be summary, or absent for the full record")
	}
	q := listQuery{
		filter: runFilter{
			Project: in.Project, IDs: items(in.ID), Key: in.Key, Benchmarks: items(in.Benchmark),
			System: in.System, Study: in.Study, Completion: in.Completion, Since: in.Since, Until: in.Until,
		},
		categories: items(in.Category),
		metrics:    items(in.Metric),
		summary:    in.View == viewSummary,
	}
	if in.Limit < 0 || in.Limit > pageMax {
		return listQuery{}, zip.Errorf(http.StatusUnprocessableEntity, "limit must be 1 to 200")
	}
	q.limit = in.Limit
	if q.limit == 0 {
		q.limit = pageDefault
	}
	if in.Cursor != "" {
		c, ok := readCursor(in.Cursor)
		if !ok {
			return listQuery{}, zip.Errorf(http.StatusUnprocessableEntity, "cursor is not one this listing issued")
		}
		q.after = &c
	}
	return q, nil
}

// answer is the listing for the runs the filter matched, in the listing's order:
// counted whole, then cut to the page, each run's measures narrowed, in the view
// asked for.
func (q listQuery) answer(rs []Run) *zip.Or[runsOut, runsSummary] {
	total, census := len(rs), censusOf(rs)
	var gaps []Gap
	if !q.summary {
		gaps = gapsIn(rs)
	}
	page, next := q.page(rs)
	for i := range page {
		page[i].Measures = q.narrow(page[i].Measures)
	}
	if q.summary {
		rows := make([]RunSummary, len(page))
		for i, r := range page {
			rows[i] = summarize(r)
		}
		return &zip.Or[runsOut, runsSummary]{B: &runsSummary{Data: rows, Total: total, Next: next, Completion: census}}
	}
	return &zip.Or[runsOut, runsSummary]{A: &runsOut{Data: page, Total: total, Next: next, Completion: census, Gaps: gaps}}
}

// page is the runs after the cursor, at most limit of them, and the cursor that
// reads the rest — empty when nothing is left.
func (q listQuery) page(rs []Run) ([]Run, string) {
	if q.after != nil {
		at := len(rs)
		for i, r := range rs {
			if r.Project > q.after.project || (r.Project == q.after.project && r.ID > q.after.id) {
				at = i
				break
			}
		}
		rs = rs[at:]
	}
	if len(rs) <= q.limit {
		return rs, ""
	}
	last := rs[q.limit-1]
	return rs[:q.limit], writeCursor(cursor{project: last.Project, id: last.ID})
}

// narrow keeps the measures whose category is one asked for or lies under one,
// and whose metric is one asked for. Nothing asked keeps everything.
func (q listQuery) narrow(ms []Measure) []Measure {
	if len(q.categories) == 0 && len(q.metrics) == 0 {
		return ms
	}
	out := []Measure{}
	for _, m := range ms {
		if q.keepsCategory(m.Category) && q.keepsMetric(m.Metric) {
			out = append(out, m)
		}
	}
	return out
}

func (q listQuery) keepsCategory(c string) bool {
	if len(q.categories) == 0 {
		return true
	}
	for _, want := range q.categories {
		if c == want || strings.HasPrefix(c, want+"/") {
			return true
		}
	}
	return false
}

func (q listQuery) keepsMetric(m string) bool {
	if len(q.metrics) == 0 {
		return true
	}
	for _, want := range q.metrics {
		if m == want {
			return true
		}
	}
	return false
}

// items is a comma-separated query value as its items, blanks dropped.
func items(v string) []string {
	var out []string
	for item := range strings.SplitSeq(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func writeCursor(c cursor) string {
	return base64.RawURLEncoding.EncodeToString([]byte(c.project + "\x00" + c.id))
}

// readCursor parses a cursor writeCursor issued. A run id is never empty, so a
// cursor naming none was not issued here.
func readCursor(s string) (cursor, bool) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursor{}, false
	}
	project, id, ok := strings.Cut(string(b), "\x00")
	if !ok || id == "" || strings.Contains(id, "\x00") {
		return cursor{}, false
	}
	return cursor{project: project, id: id}, true
}

// runsSummary is a run listing in the summary view: what a page draws of each
// run, and the census of every run the query matched.
type runsSummary struct {
	// Data are the canonical runs, ordered by project then id, each as the
	// summary view serves it: one page of them.
	Data []RunSummary `json:"data"`
	// Total is how many runs the query matched, across every page.
	Total int `json:"total"`
	// Next continues the listing: pass it back as `cursor`. Absent on the last
	// page.
	Next string `json:"next,omitempty"`
	// Completion counts the matched runs, across every page, by completion.
	Completion Census `json:"completion"`
}

// RunSummary is a run as a page draws it: what was measured, by which system,
// whether it finished, and its numbers. It leaves out what only re-running the
// measurement reads — the key, the digests, the prompt, the embedder, the
// reader, the sampling knobs, when it started and who ran it — and the consent
// flags, which say what may be done with the record rather than what it says.
type RunSummary struct {
	// Project is the sub-scope inside the org the run is filed under.
	Project string `json:"project"`
	// ID is the run's stable id.
	ID string `json:"id"`
	// Benchmark is the task the run measured against.
	Benchmark string `json:"benchmark"`
	// Split is which of the benchmark's splits the run covered. Empty means the
	// record does not say.
	Split string `json:"split"`
	// System is what was under test. Empty means the record does not say.
	System string `json:"system"`
	// Version is the system's version when it was measured. Empty means the
	// record does not say.
	Version string `json:"version"`
	// Baseline marks the run the others are read against.
	Baseline bool `json:"baseline"`
	// Study is the study the run belongs to, when it belongs to one.
	Study string `json:"study"`
	// Commit is the commit the harness was frozen at.
	Commit string `json:"commit"`
	// Questions is how many questions the run set out to answer. Null means the
	// record does not say.
	Questions *int `json:"questions"`
	// Answered is how many of them it answered. Null means the record does not
	// say.
	Answered *int `json:"answered"`
	// Ended is when the run stopped, RFC3339. Empty means the record does not
	// say it ever did.
	Ended string `json:"ended"`
	// Completion is derived from Questions, Answered and Ended: complete,
	// partial, inconsistent or unknown.
	Completion string `json:"completion"`
	// Measures are the run's numbers, narrowed as the query asked.
	Measures []MeasureSummary `json:"measures"`
	// Notes is anything about the execution no field above carries.
	Notes string `json:"notes"`
	// Revision is this version's declared kind: original, corrected or
	// retracted.
	Revision string `json:"revision"`
	// Canonical is true on every row a listing answers with.
	Canonical bool `json:"canonical"`
	// Visibility is private, org or public.
	Visibility string `json:"visibility"`
}

// MeasureSummary is one reported number as the summary view serves it: an
// interval or a count the harness did not state is absent rather than null.
type MeasureSummary struct {
	// Category is the slice of the run's items this number covers.
	Category string `json:"category"`
	// Metric is the quantity, and the only place Value's unit is stated.
	Metric string `json:"metric"`
	// Value is the number, in whatever unit Metric names.
	Value float64 `json:"value"`
	// Lo is the low end of the interval the harness reported. Absent when it
	// reported none.
	Lo *float64 `json:"lo,omitempty"`
	// Hi is the high end of that interval. Absent when it reported none.
	Hi *float64 `json:"hi,omitempty"`
	// N is how many items this number was computed over. Absent when the
	// harness did not say.
	N *int `json:"n,omitempty"`
	// Of is how many items the category holds. Absent when the harness did not
	// say.
	Of *int `json:"of,omitempty"`
}

// summarize is a run as the summary view serves it.
func summarize(r Run) RunSummary {
	ms := make([]MeasureSummary, len(r.Measures))
	for i, m := range r.Measures {
		ms[i] = MeasureSummary{Category: m.Category, Metric: m.Metric, Value: m.Value, Lo: m.Lo, Hi: m.Hi, N: m.N, Of: m.Of}
	}
	return RunSummary{
		Project: r.Project, ID: r.ID, Benchmark: r.Benchmark, Split: r.Split, System: r.System,
		Version: r.Version, Baseline: r.Baseline, Study: r.Study, Commit: r.Commit,
		Questions: r.Questions, Answered: r.Answered, Ended: r.Ended, Completion: r.Completion,
		Measures: ms, Notes: r.Notes, Revision: r.Revision, Canonical: r.Canonical, Visibility: r.Visibility,
	}
}
