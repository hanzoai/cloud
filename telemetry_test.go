// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package cloud

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/environ"
	luxlog "github.com/luxfi/log"
	"github.com/luxfi/zap"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func testLogger() luxlog.Logger { return luxlog.New("cloud-test") }

// captureSink records the span names handed to a co-resident sink.
type captureSink struct {
	mu    sync.Mutex
	names []string
}

func (c *captureSink) sink(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range spans {
		c.names = append(c.names, s.Name())
	}
	return nil
}

func (c *captureSink) got() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.names...)
}

// TestInstallTelemetry_SpanReachesCoResidentSink is THE contract: the host owns
// the tracer provider, so a span opened through the process-global tracer — the
// same handle middleware_tracing.go captures at package init — is delivered to a
// sink registered by a co-resident o11y. No socket, no wire.
//
// This is what broke when o11y became a plugin: the provider was BUILT inside
// clients/o11y and registered from its init(), so with clients/o11y unlinked the
// host installed nothing and every span went to the global no-op. Note the
// package under test is `cloud`, which does not import clients/o11y at all —
// precisely the deployment this has to survive.
func TestInstallTelemetry_SpanReachesCoResidentSink(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_ZAP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("O11Y_TRACES_ZAP_INPROCESS", "true") // a co-resident sink is expected

	sink := &captureSink{}
	RegisterTraceSink(sink.sink)
	t.Cleanup(func() { RegisterTraceSink(nil) })

	shutdown := InstallTelemetry(context.Background(), testLogger(), "hanzo-cloud")
	if shutdown == nil {
		t.Fatal("InstallTelemetry returned a nil shutdown func")
	}
	if !TracerProviderInstalled() {
		t.Fatal("InstallTelemetry did not latch TracerProviderInstalled — apps/ would skip the ai adoption and gen_ai spans would stay gated off")
	}

	// otel.Tracer, not tp.Tracer: this is the handle every caller in the process
	// already holds (httpTracer is captured at init). If the provider were not
	// installed GLOBALLY, this span would go to the no-op and never arrive.
	_, span := otel.Tracer(TracerName).Start(context.Background(), "cloud-own-span")
	span.End()

	// The batch span processor exports in the background; Shutdown forces the
	// flush — which is also exactly what Serve does on SIGTERM.
	shutdown(context.Background())

	got := sink.got()
	if len(got) != 1 || got[0] != "cloud-own-span" {
		t.Fatalf("co-resident sink received %v, want [cloud-own-span] — the host's spans did not reach o11y", got)
	}
	if TracerProviderInstalled() {
		t.Error("shutdown left TracerProviderInstalled latched")
	}
}

// TestTraceExport_FallsBackToWireWithNoSink is the o11y-as-a-plugin leg of the
// SAME code path: with no sink registered in this process the router cannot reach
// the destination, and the identical Send falls through to the wire. The producer
// never branches on where o11y lives.
func TestTraceExport_FallsBackToWireWithNoSink(t *testing.T) {
	RegisterTraceSink(nil) // o11y is a plugin: its handler lives in another process

	wire := &fakeWire{}
	exp := &routerTraceExporter{router: traceRouter, dest: traceDest, wire: wire}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSyncer(exp),
	)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	_, span := tp.Tracer("cloud").Start(context.Background(), "plugin-hop")
	span.End()

	if wire.calls() != 1 {
		t.Fatalf("wire exporter received %d batches, want 1 — spans are dropped when o11y is a plugin", wire.calls())
	}
}

// TestTraceExport_PrefersInProcessOverWire pins the Cost table: with a co-resident
// sink the wire is never touched, so the fused binary pays no socket.
func TestTraceExport_PrefersInProcessOverWire(t *testing.T) {
	sink := &captureSink{}
	RegisterTraceSink(sink.sink)
	t.Cleanup(func() { RegisterTraceSink(nil) })

	wire := &fakeWire{}
	exp := &routerTraceExporter{router: traceRouter, dest: traceDest, wire: wire}
	if err := exp.ExportSpans(context.Background(), makeSpans(t)); err != nil {
		t.Fatalf("ExportSpans: %v", err)
	}
	if wire.calls() != 0 {
		t.Errorf("wire exporter was used %d times despite a co-resident sink (Cost 0 must win)", wire.calls())
	}
	if len(sink.got()) != 1 {
		t.Errorf("co-resident sink received %d spans, want 1", len(sink.got()))
	}
}

// TestTraceExport_NoRouteSurfaces: with neither a sink nor a wire, an export
// returns ErrNoRoute. Visible, never a silent drop.
func TestTraceExport_NoRouteSurfaces(t *testing.T) {
	RegisterTraceSink(nil)
	exp := &routerTraceExporter{router: traceRouter, dest: traceDest}
	if err := exp.ExportSpans(context.Background(), makeSpans(t)); !errors.Is(err, zap.ErrNoRoute) {
		t.Fatalf("ExportSpans err = %v, want ErrNoRoute", err)
	}
}

