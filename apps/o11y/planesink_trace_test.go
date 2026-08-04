// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// planesink_trace_test.go — the event.trace fold. traceRowsOf is the one place
// this binary states what a batch contributed to a trace's summary, and it is
// pure: span rows in, partial rows out, no store and no socket. The table it
// feeds is an AggregatingMergeTree, so a wrong partial is not an error anyone
// sees — it merges cleanly into a summary that is quietly false. These tests are
// therefore about the ARITHMETIC (min start, max end, summed count, per-tenant
// grouping) and about the positional contract the fold reads rows through.

package o11y

import (
	"testing"
	"time"

	zapreceiver "github.com/hanzoai/o11y/pkg/zapreceiver"
)

// traceBase is a fixed wall clock; every span time below is stated as an offset
// from it, so the assertions read as durations rather than as timestamps.
var traceBase = time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

func traceAt(d time.Duration) time.Time { return traceBase.Add(d) }

// traceSpanRow builds one row in planeSpanColumns shape with only the four
// columns the fold reads populated — the rest stay zero on purpose, so a fold
// that wandered into another column would read a zero value and fail loudly.
func traceSpanRow(org, traceID string, start time.Time, dur time.Duration) []any {
	row := make([]any, len(planeSpanColumns))
	row[planeSpanColOrg] = org
	row[planeSpanColTime] = start
	row[planeSpanColTraceID] = traceID
	row[planeSpanColDuration] = uint64(dur.Nanoseconds())
	return row
}

func traceCol(t *testing.T, row []any, name string) any {
	return row[colIndex(t, planeTraceColumns, name)]
}

// traceOf finds the partial for one (org, trace_id) pair.
func traceOf(t *testing.T, rows [][]any, org, traceID string) []any {
	t.Helper()
	for _, row := range rows {
		if traceCol(t, row, "org") == org && traceCol(t, row, "trace_id") == traceID {
			return row
		}
	}
	t.Fatalf("no partial for (%s, %s) in %v", org, traceID, rows)
	return nil
}

func wantTraceTime(t *testing.T, row []any, col string, want time.Time) {
	t.Helper()
	got, ok := traceCol(t, row, col).(time.Time)
	if !ok {
		t.Fatalf("%s is %T, want time.Time", col, traceCol(t, row, col))
	}
	if !got.Equal(want) {
		t.Errorf("%s = %v, want %v (off by %v)", col, got, want, got.Sub(want))
	}
}

func wantNumSpans(t *testing.T, row []any, want uint64) {
	t.Helper()
	got, ok := traceCol(t, row, "num_spans").(uint64)
	if !ok {
		t.Fatalf("num_spans is %T, want uint64 (the column is SimpleAggregateFunction(sum, UInt64))",
			traceCol(t, row, "num_spans"))
	}
	if got != want {
		t.Errorf("num_spans = %d, want %d", got, want)
	}
}

// TestPlaneSpanColumnIndexesArePinned is the guard the fold's positional reads
// depend on. traceRowsOf takes BUILT span rows so the summary can never drift
// from the spans it summarizes — the cost is that it addresses them by index,
// and a reordered planeSpanColumns would silently repoint every read (trace_id
// becoming span_id, duration becoming parent). This test is what makes that
// reorder a red suite instead of a corrupt summary nobody notices.
func TestPlaneSpanColumnIndexesArePinned(t *testing.T) {
	for _, c := range []struct {
		idx  int
		name string
	}{
		{planeSpanColOrg, "org"},
		{planeSpanColTime, "time"},
		{planeSpanColTraceID, "trace_id"},
		{planeSpanColDuration, "duration"},
	} {
		if c.idx >= len(planeSpanColumns) {
			t.Fatalf("index %d is past planeSpanColumns (%d cols)", c.idx, len(planeSpanColumns))
		}
		if got := planeSpanColumns[c.idx]; got != c.name {
			t.Errorf("planeSpanColumns[%d] = %q, want %q — traceRowsOf reads this position", c.idx, got, c.name)
		}
	}
}

