// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// planesink_resource_test.go — the two DIMENSIONS a span implies, and the join
// key that reaches one of them.
//
// The span lane wrote facts and nothing else: 10,307 rows on the plane, every
// one with resource_fingerprint = '', and zero rows in event.span_resource and
// event.operation. Neither absence is an error anyone sees. The read plane
// compiles a resource filter into a CTE over event.span_resource and leaves the
// span query with `resource_fingerprint GLOBAL IN (…)`, and it resolves an
// operation filter against event.operation — so with those two empty, every
// service view, the Service Map and every operation-filtered trace query answer
// empty over a complete span table, which reads as "this service ships no
// telemetry".
//
// These are pure: rows in, dimension rows out, no store and no socket.

package o11y

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	zapreceiver "github.com/hanzoai/o11y/pkg/zapreceiver"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func spanResourceCol(t *testing.T, row []any, name string) any {
	return row[colIndex(t, planeSpanResourceColumns, name)]
}

func operationCol(t *testing.T, row []any, name string) any {
	return row[colIndex(t, planeOperationColumns, name)]
}

// wireBatch is one sender's batch: one resource, spans at fixed offsets.
func wireBatch(service string, spans ...zapreceiver.Span) *zapreceiver.SpanBatch {
	return &zapreceiver.SpanBatch{
		AppName:  service,
		Resource: map[string]string{"service.name": service, "deployment.environment": "prod"},
		Spans:    spans,
	}
}

func wireSpan(name string, at time.Time, attrs map[string]any) zapreceiver.Span {
	return zapreceiver.Span{
		TraceID: "trace-1", SpanID: "span-" + name, Name: name, Kind: "server",
		StartUnixNs: at.UnixNano(), EndUnixNs: at.Add(time.Millisecond).UnixNano(),
		Attributes: attrs,
	}
}

// EVERY SPAN CARRIES THE KEY THAT REACHES ITS RESOURCE. A row with an empty
// fingerprint is selected by no resource filter at all, which is every filter a
// service view can express.
func TestASpanRowCarriesItsResourceFingerprint(t *testing.T) {
	rows, labels := spanRowsOf(wireBatch("evalsvc", wireSpan("GET /v1/eval", traceBase, nil)))
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	fingerprint, _ := spanCol(t, rows[0], "resource_fingerprint").(string)
	if fingerprint == "" {
		t.Fatal("resource_fingerprint is empty — no resource filter can select this row")
	}
	if labels[fingerprint] == "" {
		t.Fatalf("the batch stated no labels for %s — the identity row would name nothing", fingerprint)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(labels[fingerprint]), &got); err != nil {
		t.Fatalf("labels are not the canonical resource JSON: %v", err)
	}
	if got["service.name"] != "evalsvc" {
		t.Errorf("labels service.name = %q, want evalsvc — the reader matches on this, "+
			"while `service` on the row is only what it displays", got["service.name"])
	}
}

// ONE RESOURCE IS ONE IDENTITY, whichever path carried it. The three producers
// — the ZAP wire, the in-process SDK sink and the product wire's gen_ai spans —
// resolve it through the same function, so a service that sends on two of them
// is one row in the identity table rather than two a filter picks between.
func TestOneResourceIsOneIdentityWhicheverPathCarriedIt(t *testing.T) {
	bare := &zapreceiver.SpanBatch{
		Resource: map[string]string{"service.name": "evalsvc"},
		Spans:    []zapreceiver.Span{wireSpan("GET /v1/eval", traceBase, nil)},
	}
	rows, _ := spanRowsOf(bare)
	wire, _ := spanCol(t, rows[0], "resource_fingerprint").(string)

	// What the product wire's gen_ai lens states for the same service.
	direct, _ := planeResource(nil, "evalsvc")
	if wire != direct {
		t.Errorf("one service fingerprinted two ways: %q on the span wire, %q on the product wire", wire, direct)
	}

	// And a different service is a different identity — otherwise a filter for one
	// selects the other's rows.
	other, _ := planeResource(nil, "other")
	if other == direct {
		t.Error("two services share one identity")
	}
}

