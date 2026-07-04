// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cloud

import (
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// newRecordingTracer wires httpTracer to an in-memory SpanRecorder so a test can
// assert what the middleware emitted WITHOUT a live collector. It rebinds the
// package-level httpTracer (restored via t.Cleanup) rather than touching the OTel
// global — the global's first-writer-wins delegation makes it unusable across
// tests, and the middleware reads httpTracer directly.
func newRecordingTracer(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	prev := httpTracer
	httpTracer = tp.Tracer(TracerName)
	t.Cleanup(func() { httpTracer = prev; _ = tp.Shutdown(t.Context()) })
	return sr
}

func attrOf(s sdktrace.ReadOnlySpan, key string) (attribute.Value, bool) {
	for _, kv := range s.Attributes() {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

func findSpan(spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	for _, s := range spans {
		if s.Name() == name {
			return s
		}
	}
	return nil
}

// TestTracingMiddleware_EmitsServerSpan drives a real request through a zip app
// with the middleware and asserts a SERVER span with the HTTP semantic-convention
// attributes and the mapped status.
func TestTracingMiddleware_EmitsServerSpan(t *testing.T) {
	sr := newRecordingTracer(t)

	app := zip.New(zip.Config{})
	app.Use(TracingMiddleware())
	app.Get("/v1/models", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{"ok": "yes"})
	})

	req := httptest.NewRequest("GET", "/v1/models", nil)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	s := spans[0]
	if s.Name() != "GET /v1/models" {
		t.Errorf("span name = %q, want %q", s.Name(), "GET /v1/models")
	}
	if s.SpanKind() != trace.SpanKindServer {
		t.Errorf("span kind = %v, want Server", s.SpanKind())
	}
	if v, ok := attrOf(s, "http.request.method"); !ok || v.AsString() != "GET" {
		t.Errorf("http.request.method = %v (ok=%v), want GET", v.AsString(), ok)
	}
	if v, ok := attrOf(s, "http.route"); !ok || v.AsString() != "/v1/models" {
		t.Errorf("http.route = %v (ok=%v), want /v1/models", v.AsString(), ok)
	}
	if v, ok := attrOf(s, "http.response.status_code"); !ok || v.AsInt64() != 200 {
		t.Errorf("http.response.status_code = %d (ok=%v), want 200", v.AsInt64(), ok)
	}
	if s.Status().Code != codes.Ok {
		t.Errorf("status code = %v, want Ok", s.Status().Code)
	}
}

// TestTracingMiddleware_PropagatesContext proves the span written onto the
// request context parents a downstream (handler-created) span — the invariant
// that makes an agent run one trace tree (request → run → step → chat).
func TestTracingMiddleware_PropagatesContext(t *testing.T) {
	sr := newRecordingTracer(t)

	app := zip.New(zip.Config{})
	app.Use(TracingMiddleware())
	app.Post("/v1/agents/run", func(c *zip.Ctx) error {
		// Downstream span opened off c.Context() — exactly how clients/agents and
		// clients/aihttp open their spans.
		_, child := httpTracer.Start(c.Context(), "agent.run")
		child.End()
		return c.JSON(200, map[string]string{"ok": "yes"})
	})

	req := httptest.NewRequest("POST", "/v1/agents/run", nil)
	if _, err := app.Fiber().Test(req); err != nil {
		t.Fatalf("app.Test: %v", err)
	}

	spans := sr.Ended()
	if len(spans) != 2 {
		t.Fatalf("recorded %d spans, want 2 (server + child)", len(spans))
	}
	server := findSpan(spans, "POST /v1/agents/run")
	child := findSpan(spans, "agent.run")
	if server == nil || child == nil {
		t.Fatalf("missing spans: server=%v child=%v", server != nil, child != nil)
	}
	if child.Parent().SpanID() != server.SpanContext().SpanID() {
		t.Errorf("child not parented under server span: child.parent=%v server=%v",
			child.Parent().SpanID(), server.SpanContext().SpanID())
	}
	if child.SpanContext().TraceID() != server.SpanContext().TraceID() {
		t.Errorf("child/server trace IDs differ: %v vs %v",
			child.SpanContext().TraceID(), server.SpanContext().TraceID())
	}
}

// TestTracingMiddleware_ErrorStatus maps a 5xx to an error span status.
func TestTracingMiddleware_ErrorStatus(t *testing.T) {
	sr := newRecordingTracer(t)

	app := zip.New(zip.Config{})
	app.Use(TracingMiddleware())
	app.Get("/v1/boom", func(c *zip.Ctx) error {
		return zip.Errorf(500, "boom")
	})

	req := httptest.NewRequest("GET", "/v1/boom", nil)
	if _, err := app.Fiber().Test(req); err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	if spans[0].Status().Code != codes.Error {
		t.Errorf("status = %v, want Error", spans[0].Status().Code)
	}
}

// TestTracingMiddleware_SkipsNoise asserts health/metrics + non-/v1 paths open no
// span, so probes and static paths never flood the trace store.
func TestTracingMiddleware_SkipsNoise(t *testing.T) {
	sr := newRecordingTracer(t)

	app := zip.New(zip.Config{})
	app.Use(TracingMiddleware())
	app.Get("/v1/agents/health", func(c *zip.Ctx) error { return c.JSON(200, "ok") })
	app.Get("/healthz", func(c *zip.Ctx) error { return c.JSON(200, "ok") })
	app.Get("/", func(c *zip.Ctx) error { return c.JSON(200, "ok") })

	for _, p := range []string{"/v1/agents/health", "/healthz", "/"} {
		if _, err := app.Fiber().Test(httptest.NewRequest("GET", p, nil)); err != nil {
			t.Fatalf("app.Test %s: %v", p, err)
		}
	}
	if n := len(sr.Ended()); n != 0 {
		t.Fatalf("recorded %d spans, want 0 (all skipped)", n)
	}
}

func TestTraceable(t *testing.T) {
	cases := map[string]bool{
		"/v1/chat/completions": true,
		"/v1/models":           true,
		"/v1/agents/x/run":     true,
		"/v1/agents/health":    false,
		"/v1/kms/health":       false,
		"/healthz":             false,
		"/metrics":             false,
		"/":                    false,
		"/zap":                 false,
		"/assets/app.js":       false,
	}
	for path, want := range cases {
		if got := traceable(path); got != want {
			t.Errorf("traceable(%q) = %v, want %v", path, got, want)
		}
	}
}