// The row traceRowsOf appends must be exactly as wide as the column list it is
// inserted with, and it must name the five columns event.trace declares.
func TestPlaneTraceColumns_ShapeIsFixed(t *testing.T) {
	want := []string{"org", "trace_id", "start", "end", "num_spans"}
	if len(planeTraceColumns) != len(want) {
		t.Fatalf("planeTraceColumns = %v, want %v", planeTraceColumns, want)
	}
	for i, c := range want {
		if planeTraceColumns[i] != c {
			t.Errorf("planeTraceColumns[%d] = %q, want %q", i, planeTraceColumns[i], c)
		}
	}
	rows := traceRowsOf([][]any{traceSpanRow("hanzo", "t", traceBase, time.Second)})
	if len(rows) != 1 {
		t.Fatalf("got %d partials, want 1", len(rows))
	}
	if len(rows[0]) != len(planeTraceColumns) {
		t.Fatalf("partial width %d != column count %d", len(rows[0]), len(planeTraceColumns))
	}
}

// One trace's spans in one batch fold to ONE partial: min start, max end,
// summed count.
func TestTraceRowsOf_FoldsOneTrace(t *testing.T) {
	rows := traceRowsOf([][]any{
		traceSpanRow("hanzo", "t1", traceAt(10*time.Millisecond), 5*time.Millisecond),
		traceSpanRow("hanzo", "t1", traceAt(0), 30*time.Millisecond),
		traceSpanRow("hanzo", "t1", traceAt(20*time.Millisecond), 5*time.Millisecond),
	})
	if len(rows) != 1 {
		t.Fatalf("got %d partials, want 1 — three spans of one trace are one row", len(rows))
	}
	row := rows[0]
	if got := traceCol(t, row, "org"); got != "hanzo" {
		t.Errorf("org = %v, want hanzo", got)
	}
	if got := traceCol(t, row, "trace_id"); got != "t1" {
		t.Errorf("trace_id = %v, want t1", got)
	}
	wantTraceTime(t, row, "start", traceAt(0))
	wantTraceTime(t, row, "end", traceAt(30*time.Millisecond))
	wantNumSpans(t, row, 3)
}

// THE regression this file exists for. The trace's LAST-ENDING span is not its
// LAST-STARTING one: the root starts at t=0 and runs 100ms while awaiting a
// child that starts at t=90ms and runs 5ms. end must be 100ms (the root's own
// end), never 95ms (the last-starting span's end) and never 90ms (max(start) —
// the mistake that renders a waterfall ending before its root does).
func TestTraceRowsOf_EndIsMaxOfStartPlusDuration(t *testing.T) {
	rows := traceRowsOf([][]any{
		traceSpanRow("hanzo", "t1", traceAt(0), 100*time.Millisecond),                 // starts FIRST, ends LAST
		traceSpanRow("hanzo", "t1", traceAt(90*time.Millisecond), 5*time.Millisecond), // starts LAST, ends earlier
		traceSpanRow("hanzo", "t1", traceAt(10*time.Millisecond), 20*time.Millisecond),
	})
	if len(rows) != 1 {
		t.Fatalf("got %d partials, want 1", len(rows))
	}
	row := rows[0]
	wantTraceTime(t, row, "start", traceAt(0))
	wantTraceTime(t, row, "end", traceAt(100*time.Millisecond))

	end := traceCol(t, row, "end").(time.Time)
	if end.Equal(traceAt(90 * time.Millisecond)) {
		t.Errorf("end = %v — that is max(start), not max(start+duration): the trace "+
			"would render as ending before its own root span did", end)
	}
	if end.Equal(traceAt(95 * time.Millisecond)) {
		t.Errorf("end = %v — that is the LAST-STARTING span's end, not the LAST-ENDING one", end)
	}
	// And the whole point of the summary: its extent equals the root's.
	if d := end.Sub(traceCol(t, row, "start").(time.Time)); d != 100*time.Millisecond {
		t.Errorf("summarized trace duration = %v, want 100ms (the root's)", d)
	}
}

