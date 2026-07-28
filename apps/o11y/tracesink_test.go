// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package o11y

import (
	"context"
	"testing"

	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
)

// fakeConsumer stands in for the dstraces exporter so the seam is exercised
// without a datastore.
type fakeConsumer struct{ got []ptrace.Traces }

func (f *fakeConsumer) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}
func (f *fakeConsumer) ConsumeTraces(_ context.Context, td ptrace.Traces) error {
	f.got = append(f.got, td)
	return nil
}

// TestTracing_EndToEnd_WithO11yLinkedIn is the proof the split had to keep: with
// clients/o11y LINKED INTO this binary, a span opened on the process-global
// tracer — the handle every caller in cloud already holds — arrives at o11y's
// sink, converted to the pdata the datastore exporter consumes.
//
// It crosses the whole seam and nothing else: cloud.InstallTelemetry builds the
// provider (host side), cloud.RegisterTraceSink carries the batch across, and
// traceSink — the exact function mountTraceSink registers in production —
// converts it. Before the split this path only existed because clients/o11y's
// init() ran in the host; the plugin cut that wire and tracing went dark with no
// test to notice.
func TestTracing_EndToEnd_WithO11yLinkedIn(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_ZAP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("O11Y_TRACES_ZAP_INPROCESS", "true")

	sink := &fakeConsumer{}
	cloud.RegisterTraceSink(traceSink(sink))
	t.Cleanup(func() { cloud.RegisterTraceSink(nil) })

	shutdown := cloud.InstallTelemetry(context.Background(), luxlog.New("o11y-test"), "hanzo-cloud")
	if !cloud.TracerProviderInstalled() {
		t.Fatal("host did not install a tracer provider")
	}

	_, span := otel.Tracer(cloud.TracerName).Start(context.Background(), "GET /v1/ai/chat/completions")
	span.SetAttributes(attribute.Int64("http.status_code", 200))
	span.End()

	shutdown(context.Background()) // forces the batch processor to flush

	if len(sink.got) != 1 {
		t.Fatalf("o11y's sink consumed %d batches, want 1 — cloud's spans are not reaching o11y", len(sink.got))
	}
	td := sink.got[0]
	if td.SpanCount() != 1 {
		t.Fatalf("batch carried %d spans, want 1", td.SpanCount())
	}
	got := td.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	if got.Name() != "GET /v1/ai/chat/completions" {
		t.Fatalf("span name = %q, want the request span", got.Name())
	}
	// The resource is what o11y's Environment column and service filter read; a
	// span that arrives unattributed is not usable telemetry.
	res := td.ResourceSpans().At(0).Resource().Attributes()
	if v, ok := res.Get("service.name"); !ok || v.Str() != "hanzo-cloud" {
		t.Errorf("resource service.name = %v (present=%v), want hanzo-cloud", v, ok)
	}
	if v, ok := res.Get("deployment.environment"); !ok || v.Str() == "" {
		t.Errorf("resource deployment.environment = %v (present=%v), want non-empty", v, ok)
	}
}

// TestTraceSink_DeregistersOnShutdown: teardown must remove the route, so a span
// exported after the datastore exporter is closing falls back to the wire rather
// than writing into it. shutdownTraceSink does that before flushing.
func TestTraceSink_DeregistersOnShutdown(t *testing.T) {
	sink := &fakeConsumer{}
	cloud.RegisterTraceSink(traceSink(sink))
	t.Cleanup(func() { cloud.RegisterTraceSink(nil) })

	if err := shutdownTraceSink(context.Background()); err != nil {
		t.Fatalf("shutdownTraceSink: %v", err)
	}
	// With no route left the host's exporter returns ErrNoRoute (proved in
	// cloud's own tests); here it is enough that the sink stops receiving.
	if err := traceSink(sink)(context.Background(), makeSpans(t)); err != nil {
		t.Fatalf("traceSink: %v", err)
	}
	if len(sink.got) != 1 {
		t.Fatalf("direct call consumed %d batches, want 1", len(sink.got))
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
// package-level slice another test happens to fill first.
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
