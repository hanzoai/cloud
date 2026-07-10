// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package o11y

import (
	"context"
	"errors"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/luxfi/zap"
)

// fakeWire is a stand-in otlptrace.Client for the wire fallback, so routing is
// exercised without a socket.
type fakeWire struct{ calls int }

func (f *fakeWire) Start(context.Context) error { return nil }
func (f *fakeWire) Stop(context.Context) error  { return nil }
func (f *fakeWire) UploadTraces(context.Context, []*tracepb.ResourceSpans) error {
	f.calls++
	return nil
}

// countSpans totals the spans across a proto batch.
func countSpans(batch []*tracepb.ResourceSpans) int {
	n := 0
	for _, rs := range batch {
		for _, ss := range rs.GetScopeSpans() {
			n += len(ss.GetSpans())
		}
	}
	return n
}

// The core dogfood contract: a span produced by cloud's OWN tracer provider —
// installed on the exporter NewTraceExporter builds — reaches the in-process sink
// handler as the LIVE proto batch, with NO wire client configured and NO socket
// opened. The only path from producer to handler is Router -> InProcessInterface.
func TestTraceExporter_InProcessDeliversLiveSpans_NoSocket(t *testing.T) {
	var gotName string
	var gotCount int
	traceInproc.Register(TraceDest, func(_ context.Context, _ zap.Destination, p zap.Payload) (zap.Payload, error) {
		batch, ok := p.Value.([]*tracepb.ResourceSpans)
		if !ok {
			t.Errorf("handler payload = %T, want []*tracepb.ResourceSpans", p.Value)
			return zap.Payload{}, nil
		}
		gotCount = countSpans(batch)
		if gotCount > 0 {
			gotName = batch[0].GetScopeSpans()[0].GetSpans()[0].GetName()
		}
		return zap.Payload{}, nil
	})
	defer traceInproc.Register(TraceDest, nil)

	ctx := context.Background()
	// wire=nil ⇒ in-process only. If the span did NOT route in-process, the export
	// would ErrNoRoute (no socket, no wire) and gotCount would stay 0.
	exp, err := NewTraceExporter(ctx, nil)
	if err != nil {
		t.Fatalf("NewTraceExporter: %v", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSyncer(exp), // synchronous: End -> export -> UploadTraces -> handler
	)
	defer func() { _ = tp.Shutdown(ctx) }()

	_, span := tp.Tracer("cloud").Start(ctx, "cloud-own-span")
	span.End()

	if gotCount != 1 {
		t.Fatalf("in-process handler received %d spans, want 1 (span did not route in-process)", gotCount)
	}
	if gotName != "cloud-own-span" {
		t.Fatalf("in-process handler span name = %q, want cloud-own-span", gotName)
	}
}

// protoToTraces bridges SDK-exporter proto spans to collector pdata via one
// in-memory OTLP round-trip; the span identity/name/count must survive it, since
// that pdata is what the chtraces exporter writes to o11y_traces.
func TestProtoToTraces_RoundTrip(t *testing.T) {
	traceID := make([]byte, 16)
	spanID := make([]byte, 8)
	traceID[0], spanID[0] = 0xAB, 0xCD
	batch := []*tracepb.ResourceSpans{{
		ScopeSpans: []*tracepb.ScopeSpans{{
			Spans: []*tracepb.Span{{Name: "s1", TraceId: traceID, SpanId: spanID}},
		}},
	}}

	td, err := protoToTraces(batch)
	if err != nil {
		t.Fatalf("protoToTraces: %v", err)
	}
	if td.SpanCount() != 1 {
		t.Fatalf("pdata SpanCount = %d, want 1", td.SpanCount())
	}
	if got := td.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Name(); got != "s1" {
		t.Fatalf("pdata span name = %q, want s1", got)
	}
}

// With the in-process sink present, the router prefers it (Cost 0) and the wire
// fallback is NEVER touched.
func TestRouterTraceClient_PrefersInProcess(t *testing.T) {
	called := false
	traceInproc.Register(TraceDest, func(context.Context, zap.Destination, zap.Payload) (zap.Payload, error) {
		called = true
		return zap.Payload{}, nil
	})
	defer traceInproc.Register(TraceDest, nil)

	wire := &fakeWire{}
	c := &routerTraceClient{router: Router, dest: TraceDest, wire: wire}
	if err := c.UploadTraces(context.Background(), []*tracepb.ResourceSpans{{}}); err != nil {
		t.Fatalf("UploadTraces: %v", err)
	}
	if !called {
		t.Fatal("in-process handler not called")
	}
	if wire.calls != 0 {
		t.Fatalf("wire fallback used though in-process available: calls=%d", wire.calls)
	}
}

// With NO in-process sink, the router returns ErrNoRoute and the batch rides the
// wire fallback (standalone aid / embed-off posture).
func TestRouterTraceClient_WireFallbackOnNoRoute(t *testing.T) {
	traceInproc.Register(TraceDest, nil) // ensure no handler
	wire := &fakeWire{}
	c := &routerTraceClient{router: Router, dest: TraceDest, wire: wire}
	if err := c.UploadTraces(context.Background(), []*tracepb.ResourceSpans{{}}); err != nil {
		t.Fatalf("UploadTraces: %v", err)
	}
	if wire.calls != 1 {
		t.Fatalf("wire fallback not used: calls=%d", wire.calls)
	}
}

// With NO in-process sink AND NO wire fallback, ErrNoRoute surfaces — a routing
// gap is visible, never a silent plaintext downgrade or silent drop.
func TestRouterTraceClient_NoRouteNoWire_Errors(t *testing.T) {
	traceInproc.Register(TraceDest, nil)
	c := &routerTraceClient{router: Router, dest: TraceDest}
	if err := c.UploadTraces(context.Background(), nil); !errors.Is(err, zap.ErrNoRoute) {
		t.Fatalf("want ErrNoRoute, got %v", err)
	}
}

// TraceInprocEnabled is the ONE gate both the sink and the producer read.
func TestTraceInprocEnabled_Gate(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "yes", "on"} {
		t.Setenv("O11Y_TRACES_ZAP_INPROCESS", v)
		if !TraceInprocEnabled() {
			t.Fatalf("TraceInprocEnabled()=false for %q, want true", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "off", "garbage"} {
		t.Setenv("O11Y_TRACES_ZAP_INPROCESS", v)
		if TraceInprocEnabled() {
			t.Fatalf("TraceInprocEnabled()=true for %q, want false", v)
		}
	}
}