// A zero-duration span (instant, or a batch whose end<=start collapsed duration
// to 0) still ends when it starts — end must not fall behind start.
func TestTraceRowsOf_ZeroDurationEndsAtStart(t *testing.T) {
	rows := traceRowsOf([][]any{traceSpanRow("hanzo", "t1", traceAt(5*time.Second), 0)})
	if len(rows) != 1 {
		t.Fatalf("got %d partials, want 1", len(rows))
	}
	wantTraceTime(t, rows[0], "start", traceAt(5*time.Second))
	wantTraceTime(t, rows[0], "end", traceAt(5*time.Second))
}

// start is min over the batch, including when the earliest span arrives LAST in
// the row order (batches are not sorted).
func TestTraceRowsOf_StartIsMin(t *testing.T) {
	rows := traceRowsOf([][]any{
		traceSpanRow("hanzo", "t1", traceAt(50*time.Millisecond), time.Millisecond),
		traceSpanRow("hanzo", "t1", traceAt(40*time.Millisecond), time.Millisecond),
		traceSpanRow("hanzo", "t1", traceAt(1*time.Millisecond), time.Millisecond), // earliest, listed last
	})
	if len(rows) != 1 {
		t.Fatalf("got %d partials, want 1", len(rows))
	}
	wantTraceTime(t, rows[0], "start", traceAt(1*time.Millisecond))
	wantTraceTime(t, rows[0], "end", traceAt(51*time.Millisecond))
}

// A span with no trace_id has no trace to summarize. It stays in event.span (it
// is still a fact) but must never open a partial: event.trace is ORDER BY
// (org, trace_id), so an empty id is one garbage row per org that every trace
// listing then has to explain.
func TestTraceRowsOf_SkipsEmptyTraceID(t *testing.T) {
	rows := traceRowsOf([][]any{
		traceSpanRow("hanzo", "", traceAt(0), time.Second),
		traceSpanRow("hanzo", "t1", traceAt(0), time.Second),
		traceSpanRow("hanzo", "", traceAt(time.Second), time.Second),
	})
	if len(rows) != 1 {
		t.Fatalf("got %d partials, want 1 — only the span WITH a trace id counts: %v", len(rows), rows)
	}
	if got := traceCol(t, rows[0], "trace_id"); got != "t1" {
		t.Errorf("trace_id = %v, want t1", got)
	}
	wantNumSpans(t, rows[0], 1)

	if rows := traceRowsOf([][]any{traceSpanRow("hanzo", "", traceAt(0), time.Second)}); rows != nil {
		t.Errorf("a batch of only untraced spans -> %v, want nil (no INSERT at all)", rows)
	}
}

// Two tenants in one batch stay two summaries even under the SAME trace id: org
// is sort-key position 1 in event.trace, and folding across it would publish one
// tenant's spans inside another's trace.
func TestTraceRowsOf_OrgsStaySeparate(t *testing.T) {
	rows := traceRowsOf([][]any{
		traceSpanRow("acme", "shared", traceAt(0), 10*time.Millisecond),
		traceSpanRow("hanzo", "shared", traceAt(5*time.Millisecond), 10*time.Millisecond),
		traceSpanRow("acme", "shared", traceAt(1*time.Millisecond), time.Millisecond),
		traceSpanRow("acme", "other", traceAt(0), time.Millisecond),
	})
	if len(rows) != 3 {
		t.Fatalf("got %d partials, want 3 — (acme,shared) (hanzo,shared) (acme,other): %v", len(rows), rows)
	}
	acme := traceOf(t, rows, "acme", "shared")
	wantNumSpans(t, acme, 2)
	wantTraceTime(t, acme, "start", traceAt(0))
	wantTraceTime(t, acme, "end", traceAt(10*time.Millisecond))

	hanzo := traceOf(t, rows, "hanzo", "shared")
	wantNumSpans(t, hanzo, 1)
	wantTraceTime(t, hanzo, "start", traceAt(5*time.Millisecond))
	wantTraceTime(t, hanzo, "end", traceAt(15*time.Millisecond))

	wantNumSpans(t, traceOf(t, rows, "acme", "other"), 1)
}