// THE IDENTITY ROW IS STATED IN THE WINDOW ITS SPANS FALL IN, not in the window
// the process happened to be writing. The reader bounds its CTE by
// seen_at_ts_bucket_start, so an identity stated in the wrong window is one it
// does not select.
func TestTheResourceIsStatedInTheWindowItsSpansFallIn(t *testing.T) {
	late := traceBase.Add(3 * time.Hour)
	rows, labels := spanRowsOf(wireBatch("evalsvc",
		wireSpan("a", traceBase, nil),
		wireSpan("b", late, nil),
	))
	identities := spanResourceRowsOf(rows, labels)
	if len(identities) != 2 {
		t.Fatalf("got %d identity rows, want 2 — one per window the spans fall in: %v", len(identities), identities)
	}
	buckets := map[int64]bool{}
	for _, row := range identities {
		b, ok := spanResourceCol(t, row, "seen_at_ts_bucket_start").(int64)
		if !ok {
			t.Fatalf("bucket is %T, want int64", spanResourceCol(t, row, "seen_at_ts_bucket_start"))
		}
		if b%resourceBucket != 0 {
			t.Errorf("bucket %d is not on the %ds grid the table is partitioned on", b, resourceBucket)
		}
		buckets[b] = true
	}
	if !buckets[traceBase.Unix()/resourceBucket*resourceBucket] || !buckets[late.Unix()/resourceBucket*resourceBucket] {
		t.Errorf("buckets %v do not cover both spans' windows", buckets)
	}
}

// ONE ROW PER TENANT. org is a per-ROW fact here — each span carries its own
// hanzo.org — and the identity table is keyed (org, bucket, fingerprint), so an
// identity written under one tenant does not resolve for another.
func TestEachTenantInABatchGetsItsOwnIdentity(t *testing.T) {
	rows, labels := spanRowsOf(wireBatch("evalsvc",
		wireSpan("a", traceBase, map[string]any{"hanzo.org": "acme"}),
		wireSpan("b", traceBase, map[string]any{"hanzo.org": "globex"}),
		wireSpan("c", traceBase, nil), // no tenant of its own: the platform's
	))
	identities := spanResourceRowsOf(rows, labels)
	orgs := map[string]bool{}
	for _, row := range identities {
		orgs[spanResourceCol(t, row, "org").(string)] = true
	}
	for _, want := range []string{"acme", "globex", platformOrg} {
		if !orgs[want] {
			t.Errorf("no identity row for tenant %q — its spans are unreachable by a resource filter", want)
		}
	}
}

// A row whose fingerprint names nothing is not stated. An identity row with
// empty labels satisfies no filter and would sit in the table looking answered.
func TestAnIdentityWithNoLabelsIsNotStated(t *testing.T) {
	rows, _ := spanRowsOf(wireBatch("evalsvc", wireSpan("a", traceBase, nil)))
	if got := spanResourceRowsOf(rows, nil); len(got) != 0 {
		t.Errorf("stated %v with no labels to state", got)
	}
}

// THE OPERATION INVENTORY. `(name, service) GLOBAL IN (SELECT DISTINCT name,
// serviceName FROM event.operation …)` matches nothing against an empty table,
// so every operation-filtered query answers empty over a complete span table.
func TestOperationRowsAreThePairsTheSpansName(t *testing.T) {
	rows, _ := spanRowsOf(wireBatch("evalsvc",
		wireSpan("GET /v1/eval", traceBase, nil),
		wireSpan("GET /v1/eval", traceBase.Add(time.Minute), nil),
		wireSpan("POST /v1/eval", traceBase, nil),
	))
	ops := operationRowsOf(rows)
	if len(ops) != 2 {
		t.Fatalf("got %d operations, want 2 — one per (service, name) pair: %v", len(ops), ops)
	}
	for _, row := range ops {
		if got := operationCol(t, row, "serviceName"); got != "evalsvc" {
			t.Errorf("serviceName = %v, want evalsvc", got)
		}
		if operationCol(t, row, "name") == "GET /v1/eval" {
			// LATEST, not first: the column is the table's version and the reader
			// bounds it by time, so the pair means "still in use as of".
			if got := operationCol(t, row, "time"); got != traceBase.Add(time.Minute) {
				t.Errorf("time = %v, want the latest start %v", got, traceBase.Add(time.Minute))
			}
		}
	}
}

// An operation with no name autocompletes nothing and matches nothing.
func TestAnUnnamedSpanStatesNoOperation(t *testing.T) {
	row := make([]any, len(planeSpanColumns))
	row[planeSpanColOrg] = "hanzo"
	row[planeSpanColTime] = traceBase
	row[planeSpanColService] = "evalsvc"
	if got := operationRowsOf([][]any{row}); len(got) != 0 {
		t.Errorf("stated %v for a span with no name", got)
	}
}