// TestInstallTelemetry_RetiresOTLPExporterEnv is the composition-root ownership
// invariant: once the provider is installed the OTLP-exporter env is cleared, so
// no embedded subsystem auto-configures a second, competing OTLP provider and
// forks the trace path. The ZAP endpoint stays intact.
func TestInstallTelemetry_RetiresOTLPExporterEnv(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_ZAP_ENDPOINT", "127.0.0.1:1") // never dialed at install
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://otel-collector.hanzo.svc:4318")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://otel-collector.hanzo.svc:4318")

	shutdown := InstallTelemetry(context.Background(), testLogger(), "hanzo-cloud")
	t.Cleanup(func() { shutdown(context.Background()) })

	if v := environ.Or("OTEL_EXPORTER_OTLP_ENDPOINT", ""); v != "" {
		t.Errorf("OTEL_EXPORTER_OTLP_ENDPOINT = %q, want cleared", v)
	}
	if v := environ.Or("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", ""); v != "" {
		t.Errorf("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT = %q, want cleared", v)
	}
	if v := environ.Or("OTEL_EXPORTER_ZAP_ENDPOINT", ""); v == "" {
		t.Errorf("OTEL_EXPORTER_ZAP_ENDPOINT was cleared, want intact")
	}
}

// TestInstallTelemetry_DisabledIsNoop confirms the clean no-op posture: with no
// sink expected and no endpoint set, InstallTelemetry installs nothing, returns a
// non-nil shutdown, and leaves TracerProviderInstalled false so apps/ does not
// adopt a no-op provider into ai.
func TestInstallTelemetry_DisabledIsNoop(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_ZAP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("O11Y_TRACES_ZAP_INPROCESS", "")

	shutdown := InstallTelemetry(context.Background(), testLogger(), "hanzo-cloud")
	if shutdown == nil {
		t.Fatal("InstallTelemetry returned a nil shutdown func")
	}
	if TracerProviderInstalled() {
		t.Error("InstallTelemetry latched TracerProviderInstalled while disabled")
	}
	shutdown(context.Background()) // must not panic
}

// TestTraceInprocEnabled_Gate: the ONE gate both the host producer and the
// clients/o11y sink read, so the two can never disagree about whether a
// co-resident sink is expected.
func TestTraceInprocEnabled_Gate(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "yes", "on", " true "} {
		t.Setenv("O11Y_TRACES_ZAP_INPROCESS", v)
		if !TraceInprocEnabled() {
			t.Fatalf("TraceInprocEnabled()=false for %q, want true", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "off", "maybe"} {
		t.Setenv("O11Y_TRACES_ZAP_INPROCESS", v)
		if TraceInprocEnabled() {
			t.Fatalf("TraceInprocEnabled()=true for %q, want false", v)
		}
	}
}

// fakeWire is a stand-in SpanExporter for the wire fallback, so routing is
// exercised without a socket.
type fakeWire struct {
	mu sync.Mutex
	n  int
}

func (f *fakeWire) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	return nil
}
func (f *fakeWire) Shutdown(context.Context) error { return nil }
func (f *fakeWire) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

// makeSpans produces one finished ReadOnlySpan, with no dependency on the
// exporter under test.
func makeSpans(t *testing.T) []sdktrace.ReadOnlySpan {
	t.Helper()
	rec := &recorder{}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSyncer(rec),
	)
	_, span := tp.Tracer("t").Start(context.Background(), "unit")
	span.End()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tp.Shutdown(ctx)
	if len(rec.spans) == 0 {
		t.Fatal("no span recorded")
	}
	return rec.spans
}

type recorder struct{ spans []sdktrace.ReadOnlySpan }

func (r *recorder) ExportSpans(_ context.Context, s []sdktrace.ReadOnlySpan) error {
	r.spans = append(r.spans, s...)
	return nil
}
func (r *recorder) Shutdown(context.Context) error { return nil }

// TestWireEndpointFor pins the span-destination table, including the leg whose
// absence dropped every span the sibling plugin processes produced: cloud runs its
// subsystems as separate processes in one pod, the plane sink registers in exactly
// one of them, and the rest had no route AND no endpoint.
func TestWireEndpointFor(t *testing.T) {
	for _, tc := range []struct {
		name   string
		zapEP  string
		legacy string
		inproc bool
		want   string
	}{
		{"explicit ZAP endpoint wins", "o11y.hanzo.svc:4317", "http://otel:4318", true, "o11y.hanzo.svc:4317"},
		{"legacy OTLP means ship remotely", "", "http://otel:4318", true, defaultZapEndpoint},
		{"sink in a sibling process -> pod loopback", "", "", true, localPlaneEndpoint},
		{"no destination at all -> in-process only", "", "", false, ""},
	} {
		if got := wireEndpointFor(tc.zapEP, tc.legacy, tc.inproc); got != tc.want {
			t.Errorf("%s: wireEndpointFor(%q,%q,%v) = %q, want %q",
				tc.name, tc.zapEP, tc.legacy, tc.inproc, got, tc.want)
		}
	}
}