func TestTraceRowsOf_EmptyAndNil(t *testing.T) {
	if rows := traceRowsOf(nil); rows != nil {
		t.Errorf("nil rows -> %v, want nil", rows)
	}
	if rows := traceRowsOf([][]any{}); rows != nil {
		t.Errorf("no rows -> %v, want nil", rows)
	}
	// A malformed row (shorter than the column list, or the wrong type in a read
	// position) is skipped, not panicked on: this runs inside the wire receiver's
	// handler, where a panic takes ingest down for every sender.
	short := []any{"hanzo", traceAt(0)}
	wrongType := make([]any, len(planeSpanColumns))
	wrongType[planeSpanColTraceID] = "t1"
	wrongType[planeSpanColTime] = int64(0) // not a time.Time
	if rows := traceRowsOf([][]any{short, wrongType}); rows != nil {
		t.Errorf("malformed rows -> %v, want nil", rows)
	}
}

// End to end on the REAL builder: the fold consumes exactly what spanRowsOf
// produces, so the wire path's own rows summarize correctly without any
// re-derivation from the batch. This is the coupling that makes the partial
// impossible to drift from the spans.
func TestTraceRowsOf_FoldsRealSpanRows(t *testing.T) {
	const startNs = int64(1_700_000_000_000_000_000)
	ms := int64(time.Millisecond)
	b := &zapreceiver.SpanBatch{
		AppName:  "cloud",
		Resource: map[string]string{"service.name": "cloudsvc"},
		Spans: []zapreceiver.Span{
			// root: starts first, ends last (100ms)
			{TraceID: "tr", SpanID: "root", Name: "GET /v1/x", Kind: "server",
				StartUnixNs: startNs, EndUnixNs: startNs + 100*ms},
			// child: starts last, ends before the root does
			{TraceID: "tr", SpanID: "child", Name: "db", Kind: "client",
				StartUnixNs: startNs + 90*ms, EndUnixNs: startNs + 95*ms},
			// a second tenant's span, same batch
			{TraceID: "tr2", SpanID: "s3", Name: "job",
				StartUnixNs: startNs + 10*ms, EndUnixNs: startNs + 20*ms,
				Attributes: map[string]any{"hanzo.org": "acme"}},
		},
	}
	spans := spanRowsOf(b)
	if len(spans) != 3 {
		t.Fatalf("spanRowsOf -> %d rows, want 3", len(spans))
	}
	rows := traceRowsOf(spans)
	if len(rows) != 2 {
		t.Fatalf("traceRowsOf -> %d partials, want 2", len(rows))
	}
	tr := traceOf(t, rows, platformOrg, "tr")
	wantNumSpans(t, tr, 2)
	wantTraceTime(t, tr, "start", time.Unix(0, startNs).UTC())
	wantTraceTime(t, tr, "end", time.Unix(0, startNs+100*ms).UTC())

	tr2 := traceOf(t, rows, "acme", "tr2")
	wantNumSpans(t, tr2, 1)
	wantTraceTime(t, tr2, "start", time.Unix(0, startNs+10*ms).UTC())
	wantTraceTime(t, tr2, "end", time.Unix(0, startNs+20*ms).UTC())
}

// The trace writer states its database like the other two: the DSN this package
// connects with names none, so an unqualified `trace` would address `default`.
// And it binds no ingested_at — event.trace declares no such column, and the
// four columns it does declare are all derived from the spans just written.
func TestPlaneTraceWriter_QualifiedAndNoIngestedAt(t *testing.T) {
	if planeTraceTable != "event.trace" {
		t.Errorf("planeTraceTable = %q, want event.trace", planeTraceTable)
	}
	for _, c := range planeTraceColumns {
		if c == "ingested_at" {
			t.Errorf("trace writer binds %q — event.trace declares no such column", c)
		}
	}
}