// The in-process sink owes the same two dimensions as the wire: a span made in
// this binary is as unreachable as one that arrived over a socket.
func TestTheInProcessSinkCarriesTheSameKey(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(sr),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", "evalsvc"))),
	)
	_, span := tp.Tracer("planesink-test").Start(context.Background(), "agent.run")
	span.End()

	rows, labels := sdkSpanRowsOf(sr.Ended())
	if len(rows) == 0 {
		t.Fatal("no rows from the in-process sink")
	}
	fingerprint, _ := spanCol(t, rows[0], "resource_fingerprint").(string)
	if fingerprint == "" || labels[fingerprint] == "" {
		t.Fatalf("in-process span carries fingerprint %q with labels %q", fingerprint, labels[fingerprint])
	}
	// The fingerprint is the RESOURCE's, so a span's own attributes must not enter
	// it — otherwise every span is its own resource and the identity table holds
	// one row per span, which is a resource filter that selects one row.
	var got map[string]string
	if err := json.Unmarshal([]byte(labels[fingerprint]), &got); err != nil {
		t.Fatalf("labels are not the canonical resource JSON: %v", err)
	}
	if len(got) != 1 || got["service.name"] != "evalsvc" {
		t.Errorf("labels = %v, want the resource alone", got)
	}
	if len(spanResourceRowsOf(rows, labels)) == 0 {
		t.Error("the in-process sink states no resource identity")
	}
	if len(operationRowsOf(rows)) == 0 {
		t.Error("the in-process sink states no operation")
	}
}

// AN IDENTITY IS THE COLUMNS THE READER MATCHES ON, and nothing else. Keyed on
// the whole row, an operation would be re-written every batch by its time column
// — one INSERT per batch into a table read by (service, name), which is the write
// amplification that has taken this store down before.
func TestAnIdentityIsWhatTheReaderMatchesOn(t *testing.T) {
	rows, labels := spanRowsOf(wireBatch("evalsvc", wireSpan("a", traceBase, nil)))
	again, againLabels := spanRowsOf(wireBatch("evalsvc", wireSpan("a", traceBase.Add(time.Second), nil)))

	first := spanResourceRowsOf(rows, labels)[0]
	second := spanResourceRowsOf(again, againLabels)[0]
	if resourceIdentity(first) != resourceIdentity(second) {
		t.Errorf("one resource in one window read as two identities: %q and %q",
			resourceIdentity(first), resourceIdentity(second))
	}

	op := operationRowsOf(rows)[0]
	opAgain := operationRowsOf(again)[0]
	if operationIdentity(op) != operationIdentity(opAgain) {
		t.Errorf("one operation in one window read as two identities: %q and %q",
			operationIdentity(op), operationIdentity(opAgain))
	}

	// A LATER WINDOW IS A NEW SIGHTING, though: the reader bounds both tables by
	// time, so a pair stated once at boot would fall out of every picker.
	later := operationRowsOf(func() [][]any {
		r, _ := spanRowsOf(wireBatch("evalsvc", wireSpan("a", traceBase.Add(time.Hour), nil)))
		return r
	}())[0]
	if operationIdentity(op) == operationIdentity(later) {
		t.Error("an operation seen an hour later is the same sighting — it would never be re-stated")
	}
}

// THE SINK OWES THE WHOLE ROW SET, NOT JUST THE FACTS — and it owes the
// identities FIRST.
//
// A span whose fingerprint names no identity row is unreachable by every filter
// a console panel can express, so a crash between the two writes must leave a
// reader with less to see, never with rows it cannot reach. This drives the real
// insertSpans over a recording statement and reads back what it wrote, in order.
func TestInsertSpansStatesTheIdentitiesBeforeTheFacts(t *testing.T) {
	type write struct {
		table string
		rows  [][]any
	}
	var wrote []write
	ps := &planeSink{}
	ps.insert = func(_ context.Context, table string, _ []string, rows [][]any) error {
		wrote = append(wrote, write{table, rows})
		return nil
	}
	ps.spans = newRowBuffer(planeSpanTable, func(ctx context.Context, rows [][]any) error {
		return ps.writeSpans(ctx, receiverLog(), rows)
	}, time.Hour, 1, receiverLog()) // flush on the first row, never on the clock
	t.Cleanup(ps.spans.Close)

	rows, labels := spanRowsOf(wireBatch("evalsvc", wireSpan("GET /v1/eval", traceBase, nil)))
	if err := ps.insertSpans(context.Background(), receiverLog(), rows, labels); err != nil {
		t.Fatalf("insertSpans: %v", err)
	}

	order := make([]string, 0, len(wrote))
	for _, w := range wrote {
		order = append(order, w.table)
	}
	want := []string{planeSpanResourceTable, planeOperationTable, planeSpanTable, planeTraceTable}
	for _, table := range want {
		found := false
		for _, got := range order {
			if got == table {
				found = true
			}
		}
		if !found {
			t.Errorf("%s was never written — the sink wrote %v", table, order)
		}
	}
	if len(order) >= 3 && (order[0] != planeSpanResourceTable || order[1] != planeOperationTable) {
		t.Errorf("wrote %v — the identities a row points at must be stated before the row", order)
	}
}
