// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package o11y

import (
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"context"
	"errors"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/luxfi/zap"
)

// fakeWire is a stand-in SpanExporter for the wire fallback, so routing is
// exercised without a socket.
type fakeWire struct{ calls int }

func (f *fakeWire) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	f.calls++
	return nil
}
func (f *fakeWire) Shutdown(context.Context) error { return nil }


// The core dogfood contract: a span produced by cloud's OWN tracer provider —
// installed on the exporter NewTraceExporter builds — reaches the in-process sink
// handler as the LIVE proto batch, with NO wire client configured and NO socket
// opened. The only path from producer to handler is Router -> InProcessInterface.
func TestTraceExporter_InProcessDeliversLiveSpans_NoSocket(t *testing.T) {
	var gotName string
	var gotCount int
	traceInproc.Register(TraceDest, func(_ context.Context, _ zap.Destination, p zap.Payload) (zap.Payload, error) {
		batch, ok := p.Value.(ptrace.Traces)
		if !ok {
			t.Errorf("handler payload = %T, want ptrace.Traces", p.Value)
			return zap.Payload{}, nil
		}
		gotCount = batch.SpanCount()
		if gotCount > 0 {
			gotName = batch.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Name()
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
	c := &routerTraceExporter{router: Router, dest: TraceDest, wire: wire}
	if err := c.ExportSpans(context.Background(), makeSpans(t)); err != nil {
		t.Fatalf("ExportSpans: %v", err)
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
	c := &routerTraceExporter{router: Router, dest: TraceDest, wire: wire}
	if err := c.ExportSpans(context.Background(), makeSpans(t)); err != nil {
		t.Fatalf("ExportSpans: %v", err)
	}
	if wire.calls != 1 {
		t.Fatalf("wire fallback not used: calls=%d", wire.calls)
	}
}

// With NO in-process sink AND NO wire fallback, ErrNoRoute surfaces — a routing
// gap is visible, never a silent plaintext downgrade or silent drop.
func TestRouterTraceClient_NoRouteNoWire_Errors(t *testing.T) {
	traceInproc.Register(TraceDest, nil)
	c := &routerTraceExporter{router: Router, dest: TraceDest}
	// Real spans: an empty batch short-circuits before routing, by design — the
	// SDK may call with none and that is not a routing failure.
	if err := c.ExportSpans(context.Background(), makeSpans(t)); !errors.Is(err, zap.ErrNoRoute) {
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

// spansToTraces replaced a proto marshal/unmarshal round-trip. What matters is
// that the pdata reaching dstraces still carries identity, timing and grouping —
// a converter that silently drops a parent link or flattens scopes would look
// like it worked.
func TestSpansToTraces_PreservesIdentityAndGrouping(t *testing.T) {
	td := spansToTraces(makeSpans(t))
	if got := td.ResourceSpans().Len(); got != 1 {
		t.Fatalf("ResourceSpans = %d, want 1 (both spans share a resource)", got)
	}
	rs := td.ResourceSpans().At(0)
	if got := rs.ScopeSpans().Len(); got != 1 {
		t.Fatalf("ScopeSpans = %d, want 1 (both spans share a scope)", got)
	}
	if got := rs.ScopeSpans().At(0).Spans().Len(); got != 2 {
		t.Fatalf("Spans = %d, want 2", got)
	}
	sn, _ := rs.Resource().Attributes().Get("service.name")
	if sn.Str() != "hanzo-cloud" {
		t.Fatalf("service.name = %q, want hanzo-cloud", sn.Str())
	}

	var sawParentLink bool
	for i := 0; i < rs.ScopeSpans().At(0).Spans().Len(); i++ {
		s := rs.ScopeSpans().At(0).Spans().At(i)
		if s.Name() == "inner" {
			if s.ParentSpanID().IsEmpty() {
				t.Fatal("inner span lost its parent")
			}
			sawParentLink = true
			v, ok := s.Attributes().Get("status")
			if !ok || v.Int() != 502 {
				t.Fatalf("status attribute = %v (present=%v), want 502", v, ok)
			}
		}
		if s.StartTimestamp() == 0 || s.EndTimestamp() == 0 {
			t.Fatalf("span %q lost its timestamps", s.Name())
		}
	}
	if !sawParentLink {
		t.Fatal("never saw the inner span")
	}
}



// makeSpans produces finished spans on demand. Tests must not depend on a
// package-level slice another test happens to fill first — ExportSpans returns
// early on an empty batch, so that ordering bug reads as "routing is broken".
func makeSpans(t *testing.T) []sdktrace.ReadOnlySpan {
	t.Helper()
	var out []sdktrace.ReadOnlySpan
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", "hanzo-cloud"))),
		sdktrace.WithSyncer(sinkFunc(func(s []sdktrace.ReadOnlySpan) { out = append(out, s...) })),
	)
	tr := tp.Tracer("scope-a")
	ctx, parent := tr.Start(context.Background(), "outer")
	_, child := tr.Start(ctx, "inner")
	child.SetAttributes(attribute.Int64("status", 502))
	child.End()
	parent.End()
	return out
}

type sinkFunc func([]sdktrace.ReadOnlySpan)

func (f sinkFunc) ExportSpans(_ context.Context, s []sdktrace.ReadOnlySpan) error {
	f(s)
	return nil
}
func (sinkFunc) Shutdown(context.Context) error { return nil }
